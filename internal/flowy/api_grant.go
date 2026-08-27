package flowy

// POST /api/agent/{id}/projects - widen a seat's reach without re-minting it.
//
// 01M0FNQSZ2. The operator asked how to grant an existing agent another
// project. There was no way: token_projects was written only by mint, which
// replaces the whole set, so widening meant a new token and a redistribution
// to every place the old one was configured.
//
// OPERATOR ONLY, through the same wrapper mint's neighbours use. Reach is the
// permission boundary of this fabric - a seat that could widen itself would
// make the boundary advisory - and the wrapper is used rather than a check in
// the handler because a set of routes that all need one gate is a set where
// one of them eventually does not have it.
//
// ADDITIVE AND IDEMPOTENT. It says "also this project", once. Re-running it is
// how somebody makes sure, and making sure must not fail.

import (
	"net/http"
	"strings"
)

type grantProjectRequest struct {
	// Add is the project to widen to. Named rather than positional because a
	// door that will later also narrow should not have to guess which it was
	// asked for from the shape of the body.
	Add string `json:"add"`
}

func (s *server) handleGrantProject(w http.ResponseWriter, r *http.Request) {
	var req grantProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("body must be json: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.Add) == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody(`say which project to add: {"add": "serenedb"}`))
		return
	}
	added, err := s.db.GrantProject(r.Context(), r.PathValue("id"), req.Add)
	if err != nil {
		// The store's refusals name which of the two arguments was wrong - the
		// project nobody declared, or the agent that holds no token - so they
		// are passed through rather than flattened into "bad request".
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	// THE COUNT IS THE ANSWER. 0 with no error means every token this seat
	// holds already reached that project, which is a success and reads as one.
	writeJSON(w, http.StatusOK, map[string]any{
		"agent": r.PathValue("id"), "project": req.Add, "tokens_widened": added,
	})
}
