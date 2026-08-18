package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The test this whole file is for: an agent who was not in the room when a
// defect was diagnosed must be able to act on the refusal ALONE. Tonight four
// could not, so each of these asks the question that way round - given only the
// refusal, does the reader end up holding the row.

// explains writes a todo that says which refusal it explains, and hands back its
// id. It goes in through the ordinary fields blob rather than through a verb of
// its own, because that is the claim being made: attaching a row to a refusal is
// an edit of the row, so the door that already writes rows is the door.
func explains(
	t *testing.T, ctx context.Context, db *DB, p *Principal,
	title, code, status string,
) *Artifact {
	t.Helper()

	fields, err := json.Marshal(map[string]any{ExplainsField: code})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	project := p.Project
	art := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &project,
		OwnerUser: p.UserID, Title: title, Status: status,
		Visibility: VisibilityProject, Fields: fields,
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("write the explaining row %q: %v", title, err)
	}
	return art
}

// TestARefusalFindsTheRowThatExplainsIt is the base case, and it is the whole
// feature: a code goes in, the row somebody already wrote comes back, with
// enough on it - title and route - to be worth opening.
func TestARefusalFindsTheRowThatExplainsIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pki")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	reader := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	row := explains(t, ctx, db, author,
		"merge queue: a stale deploy makes every branch read as refused",
		RefusalMergeTipDeployed, TodoStatus)

	// The reader is not the author. That is the case that matters - the person
	// staring at the refusal is nearly never the person who diagnosed it.
	found, err := db.KnownIssues(ctx, reader, []string{RefusalMergeTipDeployed}, false)
	if err != nil {
		t.Fatalf("resolve the refusal: %v", err)
	}
	issue := PickKnownIssue(found, RefusalMergeTipDeployed)
	if issue == nil {
		t.Fatal("a refusal with a row written about it came back with nothing attached")
	}
	if issue.ID != row.ID || issue.Code != RefusalMergeTipDeployed {
		t.Fatalf("the pointer names %s under %q, want %s under %q",
			issue.ID, issue.Code, row.ID, RefusalMergeTipDeployed)
	}
	// The title, because an id alone asks the reader to fetch before they can
	// tell whether it is worth fetching, and they are already annoyed.
	if issue.Title != row.Title {
		t.Fatalf("the pointer carries the title %q, want %q", issue.Title, row.Title)
	}
	// And the route the console follows: project/type/id, the three segments a
	// link is made of.
	if want := here + "/" + MemoryType + "/" + row.ID; issue.Ref != want {
		t.Fatalf("the pointer's route is %q, want %q", issue.Ref, want)
	}

	// A code nobody has written about is absent rather than empty, and asking
	// for it beside a code that resolves does not disturb the answer.
	found, err = db.KnownIssues(ctx, reader,
		[]string{"merge.nobody_wrote_this_one", RefusalMergeTipDeployed}, false)
	if err != nil {
		t.Fatalf("resolve two codes: %v", err)
	}
	if found["merge.nobody_wrote_this_one"] != nil {
		t.Fatal("a code with no row came back with something attached")
	}
	if PickKnownIssue(found, RefusalMergeTipDeployed) == nil {
		t.Fatal("an unexplained code beside an explained one lost the explained one")
	}
}

// TestARefusalNeverCitesARowTheReaderCannotOpen is the half that keeps this from
// being a disclosure channel.
//
// A pointer to something unreadable is worse than no pointer twice over: it
// tells the reader a diagnosis exists and then refuses to show it, and it tells
// a stranger that some project holds a row about this refusal - out of a door
// that answers everybody, since a refusal is what you get for asking wrongly.
// So the filter is applied to the EXPLAINING row, and the answer to a reader out
// of reach is the answer they get everywhere else here: nothing.
func TestARefusalNeverCitesARowTheReaderCannotOpen(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pkr")
	elsewhere := declaredProject(t, ctx, db, "pkr2")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	explains(t, ctx, db, author, "why the queue refuses a re-gated branch",
		RefusalMergeStaleGate, TodoStatus)

	found, err := db.KnownIssues(ctx, stranger, []string{RefusalMergeStaleGate}, false)
	if err != nil {
		t.Fatalf("resolve for a reader out of reach: %v", err)
	}
	if PickKnownIssue(found, RefusalMergeStaleGate) != nil {
		t.Fatal("a refusal handed a stranger a row out of a project they cannot read")
	}

	// The control, and the test is worth nothing without it: the same code, the
	// same row, asked by somebody who can read it, does resolve. So the silence
	// above is the filter and not a broken lookup.
	if PickKnownIssue(mustResolve(t, ctx, db, author, RefusalMergeStaleGate),
		RefusalMergeStaleGate) == nil {
		t.Fatal("the row does not resolve for a reader who can see it, so the test above proves nothing")
	}
}

// TestAnOpenRowExplainsBeforeAClosedOne. A done row still says why the rule
// exists and is worth citing - but if somebody has reopened the question, the
// live row is the one that says what is being done about it now.
func TestAnOpenRowExplainsBeforeAClosedOne(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pko")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	closed := explains(t, ctx, db, author, "the first diagnosis, since fixed",
		RefusalMergeUngated, DoneStatus)
	// Written after the closed one, so "newest wins" and "open wins" disagree
	// here only if the ordering is wrong - and the row written LAST is the open
	// one, which is the ordinary shape: somebody reopened the question.
	open := explains(t, ctx, db, author, "it is happening again and here is why",
		RefusalMergeUngated, TodoStatus)

	issue := PickKnownIssue(mustResolve(t, ctx, db, author, RefusalMergeUngated),
		RefusalMergeUngated)
	if issue == nil {
		t.Fatal("nothing came back for a code two rows explain")
	}
	if issue.ID != open.ID {
		t.Fatalf("the refusal cites %s, want the open row %s (the closed one is %s)",
			issue.ID, open.ID, closed.ID)
	}
	// One pointer, not a reading list. A refusal that hands back every row ever
	// written about it is a search result, and a search is what the reader was
	// spared.
	if len(mustResolve(t, ctx, db, author, RefusalMergeUngated)) != 1 {
		t.Fatal("one code resolved to more than one pointer")
	}
}

// TestEveryQueueRefusalNamesItself. The lookup above is worth nothing if the
// refusals it is keyed on do not carry a key - and prose is not a key, which is
// the reason the code exists as a field rather than being matched out of the
// sentence somebody will reword next week.
func TestEveryQueueRefusalNamesItself(t *testing.T) {
	gated := mergeItem(t, "01MERGE", map[string]any{
		BranchField:   "land/refusal-row",
		GatedTipField: "b48e2af",
	})
	for _, c := range []struct {
		what string
		err  error
		want string
	}{
		{"a row that is not a merge item", MergeAdmissible(&Artifact{ID: "01TODO"}, "b48e2af"),
			RefusalMergeNotAnItem},
		{"admission asked against no tip", MergeAdmissible(gated, "  "),
			RefusalMergeTipUnstated},
		{"an item no gate has measured",
			MergeAdmissible(mergeItem(t, "01MERGE", map[string]any{BranchField: "b"}), "b48e2af"),
			RefusalMergeUngated},
		{"a gate that measured another tip", MergeAdmissible(gated, "cfa290d"),
			RefusalMergeStaleGate},
	} {
		if c.err == nil {
			t.Fatalf("%s was admitted", c.what)
		}
		if got := RefusalCodeOf(c.err); got != c.want {
			t.Fatalf("%s refuses under %q, want %q", c.what, got, c.want)
		}
		// Through wrapping, because a door that has added context to an error is
		// the ordinary case and must still be able to ask.
		if got := RefusalCodeOf(errors.New("wrapped: " + c.err.Error())); got != "" {
			t.Fatalf("an unrelated error answered with the code %q", got)
		}
	}
	// An admitted merge has no refusal and so no code. Said out loud because a
	// lookup that fires on success would attach a defect to a green verdict.
	if got := RefusalCodeOf(MergeAdmissible(gated, "b48e2af")); got != "" {
		t.Fatalf("an admissible merge produced the code %q", got)
	}
}

func mustResolve(
	t *testing.T, ctx context.Context, db *DB, p *Principal, codes ...string,
) map[string]*KnownIssue {
	t.Helper()

	found, err := db.KnownIssues(ctx, p, codes, false)
	if err != nil {
		t.Fatalf("resolve %v: %v", codes, err)
	}
	return found
}
