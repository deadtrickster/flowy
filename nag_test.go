package main

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// TestTheNagSaysWhichLineWasCrossed.
//
// The verdict is one word and the two that matter mean different things to do:
// `check` is look at this, `rebalance` is hand some back. A line that printed
// the word and not the number it crossed would leave a reader doing the
// arithmetic this verb exists to stop them doing.
func TestTheNagSaysWhichLineWasCrossed(t *testing.T) {
	for _, c := range []struct {
		name    string
		view    nagView
		wants   []string
		unwants []string
	}{{
		name: "rebalance names the eighty line and says what to do",
		view: nagView{Open: 6, Workload: store.Workload{
			Open: 6, Verdict: "rebalance", Top: "orchestrator", TopShare: 0.83,
			Check: store.WorkloadCheck, Rebalance: store.WorkloadRebalance,
			Shares: []store.WorkloadShare{{Assignee: "orchestrator", Open: 5, Share: 0.83}},
		}},
		wants:   []string{"rebalance", "orchestrator", "83%", "80%", "hand some back"},
		unwants: []string{"50%"},
	}, {
		name: "check names the fifty line and does not tell anybody to hand work back",
		view: nagView{Open: 4, Workload: store.Workload{
			Open: 4, Verdict: "check", Top: "flowy-claude", TopShare: 0.75,
			Check: store.WorkloadCheck, Rebalance: store.WorkloadRebalance,
			Shares: []store.WorkloadShare{{Assignee: "flowy-claude", Open: 3, Share: 0.75}},
		}},
		wants:   []string{"check", "75%", "50%"},
		unwants: []string{"hand some back", "80%"},
	}, {
		// A lone carrier's 100% is the only number they can have, so the line
		// says so rather than reporting a share as if it were a finding.
		name: "alone explains itself rather than quoting a share",
		view: nagView{Open: 2, Workload: store.Workload{
			Open: 2, Verdict: "alone", Top: "flowy-glm", TopShare: 1,
			Check: store.WorkloadCheck, Rebalance: store.WorkloadRebalance,
			Shares: []store.WorkloadShare{{Assignee: "flowy-glm", Open: 2, Share: 1}},
		}},
		wants:   []string{"alone", "says nothing"},
		unwants: []string{"hand some back", "over the"},
	}} {
		t.Run(c.name, func(t *testing.T) {
			out := nagLines(c.view)
			for _, want := range c.wants {
				if !strings.Contains(out, want) {
					t.Errorf("the nag prints %q, which does not say %q", out, want)
				}
			}
			for _, no := range c.unwants {
				if strings.Contains(out, no) {
					t.Errorf("the nag prints %q, which says %q and should not", out, no)
				}
			}
		})
	}
}

// TestTheNagCountsAreTheCallersOwn, drawn before the spread because they are
// what an idle seat acts on: what nobody has taken, and what it is holding and
// has not started.
func TestTheNagCountsAreTheCallersOwn(t *testing.T) {
	out := nagLines(nagView{Open: 11, Unowned: 1, Mine: 3, MineTodo: 2, Stale: 1})
	for _, want := range []string{"open 11", "unowned 1", "mine 3", "todo 2", "stale 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the board line %q does not carry %q", out, want)
		}
	}
	// A board nobody is carrying says so rather than printing an empty table.
	if !strings.Contains(out, "nobody is carrying anything") {
		t.Errorf("with no shares the nag prints %q", out)
	}
	// AND IT SAYS NOTHING ABOUT BLOCKED ROWS WHEN THERE ARE NONE. This is the
	// half that makes the arm below a difference rather than a claim: a line
	// that printed unconditionally would be found by any assertion that looks
	// for it, on any board.
	for _, no := range []string{"blocked", "answers owed"} {
		if strings.Contains(out, no) {
			t.Errorf("a board with nothing blocked still prints %q:\n%s", no, out)
		}
	}
}

// TestTheNagTellsBlockedApartFromWaiting.
//
// The defect, measured on a real board: "3 row(s) assigned to orchestrator, all
// open", where all three were questions for the operator and none was work it
// could do. A seat blocked on somebody else and a seat sitting on its work
// produced the same number, so the number was evidence for neither.
//
// The two words are deliberately not the same word. "blocked" is work this seat
// holds and cannot move; "answers owed by you" is what other people want FROM
// it, which is not work and must not be counted as any - handing a row over to
// ask a question is exactly how four rows landed on the operator looking like
// their job.
func TestTheNagTellsBlockedApartFromWaiting(t *testing.T) {
	out := nagLines(nagView{
		Open: 11, Unowned: 1, Mine: 3, MineTodo: 1, MineWaiting: 2, AnswersOwed: 4,
		MineWaitingIDs: []string{"01BLOCKEDA", "01BLOCKEDB"},
		AnswersOwedIDs: []string{"01OWEDA", "01OWEDB", "01OWEDC", "01OWEDD"},
	})
	for _, want := range []string{"blocked 2", "answers owed by you 4", "todo 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the nag prints %q, which does not say %q", out, want)
		}
	}
	// THE OWED IDS ARE NAMED. Those rows are somebody else's, on a board this
	// seat has no reason to have read, and no listing finds them - the number
	// alone cannot be acted on.
	for _, want := range []string{"01OWEDA", "01OWEDD"} {
		if !strings.Contains(out, want) {
			t.Errorf("the nag says four answers are owed and does not say which:\n%s", out)
		}
	}
	// AND THE BLOCKED ONES ARE NOT, which is the arm that makes the line above
	// a decision rather than a coincidence: those rows are this seat's own, so
	// `flowy todo` lists them and repeating the ids here is noise on every tick.
	for _, no := range []string{"01BLOCKEDA", "01BLOCKEDB"} {
		if strings.Contains(out, no) {
			t.Errorf("the nag lists a blocked row's id, which the caller can already list:\n%s", out)
		}
	}
}

// TestTheNagNamesQuietReadersWithoutTheirDurations.
//
// A seat with a share and no reader is holding work nothing can reach, so the
// names belong on this answer. The SECONDS do not: nagCursor drops them
// deliberately so that a reader which is still quiet does not look like news on
// every poll, and printing them here would put a number on the screen that
// changes every tick and says nothing new - the same wake-every-tick the cursor
// exists to prevent, one surface out.
func TestTheNagNamesQuietReadersWithoutTheirDurations(t *testing.T) {
	out := nagLines(nagView{
		Open: 2,
		Quiet: []store.QuietReader{
			{Reader: "flowy-glm", Silent: 4211},
			{Reader: "claude-host", Silent: 90, Kind: "forked"},
		},
	})
	for _, want := range []string{"quiet", "flowy-glm", "claude-host", "(forked)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the nag prints %q, which does not say %q", out, want)
		}
	}
	// The durations are the arm that matters.
	for _, no := range []string{"4211", "90 ", "seconds", "ago"} {
		if strings.Contains(out, no) {
			t.Errorf("the nag prints %q, which carries the duration %q and should not", out, no)
		}
	}
	// And a fleet where everybody is listening says nothing at all rather than
	// an empty heading - absent is the honest answer, as it is on the door.
	quiet := nagLines(nagView{Open: 2})
	if strings.Contains(quiet, "quiet") {
		t.Errorf("with nobody quiet the nag still prints %q", quiet)
	}
}
