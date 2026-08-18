package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestARoomIsAFilterAndNotAPermissionAxis holds the whole claim the room field
// makes, in the query that implements it.
//
// Three things, and the middle one is the one a room-shaped change gets wrong.
// A room narrows: #build's panel holds what was raised in #build and not what
// was raised in #general. A todo with no room is in no room's panel and in
// every list that asked for no room - that is what makes this a filter rather
// than a move, and a change that only handled room-tagged todos would satisfy
// the first claim, empty the queue that has worked since before rooms had
// panels, and pass its own tests. And the room is not a visibility: a principal
// of another project asking for #build's panel gets what it could always read
// of it, which is nothing, because the permission filter is the same clause it
// was and never looks at this key.
func TestARoomIsAFilterAndNotAPermissionAxis(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pr")
	elsewhere := declaredProject(t, ctx, db, "px")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	// Three todos in one project: two raised in rooms, one raised nowhere -
	// which is the shape every todo written before the field had.
	write := func(title, room string) *Artifact {
		t.Helper()
		art := &Artifact{
			ID: ulid.NewString(), Type: "memory", Kind: "todo", Project: &project,
			OwnerUser: owner.UserID, Title: title, Status: "todo", Visibility: "project",
		}
		if room != "" {
			fields, err := json.Marshal(map[string]any{RoomField: room})
			if err != nil {
				t.Fatalf("fields: %v", err)
			}
			art.Fields = fields
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		return art
	}
	build := write("bench-test the gearbox", "build")
	general := write("rewrite the pruning notes", "general")
	roomless := write("the one nobody raised in a room", "")

	// titles is what a query answered with, as a set of ids.
	ids := func(q ArtifactQuery, p *Principal) map[string]bool {
		t.Helper()
		q.Type, q.Kind = "memory", "todo"
		list, err := db.ListArtifacts(ctx, p, q)
		if err != nil {
			t.Fatalf("list %+v: %v", q, err)
		}
		out := map[string]bool{}
		for _, a := range list {
			out[a.ID] = true
		}
		return out
	}

	inBuild := ids(ArtifactQuery{Room: "build"}, owner)
	if !inBuild[build.ID] {
		t.Fatal("#build's panel does not hold the todo raised in #build")
	}
	if inBuild[general.ID] {
		t.Fatal("#build's panel holds a todo raised in #general")
	}
	if inBuild[roomless.ID] {
		t.Fatal("#build's panel holds a todo that was raised in no room at all")
	}

	// The discriminating case: no room asked for, so every todo, including the
	// ones that carry none.
	all := ids(ArtifactQuery{}, owner)
	for _, art := range []*Artifact{build, general, roomless} {
		if !all[art.ID] {
			t.Fatalf("the unnarrowed queue is missing %q", art.Title)
		}
	}

	// And the room is not a way into another project's work.
	if got := ids(ArtifactQuery{Room: "build"}, stranger); len(got) != 0 {
		t.Fatalf("a principal of %s read %d row(s) of %s's #build panel",
			elsewhere, len(got), project)
	}
}

// TestAnAssigneeHandsTheNamedPartyNothing is the same claim for the other key
// in fields, and it is the one an assignee is most likely to get wrong.
//
// Being given a piece of work is not being given a copy of it. The assignee is
// a name somebody wrote down - the node resolves it to no principal and the
// permission filter has never looked at this key - so writing it must leave
// exactly the readers the row already had. The surface that DOES hand a
// readable copy over is an assignment, which is a share and a task and a thread
// written together, and it is a different verb on purpose.
//
// The second half is the row signature. fields is inside it (see
// sign.CanonicalArtifact), so an assignment that moved the column and left the
// signature behind would produce a row that no longer verifies under this
// node's own key - a forgery by this fabric's definition, written by the node
// itself and replicated to every peer.
func TestAnAssigneeHandsTheNamedPartyNothing(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pr")
	elsewhere := declaredProject(t, ctx, db, "px")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	// The principal the todo is about to name. It is a real user of another
	// project, so "the assignee can now read it" is a thing this can observe
	// rather than a claim about a name nobody holds.
	taker := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	fields, err := json.Marshal(map[string]any{RoomField: "build"})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	art := &Artifact{
		ID: ulid.NewString(), Type: "memory", Kind: "todo", Project: &project,
		OwnerUser: owner.UserID, Title: "bench-test the gearbox", Status: "todo",
		Visibility: VisibilityProjectOnly, Fields: fields,
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Who could read it before, so that "unchanged" is a comparison.
	reach := func(p *Principal) bool {
		t.Helper()
		_, err := db.ReadArtifact(ctx, p, art.ID, false)
		switch {
		case err == nil:
			return true
		case errors.Is(err, ErrNotFound):
			return false
		default:
			t.Fatalf("read as %s: %v", p.UserID, err)
			return false
		}
	}
	if !reach(owner) {
		t.Fatal("the owner cannot read the todo it just wrote")
	}
	if reach(taker) {
		t.Fatal("the fixture is wrong: the other project could already read this")
	}

	assigned, err := json.Marshal(map[string]any{
		RoomField: "build", AssigneeField: taker.UserID,
	})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if err := db.SetArtifactFields(ctx, art, assigned, &Event{
		Type: "chat", Room: "build", Actor: owner.UserID,
		Body: "gave bench-test the gearbox to " + taker.UserID,
	}); err != nil {
		t.Fatalf("set fields: %v", err)
	}

	if !reach(owner) {
		t.Fatal("assigning the todo took it away from the principal that could read it")
	}
	if reach(taker) {
		t.Fatal("naming a principal as the assignee handed them the row: " +
			"the assignee is a claim about the work, not a grant on it")
	}

	stored, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	// Parsed rather than compared byte for byte: fields is jsonb, and the
	// database owns the key order and the spacing it comes back in.
	var back map[string]any
	if err := json.Unmarshal(stored.Fields, &back); err != nil {
		t.Fatalf("the stored fields do not parse: %v", err)
	}
	if back[AssigneeField] != taker.UserID {
		t.Fatalf("the stored assignee is %v, want %s", back[AssigneeField], taker.UserID)
	}
	if back[RoomField] != "build" {
		t.Fatalf("assigning the todo lost the room it was raised in: %v", back[RoomField])
	}
	id, err := db.Identity(ctx)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if !verifyBytes(id.PublicKey, mustCanonicalArtifact(stored), stored.Sig) {
		t.Fatal("the row this node assigned no longer verifies under its own key")
	}
}
