package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// titleOf reads a todo's words off the row, unfiltered - what actually landed,
// rather than what a reader is allowed to see.
func titleOf(t *testing.T, ctx context.Context, db *DB, id string) (title, body string) {
	t.Helper()

	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return art.Title, art.Body
}

func ptr(s string) *string { return &s }

// THE ONE THAT MATTERS. Two writers, and the second one loses and is TOLD WHY.
//
// The author reads a todo that nobody has started and begins correcting the
// title. While they are typing, an agent picks the row up. The edit is written
// against "todo" - the state the author SAW - and it is refused, naming the
// agent who took it, with NOTHING written. A lost update that succeeded quietly
// here would change the job under an agent already working on it, and both
// writers would be told they had succeeded.
func TestAnEditLosesToWhoeverPickedTheTodoUpAndIsToldWho(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "editrace")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	builder := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "fix the relay", VisibilityProjectOnly, "")
	// What the author read before they started typing.
	saw := statusIn(t, ctx, db, author, todo.ID)
	if saw != TodoStatus {
		t.Fatalf("a fresh todo reads as %q", saw)
	}

	// And then somebody picked it up.
	if _, _, err := db.SetTodoStatus(ctx, builder, todo.ID, ActiveStatus); err != nil {
		t.Fatalf("the builder could not take the todo: %v", err)
	}

	_, _, err := db.EditTodo(ctx, author, todo.ID, ptr("fix the relay's xAI path"), nil, saw)
	var moved ErrTodoMoved
	if !errors.As(err, &moved) {
		t.Fatalf("an edit against a stale status was answered %v; it must be refused", err)
	}
	if moved.Saw != TodoStatus || moved.Now != ActiveStatus {
		t.Fatalf("the refusal says the todo went %s->%s", moved.Saw, moved.Now)
	}
	// NAMING WHO IS THE POINT. "Try again" is the wrong advice against a todo
	// somebody is now working on - the editor has to go and talk to them - and a
	// refusal that does not say who cannot tell them that.
	if moved.By != builder.AgentID {
		t.Fatalf("the refusal names %q as the one who took it, not the builder %q",
			moved.By, builder.AgentID)
	}
	if !strings.Contains(moved.Error(), builder.AgentID) {
		t.Fatalf("the sentence the editor reads does not name who took it: %s", moved.Error())
	}

	// AND NOTHING WAS WRITTEN. A refusal that had already landed the title would
	// be the failure this verb exists to prevent, reported as an error.
	title, _ := titleOf(t, ctx, db, todo.ID)
	if title != "fix the relay" {
		t.Fatalf("the refused edit landed anyway: the row says %q", title)
	}
	log, err := db.TodoEditLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("edit log: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("a refused edit left %d entries in the log", len(log))
	}
}

// The compare-and-set itself, in the window a read-then-write cannot close.
//
// This is the same story as the test above with the read held open across the
// move: the caller is holding an artifact it read while the todo was still a
// todo, exactly as a verb does between its read and its write. A read-then-write
// would compare against the copy in hand, find "todo", and write. The guard is
// in the WHERE of the one UPDATE, so it compares against the row - and the write
// touches nothing.
func TestTheGuardRefusesAWriteAgainstAStatusTheRowHasLeft(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "editcas")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	mover := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "drain the queue", VisibilityProjectOnly, "")
	// The copy in hand, read while the row was still a todo.
	stale, err := db.GetArtifact(ctx, todo.ID)
	if err != nil {
		t.Fatalf("read %s: %v", todo.ID, err)
	}
	if _, _, err := db.SetTodoStatus(ctx, mover, todo.ID, ActiveStatus); err != nil {
		t.Fatalf("the mover could not take the todo: %v", err)
	}

	guard := `coalesce(nullif(lower(btrim(status)), ''), '` + TodoStatus + `') = '` +
		TodoStatus + `'`
	err = db.SetArtifactWordsIf(ctx, stale, "drain the queue twice", "", guard)
	if !errors.Is(err, ErrGuardFailed) {
		t.Fatalf("the guarded write against a moved row answered %v", err)
	}
	title, _ := titleOf(t, ctx, db, todo.ID)
	if title != "drain the queue" {
		t.Fatalf("the guarded write landed anyway: the row says %q", title)
	}

	// AND THE GUARD IS NOT A BLANKET REFUSAL. The same write against the state
	// the row is actually in goes through, so a test that passed because
	// everything is refused would fail here.
	back := `coalesce(nullif(lower(btrim(status)), ''), '` + TodoStatus + `') = '` +
		ActiveStatus + `'`
	if err := db.SetArtifactWordsIf(ctx, stale, "drain the queue twice", "now", back); err != nil {
		t.Fatalf("a write against the state the row IS in was refused: %v", err)
	}
	title, body := titleOf(t, ctx, db, todo.ID)
	if title != "drain the queue twice" || body != "now" {
		t.Fatalf("the write that held did not land: %q / %q", title, body)
	}
	// A guarded write that misses a row that is GONE is absent, not contested:
	// the two are different facts and a caller acts differently on them.
	if _, err := db.TombstoneArtifact(ctx, author, todo.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.SetArtifactWordsIf(ctx, stale, "gone", "", back); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a guarded write against a deleted row answered %v", err)
	}
}

// Racing them for real: one editor and one agent taking the row, released at
// the same moment, over enough todos that both orders happen.
//
// The invariant is not who wins - either order is a true history - it is that a
// REFUSAL WROTE NOTHING and a SUCCESS WROTE. There is no third outcome, and the
// third outcome is the bug: an edit told it failed whose words are on the row,
// or an edit told it succeeded that landed under somebody who had already
// started.
//
// ONE PAIR AT A TIME, DELIBERATELY. Every round is a real race - both goroutines
// wait on the same channel - but the rounds do not overlap, because several of
// this package's write paths take a SECOND connection from the pool while
// already inside a transaction: setArtifactStatus signs the row from within
// inTx, and signing reads the principal's key through d.sql. Enough concurrent
// writers and every connection in the pool of 16 is held by a transaction
// waiting for a connection that will not come free until one of them finishes,
// which nothing does. Thirty-two racers hit that reliably and it looks like a
// slow test rather than what it is. It is a real defect and it is not this
// change's - flagged rather than fixed here.
func TestAnEditAndATakeRacingLeaveNoQuietOverwrite(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "editboth")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	taker := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	const rounds = 16
	for round := 0; round < rounds; round++ {
		todo := todoIn(t, ctx, db, author, "before", VisibilityProjectOnly, "")

		var (
			wg      sync.WaitGroup
			refused error
			start   = make(chan struct{})
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _, refused = db.EditTodo(ctx, author, todo.ID, ptr("after"), nil, TodoStatus)
		}()
		go func() {
			defer wg.Done()
			<-start
			if _, _, err := db.SetTodoStatus(ctx, taker, todo.ID, ActiveStatus); err != nil {
				t.Errorf("take %s: %v", todo.ID, err)
			}
		}()
		close(start)
		wg.Wait()

		title, _ := titleOf(t, ctx, db, todo.ID)
		switch {
		case refused != nil && title != "before":
			t.Fatalf("todo %s: the edit was refused (%v) and its words are on the row anyway",
				todo.ID, refused)
		case refused != nil:
			var moved ErrTodoMoved
			if !errors.As(refused, &moved) {
				t.Fatalf("todo %s: the edit was refused with %v, which is not the "+
					"refusal an editor can act on", todo.ID, refused)
			}
		case title != "after":
			t.Fatalf("todo %s: the edit reported success and the row still says %q",
				todo.ID, title)
		}
	}
}

// An ordinary edit: the words change, and everything the queue holds about the
// item does not. The entry records what the row USED to say, which is the one
// thing that is nowhere else once the write lands.
func TestAnEditChangesTheWordsAndNothingElse(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "editplain")
	author := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}
	todo := todoIn(t, ctx, db, author, "raise the thing", VisibilityProjectOnly, "b-drainer")

	art, entry, err := db.EditTodo(ctx, author, todo.ID,
		ptr("raise the right thing"), ptr("with the reason under it"), TodoStatus)
	if err != nil {
		t.Fatalf("the author could not edit their own untouched todo: %v", err)
	}
	if art.Title != "raise the right thing" || art.Body != "with the reason under it" {
		t.Fatalf("the answer carries %q / %q", art.Title, art.Body)
	}
	if entry.Type != EventTodoEdit || entry.Artifact != todo.ID {
		t.Fatalf("the entry is a %q about %q", entry.Type, entry.Artifact)
	}
	// The queue metadata is untouched: an edit is about the words and nothing
	// about who is carrying the item or where it has got to.
	fresh, err := db.GetArtifact(ctx, todo.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if TodoStatusOf(fresh) != TodoStatus || AssigneeOf(fresh) != "b-drainer" ||
		fresh.OwnerUser != author.UserID {
		t.Fatalf("the edit moved the queue metadata: %q / %q / %q",
			TodoStatusOf(fresh), AssigneeOf(fresh), fresh.OwnerUser)
	}
	title, body := titleOf(t, ctx, db, todo.ID)
	if title != "raise the right thing" || body != "with the reason under it" {
		t.Fatalf("the row says %q / %q", title, body)
	}

	log, err := db.TodoEditLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("edit log: %v", err)
	}
	if len(log) != 1 {
		t.Fatalf("the edit left %d entries in the log", len(log))
	}
	got := log[0]
	if got.Saw != TodoStatus || !got.Title || !got.Body || got.Was != "raise the thing" {
		t.Fatalf("the entry reads %+v", got)
	}
	if got.Actor != author.AgentID || got.ActorUser != author.UserID {
		t.Fatalf("the entry names %q / %q as the editor", got.Actor, got.ActorUser)
	}

	// A body may be emptied - somebody pasted the wrong paragraph in - and an
	// edit that said nothing about the title leaves it alone.
	if _, _, err := db.EditTodo(ctx, author, todo.ID, nil, ptr(""), TodoStatus); err != nil {
		t.Fatalf("emptying a body was refused: %v", err)
	}
	title, body = titleOf(t, ctx, db, todo.ID)
	if title != "raise the right thing" || body != "" {
		t.Fatalf("after emptying the body the row says %q / %q", title, body)
	}
}

// What an edit is refused for, other than losing the race: it has to be
// somebody's own words, it has to say what it changes, it may not empty the
// title, and it may not be written against a state that is not "not yet
// started".
func TestTheEditsThatAreRefusedOutright(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "editrefuse")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}
	todo := todoIn(t, ctx, db, author, "the author's words", VisibilityProjectOnly, "")

	// SOMEBODY ELSE'S WORDS. The ruling is older than this door: the queue
	// metadata changes hands and the prose does not.
	_, _, err := db.EditTodo(ctx, stranger, todo.ID, ptr("mine now"), nil, TodoStatus)
	var mine ErrNotYoursToEdit
	if !errors.As(err, &mine) {
		t.Fatalf("a stranger rewriting the author's words was answered %v", err)
	}
	if title, _ := titleOf(t, ctx, db, todo.ID); title != "the author's words" {
		t.Fatalf("the stranger's edit landed: %q", title)
	}

	for _, c := range []struct {
		name        string
		title, body *string
		saw         string
	}{
		{"says nothing", nil, nil, TodoStatus},
		{"empties the title", ptr("   "), nil, TodoStatus},
		{"saw nothing", ptr("ok"), nil, ""},
		{"saw a word that is not a status", ptr("ok"), nil, "in-progress"},
		{"saw active", ptr("ok"), nil, ActiveStatus},
		{"saw done", ptr("ok"), nil, DoneStatus},
	} {
		_, _, err := db.EditTodo(ctx, author, todo.ID, c.title, c.body, c.saw)
		var refusal DepRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("an edit that %s was answered %v, which is not a refusal the "+
				"caller can act on", c.name, err)
		}
	}
	if title, _ := titleOf(t, ctx, db, todo.ID); title != "the author's words" {
		t.Fatalf("a refused edit landed: %q", title)
	}
}
