package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// abandonRow files a merge request on a fresh target and declares a run on it,
// which is the state every test below starts from: the lock taken by a gate
// that has not landed. That is exactly the state the verb exists for and the
// one the system could not leave.
func abandonRow(t *testing.T, project, target string) (*Artifact, *Principal) {
	t.Helper()
	p := &Principal{UserID: "u-holder", Project: project}
	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: p.UserID, Title: "a branch that will not land", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-red", TargetField: target}),
	}
	return row, p
}

func TestAHolderGivesTheTargetBackWithoutLanding(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	row, holder := abandonRow(t, project, target)
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file the merge request: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run-red", "", ""); err != nil {
		t.Fatalf("declare: %v", err)
	}

	// The state this verb exists to end: held, and nothing but a land or the
	// expiry could give it back.
	held, err := db.MergeLockOf(ctx, project, target)
	if err != nil || held == nil {
		t.Fatalf("the declaration did not take the target: %+v %v", held, err)
	}

	art, entry, err := db.AbandonMerge(ctx, holder, row.ID, "gate went red on the vendored fixture")
	if err != nil {
		t.Fatalf("the holder could not give back its own target: %v", err)
	}
	if art == nil || entry == nil {
		t.Fatalf("abandon returned no row or no event: %+v %+v", art, entry)
	}

	// The release happened.
	after, err := db.MergeLockOf(ctx, project, target)
	if err != nil {
		t.Fatalf("read the lock after the abandon: %v", err)
	}
	if after != nil {
		t.Fatalf("the target is still held after its holder gave it back: %+v", after)
	}

	// And the reason is IN THE LOG, which is the half that separates this from
	// a bare unlock. A release whose only trace is a missing row cannot be told
	// apart from an expiry, and those two mean opposite things.
	if entry.Type != EventMergeAbandon {
		t.Errorf("event type = %q, want %q", entry.Type, EventMergeAbandon)
	}
	if !strings.Contains(entry.Body, "vendored fixture") {
		t.Errorf("the event does not carry the reason: %q", entry.Body)
	}
	if !strings.Contains(string(entry.Meta), "run-red") {
		t.Errorf("the event does not name the run that failed: %s", entry.Meta)
	}

	// The row is NOT closed and NOT stripped: an abandoned attempt is not a
	// withdrawn request, and the branch still wants to land.
	again, err := db.readWorkItem(ctx, holder, row.ID)
	if err != nil {
		t.Fatalf("read the row back: %v", err)
	}
	if again.Status == DoneStatus {
		t.Error("abandoning an attempt closed the request - the branch still wants to land")
	}
	if BranchOf(again) != "feat-red" {
		t.Errorf("the branch was disturbed: %q", BranchOf(again))
	}

	// AND THE DECLARATION IS OVER, which is the half that was missing. The row
	// kept gate_run and gate_at after an abandon, so GatingAt went on answering
	// true and the queue went on showing a run that had explicitly stopped -
	// blocking two landings inside twenty minutes, then healing itself when the
	// fifteen-minute belief window lapsed, which is why nothing caught it.
	//
	// Asserted through GatingAt at the instant of the abandon rather than
	// against the fields, because the flag is what the queue and the land door
	// actually read, and a row can carry any fields it likes as long as the
	// answer to "is somebody measuring this" is no.
	if GatingAt(again, time.Now().UTC()) {
		t.Error("the row still reads as gating after its runner said it stopped")
	}
	if GateRunOf(again) != "" {
		t.Errorf("the abandoned declaration left its run on the row: %q", GateRunOf(again))
	}

	// The target is free for the next declarer, which is the whole point.
	rival := &Principal{UserID: "u-next", Project: project}
	if _, err := db.TakeMergeLock(ctx, rival, project, target, "some-other-row"); err != nil {
		t.Fatalf("the next declarer could not take the freed target: %v", err)
	}
}

// A reason is not optional. Without it this door is the bare unlock it exists
// instead of, and the log learns nothing the expiry did not already say.
func TestAnAbandonWithNoReasonIsRefused(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	row, holder := abandonRow(t, project, target)
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run-red", "", ""); err != nil {
		t.Fatalf("declare: %v", err)
	}

	_, _, err := db.AbandonMerge(ctx, holder, row.ID, "   ")
	var refused *ErrAbandonRefused
	if !errors.As(err, &refused) {
		t.Fatalf("a reasonless abandon came back %v, want ErrAbandonRefused", err)
	}
	if !strings.Contains(refused.Error(), "reason") {
		t.Errorf("the refusal does not say what is missing: %q", refused.Error())
	}
	// And it refused before touching the lock, so a caller who forgot the
	// reason still holds their target rather than half-releasing it.
	still, err := db.MergeLockOf(ctx, project, target)
	if err != nil || still == nil {
		t.Fatalf("a refused abandon released the lock anyway: %+v %v", still, err)
	}
}

// Nobody hands back somebody else's reservation, exactly as nobody releases it
// and nobody deletes somebody else's reader.
func TestOnlyTheHolderAbandons(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	row, holder := abandonRow(t, project, target)
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run-red", "", ""); err != nil {
		t.Fatalf("declare: %v", err)
	}

	rival := &Principal{UserID: "u-rival", Project: project}
	_, _, err := db.AbandonMerge(ctx, rival, row.ID, "I would like this back")
	var refused *ErrAbandonRefused
	if !errors.As(err, &refused) {
		t.Fatalf("a stranger's abandon came back %v, want ErrAbandonRefused", err)
	}
	// The refusal NAMES the holder, for the same reason the gate's 409 does:
	// the caller is deciding whether to wait, and "held by somebody" is a room
	// with nobody in it to ask.
	if refused.Held == nil {
		t.Fatal("the refusal does not carry the lock, so it cannot name who holds it")
	}
	still, err := db.MergeLockOf(ctx, project, target)
	if err != nil || still == nil || still.Holder == "u-rival" {
		t.Fatalf("a stranger took or dropped the lock: %+v %v", still, err)
	}
}

// An EXPIRED lock still held by the caller is abandonable. That caller is
// precisely the principal whose expiry would otherwise be the only record of
// what happened, and replacing it with a stated reason is the point of the
// verb - refusing here would mean the longer you took, the less you could say.
func TestAnExpiredLockIsStillAbandonableByItsHolder(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	row, holder := abandonRow(t, project, target)
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run-red", "", ""); err != nil {
		t.Fatalf("declare: %v", err)
	}
	// Age it past its until rather than waiting fifteen minutes for it.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE merge_locks SET until = now() - interval '1 second' WHERE target = $1`,
		target); err != nil {
		t.Fatalf("age the lock: %v", err)
	}

	_, entry, err := db.AbandonMerge(ctx, holder, row.ID, "run died in the VM, saying so late rather than never")
	if err != nil {
		t.Fatalf("the holder of an expired lock could not say why: %v", err)
	}
	// And the log says the lock had already expired, so a reader can tell a
	// prompt abandon from a late one without doing arithmetic on timestamps.
	if !strings.Contains(string(entry.Meta), `"expired":"true"`) {
		t.Errorf("the event does not record that the lock had expired: %s", entry.Meta)
	}
}

// A target nobody holds has nothing to give back, and saying so beats a silent
// success that would let an agent believe it had released a lock it never took.
func TestAbandoningAFreeTargetIsRefused(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	row, holder := abandonRow(t, project, target)
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file: %v", err)
	}

	_, _, err := db.AbandonMerge(ctx, holder, row.ID, "never declared anything")
	var refused *ErrAbandonRefused
	if !errors.As(err, &refused) {
		t.Fatalf("abandoning a free target came back %v, want ErrAbandonRefused", err)
	}
	if !strings.Contains(refused.Error(), "nothing to give back") {
		t.Errorf("the refusal does not say the target was free: %q", refused.Error())
	}
}

// This door is about merge requests, and says nothing about rows the caller did
// not name - the same answer the gate and land verbs give a non-merge id.
func TestAbandoningSomethingThatIsNotAMergeRequestIsNotFound(t *testing.T) {
	ctx, db, project := lockCtx(t)
	p := &Principal{UserID: "u-holder", Project: project}
	todo := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "todo", Project: &project,
		OwnerUser: p.UserID, Title: "not a merge request", Visibility: "project",
	}
	if err := db.UpsertArtifact(ctx, todo); err != nil {
		t.Fatalf("file the todo: %v", err)
	}
	if _, _, err := db.AbandonMerge(ctx, p, todo.ID, "why not"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("abandoning a todo came back %v, want ErrNotFound", err)
	}
}
