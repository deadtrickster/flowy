package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The pure half first, exactly as the gate's tests split: the reading must not
// need a run to be alive.

func TestALiveLockIsLiveAndADeadOneIsNot(t *testing.T) {
	now := time.Now()
	live := &MergeLock{Target: "master", Holder: "a1", Until: now.Add(time.Minute)}
	if !live.Live(now) {
		t.Fatal("a lock whose until is in the future is live")
	}
	dead := &MergeLock{Target: "master", Holder: "a1", Until: now.Add(-time.Second)}
	if dead.Live(now) {
		t.Fatal("a lock past its until must not be believed")
	}
	var absent *MergeLock
	if absent.Live(now) {
		t.Fatal("no lock reads as held")
	}
}

// The refusal names the holder and the time, because "wait" without a until is
// "wait forever" and "somebody" without a name is a room nobody can ask.
func TestAHeldTargetNamesItsHolderAndItsUntil(t *testing.T) {
	now := time.Now()
	held := &ErrTargetHeld{
		Target: "master",
		Held: &MergeLock{
			Target: "master", Holder: "a1", HolderName: "flowy-glm",
			Until: now.Add(10 * time.Minute),
		},
		Now: now,
	}
	msg := held.Error()
	for _, want := range []string{"master", "flowy-glm", "held by"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not say %q", msg, want)
		}
	}
}

// A gate row carries where its evidence lives, and who declared it - the two
// halves the lock's answers need.
func TestARefAndActorRideTheGateRow(t *testing.T) {
	a := gateItem(t, map[string]any{
		GateRunField:   "run1",
		GateRefField:   "integration/claude-host-2",
		GateActorField: "a1",
	})
	if GateRefOf(a) != "integration/claude-host-2" {
		t.Errorf("gated_ref = %q", GateRefOf(a))
	}
	if GateActorOf(a) != "a1" {
		t.Errorf("gate_actor = %q", GateActorOf(a))
	}
	// Absent is empty, not a guess: a row gated before refs existed read as
	// its own branch, which is what it was.
	if got := GateRefOf(gateItem(t, map[string]any{GateRunField: "run1"})); got != "" {
		t.Errorf("absent gated_ref = %q, want empty", got)
	}
}

// The live half: the compare-and-set, the takeover, and the verb wiring. These
// need the database the gate starts.

// lockCtx opens the database and declares this test's own project, so no two
// runs can leave rows that make the other pass or fail by accident.
func lockCtx(t *testing.T) (context.Context, *DB, string) {
	t.Helper()
	ctx, db := open(t)
	return ctx, db, declaredProject(t, ctx, db, "ml")
}

// ownTarget is this test's private target. The lock table keys on the target,
// and a shared "master" would be one test's holder refusing another's - a
// fresh name per test is the same discipline as a fresh project.
func ownTarget(t *testing.T) string {
	t.Helper()
	return "master-" + ulid.NewString()
}

// takeBy is TakeMergeLock for one principal, so every test below exercises the
// real compare-and-set rather than a shared shortcut.
func takeBy(t *testing.T, ctx context.Context, db *DB, actor, target string) (*MergeLock, error) {
	t.Helper()
	// Each principal here declares its own work, which is the ordinary case:
	// one seat, one row. The same-seat-different-row case has its own test.
	return db.TakeMergeLock(ctx, &Principal{UserID: actor, Project: "ml"}, target, "item-"+actor)
}

func TestASecondDeclarerLosesAndIsToldWhoHolds(t *testing.T) {
	ctx, db, _ := lockCtx(t)
	target := ownTarget(t)

	first, err := takeBy(t, ctx, db, "u-first", target)
	if err != nil {
		t.Fatalf("the first declarer takes the target: %v", err)
	}
	_, err = takeBy(t, ctx, db, "u-second", target)
	var held *ErrTargetHeld
	if !errors.As(err, &held) {
		t.Fatalf("the second declarer came back %v, want ErrTargetHeld", err)
	}
	if held.Held == nil || held.Held.Holder != first.Holder {
		t.Fatalf("the refusal does not name the holder: %+v", held.Held)
	}

	// The holder's own renewal wins: a re-declare is the same principal
	// measuring again, not a rival.
	if _, err := takeBy(t, ctx, db, "u-first", target); err != nil {
		t.Fatalf("the holder's own re-declare was refused: %v", err)
	}
	// Non-holders cannot release what they do not hold: the release reports
	// nothing gone, and the lock reads back still held by the first.
	gone, err := db.ReleaseMergeLock(ctx, &Principal{UserID: "u-second"}, target, "item-u-second")
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if gone {
		t.Fatal("a non-holder released somebody else's lock")
	}
	still, err := db.MergeLockOf(ctx, target)
	if err != nil || still == nil || still.Holder != first.Holder {
		t.Fatalf("the lock did not survive the non-holder's release: %+v %v", still, err)
	}
}

func TestAnExpiredLockLosesToTheNextDeclarer(t *testing.T) {
	ctx, db, _ := lockCtx(t)
	target := ownTarget(t)

	if _, err := takeBy(t, ctx, db, "u-first", target); err != nil {
		t.Fatalf("take: %v", err)
	}
	// Age the row past its until directly: the belief window is the expiry's
	// business, and no test should wait fifteen minutes for it.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE merge_locks SET until = now() - interval '1 second' WHERE target = $1`,
		target); err != nil {
		t.Fatalf("age the lock: %v", err)
	}
	if _, err := takeBy(t, ctx, db, "u-second", target); err != nil {
		t.Fatalf("an expired lock must lose to the next declarer: %v", err)
	}
}

// The declaration takes the target, which is the piece the night was missing:
// gating reserved nothing, so every honest run raced every landing.
func TestADeclarationTakesTheTargetAndRefusesItsRival(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)

	first := &Principal{UserID: "u-first", Project: project}
	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: first.UserID, Title: "one branch", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-one", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file the merge request: %v", err)
	}

	if _, _, err := db.SetMergeGate(ctx, first, row.ID, "run1", "", ""); err != nil {
		t.Fatalf("the declaration must take the target: %v", err)
	}
	rival := &Principal{UserID: "u-second", Project: project}
	_, _, err := db.SetMergeGate(ctx, rival, row.ID, "run2", "", "")
	var held *ErrTargetHeld
	if !errors.As(err, &held) {
		t.Fatalf("a rival declaration came back %v, want ErrTargetHeld", err)
	}
}

// The land verb's refusals, each a different sentence: no verdict, no lock,
// somebody else's lock - and then the happy path, which records the tip,
// closes the row, advances the chain and releases the lock.
func TestLandRefusesAndThenLands(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)

	holder := &Principal{UserID: "u-holder", Project: project}
	other := &Principal{UserID: "u-other", Project: project}
	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: holder.UserID, Title: "one branch", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-one", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file the merge request: %v", err)
	}

	// No verdict: nothing measured it, and a land is not the place to discover
	// that.
	if _, _, err := db.LandMerge(ctx, holder, row.ID, "abc1234"); err == nil {
		t.Fatal("a land without a verdict succeeded")
	}
	// Declare, then the verdict: the declaration is what takes the target, so
	// a land that skips it skips the exclusivity.
	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run1", "", ""); err != nil {
		t.Fatalf("declare the run: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run1", "abc1234def5678", ""); err != nil {
		t.Fatalf("record the verdict: %v", err)
	}
	if _, _, err := db.LandMerge(ctx, other, row.ID, "abc1234def5678"); err == nil {
		t.Fatal("a land by a principal who holds no lock succeeded")
	}

	// The holder, landing a sha that is not the one measured. Refused: a
	// fast-forward puts the MEASURED tip on the target, so a different sha
	// means something else landed - which is exactly what a partial land looks
	// like from the node's side.
	if _, _, err := db.LandMerge(ctx, holder, row.ID, "9999999abcdef"); err == nil {
		t.Fatal("a land of a sha the gate never measured succeeded")
	}

	// The holder lands: the row carries what master became, the chain knows
	// the target's newest tip, and the lock is gone.
	art, _, err := db.LandMerge(ctx, holder, row.ID, "abc1234def5678")
	if err != nil {
		t.Fatalf("the holder's land: %v", err)
	}
	if landedTipOfRow(art) != "abc1234def5678" {
		t.Errorf("landed_tip = %q", landedTipOfRow(art))
	}
	if art.Status != DoneStatus {
		t.Errorf("status = %q, want done", art.Status)
	}
	chain, err := db.LandedTipOf(ctx, target)
	if err != nil || chain == nil || chain.Tip != "abc1234def5678" {
		t.Fatalf("the chain did not advance: %+v %v", chain, err)
	}
	lock, err := db.MergeLockOf(ctx, target)
	if err != nil {
		t.Fatalf("read the lock after land: %v", err)
	}
	if lock != nil {
		t.Fatalf("the lock survived its own land: %+v", lock)
	}
}

// landedTipOfRow reads the landed tip off a row the verb returned, for the
// assertions above without a second fetch.
func landedTipOfRow(a *Artifact) string {
	return normalizeTip(artifactString(a, LandedTipField))
}

func marshalFields(t *testing.T, fields map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	return raw
}

// ONE CLOCK STAMPS AND JUDGES. `until` was computed in Go while taken_at and the
// expiry test are the database's, so the deadline was written by one clock and
// judged by another. Under skew that is wrong in both directions and silently:
// a Go clock behind writes an `until` already past, so the lock is expired the
// moment it is taken and holds nothing; a Go clock ahead lets a dead holder
// freeze the target for the skew plus the window. The symptom either way is
// collisions returning, which reads as the lock not working rather than as a
// clock, and this is the primitive everything else now trusts.
//
// The window is measurable from the row itself: with one clock, until-taken_at
// is EXACTLY MergeLockBelievedFor. With two it is off by the skew plus a round
// trip, which is why an exact comparison is the right assertion here and a
// tolerance would defeat the whole point.
func TestTheLockWindowIsStampedByOneClock(t *testing.T) {
	ctx, db, _ := lockCtx(t)
	target := ownTarget(t)

	got, err := takeBy(t, ctx, db, "one-clock", target)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if window := got.Until.Sub(got.TakenAt); window != MergeLockBelievedFor {
		t.Fatalf("lock window = %v, want exactly %v - until and taken_at come "+
			"from different clocks, so the deadline drifts by the skew",
			window, MergeLockBelievedFor)
	}
}

// A VERDICT THAT NAMES THE BASE IS A DIFFERENT MISTAKE FROM A RACE, AND THE ROW
// CAN TELL THEM APART.
//
// Both arrive at the land door as "the sha you are landing is not the tip the
// gate measured", and the two want opposite work: a lander who copied the base
// out of their own shell has a green run in hand and needs to re-record, while
// a lander whose target moved has no verdict for what is on it now and needs to
// re-gate. The refusal that says "one or the other" is correct and costs the
// reader a diagnosis - measured on a landing of my own, which is what sent this
// here.
//
// It is decidable without git: gated_base is what the target was when the run
// started, and the tip equalling the base means the verdict named the ground
// the run stood on rather than the tree it measured. Ancestry in general is
// not decidable here and does not need to be - that stays with the lander, who
// has the repository.
func TestAVerdictNamingTheBaseIsToldSo(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	holder := &Principal{UserID: "u-holder", Project: project}

	// A first landing, so the target has a base for the second run to be
	// declared from. A target nothing has landed on stamps no base, and this
	// refusal is only available to a row that carries one.
	first := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: holder.UserID, Title: "the ground", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-ground", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, first); err != nil {
		t.Fatalf("file the first request: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, first.ID, "run0", "", ""); err != nil {
		t.Fatalf("declare the first run: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, first.ID, "run0", "1111111aaaaaa", ""); err != nil {
		t.Fatalf("record the first verdict: %v", err)
	}
	if _, _, err := db.LandMerge(ctx, holder, first.ID, "1111111aaaaaa"); err != nil {
		t.Fatalf("the first land: %v", err)
	}

	second := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: holder.UserID, Title: "the tree", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-tree", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, second); err != nil {
		t.Fatalf("file the second request: %v", err)
	}
	declared, _, err := db.SetMergeGate(ctx, holder, second.ID, "run1", "", "")
	if err != nil {
		t.Fatalf("declare the second run: %v", err)
	}
	if base := GatedBaseOf(declared); base != "1111111aaaaaa" {
		t.Fatalf("gated_base = %q, want the tip the first land put on the target", base)
	}

	// The verdict names the base. The run measured 2222222bbbbbb and the
	// lander wrote down where it started from.
	if _, _, err := db.SetMergeGate(ctx, holder, second.ID, "run1", "1111111aaaaaa", ""); err != nil {
		t.Fatalf("record the second verdict: %v", err)
	}
	_, _, err = db.LandMerge(ctx, holder, second.ID, "2222222bbbbbb")
	if err == nil {
		t.Fatal("landing a tip the verdict never named succeeded")
	}
	if !strings.Contains(err.Error(), "the base this run started from") {
		t.Fatalf("the refusal does not name the mistake: %v", err)
	}

	// AND THE OTHER SHAPE STILL READS AS THE OTHER SHAPE. A verdict naming
	// neither the base nor what landed is a race, and must not be described as
	// somebody's typo - the two ask for opposite work.
	if _, _, err := db.SetMergeGate(ctx, holder, second.ID, "run1", "3333333ccccc", ""); err != nil {
		t.Fatalf("re-record the second verdict: %v", err)
	}
	_, _, err = db.LandMerge(ctx, holder, second.ID, "2222222bbbbbb")
	if err == nil {
		t.Fatal("landing a tip the verdict never named succeeded")
	}
	if strings.Contains(err.Error(), "the base this run started from") {
		t.Fatalf("a race was described as a mis-recorded verdict: %v", err)
	}
}
