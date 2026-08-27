package flowy

// POST /api/merge/{id}/unblocked - what stopped the last caller is gone.
//
// The mirror of POST /api/merge/{id}/blocked, and it exists because that door
// had no counterpart: a reason could be recorded in a second by anything that
// tried the row, and could only be retired by a fifteen-minute clock or by a
// declaration, which takes the landing lock. So the seat that actually fixed
// the thing had to wait for whoever was landing before it could say so. See
// internal/store/mergeblocked.go for the row this was measured on.
//
// IT TAKES NO LOCK, for the same reason its mirror takes none: requiring the
// lock to withdraw a report about not having the lock is the joke version of
// the door.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeUnblockedRequest is the body. Why is what you fixed, and it is required
// rather than optional: a block that disappears with nobody's name on a reason
// is indistinguishable from a block that timed out, which is the one reading
// this door exists to make impossible.
type mergeUnblockedRequest struct {
	Why string `json:"why"`
}

func (s *server) handleMergeUnblocked(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req mergeUnblockedRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	art, entry, err := s.db.SetMergeUnblocked(r.Context(), p, r.PathValue("id"), req.Why)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such merge request"))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": art, "event": entry})
}
