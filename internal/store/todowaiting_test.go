package store

import (
	"context"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// WAITING ON SOMEBODY IS NOT THE SAME AS BEING CARRIED BY THEM, and the answer
// clears itself.
//
// The row this implements was measured on a real board: "3 row(s) assigned to
// orchestrator, all open", where all three were questions for the operator and
// none was work orchestrator could do. So every arm below is a DIFFERENCE
// between two readings that used to be one number, and the last one is the
// clearing rule, which is the part with nowhere to hide: it is a reading of the
// log, so it has to be shown answering to a write that comes AFTER the question
// and not to one that came before.
func TestWaitingOnIsNotCarrying(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "waiting")

	// TWO REAL PRINCIPALS WITH HANDLES, because waiting_on holds a handle and a
	// test using bare ids would pass while the nag - which compares handles -
	// counted nothing. That is the shape this repo calls a wrong answer wearing
	// the shape of a right one.
	asker := seatWithHandle(t, ctx, db, project, "waiting-asker")
	answerer := seatWithHandle(t, ctx, db, project, "waiting-answerer")

	file := func(title string) *Artifact {
		t.Helper()
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &project,
			OwnerUser: asker.UserID, Title: title, Status: "todo",
			Visibility: VisibilityProjectOnly,
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("file %q: %v", title, err)
		}
		return art
	}

	row := file("the one that is waiting on an answer")
	plain := file("the one nobody is waiting on")

	// A ROW NOBODY HAS ASKED ABOUT SAYS SO, and it is the control: without it
	// every assertion below would pass on a store that returned a handle for
	// every row it was given.
	if got := WaitingOnOf(plain); got != "" {
		t.Fatalf("a row nobody asked about is waiting on %q", got)
	}

	asked := "does the delivery go ahead"
	waiting, entry, err := db.SetWaitingOn(ctx, asker, row.ID, "waiting-answerer", asked)
	if err != nil {
		t.Fatalf("ask them: %v", err)
	}
	if got := WaitingOnOf(waiting); got != "waiting-answerer" {
		t.Errorf("the row waits on %q, want waiting-answerer", got)
	}
	if got := AskedOf(waiting); got != asked {
		t.Errorf("the row was asked %q, want %q", got, asked)
	}
	// THE CARRIER DID NOT MOVE. That is the entire point of the field: the two
	// ways of saying this before were handing the row over, which says they are
	// carrying work they are only answering, or saying nothing a counter reads.
	if got := AssigneeOf(waiting); got != AssigneeOf(row) {
		t.Errorf("asking a question changed the carrier to %q", got)
	}
	if entry == nil || entry.Type != EventTodoWaiting {
		t.Fatalf("asking left no entry in the log: %+v", entry)
	}

	// STILL OWED, and asked of the log rather than of a flag.
	answered, err := db.AnsweredWaiting(ctx, []*Artifact{waiting, plain})
	if err != nil {
		t.Fatalf("who has answered: %v", err)
	}
	// PRESENT AND FALSE, not absent. A map that held only the answered rows
	// would make "not in the map" mean both "still waiting" and "this node did
	// not look" - the distinction this whole row exists to keep.
	if got, ok := answered[waiting.ID]; !ok || got {
		t.Errorf("a question nobody has answered reads answered=%v present=%v", got, ok)
	}
	if _, ok := answered[plain.ID]; ok {
		t.Errorf("a row nobody is waiting on was reported on at all")
	}

	// THE ASKER WRITING IS NOT AN ANSWER. This is the arm that fails if the
	// query compares "has anybody written since" rather than "has THEY".
	if _, _, err := db.AppendTodoNote(ctx, asker, waiting.ID, "still waiting on this"); err != nil {
		t.Fatalf("the asker writes on their own row: %v", err)
	}
	answered, err = db.AnsweredWaiting(ctx, []*Artifact{waiting})
	if err != nil {
		t.Fatalf("who has answered: %v", err)
	}
	if answered[waiting.ID] {
		t.Error("the asker writing on the row was counted as the answer")
	}

	// AND THE PERSON NAMED WRITING IS. Any note from them is their answer -
	// that rule is not invented here, it is what `updated` already means.
	if _, _, err := db.AppendTodoNote(ctx, answerer, waiting.ID, "yes, go ahead"); err != nil {
		t.Fatalf("the answerer writes: %v", err)
	}
	answered, err = db.AnsweredWaiting(ctx, []*Artifact{waiting})
	if err != nil {
		t.Fatalf("who has answered: %v", err)
	}
	if !answered[waiting.ID] {
		t.Error("the person named wrote on the row and it still reads as unanswered")
	}

	// TAKING THE QUESTION BACK LEAVES NOTHING BEHIND, including the question
	// itself: a row waiting on nobody that still carries what was asked is a
	// row a reader has to work out the state of.
	cleared, _, err := db.SetWaitingOn(ctx, asker, row.ID, "", "")
	if err != nil {
		t.Fatalf("withdraw the question: %v", err)
	}
	if got := WaitingOnOf(cleared); got != "" {
		t.Errorf("after withdrawing it still waits on %q", got)
	}
	if got := AskedOf(cleared); got != "" {
		t.Errorf("after withdrawing the question is still on the row: %q", got)
	}
}

// A SELF-NAME RESOLVES TO A HANDLE RATHER THAN BEING STORED.
//
// The board grew a seat called "me" once - one row, no roster could explain it,
// and it took a sweep to find. SelfName is where that lesson lives; this is the
// arm that proves this door reaches it.
func TestWaitingOnResolvesASelfName(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "waiting-self")
	seat := seatWithHandle(t, ctx, db, project, "waiting-self-seat")

	art := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &project,
		OwnerUser: seat.UserID, Title: "I am waiting on myself", Status: "todo",
		Visibility: VisibilityProjectOnly,
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("file it: %v", err)
	}

	waiting, _, err := db.SetWaitingOn(ctx, seat, art.ID, "me", "my own move")
	if err != nil {
		t.Fatalf("wait on myself: %v", err)
	}
	if got := WaitingOnOf(waiting); got != "waiting-self-seat" {
		t.Errorf("the row waits on %q, want the handle - a board cannot resolve %q", got, got)
	}
}

// seatWithHandle makes a principal the roster can actually name.
//
// waiting_on holds a handle for AssigneeField's reason, so a fixture with only
// an id would exercise a store that never has to resolve one - and the nag,
// which compares handles, would count nothing while every test passed.
func seatWithHandle(t *testing.T, ctx context.Context, db *DB, project, handle string) *Principal {
	t.Helper()
	id := "u-" + handle
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO users (id, handle, display, node) VALUES ($1, $2, $2, 'test-node')
		 ON CONFLICT (id) DO UPDATE SET handle = EXCLUDED.handle`, id, handle); err != nil {
		t.Fatalf("seat %s: %v", handle, err)
	}
	return &Principal{UserID: id, Project: project}
}
