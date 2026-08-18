// Command handoff-runner is the trusted-host half of Flowy's repro system.
//
// It reads a finding's repro tree out of the same Postgres the Flowy node
// writes to, resolves a version string to a commit, builds or fetches the
// binary for that commit, renders the self-contained docker-compose package
// the finding hands off, runs it, and records the verdict as a signed
// finding.run event on the finding's own log.
//
//	handoff-runner [-config FILE] [-addr HOST:PORT]
//
//	POST /run              {"finding": "<id>", "version": "latest"}
//	GET  /runs             every run whose finding the caller may read
//	GET  /run/{id}/log     that run's log, as plain text
//	GET  /package          ?finding=<id>&version=latest - the tgz, no binary
//	GET  /version          ?project=<name>&v=latest - resolve without running
//	GET  /healthz          liveness, the one route with no token on it
//
// WHY IT IS A SECOND BINARY AND NOT A HANDFUL OF ROUTES IN `flowy serve`.
// Everything above needs a live Docker daemon, a source checkout on disk and
// enough disk to compile a database. The Flowy node is meant to run with
// none of that: the deployment the Python service this replaces shipped
// drops every Linux capability, sets no-new-privileges, mounts its root
// filesystem read-only and is given no Docker socket at all. Those two sets
// of requirements cannot be met by one process, and trying would mean
// relaxing the locked-down one. So: one migration, two deployables. The
// boundary is enforced by a test - see boundary_test.go - and not only by
// this comment.
//
// WHAT IT ACCEPTS: a bearer token that the shared store resolves to a
// principal, a finding that principal may already read, a project this
// deployment was configured for by name, and a version string shaped like a
// git ref.
//
// WHAT IT REFUSES: an unknown token, on every route including the read-only
// ones; a finding out of the caller's reach, with the same answer the store
// gives for one that does not exist; a project that is not a configured key,
// so nothing in a request can ever name a source path on this host; a
// version that could be read by git as an option rather than as a ref.
//
// WHAT IT NEVER SEES OR HANDS OUT: any credential of the fleet's - it holds
// no forge token, mints nothing, and signs as nobody but the principal that
// called it; the finding's Discovery, the candid investigation record, which
// never enters a package; and the paths on this host where binaries, caches
// and logs live, which are reported as facts about readiness rather than as
// filesystem paths.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/repro"
	"github.com/deadtrickster/flowy/internal/store"
)

// version is what /healthz reports. It tracks the node's own release rather
// than versioning separately: the two deployables come out of one repository
// at one commit, and two version schemes for one migration would be two
// numbers nobody could line up.
const release = "0.8.0"

var buildStamp = "src"

var version = versionOf(release, buildStamp)

func versionOf(release, stamp string) string {
	if stamp == "" {
		return release
	}
	return release + "+" + stamp
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "handoff-runner: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("handoff-runner", flag.ContinueOnError)
	configPath := fs.String("config", os.Getenv("HANDOFF_RUNNER_CONFIG"),
		"per-project configuration file (default $HANDOFF_RUNNER_CONFIG)")
	addr := fs.String("addr", "", "listen address, overriding the config")
	check := fs.Bool("check", false,
		"load the configuration, say what it holds, and exit without listening")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return errors.New("no configuration: pass -config or set HANDOFF_RUNNER_CONFIG. " +
			"This process needs to be told which projects it may touch, and by name - " +
			"there is no default, because a default would be a source checkout on this " +
			"host that nobody chose")
	}
	cfg, err := LoadConfig(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *check {
		describe(os.Stdout, cfg)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dial, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, err := store.Open(dial, cfg.DSN, cfg.Node)
	if err != nil {
		return err
	}
	defer db.Close()
	// The clock lives in memory and the rows do not: this process writes
	// run verdicts into the same event log the node does, so it has to come
	// up above the highest reading already there or its next write would
	// lose a merge it should win. Exactly the node's own reasoning in
	// serve.go, and it applies here for the same reason - two writers, one
	// log.
	if highest, err := db.SeedClock(dial); err != nil {
		return fmt.Errorf("seed clock: %w", err)
	} else if highest > 0 {
		log.Printf("clock seeded above %d", highest)
	}

	for _, dir := range []string{cfg.CacheDir, cfg.LogDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	queue, err := newRunQueue(cfg, db)
	if err != nil {
		return err
	}
	defer queue.Close()

	svc := &service{
		db:       db,
		cfg:      cfg,
		resolver: repro.NewResolver(),
		queue:    queue,
		started:  time.Now(),
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           svc.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: a package build and a log stream both legitimately
		// take longer than any number that would be right for the other four
		// routes, and a timeout that truncates a tgz produces a corrupt
		// package rather than an error anybody can read.
	}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	describe(os.Stderr, cfg)
	log.Printf("handoff-runner %s listening on %s", version, listener.Addr())

	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(listener) }()

	select {
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

// describe prints what this process was configured to do, because the answer
// is otherwise only visible by making requests at it. An operator reading a
// startup line should be able to see which projects it will touch and
// whether it can run anything at all.
func describe(w *os.File, cfg *Config) {
	fmt.Fprintf(w, "handoff-runner %s\n", version)
	fmt.Fprintf(w, "  listen      %s\n", cfg.Addr)
	fmt.Fprintf(w, "  node        %s\n", cfg.Node)
	fmt.Fprintf(w, "  cache       %s\n", cfg.CacheDir)
	fmt.Fprintf(w, "  logs        %s\n", cfg.LogDir)
	fmt.Fprintf(w, "  workers     %d\n", cfg.Workers)
	if cfg.BuildScript == "" {
		fmt.Fprintf(w, "  build       none configured - a version with no cached binary "+
			"cannot be built here\n")
	} else {
		fmt.Fprintf(w, "  build       %s\n", cfg.BuildScript)
	}
	for _, name := range cfg.ProjectNames() {
		p := cfg.Projects[name]
		fmt.Fprintf(w, "  project %-12s source=%s image=%s registry=%s\n",
			name, orNone(p.Source), orNone(p.BaseImage), orNone(p.Registry))
	}
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// errorRef mints the short reference that ties a 500 body to its log line -
// grep bait, naming no row and encoding no time, exactly as the node's own
// does.
func errorRef() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unlogged"
	}
	return hex.EncodeToString(b[:])
}
