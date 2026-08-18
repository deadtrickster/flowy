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
//
// A declaration also takes the landing lock on the target, so losing it is a
// 409 that NAMES THE HOLDER rather than a 400 with a sentence in it: the caller
// is an agent deciding whether to spawn a five-minute run, and "master is held
// by flowy-glm until 01:41" is the whole of what it needs to decide to wait.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// mergeGateRequest is one gate moment as it arrives on the wire.
//
// It is a named type rather than an anonymous struct in the handler so that the
// shape this door accepts can be decoded, and refused, in a test that needs no
// database - the refusal below happens before the store is ever reached, and
// that is the half worth pinning down.
type mergeGateRequest struct {
	Run      string `json:"run"`
	GatedTip string `json:"gated_tip"`
	// GatedRef names where the evidence lives when that is not the row's own
	// branch - an integration branch. Optional, and ignored on a verdict that
	// follows a declaration which carried it.
	GatedRef string `json:"gated_ref"`
}

func (s *server) handleMergeGate(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	// STRICT, like every other write door here: a field this struct does not
	// know is a 400 naming it, not a value dropped on the floor.
	//
	// It used to be a plain decoder, and the cost was not a lost value. A body
	// meaning "record this verdict" with the tip misspelt - `tip` for
	// `gated_tip` - decoded into a declaration with no tip in it, which is a
	// DIFFERENT VERB: it takes the landing lock on the target and pushes the
	// window out fifteen minutes. Nothing releases that lock early, so the
	// person who made the typo cannot undo it, and the 200 they got back told
	// them nothing was wrong. One misspelt field held master for a quarter of
	// an hour and sent its author looking for a bug in the store, which was
	// behaving exactly as written.
	var req mergeGateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	art, entry, err := s.db.SetMergeGate(r.Context(), p, r.PathValue("id"),
		req.Run, req.GatedTip, req.GatedRef)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such merge request"))
		return
	}
	var held *store.ErrTargetHeld
	if errors.As(err, &held) {
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
