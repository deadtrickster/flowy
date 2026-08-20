package main

// WHERE A FINDING WENT UPSTREAM, over HTTP.
//
// POST /api/finding/{id}/upstream  {state, tracker, kind, id, url, filed_at, filed_by, refs}
// GET  /api/finding/{id}/upstream
//
// THE RULES ARE ALL IN THE STORE - internal/store/findingupstream.go, which is
// where a filing's number is refused under `referenced` and where filed_at is
// stamped - so this file is argument checking, a view and status codes.
// store.SetFindingUpstream is the only thing in this program that writes one.
//
// WHY THIS EXISTS, AND IT IS THE THIRD TIME THE SAME SENTENCE HAS BEEN WRITTEN
// IN THIS REPO. findingevidence.go's head comment says it about the axis beside
// this one, and api_mergegate.go says it about the gate: a door only agents can
// knock on is half a door, and the half that is missing is the one a person
// uses. Measured again rather than argued - the operator, in the console, could
// read "upstream: unfiled" on every finding and had no way to change it, while
// any seat with an MCP connection could. The old python console has that as a
// button with the issue number beside it and "unmark to reopen".
//
// It is the same write as finding_upstream, deliberately: one store call, one
// set of refusals. Two implementations of "record where this went" would drift,
// and the thing they would drift about is whether a number under `referenced`
// counts as having filed it.
//
// A REFUSAL HERE IS ABOUT THE FINDING OR ABOUT THE FILING. An id the caller
// cannot read is answered as an id that is not there, which is every finding
// door's answer, and writeFindingError beside it already maps those.

import (
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// upstreamRequest is one filing as it arrives on the wire.
//
// STRICT decoding, evidenceRequest's reason: a misspelt field is a 400 naming it
// rather than a value dropped on the floor. Here that matters most for `state` -
// a filing that silently lost its state would leave the row saying whatever it
// said before, and the caller would be told it worked.
//
// Refs is a POINTER so that absent and empty are different calls. Nil leaves the
// citations alone; a stated list replaces them whole, which is the store's own
// rule for a citation list being a fact about what the finding cites NOW. An
// ordinary slice cannot say "leave them", and a caller changing only the state
// would silently clear every citation on the row.
type upstreamRequest struct {
	State   string               `json:"state"`
	Tracker string               `json:"tracker"`
	Kind    string               `json:"kind"`
	ID      string               `json:"id"`
	URL     string               `json:"url"`
	FiledAt string               `json:"filed_at"`
	FiledBy string               `json:"filed_by"`
	Refs    *[]store.UpstreamRef `json:"refs"`
}

// upstreamView is one finding's filing as both doors hand it back: the row, the
// filing standing on it read through the store's own reader, and the log.
//
// viewEvidence's shape, for its reason: a client that called THIS door asked
// about the filing and should not have to know which fields carry it, and both
// halves come out of one read so they cannot disagree.
type upstreamView struct {
	Item     *store.Artifact       `json:"item"`
	Upstream store.UpstreamFiling  `json:"upstream"`
	Log      []store.UpstreamEntry `json:"log"`
}

// viewUpstream assembles it. The log is never null even when nothing has been
// filed: distinguishing "never filed" from "this endpoint does not carry a log"
// off a null is the ambiguity the shape exists to remove.
func viewUpstream(art *store.Artifact, log []store.UpstreamEntry) *upstreamView {
	if log == nil {
		log = []store.UpstreamEntry{}
	}
	return &upstreamView{Item: art, Upstream: store.FindingUpstreamOf(art), Log: log}
}

// handleFindingUpstream records where a finding went upstream.
//
// POST /api/finding/{id}/upstream
//
// Read permission is the bar, which is what the evidence axis, the run log and
// the repro tree already run on: somebody who filed the issue but could not say
// so would have to ask the finding's author to say it for them.
func (s *server) handleFindingUpstream(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req upstreamRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	asked := store.UpstreamFiling{
		State:   req.State,
		Tracker: req.Tracker,
		Kind:    req.Kind,
		ID:      req.ID,
		URL:     req.URL,
		FiledAt: req.FiledAt,
		FiledBy: req.FiledBy,
	}
	if req.Refs != nil {
		asked.Refs = *req.Refs
	}
	art, _, err := s.db.SetFindingUpstream(r.Context(), p, r.PathValue("id"), asked)
	if err != nil {
		s.writeFindingError(w, r, err, r.PathValue("id"))
		return
	}
	// Read back AFTER the write so the answer carries the entry just made: a
	// caller that filed something and got a log without it would have to ask
	// again to see its own write.
	log, err := s.db.FindingUpstreamLog(r.Context(), p, art.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewUpstream(art, log))
}

// handleFindingUpstreamLog reads it back: the filing standing on the row and
// every filing ever recorded for it.
//
// GET /api/finding/{id}/upstream
func (s *server) handleFindingUpstreamLog(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	id := r.PathValue("id")
	art, err := s.db.ReadArtifact(r.Context(), p, id, false)
	if err != nil {
		s.writeFindingError(w, r, err, id)
		return
	}
	if art.Type != "finding" {
		s.writeFindingError(w, r, store.NotAFindingError{ID: id}, id)
		return
	}
	log, err := s.db.FindingUpstreamLog(r.Context(), p, id)
	if err != nil {
		s.writeFindingError(w, r, err, id)
		return
	}
	writeJSON(w, http.StatusOK, viewUpstream(art, log))
}
