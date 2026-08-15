package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/forge"
	"github.com/deadtrickster/flowy/internal/store"
)

// defaultAddr is where the node listens when FLOWY_ADDR is unset.
const defaultAddr = "127.0.0.1:8787"

// server holds what the HTTP handlers need.
type server struct {
	db   *store.DB
	node string
	// operator is the user id that runs this node. It is the only principal
	// ?scope=all obeys. It is local configuration on purpose: operator-ness is
	// a fact about this machine, not a row that could ever replicate to another
	// node and grant somebody a view of everything there.
	operator string
	// peers are the user ids that may POST /api/sync/push at this node. Like
	// operator, it is local configuration and never a row: a push is a write of
	// somebody else's rows into this database, and being allowed to do that is
	// a decision the person running the node makes about a token, not something
	// a token can carry with it. Empty means only the operator may push.
	peers map[string]bool
	// forgeRepos are the repositories this node will file into and comment on.
	// Filing is the one operation that leaves this machine, using a credential
	// nobody who calls the API holds, so where it may go is the operator's
	// choice rather than a field in a request body.
	forgeRepos map[string]bool
	started    time.Time
	// console is the embedded single-page app, opened once by routes().
	console *console
	// forge is the issue tracker this node speaks to, chosen once at startup by
	// FLOWY_FORGE or by which CLI is installed. It is nil when there is
	// neither, and forgeWhy says which of those happened.
	forge    forge.ForgeClient
	forgeWhy string
	// mockForge is the same client when it is the in-process fake, and nil
	// otherwise. It is what gates the mock's control routes: they exist exactly
	// when the fake does.
	mockForge *forge.MockForge
}

// serve runs the node's HTTP server until it is interrupted.
func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", envOr("FLOWY_ADDR", defaultAddr), "listen address")
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name of this node, stamped onto every row")
	operator := fs.String("operator", os.Getenv("FLOWY_OPERATOR"),
		"user id that may use ?scope=all on this node (default $FLOWY_OPERATOR)")
	forgeKind := fs.String("forge", os.Getenv("FLOWY_FORGE"),
		"issue tracker: gh|glab|mock, or empty to use whichever CLI is installed "+
			"(default $FLOWY_FORGE)")
	peers := fs.String("peers", os.Getenv("FLOWY_PEERS"),
		"comma-separated user ids whose token may push replication deltas at this node "+
			"(default $FLOWY_PEERS)")
	forgeRepos := fs.String("forge-repos", os.Getenv("FLOWY_FORGE_REPOS"),
		"comma-separated repositories this node may file into, e.g. owner/name "+
			"(default $FLOWY_FORGE_REPOS)")
	peerKeys := fs.String("peer-keys", os.Getenv("FLOWY_PEER_KEYS"),
		"comma-separated node=publickey pairs to pin, the key in hex or base64 "+
			"(default $FLOWY_PEER_KEYS)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("no DSN: set DATABASE_URL or pass -dsn")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	db, err := store.Open(dialCtx, *dsn, *node)
	if err != nil {
		return err
	}
	defer db.Close()

	// The clock lives in memory and the rows do not: a node that applied a
	// peer's rows and was then restarted has to come up above the highest
	// reading it is already holding, or its next write would lose a merge it
	// should have won.
	if highest, err := db.SeedClock(dialCtx); err != nil {
		log.Printf("clock: could not read the highest reading in the store: %v", err)
	} else if highest > 0 {
		log.Printf("clock: seeded above the store's highest reading (%d)", highest)
	}

	// And the key does not live in memory either. A node signs every row it
	// writes, so it needs one before it takes a request - and failing here is
	// better than failing on the first write, where the caller is told their
	// artifact could not be stored and nobody says why.
	id, err := db.Identity(dialCtx)
	if err != nil {
		return err
	}
	log.Printf("identity: node %q signs with %s", id.NodeID, store.EncodeKey(id.PublicKey))
	if n, err := db.PinFromEnv(dialCtx, *peerKeys); err != nil {
		return fmt.Errorf("peer keys: %w", err)
	} else if n > 0 {
		log.Printf("identity: %d peer key(s) pinned by the operator", n)
	}
	if db.RequirePinnedPeers() {
		log.Print("identity: rows are taken only from nodes this operator pinned " +
			"(FLOWY_REQUIRE_PINNED_PEERS)")
	}

	srv := &server{
		db: db, node: *node, operator: *operator,
		peers:      commaSet(*peers),
		forgeRepos: commaSet(*forgeRepos),
		started:    time.Now(),
	}
	if len(srv.peers) > 0 {
		log.Printf("peers: %d token holder(s) may push replication deltas here", len(srv.peers))
	} else {
		log.Print("peers: none configured; only the operator may push (set FLOWY_PEERS)")
	}
	if len(srv.forgeRepos) == 0 {
		log.Print("forge: no repositories allowed; filing will be refused (set FLOWY_FORGE_REPOS)")
	}

	// Which forge this node speaks to is decided once, here, and only by
	// looking: nothing is executed, so a node that comes up with `gh` installed
	// and no credential has still not touched GitHub. A node with no forge
	// starts anyway and answers 503 on /api/forge/* - not being able to file a
	// bug is not a reason to refuse to hold one.
	client, why, err := forge.Select(*forgeKind)
	switch {
	case errors.Is(err, forge.ErrNoForge):
		log.Printf("forge: none (%s); /api/forge/* will answer 503", why)
	case err != nil:
		return err
	default:
		srv.forge = client
		if mock, ok := client.(*forge.MockForge); ok {
			srv.mockForge = mock
		}
		log.Printf("forge: %s (%s)", client.Kind(), why)
	}
	srv.forgeWhy = why

	// Listen before announcing, so a port clash is an error rather than a
	// health check that never comes up.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *addr, err)
	}

	httpSrv := &http.Server{
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.New(os.Stderr, "http: ", log.LstdFlags),
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	log.Printf("flowy %s serving on %s as node %q", version, ln.Addr(), *node)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Print("shutting down")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return <-errCh
}

// apiRoutes are the endpoints that speak for a principal. Every one of them is
// behind authenticate, and every read they do goes through the store's
// permission filter - there is no path to an artifact that skips either.
var apiRoutes = []string{
	"POST /api/artifacts",
	"GET /api/artifacts",
	"GET /api/artifact/{id}",
	"POST /api/artifact/{id}/delete",
	"POST /api/artifact/{id}/status",
	"GET /api/artifact/{id}/history",
	"GET /api/search",
	"POST /api/events",
	"GET /api/events",
	"POST /api/chat/{room}/say",
	"GET /api/chat/{room}",
	"GET /api/chat/{room}/wait",
	"GET /api/inbox",
	"GET /api/inbox/tasks",
	"POST /api/assign",
	"GET /api/task/{id}",
	"POST /api/task/{id}/delegate",
	"POST /api/task/{id}/state",
	"PUT /api/me/auto_delegate",
	"POST /api/grants",
	"GET /api/forge",
	"POST /api/forge/file",
	"GET /api/forge/status",
	"POST /api/forge/sync",
	"GET /api/sync/pull",
	"POST /api/sync/push",
	"GET /api/peers",
	"GET /api/whoami",
	"GET /api/node",
}

// routes wires the node's surface: an open operational corner, and everything
// under /api/ behind a bearer token.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Operational, and deliberately unauthenticated: a health check that needs
	// a credential is a health check that stops working at the worst moment.
	// None of these read a row of fabric data.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	// No method on the catch-all: "GET /" would be more permissive on paths and
	// less permissive on methods than "/api/", which the mux refuses to rank.
	// Everything that is not /api/ and not operational is the console, which
	// answers its own 404s by rendering a route that does not exist.
	mux.Handle("/", s.consoleHandler())

	api := http.NewServeMux()
	api.HandleFunc("POST /api/artifacts", s.handleCreateArtifact)
	api.HandleFunc("GET /api/artifacts", s.handleListArtifacts)
	api.HandleFunc("GET /api/artifact/{id}", s.handleGetArtifact)
	api.HandleFunc("POST /api/artifact/{id}/delete", s.handleDeleteArtifact)
	api.HandleFunc("POST /api/artifact/{id}/status", s.handleArtifactStatus)
	api.HandleFunc("GET /api/artifact/{id}/history", s.handleArtifactHistory)
	api.HandleFunc("GET /api/search", s.handleSearch)
	api.HandleFunc("POST /api/events", s.handleAppendEvent)
	api.HandleFunc("GET /api/events", s.handleListEvents)
	api.HandleFunc("POST /api/chat/{room}/say", s.handleChatSay)
	api.HandleFunc("GET /api/chat/{room}", s.handleChatRead)
	api.HandleFunc("GET /api/chat/{room}/wait", s.handleChatWait)
	api.HandleFunc("GET /api/inbox", s.handleInbox)
	// Assignment and the handoff it opens. /api/inbox/tasks is a longer pattern
	// than /api/inbox, so the mux ranks it first and the two do not collide.
	api.HandleFunc("GET /api/inbox/tasks", s.handleInboxTasks)
	api.HandleFunc("POST /api/assign", s.handleAssign)
	api.HandleFunc("GET /api/task/{id}", s.handleGetTask)
	api.HandleFunc("POST /api/task/{id}/delegate", s.handleDelegateTask)
	api.HandleFunc("POST /api/task/{id}/state", s.handleTaskState)
	api.HandleFunc("PUT /api/me/auto_delegate", s.handleAutoDelegate)
	api.HandleFunc("POST /api/grants", s.handleCreateGrant)
	// The forge bridge. Every one of them is gated on reading the artifact, so
	// the permission story is the one the rest of the API already has.
	api.HandleFunc("GET /api/forge", s.handleForgeCapability)
	api.HandleFunc("POST /api/forge/file", s.handleForgeFile)
	api.HandleFunc("GET /api/forge/status", s.handleForgeStatus)
	api.HandleFunc("POST /api/forge/sync", s.handleForgeSync)
	// The mock's control surface, and only when the mock is what this node
	// selected: on a node talking to GitHub these routes do not exist.
	//
	// Every one of them is the operator's, and the gate goes on here rather
	// than inside the handlers so that it is one decision instead of six: a
	// route added to this block cannot forget it. They are the other side of
	// the conversation - who the forge says it is, what a reviewer said, when
	// the forge refuses to answer - and any token at all could drive them,
	// which made an artifact's whole forge story something a stranger writes.
	if s.mockForge != nil {
		mock := func(pattern string, h http.HandlerFunc) {
			api.HandleFunc(pattern, s.operatorOnly(h))
		}
		mock("POST /api/forge/mock/state", s.handleMockState)
		mock("POST /api/forge/mock/comment", s.handleMockComment)
		mock("GET /api/forge/mock/issue", s.handleMockIssue)
		mock("POST /api/forge/mock/fail", s.handleMockFail)
		mock("POST /api/forge/mock/login", s.handleMockLogin)
		mock("POST /api/forge/mock/on-file", s.handleMockOnFile)
	}
	// Replication. A peer is a client like any other: it holds a token, it
	// resolves to a principal, and it pulls what that principal may read.
	api.HandleFunc("GET /api/sync/pull", s.handleSyncPull)
	api.HandleFunc("POST /api/sync/push", s.handleSyncPush)
	api.HandleFunc("GET /api/peers", s.handleListPeers)
	api.HandleFunc("GET /api/whoami", s.handleWhoami)
	api.HandleFunc("GET /api/node", s.handleNode)
	// A path under /api/ that nothing claims is a 404 in JSON. Without this it
	// would be net/http's plain-text 404, which a client parsing JSON reads as
	// a broken server rather than as a typo in a URL.
	api.HandleFunc("/api/", s.handleAPINotFound)

	// One mount, so nothing under /api/ can be added later without the token
	// check - including a method or a path this mux does not know, which has to
	// answer 401 before it answers 404.
	mux.Handle("/api/", s.authenticate(api))

	return logRequests(mux)
}

type healthzResponse struct {
	OK       bool             `json:"ok"`
	Node     string           `json:"node"`
	Version  string           `json:"version"`
	DB       string           `json:"db"`
	HLC      int64            `json:"hlc"`
	UptimeMS int64            `json:"uptime_ms"`
	Counts   map[string]int64 `json:"counts,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// handleHealthz reports whether the node can reach its store. ?counts=1 adds
// the row count of each spine table, for this node's operator and nobody else.
//
// The health check itself stays open, because one that needs a credential is
// one that stops working at the worst moment, and "ok, db up, this version"
// tells a load balancer what it needs and a stranger nothing it did not
// already know from the port answering. The counts are a different thing: how
// many users, how many tokens, how many grants, how many artifacts is the shape
// and the size of what this node holds, and it was answered to anybody who
// asked. It is the operator's view of their own machine, like ?scope=all and
// like /api/peers, so it is behind the operator's token now.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	resp := healthzResponse{
		OK:      true,
		Node:    s.node,
		Version: version,
		DB:      "up",
		// Reading, not Pack: reporting the clock is not a use of it. Pack goes
		// through Now, which advances the counter, so every probe of an open
		// endpoint spent a reading nothing was ever written under.
		HLC:      s.db.Clock().Reading().Pack(),
		UptimeMS: time.Since(s.started).Milliseconds(),
	}

	if err := s.db.Ping(ctx); err != nil {
		resp.OK, resp.DB, resp.Error = false, "down", err.Error()
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}
	if r.URL.Query().Get("counts") != "" && s.isOperator(ctx, r) {
		counts, err := s.db.Counts(ctx)
		if err != nil {
			resp.OK, resp.DB, resp.Error = false, "degraded", err.Error()
			writeJSON(w, http.StatusServiceUnavailable, resp)
			return
		}
		resp.Counts = counts
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version, "node": s.node})
}

// handleNode describes the node and its surface. It used to answer at /, which
// is the console's now: a machine-readable index of the API belongs under the
// API, behind the same token as the rest of it.
//
// GET /api/node
func (s *server) handleNode(w http.ResponseWriter, _ *http.Request) {
	forgeKind := ""
	if s.forge != nil {
		forgeKind = s.forge.Kind()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node":    s.node,
		"version": version,
		"phase":   6.5,
		"console": s.console != nil && s.console.index != nil,
		"forge":   forgeKind,
		"routes":  append([]string{"GET /healthz", "GET /version"}, apiRoutes...),
	})
}

// handleAPINotFound answers the API's own 404s, in JSON.
func (s *server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, errorBody("no such endpoint: "+r.Method+" "+r.URL.Path))
}

// consoleHandler opens the embedded console once, at wiring time, and logs
// whether there is one - a node serving the API with no app in front of it is a
// perfectly good node, but it should say so at startup rather than at the first
// browser.
func (s *server) consoleHandler() http.Handler {
	c, built := newConsole()
	s.console = c
	if built {
		log.Printf("console: serving the embedded build, SPA fallback on non-/api paths")
	} else {
		log.Printf("console: not built into this binary (cd web && npm ci && npm run build)")
	}
	return c
}

// internalError is what a 500 says, and all it says.
//
// The store wraps its failures - `store: read artifact: pq: relation "..." does
// not exist`, constraint names, column names, a fragment of the statement - and
// every one of those went out in the body verbatim. Any principal holding any
// token could read the schema off a failing request, and a federated peer with
// the most minimal credential here could do it too. The operator needs the
// whole chain and gets it in the log; the caller needs to know the request
// failed here rather than because of anything they sent, and that is the whole
// of what this says.
const internalError = "internal error"

// serverError logs err and answers the opaque 500.
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	serverErrorSaying(w, r, err, internalError)
}

// serverErrorSaying is serverError for a failure the caller has to be told
// something specific about - the one case being an issue that was filed on a
// tracker before the write here failed, where the number is the only way anyone
// finds it again. msg is written by this node, never by the error.
func serverErrorSaying(w http.ResponseWriter, r *http.Request, err error, msg string) {
	ref := errorRef()
	log.Printf("500 %s %s ref=%s: %v", r.Method, r.URL.Path, ref, err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": msg, "ref": ref})
}

// errorRef mints the short reference that ties a 500 body to its log line. It
// is grep bait and nothing else - it names no row and encodes no time - so four
// bytes of randomness is enough, and a source that fails is not worth failing a
// request over.
func errorRef() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unlogged"
	}
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}

// logRequests logs one line per request with its status and duration.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Microsecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// commaSet reads a comma-separated configuration value into a set, dropping
// blanks so that "a,,b " and "a,b" are the same list.
func commaSet(raw string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out[item] = true
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultNode names this node after the host when FLOWY_NODE is unset.
func defaultNode() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "flowy-node"
}
