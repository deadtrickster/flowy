package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// THE LAND VERB: recording what actually landed, as the door that owns the
// lock's release.
//
// Tonight's partial lands were honest all the way through: a union branch went
// green, its row said "branch: feat-x", and the lander fast-forwarded what the
// ROW named - one commit of sixteen, the other fifteen stranded while three
// agents believed their work was in. The announcement and the tree disagreed
// and nobody lied. The queue could represent "this tip was measured" and "this
// row is about that branch", but never the relationship between them, which is
// the only thing that makes a landing safe.
//
// So landing is a verb now, and it takes the sha that MASTER BECAME. It refuses
// what the node can check without git - no verdict, no lock, somebody else's
// lock - and it records what only the lander knows: the tree that resulted.
// The reachability of the gated tip inside that tree stays with the lander, who
// has the repository: the declaration hands them gated_ref precisely so the
// tree they fast-forward is the tree that went green, and the protocol is
// merge-base before this call, not after.

// EventMergeLand is what a land leaves in the log.
const EventMergeLand = "merge.land"

// LandedTipField is what master became. It is recorded rather than inferred
// from the gate because the two can differ - that difference IS the partial
// land - and a queue that never writes it down cannot notice.
const LandedTipField = "landed_tip"

// ErrLandRefused is every way a land can be turned away, with the reason in
// words. It carries no code: the doors translate it, and every refusal below is
// a different sentence a caller can act on.
type ErrLandRefused struct {
	Reason string
	Held   *MergeLock
	Now    time.Time
}

func (e *ErrLandRefused) Error() string {
	if e.Held != nil {
		held := &ErrTargetHeld{Target: e.Held.Target, Held: e.Held, Now: e.Now}
		return fmt.Sprintf("store: land refused: %s (%s)", e.Reason, held)
	}
	return fmt.Sprintf("store: land refused: %s", e.Reason)
}

// LandMerge records a fast-forward of the request's target and releases the
// landing lock the declaration took.
//
// THE LOCK IS THE GATE ON THE DOOR: a land by a principal who does not hold it
// is refused with the holder's name, because a land that does not go through
// the lock is a land under somebody's run - the exact race the lock exists to
// end. An expired lock held by the caller still lands: expiry exists so a dead
// holder cannot freeze the target, not so a live verdict with a slow
// fast-forward dies at the door.
func (d *DB) LandMerge(ctx context.Context, p *Principal, id, sha string) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "merge.land")
	defer span.End()

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, fmt.Errorf("store: this token resolves to nobody, so it cannot land a merge")
	}
	art, err := d.readWorkItem(ctx, p, id)
	if err != nil {
		return nil, nil, err
	}
	if art.Kind != MergeKind {
		// Same answer as an id that is not here, for the same reason as the
		// gate verb: this door is about merge requests and says nothing about
		// rows the caller did not name.
		return nil, nil, ErrNotFound
	}

	sha = normalizeTip(sha)
	if len(sha) < minTipLen {
		return nil, nil, fmt.Errorf(
			"store: land refused: the sha master became is too short to name a commit - pass the full or 7-character tip, not %q", sha)
	}
	gated := GatedTipOf(art)
	if gated == "" {
		return nil, nil, &ErrLandRefused{
			Reason: "no gate has measured it - there is no verdict to land. Declare a run, wait for green, then land",
		}
	}

	// FAST-FORWARD ONLY, ENFORCED HERE RATHER THAN AGREED IN THE ROOM.
	//
	// If the landing is a fast-forward then the sha master became IS the tip the
	// gate measured - not a descendant of it, not a merge of it with something
	// else, the same commit. So the two disagreeing is not a detail to record,
	// it is the statement that something other than the measured tree is now on
	// master, and it is the whole of tonight's partial land: the row said
	// branch feat-x, the lander fast-forwarded one commit of sixteen, and the
	// queue wrote down both numbers without ever comparing them.
	//
	// The node has no git and needs none for this. It is not asking whether the
	// gated tip is reachable from what landed - that question needs the
	// repository and stays with the lander - it is asking whether the lander's
	// own two readings agree, which is arithmetic on what they just told us.
	// A merge commit fails it because a merge is by definition not either
	// parent, which is the ff-only rule falling out rather than being restated.
	if !sameCommit(sha, gated) {
		return nil, nil, &ErrLandRefused{
			Reason: fmt.Sprintf(
				"master became %s but the gate measured %s. A fast-forward lands the tip that was measured, so those two are the same commit or the landing was not one - re-gate the tip you actually landed, or land the tip you actually gated",
				sha, gated),
		}
	}

	target := TargetOf(art)
	lock, err := d.MergeLockOf(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	if lock == nil {
		return nil, nil, &ErrLandRefused{
			Reason: fmt.Sprintf(
				"%s is not held by anybody. A land is exclusive through the lock a gate declaration takes - declare the run and land inside it", target),
			Now: now,
		}
	}
	if lock.Holder != actor {
		return nil, nil, &ErrLandRefused{Reason: "the target is held by another declarer", Held: lock, Now: now}
	}
	// HELD FOR WHICH WORK, not merely by whom. Two agents of one seat share a
	// principal, so holder alone let a sibling land through a lock it never
	// took. An empty item is a lock from before the column and is not refused -
	// nothing took it under the new rule, so nothing may be concluded from it.
	if lock.Item != "" && lock.Item != art.ID {
		return nil, nil, &ErrLandRefused{
			Reason: "the target is held for a different merge request",
			Held:   lock, Now: now,
		}
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	fields[LandedTipField] = sha
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: land %s: %w", art.ID, err)
	}

	// What the lander fast-forwarded, named for the log: the ref when the
	// evidence lived on an integration branch, the row's own branch otherwise.
	fastForwarded := GateRefOf(art)
	if fastForwarded == "" {
		fastForwarded = BranchOf(art)
	}
	meta, err := json.Marshal(map[string]string{
		LandedTipField: sha,
		GatedTipField:  GatedTipOf(art),
		GateRunField:   GateRunOf(art),
		GateRefField:   GateRefOf(art),
		BranchField:    fastForwarded,
		"actor_kind":   actorKind,
		"actor_user":   p.UserID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: land %s: %w", art.ID, err)
	}
	entry := &Event{
		Type:     EventMergeLand,
		Project:  art.Project,
		Room:     categoryRoom(art),
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     fmt.Sprintf("landed %s as %s on %s", fastForwarded, sha, target),
		Meta:     meta,
	}
	if err := d.SetArtifactFields(ctx, art, column, entry); err != nil {
		return nil, nil, err
	}

	// The row closes with its verdict on it. Two writes rather than one
	// transaction on purpose: the fields and the event are the fact, the status
	// is bookkeeping, and a row that says landed_tip with status open is a
	// visible inconsistency somebody fixes by closing it - the reverse order
	// would be a closed row with no record of what it landed.
	if err := d.SetArtifactStatus(ctx, art, DoneStatus); err != nil {
		return nil, nil, err
	}

	// THE TIP CHAIN ADVANCES with the land, not at the deploy: this is the row
	// the queue reads when nobody stated a tip, and the whole reason it exists
	// is that the build-stamp fallback froze "where master is" twelve landings
	// behind for a night. Recorded before the lock releases, so the next
	// declarer reads a target that already includes this land.
	if err := d.RecordLandedTip(ctx, p, target, sha); err != nil {
		return nil, nil, err
	}

	// THE LOCK GOES LAST. Released only by its holder, only after the record:
	// a release that preceded the write would open the target while the landed
	// tip was still unsaid, and the next declarer would measure a tip nobody
	// had announced.
	if _, err := d.ReleaseMergeLock(ctx, p, target, art.ID); err != nil {
		// Not fatal to the land - the tip is recorded and the row is closed.
		// The lock expires on its own, and saying so beats failing a land that
		// already happened.
		fmt.Printf("merge lock on %s did not release after landing %s: %v (it expires on its own)\n",
			target, sha, err)
	}
	span.SetArtifact(art.ID)
	return art, entry, nil
}
