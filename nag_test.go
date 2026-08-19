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
