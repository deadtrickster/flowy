package main

// The work queue's doors: take one, put it back, say it is done.
//
// Three POSTs with no body, because THE CLAIM MUST BE CHEAPER THAN THE ACT. A
// claim that costs a JSON document and a second call is one an agent skips, and
// an agent who skips it is behaving sensibly - which is how two of us ran the
// same e2fsck on the same layer within a minute tonight. One call, no ceremony.
//
// The refusals go through writeQueueError, so a contested take is 400 with the
// winner named rather than 500 with a stack: the loser being TOLD they lost, and
// told by whom, is the whole point of the compare-and-set underneath.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

// handleWorkClaim takes a stray item for the caller.
//
// POST /api/work/{id}/claim
func (s *server) handleWorkClaim(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, entry, err := s.db.ClaimWork(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeWorkError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, workAnswer(art, entry))
}

// handleWorkRelease puts a stray item back for anybody to take.
//
// POST /api/work/{id}/release
func (s *server) handleWorkRelease(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, entry, err := s.db.ReleaseWork(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeWorkError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, workAnswer(art, entry))
}

// handleWorkDone records that the thing was done, by whom and when.
//
// POST /api/work/{id}/done
func (s *server) handleWorkDone(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, entry, err := s.db.FinishWork(r.Context(), p, r.PathValue("id"))
	if err != nil {
		writeWorkError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, workAnswer(art, entry))
}

// workAnswer is what every one of the three hands back: the item as it now
// stands, and the entry the move left in the log - or no entry at all when the
// move was a restatement of what was already true.
func workAnswer(art *store.Artifact, entry *store.Event) map[string]any {
	answer := map[string]any{"item": art}
	if entry != nil {
		answer["entry"] = entry
	} else {
		// SAY THAT NOTHING MOVED. A caller re-taking its own claim gets 200 and
		// no entry, and without this it cannot tell that from a fresh win - two
		// different facts one status code cannot carry.
		answer["unchanged"] = true
	}
	return answer
}

// writeWorkError maps this queue's refusals.
//
// A contested take is 409 rather than 400: the request was well formed and the
// caller may make it again later against a different item, which is exactly
// what 409 means and what a retrying client needs to tell apart from "you asked
// wrongly". Everything else goes through the mapping the other queue verbs
// already use.
func writeWorkError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		taken store.ErrTakenBy
		bound store.ErrBoundElsewhere
	)
	switch {
	case errors.As(err, &taken):
		writeJSON(w, http.StatusConflict, errorBody(taken.Error()))
	case errors.As(err, &bound):
		writeJSON(w, http.StatusConflict, errorBody(bound.Error()))
	default:
		writeQueueError(w, r, err)
	}
}
