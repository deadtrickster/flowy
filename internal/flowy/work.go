package flowy

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
		s.writeWorkError(w, r, err)
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
		s.writeWorkError(w, r, err)
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
		s.writeWorkError(w, r, err)
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
func (s *server) writeWorkError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		taken store.ErrTakenBy
		bound store.ErrBoundElsewhere
	)
	// Both conflicts carry the row explaining them when one exists - see
	// knownissue.go. "A claim lost to a compare-and-set" is on the short list of
	// refusals this room has already explained in chat and will explain again,
	// and the person it happens to is never the person who wrote the row.
	switch {
	case errors.As(err, &taken):
		s.writeRefusal(w, r, http.StatusConflict, err, taken.Error())
	case errors.As(err, &bound):
		s.writeRefusal(w, r, http.StatusConflict, err, bound.Error())
	default:
		// Every route through here names one item and names it in the path, so
		// a 404 can say what the id turned out to be instead. This is the door a
		// claim was lost at on 2026-08-18: the id came out of a room message,
		// named the conversation rather than the row, and the bare 404 read as
		// "that todo is gone" to the agent that had just been told about it.
		s.writeQueueErrorFor(w, r, err, r.PathValue("id"))
	}
}
