package flowy

// DEPENDS-ON over HTTP: the two writes that make an edge, the log behind one
// todo's edges, and the ready query.
//
// The rules are all in the store - see internal/store/deps.go, which is where
// they are and why they are those rules - so this file is argument checking and
// status codes. That is deliberate and it is the same shape the proposal surface
// has: an agent going through MCP and a drainer going through HTTP must not be
// able to reach two ideas of what blocks what, and the way to guarantee that is
// that neither of them holds one.
//
// No console view in this pass. Another run is on the cross-project read and a
// third is on the write path, and a fourth hand in the console is how a bad merge
// happens - so the data and the API land here and the view does not.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// depsView is one todo's dependencies as every surface hands them back: the
// item, whether it can be started, what is in the way, and the log the graph was
// folded out of.
//
// The log is in it rather than behind a second call for proposalView's reason.
// The adjacency is the derived thing and the entries are the record: a reader
// given only "these two block it" cannot answer who said so or when, and that
// question - WHO said A blocks B - is the whole reason an edge is an event.
type depsView struct {
	Item     *store.Artifact  `json:"item"`
	Ready    bool             `json:"ready"`
	Assignee string           `json:"assignee"`
	Blockers []store.Blocker  `json:"blockers"`
	Log      []store.DepEntry `json:"log"`
}

// viewDeps assembles it. Both surfaces call this, so a console and an agent are
// looking at one answer rather than at two that agree today.
func viewDeps(ctx context.Context, db *store.DB, p *store.Principal, id string) (*depsView, error) {
	ready, err := db.Readiness(ctx, p, id)
	if err != nil {
		return nil, err
	}
	log, err := db.DepLog(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return &depsView{
		Item: ready.Item, Ready: ready.Ready, Assignee: ready.Assignee,
		Blockers: ready.Blockers, Log: log,
	}, nil
}

// depRequest is what naming an edge takes: the other end.
type depRequest struct {
	Blocker string `json:"blocker"`
}

// handleAddDep records that a todo depends on another one.
//
// POST /api/todo/{id}/deps  {blocker}
func (s *server) handleAddDep(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var req depRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	e, err := s.db.AddDep(r.Context(), p, r.PathValue("id"), strings.TrimSpace(req.Blocker))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	// The state the edge leaves the todo in, so a caller sees what it did without
	// a second call - including that adding one blocker did not make the todo
	// ready, which is the answer that most often surprises.
	view, err := viewDeps(r.Context(), s.db, p, e.Artifact)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": e, "deps": view})
}

// handleRemoveDep records that it no longer does. The edge is not deleted -
// there is nothing to delete, it was an entry - and both entries are in the log
// afterwards.
//
// DELETE /api/todo/{id}/deps/{blocker}
func (s *server) handleRemoveDep(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	e, err := s.db.RemoveDep(r.Context(), p, r.PathValue("id"), r.PathValue("blocker"))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	view, err := viewDeps(r.Context(), s.db, p, e.Artifact)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": e, "deps": view})
}

// handleGetDeps reads one todo's edges and the log behind them.
//
// GET /api/todo/{id}/deps
func (s *server) handleGetDeps(w http.ResponseWriter, r *http.Request) {
	view, err := viewDeps(r.Context(), s.db, principalOf(r), r.PathValue("id"))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleReady is the queue: the outstanding work this principal may read, each
// item saying whether it can be started and what is in the way.
//
// GET /api/ready?room=build&scope=project&ready=true&limit=50
//
// Everything comes back by default, ready or not, because a drainer told only
// "here are three ready todos" cannot tell a queue with nothing to do from a
// queue that has stopped. ?ready=true narrows it for the caller that has already
// decided it does not care why.
//
// The answer is this reader's and nobody else's: two principals looking at the
// same queue at the same moment correctly disagree, because a blocker one of
// them cannot see holds its todo for that one and not for the other.
func (s *server) handleReady(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := store.ArtifactQuery{ScopeAll: scopeAll(r, p)}

	room, err := roomArg(r.URL.Query().Get("room"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	q.Room = room
	if scope := r.URL.Query().Get("scope"); scope != "" && scope != "all" {
		v, err := oneOf("scope", scope, memScopes, "")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		q.Visibility = visibilityOf(v)
	}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("limit is a number"))
			return
		}
		q.Limit = n
	}

	rows, err := s.db.Ready(r.Context(), p, q)
	if err != nil {
		serverError(w, r, err)
		return
	}
	ready := len(store.ReadyOnly(rows))
	if r.URL.Query().Get("ready") == "true" {
		rows = store.ReadyOnly(rows)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(rows), "ready": ready, "items": rows,
	})
}

// writeQueueError turns a store refusal by one of the queue verbs into a status
// code and a sentence. Both surfaces use it - the edges here and the assignment in
// assign.go - because a refusal added to either verb must not be one that this
// door maps to 400 and the other maps to 500. See store.DepRefusal.
//
// An id that names nothing this principal may read is a 404, and it names no
// row it did not already reach: naming an id in an edge is not a way to find out
// what else that id might be. What it now adds, when it can, is which ID SPACE
// the caller's id came from - a chat message, a chat thread - because that is a
// fact about the id in the caller's own hand and not about the store's contents.
// It is read through this principal's filter, so a reader who could not have
// read the message is told nothing and gets the bare refusal. See misreadIDNote.
//
// The rest are the caller's mistake and say what it was, because each of them
// is something the caller can fix - the two ends are the same todo, the loop
// already goes the other way, the edge is already there, the assignee is a
// paragraph.
//
// A refusal that carries a code also carries the row explaining it - see
// knownissue.go. None of the refusals below carries one yet, so this costs a
// type assertion and no query today; it is here rather than at the merge queue
// alone because the point of the mechanism is that any door refusing for a
// reason somebody has already written down cites where it is written, and the
// next such reason should need a code on the refusal and nothing else.
func (s *server) writeQueueError(w http.ResponseWriter, r *http.Request, err error) {
	s.writeQueueErrorFor(w, r, err, "")
}

// writeQueueErrorFor is writeQueueError for a door whose path names exactly one
// row: the 404 it gives for an id nothing answers to also says whether that id
// is a chat message or a chat thread, and which row that message or thread is
// about. See misreadIDNote for why, and for why the sentence names no row this
// caller could not already read.
//
// The id is passed in rather than taken off the request because the doors are
// not all one-id doors. An edge names a todo and a blocker, the store's
// not-found does not say which of the two it was, and a note that assumed would
// point the reader at the wrong end - the exact failure being fixed here. Those
// doors keep calling writeQueueError and keep giving the bare 404.
func (s *server) writeQueueErrorFor(w http.ResponseWriter, r *http.Request, err error, id string) {
	var (
		notATodo store.NotATodoError
		refusal  store.DepRefusal
	)
	switch {
	case errors.As(err, &notATodo):
		// This refusal names the id it is about, so the diagnosis needs nothing
		// from the caller and is safe at every door including the two-id ones:
		// the id in the sentence is the id that missed. It is the refusal the
		// claim door gives - see store.readWorkItem - and so the one an agent
		// pasting a thread id out of a room notification actually receives.
		writeJSON(w, http.StatusNotFound,
			errorBody(notATodo.Error()+s.misreadIDNote(r, notATodo.ID)))
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody("no such todo"+s.misreadIDNote(r, id)))
	case errors.As(err, &refusal):
		s.writeRefusal(w, r, http.StatusBadRequest, err, refusal.Error())
	default:
		serverError(w, r, err)
	}
}
