package store

import (
	"strings"
	"testing"
)

// A ROW BORN ACTIVE HAS A START.
//
// POST /api/artifacts takes a status, so a client can create a row straight
// into active without ever passing the transition verb. Measured before this
// was fixed: status=active, started=nil - a row the board shows as running with
// no answer to "since when", which is the operator's whole question.
func TestARowCreatedActiveHasAStart(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "born")

	art := &Artifact{
		Type: MemoryType, Kind: "todo", Project: &project,
		OwnerUser: "01USER-OPERATOR", Title: "born active", Visibility: VisibilityShared,
		Status: ActiveStatus,
		// Carried, because a row cannot be active with nobody on it - see
		// checkQueueRow. The two facts arrive together or the write is refused.
		Fields: []byte(`{"assignee":"somebody"}`),
	}
	if err := db.WriteMemory(ctx, art); err != nil {
		t.Fatalf("write a born-active row: %v", err)
	}
	got, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ActiveStatus {
		t.Fatalf("status is %q, so this test is not measuring what it says", got.Status)
	}
	if got.Started == nil {
		t.Error("a row created active has no start - the board would show it running since never")
	}
	// AND NOT last_worked. A create is not evidence that somebody worked the
	// row; that column answers a different question and moves on events.
	if got.LastWorked != nil {
		t.Errorf("creating a row moved last_worked to %v - a write is not work", *got.LastWorked)
	}
}

// A row created as an ordinary todo has NO start, which is what makes the
// column mean anything. Without this the test above passes for a statement that
// stamps every row unconditionally.
func TestARowCreatedAsATodoHasNoStart(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "born")
	id := todoRow(t, ctx, db, project, "an ordinary row")

	got, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Started != nil {
		t.Errorf("a fresh todo reads started=%v - then started means created", *got.Started)
	}
}

// UPDATING A ROW INTO ACTIVE STAMPS IT ONCE, and rewriting an active row does
// not restart it. The second half is what separates "when did this begin" from
// "when was this last written", which is the distinction the column exists for.
func TestUpsertingIntoActiveStampsOnceAndNotAgain(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "born")
	id := todoRow(t, ctx, db, project, "picked up by an upsert")

	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	art.Status = ActiveStatus
	art.Fields = []byte(`{"assignee":"somebody"}`)
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert into active: %v", err)
	}
	first, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if first.Started == nil {
		t.Fatal("an upsert into active left no start")
	}

	// Written again, still active, with a different title - the shape of a
	// rename, which must not read as starting over.
	art, err = db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	art.Title = "renamed while active"
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	second, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if second.Started == nil || !second.Started.Equal(*first.Started) {
		t.Errorf("started moved from %v to %v on a rename", *first.Started, second.Started)
	}
}

// THE FIELDS WRITER REFUSES TO ACTIVATE A ROW.
//
// It writes fields and status in one statement, which the queue needs - a claim
// must move both or a crash between them leaves a row active and unowned. What
// it cannot do is TRANSITION: it does not stamp started, does not move
// last_worked and does not ask checkQueueRow, so a row activated through it
// would be exactly the state this pair of columns exists to make visible.
//
// No caller passes active today. This is the rule stated where the next caller
// will meet it, rather than a property that holds by luck and breaks quietly.
func TestTheFieldsWriterWillNotActivateARow(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "born")
	id := todoRow(t, ctx, db, project, "not to be activated this way")

	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	err = db.SetArtifactFieldsAndStatusIf(ctx, art, []byte(`{"assignee":"somebody"}`), ActiveStatus, "")
	if err == nil {
		t.Fatal("the fields writer activated a row")
	}
	if !strings.Contains(err.Error(), "MoveArtifactStatus") {
		t.Errorf("the refusal does not name the verb to use instead: %v", err)
	}
	// And the row did not move. A refusal that wrote anyway would be worse than
	// no refusal, because the caller is now told it failed.
	after, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status == ActiveStatus {
		t.Error("the write was refused and landed anyway")
	}
}

// The statuses that path IS for still go through. Refusing active must not turn
// into refusing the release and the join, which are what it was built to carry.
func TestTheFieldsWriterStillMovesTheStatusesItIsFor(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "born")
	id := todoRow(t, ctx, db, project, "released the ordinary way")
	p := &Principal{UserID: "01USER-OPERATOR", AgentID: "01AGENT-T", Project: project}

	if _, _, err := db.AssignTodo(ctx, p, id, "somebody", nil); err != nil {
		t.Fatalf("take it: %v", err)
	}
	if _, _, err := db.SetTodoStatus(ctx, p, id, ActiveStatus); err != nil {
		t.Fatalf("activate it: %v", err)
	}
	// A claim of nobody is a release, and it moves the status back through the
	// combined path - the case putDownStatus exists for.
	if _, _, err := db.ClaimTodo(ctx, p, id, "", "somebody"); err != nil {
		t.Fatalf("release it: %v", err)
	}
	got, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != TodoStatus {
		t.Errorf("a released row reads %q, want %q", got.Status, TodoStatus)
	}
	// AND KEEPS ITS START. Putting a row down is not un-starting it; that is
	// the difference between started and last_worked, and a release that reset
	// it would make "active since" restart every time somebody handed work on.
	if got.Started == nil {
		t.Error("releasing the row cleared its start")
	}
}

// AND THE CREATE-ONLY DOOR STAMPS TOO.
//
// CreateArtifact is a different statement from the upsert - ON CONFLICT DO
// NOTHING rather than DO UPDATE - and it is what POST /api/artifacts uses for a
// row that is new. Asserted separately because the two inserts are two places
// the rule has to be written, which is the count this row's ruling is about:
// three local sites can write a status, so three of them owe the stamp.
//
// The third, InsertArtifact in store.go, is reached only by cmd/smoke. It
// carries the same CASE and is NOT covered here - a mutation of it goes
// unnoticed by this package, and saying so is better than implying otherwise.
func TestCreateArtifactStampsARowBornActive(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "born")

	art := &Artifact{
		Type: MemoryType, Kind: "todo", Project: &project,
		OwnerUser: "01USER-OPERATOR", Title: "created active", Visibility: VisibilityShared,
		Status: ActiveStatus, Fields: []byte(`{"assignee":"somebody"}`),
	}
	if err := db.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("create a born-active row: %v", err)
	}
	got, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ActiveStatus {
		t.Fatalf("status is %q, so this is not measuring what it says", got.Status)
	}
	if got.Started == nil {
		t.Error("a row created active through CreateArtifact has no start")
	}
}
