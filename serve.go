package main

import (
	"context"
	"encoding/json"
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

	"github.com/deadtrickster/flowy/internal/store"
)

// defaultAddr is where the node listens when FLOWY_ADDR is unset.
const defaultAddr = "127.0.0.1:8787"

// server holds what the HTTP handlers need.
type server struct {
	db      *store.DB
	node    string
	started time.Time
}

// serve runs the node's HTTP server until it is interrupted.
func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", envOr("FLOWY_ADDR", defaultAddr), "listen address")
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name of this node, stamped onto every row")
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

	srv := &server{db: db, node: *node, started: time.Now()}

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

// routes wires the Phase 0 surface. Later phases hang artifacts, events and the
// handoff endpoints off the same mux.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /", s.handleRoot)
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
// the row count of each spine table.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	resp := healthzResponse{
		OK:       true,
		Node:     s.node,
		Version:  version,
		DB:       "up",
		HLC:      s.db.Clock().Pack(),
		UptimeMS: time.Since(s.started).Milliseconds(),
	}

	if err := s.db.Ping(ctx); err != nil {
		resp.OK, resp.DB, resp.Error = false, "down", err.Error()
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}
	if r.URL.Query().Get("counts") != "" {
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

func (s *server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node":    s.node,
		"version": version,
		"phase":   0,
		"routes":  []string{"/healthz", "/version"},
	})
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
