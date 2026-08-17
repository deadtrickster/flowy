package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// These are pure and need no database, which is the point tonight: the VM gate
// is not trustworthy while a shared layer is being corrupted underneath it, and
// a rule about what may land must not itself land on an untrustworthy verdict.
//
// What they assert is the one opinion the queue has - a branch lands only on the
// tip its gate actually measured - plus the shape of the refusal, because a
// refusal nobody can act on is the failure mode this whole system keeps hitting.

func mergeItem(t *testing.T, id string, fields map[string]any) *Artifact {
	t.Helper()
	a := &Artifact{ID: id, Kind: MergeKind}
	if fields != nil {
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("marshal fields: %v", err)
		}
		a.Fields = raw
	}
	return a
}

func TestMergeAdmissibleOnTheTipItWasGatedOn(t *testing.T) {
	a := mergeItem(t, "01MERGE", map[string]any{
		BranchField:   "land/todo-category",
		GatedTipField: "b48e2af",
		GateRunField:  "4b933f502fbd",
	})
	if err := MergeAdmissible(a, "b48e2af"); err != nil {
		t.Fatalf("a branch gated on the tip it lands on must be admissible, got: %v", err)
	}
	// The reading is of the same commit, not of the same typing of it. A tip
	// copied out of one tool upper-cased and out of another lower-cased is one
	// tip, and refusing that pair would train everyone to bypass the queue.
	if err := MergeAdmissible(a, "  B48E2AF "); err != nil {
		t.Fatalf("the same commit written differently is the same commit, got: %v", err)
	}
}

func TestMergeRefusedWhenTheTargetMovedUnderIt(t *testing.T) {
	a := mergeItem(t, "01MERGE", map[string]any{
		BranchField:   "land/todo-status-default",
		GatedTipField: "b48e2af",
	})
	err := MergeAdmissible(a, "cfa290d")
	if err == nil {
		t.Fatal("a branch gated on a tip that has moved must be refused - this is the whole queue")
	}
	var refusal *ErrMergeNotAdmissible
	if !errors.As(err, &refusal) {
		t.Fatalf("want a typed refusal a caller can branch on, got %T", err)
	}
	// The refusal has to carry BOTH tips. "Re-gate it" is not an instruction
	// until the reader knows what to re-gate against, and every time today a
	// refusal left that out somebody went and measured the wrong thing again.
	msg := err.Error()
	for _, want := range []string{"b48e2af", "cfa290d", "land/todo-status-default", "master"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must name %q so it can be acted on, got: %s", want, msg)
		}
	}
}

func TestMergeRefusedWhenNothingGatedItAtAll(t *testing.T) {
	a := mergeItem(t, "01MERGE", map[string]any{BranchField: "land/hopeful"})
	err := MergeAdmissible(a, "cfa290d")
	if err == nil {
		t.Fatal("an ungated branch must be refused as loudly as a stale one")
	}
	if !strings.Contains(err.Error(), "no gate has measured it") {
		t.Errorf("the refusal must say the evidence is absent rather than stale, got: %s", err)
	}
}

// A comparison against an unstated tip always passes, which would turn the
// queue into a rubber stamp exactly when the caller is broken. It refuses.
func TestMergeRefusedWhenTheTargetTipIsUnstated(t *testing.T) {
	a := mergeItem(t, "01MERGE", map[string]any{
		BranchField:   "land/whatever",
		GatedTipField: "b48e2af",
	})
	if err := MergeAdmissible(a, "   "); err == nil {
		t.Fatal("an unstated target tip must refuse, not pass by comparing against nothing")
	}
}

func TestOnlyAMergeItemGoesThroughTheMergeDoor(t *testing.T) {
	todo := &Artifact{ID: "01TODO", Kind: "todo"}
	if err := MergeAdmissible(todo, "cfa290d"); err == nil {
		t.Fatal("a todo is not a merge request and must not be admissible through this door")
	}
	if err := MergeAdmissible(nil, "cfa290d"); err == nil {
		t.Fatal("nothing at all must refuse rather than panic")
	}
}

// The target defaults rather than reading empty: every merge lands somewhere,
// and a caller that has to remember the default is a caller that will forget it.
func TestTargetDefaultsToMasterAndFieldsReadBack(t *testing.T) {
	a := mergeItem(t, "01MERGE", map[string]any{
		BranchField:   " land/spaced ",
		GatedTipField: " B48E2AF ",
		GateRunField:  " 4b933f502fbd ",
	})
	if got := TargetOf(a); got != DefaultMergeTarget {
		t.Errorf("an unstated target is %q, got %q", DefaultMergeTarget, got)
	}
	if got := BranchOf(a); got != "land/spaced" {
		t.Errorf("branch reads back trimmed, got %q", got)
	}
	if got := GatedTipOf(a); got != "b48e2af" {
		t.Errorf("the gated tip normalizes, got %q", got)
	}
	if got := GateRunOf(a); got != "4b933f502fbd" {
		t.Errorf("the gate run reads back so a green claim points at its log, got %q", got)
	}
	stated := mergeItem(t, "01MERGE2", map[string]any{TargetField: "release-0.8"})
	if got := TargetOf(stated); got != "release-0.8" {
		t.Errorf("a stated target stands, got %q", got)
	}
}

// A field holding something that is not a string is a malformed item, and the
// answer that makes admission REFUSE is the safe one - reading it as an empty
// gated tip means "nothing measured it", which is exactly right.
func TestAMalformedFieldRefusesRatherThanProceeds(t *testing.T) {
	a := mergeItem(t, "01MERGE", map[string]any{
		BranchField:   "land/odd",
		GatedTipField: 42,
	})
	if err := MergeAdmissible(a, "cfa290d"); err == nil {
		t.Fatal("a gated tip that is not a string must refuse")
	}
}

// Short sha against full sha. Two green branches were refused for "measured a
// different tip" because the run recorded 9e31abb from git log --oneline and the
// node read master as 9e31abb4ecd5. Both are correct readings of one commit.
func TestAShortShaAndAFullShaAreTheSameCommit(t *testing.T) {
	a := mergeItem(t, "01MERGE", map[string]any{
		BranchField:   "feat-citation-grants",
		GatedTipField: "9e31abb",
	})
	if err := MergeAdmissible(a, "9e31abb4ecd5f0a1b2c3d4e5f60718293a4b5c6d"); err != nil {
		t.Fatalf("a short sha is the same commit as the full one it prefixes: %v", err)
	}
	// And the other way round, because which side is abbreviated depends on
	// which tool printed it.
	b := mergeItem(t, "01MERGE", map[string]any{
		BranchField:   "feat-citation-grants",
		GatedTipField: "9e31abb4ecd5f0a1b2c3d4e5f60718293a4b5c6d",
	})
	if err := MergeAdmissible(b, "9e31abb"); err != nil {
		t.Fatalf("the abbreviation can be on either side: %v", err)
	}
}

// A prefix match must not become "matches anything". A different commit that
// happens to share a few characters is still a different commit.
func TestAPrefixMatchStillRefusesADifferentCommit(t *testing.T) {
	a := mergeItem(t, "01MERGE", map[string]any{BranchField: "b", GatedTipField: "9e31abb"})
	if err := MergeAdmissible(a, "9e31abc"); err == nil {
		t.Fatal("one character different is a different commit")
	}
	// Too short to be a commit anybody typed on purpose: refused rather than
	// matched against half the repository.
	short := mergeItem(t, "01MERGE", map[string]any{BranchField: "b", GatedTipField: "9e3"})
	if err := MergeAdmissible(short, "9e31abb4ecd5"); err == nil {
		t.Fatal("an abbreviation below git's own floor must not be treated as a prefix")
	}
}
