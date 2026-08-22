package main

// The openspec doors.
//
// Two kinds of memory row - spec and change - are the openspec floor, and
// these are the doors that speak their names. The rows themselves are ordinary
// artifact rows: the general doors create and read them too, and the shape a
// row of either kind must hold is the store's rule
// (internal/store/openspec.go), asked of every write path, not this door's.
//
// What this door adds over POST /api/artifacts is the narrowing: kind is
// closed here, type is memory or nothing, and a request that belongs at the
// general door is refused naming it rather than accepted on a guess. The list
// is one call for both kinds because the two are one board - a change is read
// next to the capability it touches.
//
// The lifecycle, derivation and validation doors are later siblings - p2-p4 of
// the openspec plan (room message 01M0KA567A9GQTZH5650RA2V91).

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// openspecListParams are the query parameters GET /api/openspec honours, and
// the whole of them - the same deny-by-default contract listParams keeps for
// GET /api/artifacts. Anything else is refused by name rather than dropped.
var openspecListParams = map[string]bool{
	"limit":  true,
	"room":   true,
	"scope":  true,
	"status": true,
}

// openspecKindAtThisDoor refuses what POST /api/openspec does not write, and
// says where it belongs instead. Empty means the request is one this door
// takes.
//
// A spec or a change is a memory row - kind is the identity (entitytype.go) -
// so a request that names another type is refused rather than quietly
// rewritten, and one that names another kind is refused pointing at the
// general door: this door is a narrowing, not a second vocabulary.
//
// A pure function so the refusal is exercised without a database or a request.
func openspecKindAtThisDoor(typ, kind string) string {
	if typ != "" && typ != store.MemoryType {
		return fmt.Sprintf(
			"an openspec row is type %q - send type %q or none, and POST /api/artifacts takes type %q",
			store.MemoryType, store.MemoryType, typ)
	}
	if kind != store.SpecKind && kind != store.ChangeKind {
		return fmt.Sprintf("kind must be %q or %q here - POST /api/artifacts takes the general case",
			store.SpecKind, store.ChangeKind)
	}
	return ""
}

// handleOpenspecCreate files a spec or a change.
//
// POST /api/openspec
//
// The body is artifactRequest narrowed to the two openspec kinds. Everything
// else - ownership, the update-vs-create decision, the store's shape checks -
// is the shared write path in createArtifact, so a change with no proposal.md
// is refused here with the store's own sentence, exactly as it is at
// POST /api/artifacts.
func (s *server) handleOpenspecCreate(w http.ResponseWriter, r *http.Request) {
	var req artifactRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if why := openspecKindAtThisDoor(req.Type, req.Kind); why != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(why))
		return
	}
	req.Type = store.MemoryType
	s.createArtifact(w, r, req)
}

// handleOpenspecList lists the spec and change rows the principal may read,
// newest first, the same permission filter as every other artifact read.
//
// GET /api/openspec?status=&room=&limit=
//
// status narrows the same way it does on GET /api/artifacts - the artifact
// status COLUMN, which is the issue and queue vocabulary. The openspec
// lifecycle state (proposed/in-progress/complete/archived) lives in
// fields.openspec.state, moved only by the transition door (p3); this filter
// does not read it.
func (s *server) handleOpenspecList(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	if why := refuseUnknownParams(q, openspecListParams); why != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(why))
		return
	}
	room, err := roomArg(q.Get("room"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	list, err := s.db.ListArtifacts(r.Context(), p, store.ArtifactQuery{
		Type:     store.MemoryType,
		Kinds:    []string{store.SpecKind, store.ChangeKind},
		Status:   q.Get("status"),
		Room:     room,
		ScopeAll: scopeAll(r, p),
		Limit:    intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stampScope(map[string]any{"artifacts": list}, answerScopeOf(r, p)))
}

// handleOpenspecConflicts lists what one change clashes with: every other
// change whose spec delta touches the same capability. The edges are kept by
// the store on every write of a change (internal/store/openspec_conflict.go);
// this is their read.
//
// GET /api/openspec/{id}/conflicts
//
// The answer names the other change and the capability, nothing more - the
// caller reads the other row the way it reads any row. An edge whose other
// end this principal cannot read is dropped rather than leaked: the edge is
// a fact about two rows, and a reader that cannot see one of them gets the
// half they can.
func (s *server) handleOpenspecConflicts(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusBadRequest, errorBody(
			"a spec edits nothing and clashes with nothing - conflicts are edges between changes"))
		return
	}
	edges, err := s.db.ConflictsOf(r.Context(), id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := make([]store.OpenspecConflict, 0, len(edges))
	for _, e := range edges {
		if _, err := s.db.ReadArtifact(r.Context(), p, e.Change, scopeAll(r, p)); err == nil {
			out = append(out, e)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": out})
}
