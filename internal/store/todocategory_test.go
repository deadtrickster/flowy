package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// categoryIn is what a reader's own filtered read says one todo is filed as -
// off the ROW rather than out of fields, because the row is where the other two
// queue facts already are and "one read answers all three" is half of what this
// field is for.
func categoryIn(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) string {
	t.Helper()

	art, err := db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("%s cannot read todo %s: %v", p.UserID, id, err)
	}
	return art.Category
}

// THE ONE THAT MATTERS.
//
// A todo one principal raised, CLASSIFIED by another one who did not write it
// and may not write anything else about it. That is the ruling, and it is the
// same one that governs the assignee and the status: what kind of work this is,
// is a claim about the WORK. The seat that picked the row up and found a bug
// underneath is in a position to say so, and it is routinely not the seat that
// typed the title.
//
// The second half is the record. The entry names the seat that made the call,
// the person behind that seat, and both ends of the move - so a reclassification
// is an argument with two sides on the record rather than a silent field write
// that leaves the queue's own numbers unaccountable.
func TestAnybodyWhoCanReadATodoCanCategoriseIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pca")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	// The one who picked it up: another person in the same project, with an agent
	// of their own. The agent is its own seat here, exactly as it is its own voter.
	triager := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "the pane loses the scroll position", VisibilityProjectOnly, "")
	if got := categoryIn(t, ctx, db, triager, todo.ID); got != "" {
		t.Fatalf("a fresh todo reads as filed under %q, want unclassified", got)
	}

	art, entry, err := db.SetTodoCategory(ctx, triager, todo.ID, CategoryBug)
	if err != nil {
		t.Fatalf("a principal who did not raise the todo was refused: %v", err)
	}
	if art.Category != CategoryBug {
		t.Fatalf("the row came back filed as %q", art.Category)
	}
	if entry.Type != EventTodoCategory || entry.Artifact != todo.ID {
		t.Fatalf("the entry is a %q about %q", entry.Type, entry.Artifact)
	}
	// The row is still the author's. A classification moves one key in fields, and
	// nothing about who owns the item or what it says.
	if art.OwnerUser != author.UserID || art.Title != "the pane loses the scroll position" {
		t.Fatalf("the classification rewrote the item: owner %q, title %q", art.OwnerUser, art.Title)
	}

	// ONE READ ANSWERS ALL THREE, which is the whole reason this value is lifted
	// onto the row beside Status and Assignee instead of being left for each
	// client to dig out of fields. e891944 is what happens when it is not.
	read, err := db.ReadArtifact(ctx, author, todo.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if read.Category != CategoryBug || read.Status != TodoStatus || read.Assignee != "" {
		t.Fatalf("one read says category %q, status %q, assignee %q - want all three off the row",
			read.Category, read.Status, read.Assignee)
	}

	// What the AUTHOR reads, which is the half that fails when the entry hangs off
	// the wrong row: the value, and who put it there.
	log, err := db.TodoCategoryLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("category log: %v", err)
	}
	standing := LatestTodoCategory(log)
	if standing == nil {
		t.Fatal("the author cannot read the entry behind a classification of their own todo")
	}
	if standing.Category != CategoryBug || standing.From != "" {
		t.Fatalf("the entry says %q->%q, want it filed out of nothing", standing.From, standing.Category)
	}
	if standing.By != triager.AgentID || standing.ByUser != triager.UserID || standing.ByKind != "agent" {
		t.Fatalf("the call reads %+v, want it made by %s for %s", standing, triager.AgentID, triager.UserID)
	}
	if standing.At == "" || standing.Entry != entry.ID {
		t.Fatalf("the call does not say when or which entry: %+v", standing)
	}

	// AND THE ARGUMENT. The author disagrees: it is not a bug, it is work nobody
	// asked for. The override appends rather than erasing, so both calls and both
	// seats stay answerable - which is the reason this is an event and not a
	// column.
	if _, _, err := db.SetTodoCategory(ctx, author, todo.ID, CategoryChore); err != nil {
		t.Fatalf("reclassifying was refused: %v", err)
	}
	if got := categoryIn(t, ctx, db, triager, todo.ID); got != CategoryChore {
		t.Fatalf("after the override the todo reads as %q", got)
	}
	log, err = db.TodoCategoryLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("category log: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("the log holds %d entries, want both calls", len(log))
	}
	if log[0].Category != CategoryBug || log[0].ActorUser != triager.UserID {
		t.Fatalf("the first call reads %+v, want a bug called by %s", log[0], triager.UserID)
	}
	standing = LatestTodoCategory(log)
	if standing == nil || standing.Category != CategoryChore || standing.From != CategoryBug {
		t.Fatalf("the standing call is %+v, want a chore out of a bug", standing)
	}
	if standing.ByUser != author.UserID {
		t.Fatalf("the override was recorded as made by %q", standing.ByUser)
	}
}

// A principal who cannot READ the todo cannot classify it, and finds out exactly
// what a read of it would have told them - which is nothing about the row.
//
// Read permission is the whole bar, so this is the only refusal about WHO that
// matters, and a write that got through here would be a principal filing work in
// a project it has no reach into - and, worse, one whose category then lands in
// somebody else's counts.
func TestAPrincipalWhoCannotReadATodoCannotCategoriseIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pcb")
	elsewhere := declaredProject(t, ctx, db, "pcc")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	todo := todoIn(t, ctx, db, author, "rebuild the gearbox", VisibilityProjectOnly, "a-bench")

	_, _, err := db.SetTodoCategory(ctx, stranger, todo.ID, CategoryBug)
	if err == nil {
		t.Fatal("a principal with no reach into the project classified the todo anyway")
	}
	var notATodo NotATodoError
	if !errors.As(err, &notATodo) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refusal is %v, want the answer a read of the id would give", err)
	}
	// And nothing moved. A refusal that wrote the value and then said no would be
	// the same failure from the other end.
	if got := categoryIn(t, ctx, db, author, todo.ID); got != "" {
		t.Fatalf("the refused write filed the todo as %q", got)
	}
	log, err := db.TodoCategoryLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("category log: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("the refused write left %d entries in the log", len(log))
	}
}

// THE SET IS CLOSED AND IT REFUSES.
//
// This is the whole difference between this field and the tags beside it. A
// vocabulary that quietly accepted "defect" would hold two words for one
// population, and the count that was the entire reason for having a closed set
// would be wrong in a way nobody could see from the answer. So an unknown word
// is an error and nothing is written.
//
// Case and spacing are the caller's typing rather than a different category, and
// empty is unclassified - which is not an unknown word, it is the state most of
// this queue is in and the way a wrong call is taken back.
func TestTheVerbRefusesACategoryThatIsNotOne(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pcd")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	todo := todoIn(t, ctx, db, p, "keep the ontology an ontology", VisibilityProjectOnly, "a-bench")

	for _, word := range []string{"defect", "bugs", "epic", "todo", "urgent"} {
		_, _, err := db.SetTodoCategory(ctx, p, todo.ID, word)
		var refusal DepRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("%q was answered with %v, want a refusal the caller can fix", word, err)
		}
		// The refusal has to say what the vocabulary IS, or the caller's only way
		// forward is to guess again.
		for _, known := range TodoCategories {
			if !strings.Contains(err.Error(), known) {
				t.Fatalf("the refusal of %q does not name %q: %v", word, known, err)
			}
		}
		if got := categoryIn(t, ctx, db, p, todo.ID); got != "" {
			t.Fatalf("the refused word %q still filed the todo as %q", word, got)
		}
	}
	log, err := db.TodoCategoryLog(ctx, p, todo.ID)
	if err != nil {
		t.Fatalf("category log: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("five refused words left %d entries in the log", len(log))
	}

	// Typed by a person, which is not a different category.
	if _, _, err := db.SetTodoCategory(ctx, p, todo.ID, "  BUG "); err != nil {
		t.Fatalf("a typed category was refused: %v", err)
	}
	if got := categoryIn(t, ctx, db, p, todo.ID); got != CategoryBug {
		t.Fatalf("the normalised category landed as %q", got)
	}
	// And taking it back. Empty is a value somebody chose, it leaves an entry
	// saying so, and it does not read as an unknown word.
	if _, _, err := db.SetTodoCategory(ctx, p, todo.ID, ""); err != nil {
		t.Fatalf("unfiling was refused: %v", err)
	}
	if got := categoryIn(t, ctx, db, p, todo.ID); got != "" {
		t.Fatalf("after being unfiled the todo reads as %q", got)
	}
	log, err = db.TodoCategoryLog(ctx, p, todo.ID)
	if err != nil {
		t.Fatalf("category log: %v", err)
	}
	if len(log) != 2 || log[1].Category != "" || log[1].From != CategoryBug {
		t.Fatalf("the log is %+v, want a bug and then the unfiling of it", log)
	}
}

// A TODO WITH NO CATEGORY READS AND LISTS EXACTLY AS IT DID YESTERDAY.
//
// Absent is a value, not an error. The whole queue predates this field and none
// of it is backfilled: nothing refuses a row for having no category, nothing
// invents one from the title, and an unclassified todo is on every page it was
// on before - which is the property that makes adding this field a change to
// what can be ASKED rather than a change to what is there.
//
// The narrowed read is the other half. A filter is only a filter if the rows
// without the key drop out of it and stay in everything else - the same claim
// the room field makes, and the same one that makes the closed set worth having:
// asking for the bugs has to give the bugs.
func TestATodoWithNoCategoryReadsAndListsFine(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pce")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	// A row written the way the whole queue was before this field: no category key
	// at all, which is what "predates the field" actually looks like on disk.
	legacy := todoIn(t, ctx, db, p, "a todo from before there were kinds", VisibilityProjectOnly, "a-bench")
	legacy.Fields = fieldsWithout(t, legacy.Fields, CategoryField)
	if err := db.UpsertArtifact(ctx, legacy); err != nil {
		t.Fatalf("rewrite the legacy row: %v", err)
	}
	filed := todoIn(t, ctx, db, p, "the scroll jumps on a new message", VisibilityProjectOnly, "a-bench")
	if _, _, err := db.SetTodoCategory(ctx, p, filed.ID, CategoryBug); err != nil {
		t.Fatalf("classify: %v", err)
	}

	// It reads.
	old, err := db.ReadArtifact(ctx, p, legacy.ID, false)
	if err != nil {
		t.Fatalf("a todo with no category cannot be read: %v", err)
	}
	if old.Category != "" {
		t.Fatalf("a todo with no category key reads as filed under %q", old.Category)
	}
	// And its own log is empty rather than absent, which is a todo nobody has
	// classified rather than one this reader cannot see the calls on.
	log, err := db.TodoCategoryLog(ctx, p, legacy.ID)
	if err != nil {
		t.Fatalf("the log of an unclassified todo could not be read: %v", err)
	}
	if len(log) != 0 || LatestTodoCategory(log) != nil {
		t.Fatalf("an unclassified todo has %d entries behind it", len(log))
	}

	// It lists, beside the classified one.
	all, err := db.ListArtifacts(ctx, p, ArtifactQuery{Type: MemoryType, Kind: "todo"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !listed(all, legacy.ID) || !listed(all, filed.ID) {
		t.Fatalf("the unnarrowed list holds %d rows and not both todos", len(all))
	}

	// And the narrowed read is the bugs: the classified row and not the other.
	bugs, err := db.ListArtifacts(ctx, p, ArtifactQuery{
		Type: MemoryType, Kind: "todo", Category: CategoryBug,
	})
	if err != nil {
		t.Fatalf("list bugs: %v", err)
	}
	if !listed(bugs, filed.ID) {
		t.Fatal("the bug is not in the list of bugs")
	}
	if listed(bugs, legacy.ID) {
		t.Fatal("a todo nobody classified came back as a bug")
	}
	for _, art := range bugs {
		if art.Category != CategoryBug {
			t.Fatalf("the narrowed list holds %q, which is not what was asked for", art.Category)
		}
	}
}

func listed(arts []*Artifact, id string) bool {
	for _, art := range arts {
		if art.ID == id {
			return true
		}
	}
	return false
}
