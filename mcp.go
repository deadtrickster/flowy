package main

// The MCP surface: one shared memory, reachable by every agent that speaks
// Model Context Protocol.
//
// There are two transports and one set of handlers. `flowy mcp` speaks
// newline-delimited JSON-RPC over stdin and stdout, which is what a locally
// launched client (Claude Code, opencode, GLM) starts as a subprocess.
// `flowy mcp --http :PORT` speaks the same JSON-RPC over POST /mcp, which is
// what a remote client (a Claude web connector) can reach. Neither transport
// knows anything about memory: both hand a request to handle() and write back
// what it returns, so a tool cannot behave differently depending on how the
// agent got here.
//
// Identity is the Phase 1 principal and nothing else. The token arrives in the
// Authorization header over HTTP and in FLOWY_TOKEN over stdio; either way it
// is resolved by store.PrincipalForToken and every read it drives goes through
// the same permission filter the HTTP API uses.

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/store"
)

// protocolVersion is the MCP revision this server implements.
const protocolVersion = "2024-11-05"

// instructionsURI names the full guide as a resource, so a client that ignores
// initialize.instructions can still fetch it.
const instructionsURI = "flowy://instructions"

// instructions is what the node hands a client at initialize, and guide is the
// long form behind it. They are two documents rather than one because of how
// the two harnesses in this fleet treat them.
//
// Claude Code truncates server instructions at about 2 KB; opencode does not.
// A single 5.8 KB document therefore arrived whole on one side and cut off
// mid-sentence on the other, and neither said so - which is worse than either,
// because the two halves of a fleet were reading different protocols while
// appearing to read the same one. There is a second, sharper way to lose it:
// opencode drops a server's instructions ENTIRELY when every one of its tools
// is disabled by permission, so a restricted client gets no protocol at all and
// behaves as though there never was one.
//
// So the short text carries the mechanism - identity, the scope rule, the verbs,
// and the pointer - and stays under the limit; the guide carries the detail and
// is reachable two ways that do not depend on the client reading instructions at
// all: the `guide` tool and the flowy://instructions resource. The instructions
// are a pointer, never the only copy.
//
//go:embed instructions.md
var instructions string

//go:embed guide.md
var guide string

// maxLine caps one JSON-RPC message on the stdio transport. Memory bodies are
// prose, but prose can be long, and bufio.Scanner's default 64KiB is not.
const maxLine = 8 << 20

// mcpServer is what the handlers need: the store, and the name this node stamps
// onto the rows it writes.
type mcpServer struct {
	db   *store.DB
	node string
	// started, tracer and cpu are what the observability tools report about the
	// process the agent is actually talking to. An MCP server is a process of
	// its own - `flowy mcp` over a pipe is not `flowy serve` - so "how long has
	// this been up" has to be this one's answer rather than another's.
	started time.Time
	tracer  *otel.Tracer
	cpu     cpuMeter
	// operator is the user id this node's operator holds, read from the same
	// environment variable serve reads. It is what scope=all obeys here.
	operator string
}

// mcpCmd runs `flowy mcp`.
func mcpCmd(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	httpAddr := fs.String("http", "", "serve streamable HTTP on this address instead of stdio (e.g. :8788)")
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name of this node, stamped onto every row")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("no DSN: set DATABASE_URL or pass -dsn")
	}

	// Logging goes to stderr, always. On stdio, stdout is the protocol.
	log.SetOutput(os.Stderr)
	log.SetPrefix("mcp: ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	db, err := store.Open(dialCtx, *dsn, *node)
	if err != nil {
		return err
	}
	defer db.Close()

	// Same as serve: a write through a tool lands in a declared project or it
	// is refused, so a node reached only over MCP has to adopt what its
	// database already names before it takes a call.
	if _, err := db.BackfillProjects(dialCtx); err != nil {
		return fmt.Errorf("projects: %w", err)
	}

	m := &mcpServer{
		db: db, node: *node, started: startedNow(), operator: mcpOperator(),
		tracer: newTracer(*node, db),
	}
	defer m.tracer.Close()
	if *httpAddr != "" {
		return m.serveHTTP(ctx, *httpAddr)
	}
	return m.serveStdio(ctx, os.Stdin, os.Stdout, os.Getenv("FLOWY_TOKEN"))
}

// ------------------------------------------------------------- JSON-RPC 2.0

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// The codes: the four standard ones, -32001 for a request that named a method it
// may not use without a principal, and -32003 for one this principal is not
// allowed to make of this row. See forbidden below for why the second of those is
// an error rather than a result.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeUnauthorized   = -32001
	codeForbidden      = -32003
)

// forbidden is what a refusal that is about PERMISSION satisfies: this principal
// may not do this to this row, and no rewording of the arguments will change it.
//
// IT IS A PROTOCOL ERROR AND NOT A TOOL RESULT, and that distinction is the
// second half of this round. A tool refusal comes back as a JSON-RPC RESULT with
// isError set - which at the transport is a 200 with a normal-looking envelope -
// so a client or a harness that checks the call rather than the flag reads it as
// the call having happened. mem_write on somebody else's item answered exactly
// that way: nine calls in a row "succeeded" and wrote nothing, and the operator
// spent three hours looking for what was wrong with the ids because a refusal
// that reports success is indistinguishable from success. A permission refusal is
// now an error with a code - the nearest thing this protocol has to the 403 that
// POST /api/artifact/{id}/delete already answers with - so nothing can mistake it
// for a write that landed.
//
// It is deliberately NOT how "no such item" answers. An id out of reach is
// reported exactly as an id that does not exist, here as everywhere else, and
// that is a tool result: the model asked about something it cannot see, which is
// an ordinary answer to an ordinary question.
type forbidden interface {
	error
	forbidden()
}

// forbiddenError is the plain form, for the refusals that are a sentence and
// nothing more.
type forbiddenError struct{ reason string }

func (e forbiddenError) Error() string { return e.reason }
func (e forbiddenError) forbidden()    {}

// refuseForbidden builds one.
func refuseForbidden(format string, a ...any) error {
	return forbiddenError{reason: fmt.Sprintf(format, a...)}
}

// isNotification reports whether a request wants no answer. A JSON-RPC
// notification carries no id, and answering one is a protocol error.
func (r *rpcRequest) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

func result(id json.RawMessage, v any) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: v}
}

func rpcFail(id json.RawMessage, code int, msg string) *rpcResponse {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// ------------------------------------------------------------------ methods

// handle dispatches one request. token is whatever credential the transport
// carried; it is resolved here rather than at the transport, so both transports
// authenticate identically. A nil response means "notification, say nothing".
func (m *mcpServer) handle(ctx context.Context, token string, req *rpcRequest) *rpcResponse {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return rpcFail(req.ID, codeInvalidRequest, "jsonrpc must be \"2.0\"")
	}

	switch req.Method {
	case "initialize":
		// Deliberately open: a client has to be able to learn what this server
		// is, and read the instructions, before it has a token. Nothing here
		// reads a row.
		return result(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"listChanged": false, "subscribe": false},
			},
			"serverInfo":   map[string]any{"name": "flowy", "version": version},
			"instructions": instructions,
		})

	case "ping":
		return result(req.ID, map[string]any{})

	case "tools/list":
		return result(req.ID, map[string]any{"tools": toolSpecs()})

	case "resources/list":
		return result(req.ID, map[string]any{"resources": resourceSpecs()})

	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return rpcFail(req.ID, codeInvalidParams, err.Error())
		}
		if p.URI != instructionsURI {
			return rpcFail(req.ID, codeInvalidParams, "no such resource: "+p.URI)
		}
		// The resource answers with the guide, not the short text: a client that
		// comes here has already got the short one or ignored it, and either way
		// what it is missing is the detail.
		return result(req.ID, map[string]any{"contents": []any{map[string]any{
			"uri":      instructionsURI,
			"mimeType": "text/markdown",
			"text":     guide,
		}}})

	case "tools/call":
		return m.callTool(ctx, token, req)
	}

	if strings.HasPrefix(req.Method, "notifications/") {
		return nil // initialized, cancelled, progress: nothing to answer.
	}
	return rpcFail(req.ID, codeMethodNotFound, "unknown method: "+req.Method)
}

// callTool resolves the principal, finds the tool and runs it.
//
// The three failures are kept apart on purpose. No principal is a protocol error:
// the request could not be attempted at all. A row this principal may not write is
// a protocol error as well, for the reason written down at forbidden above: a
// refusal that arrives in a success envelope is a refusal nobody notices. A tool
// that ran and failed - an item that is not there, a scope that is not a scope -
// comes back as a result with isError set, which is what an MCP client shows the
// model.
func (m *mcpServer) callTool(ctx context.Context, token string, req *rpcRequest) *rpcResponse {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeParams(req.Params, &call); err != nil {
		return rpcFail(req.ID, codeInvalidParams, err.Error())
	}

	p, err := m.principal(ctx, token)
	if err != nil {
		return rpcFail(req.ID, codeUnauthorized, err.Error())
	}

	tool, ok := toolByName(call.Name)
	if !ok {
		return rpcFail(req.ID, codeInvalidParams, "unknown tool: "+call.Name)
	}

	// The call is a span, and the permission check above it is the reason the
	// rows it returns are the rows it returns: an agent's work is followable
	// here the same way a request over HTTP is.
	ctx, span := otel.Start(otel.WithTracer(ctx, m.tracer), otel.KindMCP, "mcp."+call.Name)
	actor, _ := chatActor(p)
	span.SetPrincipal(p.UserID, actor, p.Project)
	defer span.End()

	out, err := tool.call(ctx, m, p, call.Arguments)
	if err != nil {
		var denied forbidden
		if errors.As(err, &denied) {
			span.Fail("the tool refused: not this principal's row to write")
			return rpcFail(req.ID, codeForbidden, denied.Error())
		}
		span.Fail("the tool returned an error to the agent")
		return result(req.ID, toolError(err))
	}
	span.OK()
	return result(req.ID, toolResult(out))
}

// principal resolves a bearer token, or explains that there was not one. The
// message says how to supply it, because the two transports take it in
// different places and getting that wrong is the common mistake.
func (m *mcpServer) principal(ctx context.Context, token string) (*store.Principal, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("unauthenticated: send Authorization: Bearer <token>, " +
			"or set FLOWY_TOKEN when running over stdio")
	}
	p, err := m.db.PrincipalForToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errors.New("unauthenticated: unknown token")
	}
	if err != nil {
		return nil, err
	}
	if p.UserID == "" && p.AgentID == "" {
		return nil, errors.New("unauthenticated: token resolves to no principal")
	}
	// Decided here, from this machine's configuration, exactly as the HTTP
	// middleware decides it - never from the tokens row, which does not carry it
	// and which nothing replicates.
	p.Operator = m.operator != "" && p.UserID == m.operator
	return p, nil
}

// decodeParams reads a params object strictly. Absent params decode as the zero
// value, which is what a method with only optional parameters wants.
func decodeParams(raw json.RawMessage, into any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("bad params: %w", err)
	}
	return nil
}

// toolResult wraps a tool's return value the way MCP expects: text content,
// holding the JSON, so a client that renders content verbatim shows something
// a model can read.
func toolResult(v any) map[string]any {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolError(fmt.Errorf("marshal result: %w", err))
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(body)}},
		"isError": false,
	}
}

func toolError(err error) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": err.Error()}},
		"isError": true,
	}
}

// resourceSpecs is what resources/list answers. The instructions are the only
// resource this phase serves; artifacts get their own URIs when the FUSE mount
// lands and there is a path to name them by.
func resourceSpecs() []map[string]any {
	return []map[string]any{{
		"uri":  instructionsURI,
		"name": "flowy shared memory guide",
		"description": "The full guide to flowy shared memory: scopes, kinds, tags, " +
			"when to store and when to recall. The same text the guide tool returns.",
		"mimeType": "text/markdown",
	}}
}

// --------------------------------------------------------------- transports

// serveStdio speaks newline-delimited JSON-RPC. One message per line in, one
// response per line out, nothing else on stdout ever.
//
// The reading is on a goroutine of its own so that the loop can wait on the
// context as well as on the client. A blocking read of stdin is not
// interruptible: a plain `for scanner.Scan()` sits in the read until the other
// end closes the pipe, so SIGTERM was noted by the signal handler and then
// nothing happened - `flowy mcp` outlived the signal, and a client that kills
// its server and waits for it waited for its own timeout. The goroutine is left
// blocked in that read when the context goes; the process is on its way out and
// it holds nothing but the pipe.
func (m *mcpServer) serveStdio(ctx context.Context, in io.Reader, out io.Writer, token string) error {
	w := bufio.NewWriter(out)

	log.Printf("flowy %s: stdio transport, node %q", version, m.node)

	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64<<10), maxLine)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			readErr <- err
		}
	}()

	for {
		select {
		case <-ctx.Done():
			// Interrupted or terminated. Anything half-read on stdin is the
			// client's to resend to whatever it starts next.
			return nil
		case line, ok := <-lines:
			if !ok {
				// The client closed stdin. A read that failed rather than ended
				// is waiting on readErr, and is the reason this is over.
				select {
				case err := <-readErr:
					return fmt.Errorf("mcp stdio: read: %w", err)
				default:
					return nil
				}
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			resp := m.dispatchRaw(ctx, token, []byte(strings.TrimSpace(line)))
			if resp == nil {
				continue
			}
			if err := writeLine(w, resp); err != nil {
				return err
			}
		}
	}
}

// writeLine writes one JSON value and a newline, and flushes: a client is
// blocked on this response and a buffered answer is no answer.
func writeLine(w *bufio.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("mcp stdio: marshal: %w", err)
	}
	if _, err := w.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("mcp stdio: write: %w", err)
	}
	return w.Flush()
}

// dispatchRaw parses one message and answers it. It returns nil when the
// message was a notification, which has no reply.
func (m *mcpServer) dispatchRaw(ctx context.Context, token string, raw []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return rpcFail(nil, codeParse, "parse error: "+err.Error())
	}
	resp := m.handle(ctx, token, &req)
	if req.isNotification() {
		return nil
	}
	return resp
}

// serveHTTP is the streamable HTTP transport: JSON-RPC over POST /mcp, which is
// what a remote client - a Claude web connector - can reach. There is no SSE
// stream in this phase; every response is the body of the POST that asked for
// it, which is the half of the transport a request/response server needs.
func (m *mcpServer) serveHTTP(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", m.handleHTTP)
	mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, _ *http.Request) {
		// A client may open a GET for the server-to-client SSE stream. This
		// server never pushes, so say so rather than leaving a socket open.
		w.Header().Set("Allow", "POST")
		http.Error(w, "this endpoint answers POST; there is no server-initiated stream", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := m.db.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "db": "up", "node": m.node,
			"version": version, "protocol": protocolVersion,
		})
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.New(os.Stderr, "mcp http: ", log.LstdFlags),
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	log.Printf("flowy %s: mcp on http://%s/mcp, node %q", version, ln.Addr(), m.node)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return <-errCh
}

// handleHTTP answers one POST /mcp. A single request gets a single response; a
// batch gets an array of the responses that are not notifications; a body that
// is nothing but notifications gets 202 and no content, which is what the
// transport says.
func (m *mcpServer) handleHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLine))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcFail(nil, codeParse, "read body: "+err.Error()))
		return
	}
	token, _ := bearerToken(r)

	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var batch []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &batch); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcFail(nil, codeParse, "parse error: "+err.Error()))
			return
		}
		out := make([]*rpcResponse, 0, len(batch))
		for _, one := range batch {
			if resp := m.dispatchRaw(r.Context(), token, one); resp != nil {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	resp := m.dispatchRaw(r.Context(), token, []byte(trimmed))
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// The status is 200 even when the JSON-RPC envelope carries an error: the
	// HTTP request succeeded, and it is the body that says what happened.
	writeJSON(w, http.StatusOK, resp)
}
