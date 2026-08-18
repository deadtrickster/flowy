package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// THE LANDING LOCK: gating reserves the target it measures.
//
// The admission rule refuses a branch whose evidence is stale, and that rule is
// right - it is what keeps master green. But a refusal reserves nothing. A gate
// runs about five minutes, and on a floor of four agents somebody lands inside
// nearly every window, so honest measurements die honest and the queue
// livelocks: everyone green, nothing landing, everyone re-gating. That was one
// whole night - six wasted runs by three agents, every branch correct the whole
// time.
//
// So a declaration now TAKES the target. One holder at a time, compare-and-set
// with the loser told who holds it and until when - the same shape as the work
// claim, because it is the same problem: two principals, one resource, and a
// last-write-wins that silently eats the slower one.
//
// The lock is believed for a bounded time, stamped by the node, for the same
// reason a gate declaration is (see GateBelievedFor): a holder that dies mid-run
// must not freeze the target until a human notices, and "held slightly too long"
// is the trade this takes over "held until somebody asks".

// MergeLockBelievedFor is how long a landing lock is taken seriously.
//
// A gate runs four to six minutes and a fast-forward is seconds, so fifteen
// covers a slow gate plus the land without ever calling a live holder dead.
// Same trade as GateBelievedFor, one size up.
const MergeLockBelievedFor = 15 * time.Minute

// lockInterval is MergeLockBelievedFor in the form Postgres takes, so the window
// is stated once and both halves cannot drift apart.
var lockInterval = fmt.Sprintf("%d seconds", int(MergeLockBelievedFor.Seconds()))

// MergeLock is one target held by one declarer. HolderName is resolved at read
// time rather than snapshotted at take: a refusal says who to talk to, and the
// name a holder answers to now is the name worth saying, exactly as the roster
// resolves speakers from their rows and not from what somebody cached once.
type MergeLock struct {
	Target     string `json:"target"`
	Holder     string `json:"holder"`
	HolderName string `json:"holder_name,omitempty"`
	// Item is WHICH MERGE REQUEST the target is held for.
	//
	// The lock used to record only the principal, and every subagent of a seat
	// runs under its parent's token - so two processes of one seat read each
	// other's lock as their own. One could renew a lock it never took and land
	// through it, and on 18 Aug a sibling session did exactly that: it finished
	// its own landing, released, and deleted a live holder's lock, invalidating
	// a green verdict mid-flight.
	//
	// The row id is the natural discriminator and it costs no new parameter:
	// the lock is held BY a principal FOR a piece of work, and two agents of a
	// seat are working on two different rows. Same row is the one case that
	// should renew - a re-gate after a rebase is the same work measured again.
	Item    string    `json:"item,omitempty"`
	TakenAt time.Time `json:"taken_at"`
	Until   time.Time `json:"until"`
}

// Live says whether the lock should still be believed at now. Absent answers
// not-held rather than held-forever, which is the reading every bounded stamp
// in this store takes: an expiry nobody can see must not block anybody.
func (l *MergeLock) Live(now time.Time) bool {
	return l != nil && now.Before(l.Until)
}

// ErrTargetHeld is what a lost compare-and-set is, so a caller can tell "the
// target is reserved, retry at T" from "my evidence is stale, re-gate". Those
// are different facts and collapsing them is the defect one level down in
// GatingAt: an agent that cannot tell them apart either sits forever or races.
type ErrTargetHeld struct {
	Target string
	Held   *MergeLock
	Now    time.Time
}

func (e *ErrTargetHeld) Error() string {
	if e.Held == nil {
		return fmt.Sprintf("%s is held and the holder could not be read", e.Target)
	}
	name := e.Held.HolderName
	if name == "" {
		name = e.Held.Holder
	}
	return fmt.Sprintf(
		"%s is held by %s until %s (%s left) - declare again once it releases",
		e.Target, name, e.Held.Until.Format(time.RFC3339),
		e.Held.Until.Sub(e.Now).Round(time.Second))
}

// lockTarget is the caller's target with the same default every other read of
// a merge target applies, so a door that was handed nothing lands on the same
// target the queue reads.
func lockTarget(target string) string {
	if t := strings.TrimSpace(target); t != "" {
		return t
	}
	return DefaultMergeTarget
}

// TakeMergeLock takes the target for this principal, or reports who holds it.
//
// The compare-and-set is one statement: insert, or overwrite only when the row
// is the caller's own renewal or has expired. Everything else loses, and the
// loser is HANDED the holder rather than a bare refusal - "held by X until T"
// is actionable in a way "not admissible" is not, which is the whole point of
// distinguishing this from the stale-evidence refusal.
func (d *DB) TakeMergeLock(ctx context.Context, p *Principal, target, item string) (*MergeLock, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, fmt.Errorf("store: this token resolves to nobody, so it cannot hold a landing lock")
	}
	target = lockTarget(target)

	// ONE CLOCK STAMPS AND JUDGES. `until` used to be computed here, in Go, while
	// taken_at and the expiry test `until <= now()` are the database's. A deadline
	// written by one clock and judged by another is wrong in both directions under
	// skew: a Go clock behind the database writes an `until` already in the past,
	// so the lock is expired the instant it is taken and holds nothing; a Go clock
	// ahead lets a dead holder freeze the target for the skew plus the window. Both
	// are silent - the symptom is collisions coming back, which reads as the lock
	// not working rather than as a clock. Computing it in SQL makes skew impossible
	// by construction rather than unlikely by deployment.
	var got MergeLock
	err := d.sql.QueryRowContext(ctx,
		`INSERT INTO merge_locks (target, holder, item, taken_at, until)
		 VALUES ($1, $2, $4, now(), now() + $3::interval)
		 ON CONFLICT (target) DO UPDATE
		    SET holder = $2, item = $4, taken_at = now(), until = now() + $3::interval
		  WHERE (merge_locks.holder = $2 AND merge_locks.item = $4)
		     OR merge_locks.until <= now()
		 RETURNING target, holder, item, taken_at, until`,
		target, actor, lockInterval, strings.TrimSpace(item)).
		Scan(&got.Target, &got.Holder, &got.Item, &got.TakenAt, &got.Until)
	if err == nil {
		return &got, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("store: take merge lock on %s: %w", target, err)
	}
	// No row came back: the conflict lost. Say to whom.
	held, herr := d.MergeLockOf(ctx, target)
	if herr != nil {
		return nil, fmt.Errorf("store: take merge lock on %s: %w", target, herr)
	}
	return nil, &ErrTargetHeld{Target: target, Held: held, Now: time.Now().UTC()}
}

// ReleaseMergeLock gives the target back. The holder only: nobody releases
// somebody else's reservation, exactly as nobody deletes somebody else's reader.
func (d *DB) ReleaseMergeLock(ctx context.Context, p *Principal, target, item string) (bool, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return false, fmt.Errorf("store: this token resolves to nobody, so it cannot release a landing lock")
	}
	res, err := d.sql.ExecContext(ctx,
		// item = '' is a lock taken before the column existed. Its holder may
		// still give it back: refusing there would strand every lock held
		// across the deploy that introduced this, for the full expiry, with
		// nothing anybody could do - which is the freeze the abandon door was
		// built to end, reintroduced by its own fix.
		`DELETE FROM merge_locks
		  WHERE target = $1 AND holder = $2 AND (item = $3 OR item = '')`,
		lockTarget(target), actor, strings.TrimSpace(item))
	if err != nil {
		return false, fmt.Errorf("store: release merge lock on %s: %w", lockTarget(target), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: release merge lock on %s: %w", lockTarget(target), err)
	}
	return n > 0, nil
}

// mergeLockSQL is the lock with its holder's name, resolved the way the roster
// resolves speakers: an agent row points at the user whose handle it speaks
// under, a user row is its own handle, and neither is a display string the lock
// stores - it is a join, so a renamed holder is named by their current name in
// every refusal that mentions them.
const mergeLockSQL = `SELECT l.target, l.holder,
        coalesce(u.handle, '') AS holder_name, l.item,
        l.taken_at, l.until
   FROM merge_locks l
   LEFT JOIN agents g ON g.id = l.holder
   LEFT JOIN users u ON u.id = coalesce(g.user_id, l.holder)
  WHERE l.target = $1`

// MergeLockOf reads the target's lock as it stands. A row past its until is
// still returned - the caller decides with Live() - because "held by X until
// T, which has passed" is a different sentence from "not held", and the first
// one is what a lander arriving late needs to see about their own reservation.
func (d *DB) MergeLockOf(ctx context.Context, target string) (*MergeLock, error) {
	var got MergeLock
	err := d.sql.QueryRowContext(ctx, mergeLockSQL, lockTarget(target)).
		Scan(&got.Target, &got.Holder, &got.HolderName, &got.Item, &got.TakenAt, &got.Until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read merge lock on %s: %w", lockTarget(target), err)
	}
	return &got, nil
}

// LandedTip is the newest link of a target's landed-tip chain.
type LandedTip struct {
	Target string    `json:"target"`
	Tip    string    `json:"tip"`
	Actor  string    `json:"actor"`
	At     time.Time `json:"landed_at"`
}

// RecordLandedTip advances the chain. Written by the land verb only, so "where
// the queue believes master is" moves exactly when a land says it moved - never
// at a deploy, never at a push nobody announced, and never backwards: the row
// states what the target became, and a stale write arriving late would need a
// land behind it to have overwritten with.
func (d *DB) RecordLandedTip(ctx context.Context, p *Principal, target, tip string) error {
	actor, _ := voteActor(p)
	if actor == "" {
		return fmt.Errorf("store: this token resolves to nobody, so it cannot record a landed tip")
	}
	if _, err := d.sql.ExecContext(ctx,
		`INSERT INTO merge_lands (target, tip, actor, landed_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (target) DO UPDATE
		    SET tip = $2, actor = $3, landed_at = now()`,
		lockTarget(target), normalizeTip(tip), actor); err != nil {
		return fmt.Errorf("store: record landed tip on %s: %w", lockTarget(target), err)
	}
	return nil
}

// LandedTipOf reads the newest landed tip for a target, or nil when nothing has
// landed through the verb yet. The queue prefers this to its build stamp when
// nobody stated a tip: it is the last LAND, which is the question being asked,
// rather than the last deploy, which froze the pointer twelve landings behind
// for a whole night.
func (d *DB) LandedTipOf(ctx context.Context, target string) (*LandedTip, error) {
	var got LandedTip
	err := d.sql.QueryRowContext(ctx,
		`SELECT target, tip, actor, landed_at FROM merge_lands WHERE target = $1`,
		lockTarget(target)).
		Scan(&got.Target, &got.Tip, &got.Actor, &got.At)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read landed tip on %s: %w", lockTarget(target), err)
	}
	return &got, nil
}
