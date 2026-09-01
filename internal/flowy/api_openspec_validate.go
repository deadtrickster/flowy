package flowy

// The openspec validate door.
//
// A change's delta checks are the store's (ValidateOpenspecChange); this door
// runs them and caches the verdict on the row through the ordinary artifact
// path - the same statement the general doors use, so the lifecycle state is
// carried (carryOpenspecState) and the shape checks run exactly as they do on
// any other edit. Validation REPORTS, it does not refuse: an invalid change
// is a fact the caller holds and the row keeps, and the lifecycle refuses it
// later, at the complete arm (ValidateChange), with the cached sentence.
//
// Read permission is write permission here, the same ruling as the transition
// door: whoever can read a change is a participant in it, and a participant
// who cannot ask "does this validate" has to ask somebody else to ask.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// handleOpenspecValidate runs a change's delta checks and caches the verdict
// on its row.
//
// POST /api/openspec/{id}/validate
//
// No body: the door validates the row it is pointed at, not a body. The
// answer is the verdict itself - ok, the problems when not, and the hash the
// verdict covered - and the cached copy on the row is what the lifecycle's
// complete arm reads.
func (s *server) handleOpenspecValidate(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()
	id := r.PathValue("id")

	art, err := s.db.ReadArtifact(ctx, p, id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"+s.notFoundNote(r, id)))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The kind check is this door's, with its own sentence, the same split as
	// the transition and conflicts doors: a spec has no tasks and no deltas,
	// and the refusal should say what the caller is holding.
	if !store.IsEntityType(art, store.ChangeKind) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("a "+whatItIs(art)+" has nothing to validate - a change checks its "+
				"tasks against its deltas"))
		return
	}

	verdict, err := s.db.ValidateOpenspecChange(ctx, art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := store.SetOpenspecValidation(art, verdict); err != nil {
		serverError(w, r, err)
		return
	}
	// The ordinary write path: the funnel carries the held state and re-runs
	// the shape checks, so a validate is an edit like any other and proves the
	// row still is what it says it is.
	if err := s.db.UpsertArtifact(ctx, art); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, verdict)
}
