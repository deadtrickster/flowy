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

// TestAnUncheckedBlockIsNotAClearRow: "nothing is blocking this" and "nobody
// has looked lately" are different answers and must not arrive as one value.
//
// BlockedAt returned "" for both, which made a row whose branch was still
// checked out in somebody's worktree read exactly like a row with nothing wrong
// with it. That is the empty-vs-absent collapse, inside the guard whose whole
// job is to report the thing being collapsed.
//
// It fails in the dangerous direction, which is why it is worth a change of its
// own. A stale RED makes a row look stuck when it is not, and somebody
// eventually goes and looks. An expired BLOCK makes a stuck row look fine, and
// nobody does - measured on 2026-08-20, when a row of mine sat unrebasable for
// over an hour behind a worktree I had forgotten.
func TestAnUncheckedBlockIsNotAClearRow(t *testing.T) {
	now := time.Now().UTC()
	fields, err := json.Marshal(map[string]any{
		BlockedWhyField: "feat/x is checked out in /home/dead/Projects/wt-drain",
		BlockedAtField:  now.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	blocked := &Artifact{Fields: fields}

	// Fresh: a reason, vouched for.
	why, fresh := BlockedNow(blocked, now.Add(time.Minute))
	if why == "" || !fresh {
		t.Errorf("a skip one minute old reads as (%q, %v), want the reason and fresh", why, fresh)
	}

	// Aged out: THE SAME REASON, and nobody standing behind it. The reason must
	// survive - a reader that is told nothing cannot tell this row from a clear
	// one, which is the whole defect.
	why, fresh = BlockedNow(blocked, now.Add(BlockBelievedFor+time.Minute))
	if why == "" {
		t.Errorf("a skip %v old lost its reason - the row now reads as clear", BlockBelievedFor)
	}
	if fresh {
		t.Errorf("a skip %v old still claims somebody vouched for it", BlockBelievedFor)
	}

	// No block at all: nothing, and not fresh. This is the answer the other two
	// must not be confusable with.
	if why, fresh := BlockedNow(&Artifact{}, now); why != "" || fresh {
		t.Errorf("a row with no skip reads as (%q, %v), want empty and not fresh", why, fresh)
	}

	// AND THE OLD CONTRACT IS UNCHANGED. BlockedAt is the fresh-only answer and
	// callers that want exactly that still get it, so this change adds a
	// question rather than altering one.
	if got := BlockedAt(blocked, now.Add(BlockBelievedFor+time.Minute)); got != "" {
		t.Errorf("BlockedAt returned %q for an aged-out skip - its meaning moved", got)
	}
	if got := BlockedAt(blocked, now.Add(time.Minute)); got == "" {
		t.Errorf("BlockedAt lost a fresh skip")
	}
}

// TestAFixedBlockIsWithdrawnByWhoeverFixedIt is the other half of a skip: the
// caller that could not take the row records why, and the caller that fixes it
// says so.
//
// MEASURED 2026-08-20, by walking into it. A row was blocked with "conflicts
// with master as it is now - a person resolves this, the drainer cannot". The
// person did: rebased, verified, released the worktree - and the row went on
// saying it conflicted, because the only two things that retired a reason were
// the fifteen-minute window and a declaration, and declaring takes the landing
// lock, which another seat's gate held for another twelve minutes. So the fix
// existed and there was nowhere to put it, through exactly the window when
// other agents were reading the queue to decide what to pick up.
//
// The three properties this asserts are the three the door was written for: it
// takes NO LOCK, ANY caller may clear a block somebody else reported, and the
// withdrawn reason survives in the log after the fields have let go of it.
func TestAFixedBlockIsWithdrawnByWhoeverFixedIt(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)

	drainer := &Principal{UserID: "u-drainer", Project: project}
	person := &Principal{UserID: "u-person", Project: project}
	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: "u-author", Title: "a branch that conflicted until it did not",
		Visibility: "project",
		Fields:     marshalFields(t, map[string]any{BranchField: "feat-rebased", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file the merge request: %v", err)
	}

	// NOTHING TO CLEAR IS ITS OWN ANSWER. "There was no block on this row" and
	// "the block is gone now" are different facts, and a door that returned
	// success for both would let a caller believe it had withdrawn a reason that
	// some other seat had just replaced with a fresher one.
	if _, _, err := db.SetMergeUnblocked(ctx, person, row.ID, "rebased it"); err == nil {
		t.Fatal("a row with no skip was unblocked anyway")
	}

	why := "conflicts with master as it is now - a person resolves this, the drainer cannot"
	if _, _, err := db.SetMergeBlocked(ctx, drainer, row.ID, why); err != nil {
		t.Fatalf("record the skip: %v", err)
	}

	// A withdrawal with no account of itself is the silence the block was
	// written down to end, one field further along.
	if _, _, err := db.SetMergeUnblocked(ctx, person, row.ID, "  "); err == nil {
		t.Fatal("a block was cleared with no reason")
	}

	// A DIFFERENT SEAT CLEARS IT than the one that set it, deliberately: the
	// drainer that reports a skip has no repository to fix anything in.
	fixed := "rebased onto 0bf19e5 and released the worktree"
	art, entry, err := db.SetMergeUnblocked(ctx, person, row.ID, fixed)
	if err != nil {
		t.Fatalf("clear the skip: %v", err)
	}
	now := time.Now().UTC()
	if got, fresh := BlockedNow(art, now); got != "" || fresh {
		t.Errorf("the row still reads as blocked: (%q, %v)", got, fresh)
	}
	for _, f := range blockedFields {
		if v := strings.TrimSpace(artifactString(art, f)); v != "" {
			t.Errorf("%s survived the withdrawal as %q", f, v)
		}
	}

	// THE WITHDRAWN REASON SURVIVES IN THE LOG. The fields say what is true now
	// and no longer hold it, so an entry recording only the fix would leave
	// "what was it that stopped them" answerable only by hunting for the last
	// merge.blocked and hoping it was the one.
	if entry == nil || entry.Type != EventMergeUnblocked {
		t.Fatalf("the withdrawal left no entry in the log: %+v", entry)
	}
	var meta map[string]string
	if err := json.Unmarshal(entry.Meta, &meta); err != nil {
		t.Fatalf("entry meta: %v", err)
	}
	if meta["unblocked_why"] != fixed {
		t.Errorf("the entry does not say what was fixed: %v", meta)
	}
	if meta[BlockedWhyField] != why {
		t.Errorf("the entry does not carry the reason it withdrew: %v", meta)
	}
	if meta[BlockedByField] != drainer.UserID {
		t.Errorf("the entry does not say whose reason it withdrew: %v", meta)
	}
	if !strings.Contains(entry.Body, fixed) || !strings.Contains(entry.Body, why) {
		t.Errorf("the entry body reads %q - it should carry both ends", entry.Body)
	}

	// AND IT TAKES NO LOCK, which is the reason the door exists at all: the act
	// that retires a false reason must not require the thing whose absence is
	// being reported. A person holding the lock is a person who could have
	// declared instead.
	lock, err := db.MergeLockOf(ctx, project, target)
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}
	if lock != nil && lock.Live(now) {
		t.Errorf("clearing a skip took the target: held by %s", lock.Holder)
	}

	// It read back from the store, not only from the value the call returned.
	after, err := db.GetArtifact(ctx, row.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if BlockedWhyOf(after) != "" {
		t.Errorf("the skip is still on the stored row: %q", BlockedWhyOf(after))
	}
}
