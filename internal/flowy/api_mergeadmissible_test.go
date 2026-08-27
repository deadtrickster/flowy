package flowy

import (
	"strings"
	"testing"
	"time"
)

// THE SENTENCE IS THE PRODUCT. A caller reads one line and does one thing, so
// the order the refusals are asked in IS the behaviour - and two of the four
// orderings would tell an agent to spend a gate run it should not spend.
//
// Pure: a function of the answer, which is what makes it worth having as a
// function at all.
func TestTheReasonSaysTheThingThatBlocksYouFirst(t *testing.T) {
	until := time.Date(2026, 8, 19, 20, 35, 0, 0, time.UTC)
	yes, no := true, false

	base := func() mergeAdmissibleAnswer {
		return mergeAdmissibleAnswer{
			Target: "master", TargetTip: "abc1234", TipFrom: "landed",
			Lock:       &mergeQueueLock{},
			Declarable: true,
			Item:       mergeQueueItem{ID: "01ROW", Admissible: &yes},
		}
	}

	if got := admissibleWhy(base()); !strings.Contains(got, "may land on abc1234") {
		t.Errorf("a green admissible row said %q", got)
	}

	// A HELD TARGET SAYS WAIT, and it says it EVEN WHEN THE EVIDENCE IS ALSO
	// STALE. Re-gating under somebody else's lock spends a run measuring from a
	// base they are about to move - which is the six-wasted-runs night this
	// lock was built after.
	stale := base()
	stale.Declarable = false
	stale.Item.Admissible = &no
	stale.Item.Reason = "the target moved after its gate ran - it measured from 3ae350c"
	stale.Lock = &mergeQueueLock{Held: true, Holder: "u-2", HolderName: "orchestrator", Until: until}
	got := admissibleWhy(stale)
	if !strings.Contains(got, "held by orchestrator") || !strings.Contains(got, "do not re-gate") {
		t.Errorf("a row under somebody else's lock said %q - it must say wait", got)
	}
	if strings.Contains(got, "measured from 3ae350c") {
		t.Error("it led with the stale gate while the target was held by somebody else, " +
			"which sends a run to measure from a base that is about to move")
	}

	// HELD BY ANOTHER SESSION OF MINE IS ITS OWN SENTENCE. "master is held by
	// claude-host" in front of a claude-host session reads as permission, and
	// acting on that reading is how a sibling session landed through a lock it
	// never took.
	sibling := stale
	sibling.LockIsMine = true
	sibling.Lock = &mergeQueueLock{
		Held: true, Holder: "u-1", HolderName: "claude-host", Item: "01OTHER", Until: until,
	}
	got = admissibleWhy(sibling)
	if !strings.Contains(got, "another session of YOURS") || !strings.Contains(got, "01OTHER") {
		t.Errorf("a lock held by my own other session for another row said %q - a reader "+
			"seeing their own name reads it as permission", got)
	}

	// A RUN IN FLIGHT SAYS SO rather than inviting a second one on the same
	// tree. It is below the lock because a held target is the stronger fact.
	gating := base()
	gating.Item.Gating = true
	gating.Item.GateRun = "drain-20260819T124754Z"
	if got := admissibleWhy(gating); !strings.Contains(got, "drain-20260819T124754Z") {
		t.Errorf("a branch already being measured said %q", got)
	}

	// AND A NODE THAT DOES NOT KNOW WHERE THE TARGET IS SAYS THAT FIRST OF ALL,
	// because every other sentence here would be an opinion about a comparison
	// that never happened.
	blind := base()
	blind.TipFrom = "none"
	blind.TargetTip = ""
	blind.Declarable = false
	blind.Lock = &mergeQueueLock{Held: true, Holder: "u-2", Until: until}
	if got := admissibleWhy(blind); !strings.Contains(got, "?target_tip=") {
		t.Errorf("a node with no idea where master is said %q", got)
	}
}
