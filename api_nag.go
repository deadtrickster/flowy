package main

// WHAT AN IDLE AGENT SHOULD KNOW, computed where the rows are.
//
//	GET /api/nag
//
// The operator, 2026-08-18: "please move the logic of the work nagger to the go
// side and the nagger then will be a simple http call. mind the right tokens.
// And add a new probe - the task distribution - you dont hold it yourself."
//
// WHY IT MOVED. scripts/board-nag.sh pulled 200 rows, decided in jq what counts
// as work, computed staleness against a threshold it carried, and shouted into
// one session. Four seats each held a copy of those rules, and they had already
// disagreed twice today about what `active` means. The rows are here; the
// decisions belong here with them.
//
// MIND THE RIGHT TOKENS, in the operator's words, and it is the only sharp edge
// in this file: every count is computed for THE CALLER. "mine" is the caller's
// rows, and a reader who cannot see a row does not see it in any total. There is
// no name parameter, deliberately - a door that answered about somebody else
// would be a door that reports on a seat that cannot see the same board.
//
// THE PROBE IS NOT HELD BY THE ONE IT MEASURES. That is the operator's framing
// and it is the design: the seat carrying most of the board is the seat least
// able to notice, and at the moment they asked, orchestrator held 20 of 34 open
// rows with nobody in a position to say so. store.WorkloadOf does the
// arithmetic and this hands it back whole - the thresholds, the shares and the
// verdict - so that a console, a nag and a person all read one answer.

import (
	"net/http"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// nagView is everything the nag used to compute for itself.
type nagView struct {
	// Mine, Unowned and Open are the caller's view of the board: what they are
	// carrying, what nobody is, and how much is open at all.
	Mine    int `json:"mine"`
	Unowned int `json:"unowned"`
	Open    int `json:"open"`
	// Stale is rows the caller holds as `active` that nothing has written to
	// for a while. It reports what was SEEN, never what it means - a session
	// forty minutes into a gate looks exactly like an abandoned claim from
	// here, which is why the field is a count and the sentence is the reader's.
	Stale      int `json:"stale"`
	StaleAfter int `json:"stale_after_seconds"`
	// Workload is the distribution probe, whole, including its thresholds so
	// that nobody re-derives them from the shares.
	Workload store.Workload `json:"workload"`
}

// handleNag answers the whole nag in one read.
func (s *server) handleNag(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	// The caller's own board. ScopeAll only when the caller is the operator and
	// asked for it, exactly as every other list door decides it.
	rows, err := s.db.ListArtifacts(r.Context(), p, store.ArtifactQuery{
		Type: store.MemoryType, Kind: "todo", ScopeAll: scopeAll(r, p), Limit: 500,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	view := nagView{StaleAfter: int(nagStaleAfter.Seconds())}
	me := s.db.SeatHandle(r.Context(), p)
	now := time.Now()
	for _, a := range rows {
		if store.DoneAt(a) {
			continue
		}
		view.Open++
		who := store.AssigneeOf(a)
		switch {
		case who == "" || store.NobodyName(who):
			view.Unowned++
		case who == me:
			view.Mine++
			// ACTIVE IS A CLAIM, NOT AN OBSERVATION - the operator's own
			// complaint, and the reason this counts rather than concludes.
			if a.Status == "active" && now.Sub(a.Updated) > nagStaleAfter {
				view.Stale++
			}
		}
	}
	view.Workload = store.WorkloadOf(rows)

	writeJSON(w, http.StatusOK, view)
}

// nagStaleAfter is how long a row this seat holds as `active` may go without a
// write before the nag counts it.
//
// Twenty minutes, which is the operator's own number: "set `since` threshold to
// say 20 minutes". Generous on purpose - `updated` moves on ANY write, so a
// session forty minutes into a gate with nothing to record is indistinguishable
// from an abandoned claim, and the count says what was seen rather than what it
// means.
const nagStaleAfter = 20 * time.Minute
