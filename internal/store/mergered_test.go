package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestARedVerdictEndsTheDeclarationAndDoesNotAdmitTheBranch is the third case
// applyGate did not have, in the two properties that matter.
//
// It is driven at the field level, without a database, for the reason applyGate
// and GatingAt already are: the whole content of a gate moment is which fields
// end up set, and the defect this fixes was invisible for a day because that
// logic could only be exercised through a door needing a live run.
func TestARedVerdictEndsTheDeclarationAndDoesNotAdmitTheBranch(t *testing.T) {
	now := time.Now().UTC()
	fields := map[string]any{}

	// A declaration: gating, no verdict either way.
	if !applyGate(fields, "run-1", "", now) {
		t.Fatal("a gate with no tip is a declaration")
	}
	art := gateItem(t, fields)
	if !GatingAt(art, now.Add(time.Minute)) {
		t.Fatal("a declaration does not read as gating")
	}

	// The red. The declaration is over, and the branch is no more landable than
	// it was before anybody measured it.
	applyRed(fields, "run-1", "ABC1234DEF5678", "647/5, the login check", now)
	art = gateItem(t, fields)

	if GatingAt(art, now.Add(time.Minute)) {
		t.Error("a row nothing is gating still reads GATING - the run ended, red, " +
			"and the queue shows a finished failed run as work in progress")
	}
	if got := RedTipOf(art); got != "abc1234def5678" {
		t.Errorf("red tip %q, want the measured tip normalised", got)
	}
	if RedAtOf(art) == "" {
		t.Error("a red with no time on it cannot be told from one three landings ago")
	}
	if !strings.Contains(RedNoteOf(art), "647/5") {
		t.Errorf("the note is %q, and it is what a reader has instead of the log", RedNoteOf(art))
	}
	if GatedTipOf(art) != "" {
		t.Fatal("a red wrote gated_tip - MergeAdmissible reads that as evidence FOR " +
			"landing, so the broken branch just became landable")
	}
	if err := MergeAdmissible(art, "abc1234def5678"); err == nil {
		t.Fatal("a branch whose only verdict is red is admissible")
	} else if !strings.Contains(err.Error(), "no gate has measured it") {
		// The honest answer for a branch with no green, and the reason
		// MergeAdmissible needs no change at all.
		t.Errorf("refused with %q, want the ungated answer", err)
	}

	// A NEW DECLARATION REPLACES THE EVIDENCE, red included. A red left behind
	// would outlive the run that found it and describe a tree this run is not
	// measuring.
	if !applyGate(fields, "run-2", "", now.Add(time.Hour)) {
		t.Fatal("the re-declaration is a declaration")
	}
	art = gateItem(t, fields)
	if RedTipOf(art) != "" || RedAtOf(art) != "" || RedNoteOf(art) != "" {
		t.Errorf("the old red survived a re-declaration: tip %q at %q note %q",
			RedTipOf(art), RedAtOf(art), RedNoteOf(art))
	}
	if !GatingAt(art, now.Add(time.Hour).Add(time.Minute)) {
		t.Error("the re-declaration does not read as gating")
	}
}

// TestARedIsRecordedByTheHolderAndRefusedFromAnybodyElse is the lock half.
//
// A red from somebody who never held the target is the same forgery as a green
// from them, and worse in one way: it tells the queue a branch is broken on the
// word of a run nobody declared. So the red takes the VERDICT branch of the lock
// rule - renew, and refuse when there was nothing to renew - rather than the
// declaring branch, which would let a red STEAL the target from whoever is
// measuring.
func TestARedIsRecordedByTheHolderAndRefusedFromAnybodyElse(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)

	holder := &Principal{UserID: "u-red-holder", Project: project}
	rival := &Principal{UserID: "u-red-rival", Project: project}
	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: holder.UserID, Title: "one branch", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-red", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file the merge request: %v", err)
	}

	// A red names the tree it measured. Without one it is a rumour, and the next
	// run cannot tell whether it is about to measure the same one.
	if _, _, err := db.SetMergeRed(ctx, holder, row.ID, "run1", "", "", ""); err == nil {
		t.Fatal("a red with no tip was recorded")
	}

	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run1", "", ""); err != nil {
		t.Fatalf("declare the run: %v", err)
	}
	if _, _, err := db.SetMergeRed(ctx, rival, row.ID, "run2", "abc1234def5678", "", "not mine"); err == nil {
		t.Fatal("a red from a principal who never held the target was recorded")
	}

	art, entry, err := db.SetMergeRed(ctx, holder, row.ID, "run1", "ABC1234DEF5678", "", "647/5")
	if err != nil {
		t.Fatalf("the holder's red was refused: %v", err)
	}
	if RedTipOf(art) != "abc1234def5678" {
		t.Errorf("red tip %q on the row", RedTipOf(art))
	}
	if GatedTipOf(art) != "" {
		t.Fatal("the red wrote gated_tip, so the broken branch is landable")
	}
	if GatingAt(art, time.Now().UTC()) {
		t.Error("the row still reads GATING after the run reported")
	}
	// The entry is the record; the fields are its projection. A field is
	// superseded by the next declaration and the entry never is, which is what
	// makes "has this tree ever been measured" answerable later.
	if entry == nil || entry.Type != EventMergeGate {
		t.Fatalf("the red left no entry in the log: %+v", entry)
	}
	var meta map[string]string
	if err := json.Unmarshal(entry.Meta, &meta); err != nil {
		t.Fatalf("entry meta: %v", err)
	}
	if meta["result"] != "red" {
		t.Errorf("the entry says result %q", meta["result"])
	}
	if meta[RedTipField] != "abc1234def5678" {
		t.Errorf("the entry does not name the tree it measured: %v", meta)
	}
	if _, ok := meta[GatedTipField]; ok {
		t.Error("the red rode the gated_tip key, which every reader treats as the tree that passed")
	}
	if !strings.Contains(entry.Body, "did not pass") {
		t.Errorf("the entry body reads %q", entry.Body)
	}

	// THE TARGET IS GIVEN BACK. A green holds the lock because the land follows
	// it; a red is the end of the run, and a target still held after a failure
	// blocks every other row until the lease expires - one failed pass stalling
	// a whole queue is the outage shape rather than the defect shape.
	lock, err := db.MergeLockOf(ctx, target)
	if err != nil {
		t.Fatalf("read the lock: %v", err)
	}
	if lock != nil && lock.Live(time.Now().UTC()) {
		t.Errorf("the target is still held by %s after the run reported red - "+
			"every other row waits for a lease nobody is using", lock.Holder)
	}
	// And it is takeable, which is the property that matters to the next row
	// rather than the absence of a struct.
	rivalRow := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: rival.UserID, Title: "the row behind it", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-next", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, rivalRow); err != nil {
		t.Fatalf("file the next request: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, rival, rivalRow.ID, "run3", "", ""); err != nil {
		t.Fatalf("the next row could not declare after a red: %v", err)
	}

	// And the land door still refuses it, which is the property MergeAdmissible
	// needed no change to keep.
	if _, _, err := db.LandMerge(ctx, holder, row.ID, "abc1234def5678"); err == nil {
		t.Fatal("a branch whose only verdict is red was landed")
	}
}
