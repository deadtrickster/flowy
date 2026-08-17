package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// expire is the same ask, made with a deadline that has already passed - which
// is how a test reaches the case this whole file exists for without waiting half
// an hour for it.
//
// It appends a real ask entry rather than editing anything, because the request
// IS the fold of the log now: there is no field to reach in and change, and a
// test that faked one would be testing a shape the verb does not use. The latest
// ask wins, so this one stands, and it stands already mature.
func expire(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) {
	t.Helper()

	art, err := db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("re-read %s: %v", id, err)
	}
	log, err := db.StealLog(ctx, p, id)
	if err != nil {
		t.Fatalf("log of %s: %v", id, err)
	}
	open := FoldStealRequest(log, time.Now().UTC())
	if open == nil {
		t.Fatalf("%s carries no standing request to expire", id)
	}
	actor, actorKind := voteActor(p)
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	e, err := stealEvent(art, p, actor, actorKind, StealAsk, open.By, open.From, past, "")
	if err != nil {
		t.Fatalf("expire %s: %v", id, err)
	}
	if err := db.AppendEvent(ctx, e); err != nil {
		t.Fatalf("expire %s: %v", id, err)
	}
}

// THE ONE THAT MATTERS.
//
// A holder that never answers. This is the case the operator named - an agent
// that died or was decommissioned still holding rows - and it is the case where
// a consent-only protocol deadlocks forever. The deadline is what breaks it, and
// the record is what keeps the break honest: after the take, the log must still
// say this work was TAKEN rather than handed over, because "we agreed" and
// "nobody objected in time" are different facts about the same row and only one
// of them is a handover.
func TestWorkComesOffAHolderWhoNeverAnswers(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "stl")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	asker := &Principal{
		UserID:  "u-" + ulid.NewString(),
		AgentID: "a-" + ulid.NewString(),
		Project: here,
	}

	todo := todoIn(t, ctx, db, author, "drain the merge queue", VisibilityProjectOnly, "a-gone")

	res, err := db.StealTodo(ctx, asker, todo.ID, "", "b-idle", "", 0, nil)
	if err != nil {
		t.Fatalf("the ask was refused: %v", err)
	}
	if res.Request == nil || res.Request.By != "b-idle" || res.Request.From != "a-gone" {
		t.Fatalf("the ask recorded %+v, want b-idle asking a-gone", res.Request)
	}
	if res.Request.Mature {
		t.Fatal("a request made just now is already mature, so the deadline means nothing")
	}
	// The work has NOT moved yet. An ask that moved it would be a take wearing a
	// protocol, which is the thing this must not be.
	if res.Assignee != "a-gone" {
		t.Fatalf("the ask moved the work to %q before anybody answered", res.Assignee)
	}

	// Before the deadline, the asker may not take it. This is the refusal that
	// makes the deadline a deadline rather than a formality.
	if _, err := db.StealTodo(ctx, asker, todo.ID, StealTake, "", "", 0, nil); err == nil {
		t.Fatal("the asker took the work before the deadline passed")
	}

	expire(t, ctx, db, asker, todo.ID)

	took, err := db.StealTodo(ctx, asker, todo.ID, StealTake, "", "", 0, nil)
	if err != nil {
		t.Fatalf("the take was refused after the deadline passed: %v", err)
	}
	if took.Assignee != "b-idle" {
		t.Fatalf("after the take the work reads as carried by %q", took.Assignee)
	}
	if took.Held != "a-gone" {
		t.Fatalf("the take says it came off %q, want a-gone", took.Held)
	}
	if took.Request != nil {
		t.Fatalf("the request is still standing after it was settled: %+v", took.Request)
	}

	// THE RECORD. A reader coming to this row later must be able to tell that
	// nobody agreed to this, and the two steps are what say so.
	log, err := db.StealLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("the author cannot read the log of a negotiation over their own todo: %v", err)
	}
	// Three: the ask, the re-ask the test aged, and the take. What matters is
	// that the take is last and that it is a take.
	if len(log) != 3 {
		t.Fatalf("the log holds %d steps, want the two asks and the take", len(log))
	}
	if log[0].Step != StealAsk || log[0].By != "b-idle" || log[0].From != "a-gone" {
		t.Fatalf("the first step reads %+v, want b-idle asking a-gone", log[0])
	}
	if log[0].After == "" {
		t.Fatal("the ask did not record the deadline it set, so nothing says what was waited for")
	}
	last := log[len(log)-1]
	if last.Step != StealTake || last.Actor != asker.AgentID {
		t.Fatalf("the last step reads %+v, want a take by %s", last, asker.AgentID)
	}
	// And it is NOT recorded as an agreement. This is the assertion that would
	// fail if somebody ever folded the steps into a boolean.
	if last.Step == StealYes {
		t.Fatal("an unanswered taking was recorded as a handover")
	}

	// The assignment log is complete too: the work changed hands, so the surface
	// that answers "who is carrying this and who put them there" must have it.
	claims, err := db.AssignLog(ctx, author, todo.ID)
	if err != nil {
		t.Fatalf("assign log: %v", err)
	}
	if len(claims) != 1 || claims[0].Assignee != "b-idle" || claims[0].Held != "a-gone" {
		t.Fatalf("the assign log reads %+v, want one claim of b-idle off a-gone", claims)
	}
}

// A holder who IS there answers, and the work moves at once with no deadline
// involved. The deadline is the escape hatch, not the path.
func TestAHolderWhoAnswersHandsItOverAtOnce(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "stm")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	holder := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	asker := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := todoIn(t, ctx, db, author, "rebase the branch", VisibilityProjectOnly, "a-busy")

	if _, err := db.StealTodo(ctx, asker, todo.ID, "", "b-free", "", 0, nil); err != nil {
		t.Fatalf("the ask was refused: %v", err)
	}
	res, err := db.StealTodo(ctx, holder, todo.ID, StealYes, "", "", 0, nil)
	if err != nil {
		t.Fatalf("the answer was refused: %v", err)
	}
	if res.Assignee != "b-free" || res.Held != "a-busy" {
		t.Fatalf("after yes the work reads %q off %q, want b-free off a-busy", res.Assignee, res.Held)
	}
	if res.Request != nil {
		t.Fatalf("the request is still standing after it was answered: %+v", res.Request)
	}
	// A second answer has nothing to answer. Otherwise a yes could be replayed
	// onto a row that has since moved on.
	if _, err := db.StealTodo(ctx, holder, todo.ID, StealYes, "", "", 0, nil); err == nil {
		t.Fatal("a second yes was accepted with no request standing")
	}
}

// A refusal is an answer, and it leaves the work where it is with the reason on
// the record. Nothing about it is automatic - the asker may ask again, which is
// deliberate: a cooldown is a policy nobody asked for.
func TestARefusalKeepsTheWorkAndSaysWhy(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "stn")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	holder := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	asker := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := todoIn(t, ctx, db, author, "finish the gate", VisibilityProjectOnly, "a-busy")

	if _, err := db.StealTodo(ctx, asker, todo.ID, "", "b-free", "", 0, nil); err != nil {
		t.Fatalf("the ask was refused: %v", err)
	}
	res, err := db.StealTodo(ctx, holder, todo.ID, StealNo, "", "mid-run, ten minutes out", 0, nil)
	if err != nil {
		t.Fatalf("the no was refused: %v", err)
	}
	if res.Assignee != "a-busy" {
		t.Fatalf("a refusal moved the work to %q", res.Assignee)
	}
	if res.Request != nil {
		t.Fatal("a refused request is still standing, so the asker could still take it")
	}
	if !strings.Contains(res.Step.Reason, "mid-run") {
		t.Fatalf("the refusal recorded reason %q", res.Step.Reason)
	}
	// And the take is gone with it: a refusal that left a live deadline behind
	// would be a no that expires into a yes.
	if _, err := db.StealTodo(ctx, asker, todo.ID, StealTake, "", "", 0, nil); err == nil {
		t.Fatal("the asker took the work after being refused")
	}
}

// The take is restricted to the SEAT that asked, and that is the only
// restriction in the verb. A handle is a string anybody may write; the actor is
// what the node actually knows, so it is what the rule is written against.
func TestOnlyTheSeatThatAskedMayTake(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "sto")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	asker := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	bystander := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := todoIn(t, ctx, db, author, "land the fix", VisibilityProjectOnly, "a-gone")

	if _, err := db.StealTodo(ctx, asker, todo.ID, "", "b-free", "", 0, nil); err != nil {
		t.Fatalf("the ask was refused: %v", err)
	}
	expire(t, ctx, db, asker, todo.ID)

	if _, err := db.StealTodo(ctx, bystander, todo.ID, StealTake, "", "", 0, nil); err == nil {
		t.Fatal("a seat that never asked took the matured request")
	}
	if _, err := db.StealTodo(ctx, asker, todo.ID, StealTake, "", "", 0, nil); err != nil {
		t.Fatalf("the seat that asked was refused its own matured request: %v", err)
	}
}

// A deadline says nobody answered. It does not say nothing changed.
//
// If the row moved while the clock ran, the request is about a situation that no
// longer exists: it was made against one holder and would mature into a taking
// from another, who was never asked. Refused, and the refusal names both.
func TestAMaturedRequestGoesStaleWhenTheRowMoves(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "stp")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	asker := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := todoIn(t, ctx, db, author, "write the migration", VisibilityProjectOnly, "a-gone")

	if _, err := db.StealTodo(ctx, asker, todo.ID, "", "b-free", "", 0, nil); err != nil {
		t.Fatalf("the ask was refused: %v", err)
	}
	expire(t, ctx, db, asker, todo.ID)
	// Somebody else picked it up in the meantime, through the door that has no
	// protocol - which is still legal and is exactly why this case exists.
	if _, _, err := db.AssignTodo(ctx, author, todo.ID, "c-new", nil); err != nil {
		t.Fatalf("assign: %v", err)
	}

	_, err := db.StealTodo(ctx, asker, todo.ID, StealTake, "", "", 0, nil)
	if err == nil {
		t.Fatal("a request made against a-gone matured into a taking from c-new")
	}
	if !strings.Contains(err.Error(), "a-gone") || !strings.Contains(err.Error(), "c-new") {
		t.Fatalf("the refusal is %q and names neither holder", err)
	}
	// And it did not move.
	art, err := db.ReadArtifact(ctx, author, todo.ID, false)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got := AssigneeOf(art); got != "c-new" {
		t.Fatalf("the refused take moved the work to %q", got)
	}
}

// A live request belongs to whoever made it. A second asker overwriting it would
// reset the clock the first one is waiting on, so the slowest asker would win
// every race by arriving last.
func TestASecondAskerCannotResetTheClock(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "stq")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	first := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	second := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := todoIn(t, ctx, db, author, "review the diff", VisibilityProjectOnly, "a-busy")

	if _, err := db.StealTodo(ctx, first, todo.ID, "", "b-first", "", 0, nil); err != nil {
		t.Fatalf("the first ask was refused: %v", err)
	}
	_, err := db.StealTodo(ctx, second, todo.ID, "", "c-second", "", 0, nil)
	if err == nil {
		t.Fatal("a second asker overwrote a live request")
	}
	if !strings.Contains(err.Error(), "b-first") {
		t.Fatalf("the refusal is %q and does not say who is already waiting", err)
	}
	// The first asker may re-ask their own - restating a request you already made
	// is not a race with anybody.
	if _, err := db.StealTodo(ctx, first, todo.ID, "", "b-first", "", 0, nil); err != nil {
		t.Fatalf("the asker could not restate their own request: %v", err)
	}
}

// Asking for work nobody is carrying is a step with nothing on the other side of
// it. The answer names the verb that does what the caller wants rather than
// starting a clock that can only ever run out.
func TestAskingForUnownedWorkIsRefusedWithTheVerbThatWorks(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "str")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	asker := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := todoIn(t, ctx, db, author, "nobody has this", VisibilityProjectOnly, "")

	_, err := db.StealTodo(ctx, asker, todo.ID, "", "b-free", "", 0, nil)
	if err == nil {
		t.Fatal("an ask against an unowned row was accepted")
	}
	if !strings.Contains(err.Error(), "assign") {
		t.Fatalf("the refusal is %q and does not name the verb that works", err)
	}
}

// A deadline outside the bounds is refused in both directions: a one-second one
// is a taking with a formality attached, and one measured in weeks is the
// deadlock this verb exists to break, reintroduced by argument.
func TestTheDeadlineCannotBeMadeMeaningless(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "sts")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	asker := &Principal{UserID: "u-" + ulid.NewString(), Project: here}

	todo := todoIn(t, ctx, db, author, "bounded", VisibilityProjectOnly, "a-busy")

	for _, wait := range []time.Duration{time.Second, 8 * 24 * time.Hour} {
		if _, err := db.StealTodo(ctx, asker, todo.ID, "", "b-free", "", wait, nil); err == nil {
			t.Fatalf("a deadline of %s was accepted", wait)
		}
	}
	// And the default is what an unstated one means - not an instant deadline.
	res, err := db.StealTodo(ctx, asker, todo.ID, "", "b-free", "", 0, nil)
	if err != nil {
		t.Fatalf("an ask with no deadline was refused: %v", err)
	}
	after, err := time.Parse(time.RFC3339, res.Request.After)
	if err != nil {
		t.Fatalf("the deadline %q does not parse: %v", res.Request.After, err)
	}
	if time.Until(after) < DefaultStealWait-time.Minute {
		t.Fatalf("the default deadline is %s away, want about %s", time.Until(after), DefaultStealWait)
	}
}

// A principal who cannot READ the item cannot negotiate over it, and finds out
// exactly what a read of it would have told them. Read permission is the bar
// here for the reason it is the bar on assignment: this verb moves a name in
// fields and grants nothing.
func TestAPrincipalWhoCannotReadTheItemCannotAskForIt(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "stt")
	elsewhere := declaredProject(t, ctx, db, "stu")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	todo := todoIn(t, ctx, db, author, "out of reach", VisibilityProjectOnly, "a-busy")

	_, err := db.StealTodo(ctx, stranger, todo.ID, "", "b-free", "", 0, nil)
	if err == nil {
		t.Fatal("a principal with no reach into the project asked for the work anyway")
	}
	var notATodo NotATodoError
	if !errors.As(err, &notATodo) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refusal is %v, want the answer a read of the id would give", err)
	}
}
