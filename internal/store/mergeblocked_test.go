package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestASkipIsRecordedAndAgesOut is the third answer the queue owes a reader,
// and the two properties that keep it honest.
//
// Measured on 18 Aug: drain.sh skips a row whose branch is checked out
// elsewhere, into its own log. Three rows were held that way, the drainer woke
// every ninety seconds and took nothing for twenty minutes, and the queue
// showed all three as plain todo. A row nobody can take and a row waiting its
// turn read identically.
func TestASkipIsRecordedAndAgesOut(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)

	drainer := &Principal{UserID: "u-drainer", Project: project}
	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: "u-author", Title: "a branch somebody is holding", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-held", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file the merge request: %v", err)
	}

	// A skip with no reason is the silence this replaces, one field along.
	if _, _, err := db.SetMergeBlocked(ctx, drainer, row.ID, "  "); err == nil {
		t.Fatal("a skip with no reason was recorded")
	}

	why := "branch checked out in /home/dead/Projects/flowy-qorder"
	art, entry, err := db.SetMergeBlocked(ctx, drainer, row.ID, why)
	if err != nil {
		t.Fatalf("record the skip: %v", err)
	}
	now := time.Now().UTC()
	if got := BlockedAt(art, now); got != why {
		t.Errorf("the row says %q, want the reason it was skipped", got)
	}
	if BlockedByOf(art) != drainer.UserID {
		t.Errorf("blocked_by is %q - a skip is one drainer's answer, not the world's",
			BlockedByOf(art))
	}
	if entry == nil || entry.Type != EventMergeBlocked {
		t.Fatalf("the skip left no entry in the log: %+v", entry)
	}
	if !strings.Contains(entry.Body, "could not take") {
		t.Errorf("the entry body reads %q", entry.Body)
	}
	var meta map[string]string
	if err := json.Unmarshal(entry.Meta, &meta); err != nil {
		t.Fatalf("entry meta: %v", err)
	}
	if meta[BlockedWhyField] != why {
		t.Errorf("the entry does not carry the reason: %v", meta)
	}

	// IT DOES NOT TOUCH THE LOCK, in either direction. This is the verb for a
	// caller that never got as far as taking the target, and requiring the lock
	// to report not having it would be the joke version of the door.
	lock, err := db.MergeLockOf(ctx, project, target)
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}
	if lock != nil && lock.Live(now) {
		t.Errorf("recording a skip took the target: held by %s", lock.Holder)
	}

	// A FACT ABOUT A MOMENT. "Checked out elsewhere" is true until somebody
	// detaches, and a reason left lying on the row would read as grounds to skip
	// it forever.
	if got := BlockedAt(art, now.Add(BlockBelievedFor+time.Minute)); got != "" {
		t.Errorf("a skip from %v ago still reads as %q", BlockBelievedFor, got)
	}

	// AND A DECLARATION CLEARS IT, which is the strongest case of the three
	// things a declare clears: somebody has just taken the row, so whatever
	// stopped the last caller taking it has been disproved.
	if _, _, err := db.SetMergeGate(ctx, drainer, row.ID, "run-after-skip", "", ""); err != nil {
		t.Fatalf("declare after the skip: %v", err)
	}
	after, err := db.GetArtifact(ctx, row.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if got := BlockedAt(after, time.Now().UTC()); got != "" {
		t.Errorf("the skip survived a declaration: %q", got)
	}
	if BlockedWhyOf(after) != "" || BlockedAtOf(after) != "" || BlockedByOf(after) != "" {
		t.Errorf("the skip's fields survived a declaration: %q %q %q",
			BlockedWhyOf(after), BlockedAtOf(after), BlockedByOf(after))
	}
}
