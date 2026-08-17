package store

import (
	"context"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// statusIn is where a reader's own filtered read says one todo is.
//
// It is a read rather than the ready query, which the assignment tests use: the
// queue answers with OUTSTANDING work, so a todo that has just been closed is
// absent from it - correctly, and uselessly for a check about closing.
func statusIn(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) string {
	t.Helper()

	art, err := db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("%s cannot read todo %s: %v", p.UserID, id, err)
	}
	return TodoStatusOf(art)
}

// THE ONE THAT MATTERS.
//
// A todo one principal raised, CLOSED by another one who did not write it and
// may not write anything else about it. That is the ruling: an agent built and
// deployed the work and could not mark it done because somebody else had raised
// the row, so finished work went on advertising itself as open and the queue
// produced the duplicated builds it exists to prevent.
//
// The second half is the record. The entry names the seat that closed it, the
// person behind that seat, and both ends of the move - so "the agent that did
// the work closed it" and "the operator closed it" are different answers rather
// than the same silent field write, which is what a column cannot say and the
// reason this is an event.
func TestAnybodyWhoCanReadATodoCanCloseIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pba")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	// The one who did the work: another person in the same project, with an agent
	// of their own. The agent is its own seat here, exactly as it is its own voter.
	builder := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "build the linear-thread pane", VisibilityProjectOnly, "")
	if got := statusIn(t, ctx, db, builder, todo.ID); got != TodoStatus {
		t.Fatalf("a fresh todo reads as %q", got)
	}

	art, entry, err := db.SetTodoStatus(ctx, builder, todo.ID, DoneStatus)
	if err != nil {
		t.Fatalf("a principal who did not raise the todo was refused: %v", err)
	}
	if art.Status != DoneStatus {
		t.Fatalf("the row came back at %q", art.Status)
	}
	if entry.Type != EventTodoStatus || entry.Artifact != todo.ID {
		t.Fatalf("the entry is a %q about %q", entry.Type, entry.Artifact)
	}
	// The row is still the author's. A closure moves one column, and nothing about
	// who owns the item or what it says.
	if art.OwnerUser != author.UserID || art.Title != "build the linear-thread pane" {
		t.Fatalf("the closure rewrote the item: owner %q, title %q", art.OwnerUser, art.Title)
	}

	// What the AUTHOR reads, which is the half that fails when the entry hangs off
	// the wrong row: the state, and who put it there.
	if got := statusIn(t, ctx, db, author, todo.ID); got != DoneStatus {
		t.Fatalf("the author's queue still reads the todo as %q", got)
	}
	log, err := db.TodoStatusLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("status log: %v", err)
	}
	state := LatestTodoStatus(log)
	if state == nil {
		t.Fatal("the author cannot read the entry behind a closure of their own todo")
	}
	if state.Status != DoneStatus || state.From != TodoStatus {
		t.Fatalf("the entry says %q->%q", state.From, state.Status)
	}
	if state.By != builder.AgentID || state.ByUser != builder.UserID || state.ByKind != "agent" {
		t.Fatalf("the closure reads %+v, want it made by %s for %s", state, builder.AgentID, builder.UserID)
	}
	if state.At == "" || state.Entry != entry.ID {
		t.Fatalf("the closure does not say when or which entry: %+v", state)
	}
}

// A principal who cannot READ the todo cannot close it, and finds out exactly
// what a read of it would have told them - which is nothing about the row.
//
// Read permission is the whole bar, so this is the only refusal that matters,
// and a write that got through here would be a principal closing work in a
// project it has no reach into.
func TestAPrincipalWhoCannotReadATodoCannotCloseIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pbb")
	elsewhere := declaredProject(t, ctx, db, "pbc")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	todo := todoIn(t, ctx, db, author, "rebuild the gearbox", VisibilityProjectOnly, "a-bench")

	_, _, err := db.SetTodoStatus(ctx, stranger, todo.ID, DoneStatus)
	if err == nil {
		t.Fatal("a principal with no reach into the project closed the todo anyway")
	}
	var notATodo NotATodoError
	if !errors.As(err, &notATodo) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refusal is %v, want the answer a read of the id would give", err)
	}
	// And nothing moved. A refusal that wrote the status and then said no would be
	// this round's own failure, from the other end.
	if got := statusIn(t, ctx, db, author, todo.ID); got != TodoStatus {
		t.Fatalf("the refused write moved the todo to %q", got)
	}
	log, err := db.TodoStatusLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("status log: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("the refused write left %d entries in the log", len(log))
	}
}

// Work that was called done and was not done is REOPENED, and the log says so.
//
// This is why the queue's lifecycle has no terminal state. Refiling it as a new
// todo would leave the trail of what actually happened ending at a closure that
// was wrong, and the row the room had been reading would stay closed - which is
// the same invisibility this whole change is about, one step later.
//
// The grant is what makes this one queue seen by two principals rather than two
// principals who cannot see each other at all.
func TestAClosedTodoIsReopenedAndTheLogKeepsBoth(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pbd")
	elsewhere := declaredProject(t, ctx, db, "pbe")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	across := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}
	if err := db.InsertGrant(ctx, &Grant{
		FromProject: elsewhere, ToProject: here, GrantedBy: author.UserID,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	todo := todoIn(t, ctx, db, author, "deploy the pane", VisibilityShared, "a-bench")

	if _, _, err := db.SetTodoStatus(ctx, author, todo.ID, ActiveStatus); err != nil {
		t.Fatalf("picking it up was refused: %v", err)
	}
	// Somebody else finishes it, from another project, over the grant.
	if _, _, err := db.SetTodoStatus(ctx, across, todo.ID, DoneStatus); err != nil {
		t.Fatalf("a reader across the grant was refused: %v", err)
	}
	if got := statusIn(t, ctx, db, author, todo.ID); got != DoneStatus {
		t.Fatalf("after being closed the todo reads as %q", got)
	}

	// And it was not done after all.
	if _, _, err := db.SetTodoStatus(ctx, author, todo.ID, TodoStatus); err != nil {
		t.Fatalf("reopening was refused: %v", err)
	}
	if got := statusIn(t, ctx, db, author, todo.ID); got != TodoStatus {
		t.Fatalf("the reopened todo reads as %q", got)
	}

	log, err := db.TodoStatusLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("status log: %v", err)
	}
	if len(log) != 3 {
		t.Fatalf("the log holds %d entries, want all three moves", len(log))
	}
	if log[0].Status != ActiveStatus || log[1].Status != DoneStatus || log[2].Status != TodoStatus {
		t.Fatalf("the log is %q, %q, %q, want oldest first",
			log[0].Status, log[1].Status, log[2].Status)
	}
	// The closure is still in it, naming who made it: a reopen appends rather than
	// erasing, so "this was called done on friday by somebody in another project"
	// stays answerable.
	if log[1].ActorUser != across.UserID || log[1].From != ActiveStatus {
		t.Fatalf("the closure reads %+v, want it made by %s from active", log[1], across.UserID)
	}
	state := LatestTodoStatus(log)
	if state == nil || state.Status != TodoStatus || state.From != DoneStatus {
		t.Fatalf("the standing state is %+v, want reopened out of done", state)
	}
	if state.ByUser != author.UserID {
		t.Fatalf("the reopen was recorded as made by %q", state.ByUser)
	}
}

// What is not a queue item is not moved by this verb, and says so the way every
// other queue verb does: naming an id here is not a way to find out what else it
// might be. A bug HAS a lifecycle and it is the issue workflow's - the door sends
// it there rather than this verb taking it, and a bug moved to "done" from here
// would have walked past every transition rule that workflow has.
func TestOnlyAQueueItemHasTheQueuesLifecycle(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pbf")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	project := here
	bug := &Artifact{
		ID: ulid.NewString(), Type: "bug", Project: &project,
		OwnerUser: p.UserID, Title: "the shaft is bent", Status: "open",
		Visibility: VisibilityProjectOnly,
	}
	if err := db.UpsertArtifact(ctx, bug); err != nil {
		t.Fatalf("write bug: %v", err)
	}
	if IsQueueItem(bug) {
		t.Fatal("a bug reads as a queue item, and would take the wrong lifecycle at the door")
	}

	_, _, err := db.SetTodoStatus(ctx, p, bug.ID, DoneStatus)
	var notATodo NotATodoError
	if !errors.As(err, &notATodo) {
		t.Fatalf("closing a bug through the queue verb was answered with %v", err)
	}
	// And it is still open. The refusal is the whole answer.
	got, err := db.ReadArtifact(ctx, p, bug.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Status != "open" {
		t.Fatalf("the refused move left the bug at %q", got.Status)
	}
}

// A status is a lifecycle state and not free text, wherever it arrives. The verb
// normalises it itself rather than trusting the door to have done it, because
// every door calls this - HTTP, the memory tools, and the verb - and a queue
// holding "finished" beside "done" is a queue where half the dependencies are
// satisfied and nothing says why.
func TestTheVerbRefusesAStatusThatIsNotOne(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pbg")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	todo := todoIn(t, ctx, db, p, "keep the vocabulary a vocabulary", VisibilityProjectOnly, "a-bench")

	_, _, err := db.SetTodoStatus(ctx, p, todo.ID, "finished")
	var refusal DepRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a word outside the vocabulary was answered with %v, want a refusal the caller can fix", err)
	}
	if got := statusIn(t, ctx, db, p, todo.ID); got != TodoStatus {
		t.Fatalf("the refused word still moved the todo to %q", got)
	}
	// Case and spacing are the caller's typing rather than a different state: a
	// queue holding "Done" and "done" is two states nothing can compare.
	if _, _, err := db.SetTodoStatus(ctx, p, todo.ID, "  DONE "); err != nil {
		t.Fatalf("a typed status was refused: %v", err)
	}
	if got := statusIn(t, ctx, db, p, todo.ID); got != DoneStatus {
		t.Fatalf("the normalised status landed as %q", got)
	}
}

// A closure satisfies a dependency, which is the point of having one status word
// rather than two ideas of finished. The verb is what a drainer calls, and what
// makes the next todo startable is the row it wrote.
func TestClosingATodoUnblocksWhatWaitsOnIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "pbh")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	other := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	blocker := todoIn(t, ctx, db, author, "land the pane", VisibilityProjectOnly, "a-bench")
	waiting := todoIn(t, ctx, db, author, "announce the pane", VisibilityProjectOnly, "a-bench")
	if _, err := db.AddDep(ctx, author, waiting.ID, blocker.ID); err != nil {
		t.Fatalf("add dep: %v", err)
	}

	r, found := readyOf(t, ctx, db, author, waiting.ID)
	if !found || r.Ready {
		t.Fatalf("the waiting todo is ready before its blocker moved: %+v", r)
	}
	// Closed by somebody who did not raise either row - which is the case the
	// drainer is in.
	if _, _, err := db.SetTodoStatus(ctx, other, blocker.ID, DoneStatus); err != nil {
		t.Fatalf("closing the blocker was refused: %v", err)
	}
	r, found = readyOf(t, ctx, db, author, waiting.ID)
	if !found || !r.Ready {
		t.Fatalf("the waiting todo is still not ready after its blocker was closed: %+v", r)
	}
}
