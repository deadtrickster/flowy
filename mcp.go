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

	"github.com/deadtrickster/flowy/internal/store"
)

// protocolVersion is the MCP revision this server implements.
const protocolVersion = "2024-11-05"

// instructionsURI names the server instructions as a resource, so a client that
// ignores initialize.instructions can still fetch the same text.
const instructionsURI = "flowy://instructions"

// instructions is the document agents read before they touch shared memory. It
// is served twice - as initialize.instructions and as the flowy://instructions
// resource - and is the same bytes both times.
//
//go:embed instructions.md
var instructions string

// maxLine caps one JSON-RPC message on the stdio transport. Memory bodies are
// prose, but prose can be long, and bufio.Scanner's default 64KiB is not.
const maxLine = 8 << 20

// mcpServer is what the handlers need: the store, and the name this node stamps
// onto the rows it writes.
type mcpServer struct {
	db   *store.DB
	node string
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

	m := &mcpServer{db: db, node: *node}
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

// The codes: the four standard ones, plus -32001 for a request that named a
// method it may not use without a principal.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeUnauthorized   = -32001
)

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
		return result(req.ID, map[string]any{"contents": []any{map[string]any{
			"uri":      instructionsURI,
			"mimeType": "text/markdown",
			"text":     instructions,
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
// The two failures are kept apart on purpose. No principal is a protocol error:
// the request could not be attempted at all. A tool that ran and failed - an
// item that is not there, a scope that is not a scope - comes back as a result
// with isError set, which is what an MCP client shows the model.
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
	out, err := tool.call(ctx, m, p, call.Arguments)
	if err != nil {
		return result(req.ID, toolError(err))
	}
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
		"uri":         instructionsURI,
		"name":        "flowy shared memory instructions",
		"description": "How to use flowy shared memory: scopes, kinds, tags, when to store and when to recall.",
		"mimeType":    "text/markdown",
	}}
}

// --------------------------------------------------------------- transports

// serveStdio speaks newline-delimited JSON-RPC. One message per line in, one
// response per line out, nothing else on stdout ever.
func (m *mcpServer) serveStdio(ctx context.Context, in io.Reader, out io.Writer, token string) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLine)
	w := bufio.NewWriter(out)

	log.Printf("flowy %s: stdio transport, node %q", version, m.node)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		resp := m.dispatchRaw(ctx, token, []byte(line))
		if resp == nil {
			continue
		}
		if err := writeLine(w, resp); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcp stdio: read: %w", err)
	}
	return nil
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
