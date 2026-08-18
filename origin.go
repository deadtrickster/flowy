package main

import (
	"net/http"
	"strings"
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

// viewOrigins is a row's live origins and the log behind them, which is the
// answer both writes return so a caller sees what it did without asking again.
func viewOrigins(r *http.Request, s *server, id string) (map[string]any, error) {
	p := principalOf(r)
	live, err := s.db.OriginsOf(r.Context(), p, id)
	if err != nil {
		return nil, err
	}
	log, err := s.db.OriginLog(r.Context(), p, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact": id, "origins": live, "log": log}, nil
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
