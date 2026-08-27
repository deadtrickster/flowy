package flowy

// A FINDING'S RUN HISTORY, over HTTP.
//
//	GET /api/finding/{id}/runs
//
// THE RULES ARE ALL IN THE STORE - see internal/store/findingruns.go, which is
// where "a verdict is an event, not a field" is decided and why - so this file
// is a view and status codes.
//
// WHY IT EXISTS, measured rather than argued. On 2026-08-18 the repro path ran
// end to end for the first time: release 26.07.5 resolved, packaged, built its
// dind image, reproduced the finding and exited 0, and the runner recorded the
// verdict. The record was correct and no browser could reach it. store.
// FindingRuns had exactly two callers - its own test, and the MCP tool
// finding_run_list - and the console speaks HTTP, not MCP. So the one surface
// whose entire job is to show that a run happened was the one surface that
// could not ask.
//
// That is findingevidence.go's complaint made a third time, in its own words: a
// door only agents can knock on is half a door, and the half that is missing is
// the one a person uses.
//
// THE HISTORY, NOT THE LATEST. Oldest first, every entry, no paging - the store
// returns it that way deliberately, because red-then-green across reruns of the
// same version is the fact the log exists to keep. A door that answered with the
// most recent verdict would be a field with extra steps, and the field is what
// this log was written to replace.

import (
	"net/http"
)

// runsView is a finding's run log as the HTTP door hands it back: the finding it
// is about, how many entries there are, and the entries themselves.
//
// count is not len(runs) for the caller's convenience - it is there so a reader
// scanning a log of answers can tell "this finding has never been run" from "the
// call failed and something wrote an empty list", which are the same two lines
// of JSON apart from this one.
type runsView struct {
	Finding string `json:"finding"`
	Count   int    `json:"count"`
	Runs    any    `json:"runs"`
}

// handleFindingRuns hands back every run recorded against one finding.
//
// GET /api/finding/{id}/runs
//
// Read permission is the whole bar, the same as the evidence log beside it: the
// store filters the log by what this principal may read, and a finding they
// cannot read answers as an id that is not there.
func (s *server) handleFindingRuns(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	id := r.PathValue("id")

	// The store's own first move is readFinding, so an id that is not a finding
	// answers "no such finding" rather than an empty list - which a caller could
	// not tell from a finding that has never been run. Nothing extra is asked
	// here; asking twice would only add a second answer to keep in step.
	runs, err := s.db.FindingRuns(r.Context(), p, id)
	if err != nil {
		s.writeFindingError(w, r, err, id)
		return
	}
	writeJSON(w, http.StatusOK, runsView{Finding: id, Count: len(runs), Runs: runs})
}
