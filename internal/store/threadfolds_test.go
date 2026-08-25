package store

import "testing"

// What a reader's own log folds into. The order is the half worth asserting
// for LiveBookmarks' reason: the thread a reader unfolded a minute ago is the
// one they are most likely reading.
func TestTheNewestThreadUnfoldedIsFirst(t *testing.T) {
	open := LiveUnfolded([]UnfoldEntry{
		{Thread: "01HONE", Verb: EventThreadUnfold},
		{Thread: "01HTWO", Verb: EventThreadUnfold},
		{Thread: "01HTHREE", Verb: EventThreadUnfold},
	})
	want := []string{"01HTHREE", "01HTWO", "01HONE"}
	if len(open) != len(want) {
		t.Fatalf("got %v, want %v", open, want)
	}
	for i := range want {
		if open[i] != want[i] {
			t.Fatalf("got %v, want %v", open, want)
		}
	}
}

// FOLDING AND UNFOLDING AGAIN PUTS THE THREAD ON TOP, for the same reason
// re-keeping a bookmark does: unfolding is a reader saying "this one, again".
func TestFoldingAndUnfoldingAgainMovesItToTheTop(t *testing.T) {
	open := LiveUnfolded([]UnfoldEntry{
		{Thread: "01HOLD", Verb: EventThreadUnfold},
		{Thread: "01HNEWER", Verb: EventThreadUnfold},
		{Thread: "01HOLD", Verb: EventThreadFold},
		{Thread: "01HOLD", Verb: EventThreadUnfold},
	})
	want := []string{"01HOLD", "01HNEWER"}
	if len(open) != len(want) || open[0] != want[0] || open[1] != want[1] {
		t.Fatalf("got %v, want %v - unfolding again is a reader saying 'show me this one'", open, want)
	}
}

// Folding a thread drops it from the set, unfolding twice is harmless, and an
// entry with no thread is not an unfold of nothing.
func TestFoldedThreadsDropOutAndEmptyEntriesAreSkipped(t *testing.T) {
	open := LiveUnfolded([]UnfoldEntry{
		{Thread: "01HONE", Verb: EventThreadUnfold},
		{Thread: "01HTWO", Verb: EventThreadUnfold},
		{Thread: "01HONE", Verb: EventThreadFold},
		{Thread: "", Verb: EventThreadUnfold},
		{Thread: "01HTWO", Verb: EventThreadUnfold},
	})
	want := []string{"01HTWO"}
	if len(open) != 1 || open[0] != "01HTWO" {
		t.Fatalf("got %v, want %v", open, want)
	}
}
