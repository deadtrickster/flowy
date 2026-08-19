package store

import (
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// THE CASE THE LOCK WAS BLIND TO: one seat, two processes, two rows.
//
// Every subagent runs under its parent seat's token, so `holder` alone is the
// same value for both. Before the item column the second take RENEWED the
// first's lock and a land through it was allowed - which is not a hypothetical:
// on 18 Aug a sibling session finished its own landing, released, and deleted a
// live holder's lock, invalidating a green verdict mid-flight.

func TestOneSeatTwoRowsCannotRenewEachOthersLock(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	seat := &Principal{UserID: "u-seat", Project: project}

	first, err := db.TakeMergeLock(ctx, seat, project, target, "row-one")
	if err != nil {
		t.Fatalf("the first process takes the target: %v", err)
	}
	if first.Item != "row-one" {
		t.Errorf("the lock does not record what it is held for: %q", first.Item)
	}

	// SAME PRINCIPAL, different work. This is the whole finding.
	_, err = db.TakeMergeLock(ctx, seat, project, target, "row-two")
	var held *ErrTargetHeld
	if !errors.As(err, &held) {
		t.Fatalf("a sibling of the same seat took the target for other work: %v", err)
	}
	if held.Held == nil || held.Held.Item != "row-one" {
		t.Fatalf("the refusal does not say which work holds it: %+v", held.Held)
	}

	// Same principal, SAME work: a re-gate after a rebase is the same work
	// measured again and must renew, or every rebase would deadlock on itself.
	if _, err := db.TakeMergeLock(ctx, seat, project, target, "row-one"); err != nil {
		t.Fatalf("the holder's own re-declare of the same row was refused: %v", err)
	}

	// And a sibling cannot release what it did not take, which is the exact
	// shape of the incident: release ran, matched on holder, deleted a lock
	// that belonged to another process of the same seat.
	gone, err := db.ReleaseMergeLock(ctx, seat, project, target, "row-two")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if gone {
		t.Fatal("a sibling released a lock taken for different work")
	}
	still, err := db.MergeLockOf(ctx, project, target)
	if err != nil || still == nil || still.Item != "row-one" {
		t.Fatalf("the lock did not survive the sibling's release: %+v %v", still, err)
	}
}

// The consequence at the door that matters: landing.
func TestALandThroughASiblingsLockIsRefused(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	seat := &Principal{UserID: "u-seat", Project: project}

	mine := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: seat.UserID, Title: "my branch", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-mine", TargetField: target}),
	}
	theirs := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: seat.UserID, Title: "the sibling's branch", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-theirs", TargetField: target}),
	}
	for _, a := range []*Artifact{mine, theirs} {
		if err := db.UpsertArtifact(ctx, a); err != nil {
			t.Fatalf("file %s: %v", a.Title, err)
		}
	}

	// The sibling declares and holds the target.
	if _, _, err := db.SetMergeGate(ctx, seat, theirs.ID, "run-theirs", "", ""); err != nil {
		t.Fatalf("the sibling's declaration: %v", err)
	}
	// My row has a verdict but never took the target - which used to be enough,
	// because the lock said "held by this seat" and I am this seat.
	if _, _, err := db.SetMergeGate(ctx, seat, mine.ID, "run-mine", "abc1234def5678", ""); err != nil {
		// The declaration inside SetMergeGate must lose to the sibling's lock;
		// recording a verdict on a row that never declared is the setup here,
		// so a refusal is the correct outcome and this test proves the door
		// below rather than this line.
		t.Logf("recording my verdict was refused as expected: %v", err)
	}

	_, _, err := db.LandMerge(ctx, seat, mine.ID, "abc1234def5678")
	if err == nil {
		t.Fatal("a land went through a lock taken for a different merge request")
	}
	var refused *ErrLandRefused
	if errors.As(err, &refused) && refused.Held != nil && refused.Held.Item != theirs.ID {
		t.Errorf("the refusal names the wrong holder: %+v", refused.Held)
	}
}

// A lock taken before the column existed carries no item, and its holder must
// still be able to give it back. Refusing there would strand every lock held
// across the deploy that added this, for the full expiry, with nothing anybody
// could do - the freeze the abandon door was built to end, reintroduced by its
// own fix.
func TestALegacyLockWithNoItemIsStillReleasableByItsHolder(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	seat := &Principal{UserID: "u-seat", Project: project}

	if _, err := db.TakeMergeLock(ctx, seat, project, target, "row-one"); err != nil {
		t.Fatalf("take: %v", err)
	}
	// Age it back to the pre-column shape directly, which is what a lock held
	// across the deploy actually looks like.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE merge_locks SET item = '' WHERE target = $1`, target); err != nil {
		t.Fatalf("blank the item: %v", err)
	}

	gone, err := db.ReleaseMergeLock(ctx, seat, project, target, "row-one")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !gone {
		t.Fatal("a lock from before the item column could not be released by its holder")
	}
}
