package store

import (
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestTombstoneNamesTheOwner is the delete's own ownership test, in the
// statement that does the deleting.
//
// The handler reads the artifact, checks the owner and then deletes by id, and
// those are two statements with a gap between them: a merge landing in that gap
// changes the owner, and the delete goes ahead on the strength of a read of
// somebody else's row. Naming the caller in the UPDATE makes it find nothing
// instead, which is ErrNotFound - and it makes the rule the store's rather than
// a promise the handler happens to keep, so a reader who calls it directly is
// refused too.
func TestTombstoneNamesTheOwner(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pd")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	reader := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := &Artifact{
		ID: ulid.NewString(), Type: "note", Project: &project, OwnerUser: owner.UserID,
		Title: "the one somebody else deletes", Body: "brindlewick", Visibility: "project",
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// The reader really can read it - same project - so what refuses the delete
	// is the ownership rather than the read.
	if _, err := db.ReadArtifact(ctx, reader, art.ID, false); err != nil {
		t.Fatalf("the reader cannot read it, so this tests nothing: %v", err)
	}
	if _, err := db.TombstoneArtifact(ctx, reader, art.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting somebody else's artifact came back %v, want ErrNotFound", err)
	}
	got, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tombstone {
		t.Fatal("the row was tombstoned by somebody who does not own it")
	}

	// And the owner's own delete still lands.
	deleted, err := db.TombstoneArtifact(ctx, owner, art.ID)
	if err != nil {
		t.Fatalf("the owner's delete: %v", err)
	}
	if !deleted.Tombstone {
		t.Fatal("the owner's delete came back without the tombstone set")
	}
	if got, err = db.GetArtifact(ctx, art.ID); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Tombstone || got.HLC != deleted.HLC {
		t.Fatalf("the row after the owner's delete: %+v", got)
	}
}
