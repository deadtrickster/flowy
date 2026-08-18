package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// fieldsJSON is the row's fields as the column holds them. A helper rather than
// a literal per row, because a test that hand-writes JSON gets to be wrong
// about the shape in a way the store never is.
func fieldsJSON(t *testing.T, f map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	return raw
}

// A QUEUE IS ORDERED BY WHEN ROWS JOINED IT, not by when they were last
// written.
//
// The defect these pin down, measured on 2026-08-18: /api/merge-queue read
// ListArtifacts, which sorts `updated DESC`, and the drainer takes the first
// row of that answer it can work. So every write to a row was a promotion -
// filing one, declaring a gate on one, renaming one - and a row nobody touched
// sank. batch/orchestrator-evening sat last for two hours with its turn
// arriving only when the queue emptied.

// The sort itself, with no database: the whole content of the fix is which
// column the ORDER BY names, and that must be checkable without a live
// Postgres or it is checked by reading it.
func TestAQueuedOrderReadSortsByWhenTheRowWasQueued(t *testing.T) {
	browsing := ArtifactQuery{}
	if got := browsing.order("ar"); got != "ar.updated DESC, ar.id DESC" {
		t.Fatalf("a board read sorts by %q, want the most recently written first", got)
	}
	queue := ArtifactQuery{QueuedOrder: true}
	if got := queue.order("ar"); got != "ar.created ASC, ar.id ASC" {
		t.Fatalf("a queue read sorts by %q, want the oldest queued first", got)
	}
}

// And the behaviour the sort exists for: writing to a row must not move it.
//
// This is the assertion that would have failed all evening. It needs the
// database, because the reordering happened in SQL and a test that sorted a Go
// slice would have agreed with itself.
func TestWritingToAQueuedRowDoesNotMoveItUpTheQueue(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "queueorder")
	owner := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	room := "queueorder-" + ulid.NewString()

	// Queued in this order, and that is the order they must be worked in.
	var ids []string
	for _, title := range []string{"first in", "second in", "third in"} {
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind,
			Project: &project, OwnerUser: owner.UserID,
			Title: title, Visibility: "project", Status: TodoStatus,
			Fields: fieldsJSON(t, map[string]any{
				"branch": "b-" + ulid.NewString(), "target": "master", "room": room,
			}),
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("queue %q: %v", title, err)
		}
		ids = append(ids, art.ID)
	}

	// The oldest row is written to - the shape of a gate declaration, which
	// writes gate_at and gate_run onto the fields, and of every other write a
	// row takes while it waits.
	first, err := db.GetArtifact(ctx, ids[0])
	if err != nil {
		t.Fatalf("read the first row back: %v", err)
	}
	first.Fields = fieldsJSON(t, map[string]any{
		"branch": BranchOf(first), "target": "master", "room": room,
		GateRunField: "a-run-that-touched-it",
	})
	if err := db.UpsertArtifact(ctx, first); err != nil {
		t.Fatalf("write to the first row: %v", err)
	}
	// The write really did move `updated`, so what follows is about the sort
	// and not about a write that never happened.
	touched, err := db.GetArtifact(ctx, ids[0])
	if err != nil {
		t.Fatalf("read the touched row: %v", err)
	}
	if !touched.Updated.After(touched.Created.Add(-time.Second)) {
		t.Fatalf("the write left updated at %v, so this measures nothing", touched.Updated)
	}

	got, err := db.ListArtifacts(ctx, owner, ArtifactQuery{
		Type: MemoryType, Kind: MergeKind, Room: room, QueuedOrder: true,
	})
	if err != nil {
		t.Fatalf("read the queue: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("the queue has %d rows, want %d", len(got), len(ids))
	}
	for i, want := range ids {
		if got[i].ID != want {
			t.Fatalf("row %d of the queue is %q (%q), want %q - the queue reordered itself",
				i, got[i].ID, got[i].Title, want)
		}
	}

	// The negative control: the browsing sort really does put the touched row
	// first, so the two orders are different questions rather than one question
	// that happens to be stable here.
	browsed, err := db.ListArtifacts(ctx, owner, ArtifactQuery{
		Type: MemoryType, Kind: MergeKind, Room: room,
	})
	if err != nil {
		t.Fatalf("read the board: %v", err)
	}
	if len(browsed) == 0 || browsed[0].ID != ids[0] {
		t.Fatal("the board read did not put the row just written at the top, " +
			"so the queued-order check above proves nothing")
	}
}
