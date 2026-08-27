package flowy

// POST /api/merge/{id}/abandon {reason} - give master back without landing.
//
// The door that was missing. Until it existed the lock could be taken by a gate
// declaration and released only by a land, so an agent whose gate went red held
// the shared tree for the full fifteen minutes with no way to say so. The
// expiry was doing double duty - safety net for a dead holder AND normal exit
// for a live one - and a signal that means two things means neither.
//
// The reason is required by the store, not by this handler, so the CLI and any
// MCP verb that grows here refuse identically. 409 for somebody else's lock,
// 400 for a missing reason or a target nobody holds, 404 for a row that is not
// a merge request - the same three answers the land door gives, because a
// caller should not have to learn two vocabularies for the same lock.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeAbandonRequest is why the holder is giving up, as it arrives on the wire.
type mergeAbandonRequest struct {
	Reason string `json:"reason"`
}

func (s *server) handleMergeAbandon(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req mergeAbandonRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	art, entry, err := s.db.AbandonMerge(r.Context(), p, r.PathValue("id"), req.Reason)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such merge request"))
		return
	}
	var refused *store.ErrAbandonRefused
	if errors.As(err, &refused) && refused.Held != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(),
			"held":  true,
			"lock":  refused.Held,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": art, "event": entry})
}
