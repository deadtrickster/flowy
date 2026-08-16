package store

import (
	"encoding/json"
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
