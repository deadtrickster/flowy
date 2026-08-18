package main

import (
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// Where a row came from, over HTTP.
//
// The verbs mirror /api/todo/{id}/deps because the shape is the same - an entry
// is the edge, a removal appends rather than deletes, and a read returns the
// live set with the log behind it. What is NOT the same is the meaning, and the
// path says so: this is /origins on any artifact, not /deps on a todo, because
// DEPENDS-ON orders the queue and this orders nothing. See
// internal/store/origin.go for why they must not share a verb.
//
// Any artifact this principal can read may be either end. That is the point:
// a todo comes out of a diagram, a diagram comes out of the work, a report
// comes out of a finding. None of those is a blocker.

// originRequest is what naming a relation takes: the other end.
type originRequest struct {
	Origin string `json:"origin"`
}

// originView is one end of a relation as a reader sees it: always the id,
// and a route and a title only when this reader can already read that row.
//
// THE ID IS ALWAYS THERE AND THE REST IS EARNED. The entry itself carries a
// bare id on purpose - it is readable by principals who cannot read the origin,
// so a title in the meta would be a leak. Resolving it HERE is not the same
// thing: this asks the store, as this principal, and only says what a read
// would have told them anyway. A reader who cannot see it still learns that
// their row came out of something, which is the honest half of the answer and
// is what makes an unresolvable origin a fact rather than an absence.
type originView struct {
	ID    string `json:"id"`
	Ref   string `json:"ref,omitempty"`
	Title string `json:"title,omitempty"`
}

// viewOrigins is a row's live origins and the log behind them, which is the
// answer both writes return so a caller sees what it did without asking again.
func viewOrigins(r *http.Request, s *server, id string) (map[string]any, error) {
	p := principalOf(r)
	live, err := s.db.OriginsOf(r.Context(), p, id)
	if err != nil {
		return nil, err
	}
	seen := make([]originView, 0, len(live))
	for _, origin := range live {
		row := originView{ID: origin}
		if art, err := s.db.ReadArtifact(r.Context(), p, origin, false); err == nil {
			row.Title = art.Title
			if ref, err := store.RefOf(art); err == nil {
				row.Ref = ref.String()
			}
		}
		seen = append(seen, row)
	}
	log, err := s.db.OriginLog(r.Context(), p, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact": id, "origins": seen, "log": log}, nil
}

// handleAddOrigin records that a row came out of another.
//
// POST /api/artifact/{id}/origins  {origin}
func (s *server) handleAddOrigin(w http.ResponseWriter, r *http.Request) {
	var req originRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	e, err := s.db.AddOrigin(r.Context(), principalOf(r), r.PathValue("id"), strings.TrimSpace(req.Origin))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	view, err := viewOrigins(r, s, e.Artifact)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": e, "provenance": view})
}

// handleRemoveOrigin records that it no longer says so. Nothing is deleted -
// the entry that takes it back is appended, and both are in the log.
//
// DELETE /api/artifact/{id}/origins/{origin}
func (s *server) handleRemoveOrigin(w http.ResponseWriter, r *http.Request) {
	e, err := s.db.RemoveOrigin(r.Context(), principalOf(r), r.PathValue("id"), r.PathValue("origin"))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	view, err := viewOrigins(r, s, e.Artifact)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"event": e, "provenance": view})
}

// handleGetOrigins reads where a row says it came from.
//
// GET /api/artifact/{id}/origins
func (s *server) handleGetOrigins(w http.ResponseWriter, r *http.Request) {
	view, err := viewOrigins(r, s, r.PathValue("id"))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
