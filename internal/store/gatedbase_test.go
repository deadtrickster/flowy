package store

import (
	"errors"
	"testing"
)

// gatedRow reuses mergequeue_test.go's builder so these cases and the ones
// above it cannot drift apart over what a merge row is.
func gatedRow(t *testing.T, fields map[string]any) *Artifact {
	t.Helper()
	f := map[string]any{BranchField: "feature", TargetField: "master"}
	for k, v := range fields {
		f[k] = v
	}
	return mergeItem(t, "01HMERGE", f)
}

// The case that made the field necessary. A fast-forward gates the BRANCH tip,
// which contains master and therefore differs from it, so the old rule refused
// every pending item and admitted only ones that had already landed.
func TestAdmissibleJudgesTheBaseNotTheTip(t *testing.T) {
	a := gatedRow(t, map[string]any{
		GatedTipField:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		GatedBaseField: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err := MergeAdmissible(a, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("gated from the tip master is on, so it may land: %v", err)
	}
}

// And the fact the field exists to catch: the target moved after the run began.
func TestAdmissibleRefusesWhenTheTargetMovedUnderTheGate(t *testing.T) {
	a := gatedRow(t, map[string]any{
		GatedTipField:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		GatedBaseField: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	err := MergeAdmissible(a, "cccccccccccccccccccccccccccccccccccccccc")
	if err == nil {
		t.Fatal("the target moved to c after the gate ran from a - that must refuse")
	}
	var refused *ErrMergeNotAdmissible
	if !errors.As(err, &refused) || refused.Code != RefusalMergeStaleGate {
		t.Fatalf("wrong refusal: %v", err)
	}
}

// A row written before the field is judged the old way. Refusing it for
// lacking a field nobody could have written would turn a migration into an
// outage on every row already in the queue.
func TestAdmissibleFallsBackWhenNoBaseWasRecorded(t *testing.T) {
	a := gatedRow(t, map[string]any{
		GatedTipField: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err := MergeAdmissible(a, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("old row, old rule, gated tip equals target tip: %v", err)
	}
	if err := MergeAdmissible(a, "cccccccccccccccccccccccccccccccccccccccc"); err == nil {
		t.Fatal("old row, old rule, gated tip differs - still refused")
	}
}

// Ungated stays ungated. A base without a verdict is not a verdict, and this
// is the check that stops the new field becoming a way to look measured.
func TestBaseAloneIsNotAGate(t *testing.T) {
	a := gatedRow(t, map[string]any{
		GatedBaseField: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	err := MergeAdmissible(a, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil {
		t.Fatal("no gated tip means nothing measured it, base or no base")
	}
	var refused *ErrMergeNotAdmissible
	if !errors.As(err, &refused) || refused.Code != RefusalMergeUngated {
		t.Fatalf("wrong refusal: %v", err)
	}
}
