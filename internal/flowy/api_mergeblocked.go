package flowy

// POST /api/merge/{id}/blocked - why nothing could take this row.
//
// The third answer the queue owes a reader, beside "no verdict" and "the gate
// failed": nothing could pick it up. See internal/store/mergeblocked.go for the
// twenty minutes that argument cost.
//
// IT TAKES NO LOCK AND NEEDS NONE. Every other verb on a merge request either
// takes the landing lock or holds it already; this is the one for a caller that
// never got that far, and requiring the lock to report not having it would be
// the joke version of this door.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeBlockedRequest is the body, strict like every other write door here: a
// field this struct does not know is a 400 naming it rather than a value
// dropped, because a skip with its reason silently discarded is exactly the
// silence this route exists to end.
type mergeBlockedRequest struct {
	Why string `json:"why"`
}

func (s *server) handleMergeBlocked(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req mergeBlockedRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	art, entry, err := s.db.SetMergeBlocked(r.Context(), p, r.PathValue("id"), req.Why)
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
