package store

// The work queue: what somebody is about to do, before there is anything to
// show for it.
//
// The operator's shape, and the distinction is about WHO CAN DO THE THING
// rather than about size:
//
//	OWNED   bound to a principal. Nobody else can do it - it is their commit on
//	        their branch - so it needs no claim. Listing it is the whole point:
//	        it tells everybody else what NOT to start.
//
//	STRAY   bound to nobody. Anybody with access can do it - run the gate,
//	        restart the node, repair the layer - so it is ENTIRELY A CLAIM
//	        PROBLEM.
//
// Three rules come out of that, and all three are things this fleet got wrong
// before the queue existed:
//
// A STRAY CLAIM IS NOT AN ASSIGNEE. Assignee is collaborative metadata any
// reader may set, which is right for a todo and wrong here: two agents can set
// it in the same second and both come away believing they own it. A claim is a
// COMPARE-AND-SET exactly one wins, and the loser is TOLD they lost. A claim
// that silently succeeds twice is worse than no queue, because it manufactures
// the confidence that makes both of them act.
//
// THE CLAIM MUST BE CHEAPER THAN THE ACT. The e2fsck collision happened because
// claiming cost a chat message and doing cost one command, so the agent who
// just did it was behaving sensibly. One call, no ceremony, or the queue is
// decoration.
//
// REMOVAL IS A TOMBSTONE. "Gone from the queue" has to distinguish SOMEBODY DID
// IT - and who, and when - from NOBODY EVER DID IT. A delete makes those
// identical, which is the same defect as answering 404 for a tombstone.
//
// AND THE PROJECT IS NEVER THE KEY. The operator asked for a global projection
// later, with no per-project filtering, so nothing here keys on project. It is
// a column an item carries and a filter a reader may apply, never the identity
// of a queue.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WorkKind is the kind a work item carries. It is its own kind rather than a
// todo with a flag, because the queue's read is "what is being worked on now"
// and a todo's is "what is outstanding" - one item can be neither, either or
// both, and a flag on a todo cannot say that.
const WorkKind = "work"

// The fields a work item carries beyond an artifact's own.
const (
	// BoundField names the principal an OWNED item belongs to. Empty is STRAY,
	// and the difference is not a status: it is who is able to do the thing.
	BoundField = "bound"
	// TakenField is who holds a stray item right now. Written only through
	// ClaimWork, and only by a write that won the compare-and-set.
	TakenField = "taken"
	// TakenAtField is when, so a stale claim can be seen for what it is.
	TakenAtField = "taken_at"
	// DidField and DidAtField are the tombstone: who finished it and when.
	// Present exactly when the item was done, which is what makes "somebody did
	// this" different from "this is gone".
	DidField   = "did"
	DidAtField = "did_at"
)

// EventWorkClaim and EventWorkDone are what the two moves leave in the log.
// Minted, so the only way to get one is through the verb that refuses.
const (
	EventWorkClaim = "work.claim"
	EventWorkDone  = "work.done"
)

// WorkRoom is where an entry lands when the item names no room, for StatusRoom's
// reason: an entry nobody can find is an entry nobody reads.
const WorkRoom = "work"

// ErrTaken is a stray item somebody else holds. The holder is named, because
// the loser being told WHO won is what turns a refusal into information they
// can act on - ask that agent, or take the next item.
type ErrTakenBy struct {
	Item   string
	Holder string
}

func (e ErrTakenBy) Error() string {
	return fmt.Sprintf("work %s is already taken by %s - ask them before you start, "+
		"or take something else", e.Item, e.Holder)
}

// depRefusal marks this as the caller's mistake rather than a broken node, so
// HTTP answers 409-shaped rather than 500. It is the interface the other queue
// verbs already refuse through.
func (e ErrTakenBy) depRefusal() {}

// ErrBoundElsewhere is an OWNED item somebody else's principal is bound to.
// Nobody else can do it, which is the whole meaning of owned, so taking it is
// refused rather than reported.
type ErrBoundElsewhere struct {
	Item  string
	Bound string
}

func (e ErrBoundElsewhere) Error() string {
	return fmt.Sprintf("work %s is owned by %s and only they can do it - "+
		"a stray item is the kind anybody may take", e.Item, e.Bound)
}

func (e ErrBoundElsewhere) depRefusal() {}

// ClaimWork takes a stray work item for this principal, and EXACTLY ONE caller
// wins.
//
// The win is a conditional UPDATE, not a read followed by a write: the guard
// says the taken field is still empty, so a second claimer's write touches no
// rows and comes back ErrTakenBy naming the winner. Two goroutines, two nodes
// racing through one node's pool, or one agent double-clicking all get the same
// answer.
//
// Retaking your own claim is allowed and is a no-op that succeeds: an agent
// re-reading its own queue after a restart should not be told it lost to itself.
func (d *DB) ClaimWork(ctx context.Context, p *Principal, id string) (*Artifact, *Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseWork("this token resolves to nobody, so it cannot claim work")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	if did := fieldText(fields, DidField); did != "" {
		return nil, nil, refuseWork("work %s was already done by %s - a finished item is "+
			"history, not a queue entry", art.ID, did)
	}
	// OWNED IS NOT TAKEABLE. Bound to somebody else means nobody else can do the
	// thing, so this is a refusal about the world rather than about a race.
	if bound := fieldText(fields, BoundField); bound != "" && bound != actor && bound != p.UserID {
		return nil, nil, ErrBoundElsewhere{Item: art.ID, Bound: bound}
	}
	if holder := fieldText(fields, TakenField); holder != "" {
		if holder == actor {
			return art, nil, nil // already ours, and saying so is not losing
		}
		return nil, nil, ErrTakenBy{Item: art.ID, Holder: holder}
	}

	fields[TakenField] = actor
	fields[TakenAtField] = nowRFC3339()
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: claim %s: %w", art.ID, err)
	}
	entry := workEvent(art, p, EventWorkClaim, actor, actorKind, map[string]string{
		TakenField: actor,
	})
	// THE GUARD IS THE CLAIM. Everything above is courtesy - a better message
	// for the common case - and this line is what makes it true.
	err = d.SetArtifactFieldsIf(ctx, art, column,
		`coalesce(fields->>'`+TakenField+`', '') = ''`, entry)
	if errors.Is(err, ErrGuardFailed) {
		// Somebody won between the read and the write, which is exactly the
		// window this exists for. Say who.
		holder := "somebody else"
		if fresh, ferr := d.GetArtifact(ctx, art.ID); ferr == nil {
			if ff, ferr := ArtifactFields(fresh); ferr == nil {
				if who := fieldText(ff, TakenField); who != "" {
					holder = who
				}
			}
		}
		return nil, nil, ErrTakenBy{Item: art.ID, Holder: holder}
	}
	if err != nil {
		return nil, nil, err
	}
	return art, entry, nil
}

// ReleaseWork puts a stray item back, and only its holder may.
//
// It is not a delete and not a done: the item returns to the queue with nobody
// on it, which is what an agent that could not finish owes everybody else.
func (d *DB) ReleaseWork(ctx context.Context, p *Principal, id string) (*Artifact, *Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseWork("this token resolves to nobody, so it cannot release work")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	holder := fieldText(fields, TakenField)
	if holder == "" {
		return art, nil, nil // nobody holds it, so it is already back
	}
	if holder != actor && !p.Operator {
		return nil, nil, ErrTakenBy{Item: art.ID, Holder: holder}
	}
	delete(fields, TakenField)
	delete(fields, TakenAtField)
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: release %s: %w", art.ID, err)
	}
	entry := workEvent(art, p, EventWorkClaim, actor, actorKind, map[string]string{
		TakenField: "",
		"released": holder,
	})
	if err := d.SetArtifactFieldsIf(ctx, art, column,
		`coalesce(fields->>'`+TakenField+`', '') = '`+sqlLiteral(holder)+`'`, entry); err != nil {
		if errors.Is(err, ErrGuardFailed) {
			return nil, nil, refuseWork("work %s moved while you were releasing it - read it again",
				art.ID)
		}
		return nil, nil, err
	}
	return art, entry, nil
}

// FinishWork records that the thing was done, by whom, and when.
//
// A TOMBSTONE RATHER THAN A DELETE. "Gone from the queue" must not be the same
// row-shape as "never existed": the operator asking whether the layer was
// repaired needs to read that somebody repaired it at 23:14, and a deleted row
// answers that question with silence.
func (d *DB) FinishWork(ctx context.Context, p *Principal, id string) (*Artifact, *Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseWork("this token resolves to nobody, so it cannot finish work")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	if did := fieldText(fields, DidField); did != "" {
		return art, nil, nil // already done, and saying so twice is not an error
	}
	// Whoever holds it, or anybody at all if nobody does: a stray item somebody
	// simply did without claiming first is the ordinary case this queue is
	// trying to make cheap, not a violation.
	if holder := fieldText(fields, TakenField); holder != "" && holder != actor && !p.Operator {
		return nil, nil, ErrTakenBy{Item: art.ID, Holder: holder}
	}
	fields[DidField] = actor
	fields[DidAtField] = nowRFC3339()
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: finish %s: %w", art.ID, err)
	}
	entry := workEvent(art, p, EventWorkDone, actor, actorKind, map[string]string{
		DidField: actor,
	})
	if err := d.SetArtifactFieldsIf(ctx, art, column,
		`coalesce(fields->>'`+DidField+`', '') = ''`, entry); err != nil {
		if errors.Is(err, ErrGuardFailed) {
			return nil, nil, refuseWork("work %s was finished by somebody else while you were "+
				"writing - read it again", art.ID)
		}
		return nil, nil, err
	}
	return art, entry, nil
}

// workEvent builds the entry a move leaves in the log.
func workEvent(art *Artifact, p *Principal, kind, actor, actorKind string, extra map[string]string) *Event {
	fields := map[string]string{
		"actor_kind": actorKind,
		"actor_user": p.UserID,
	}
	for k, v := range extra {
		fields[k] = v
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		meta = nil
	}
	room := RoomOf(art)
	if room == "" {
		room = WorkRoom
	}
	return &Event{
		Type:    kind,
		Project: art.Project,
		Room:    room,
		Actor:   actor,
		Body:    art.Title,
		Meta:    meta,
	}
}

// workRefusal is the caller's mistake, and fixable by the caller.
type workRefusal struct{ reason string }

func (e workRefusal) Error() string { return e.reason }
func (e workRefusal) depRefusal()   {}

func refuseWork(format string, a ...any) error {
	return workRefusal{reason: fmt.Sprintf(format, a...)}
}

// fieldText reads one string out of an item's fields, answering empty for a key
// that is absent or is not a string - a field written as a number by some other
// client is not a holder's name.
func fieldText(fields map[string]any, key string) string {
	v, ok := fields[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// sqlLiteral escapes a value being spliced into a guard fragment. The guards in
// this file are built from names the store itself wrote, but a name is still a
// string somebody chose, and one apostrophe in an agent's handle would turn a
// guard into a syntax error at best.
func sqlLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// nowRFC3339 is the one spelling of a timestamp this file writes, so a reader
// parsing one field can parse them all.
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }
