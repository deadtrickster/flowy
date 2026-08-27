package flowy

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// The openspec lifecycle door.
//
// A change moves proposed -> in-progress -> complete -> archived, one step at
// a time, and the state lives in fields.openspec.state - not the status
// column, which carries the issue and queue vocabularies and already means
// two things. The rules are the store's (CheckOpenspecTransition); this door
// owns the HTTP: read the row, refuse whatever is not a change, ask the store
// about the move, and hand it the pair to write.
//
// THE STATE IS THIS DOOR'S TO WRITE, and that is the invariant the route
// carries: every other write path preserves the held row's state at the store
// funnel (carryOpenspecState), so the only way a state appears without a
// transition event behind it is a move that went around the door - and there
// is no such path. The event is minted (api.go), so the trail cannot be
// forged either.
//
// Each move appends one event of type openspec.transition chained on the
// previous one, the same shape as the status trail and for the same reason: a
// state with no entry behind it is a lifecycle nobody can audit.
const openspecTransitionEventType = "openspec.transition"

// openspecTransitionRequest is the body: where the change goes. Nothing else
// - the from side is read off the row, because a caller who claims the state
// and a row that has one can disagree, and the row is the one that counts.
type openspecTransitionRequest struct {
	To string `json:"to"`
}

// handleOpenspecTransition moves a change along the line.
//
// Read permission is write permission here, the same ruling as the issue
// workflow (handleArtifactStatus): whoever can read a change is a participant
// in it, and a participant who cannot say "this is now in progress" has to
// ask somebody else to say it for them.
//
// POST /api/openspec/{id}/transition  {to}
func (s *server) handleOpenspecTransition(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	id := r.PathValue("id")

	var req openspecTransitionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("to is required - a change moves "+
			"proposed -> in-progress -> complete -> archived"))
		return
	}

	art, err := s.db.ReadArtifact(ctx, p, id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The kind check is this door's, with its own sentence, the same split as
	// the conflicts door: a spec's lifecycle is different machinery (p6 gates
	// archive on the merge), and the refusal should say what the caller is
	// holding, not what a change can do.
	if !store.IsEntityType(art, store.ChangeKind) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("a "+whatItIs(art)+" has no openspec lifecycle - a change moves "+
				"proposed -> in-progress -> complete -> archived"))
		return
	}

	if err := s.db.CheckOpenspecTransition(ctx, art, to); err != nil {
		// The store's sentence is the whole answer: it names the row's state,
		// the move, and the reason - a backward edge says what the line
		// allows, an open task says which one, validation says what failed
		// or which door fixes it. The caller is holding the row, so the
		// sentence is about their move and not a generic refusal.
		writeJSON(w, http.StatusConflict, errorBody(err.Error()))
		return
	}

	from, err := store.OpenspecStateOf(art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	event, err := s.openspecTransitionEvent(r, art, from, to)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.db.MoveOpenspecState(ctx, art, to, event); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": art, "event": event})
}

// openspecTransitionEvent builds the move's entry in the log, chained on the
// previous transition - the same shape as statusEvent, over the openspec
// event type, so the two trails never share a thread.
func (s *server) openspecTransitionEvent(
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

	parents, thread := []string{}, ""
	last, err := s.db.LatestArtifactEvent(r.Context(), art.ID, openspecTransitionEventType)
	switch {
	case err == nil:
		parents, thread = []string{last.ID}, last.Thread
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}

	return &store.Event{
		Type:     openspecTransitionEventType,
		Project:  art.Project,
		Thread:   thread,
		Parents:  parents,
		Actor:    actor,
		Artifact: art.ID,
		Body:     from + "->" + to,
		Meta:     json.RawMessage(meta),
	}, nil
}
