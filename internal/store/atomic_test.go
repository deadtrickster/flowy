package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The multi-row operations are all-or-nothing.
//
// An assignment is three rows and a status move is two, and each of them is one
// thing to the person who asked for it. Written one statement at a time, a
// failure halfway through left the half behind: a share nothing points at, a
// task about an artifact the assignee gets a 404 on, a status with no entry in
// the trail behind it. Nothing in the node ever comes back to finish it, and
// the half replicates on its own, because every row carries its own reading and
// a peer merges what is there.
//
// The failure is forced with a primary key that is already taken, which is the
// one error these statements can be made to hit on demand.

// assignmentParts is the three rows one handoff writes, ready to hand to
// WriteAssignment: fresh ids throughout, so what the test looks for afterwards
// is what it put in.
func assignmentParts(t *testing.T, db *DB, project string, from, to *User, art *Artifact) (*Grant, *Task, *Event) {
	t.Helper()
	thread := ulid.NewString()
	return &Grant{
			ID: ulid.NewString(), FromProject: project, ToProject: project,
			Subject: to.ID, Artifact: art.ID, Cap: "read", GrantedBy: from.ID,
		},
		&Task{
			ID: ulid.NewString(), Artifact: art.ID, FromUser: from.ID, ToUser: to.ID,
			Project: project, State: TaskOpen, Thread: thread,
		},
		&Event{
			ID: ulid.NewString(), Type: "chat", Project: &project, Room: "handoffs",
			Thread: thread, Parents: []string{}, Actor: from.ID, Artifact: art.ID,
			Body: "yours",
		}
}

func TestWriteAssignmentIsAllOrNothing(t *testing.T) {
	ctx, db := open(t)

	project := "pw-" + ulid.NewString()
	from := &User{Handle: "from-" + ulid.NewString()}
	to := &User{Handle: "to-" + ulid.NewString()}
	for _, u := range []*User{from, to} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	art := &Artifact{Type: "bug", Project: &project, OwnerUser: from.ID, Title: "the work"}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	// The happy path first, so the failure below is a failure of the write and
	// not of the fixture. All three rows land, and all three carry one reading.
	grant, task, opening := assignmentParts(t, db, project, from, to, art)
	if err := db.WriteAssignment(ctx, grant, task, opening); err != nil {
		t.Fatalf("write assignment: %v", err)
	}
	if grant.HLC == 0 || grant.HLC != task.HLC || task.HLC != opening.SeqHLC {
		t.Fatalf("the three rows read %d, %d and %d, want one reading",
			grant.HLC, task.HLC, opening.SeqHLC)
	}
	for _, want := range []struct {
		table, id string
	}{{"grants", grant.ID}, {"tasks", task.ID}, {"events", opening.ID}} {
		if rows(t, db, want.table, want.id) != 1 {
			t.Fatalf("%s %s is not there after a successful assignment", want.table, want.id)
		}
	}

	// And now one that cannot finish: the task collides with the one already
	// written, so the insert fails after the share has gone in.
	second, collides, message := assignmentParts(t, db, project, from, to, art)
	collides.ID = task.ID
	if err := db.WriteAssignment(ctx, second, collides, message); err == nil {
		t.Fatal("an assignment whose task collides with an existing id reported success")
	}
	if n := rows(t, db, "grants", second.ID); n != 0 {
		t.Errorf("the share is still there (%d rows): a grant with no task behind it", n)
	}
	if n := rows(t, db, "events", message.ID); n != 0 {
		t.Errorf("the opening message is still there (%d rows)", n)
	}
}

func TestMoveArtifactStatusIsAllOrNothing(t *testing.T) {
	ctx, db := open(t)

	project := "ps-" + ulid.NewString()
	owner := &User{Handle: "owner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	art := &Artifact{
		Type: "bug", Project: &project, OwnerUser: owner.ID,
		Title: "the one that moves", Status: "open",
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	moved := &Event{
		ID: ulid.NewString(), Type: "status", Project: &project, Actor: owner.ID,
		Artifact: art.ID, Parents: []string{}, Body: "open->triaged",
	}
	if err := db.MoveArtifactStatus(ctx, art, "triaged", moved); err != nil {
		t.Fatalf("move status: %v", err)
	}
	if art.HLC != moved.SeqHLC {
		t.Errorf("the move read %d and its entry %d, want one reading", art.HLC, moved.SeqHLC)
	}

	// The entry cannot be written, so the move must not be either: a status
	// nothing in the trail accounts for is a lifecycle nobody can audit.
	collides := &Event{
		ID: moved.ID, Type: "status", Project: &project, Actor: owner.ID,
		Artifact: art.ID, Parents: []string{}, Body: "triaged->in-progress",
	}
	if err := db.MoveArtifactStatus(ctx, art, "in-progress", collides); err == nil {
		t.Fatal("a move whose entry collides with an existing event id reported success")
	}
	here, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if here.Status != "triaged" {
		t.Errorf("the artifact is at %q, want triaged: the move outlived the entry it needed",
			here.Status)
	}
}

// rows counts the rows of one table with one id.
func rows(t *testing.T, db *DB, table, id string) int {
	t.Helper()
	var n int
	// table is one of this file's own literals, never a value from outside.
	if err := db.SQL().QueryRow(`SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
