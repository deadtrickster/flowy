package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// WHAT TO DO FIRST, and what an unjudged row is worth.
//
// The operator asked for priorities on todos and merges with sixteen unowned
// rows on the board and nothing saying which they wanted. The two things this
// asserts are the two decisions in the field: the set is CLOSED, and an
// unranked row sorts ABOVE one somebody deliberately shelved.
func TestAQueueSaysWhatToDoFirst(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "prio")
	p := &Principal{UserID: "u-ranker", Project: project}

	file := func(title string) *Artifact {
		t.Helper()
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &project,
			OwnerUser: p.UserID, Title: title, Status: "todo",
			Visibility: VisibilityProjectOnly,
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("file %q: %v", title, err)
		}
		return art
	}

	first := file("the one they want now")
	shelved := file("the one somebody set down")
	unjudged := file("the one nobody has looked at")

	// A WORD OUTSIDE THE SET IS REFUSED AND NOT STORED, which is the whole
	// value of a closed set: a vocabulary that accepts anything is tags.
	if _, _, err := db.SetTodoPriority(ctx, p, first.ID, "urgent!!"); err == nil {
		t.Fatal("a word outside the set was accepted")
	}
	back, err := db.GetArtifact(ctx, first.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if PriorityOf(back) != "" {
		t.Errorf("a refused word was stored anyway: %q", PriorityOf(back))
	}

	ranked, entry, err := db.SetTodoPriority(ctx, p, first.ID, "NOW")
	if err != nil {
		t.Fatalf("rank it: %v", err)
	}
	// Case and whitespace are the caller's, the value is the store's.
	if PriorityOf(ranked) != PriorityNow {
		t.Errorf("the row says %q, want %q", PriorityOf(ranked), PriorityNow)
	}
	if entry == nil || entry.Type != EventTodoPriority {
		t.Fatalf("ranking left no entry in the log: %+v", entry)
	}
	if _, _, err := db.SetTodoPriority(ctx, p, shelved.ID, PriorityLater); err != nil {
		t.Fatalf("shelve it: %v", err)
	}

	// THE ORDER, which is the opinion. now, then next, then the UNJUDGED, then
	// the one somebody deliberately set down.
	list, err := db.ListArtifacts(ctx, p, ArtifactQuery{
		Kind: "todo", Status: "todo", QueuedOrder: true,
	})
	if err != nil {
		t.Fatalf("list the queue: %v", err)
	}
	var order []string
	for _, a := range list {
		switch a.ID {
		case first.ID:
			order = append(order, "now")
		case unjudged.ID:
			order = append(order, "unjudged")
		case shelved.ID:
			order = append(order, "later")
		}
	}
	want := []string{"now", "unjudged", "later"}
	if len(order) != len(want) {
		t.Fatalf("the queue came back with %v, wanted all three of %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("the queue is ordered %v, want %v - an unjudged row must outrank one "+
				"somebody deliberately shelved", order, want)
		}
	}

	// AND A RANKING CAN BE TAKEN BACK, which is what empty means. It is
	// DELETED rather than stored as "": a board counting its vocabulary must
	// not see a population of empty strings.
	cleared, _, err := db.SetTodoPriority(ctx, p, first.ID, "")
	if err != nil {
		t.Fatalf("take it back: %v", err)
	}
	if PriorityOf(cleared) != "" {
		t.Errorf("clearing left %q", PriorityOf(cleared))
	}
	fields, err := ArtifactFields(cleared)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if _, still := fields[PriorityField]; still {
		t.Error("clearing left an empty priority in the column rather than removing it")
	}
}

// A MERGE ROW RANKS THE SAME WAY, because the operator asked for both in one
// sentence and they are one question: which of the things waiting moves first.
//
// The store verb reads a work item rather than a todo, so this is the assertion
// that the door is not quietly todo-only - which is what it would become the
// first time somebody added a Kind check to make an error message nicer.
func TestAMergeRowRanksLikeATodo(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "prio-merge")
	p := &Principal{UserID: "u-ranker", Project: project}

	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: p.UserID, Title: "land something", Visibility: VisibilityProjectOnly,
		Fields: marshalFields(t, map[string]any{BranchField: "feat-x", TargetField: "master"}),
	}
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file the merge request: %v", err)
	}

	ranked, entry, err := db.SetTodoPriority(ctx, p, row.ID, PriorityNext)
	if err != nil {
		t.Fatalf("rank the merge: %v", err)
	}
	if PriorityOf(ranked) != PriorityNext {
		t.Errorf("the merge row says %q", PriorityOf(ranked))
	}
	// The log says which kind of thing was ranked, because "this is next" about
	// a todo and about a branch are different sentences to whoever reads it.
	if entry == nil || entry.Body == "" {
		t.Fatal("no entry")
	}
	if got := entry.Body; got != "this merge is next" {
		t.Errorf("the log says %q, want it to name the merge", got)
	}
}
