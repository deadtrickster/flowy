package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestFillAuthorNames asserts each rule on its own rows: a person is their
// registry handle, an agent their person's handle or else their runtime kind,
// and an id naming neither stays "" - unnameable, never the id dressed as a
// name. The note half runs through the same resolution, so one note entry
// rides along to prove the actor fill is not a different rule.
func TestFillAuthorNames(t *testing.T) {
	ctx, db := open(t)

	// A person with a handle: the row's owner IS this id.
	alice := fsUser(t, ctx, db, "alice")
	// A person with no handle, and an agent of theirs with a kind. The agent
	// speaks under the handle of the person it acts for - which is absent
	// here - so it falls back to its runtime kind. This is the no-handle
	// branch chat's speakerNameOf already walks. The handle is cleared AFTER
	// the insert because handle is UNIQUE and two rows carrying '' would
	// collide across runs; NULL is the absent shape the constraint allows.
	nameless := fsUser(t, ctx, db, "nameless")
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE users SET handle = NULL WHERE id = $1`, nameless.ID); err != nil {
		t.Fatalf("clear handle: %v", err)
	}
	builder := &Agent{ID: "a-" + ulid.NewString(), UserID: nameless.ID, Kind: "builder"}
	if err := db.InsertAgent(ctx, builder); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	// An agent whose person HAS a handle: the handle wins over the kind.
	carol := fsUser(t, ctx, db, "carol")
	driver := &Agent{ID: "a-" + ulid.NewString(), UserID: carol.ID, Kind: "worker"}
	if err := db.InsertAgent(ctx, driver); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	// An id that names neither table, which must stay "" rather than come
	// back as the id itself.
	ghost := "u-" + ulid.NewString()

	arts := []*Artifact{
		{ID: "01TEST00000000000000000001", OwnerUser: alice.ID},
		{ID: "01TEST00000000000000000002", OwnerUser: builder.ID},
		{ID: "01TEST00000000000000000003", OwnerUser: driver.ID},
		{ID: "01TEST00000000000000000004", OwnerUser: ghost},
		{ID: "01TEST00000000000000000005", OwnerUser: alice.ID,
			Notes: []NoteEntry{{ID: "e1", Actor: builder.ID}}},
	}
	if err := db.FillAuthorNames(ctx, arts); err != nil {
		t.Fatalf("resolve names: %v", err)
	}

	if got := arts[0].Author; got != alice.Handle {
		t.Fatalf("a person is their handle, got %q want %q", got, alice.Handle)
	}
	if got := arts[1].Author; got != "builder" {
		t.Fatalf("an agent without a handle to lend is their runtime kind, got %q", got)
	}
	if got := arts[2].Author; got != carol.Handle {
		t.Fatalf("an agent speaks under their person's handle, got %q want %q", got, carol.Handle)
	}
	if got := arts[3].Author; got != "" {
		t.Fatalf("an id naming nobody stays unnameable, got %q", got)
	}
	if got := arts[4].Notes[0].ActorName; got != "builder" {
		t.Fatalf("a note's actor resolves by the same rule, got %q", got)
	}
	// Nil rows and empty pages are no-ops, not errors - a fill must never
	// fail a read over rows that carry nothing for it.
	if err := db.FillAuthorNames(ctx, nil); err != nil {
		t.Fatalf("no rows must be a no-op: %v", err)
	}
	if err := db.FillAuthorNames(ctx, []*Artifact{nil}); err != nil {
		t.Fatalf("a nil row must be a no-op: %v", err)
	}
}
