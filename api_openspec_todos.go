package main

// The derived-todos door.
//
// A change's tasks.md is the authoritative source of its todos (p2), and the
// derived rows are the store's answer to it - but they are ordinary todo rows,
// and no filter on GET /api/artifacts reaches a todo's origin fields. The
// console's openspec row view needs the list a change derived, so this door is
// that read: it answers the store's DerivedTodosOf, and nothing else.
//
// A derived todo the caller cannot read is dropped rather than leaked, the
// same rule the conflicts door keeps for its edges: the row is a todo in its
// own right and a principal that cannot see it gets the todos they can.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// handleOpenspecTodos lists the todos a change has derived from its tasks.md.
//
// GET /api/openspec/{id}/todos
//
// The answer is the todos themselves - the ordinary artifact shape, so the
// caller renders them the way it renders any todo and reads their deps through
// the todo deps doors. A spec derives nothing: it has no tasks.md, and the
// refusal says so rather than answering an empty list.
func (s *server) handleOpenspecTodos(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := r.PathValue("id")
	art, err := s.db.ReadArtifact(r.Context(), p, id, scopeAll(r, p))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound,
			errorBody("no such artifact"+s.misreadIDNote(r, id)))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	if !store.IsEntityType(art, store.ChangeKind) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("a "+whatItIs(art)+" derives no todos - a change's tasks.md is the "+
				"source, and only a change has one"))
		return
	}
	todos, err := s.db.DerivedTodosOf(r.Context(), id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := make([]*store.Artifact, 0, len(todos))
	for _, t := range todos {
		if _, err := s.db.ReadArtifact(r.Context(), p, t.ID, scopeAll(r, p)); err == nil {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"todos": out})
}
