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
	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/store"
)

// defaultAddr is where the node listens when FLOWY_ADDR is unset.
const defaultAddr = "127.0.0.1:8787"

// server holds what the HTTP handlers need.
type server struct {
	db   *store.DB
	node string
	// joins rate-limits the one unauthenticated door. It is per-process and
	// deliberately simple: the endpoint grants nothing, so the limit is about
	// keeping the board tidy rather than about security.
	joins *joinLimiter
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
	// reproBase is the repro runner this node forwards to, or empty when there
	// is none. Local configuration for the reason operator and forgeRepos are:
	// which machine holds Docker is a fact about this deployment, and a caller
	// that could name one could make this process reach any address it liked.
	//
	// It is here rather than in the browser because it used to be in the
	// browser - see repro_proxy.go, and the operator's "i dont want to type
	// address of the runner".
	reproBase string
	// apiPatterns is every route registered on the guarded mux, filled in by
	// routes(). It is here so a check can read the router instead of reading
	// this file - see recordingMux.
	apiPatterns []string
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
	// cpuMeter turns process CPU time into a share of one core over the window
	// since it was last read. It is on the server because the window is the gap
	// between two reads of the metrics endpoint.
	cpuMeter cpuMeter
	// tracer records what this node did: one span per request, children for the
	// permission check, the queries and the legs of replication inside it. It
	// records into the store and, when the operator configured one, exports to
	// an OTLP collector. See tracing.go.
	tracer *otel.Tracer
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
	principalKeys := fs.String("principal-keys", os.Getenv("FLOWY_PRINCIPAL_KEYS"),
		"comma-separated principal=publickey[@epoch] pairs to pin, so that rows naming "+
			"those principals as their author have to carry their own signature "+
			"(default $FLOWY_PRINCIPAL_KEYS)")
	repro := fs.String("repro", os.Getenv("FLOWY_REPRO"),
		"base url of the repro runner this node forwards /api/repro/* to, e.g. "+
			"http://127.0.0.1:8806 - empty means this deployment runs no repros "+
			"(default $FLOWY_REPRO)")
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

	// The registry describes the database it was added to. schema.sql declares
	// the names the rows already carry, because the foreign key on tokens
	// cannot be added while one points at a project with no row; this adopts
	// those rows under this node's key, so a name found here can replicate like
	// one that was declared here. It needs the key, so it runs after it.
	if n, err := db.BackfillProjects(dialCtx); err != nil {
		return fmt.Errorf("projects: %w", err)
	} else if n > 0 {
		log.Printf("projects: adopted %d project(s) the data already named", n)
	}
	if n, err := db.PinFromEnv(dialCtx, *peerKeys); err != nil {
		return fmt.Errorf("peer keys: %w", err)
	} else if n > 0 {
		log.Printf("identity: %d peer key(s) pinned by the operator", n)
	}
	// And the principal keys beside them, which are a different claim: a node
	// key says which machine relayed a row, a principal key says who wrote it.
	// See internal/store/principal.go.
	if n, err := db.PinPrincipalsFromEnv(dialCtx, *principalKeys); err != nil {
		return fmt.Errorf("principal keys: %w", err)
	} else if n > 0 {
		log.Printf("authorship: %d principal key(s) pinned by the operator", n)
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
		tracer:     newTracer(*node, db),
		joins:      newJoinLimiter(),
		reproBase:  strings.TrimRight(strings.TrimSpace(*repro), "/"),
	}
	defer srv.tracer.Close()
	// The bootstrap operator becomes a row before anything reads one - see
	// adoptoperator.go. It runs here rather than lazily because everything that
	// asks "who is the operator" of the STORE (a mention of @operator, the role
	// the roster draws) would otherwise answer "nobody" on a node that has one.
	adoptBootstrapOperator(ctx, db, srv.operator)
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
	// The event stream. It is on this list for the reason the queue doors are: a
	// client asks /api/node what this node can do, and a door nobody finds is a
	// door everybody works around with another poll.
	"GET /api/stream",
	"POST /api/chat/{room}/say",
	"GET /api/chat/{room}",
	"GET /api/chat/{room}/wait",
	"POST /api/chat/{room}/todo",
	"POST /api/chat/{room}/react",
	"POST /api/bookmark",
	"DELETE /api/bookmark/{id}",
	"GET /api/bookmarks",
	"POST /api/chat/{room}/pin",
	"DELETE /api/chat/{room}/pin/{id}",
	"GET /api/chat/{room}/pins",
	// The assignment doors. They are on this list because an operator went
	// looking for one and found nothing: /api/node is what a client asks when it
	// wants to know what this node can do, and a door that is not on the answer is
	// a door nobody finds.
	"POST /api/chat/{room}/todo/{id}/assignee",
	"POST /api/todo/{id}/assignee",
	"GET /api/todo/{id}/assignee",
	// The work queue, for the same reason: a door nobody finds is a door
	// nobody uses, and this one has to be cheaper to use than doing the thing.
	"POST /api/work/{id}/claim",
	"POST /api/work/{id}/release",
	"POST /api/work/{id}/done",
	// Correcting a todo nobody has started. It is on this list for the reason
	// the queue doors are: a client asks /api/node what this node can do, and an
	// editor who cannot find the door falls back to mem_write, which is the
	// unguarded write this one exists to replace.
	"POST /api/todo/{id}/edit",
	"GET /api/todo/{id}/edits",
	// And adding to a row without changing it. It is on this list for the same
	// reason and a sharper one: an agent that cannot find this door writes what
	// it learned in the room, where it scrolls away and the next agent
	// rediscovers it - which is the whole failure the door exists to end.
	"POST /api/todo/{id}/note",
	"GET /api/todo/{id}/notes",
	"GET /api/proposals",
	"GET /api/proposal/{id}",
	"POST /api/attachment",
	"GET /api/attachment/{id}",
	// A finding's run history. It is on this list for the reason the queue doors
	// are, with a measurement behind it: the repro path ran end to end on
	// 2026-08-18, recorded a correct verdict, and no browser could reach it. The
	// evidence doors two lines below this one in serve.go are still missing from
	// here, which is the same omission one axis over.
	"GET /api/finding/{id}/runs",
	// And the runner behind it. On this list because a console that cannot find
	// these doors falls back to asking the operator for an address, which is the
	// thing they asked to stop doing.
	"GET /api/vm/projects",
	"GET /api/vm/list",
	"POST /api/vm/spawn",
	"GET /api/vm/{name}/log",
	"POST /api/vm/{name}/say",
	"POST /api/vm/{name}/down",
	"POST /api/repro/run",
	"GET /api/repro/runs",
	"GET /api/repro/run/{id}/log",
	"GET /api/repro/version",
	"GET /api/repro/package",
	"GET /api/repro/healthz",
	"POST /api/dm/{to}",
	"GET /api/dm",
	"GET /api/dm/wait",
	"GET /api/inbox",
	"GET /api/inbox/tasks",
	"GET /api/inbox/wait",
	"POST /api/inbox/ack",
	"POST /api/inbox/reader",
	"GET /api/inbox/readers",
	"GET /api/inbox/unread",
	"DELETE /api/inbox/reader/{name}",
	"GET /api/presence",
	"POST /api/assign",
	"GET /api/task/{id}",
	"POST /api/task/{id}/delegate",
	"POST /api/task/{id}/state",
	"PUT /api/me/auto_delegate",
	"POST /api/grants",
	"GET /api/projects",
	"POST /api/projects",
	"POST /api/announcements",
	"GET /api/announcements",
	"GET /api/announcement/{id}/quiesce",
	"POST /api/announcement/{id}/ack",
	"POST /api/announcement/{id}/resolve",
	"POST /api/quiesce/hold",
	"POST /api/quiesce/release",
	"GET /api/forge",
	"POST /api/forge/file",
	"GET /api/forge/status",
	"POST /api/forge/sync",
	"GET /api/sync/pull",
	"POST /api/sync/push",
	"GET /api/peers",
	// Would this one row land right now, and if not why - the question four
	// seats were each answering in their own bash. It is on this list for the
	// reason the nag is: the door exists to REPLACE hand-written answers, and a
	// door nobody can find is a door everybody reimplements.
	//
	// The queue doors themselves are not on this list and should be. Not fixed
	// here: that is somebody's answer changing shape, and this change is a new
	// door rather than a correction to the old ones.
	"GET /api/merge/{id}/admissible",
	// The nag, whole, in one read. It is on this list because it exists to
	// REPLACE a script that four seats each carried a copy of - a door nobody
	// can find is a door everybody reimplements in jq, which is what it is
	// being built out of.
	"GET /api/nag",
	"GET /api/schedules",
	"PUT /api/schedules",
	"DELETE /api/schedules/{signal}",
	"GET /api/schedules/resolved",
	// And the same answer waited on, because a nag that a seat has to remember
	// to ask for is one an idle seat never asks for - see api_nagwait.go.
	"GET /api/nag/wait",
	"POST /api/projects/{project}/enter",
	"POST /api/projects/{project}/members",
	"GET /api/whoami",
	"GET /api/node",
	"GET /api/metrics",
	"GET /api/activity",
	"POST /api/activity",
	"GET /api/traces",
	"GET /api/trace/{id}",
	// RECOVERED ON 2026-08-19, in one block and dated, because the honest
	// thing to say about them is that they were missing rather than that they
	// were added. 32 doors were registered and not on this answer - the whole
	// merge chain, both lock doors, /api/me, rooms, todo deps and category,
	// finding evidence, the join approvals, worklog and role - while two new
	// ones were added the same week quoting the rule this list exists for.
	//
	// TestEveryRegisteredAPIRouteIsAdvertised is what keeps it true now. The
	// rule was already understood and written down; being understood is not a
	// mechanism.
	"DELETE /api/artifact/{id}/origins/{origin}",
	"DELETE /api/todo/{id}/deps/{blocker}",
	"GET /api/artifact/{id}/origins",
	"GET /api/finding/{id}/evidence",
	"GET /api/finding/{id}/upstream",
	"GET /api/instructions",
	"GET /api/lock",
	"GET /api/me",
	"GET /api/merge-queue",
	"GET /api/merge-queue/wait",
	"GET /api/repudiations",
	"GET /api/rooms",
	"GET /api/todo/{id}/category",
	"GET /api/todo/{id}/deps",
	"POST /api/artifact/{id}/origins",
	"POST /api/finding/{id}/evidence",
	"POST /api/finding/{id}/upstream",
	"POST /api/instructions",
	"POST /api/join/{id}/approve",
	"POST /api/join/{id}/refuse",
	"POST /api/lock",
	"POST /api/lock/release",
	"POST /api/merge/{id}/abandon",
	"POST /api/merge/{id}/blocked",
	"POST /api/merge/{id}/unblocked",
	"POST /api/merge/{id}/gate",
	"POST /api/merge/{id}/land",
	"POST /api/merge/{id}/renew",
	"POST /api/rooms",
	"POST /api/rooms/{room}/invite",
	"POST /api/rooms/{room}/leave",
	"POST /api/todo/{id}/category",
	"POST /api/todo/{id}/priority",
	"POST /api/todo/{id}/deps",
	"POST /api/agent/{id}/projects",
	"POST /api/user/{id}/role",
	"POST /api/worklog",
	"PUT /api/me",
}

// routes wires the node's surface: an open operational corner, and everything
// under /api/ behind a bearer token.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Operational, and deliberately unauthenticated: a health check that needs
	// a credential is a health check that stops working at the worst moment.
	// None of these read a row of fabric data.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	// The one door that takes no token, because the thing knocking does not
	// have one yet and cannot be given one without asking. It writes a request
	// row and grants nothing - see handleJoin.
	mux.HandleFunc("POST /api/join", s.handleJoin)
	// The two doors a PERSON uses, outside the token for the reason join is:
	// somebody logging in has no credential yet, and a 401 in front of the
	// login form can only be opened by whoever is already through it. Login
	// grants one session for a password that was already right; logout grants
	// nothing. Neither reads a row of fabric data. See api_login.go.
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /version", s.handleVersion)
	// The Prometheus text endpoint. It is at /metrics because that is where a
	// scraper looks, and it is behind the same token and the same scope filter
	// as GET /api/metrics: a scrape is a read, and an unauthenticated one would
	// hand the whole corpus's shape to anybody who asked. A scraper is
	// configured with a token like any other client.
	//
	// /metrics is also a page in the console, and the two share the path the way
	// they share the name. A request with a bearer token is a scrape and is
	// answered as one; a request without is a browser following a link, and gets
	// the app - which then reads GET /api/metrics with the token it holds, over
	// fetch, where an Authorization header is something a client can set and a
	// navigation is not.
	scrape := s.observed(s.authenticate(http.HandlerFunc(s.handlePrometheus)))
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerToken(r); !ok {
			s.console.ServeHTTP(w, r)
			return
		}
		scrape.ServeHTTP(w, r)
	})
	// No method on the catch-all: "GET /" would be more permissive on paths and
	// less permissive on methods than "/api/", which the mux refuses to rank.
	// Everything that is not /api/ and not operational is the console, which
	// answers its own 404s by rendering a route that does not exist.
	mux.Handle("/", s.consoleHandler())

	api := &recordingMux{ServeMux: http.NewServeMux()}
	api.HandleFunc("POST /api/artifacts", s.handleCreateArtifact)
	api.HandleFunc("GET /api/artifacts", s.handleListArtifacts)
	// Who may do what here, and only an operator may say. See internal/store/role.go.
	api.HandleFunc("POST /api/user/{id}/role", s.operatorOnly(s.handleSetRole))
	api.HandleFunc("POST /api/agent/{id}/projects", s.operatorOnly(s.handleGrantProject))
	api.HandleFunc("POST /api/rooms", s.handleCreateRoom)
	api.HandleFunc("GET /api/rooms", s.handleListRooms)
	api.HandleFunc("POST /api/rooms/{room}/invite", s.handleInviteRoom)
	api.HandleFunc("POST /api/rooms/{room}/leave", s.handleLeaveRoom)
	api.HandleFunc("GET /api/merge-queue", s.handleMergeQueue)
	api.HandleFunc("GET /api/merge-queue/wait", s.handleMergeQueueWait)
	api.HandleFunc("GET /api/merge/{id}/admissible", s.handleMergeAdmissible)
	api.HandleFunc("POST /api/merge/{id}/gate", s.handleMergeGate)
	// The lock on its own, because landing is not the only thing that needs the
	// shared checkout to itself - see api_lock.go. Same table and same holder
	// rules as the merge verbs take, deliberately: a second mechanism for one
	// exclusion is two locks that cannot see each other.
	api.HandleFunc("POST /api/lock", s.handleTakeLock)
	// Instructions as rows - the read is what an agent calls at session start,
	// and it composes nothing. See api_instructions.go.
	api.HandleFunc("POST /api/instructions", s.handleWriteInstruction)
	api.HandleFunc("GET /api/instructions", s.handleReadInstructions)
	// A person changes their own name and password without a shell - see
	// api_me.go. PUT because it is an update to one thing that already exists.
	api.HandleFunc("PUT /api/me", s.handleUpdateMe)
	api.HandleFunc("GET /api/me", s.handleReadMe)
	api.HandleFunc("POST /api/lock/release", s.handleReleaseLock)
	api.HandleFunc("GET /api/lock", s.handleReadLock)
	api.HandleFunc("GET /api/repudiations", s.handleRepudiations)
	api.HandleFunc("POST /api/merge/{id}/land", s.handleMergeLand)
	api.HandleFunc("POST /api/merge/{id}/abandon", s.handleMergeAbandon)
	api.HandleFunc("POST /api/merge/{id}/blocked", s.handleMergeBlocked)
	api.HandleFunc("POST /api/merge/{id}/unblocked", s.handleMergeUnblocked)
	api.HandleFunc("POST /api/merge/{id}/renew", s.handleMergeRenew)
	api.HandleFunc("GET /api/artifact/{id}", s.handleGetArtifact)
	api.HandleFunc("POST /api/artifact/{id}/delete", s.handleDeleteArtifact)
	api.HandleFunc("POST /api/artifact/{id}/status", s.handleArtifactStatus)
	api.HandleFunc("GET /api/artifact/{id}/history", s.handleArtifactHistory)

	// How strong a finding's evidence is, and the log behind it. Over HTTP as
	// well as MCP, for api_mergegate.go's reason: a door only agents can knock
	// on is half a door - see findingevidence.go.
	api.HandleFunc("POST /api/finding/{id}/evidence", s.handleFindingEvidence)
	api.HandleFunc("POST /api/finding/{id}/upstream", s.handleFindingUpstream)
	api.HandleFunc("GET /api/finding/{id}/upstream", s.handleFindingUpstreamLog)
	api.HandleFunc("GET /api/finding/{id}/evidence", s.handleFindingEvidenceLog)
	// And the run history behind it, for findingevidence.go's own reason: the
	// verdict a repro run records is the console's red/green line, and the
	// console speaks HTTP - see findingruns.go.
	api.HandleFunc("GET /api/finding/{id}/runs", s.handleFindingRuns)
	// The repro runner, forwarded. One origin and one token for the console, and
	// no address in anybody's browser - see repro_proxy.go.
	// The VMs, run here rather than behind a configured address - see
	// api_vm.go for why this is an exec and the repro path is a proxy.
	api.HandleFunc("GET /api/vm/projects", s.operatorOnly(s.handleVMProjects))
	api.HandleFunc("GET /api/vm/list", s.operatorOnly(s.handleVMList))
	api.HandleFunc("POST /api/vm/spawn", s.operatorOnly(s.handleVMSpawn))
	api.HandleFunc("GET /api/vm/{name}/log", s.operatorOnly(s.handleVMLog))
	api.HandleFunc("POST /api/vm/{name}/say", s.operatorOnly(s.handleVMSay))
	api.HandleFunc("POST /api/vm/{name}/down", s.operatorOnly(s.handleVMDown))
	api.HandleFunc("POST /api/repro/run", s.handleRepro)
	api.HandleFunc("GET /api/repro/runs", s.handleRepro)
	api.HandleFunc("GET /api/repro/run/{id}/log", s.handleRepro)
	api.HandleFunc("GET /api/repro/version", s.handleRepro)
	api.HandleFunc("GET /api/repro/package", s.handleRepro)
	api.HandleFunc("GET /api/repro/healthz", s.handleRepro)
	api.HandleFunc("GET /api/search", s.handleSearch)
	api.HandleFunc("POST /api/events", s.handleAppendEvent)
	api.HandleFunc("GET /api/events", s.handleListEvents)
	// One connection per tab, carrying envelopes for the topics it asks for.
	// See stream.go: it is the door that replaces a poll per panel.
	api.HandleFunc("GET /api/stream", s.handleStream)
	api.HandleFunc("POST /api/chat/{room}/say", s.handleChatSay)
	api.HandleFunc("GET /api/chat/{room}", s.handleChatRead)
	api.HandleFunc("GET /api/chat/{room}/wait", s.handleChatWait)
	// The room's plan, raised from the room: a todo and the message that raised
	// it, written where the conversation is.
	api.HandleFunc("POST /api/chat/{room}/todo", s.handleRoomTodoRaise)
	api.HandleFunc("POST /api/chat/{room}/react", s.handleRoomReact)
	api.HandleFunc("POST /api/chat/{room}/pin", s.handleRoomPin)
	api.HandleFunc("DELETE /api/chat/{room}/pin/{id}", s.handleRoomUnpin)
	api.HandleFunc("GET /api/chat/{room}/pins", s.handleRoomPins)
	// A reader's own list. No room in the path, deliberately: a bookmark is
	// about a message, and the reader who kept it may have kept messages from
	// four rooms. See bookmarks.go.
	api.HandleFunc("POST /api/bookmark", s.handleBookmark)
	api.HandleFunc("DELETE /api/bookmark/{id}", s.handleUnbookmark)
	api.HandleFunc("GET /api/bookmarks", s.handleBookmarks)
	// And who is carrying one, said in the room the same way raising it is.
	api.HandleFunc("POST /api/chat/{room}/todo/{id}/assignee", s.handleRoomTodoAssign)
	// What a room decided, as data: the proposal, the votes in the order they
	// were cast, and the tally. Read-only here on purpose - the writes are the
	// MCP verbs, and the console's view of this is deliberately not in this
	// change.
	api.HandleFunc("GET /api/proposals", s.handleListProposals)
	api.HandleFunc("GET /api/proposal/{id}", s.handleGetProposal)
	// DEPENDS-ON, and the queue that reads it. The edges are events on the
	// BLOCKED todo, so they reach exactly its readers - and the ready query is
	// computed per reader over that graph, never stored, because two principals
	// seeing different halves of a cross-project edge correctly disagree about
	// whether the same todo can be started. See internal/store/deps.go.
	api.HandleFunc("POST /api/todo/{id}/deps", s.handleAddDep)
	api.HandleFunc("DELETE /api/todo/{id}/deps/{blocker}", s.handleRemoveDep)
	api.HandleFunc("GET /api/todo/{id}/deps", s.handleGetDeps)
	// Where a row came from, which is not what blocks it - on any artifact
	// rather than on a todo, because either end may be anything readable. See
	// internal/store/origin.go.
	api.HandleFunc("POST /api/artifact/{id}/origins", s.handleAddOrigin)
	api.HandleFunc("DELETE /api/artifact/{id}/origins/{origin}", s.handleRemoveOrigin)
	api.HandleFunc("GET /api/artifact/{id}/origins", s.handleGetOrigins)
	api.HandleFunc("GET /api/ready", s.handleReady)
	// The whole nag in one read - see api_nag.go, and the operator: "move the
	// logic of the work nagger to the go side and the nagger then will be a
	// simple http call".
	api.HandleFunc("GET /api/nag", s.handleNag)
	// THE SCHEDULE THE NODE HOLDS (01M0EW45RE). Listing what is SET at a
	// scope and resolving what a reader RECEIVES are separate doors because
	// they are separate questions - see api_schedules.go. Delete is its own
	// verb: an off row and no row are different answers, and unchecking a
	// box cannot get back to inheriting.
	api.HandleFunc("GET /api/schedules", s.handleListSchedules)
	api.HandleFunc("PUT /api/schedules", s.handlePutSchedule)
	api.HandleFunc("DELETE /api/schedules/{signal}", s.handleDeleteSchedule)
	api.HandleFunc("GET /api/schedules/resolved", s.handleResolvedSchedules)
	api.HandleFunc("GET /api/nag/wait", s.handleNagWait)
	// Who is carrying a todo - any todo the caller can read, wherever it was
	// raised, and whoever wrote it. Read permission is the whole bar: the assignee
	// is a name in fields and grants nobody anything, so a queue one agent filed
	// is a queue the room can drain. The claim lands as an entry on the todo, so
	// it reaches exactly the todo's readers and says who made it. See
	// internal/store/assign.go, and assign.go for the room's door beside this one.
	api.HandleFunc("POST /api/todo/{id}/assignee", s.handleTodoAssign)
	// The work queue: take one, put it back, say it is done. See work.go.
	api.HandleFunc("POST /api/work/{id}/claim", s.handleWorkClaim)
	api.HandleFunc("POST /api/work/{id}/release", s.handleWorkRelease)
	api.HandleFunc("POST /api/work/{id}/done", s.handleWorkDone)
	api.HandleFunc("GET /api/todo/{id}/assignee", s.handleTodoAssignee)
	// The third piece of queue metadata, on the same terms as the other two:
	// WHAT KIND OF WORK this is, out of a closed set that REFUSES anything else -
	// which is what makes it countable and routable, and is the difference
	// between it and the free-form tags beside it. Whoever can read the todo may
	// set it, and the call lands as an entry naming who made it. See
	// internal/store/todocategory.go, and category.go for this door.
	api.HandleFunc("POST /api/todo/{id}/category", s.handleTodoCategory)
	api.HandleFunc("POST /api/todo/{id}/priority", s.handleTodoPriority)
	api.HandleFunc("GET /api/todo/{id}/category", s.handleTodoCategoryRead)
	// The words, which is the other half and the one with a race in it. An
	// author may correct a todo NOBODY HAS STARTED, and the edit carries the
	// status they saw: if somebody picked the row up while they were typing the
	// write is refused naming who took it, rather than landing on top of what
	// that agent is working from. See todoedit.go and
	// internal/store/todoedit.go.
	api.HandleFunc("POST /api/todo/{id}/edit", s.handleTodoEdit)
	api.HandleFunc("GET /api/todo/{id}/edits", s.handleTodoEdits)
	// And the other way a row changes: an APPEND. Anybody who can read the row
	// may attach what they learned about it - a measurement, the fix shape, what
	// it turned out to be blocked on - and nothing already written moves. It is
	// the opposite door to the one above it on purpose: the edit is the author's
	// words and is guarded on the row not having been started, a note is a second
	// person's words beside them and is most worth writing once it has. See
	// todonote.go and internal/store/todonote.go.
	api.HandleFunc("POST /api/todo/{id}/note", s.handleTodoNote)
	api.HandleFunc("GET /api/todo/{id}/notes", s.handleTodoNotes)
	// An attachment's bytes, permission-filtered exactly as the row is - the
	// card a reader sees names this, and "the card is there but the bytes are
	// not" is a real state the filter produces, answered as metadata without
	// content rather than as an error.
	api.HandleFunc("POST /api/attachment", s.handleWriteAttachment)
	api.HandleFunc("GET /api/attachment/{id}", s.handleAttachmentRead)
	// Direct messages. They are chat events and they are read back through the
	// same filter every other event is, so these are three narrowings of reads
	// that already existed rather than a private surface of their own - the
	// privacy is on the event, in EventFilterSQL, and not on this route.
	api.HandleFunc("POST /api/dm/{to}", s.handleDMSay)
	api.HandleFunc("GET /api/dm", s.handleDMRead)
	api.HandleFunc("GET /api/dm/wait", s.handleDMWait)
	api.HandleFunc("GET /api/inbox", s.handleInbox)
	// The waiter: a long poll over the inbox, and the two calls that keep its
	// place. The place is on the node rather than in a file beside the client,
	// which is the whole of why `flowy inbox` can be restarted.
	api.HandleFunc("GET /api/inbox/wait", s.handleInboxWait)
	api.HandleFunc("POST /api/inbox/ack", s.handleInboxAck)
	api.HandleFunc("POST /api/inbox/reader", s.handleInboxReader)
	// Where this principal's own marks stand. A waiter never needed it - it is
	// handed its position by the poll it is already making - but a reader that
	// is a browser has no poll to be told by, and the console reads this on
	// every refresh rather than keeping a copy of the mark in the tab.
	api.HandleFunc("GET /api/inbox/readers", s.handleInboxReaders)
	// And how much one of them has not read. The node counts because the mark
	// is a 57-bit reading and the client asking is a browser, whose numbers are
	// doubles - see handleInboxUnread.
	api.HandleFunc("GET /api/inbox/unread", s.handleInboxUnread)
	api.HandleFunc("DELETE /api/inbox/reader/{name}", s.handleInboxReaderDelete)
	api.HandleFunc("GET /api/presence", s.handlePresence)
	// Assignment and the handoff it opens. /api/inbox/tasks is a longer pattern
	// than /api/inbox, so the mux ranks it first and the two do not collide.
	api.HandleFunc("GET /api/inbox/tasks", s.handleInboxTasks)
	api.HandleFunc("POST /api/assign", s.handleAssign)
	api.HandleFunc("GET /api/task/{id}", s.handleGetTask)
	api.HandleFunc("POST /api/task/{id}/delegate", s.handleDelegateTask)
	api.HandleFunc("POST /api/task/{id}/state", s.handleTaskState)
	api.HandleFunc("PUT /api/me/auto_delegate", s.handleAutoDelegate)
	api.HandleFunc("POST /api/grants", s.handleCreateGrant)
	// Phase 10. The project registry: what exists, and how a name comes to
	// exist. Both are permission-filtered like everything else - the list is
	// narrowed to the projects the principal is in or holds a grant with - and
	// neither decides what anybody may read.
	api.HandleFunc("GET /api/projects", s.handleListProjects)
	api.HandleFunc("POST /api/projects", s.handleCreateProject)
	// Approving is authenticated and operator-only: asking takes no token,
	// admitting is the operator's alone.
	api.HandleFunc("POST /api/join/{id}/approve", s.handleJoinApprove)
	api.HandleFunc("POST /api/join/{id}/refuse", s.handleJoinRefuse)
	// Phase 9. Announcements, and the quiesce a maintenance one can hold.
	// Posting is capability-gated on the agent kind rather than on the route,
	// because the gate is per scope and not per endpoint: anybody may say
	// something about this node, and only a system or monitor agent may say it
	// to the whole fabric.
	api.HandleFunc("POST /api/announcements", s.handleCreateAnnouncement)
	api.HandleFunc("GET /api/announcements", s.handleListAnnouncements)
	api.HandleFunc("GET /api/announcement/{id}/quiesce", s.handleQuiesce)
	api.HandleFunc("POST /api/announcement/{id}/ack", s.handleAckAnnouncement)
	api.HandleFunc("POST /api/announcement/{id}/resolve", s.handleResolveAnnouncement)
	api.HandleFunc("POST /api/quiesce/hold", s.handleHoldResource)
	api.HandleFunc("POST /api/quiesce/release", s.handleReleaseResource)
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
	// A PERSON'S PROJECTS: where they may work, and which one this browser is
	// working in. See projects.go - a session act and an operator act, not a
	// credential act, which is the whole point of them.
	api.HandleFunc("POST /api/projects/{project}/enter", s.handleEnterProject)
	api.HandleFunc("POST /api/projects/{project}/members", s.handleJoinProject)
	api.HandleFunc("GET /api/whoami", s.handleWhoami)
	api.HandleFunc("GET /api/node", s.handleNode)
	// Phase 8. The fabric watching itself: what it holds, what it did, and what
	// happened. Every one of them is scope-filtered by the same rules the reads
	// they aggregate are - a metric is an aggregate, and an aggregate over rows
	// somebody may not read tells them how many there are.
	api.HandleFunc("GET /api/metrics", s.handleMetrics)
	api.HandleFunc("GET /api/activity", s.handleActivity)
	api.HandleFunc("POST /api/activity", s.handlePostActivity)
	// The worklog's own write verb, because the agents doing the work are given
	// no MCP server and could not record it. There is no GET beside it on
	// purpose: the read is GET /api/activity narrowed to the kind, and a second
	// read door would be a second place the permission filter can be missing.
	// The write needs a verb of its own because it has arguments - the refs - the
	// generic door cannot check. See worklog.go.
	api.HandleFunc("POST /api/worklog", s.handleWorklogAppend)
	api.HandleFunc("GET /api/traces", s.handleListTraces)
	api.HandleFunc("GET /api/trace/{id}", s.handleReadTrace)
	// A path under /api/ that nothing claims is a 404 in JSON. Without this it
	// would be net/http's plain-text 404, which a client parsing JSON reads as
	// a broken server rather than as a typo in a URL.
	api.HandleFunc("/api/", s.handleAPINotFound)

	// One mount, so nothing under /api/ can be added later without the token
	// check - including a method or a path this mux does not know, which has to
	// answer 401 before it answers 404.
	//
	// observed is outside authenticate, so that resolving the token is a span
	// inside the request's rather than something that happened before it, and so
	// that a 401 is counted like every other refusal.
	// What was registered, kept so a test can ask the router rather than read
	// this file. The parameter table is only deny-by-default if every route it
	// governs is IN it, and a route that is absent is indistinguishable from one
	// declared to take nothing - see TestEveryGuardedRouteDeclaresItsParams.
	s.apiPatterns = api.patterns
	// THE ORDER IS THE MEANING: authenticate resolves who this is, paramGuard
	// refuses a request nobody can answer, and roleGuard refuses one this
	// person may not make. Roles after parameters because a reader typing a
	// misspelled parameter should be told about the parameter - the answer
	// they can act on - rather than about a permission that was never their
	// problem.
	mux.Handle("/api/", s.observed(s.authenticate(paramGuard(api.ServeMux, s.roleGuard(api.ServeMux, api)))))

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
	// Which console this binary serves, so a page open across a deploy can find
	// out it is stale. The hashed bundle name rather than a version string,
	// because it changes exactly when the bytes do.
	bundle := ""
	if s.console != nil {
		bundle = s.console.bundle
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node":    s.node,
		"version": version,
		"phase":   9,
		"console": s.console != nil && s.console.index != nil,
		"bundle":  bundle,
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

// Unwrap hands back the ResponseWriter underneath, for the reason
// traceWriter.Unwrap does: http.ResponseController finds the real connection by
// walking Unwrap, and a wrapper without one is a wall it cannot see past.
//
// This is the OUTERMOST wrapper on every response this node writes, so without
// it no handler anywhere can flush or set a deadline - and both wrappers had to
// be fixed before /api/stream would open, which is the shape of this class of
// bug: the second one is invisible while the first one is still broken.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
