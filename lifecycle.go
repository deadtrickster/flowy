package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// The issue lifecycle.
//
// A bug moves open -> triaged -> in-progress -> in-review -> done, and may leave
// the line at any point for one of the two terminal exits: wont-fix and
// duplicate. Nothing skips a step, because a status that can jump is a status
// nobody trusts - "in-review" has to mean the work happened.
//
// Every move appends an event of type 'status' naming the artifact, the actor
// and the move itself ("open->triaged"), with the previous status event as its
// parent. That chain is the audit trail: it is in the same log as the chat and
// the handoffs, it carries the same clock, and reading it in order tells you who
// moved the issue and when without a second table.
const statusEventType = "status"

// Statuses.
const (
	statusOpen       = "open"
	statusTriaged    = "triaged"
	statusInProgress = "in-progress"
	statusInReview   = "in-review"
	statusDone       = "done"
	statusWontFix    = "wont-fix"
	statusDuplicate  = "duplicate"
)

// statusFlow is the line: each status and the one that follows it.
var statusFlow = map[string]string{
	statusOpen:       statusTriaged,
	statusTriaged:    statusInProgress,
	statusInProgress: statusInReview,
	statusInReview:   statusDone,
}

// statusExits are the ways out of the line. They are reachable from anywhere
// that is not already terminal: a bug is found to be a duplicate at triage as
// often as in review.
var statusExits = []string{statusWontFix, statusDuplicate}

// terminalStatus is where a lifecycle stops. Nothing moves out of one - an issue
// that comes back is reopened by being worked on again, and the trail should say
// so rather than quietly rewinding.
var terminalStatus = map[string]bool{
	statusDone:      true,
	statusWontFix:   true,
	statusDuplicate: true,
}

// lifecycleTypes are the artifact types that have a lifecycle at all. A
// transcript has no status to move and a memory item's status is its own thing -
// mem_write uses it to mark a todo done - so neither is dragged into this.
var lifecycleTypes = map[string]bool{
	"bug": true, "feature": true, "note": true, "task": true,
}

// knownStatus reports whether s is one of the seven.
func knownStatus(s string) bool {
	if _, ok := statusFlow[s]; ok {
		return true
	}
	return terminalStatus[s]
}

// statusOf reads an artifact's current status, treating a row that was never
// given one as open. An artifact created without a status is a fresh issue.
func statusOf(art *store.Artifact) string {
	if s := strings.TrimSpace(art.Status); s != "" {
		return s
	}
	return statusOpen
}

// nextStatuses is everywhere an artifact at from may go, in a stable order so
// the console can draw them and the error message reads the same twice.
func nextStatuses(from string) []string {
	if terminalStatus[from] {
		// Empty rather than nil: a console asking where an issue can go next
		// should be told "nowhere", not handed a null to guard against.
		return []string{}
	}
	out := append([]string{}, statusExits...)
	if next, ok := statusFlow[from]; ok {
		out = append(out, next)
	}
	sort.Strings(out)
	return out
}

// canTransition reports whether from -> to is a move the workflow allows.
func canTransition(from, to string) bool {
	for _, allowed := range nextStatuses(from) {
		if allowed == to {
			return true
		}
	}
	return false
}

// statusRequest is the body of a transition.
type statusRequest struct {
	Status string `json:"status"`
}

// handleArtifactStatus moves an artifact through the workflow and records the
// move.
//
// Anybody who can read the artifact can move it, which for a shared bug means
// the person it was assigned to as well as the project it lives in. That is the
// point of the assignment: the share is what makes them a participant, and a
// participant who cannot say "I am working on this" has to ask somebody else to
// say it for them.
//
// POST /api/artifact/{id}/status  {status}
func (s *server) handleArtifactStatus(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	id := r.PathValue("id")

	var req statusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	to := strings.TrimSpace(req.Status)
	if !knownStatus(to) {
		writeJSON(w, http.StatusBadRequest, errorBody(
			"status must be one of open, triaged, in-progress, in-review, done, wont-fix, duplicate"))
		return
	}

	art, err := s.db.ReadArtifact(ctx, p, id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	if !lifecycleTypes[art.Type] {
		writeJSON(w, http.StatusBadRequest,
			errorBody("a "+art.Type+" has no lifecycle; bug, feature, note and task do"))
		return
	}

	from := statusOf(art)
	if !canTransition(from, to) {
		next := nextStatuses(from)
		msg := from + " is terminal"
		if len(next) > 0 {
			msg = "from " + from + " the workflow allows " + strings.Join(next, ", ")
		}
		writeJSON(w, http.StatusConflict, errorBody("cannot move "+from+" -> "+to+": "+msg))
		return
	}

	if err := s.db.SetArtifactStatus(ctx, art, to); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}

	event, err := s.appendStatusEvent(r, art, from, to)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": art, "event": event})
}

// handleArtifactHistory is the trail: every status move on this artifact, in
// order, with the parent links that chain them.
//
// It is gated on reading the artifact rather than on reading each event,
// because the trail is a property of the artifact - showing a reader only the
// half of it that was written from their own project would be a history that
// disagrees with itself depending on who asks.
//
// GET /api/artifact/{id}/history
func (s *server) handleArtifactHistory(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	id := r.PathValue("id")

	art, err := s.db.ReadArtifact(ctx, p, id, scopeAll(r, p))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}

	events, err := s.db.ArtifactEvents(ctx, art.ID, statusEventType)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifact": art.ID,
		"status":   statusOf(art),
		"next":     nextStatuses(statusOf(art)),
		"events":   events,
	})
}

// appendStatusEvent writes the move into the log, as a child of the move before
// it. The event lands in the artifact's project rather than the actor's: the
// trail belongs with the thing it is about, and an assignee moving a shared bug
// from another project should not fork its history into a second log.
func (s *server) appendStatusEvent(
	r *http.Request, art *store.Artifact, from, to string,
) (*store.Event, error) {
	p := principalOf(r)
	actor, kind := chatActor(p)

	meta, err := json.Marshal(map[string]string{
		"actor_kind": kind,
		"actor_user": p.UserID,
		"from":       from,
		"to":         to,
	})
	if err != nil {
		return nil, err
	}

	// The chain: the previous status event is this one's parent, and the thread
	// is the one it opened. An artifact whose status has never moved starts
	// both here.
	parents, thread := []string{}, ""
	last, err := s.db.LatestArtifactEvent(r.Context(), art.ID, statusEventType)
	switch {
	case err == nil:
		parents, thread = []string{last.ID}, last.Thread
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}

	e := &store.Event{
		Type:     statusEventType,
		Project:  art.Project,
		Thread:   thread,
		Parents:  parents,
		Actor:    actor,
		Artifact: art.ID,
		Body:     from + "->" + to,
		Meta:     json.RawMessage(meta),
	}
	if err := s.db.AppendEvent(r.Context(), e); err != nil {
		return nil, err
	}
	return e, nil
}
