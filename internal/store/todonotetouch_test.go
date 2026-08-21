package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A NOTE FROM THE HOLDER IS WORK; A NOTE FROM ANYBODY ELSE IS A FACT.
//
// `updated` is what the stale count reads, so a seat that says why a row is
// still open should quiet the number that asked - and until 01M0HRZM3N it could
// not, because AppendTodoNote never touched the row.
//
// BOTH ARMS, and the second is the one that matters. scripts/landed-to-live.sh
// writes a note on every landing; if any note moved `updated`, a loop running
// every two minutes would keep every row it touches permanently fresh and the
// stale count would measure the loop. And a note can be written by anybody who
// can read the row, so a blanket rule lets a stranger quiet a staleness that
// belongs to its holder.
func TestANoteFromTheHolderMovesUpdatedAndOneFromAnybodyElseDoesNot(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "notetouch")

	holder := &User{ID: "u-" + ulid.NewString(), Handle: "holder-" + ulid.Short()}
	other := &User{ID: "u-" + ulid.NewString(), Handle: "other-" + ulid.Short()}
	for _, u := range []*User{holder, other} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	as := func(u *User) *Principal { return &Principal{UserID: u.ID, Project: project} }

	todo := todoIn(t, ctx, db, as(holder), "a row somebody is carrying", VisibilityProjectOnly, "")
	if _, _, err := db.AssignTodo(ctx, as(holder), todo.ID, holder.Handle, nil); err != nil {
		t.Fatalf("assign: %v", err)
	}

	before, err := db.ReadArtifact(ctx, as(holder), todo.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	// A STRANGER'S NOTE IS A FACT ABOUT THE ROW. It lands - nothing here is
	// about permission - and it must not read as activity by the holder.
	if _, _, err := db.AppendTodoNote(ctx, as(other), todo.ID, "measured from outside"); err != nil {
		t.Fatalf("a note from another seat was refused: %v", err)
	}
	afterStranger, err := db.ReadArtifact(ctx, as(holder), todo.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !afterStranger.Updated.Equal(before.Updated) {
		t.Fatalf("a note from a seat that does not hold the row moved updated: %s -> %s",
			before.Updated, afterStranger.Updated)
	}

	// AND THE HOLDER'S NOTE IS WORK.
	if _, _, err := db.AppendTodoNote(ctx, as(holder), todo.ID, "still blocked on the gate"); err != nil {
		t.Fatalf("a note from the holder was refused: %v", err)
	}
	afterHolder, err := db.ReadArtifact(ctx, as(holder), todo.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !afterHolder.Updated.After(before.Updated) {
		t.Fatalf("the holder wrote a note and updated did not move: %s -> %s",
			before.Updated, afterHolder.Updated)
	}

	// AND AN UNHELD ROW HAS NO HOLDER TO BE. Releasing it makes every note a
	// stranger's note, including the previous holder's - which is right: a row
	// nobody carries has no work to be evidence of.
	if _, _, err := db.AssignTodo(ctx, as(holder), todo.ID, "nobody", nil); err != nil {
		t.Fatalf("release: %v", err)
	}
	released, err := db.ReadArtifact(ctx, as(holder), todo.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, _, err := db.AppendTodoNote(ctx, as(holder), todo.ID, "put down, and here is why"); err != nil {
		t.Fatalf("note on a released row: %v", err)
	}
	afterRelease, err := db.ReadArtifact(ctx, as(holder), todo.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !afterRelease.Updated.Equal(released.Updated) {
		t.Fatalf("a note on a row nobody holds moved updated: %s -> %s",
			released.Updated, afterRelease.Updated)
	}
}
