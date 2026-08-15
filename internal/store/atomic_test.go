package store

import (
	"errors"
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

// TestUpdateTaskEventIsAllOrNothing is the same rule for a task move.
//
// Delegating a task and moving its state were each two writes with nothing
// holding them together: the row moved, then the entry that accounts for the
// move was appended. A failure between them left a task in a state its own
// thread does not explain - and because each row carries its own reading and
// replicates on its own, the half that landed reached every peer while the half
// that did not never existed anywhere.
func TestUpdateTaskEventIsAllOrNothing(t *testing.T) {
	ctx, db := open(t)

	project := "pt-" + ulid.NewString()
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
	grant, task, opening := assignmentParts(t, db, project, from, to, art)
	if err := db.WriteAssignment(ctx, grant, task, opening); err != nil {
		t.Fatalf("write assignment: %v", err)
	}

	move := func(state, id string) *Event {
		return &Event{
			ID: id, Type: "task", Project: &project, Room: "handoffs", Thread: task.Thread,
			Parents: []string{}, Actor: to.ID, Artifact: art.ID,
			Body: task.State + "->" + state,
		}
	}

	// The happy path first, so the failure below is a failure of the write and
	// not of the fixture. The row and the entry land together, under one
	// reading, stamped on both.
	entry := move(TaskDone, ulid.NewString())
	was := task.State
	task.State = TaskDone
	if err := db.UpdateTaskEvent(ctx, task, entry); err != nil {
		t.Fatalf("move task: %v", err)
	}
	if task.HLC == 0 || task.HLC != entry.SeqHLC {
		t.Fatalf("the move read %d and its entry %d, want one reading", task.HLC, entry.SeqHLC)
	}
	if rows(t, db, "events", entry.ID) != 1 {
		t.Fatal("the entry is not there after a successful move")
	}
	if here, err := db.GetTask(ctx, task.ID); err != nil {
		t.Fatalf("read back: %v", err)
	} else if here.State != TaskDone {
		t.Fatalf("the task is at %q, want %s", here.State, TaskDone)
	}

	// And now one whose entry cannot be written, because its id is taken. The
	// move must not outlive it.
	collides := move(TaskOpen, entry.ID)
	task.State = TaskOpen
	if err := db.UpdateTaskEvent(ctx, task, collides); err == nil {
		t.Fatal("a move whose entry collides with an existing event id reported success")
	}
	here, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if here.State != TaskDone {
		t.Errorf("the task is at %q, want %s: the move outlived the entry it needed, and %q "+
			"is a state its own thread never accounts for", here.State, TaskDone, was)
	}
}

// TestUpdateTaskEventNeedsTheTaskToBeThere is the half-write from the other
// side.
//
// updateTask ran its UPDATE and threw the result away, so a WHERE that matched
// nothing - a task id that is not here, or one a peer's tombstone has already
// taken away - reported success. UpdateTaskEvent then appended the entry that
// accounts for the move and committed it: a trail entry for a move that did not
// happen, in a thread whose task does not exist, replicating outwards from here.
func TestUpdateTaskEventNeedsTheTaskToBeThere(t *testing.T) {
	ctx, db := open(t)

	project := "pu-" + ulid.NewString()
	actor := &User{Handle: "actor-" + ulid.NewString()}
	if err := db.InsertUser(ctx, actor); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	gone := &Task{
		ID: ulid.NewString(), Artifact: ulid.NewString(), FromUser: actor.ID,
		ToUser: actor.ID, Project: project, State: TaskDone, Thread: ulid.NewString(),
	}
	entry := &Event{
		ID: ulid.NewString(), Type: "task", Project: &project, Room: "handoffs",
		Thread: gone.Thread, Parents: []string{}, Actor: actor.ID,
		Body: "open->" + TaskDone,
	}

	err := db.UpdateTaskEvent(ctx, gone, entry)
	if err == nil {
		t.Fatal("moving a task that is not here reported success")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("moving a task that is not here failed with %v, want %v", err, ErrNotFound)
	}
	if n := rows(t, db, "events", entry.ID); n != 0 {
		t.Errorf("the entry is there (%d rows): a record of a move that never happened", n)
	}
	if n := rows(t, db, "tasks", gone.ID); n != 0 {
		t.Errorf("the task is there (%d rows), which the fixture did not write", n)
	}
}

// TestADeletedArtifactIsNotMovedOrFiled is the half-write from the third side:
// an UPDATE whose WHERE matched a row that is not supposed to be there any more.
//
// setArtifactStatus and setArtifactExternal were both `WHERE id = $1` with the
// result thrown away. The handlers gate on a filtered read, which refuses a
// tombstoned artifact - but the read and the write are two statements, and the
// owner's delete lands in between. The move then matched the dead row anyway:
// a new reading, this node's name and this node's signature stamped onto a
// deleted artifact by somebody who is not its owner, and - because both
// operations write their entry in the same transaction - a trail entry for a
// transition of an artifact that no longer exists, replicating outwards.
//
// updateTask already answers this with ErrNotFound. These two did not.
func TestADeletedArtifactIsNotMovedOrFiled(t *testing.T) {
	ctx, db := open(t)

	project := "px-" + ulid.NewString()
	owner := &User{Handle: "owner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	art := &Artifact{
		Type: "bug", Project: &project, OwnerUser: owner.ID,
		Title: "the one that goes away", Status: "open",
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	// The handler's read has happened by now; the delete lands next.
	if _, err := db.TombstoneArtifact(ctx, &Principal{UserID: owner.ID, Project: project},
		art.ID); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	was, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("read the tombstone back: %v", err)
	}

	moved := &Event{
		ID: ulid.NewString(), Type: "status", Project: &project, Actor: owner.ID,
		Artifact: art.ID, Parents: []string{}, Body: "open->done",
	}
	err = db.MoveArtifactStatus(ctx, art, "done", moved)
	if err == nil {
		t.Fatal("moving the status of a deleted artifact reported success")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("moving the status of a deleted artifact failed with %v, want %v", err, ErrNotFound)
	}
	if n := rows(t, db, "events", moved.ID); n != 0 {
		t.Errorf("the status entry is there (%d rows): a transition of an artifact that is gone", n)
	}

	filed := &Event{
		ID: ulid.NewString(), Type: "forge", Project: &project, Actor: owner.ID,
		Artifact: art.ID, Parents: []string{}, Body: "filed as mock#1",
	}
	ref := &ExternalRef{Forge: "mock", Repo: "owner/repo", Number: 1, URL: "mock://1", State: "open"}
	err = db.LinkArtifactExternal(ctx, art, ref, true, filed)
	if err == nil {
		t.Fatal("filing a deleted artifact reported success")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("filing a deleted artifact failed with %v, want %v", err, ErrNotFound)
	}
	if n := rows(t, db, "events", filed.ID); n != 0 {
		t.Errorf("the filing entry is there (%d rows): an issue opened for an artifact that is gone", n)
	}

	// And the row itself is untouched: same reading, same node, same status, no
	// link - nothing about it was written on the strength of a stale read.
	here, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !here.Tombstone {
		t.Fatal("the delete was undone")
	}
	if here.HLC != was.HLC || here.Node != was.Node || here.Status != was.Status ||
		here.External != nil || here.Reported {
		t.Errorf("the deleted row moved under a stale read: %+v, was %+v", here, was)
	}
}

// TestWriteMemoryIsAllOrNothing is the same rule for the memory tools' write.
//
// mem_write is two rows - the item, and the memory.write entry that says the
// fabric moved - and they were two independent statements. A node that stopped
// between them left a memory with nothing in the log behind it, permanently:
// nothing here ever comes back to finish a half-written operation, and the half
// that landed replicates on its own, because it carries its own reading and a
// peer merges what it is given.
//
// The failure is forced the way the ones above are: the entry collides with an
// event id that is already here, so the append fails after the item has gone in.
func TestWriteMemoryIsAllOrNothing(t *testing.T) {
	ctx, db := open(t)

	project := "pm-" + ulid.NewString()
	owner := &User{Handle: "owner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// The happy path first, so the failure below is a failure of the write and
	// not of the fixture. Both rows land, both carry one reading, and the entry
	// names the item it is the record of.
	item := &Artifact{
		Type: "memory", Kind: "note", Project: &project, OwnerUser: owner.ID,
		Title: "the first one", Visibility: VisibilityProject,
	}
	wrote := &Event{Type: "memory.write", Room: "memory", Actor: owner.ID, Body: item.Title}
	if err := db.WriteMemory(ctx, item, wrote); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	if item.HLC == 0 || item.HLC != wrote.SeqHLC {
		t.Errorf("the item reads %d and its entry %d, want one reading", item.HLC, wrote.SeqHLC)
	}
	if wrote.Artifact != item.ID {
		t.Errorf("the entry names artifact %q, want %q", wrote.Artifact, item.ID)
	}
	if rows(t, db, "artifacts", item.ID) != 1 || rows(t, db, "events", wrote.ID) != 1 {
		t.Fatal("a successful memory write did not leave both rows")
	}

	// And now one that cannot finish. A fresh item, and an entry whose id is
	// already taken by the one above.
	second := &Artifact{
		Type: "memory", Kind: "note", Project: &project, OwnerUser: owner.ID,
		Title: "the one that never happened", Visibility: VisibilityProject,
	}
	collides := &Event{
		ID: wrote.ID, Type: "memory.write", Room: "memory", Actor: owner.ID, Body: second.Title,
	}
	if err := db.WriteMemory(ctx, second, collides); err == nil {
		t.Fatal("a memory write whose entry collides with an existing event id reported success")
	}
	if n := rows(t, db, "artifacts", second.ID); n != 0 {
		t.Errorf("the item is there (%d rows): a memory with no memory.write behind it", n)
	}

	// The update path is the same write, so an update that cannot record itself
	// must not move the item either.
	item.Title = "edited, and never logged"
	if err := db.WriteMemory(ctx, item, &Event{
		ID: wrote.ID, Type: "memory.write", Room: "memory", Actor: owner.ID, Body: item.Title,
	}); err == nil {
		t.Fatal("an update whose entry collides reported success")
	}
	here, err := db.GetArtifact(ctx, item.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if here.Title != "the first one" {
		t.Errorf("the item reads %q: the edit outlived the entry it needed", here.Title)
	}
}
