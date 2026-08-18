package main

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// Filing a merge request, and the two ways of filing one that must not quietly
// half-work. Pure: no database, no VM, which is what makes them worth anything
// on a night when the gate cannot be trusted.

func TestAMergeRequestCarriesItsBranchAndVerdict(t *testing.T) {
	art := &store.Artifact{Kind: store.MergeKind}
	var fields map[string]any
	err := mergeFields(art, &fields, memWriteArgs{
		Branch:   " land/merge-queue ",
		GatedTip: " 1710323 ",
		GateRun:  "2e697626e50d",
	})
	if err != nil {
		t.Fatalf("a merge request naming a branch is filable: %v", err)
	}
	if fields[store.BranchField] != "land/merge-queue" {
		t.Errorf("branch stored trimmed, got %v", fields[store.BranchField])
	}
	if fields[store.GatedTipField] != "1710323" {
		t.Errorf("the gated tip is what admission compares, got %v", fields[store.GatedTipField])
	}
	if fields[store.GateRunField] != "2e697626e50d" {
		t.Errorf("the run behind the verdict is kept, got %v", fields[store.GateRunField])
	}
	// An unstated target is not written, so TargetOf's default answers - one
	// default in one place beats a copy of it at every write.
	if _, ok := fields[store.TargetField]; ok {
		t.Errorf("an unstated target is not stored, got %v", fields[store.TargetField])
	}
}

// A branch nobody has gated yet is the NORMAL state of a queued merge and the
// whole reason the queue exists. Filing is not admission: MergeAdmissible is
// what refuses to land it.
func TestAnUngatedMergeRequestIsFilableAndUnlandable(t *testing.T) {
	art := &store.Artifact{Kind: store.MergeKind}
	var fields map[string]any
	if err := mergeFields(art, &fields, memWriteArgs{Branch: "land/hopeful"}); err != nil {
		t.Fatalf("an ungated merge request must be filable - that is what a queue holds: %v", err)
	}
	art.Kind = store.MergeKind
	if err := store.MergeAdmissible(&store.Artifact{Kind: store.MergeKind}, "1710323"); err == nil {
		t.Fatal("and it must not be admissible, or the queue is decoration")
	}
}

func TestAMergeRequestWithNoBranchIsRefused(t *testing.T) {
	art := &store.Artifact{Kind: store.MergeKind}
	var fields map[string]any
	err := mergeFields(art, &fields, memWriteArgs{GatedTip: "1710323"})
	if err == nil {
		t.Fatal("a merge request that does not say what it would land is not one")
	}
	if !strings.Contains(err.Error(), "branch") {
		t.Errorf("the refusal must name what is missing, got: %v", err)
	}
}

// An update that restates nothing keeps the branch the item was filed with. The
// map arrives already holding the row's fields - memWrite loads them first - so
// this is the case that would otherwise refuse a perfectly good edit.
func TestAnUpdateThatRestatesNothingKeepsTheBranch(t *testing.T) {
	art := &store.Artifact{Kind: store.MergeKind}
	fields := map[string]any{store.BranchField: "land/merge-queue"}
	if err := mergeFields(art, &fields, memWriteArgs{}); err != nil {
		t.Fatalf("restating nothing on an existing merge request is fine: %v", err)
	}
	if fields[store.BranchField] != "land/merge-queue" {
		t.Errorf("and it keeps its branch, got %v", fields[store.BranchField])
	}
	// A new verdict lands on the item it is about without restating the branch,
	// which is exactly how a gate reports back.
	if err := mergeFields(art, &fields, memWriteArgs{GatedTip: "cfa290d"}); err != nil {
		t.Fatalf("a gate reporting a verdict restates nothing else: %v", err)
	}
	if fields[store.GatedTipField] != "cfa290d" {
		t.Errorf("the verdict landed, got %v", fields[store.GatedTipField])
	}
}

// The fields are refused on anything that is not a merge request, because
// nothing would ever read them there. A write that succeeds and changes nothing
// visible is the failure shape this whole system keeps producing.
func TestMergeFieldsAreRefusedOnATodo(t *testing.T) {
	art := &store.Artifact{Kind: "todo"}
	var fields map[string]any
	err := mergeFields(art, &fields, memWriteArgs{Branch: "land/wrong-door"})
	if err == nil {
		t.Fatal("branch on a todo must be refused, not stored where nothing reads it")
	}
	if !strings.Contains(err.Error(), store.MergeKind) {
		t.Errorf("the refusal must say which kind to use, got: %v", err)
	}
	// And a todo that says nothing about any of them is untouched.
	if err := mergeFields(&store.Artifact{Kind: "todo"}, &fields, memWriteArgs{}); err != nil {
		t.Fatalf("an ordinary todo is not affected by this door: %v", err)
	}
	if fields != nil {
		t.Errorf("nothing was stated, so nothing is written, got %v", fields)
	}
}

// A QUEUE ROW ONLY ITS AUTHOR CAN READ IS NOT ON THE QUEUE, which is what an
// unstated scope used to make one.
//
// Measured on the live node before this default changed: three merge requests
// filed at the old default sat in the queue at personal scope for hours, where
// the drainer meant to land them could not see them. Every symptom of that
// reads as "the queue is short tonight".
//
// Pure, because the decision is: a memory stays personal, a work item goes to
// the project, and a token with no project cannot widen anything.
func TestAWorkItemDefaultsToTheProjectAndAMemoryDoesNot(t *testing.T) {
	in := &store.Principal{UserID: "u-1", Project: "flowy"}
	nowhere := &store.Principal{UserID: "u-1"}

	for _, kind := range []string{store.MergeKind, "todo", "handoff"} {
		if got := defaultMemScope(kind, in); got != "project" {
			t.Errorf("a %s defaults to %q - a queue row only its author can read "+
				"is not on the queue", kind, got)
		}
		if got := defaultMemScope(kind, nowhere); got != "personal" {
			t.Errorf("a %s from a token with no project defaults to %q, and there is "+
				"no project for it to live in", kind, got)
		}
	}
	for _, kind := range []string{"note", "fact", "report"} {
		if got := defaultMemScope(kind, in); got != "personal" {
			t.Errorf("a %s defaults to %q - a memory is one agent's note to itself "+
				"and widening it silently publishes what nobody offered", kind, got)
		}
	}
	if got := defaultMemScope(store.MergeKind, nil); got != "personal" {
		t.Errorf("no principal at all defaults to %q", got)
	}
}
