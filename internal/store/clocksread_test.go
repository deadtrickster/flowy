package store

import (
	"database/sql"
	"testing"
	"time"
)

// THE CHECK THAT WAS MISSING WHEN THESE COLUMNS SHIPPED.
//
// clocks_test.go asserts what workEvidence() classifies as work. That is a pure
// function over strings: it passed while `started` and `last_worked` were
// written by the store, indexed by the schema, and readable by nothing - no
// field on Artifact, no entry in artifactColumns, no API, no console. The
// operator's ask was "active since <time>" and the answer existed only in
// psql. A test that never goes near the database cannot notice that.
//
// So this one reads a stamp back the way a caller does, and then asks the
// database the same question directly, because "the reader agrees with the
// writer" and "the reader made something up" look identical from one side.
func TestTheClocksCanBeReadBack(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "clocks")
	id := todoRow(t, ctx, db, project, "a row that gets picked up")

	// A row nobody has started has NO start. Absent is the honest answer here -
	// the zero time would read as 1970, which is the worst case, for the most
	// ordinary state a row can be in.
	before, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read the fresh row: %v", err)
	}
	if before.Started != nil {
		t.Errorf("a row nobody has started reads started=%v", *before.Started)
	}
	if before.LastWorked != nil {
		t.Errorf("a row nobody has touched reads last_worked=%v", *before.LastWorked)
	}

	// TAKEN FIRST, because active-with-nobody-carrying-it is refused - see
	// checkQueueRow. That refusal is the reason this sequence is the real one:
	// a row reaches active by being claimed and then moved, and both halves are
	// writes to the same row by different verbs.
	p := &Principal{UserID: "01USER-OPERATOR", AgentID: "01AGENT-CLOCKS", Project: project}
	if _, _, err := db.AssignTodo(ctx, p, id, "clocks-agent", nil); err != nil {
		t.Fatalf("take the row: %v", err)
	}
	if _, _, err := db.SetTodoStatus(ctx, p, id, ActiveStatus); err != nil {
		t.Fatalf("move the row to active: %v", err)
	}

	after, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read the active row: %v", err)
	}
	if after.Started == nil {
		t.Fatal("the row is active and reads no start - which is the state this row exists to fix")
	}
	if after.LastWorked == nil {
		t.Fatal("the row moved status and reads no last_worked")
	}

	// AND THE READER IS NOT INVENTING IT. A scan that filled these from the
	// wrong column, or a struct tag pointing at `updated`, would satisfy every
	// assertion above.
	var startedCol, workedCol sql.NullTime
	if err := db.sql.QueryRowContext(ctx,
		`SELECT started, last_worked FROM artifacts WHERE id = $1`, id,
	).Scan(&startedCol, &workedCol); err != nil {
		t.Fatalf("read the columns directly: %v", err)
	}
	if !startedCol.Valid || !startedCol.Time.Equal(*after.Started) {
		t.Errorf("the column says %v and the reader says %v", startedCol, *after.Started)
	}
	if !workedCol.Valid || !workedCol.Time.Equal(*after.LastWorked) {
		t.Errorf("the column says %v and the reader says %v", workedCol, *after.LastWorked)
	}
}

// STARTED IS STAMPED ONCE. A row released and picked up again has not started
// twice, and the clock somebody is measured against must not reset when they
// do - that is the difference between "how long has this been open" and "how
// long since the last time somebody touched it", which is what LastWorked is
// for and why both columns exist.
func TestStartedIsStampedOnceAndLastWorkedKeepsMoving(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "clocks")
	id := todoRow(t, ctx, db, project, "a row that goes round twice")
	p := &Principal{UserID: "01USER-OPERATOR", AgentID: "01AGENT-CLOCKS", Project: project}
	if _, _, err := db.AssignTodo(ctx, p, id, "clocks-agent", nil); err != nil {
		t.Fatalf("take the row: %v", err)
	}

	if _, _, err := db.SetTodoStatus(ctx, p, id, ActiveStatus); err != nil {
		t.Fatalf("first move to active: %v", err)
	}
	first, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read after the first move: %v", err)
	}
	if first.Started == nil {
		t.Fatal("no start after the first move to active")
	}

	// now() is per-statement in postgres, so two moves inside one clock tick
	// would compare equal and prove nothing either way. Separate them.
	time.Sleep(10 * time.Millisecond)

	if _, _, err := db.SetTodoStatus(ctx, p, id, TodoStatus); err != nil {
		t.Fatalf("put it back: %v", err)
	}
	if _, _, err := db.SetTodoStatus(ctx, p, id, ActiveStatus); err != nil {
		t.Fatalf("take it again: %v", err)
	}

	second, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read after the second move: %v", err)
	}
	if second.Started == nil || !second.Started.Equal(*first.Started) {
		t.Errorf("started moved from %v to %v - taking a row back is not starting it over",
			*first.Started, second.Started)
	}
	if second.LastWorked == nil {
		t.Fatal("no last_worked after three status moves")
	}
	if !second.LastWorked.After(*first.LastWorked) {
		t.Errorf("last_worked did not move: %v then %v", *first.LastWorked, *second.LastWorked)
	}
}
