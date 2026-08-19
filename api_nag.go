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
	"context"
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
	// MineTodo is the caller's rows that have not been started - claimed, or
	// handed to them, and still sitting at todo.
	//
	// It is here because the WAKE CONDITION needed it and the alternative was a
	// fifth copy of the rule. board-nag.sh --watch decided this in jq with its
	// own reading of what `active` means, which is the exact drift this row
	// moved the decisions here to end: a row a seat holds and is working is not
	// work waiting for that seat, and a row it holds and has not started is.
	MineTodo int `json:"mine_todo"`
	// Stale is rows the caller holds as `active` that nothing has written to
	// for a while. It reports what was SEEN, never what it means - a session
	// forty minutes into a gate looks exactly like an abandoned claim from
	// here, which is why the field is a count and the sentence is the reader's.
	Stale      int `json:"stale"`
	StaleAfter int `json:"stale_after_seconds"`
	// Workload is the distribution probe, whole, including its thresholds so
	// that nobody re-derives them from the shares.
	Workload store.Workload `json:"workload"`
	// Quiet is the readers this node has stopped hearing from: attached once,
	// and not polling now.
	//
	// A SEAT CANNOT REPORT ITS OWN DEATH, which is why this is here rather than
	// in each session. Twenty-five times in one session I asked /api/presence
	// whether MY OWN reader was alive, on a timer, and was told yes twenty-five
	// times - and the one answer that would have mattered is the one a dead
	// seat cannot go and fetch. Asking from inside is a check that cannot fire
	// in the case it exists for.
	//
	// So the node says it, on the surface the other seats already read. It
	// cannot wake the seat that died - nothing can, from here - but it can stop
	// that death being invisible to everybody who is still listening, without
	// anybody polling for it.
	//
	// A COUNT AND THE NAMES, because "one reader is quiet" starts an
	// investigation and "orchestrator is quiet" ends it. Absent rather than
	// zero, like every other finding on this node: a reader that never attached
	// is not quiet, it is simply not here, and saying nothing about it is the
	// honest answer.
	Quiet []store.QuietReader `json:"quiet_readers,omitempty"`
	// Changed and Cursor are the wait door's answer only - see api_nagwait.go.
	// A plain GET /api/nag carries neither, and a caller that asked one question
	// should not have to skip a field about a wait it never started.
	Changed *bool  `json:"changed,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
}

// handleNag answers the whole nag in one read.
func (s *server) handleNag(w http.ResponseWriter, r *http.Request) {
	view, err := s.readNag(r.Context(), principalOf(r), scopeAll(r, principalOf(r)))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// readNag is the nag itself: the counts, computed for this principal.
//
// It is a function rather than the body of the door because there are two doors
// onto it - GET /api/nag asks it once, GET /api/nag/wait asks it until the
// answer changes - and two implementations of "what counts as mine" is the
// exact drift this row moved the decision here to end. A door that computed its
// own would be a fifth copy of the rules, after the four bash ones.
func (s *server) readNag(ctx context.Context, p *store.Principal, all bool) (nagView, error) {
	// The caller's own board. ScopeAll only when the caller is the operator and
	// asked for it, exactly as every other list door decides it.
	rows, err := s.db.ListArtifacts(ctx, p, store.ArtifactQuery{
		Type: store.MemoryType, Kind: "todo", ScopeAll: all, Limit: 500,
	})
	if err != nil {
		return nagView{}, err
	}

	view := nagView{StaleAfter: int(nagStaleAfter.Seconds())}
	// Asked of the node rather than computed here, so the rule for "quiet" has
	// one home - the same reason the workload moved out of four bash scripts.
	quiet, err := s.db.QuietReaders(ctx, time.Now())
	if err != nil {
		return nagView{}, err
	}
	view.Quiet = quiet
	me := s.db.SeatHandle(ctx, p)
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
			// NOT STARTED IS NOT THE SAME AS NOT DONE. A row this seat holds
			// and is working is not work waiting for it; one it holds and has
			// not begun is, and that is the difference a waiter has to be able
			// to see or it wakes an agent every tick of its own job.
			if a.Status != "active" {
				view.MineTodo++
			}
			// ACTIVE IS A CLAIM, NOT AN OBSERVATION - the operator's own
			// complaint, and the reason this counts rather than concludes.
			if a.Status == "active" && now.Sub(a.Updated) > nagStaleAfter {
				view.Stale++
			}
		}
	}
	view.Workload = store.WorkloadOf(rows)
	return view, nil
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
