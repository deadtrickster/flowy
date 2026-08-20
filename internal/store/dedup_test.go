package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TWO ROWS CLOSED INTO EACH OTHER LEAVE THE WORK FILED NOWHERE, and the second
// close is where that is refusable.
//
// MEASURED TWICE IN ONE EVENING, both times between two seats working the same
// ask: each filed a row within a minute of the other, and each then closed
// THEIR OWN as a duplicate of the other's. Both ended done. Neither close is
// wrong on its own - "this duplicates that, closing mine" is correct behaviour
// from both seats at the same moment - and nothing had an opinion about the
// pair.
func TestTwoRowsCannotReplaceEachOther(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "dedup")
	a := &Principal{UserID: "u-" + ulid.NewString(), AgentID: "a-" + ulid.NewString(), Project: here}
	b := &Principal{UserID: "u-" + ulid.NewString(), AgentID: "a-" + ulid.NewString(), Project: here}

	mine := todoIn(t, ctx, db, a, "the lock is keyed on the target name", VisibilityProjectOnly, "")
	yours := todoIn(t, ctx, db, b, "a merge target is a name, not an identity", VisibilityProjectOnly, "")

	// The first close is ordinary and must stay so: one seat decides the other's
	// row survives.
	closed, entry, err := db.CloseAsDuplicate(ctx, a, mine.ID, yours.ID)
	if err != nil {
		t.Fatalf("the first dedup was refused: %v", err)
	}
	if got := SupersedesOf(closed); got != yours.ID {
		t.Fatalf("the survivor reads as %q", got)
	}
	if closed.Status != DoneStatus || entry.Type != EventTodoStatus {
		t.Fatalf("closed at %q with a %q entry", closed.Status, entry.Type)
	}
	// NAMING THE SURVIVOR IS SAYING THE MEASUREMENT. The work is not gone, it is
	// over there - so IsSilentClose's rule is kept rather than bypassed.
	if len(closed.Notes) != 1 || !strings.Contains(closed.Notes[0].Note, yours.ID) {
		t.Fatalf("the close said %+v, want a note naming the survivor", closed.Notes)
	}

	// THE SECOND CLOSE IS THE ONE THAT ATE THE WORK. Refused, with both ids in
	// the sentence, because the caller has to pick which row lives.
	_, _, err = db.CloseAsDuplicate(ctx, b, yours.ID, mine.ID)
	if err == nil {
		t.Fatal("both rows closed into each other, so the work is filed nowhere")
	}
	var refusal DepRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal is %v, want one the caller can act on", err)
	}
	for _, want := range []string{mine.ID, yours.ID} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name %s: %v", want, err)
		}
	}

	// AND THE SURVIVING ROW IS STILL OPEN, which is the whole point. A refusal
	// that closed it anyway would be the defect with a sentence attached.
	back, err := db.ReadArtifact(ctx, b, yours.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if TodoStatusOf(back) == DoneStatus {
		t.Fatal("the refused close closed the survivor")
	}
}

// A ROW CANNOT REPLACE ITSELF, and a survivor nobody can read is a typo rather
// than a fact.
func TestASurvivorIsAnotherReadableRow(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "dedupself")
	elsewhere := declaredProject(t, ctx, db, "dedupfar")
	p := &Principal{UserID: "u-" + ulid.NewString(), AgentID: "a-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	row := todoIn(t, ctx, db, p, "a row that survives itself", VisibilityProjectOnly, "")
	hidden := todoIn(t, ctx, db, stranger, "in another project", VisibilityProjectOnly, "")

	if _, _, err := db.CloseAsDuplicate(ctx, p, row.ID, row.ID); err == nil {
		t.Fatal("a row replaced itself")
	}
	// An id this token cannot read is a typo as far as the caller is concerned,
	// and recording it would make a chain that nobody can follow.
	if _, _, err := db.CloseAsDuplicate(ctx, p, row.ID, hidden.ID); err == nil {
		t.Fatal("a row was closed into one the caller cannot read")
	}
	if _, _, err := db.CloseAsDuplicate(ctx, p, row.ID, "01HNOSUCHROW00000000000000"); err == nil {
		t.Fatal("a row was closed into an id that is not there")
	}
	// Nothing above moved it.
	if got := statusIn(t, ctx, db, p, row.ID); got == DoneStatus {
		t.Fatalf("a refused dedup closed the row anyway")
	}

	// And a dedup with no survivor named is refused rather than silently
	// becoming an ordinary close - the caller asked for something the node
	// would not have done.
	if _, _, err := db.CloseAsDuplicate(ctx, p, row.ID, ""); err == nil {
		t.Fatal("a dedup named no survivor and was accepted")
	}
}

// THE CALLER'S OWN WORDS WIN over the generated sentence, because a dedup
// usually has something worth saying that the edge does not carry - which of
// the two was further along, what was already noted on the one being closed.
func TestADedupKeepsWhatTheCloserSaid(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "dedupsaid")
	p := &Principal{UserID: "u-" + ulid.NewString(), AgentID: "a-" + ulid.NewString(), Project: here}

	mine := todoIn(t, ctx, db, p, "mine", VisibilityProjectOnly, "")
	yours := todoIn(t, ctx, db, p, "yours", VisibilityProjectOnly, "")

	said := "folded into yours - it has the merge_lands half that mine does not"
	closed, _, err := db.CloseAsDuplicate(ctx, p, mine.ID, yours.ID, said)
	if err != nil {
		t.Fatalf("dedup: %v", err)
	}
	if len(closed.Notes) != 1 || closed.Notes[0].Note != said {
		t.Fatalf("the note reads %+v, want the closer's own words", closed.Notes)
	}
}
