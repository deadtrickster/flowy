package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// strayItem writes one stray work item and hands back its id.
func strayItem(t *testing.T, ctx context.Context, db *DB, project, title string) string {
	t.Helper()

	art := &Artifact{
		Type:       MemoryType,
		Kind:       WorkKind,
		Project:    &project,
		OwnerUser:  "01USER-OPERATOR",
		Title:      title,
		Visibility: VisibilityShared,
	}
	if err := db.WriteMemory(ctx, art); err != nil {
		t.Fatalf("write work item: %v", err)
	}
	return art.ID
}

// takenBy reads who holds an item, off the row.
func takenBy(t *testing.T, ctx context.Context, db *DB, id string) string {
	t.Helper()

	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	fields, err := ArtifactFields(art)
	if err != nil {
		t.Fatalf("fields of %s: %v", id, err)
	}
	return fieldText(fields, TakenField)
}

// THE ONE THAT MATTERS. Two agents take the same stray item at the same moment
// and exactly one of them may come away believing they own it - a claim that
// silently succeeds twice manufactures the confidence that makes both of them
// act, which is how the same e2fsck ran twice on one layer tonight.
func TestExactlyOneClaimerWinsAndTheLoserIsToldWho(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "workq")
	id := strayItem(t, ctx, db, project, "repair the shared layer")

	const racers = 8
	claimers := make([]*Principal, racers)
	for i := range claimers {
		claimers[i] = &Principal{
			UserID:  "01USER-RACER-" + string(rune('A'+i)),
			AgentID: "01AGENT-RACER-" + string(rune('A'+i)),
			Project: project,
		}
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		won   []string
		lost  []error
		start = make(chan struct{})
	)
	for _, p := range claimers {
		wg.Add(1)
		go func(p *Principal) {
			defer wg.Done()
			<-start
			_, _, err := db.ClaimWork(ctx, p, id)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won = append(won, p.AgentID)
			} else {
				lost = append(lost, err)
			}
		}(p)
	}
	close(start)
	wg.Wait()

	if len(won) != 1 {
		t.Fatalf("%d of %d claimers won; exactly one may - winners %v", len(won), racers, won)
	}
	if len(lost) != racers-1 {
		t.Fatalf("%d losers, want %d", len(lost), racers-1)
	}
	holder := takenBy(t, ctx, db, id)
	if holder != won[0] {
		t.Fatalf("the row says %q holds it and the winner was %q", holder, won[0])
	}
	// AND THE LOSER IS TOLD WHO WON, by name. A refusal that only says "no" is
	// one an agent cannot act on: with the name they can ask, or take something
	// else.
	for _, err := range lost {
		var taken ErrTakenBy
		if !errors.As(err, &taken) {
			t.Fatalf("a loser was refused with %v, want an ErrTakenBy naming the winner", err)
		}
		if taken.Holder != holder {
			t.Errorf("a loser was told %q holds it; the holder is %q", taken.Holder, holder)
		}
	}
}

// Retaking your own claim is not losing. An agent re-reading its queue after a
// restart must not be told it lost to itself, and the answer says nothing moved.
func TestRetakingYourOwnClaimSucceedsAndMovesNothing(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "workq")
	id := strayItem(t, ctx, db, project, "restart the relay")
	me := &Principal{UserID: "01USER-ME", AgentID: "01AGENT-ME", Project: project}

	if _, entry, err := db.ClaimWork(ctx, me, id); err != nil || entry == nil {
		t.Fatalf("first claim: entry=%v err=%v", entry, err)
	}
	_, entry, err := db.ClaimWork(ctx, me, id)
	if err != nil {
		t.Fatalf("retaking my own claim was refused: %v", err)
	}
	if entry != nil {
		t.Error("retaking my own claim wrote a second entry in the log")
	}
}

// OWNED work is bound to a principal and nobody else can do it, so taking it is
// refused rather than raced for.
func TestOwnedWorkCannotBeTakenBySomebodyElse(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "workq")
	id := strayItem(t, ctx, db, project, "push my own branch")

	owner := &Principal{UserID: "01USER-OWNER", AgentID: "01AGENT-OWNER", Project: project}
	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	fields, err := ArtifactFields(art)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	fields[BoundField] = owner.AgentID
	column := mustJSON(t, fields)
	if err := db.SetArtifactFields(ctx, art, column); err != nil {
		t.Fatalf("bind: %v", err)
	}

	other := &Principal{UserID: "01USER-OTHER", AgentID: "01AGENT-OTHER", Project: project}
	_, _, err = db.ClaimWork(ctx, other, id)
	var bound ErrBoundElsewhere
	if !errors.As(err, &bound) {
		t.Fatalf("somebody else's owned item was refused with %v, want ErrBoundElsewhere", err)
	}
	if _, _, err := db.ClaimWork(ctx, owner, id); err != nil {
		t.Fatalf("the owner could not take their own owned item: %v", err)
	}
}

// Done is a tombstone, not a delete: the row stays and says who did it and when,
// because "somebody did this" and "this never happened" are different answers.
func TestFinishingLeavesWhoDidItOnTheRow(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "workq")
	id := strayItem(t, ctx, db, project, "run the gate")
	me := &Principal{UserID: "01USER-ME", AgentID: "01AGENT-ME", Project: project}

	if _, _, err := db.ClaimWork(ctx, me, id); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, _, err := db.FinishWork(ctx, me, id); err != nil {
		t.Fatalf("finish: %v", err)
	}
	art, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("the item is gone after being finished: %v", err)
	}
	fields, err := ArtifactFields(art)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if got := fieldText(fields, DidField); got != me.AgentID {
		t.Errorf("the row says %q did it, want %q", got, me.AgentID)
	}
	if fieldText(fields, DidAtField) == "" {
		t.Error("the row does not say when it was done")
	}
	// And a finished item is not a queue entry any more.
	if _, _, err := db.ClaimWork(ctx, me, id); err == nil {
		t.Error("a finished item was claimable")
	}

	// AND THE BOARD AGREES. The row's `did` said finished while artifacts.status
	// still said todo, so every surface that reads the status went on drawing it
	// open - measured on the live node as 200 from /api/work/{id}/done with the
	// row unchanged. Two columns meaning one thing, and the one the board reads
	// was the one nobody wrote.
	after, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if after.Status != DoneStatus {
		t.Errorf("finished work reads status %q - the board still shows it open", after.Status)
	}
}

// Releasing puts it back for anybody, and only the holder may.
func TestOnlyTheHolderMayReleaseAndItGoesBackToTheQueue(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "workq")
	id := strayItem(t, ctx, db, project, "repair the layer")
	mine := &Principal{UserID: "01USER-ME", AgentID: "01AGENT-ME", Project: project}
	theirs := &Principal{UserID: "01USER-THEM", AgentID: "01AGENT-THEM", Project: project}

	if _, _, err := db.ClaimWork(ctx, mine, id); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, _, err := db.ReleaseWork(ctx, theirs, id); err == nil {
		t.Fatal("somebody who does not hold it released it")
	}
	if _, _, err := db.ReleaseWork(ctx, mine, id); err != nil {
		t.Fatalf("the holder could not release it: %v", err)
	}
	if holder := takenBy(t, ctx, db, id); holder != "" {
		t.Fatalf("after release the row still says %q holds it", holder)
	}
	if _, _, err := db.ClaimWork(ctx, theirs, id); err != nil {
		t.Fatalf("a released item could not be taken by somebody else: %v", err)
	}
}

// mustJSON marshals fields for a test, failing the test rather than the write.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// AND A ROW THAT NEVER HAD A STATUS FINISHES TO DONE, rather than to the empty
// string it started with.
//
// This is the shape the defect was reported in, and it is not the same row as
// the one above. Two merge rows were closed through this door two hours apart:
// one came out `done` and one came out EMPTY, and an empty status renders as
// pending on every surface - so a finished row read as open work for two hours
// and was reported as blocked. Calling the door again answered 200 and left it
// empty again.
//
// A merge row is filed with no status at all; a queue item is filed with todo.
// The move now runs on whatever the row's status is rather than on the one the
// queue expects, so the two arms differ in their starting state and agree at
// the end - which is the whole claim.
func TestFinishingARowThatNeverHadAStatus(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "workq")
	me := &Principal{UserID: "01USER-ME", AgentID: "01AGENT-ME", Project: project}

	art := &Artifact{
		Type: MemoryType, Kind: WorkKind, Project: &project,
		OwnerUser: "01USER-OPERATOR", Title: "a row filed with no status",
		Visibility: VisibilityShared,
	}
	if err := db.WriteMemory(ctx, art); err != nil {
		t.Fatalf("write the item: %v", err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE artifacts SET status = '' WHERE id = $1`, art.ID); err != nil {
		t.Fatalf("empty its status: %v", err)
	}
	before, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if before.Status != "" {
		t.Fatalf("the fixture starts at %q, so this arm is the other arm", before.Status)
	}

	if _, _, err := db.FinishWork(ctx, me, art.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	after, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("the item is gone after being finished: %v", err)
	}
	if after.Status != DoneStatus {
		t.Errorf("a finished row reads %q, want %q - an empty status draws as pending everywhere",
			after.Status, DoneStatus)
	}
}
