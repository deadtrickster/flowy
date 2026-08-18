package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// ACTIVE MEANS SOMEBODY IS ON IT, and no door writes a row that says otherwise.
//
// The pair was reachable from two directions and both are asked here: the status
// door moving an unowned row to active, and a row created active with nobody on
// it. What makes this check worth having is the second half of each - the same
// write with a carrier named goes straight through, so what is being refused is
// the incoherent pair rather than the word "active".
func TestActiveWithNobodyCarryingItIsRefused(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "coherence")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := todoIn(t, ctx, db, author, "regrind the escapement", VisibilityProjectOnly, "")

	_, _, err := db.SetTodoStatus(ctx, author, todo.ID, ActiveStatus)
	var unowned ActiveUnownedError
	if !errors.As(err, &unowned) {
		t.Fatalf("moving an unowned row to active answered %v, want it refused", err)
	}
	if unowned.Todo != todo.ID {
		t.Fatalf("the refusal names %q, want the row it refused", unowned.Todo)
	}
	// A refusal that is the caller's mistake, so every door maps it to a 400
	// rather than reporting the node as broken.
	var refusal DepRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal is %T, which no door maps to the caller's mistake", err)
	}
	// AND NOTHING MOVED. A refusal that wrote the status and then said no would
	// be this round's own defect, from the other end.
	if got := statusIn(t, ctx, db, author, todo.ID); got != TodoStatus {
		t.Fatalf("the refused move left the row at %q", got)
	}
	log, err := db.TodoStatusLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("status log: %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("the refused move left %d entries in the log", len(log))
	}

	// The same move, once somebody is carrying it.
	if _, _, err := db.AssignTodo(ctx, author, todo.ID, "a-escapement", nil); err != nil {
		t.Fatalf("taking the row: %v", err)
	}
	if _, _, err := db.SetTodoStatus(ctx, author, todo.ID, ActiveStatus); err != nil {
		t.Fatalf("starting work somebody is carrying was refused: %v", err)
	}
	if got := statusIn(t, ctx, db, author, todo.ID); got != ActiveStatus {
		t.Fatalf("the row reads as %q after being taken up", got)
	}
}

// The same rule at the other end: a row cannot be RAISED into the pair either.
//
// Both statements that write a row locally are asked, because a rule kept at one
// of them is a rule the other one is missing - which is how the queue got here.
func TestARowCannotBeRaisedActiveWithNobodyCarryingIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "coherenceraise")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	project := here

	row := func() *Artifact {
		return &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &project,
			OwnerUser: author.UserID, Title: "filed as in flight", Status: ActiveStatus,
			Visibility: VisibilityProjectOnly,
		}
	}
	var unowned ActiveUnownedError
	if err := db.CreateArtifact(ctx, row()); !errors.As(err, &unowned) {
		t.Fatalf("creating an active row nobody carries answered %v, want it refused", err)
	}
	if err := db.UpsertArtifact(ctx, row()); !errors.As(err, &unowned) {
		t.Fatalf("upserting an active row nobody carries answered %v, want it refused", err)
	}
	if err := db.WriteMemory(ctx, row()); !errors.As(err, &unowned) {
		t.Fatalf("writing an active row nobody carries answered %v, want it refused", err)
	}

	// With a carrier named, the same row goes in. It is the pair that is
	// refused, not the word.
	carried := row()
	fields, err := json.Marshal(map[string]any{AssigneeField: "a-escapement"})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	carried.Fields = fields
	if err := db.CreateArtifact(ctx, carried); err != nil {
		t.Fatalf("creating an active row somebody carries was refused: %v", err)
	}

	// And a BUG is not in this vocabulary at all: the issue workflow's `active`
	// is a different word on a different lifecycle, and this rule is not its.
	bug := row()
	bug.ID, bug.Type, bug.Kind = ulid.NewString(), "bug", ""
	if err := db.CreateArtifact(ctx, bug); err != nil {
		t.Fatalf("a bug was refused a queue rule that is not about it: %v", err)
	}
}

// PUTTING WORK DOWN RETURNS THE ROW TO THE QUEUE, and says so in the log.
//
// This is the half that must not be a refusal. An agent that cannot hand work
// back holds it forever, so the release is taken and the status moves with it -
// in ONE write, because the gap between two writes is the state this whole
// change exists to make unreachable.
func TestPuttingWorkDownReturnsTheRowToTheQueue(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "coherenceputdown")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	worker := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "drain the queue", VisibilityProjectOnly, "")
	pickUp(t, ctx, db, worker, todo.ID, worker.AgentID)

	if _, _, err := db.AssignTodo(ctx, worker, todo.ID, "", nil); err != nil {
		t.Fatalf("putting the row down was refused: %v", err)
	}
	if got := statusIn(t, ctx, db, author, todo.ID); got != TodoStatus {
		t.Fatalf("after being put down the row reads as %q", got)
	}
	art, err := db.GetArtifact(ctx, todo.ID)
	if err != nil {
		t.Fatalf("read %s: %v", todo.ID, err)
	}
	if who := AssigneeOf(art); who != "" {
		t.Fatalf("after being put down the row is carried by %q", who)
	}

	// The move is in the trail, not only on the row: a status that changed with
	// no entry behind it is a queue that cannot say what happened to the work.
	log, err := db.TodoStatusLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("status log: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("the log holds %d entries, want the pick-up and the put-down", len(log))
	}
	back := log[1]
	if back.From != ActiveStatus || back.Status != TodoStatus {
		t.Fatalf("the put-down reads %q->%q, want active->todo", back.From, back.Status)
	}
	if back.Actor != worker.AgentID {
		t.Fatalf("the put-down was recorded as made by %q, want the seat that made it", back.Actor)
	}

	// A HANDOVER IS NOT A RELEASE. Naming somebody else leaves the work in
	// flight, because somebody is still on it.
	pickUp(t, ctx, db, worker, todo.ID, worker.AgentID)
	if _, _, err := db.AssignTodo(ctx, worker, todo.ID, "a-nextshift", nil); err != nil {
		t.Fatalf("handing the row on was refused: %v", err)
	}
	if got := statusIn(t, ctx, db, author, todo.ID); got != ActiveStatus {
		t.Fatalf("a handover left the row at %q, want the work still in flight", got)
	}
}

// The same put-down through the CAS door, which is the one a careful caller
// uses: stating what it expected to find must not get a worse outcome than
// stating nothing.
func TestAClaimOfNobodyIsAReleaseAndMovesTheRowWithIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "coherenceclaim")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	worker := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "gate the branch", VisibilityProjectOnly, "")
	pickUp(t, ctx, db, worker, todo.ID, worker.AgentID)

	// The guard still guards: a release that expected somebody else loses.
	_, _, err := db.ClaimTodo(ctx, worker, todo.ID, "", "a-somebodyelse")
	var held ErrHeldBy
	if !errors.As(err, &held) {
		t.Fatalf("a release against the wrong holder answered %v, want it refused", err)
	}
	if got := statusIn(t, ctx, db, author, todo.ID); got != ActiveStatus {
		t.Fatalf("the lost release moved the row to %q", got)
	}

	if _, _, err := db.ClaimTodo(ctx, worker, todo.ID, "", worker.AgentID); err != nil {
		t.Fatalf("putting the row down under a guard was refused: %v", err)
	}
	if got := statusIn(t, ctx, db, author, todo.ID); got != TodoStatus {
		t.Fatalf("after a guarded put-down the row reads as %q", got)
	}
}
