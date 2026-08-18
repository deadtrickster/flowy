package main

// POST /api/merge/{id}/land {sha} - record what master became, release the lock.
//
// The fast-forward itself is the lander's, in the repository: the node has no
// git and should not. This door is the other half of landing - the record and
// the exclusivity. A land that does not pass through it leaves the lock held
// until expiry and the row open with no landed_tip, which are both visible,
// which is the point: the queue can now SEE a land that skipped the protocol
// instead of silently believing a fast-forward nobody announced.
//
// 409 for a lost lock, 400 for everything else the store refuses. The refusal
// sentences are the store's, so the door and the MCP surface, if one grows
// here, cannot disagree about what a land needs.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeLandRequest is the sha master became, as it arrives on the wire. Named
// for the same reason mergeGateRequest is, and decoded by the same strict
// decoder: a misspelt `sha` here refuses with a sentence about the sha being
// too short to name a commit, which is a true refusal for a reason that is not
// the caller's actual mistake.
type mergeLandRequest struct {
	SHA string `json:"sha"`
}

func (s *server) handleMergeLand(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req mergeLandRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	art, entry, err := s.db.LandMerge(r.Context(), p, r.PathValue("id"), req.SHA)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such merge request"))
		return
	}
	var held *store.ErrLandRefused
	if errors.As(err, &held) && held.Held != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(),
			"held":  true,
			"lock":  held.Held,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": art, "event": entry})
}
