package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// THE ABANDON VERB: saying "I am done and I failed", which until now had no door.
//
// The lock could be TAKEN without landing - a gate declaration takes it - but it
// could only be RELEASED by landing, because LandMerge was ReleaseMergeLock's
// only caller. That asymmetry has one consequence and it is not small: a gate
// that goes red holds the shared tree for the full fifteen minutes, the holder
// has no way to give it back, and every other agent waits out a freeze nobody
// chose. It happened twice this morning before anybody measured why.
//
// WHY THIS IS NOT A BARE UNLOCK. A release with nothing on it is
// indistinguishable from an expiry, and those two mean opposite things: an
// expiry is the safety net for a holder who died, an abandon is a live holder
// reporting a result. If the only trace of either is a row vanishing from
// merge_locks, then the log cannot answer "did that agent give up, or stop
// existing" - and that question is the whole of what the next declarer wants to
// know before spawning a five-minute run. So the reason is required, and the
// event is the point of the verb; the release is almost a side effect.
//
// WHAT IT DOES NOT TOUCH. Not the row's status, not its gated tip, not its gate
// run. An abandoned attempt is not a closed request - the branch still wants to
// land - and the run that failed still happened. Nothing here rewrites what was
// measured; it records that the measuring stopped, which is a different fact.

// EventMergeAbandon is what giving up leaves in the log.
const EventMergeAbandon = "merge.abandon"

// ErrAbandonRefused is every way an abandon can be turned away, in words.
// It mirrors ErrLandRefused rather than reusing it because the two doors refuse
// for different reasons and a caller reading "land refused" from a call it did
// not make is a small lie in the one place that must not tell them.
type ErrAbandonRefused struct {
	Reason string
	Held   *MergeLock
	Now    time.Time
}

func (e *ErrAbandonRefused) Error() string {
	if e.Held != nil {
		held := &ErrTargetHeld{Target: e.Held.Target, Held: e.Held, Now: e.Now}
		return fmt.Sprintf("store: abandon refused: %s (%s)", e.Reason, held)
	}
	return fmt.Sprintf("store: abandon refused: %s", e.Reason)
}

// AbandonMerge gives the target back without landing, and says why in the log.
//
// Holder-only, like the release it wraps: nobody hands back somebody else's
// reservation. An EXPIRED lock still held by the caller is abandonable - the
// caller is exactly the principal whose expiry would otherwise be the only
// record, and letting them replace it with a reason is the whole point.
func (d *DB) AbandonMerge(ctx context.Context, p *Principal, id, reason string) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "merge.abandon")
	defer span.End()

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, fmt.Errorf("store: this token resolves to nobody, so it cannot abandon a merge")
	}
	art, err := d.readWorkItem(ctx, p, id)
	if err != nil {
		return nil, nil, err
	}
	if art.Kind != MergeKind {
		// Same answer as an id that is not here, exactly as the gate and land
		// verbs give: this door is about merge requests and says nothing about
		// rows the caller did not name.
		return nil, nil, ErrNotFound
	}

	// THE REASON IS THE VERB. Refused before the lock is read, because an
	// abandon with nothing on it is the bare unlock this door exists instead of.
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, nil, &ErrAbandonRefused{
			Reason: "an abandon has to say why - a release with no reason on it reads exactly like an expiry, and those mean opposite things. Send reason",
		}
	}

	target := TargetOf(art)
	lock, err := d.MergeLockOf(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	if lock == nil {
		return nil, nil, &ErrAbandonRefused{
			Reason: fmt.Sprintf("%s is not held by anybody - there is nothing to give back", target),
			Now:    now,
		}
	}
	if lock.Holder != actor {
		return nil, nil, &ErrAbandonRefused{Reason: "the target is held by another declarer", Held: lock, Now: now}
	}
	// HELD FOR WHICH WORK, not merely by whom. Two agents of one seat share a
	// principal, so holder alone let a sibling release through a lock it never
	// took. An empty item is a lock from before the column and is not refused -
	// nothing took it under the new rule, so nothing may be concluded from it.
	if lock.Item != "" && lock.Item != art.ID {
		return nil, nil, &ErrAbandonRefused{
			Reason: "the target is held for a different merge request",
			Held:   lock, Now: now,
		}
	}

	meta, err := json.Marshal(map[string]string{
		"target":      target,
		GateRunField:  GateRunOf(art),
		GatedTipField: GatedTipOf(art),
		BranchField:   BranchOf(art),
		"reason":      reason,
		"expired":     fmt.Sprintf("%t", !lock.Live(now)),
		"actor_kind":  actorKind,
		"actor_user":  p.UserID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: abandon %s: %w", art.ID, err)
	}
	entry := &Event{
		Type:     EventMergeAbandon,
		Project:  art.Project,
		Room:     categoryRoom(art),
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     fmt.Sprintf("gave %s back without landing %s: %s", target, BranchOf(art), reason),
		Meta:     meta,
	}

	// THE RECORD GOES FIRST, the release second - the same order the land verb
	// uses and for the same reason. A release that preceded the write would open
	// the target while the log still said nothing, and the next declarer would
	// take a lock that looks like it was never held.
	if err := d.AppendEvent(ctx, entry); err != nil {
		return nil, nil, err
	}
	if _, err := d.ReleaseMergeLock(ctx, p, target, art.ID); err != nil {
		return nil, nil, err
	}
	span.SetArtifact(art.ID)
	return art, entry, nil
}
