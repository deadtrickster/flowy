package flowy

// HOW STRONG A FINDING'S EVIDENCE IS, over HTTP.
//
// POST /api/finding/{id}/evidence  {state, verified_on, verified_at, last_run}
// GET  /api/finding/{id}/evidence
//
// THE RULES ARE ALL IN THE STORE - see internal/store/findingevidence.go, which
// is where `verified` requires a commit and why - so this file is argument
// checking, a view and status codes. store.SetFindingEvidence is the only thing
// in this program that writes one.
//
// WHY THIS EXISTS AT ALL, WHICH IS THE PART WORTH READING. The filing axis
// beside it shipped MCP-only, and api_mergegate.go's head comment is the same
// complaint made twice in one evening: a door only agents can knock on is half a
// door, and the half that is missing is the one a person uses. It was measured
// again here rather than argued: the backfill this verb was written for could
// not be run at all, because the seat that had to run it reaches this node over
// HTTP and there was no MCP transport to reach. A verb whose only door is one
// the caller cannot open is a verb nobody has.
//
// It is the same write as the tool, deliberately - one store call, one set of
// refusals. Two implementations of "record the evidence" would drift, and the
// thing they would drift about is whether a commit was required.
//
// A REFUSAL HERE IS ABOUT THE FINDING OR ABOUT THE CLAIM, and nothing else. An
// id the caller cannot read is answered as an id that is not there, which is
// every finding door's answer. A verified claim with no commit, an empty state
// and a projectless finding are 400 and say why.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// evidenceRequest is one evidence claim as it arrives on the wire.
//
// A named type rather than an anonymous struct for mergeGateRequest's reason:
// the shape this door accepts can then be refused in a test that needs no
// database, and STRICT decoding means a misspelt `verified_sha` is a 400 naming
// it rather than a value dropped on the floor - which here would be `verified`
// silently losing the commit that is the whole content of the word.
type evidenceRequest struct {
	State      string `json:"state"`
	VerifiedOn string `json:"verified_on"`
	VerifiedAt string `json:"verified_at"`
	LastRun    string `json:"last_run"`
}

// evidenceView is one finding's evidence as both doors here hand it back: the
// row, the claim standing on it read back through the store's own reader, and
// the log behind it.
//
// The claim is a second copy of keys already in the row's fields, deliberately
// and for viewNotes' reason: a client that called THIS door asked about the
// evidence and should not have to know which keys carry it, and a client reading
// the row gets it without knowing this door exists. Both come out of one read,
// so they cannot disagree.
type evidenceView struct {
	Item     *store.Artifact       `json:"item"`
	Evidence store.Evidence        `json:"evidence"`
	Log      []store.EvidenceEntry `json:"log"`
}

// viewEvidence assembles it. The log is never null, even when nothing has been
// claimed: a client distinguishing "no claims" from "this endpoint does not
// carry them" off a null is the ambiguity the shape exists to remove.
func viewEvidence(art *store.Artifact, log []store.EvidenceEntry) *evidenceView {
	if log == nil {
		log = []store.EvidenceEntry{}
	}
	return &evidenceView{Item: art, Evidence: store.FindingEvidenceOf(art), Log: log}
}

// handleFindingEvidence records how strong a finding's evidence is.
//
// POST /api/finding/{id}/evidence
//
// Read permission is the whole bar, which is what the filing axis, the run log
// and the repro tree already run on: a finding is collaborative exactly the way
// a bug is, and somebody who ran the reproduction but could not say so would
// have to ask its author to say it for them.
func (s *server) handleFindingEvidence(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req evidenceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	art, _, err := s.db.SetFindingEvidence(r.Context(), p, r.PathValue("id"), store.Evidence{
		State:      req.State,
		VerifiedOn: req.VerifiedOn,
		VerifiedAt: req.VerifiedAt,
		LastRun:    req.LastRun,
	})
	if err != nil {
		s.writeFindingError(w, r, err, r.PathValue("id"))
		return
	}
	// The log is read back after the write so that the answer carries the entry
	// just made: a caller that recorded a claim and got back a log without it
	// would have to ask again to see its own write.
	log, err := s.db.FindingEvidenceLog(r.Context(), p, art.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewEvidence(art, log))
}

// handleFindingEvidenceLog reads it back: the claim standing on the row and
// every claim ever made about it, oldest first.
//
// GET /api/finding/{id}/evidence
func (s *server) handleFindingEvidenceLog(w http.ResponseWriter, r *http.Request) {
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
	log, err := s.db.FindingEvidenceLog(r.Context(), p, id)
	if err != nil {
		s.writeFindingError(w, r, err, id)
		return
	}
	writeJSON(w, http.StatusOK, viewEvidence(art, log))
}

// writeFindingError is writeQueueErrorFor for the other id namespace: an id that
// is not a finding this principal may read is a 404 that says SO, rather than
// the "no such todo" the queue's mapper would answer with. A caller pasting a
// finding id and being told there is no such todo goes looking in the wrong
// space, which is the reason NotATodoError and NotAFindingError are two types.
func (s *server) writeFindingError(w http.ResponseWriter, r *http.Request, err error, id string) {
	var (
		notAFinding store.NotAFindingError
		refusal     store.DepRefusal
	)
	switch {
	case errors.As(err, &notAFinding):
		writeJSON(w, http.StatusNotFound,
			errorBody(notAFinding.Error()+s.misreadIDNote(r, notAFinding.ID)))
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody("no such finding"+s.misreadIDNote(r, id)))
	case errors.As(err, &refusal):
		s.writeRefusal(w, r, http.StatusBadRequest, err, refusal.Error())
	default:
		serverError(w, r, err)
	}
}
