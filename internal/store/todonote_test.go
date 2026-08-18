package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// notesOn is what a reader's own filtered read of the row says has been learned
// about it. It goes through ReadArtifact rather than through the log door
// because the claim being tested is that the notes come back WITH the row: a
// reader who never learns the notes door exists still sees them.
func notesOn(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) []NoteEntry {
	t.Helper()

	art, err := db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("%s cannot read todo %s: %v", p.UserID, id, err)
	}
	return art.Notes
}

// THE ONE THAT MATTERS. Somebody who is NOT the author attaches what they
// learned to a row that is already being worked on, and the AUTHOR reads it back
// off the row.
//
// Both halves are the feature. An edit would have been refused twice over here -
// the words are the author's, and the row has been picked up - and that is right
// for an edit and is exactly what produced this file: the agent that worked out
// the fix shape had nowhere to put it, so it went in the room and scrolled away.
// A note is a second person's words BESIDE the author's, and the reader who most
// needs them is whoever opens the row next.
func TestSomebodyElseCanAttachWhatTheyLearnedAndTheAuthorReadsItOnTheRow(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "notelearn")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	// The one who did the work: another person in the same project, with an agent
	// of their own. The agent is its own seat here, exactly as it is its own voter.
	builder := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "audit the liveness guards", VisibilityProjectOnly, "")
	if _, _, err := db.SetTodoStatus(ctx, builder, todo.ID, ActiveStatus); err != nil {
		t.Fatalf("the builder could not pick the todo up: %v", err)
	}

	learned := "wait for PRESENCE, then for ABSENCE - two loops, in that order. " +
		"Boot IS the state the first loop waits out, so the second can never fire during it."
	art, entry, err := db.AppendTodoNote(ctx, builder, todo.ID, learned)
	if err != nil {
		t.Fatalf("a principal who did not raise the todo was refused a note: %v", err)
	}
	if entry.Type != EventTodoNote {
		t.Fatalf("a note landed as a %q entry", entry.Type)
	}
	// The text is the entry's BODY rather than a key in meta, so every surface
	// that renders an event body shows the note itself instead of the fact that
	// one exists.
	if entry.Body != learned {
		t.Fatalf("the entry's body is %q, and the note said %q", entry.Body, learned)
	}
	// The answer is the row in the shape a read of it would give, including the
	// note just written - a client reading `notes` off this answer must not get
	// the row as it was one entry ago.
	if len(art.Notes) != 1 || art.Notes[0].Note != learned {
		t.Fatalf("the write answered with %d notes on the row: %+v", len(art.Notes), art.Notes)
	}

	// AND THE AUTHOR READS IT OFF THE ROW. Not out of the room, not out of a log
	// door they have to know about.
	notes := notesOn(t, ctx, db, author, todo.ID)
	if len(notes) != 1 {
		t.Fatalf("the author's read of the row carries %d notes, want 1", len(notes))
	}
	got := notes[0]
	if got.Note != learned {
		t.Fatalf("the note reads back as %q", got.Note)
	}
	// Attributed to the SEAT that learned it, with the person behind the seat
	// beside it: "the agent that did the work measured this" and "the operator
	// says this" are the two things a reader of a note is telling apart.
	if got.Actor != builder.AgentID {
		t.Fatalf("the note is attributed to %q, not to the builder's seat %q",
			got.Actor, builder.AgentID)
	}
	if got.ActorUser != builder.UserID {
		t.Fatalf("the note names %q as the person behind the seat, want %q",
			got.ActorUser, builder.UserID)
	}
	if got.Created == "" || got.Todo != todo.ID {
		t.Fatalf("a note came back without a time or without its row: %+v", got)
	}

	// NOTHING ALREADY WRITTEN MOVED. That is the whole difference from an edit,
	// and it is checked against the unfiltered row rather than against the answer.
	title, body := titleOf(t, ctx, db, todo.ID)
	if title != "audit the liveness guards" || body != "" {
		t.Fatalf("appending a note changed the author's words: title %q, body %q", title, body)
	}
}

// A note is NOT written against a state, so nothing refuses one because the work
// has started or finished - which is when it is most worth writing.
//
// The edit door is the opposite on purpose: it is a compare-and-set against the
// status the editor saw, because rewording a row under whoever is working from
// it changes the job. Adding to it does not, so this verb takes no saw and has
// no window in which it loses.
func TestANoteLandsOnWorkThatIsUnderWayAndOnWorkThatIsFinished(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "noteany")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	worker := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "drain the merge queue", VisibilityProjectOnly, "")
	if _, _, err := db.AppendTodoNote(ctx, worker, todo.ID, "first: nobody has started it"); err != nil {
		t.Fatalf("a note on an untouched todo was refused: %v", err)
	}
	if _, _, err := db.SetTodoStatus(ctx, worker, todo.ID, ActiveStatus); err != nil {
		t.Fatalf("the worker could not pick the todo up: %v", err)
	}
	if _, _, err := db.AppendTodoNote(ctx, worker, todo.ID, "second: measured 4.2s per gate"); err != nil {
		t.Fatalf("a note on active work was refused: %v", err)
	}
	if _, _, err := db.SetTodoStatus(ctx, worker, todo.ID, DoneStatus); err != nil {
		t.Fatalf("the worker could not close the todo: %v", err)
	}
	if _, _, err := db.AppendTodoNote(ctx, worker, todo.ID, "third: landed at 3e5a942"); err != nil {
		t.Fatalf("a note on finished work was refused: %v", err)
	}

	// OLDEST FIRST, which is the order they were learned in and the order the
	// reader wants them in. Nothing earlier was rewritten by anything later:
	// three appends are three entries, not one field holding the last of them.
	notes := notesOn(t, ctx, db, author, todo.ID)
	if len(notes) != 3 {
		t.Fatalf("three notes read back as %d", len(notes))
	}
	for i, want := range []string{"first:", "second:", "third:"} {
		if !strings.HasPrefix(notes[i].Note, want) {
			t.Fatalf("note %d reads %q, want the one starting %q", i, notes[i].Note, want)
		}
	}
	// The log door says the same thing the row does, from the same rows.
	log, err := db.TodoNoteLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("note log: %v", err)
	}
	if len(log) != len(notes) || log[0].ID != notes[0].ID {
		t.Fatalf("the log and the row disagree: %d entries against %d", len(log), len(notes))
	}
}

// A note on a PROJECTLESS row is refused rather than written where only its
// writer could read it.
//
// A projectless event is read back by its actor and by nobody else -
// EventFilterSQL - so the note would be invisible to the row's own author, which
// for a verb whose entire content is the entry is the whole thing lost silently.
// RecordFindingRun refuses one for the same reason. The status trail does not,
// and is right not to: a status also lands on the row, so a personal todo keeps
// the value either way.
func TestANoteOnAProjectlessRowIsRefusedRatherThanWrittenWhereNobodyReadsIt(t *testing.T) {
	ctx, db := open(t)

	author := &Principal{UserID: "u-" + ulid.NewString()}
	fields, err := json.Marshal(map[string]any{RoomField: "build"})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	personal := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "todo",
		OwnerUser: author.UserID, Title: "my own list", Status: TodoStatus,
		Visibility: VisibilityPersonal, Fields: fields,
	}
	if err := db.UpsertArtifact(ctx, personal); err != nil {
		t.Fatalf("write the personal todo: %v", err)
	}

	_, _, err = db.AppendTodoNote(ctx, author, personal.ID, "measured 4.2s")
	if err == nil {
		t.Fatal("a note on a projectless row was written; it is readable by nobody but its writer")
	}
	if !strings.Contains(err.Error(), "no project") {
		t.Fatalf("the refusal does not say what is wrong with the row: %v", err)
	}
	// And it left nothing behind: a refusal that had already appended the entry
	// would be the silent loss this refusal exists to prevent, reported as an
	// error.
	log, err := db.TodoNoteLog(ctx, author, personal.ID)
	if err != nil {
		t.Fatalf("note log: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("a refused note left %d entries behind", len(log))
	}
}

// An empty note is refused, and a row nobody may read is answered the way every
// other queue verb answers an id that is not there.
func TestTheAppendRefusesNothingToSayAndARowTheWriterCannotRead(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "noterefuse")
	elsewhere := declaredProject(t, ctx, db, "noteout")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	todo := todoIn(t, ctx, db, author, "rotate the node key", VisibilityProjectOnly, "")

	if _, _, err := db.AppendTodoNote(ctx, author, todo.ID, "   \n  "); err == nil {
		t.Fatal("an empty note was written")
	}
	// Out of reach is answered as not there. Naming an id here must not be a way
	// to find out what else it might be.
	_, _, err := db.AppendTodoNote(ctx, stranger, todo.ID, "I can see this row")
	var missing NotATodoError
	if !errors.As(err, &missing) {
		t.Fatalf("a note on a row out of reach was answered %v", err)
	}
	if len(notesOn(t, ctx, db, author, todo.ID)) != 0 {
		t.Fatal("a refused note landed on the row anyway")
	}
}

// A note is MINTED, so the only way to get one is to have gone through the verb.
//
// An entry a client could hand over would be words attributed to a seat that
// never wrote them, sitting under the author's body as what somebody learned
// about the work - which is worse here than for the entries beside it, because
// for this type the entry IS the content rather than the record of a value that
// lands on the row.
func TestANoteCannotBeHandedOver(t *testing.T) {
	if !MintedEventType(EventTodoNote) {
		t.Fatal("todo.note is not minted, so a client could push one in over the wire")
	}
}
