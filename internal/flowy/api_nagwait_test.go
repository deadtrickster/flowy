package flowy

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// The cursor IS what "changed" means to every waiter on this door, so what it
// notices and what it ignores is the contract - and both halves are worth a
// check, because getting either wrong produces a waiter that looks like it
// works.
//
// Pure: the digest is a function of the answer, and a check that needed a node
// and a seeded board to establish this would not be run.
func TestTheNagCursorMovesOnWhatASeatActsOn(t *testing.T) {
	view := func(mutate func(*nagView)) nagView {
		v := nagView{
			Mine: 2, Unowned: 0, Open: 9, Stale: 0, StaleAfter: 1200,
			Workload: store.Workload{
				Verdict: "ok",
				Shares: []store.WorkloadShare{
					{Assignee: "claude-host", Open: 2, Share: 0.22},
					{Assignee: "orchestrator", Open: 3, Share: 0.33},
				},
			},
		}
		if mutate != nil {
			mutate(&v)
		}
		return v
	}

	base, err := nagCursor(view(nil))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}

	// A ROW NOBODY IS ON IS THE WHOLE POINT OF THE DOOR. An idle seat waiting
	// here is waiting for exactly this.
	unowned, err := nagCursor(view(func(v *nagView) { v.Unowned = 1 }))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if unowned == base {
		t.Error("a row went unowned and the cursor did not move - the one thing " +
			"an idle seat is waiting to hear")
	}

	// AND A ROW OF MINE THAT HAS NOT BEEN STARTED. This is the count that used
	// to be decided in jq by every seat's own copy of board-nag.sh, and it is
	// the wake condition an idle agent with an unstarted claim is waiting on.
	mineTodo, err := nagCursor(view(func(v *nagView) { v.MineTodo = 1 }))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if mineTodo == base {
		t.Error("a row this seat holds and has not started appeared, and the cursor did not move")
	}

	// SO IS THIS SEAT'S OWN CLAIM GOING QUIET.
	stale, err := nagCursor(view(func(v *nagView) { v.Stale = 1 }))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if stale == base {
		t.Error("a claim of this seat's went quiet and the cursor did not move")
	}

	// AND THE DISTRIBUTION PROBE CHANGING ITS MIND, which is the operator's
	// protocol: past 80% somebody stops and rebalances, and nobody can act on
	// that if the waiter does not carry it.
	verdict, err := nagCursor(view(func(v *nagView) { v.Workload.Verdict = "rebalance" }))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if verdict == base {
		t.Error("the workload verdict became rebalance and the cursor did not move")
	}

	// A SHARE MOVING IS NOT, ON ITS OWN, A CHANGE. Shares are ratios: one row
	// filed anywhere on the board moves every seat's by a fraction of a
	// percent, and a cursor that carried them would wake every waiter in the
	// fleet for a number none of them act on - the feed that always speaks and
	// is therefore never read.
	drift, err := nagCursor(view(func(v *nagView) {
		v.Workload.Shares = []store.WorkloadShare{
			{Assignee: "claude-host", Open: 2, Share: 0.2181},
			{Assignee: "orchestrator", Open: 3, Share: 0.3312},
		}
	}))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if drift != base {
		t.Error("the cursor moved because two shares drifted a fraction of a " +
			"percent, which nobody acts on")
	}

	// NOR IS THE THRESHOLD THIS NODE COMPILED IN. A waiter that woke when a
	// constant was re-read would be waking on its own arithmetic.
	threshold, err := nagCursor(view(func(v *nagView) { v.StaleAfter = 600 }))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if threshold != base {
		t.Error("the cursor moved on this node's own staleness constant")
	}
}

// A READER GOING QUIET WAKES A WAITER, and its silence growing does not.
//
// Both halves matter and they pull opposite ways. A seat going quiet is
// somebody else's death and the only reader who can act on it is one still
// alive, so it must wake them. But the silence grows every second, so a cursor
// carrying the DURATION would wake every waiter on every poll forever - the
// always-speaking feed nobody reads, which this door was built to avoid.
func TestTheNagCursorWakesOnAQuietReaderButNotOnItsSilenceGrowing(t *testing.T) {
	base := nagView{Mine: 1, Open: 3}
	quiet := base
	quiet.Quiet = []store.QuietReader{{Reader: "orchestrator", Silent: 700}}
	longer := base
	longer.Quiet = []store.QuietReader{{Reader: "orchestrator", Silent: 4000}}
	second := base
	second.Quiet = []store.QuietReader{
		{Reader: "claude-host", Silent: 640},
		{Reader: "orchestrator", Silent: 700},
	}

	was, err := nagCursor(base)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	went, err := nagCursor(quiet)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if went == was {
		t.Fatal("a reader went quiet and no waiter would wake")
	}
	grew, err := nagCursor(longer)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if grew != went {
		t.Fatal("the same reader being quiet for longer moved the cursor - every waiter " +
			"wakes on every poll, which is the feed nobody reads")
	}
	also, err := nagCursor(second)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if also == went {
		t.Fatal("a SECOND reader went quiet and no waiter would wake")
	}
}
