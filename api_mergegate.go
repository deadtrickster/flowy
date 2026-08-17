package main

// POST /api/merge/{id}/gate - declare a run, or record what it measured.
//
// The MCP verb landed first and I made the same mistake twice in one evening:
// merge_queue was MCP-only until somebody needed it from the console, and
// merge_gate shipped MCP-only an hour later. A door only agents can knock on is
// half a door, and the half that is missing is the one a person uses.
//
// It is the same write as the tool, deliberately: one store call, one set of
// refusals. Two implementations of "declare a gate" would drift, and the thing
// they would drift about is whether a run was declared at all.

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

func (s *server) handleMergeGate(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req struct {
		Run      string `json:"run"`
		GatedTip string `json:"gated_tip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	art, entry, err := s.db.SetMergeGate(r.Context(), p, r.PathValue("id"), req.Run, req.GatedTip)
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
