package main

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

type setRoleRequest struct {
	Role string `json:"role"`
}

// POST /api/user/{id}/role - say what somebody may do here.
//
// Operator-only, through the same operatorOnly wrapper the rest of the
// operator's surface uses, so this route cannot be the one that forgets. That
// wrapper is also what makes the grant safe to expose at all: the set of
// operators can only grow by the decision of somebody already in it, and
// $FLOWY_OPERATOR names the first.
func (s *server) handleSetRole(w http.ResponseWriter, r *http.Request) {
	var req setRoleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	err := s.db.SetRole(r.Context(), principalOf(r), r.PathValue("id"), req.Role)
	// NOT FOUND IS ITS OWN ANSWER. A grant that names nobody and a grant you may
	// not make are different problems, and answering both with 403 sends the
	// caller to check their own standing when the real fault is a typo in an id.
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such person here: "+r.PathValue("id")))
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": r.PathValue("id"), "role": req.Role})
}
