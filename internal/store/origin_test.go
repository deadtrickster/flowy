package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// WHERE A ROW CAME FROM IS NOT WHAT BLOCKS IT, and the two must not share a
// verb.
//
// The diagram row asked for dep.add between a diagram and a todo. deps.go
// refuses both ends unless they are queue items and gives the reason: an edge
// the ready query never reads is a dependency that silently does nothing. For a
// diagram it is worse than silent - a diagram never becomes done, so a todo
// blocked by one would never be ready.
//
// So the relation gets its own verb, and this asserts the part that matters:
// EITHER END MAY BE ANYTHING this principal can read, and nothing about the
// queue changes because a row came from somewhere.
func TestARowSaysWhereItCameFromWithoutBlockingOnIt(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "por")
	me := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	// A diagram and a todo - the pair the row is about, and the pair dep.add
	// refuses.
	diagram := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "diagram", Project: &project,
		OwnerUser: me.UserID, Title: "the shape of the thing", Visibility: VisibilityShared,
	}
	if err := db.WriteMemory(ctx, diagram); err != nil {
		t.Fatalf("write the diagram: %v", err)
	}
	todo := todoIn(t, ctx, db, me, "build the thing the drawing shows", VisibilityShared, "")

	// dep.add still refuses it, which is the reason this file exists rather
	// than a widening of that gate.
	if _, err := db.AddDep(ctx, me, todo.ID, diagram.ID); err == nil {
		t.Fatal("dep.add took a diagram as a blocker, so the queue can now stop on a row that never finishes")
	}

	if _, err := db.AddOrigin(ctx, me, todo.ID, diagram.ID); err != nil {
		t.Fatalf("the todo could not say it came out of the diagram: %v", err)
	}
	origins, err := db.OriginsOf(ctx, me, todo.ID)
	if err != nil {
		t.Fatalf("origins: %v", err)
	}
	if len(origins) != 1 || origins[0] != diagram.ID {
		t.Fatalf("the todo says it came from %v, want [%s]", origins, diagram.ID)
	}

	// AND THE QUEUE DOES NOT CARE. A row with an origin is exactly as ready as
	// it was, which is the whole difference between this and dep.add - and the
	// assertion that catches somebody wiring provenance into the ready query
	// later because it looked like an edge.
	entries, err := db.DepLog(ctx, me, todo.ID)
	if err != nil {
		t.Fatalf("dep log: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("saying where a todo came from put %d entr(y/ies) in its DEPENDS-ON log", len(entries))
	}
	ready, err := db.Ready(ctx, me, ArtifactQuery{Type: MemoryType, Kind: "todo"})
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	for _, r := range ready {
		if r.Item.ID == todo.ID && len(r.Blockers) != 0 {
			t.Errorf("the todo has %d blocker(s) after saying where it came from", len(r.Blockers))
		}
	}

	// The other direction is the same relation from the other end: a diagram
	// that came out of the work.
	drawn := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "diagram", Project: &project,
		OwnerUser: me.UserID, Title: "what we ended up with", Visibility: VisibilityShared,
	}
	if err := db.WriteMemory(ctx, drawn); err != nil {
		t.Fatalf("write the second diagram: %v", err)
	}
	if _, err := db.AddOrigin(ctx, me, drawn.ID, todo.ID); err != nil {
		t.Fatalf("a diagram could not say it came out of the work: %v", err)
	}

	// Taking it back appends rather than deletes, and the log keeps both.
	if _, err := db.RemoveOrigin(ctx, me, todo.ID, diagram.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	origins, err = db.OriginsOf(ctx, me, todo.ID)
	if err != nil {
		t.Fatalf("origins after remove: %v", err)
	}
	if len(origins) != 0 {
		t.Errorf("after taking it back the todo still says it came from %v", origins)
	}
	log, err := db.OriginLog(ctx, me, todo.ID)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(log) != 2 || log[0].Type != EventOriginAdd || log[1].Type != EventOriginRemove {
		t.Fatalf("the log reads %+v, want the add and then the removal", log)
	}
}

// The refusals, which are the parts a caller meets when it is wrong.
func TestProvenanceRefusesWhatItCannotMean(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "pos")
	me := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	one := todoIn(t, ctx, db, me, "the first", VisibilityShared, "")
	two := todoIn(t, ctx, db, me, "the second", VisibilityShared, "")

	if _, err := db.AddOrigin(ctx, me, one.ID, one.ID); err == nil {
		t.Error("a row was allowed to have come out of itself")
	}
	if _, err := db.AddOrigin(ctx, me, one.ID, ""); err == nil {
		t.Error("a relation with one end was written")
	}
	// An id this principal cannot read answers the way every other door
	// answers, so this does not become a way to find out what an id is.
	if _, err := db.AddOrigin(ctx, me, one.ID, ulid.NewString()); err == nil {
		t.Error("a row came out of something the writer cannot read")
	}

	if _, err := db.AddOrigin(ctx, me, one.ID, two.ID); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Saying it twice is not a second fact, and taking back what was never said
	// is not an event either.
	if _, err := db.AddOrigin(ctx, me, one.ID, two.ID); err == nil {
		t.Error("the same relation was written twice")
	}
	if _, err := db.RemoveOrigin(ctx, me, two.ID, one.ID); err == nil {
		t.Error("a relation nobody wrote was taken back")
	}

	// AND A CYCLE IS ALLOWED, deliberately: nothing walks this graph, so a loop
	// costs a reader a puzzled moment rather than a queue that stops. dep.add
	// refuses one because a loop in an ORDERING is work that can never start.
	if _, err := db.AddOrigin(ctx, me, two.ID, one.ID); err != nil {
		t.Errorf("provenance refused a loop, which costs a walk on every write for a defect with no victim: %v", err)
	}
}
