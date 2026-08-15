package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/store"
)

// The trace surface: the spans this node recorded, and the collector that puts
// two nodes' halves of one trace back together.
//
// GET /api/traces        the recent traces this principal may read
// GET /api/trace/{id}    one trace, in start order - the console's waterfall
// flowy traces           the collector: local spans plus a peer's, as one trace

// handleListTraces summarises the recent traces the principal may read.
//
// GET /api/traces?since=&limit=
func (s *server) handleListTraces(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	since := time.Time{}
	if raw := q.Get("since"); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("since must be an RFC3339 timestamp"))
			return
		}
		since = at
	}
	list, err := s.db.ListTraces(r.Context(), p, store.SpanQuery{
		Since: since, ScopeAll: scopeAll(r, p), Limit: intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": s.node, "traces": list})
}

// handleReadTrace answers one trace: the spans of it this principal may read,
// in start order, with the nodes they were recorded on.
//
// A trace the caller may see nothing of comes back as an empty trace rather
// than a 404, and says which node answered. The distinction matters here in a
// way it does not for an artifact: a trace id is not a row and is not secret -
// it comes back on the Trace-Id header of the caller's own request - and
// "nothing of this trace is yours" is the honest answer to give about spans
// that exist and are somebody else's.
//
// GET /api/trace/{id}
func (s *server) handleReadTrace(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := strings.ToLower(r.PathValue("id"))
	if !otel.ValidTraceID(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("a trace id is 32 hex digits"))
		return
	}
	trace, err := s.db.ReadTrace(r.Context(), p, id, scopeAll(r, p))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": s.node, "trace": trace})
}

// ------------------------------------------------------------- the collector

// traceResponse is what GET /api/trace/{id} answers with, and what the
// collector reads off a peer.
type traceResponse struct {
	Node  string       `json:"node"`
	Trace *store.Trace `json:"trace"`
}

// collected is what `flowy traces` prints: one trace, reassembled out of every
// node that holds part of it.
//
// Sources is the point. A handoff assigned on A and worked on B is one trace
// with spans in two databases, and a reader has to be able to tell "this is the
// whole thing" from "this is the half of it that one node holds" - so the
// collector says which nodes it actually reached and how many spans each of
// them contributed. A peer that could not be reached is named with the reason,
// rather than quietly leaving its half out of a trace that then reads as
// complete.
type collected struct {
	TraceID string        `json:"trace_id"`
	Sources []traceSource `json:"sources"`
	Nodes   []string      `json:"nodes"`
	Spans   []*store.Span `json:"spans"`
	Root    string        `json:"root,omitempty"`
	Started time.Time     `json:"started"`
	Ended   time.Time     `json:"ended"`
	Errors  int           `json:"errors"`
}

type traceSource struct {
	From  string `json:"from"`
	Node  string `json:"node,omitempty"`
	Spans int    `json:"spans"`
	Error string `json:"error,omitempty"`
}

// tracesCmd is `flowy traces --trace <id> [--peer <url> --token <t>]`.
//
// It reads this node's spans for the trace out of the database, asks each peer
// for theirs over the same API the console uses, and assembles the lot into one
// waterfall. That is the whole of cross-node reassembly: the trace id is already
// the same on both sides - it rode over in the meta of the event that opened the
// handoff's thread - so the collector does not have to correlate anything, only
// to gather.
func tracesCmd(args []string) error {
	fs := flag.NewFlagSet("traces", flag.ContinueOnError)
	trace := fs.String("trace", "", "the trace id to collect")
	peers := fs.String("peer", "", "comma-separated peer base URLs to collect from as well")
	token := fs.String("token", os.Getenv("FLOWY_TOKEN"), "bearer token, here and at the peers")
	dsn := fs.String("dsn", os.Getenv("DATABASE_URL"), "Postgres-wire DSN (default $DATABASE_URL)")
	node := fs.String("node", envOr("FLOWY_NODE", defaultNode()), "name of this node")
	operator := fs.String("operator", os.Getenv("FLOWY_OPERATOR"),
		"user id that may use scope=all on this node (default $FLOWY_OPERATOR)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !otel.ValidTraceID(strings.ToLower(*trace)) {
		return errors.New("pass --trace <id>, where the id is 32 hex digits")
	}
	if *dsn == "" {
		return errors.New("no DSN: set DATABASE_URL or pass -dsn")
	}
	if *token == "" {
		return errors.New("no token: pass --token <t> or set FLOWY_TOKEN")
	}
	id := strings.ToLower(*trace)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, err := store.Open(ctx, *dsn, *node)
	if err != nil {
		return err
	}
	defer db.Close()

	// The collector is a client like any other: it reads what its token may
	// read, here and at the peer. Operator-ness is this machine's configuration
	// and is decided here exactly as `flowy serve` decides it - never from a
	// row, and never from anything the peer says.
	p, err := db.PrincipalForToken(ctx, *token)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("token does not resolve on this node: %s", short(*token))
	}
	if err != nil {
		return err
	}
	p.Operator = *operator != "" && p.UserID == *operator

	out := collected{TraceID: id, Sources: []traceSource{}, Spans: []*store.Span{}}
	local, err := db.ReadTrace(ctx, p, id, p.Operator)
	if err != nil {
		return err
	}
	out.Spans = append(out.Spans, local.Spans...)
	out.Sources = append(out.Sources, traceSource{From: "local", Node: *node, Spans: len(local.Spans)})

	client := &http.Client{Timeout: syncTimeout}
	for _, peer := range strings.Split(*peers, ",") {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		base, err := peerBase(peer)
		if err != nil {
			out.Sources = append(out.Sources, traceSource{From: peer, Error: err.Error()})
			continue
		}
		url := base + "/api/trace/" + id
		if p.Operator {
			url += "?scope=all"
		}
		var got traceResponse
		if err := peerRequest(ctx, client, http.MethodGet, url, *token, nil, &got); err != nil {
			// Named, with the reason. A peer that did not answer is a hole in
			// the trace, and a collected trace that does not say so reads as
			// the whole of what happened.
			out.Sources = append(out.Sources, traceSource{From: base, Error: err.Error()})
			continue
		}
		spans := 0
		if got.Trace != nil {
			spans = len(got.Trace.Spans)
			out.Spans = append(out.Spans, got.Trace.Spans...)
		}
		out.Sources = append(out.Sources, traceSource{From: base, Node: got.Node, Spans: spans})
	}

	whole := store.AssembleTrace(id, out.Spans)
	out.Spans, out.Nodes = whole.Spans, whole.Nodes
	out.Root, out.Started, out.Ended, out.Errors = whole.Root, whole.Started, whole.Ended, whole.Errors

	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Println(string(body))
	return nil
}
