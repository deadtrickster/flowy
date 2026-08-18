package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// EventMergeGate is what a gate declaration leaves in the log.
const EventMergeGate = "merge.gate"

// GateAtField is WHEN the declaration was made, stamped by the node.
//
// It exists because the first version of this had no off switch. "Gating" was
// derived from having a run and no verdict yet, which is indistinguishable from
// two other things: the run died, and nobody bothered to record what it found.
// Mine sat on for twenty minutes after a green run had already landed, and
// board-nag read it and told everybody not to land.
//
// A release verb does not fix that. It fails exactly when it matters - when the
// run dies and there is nobody left to call it.
//
// So a declaration is BELIEVED FOR A BOUNDED TIME. The clock is the node's, not
// the caller's, for the reason every deadline here is: a caller who sets its own
// expiry can set it to never.
const GateAtField = "gate_at"

// GateRefField names where the evidence lives when that is not the row's own
// branch: an integration branch whose tree the run actually measured. It is
// what a lander is handed after batching - the row's branch would hand them one
// commit of a union that went green as a whole, and it did, twice, silently.
const GateRefField = "gated_ref"

// GateActorField records WHO declared the run, which is the half the lock
// needs: "the target is held" and "the target is held by somebody else" are
// different facts to a holder who is green and ready to land.
const GateActorField = "gate_actor"

// A VERDICT THAT SAYS NO.
//
// gated_tip carried two facts at once - which tree was measured, and that the
// measurement passed - and every symptom on 18 Aug came from those two coming
// apart. applyGate had exactly two cases: a declaration (stamp gate_at, clear
// the tip) and a verdict (write the tip, clear gate_at). There was no third,
// and MergeAdmissible reads a written tip as evidence FOR landing, so recording
// a red would have made the red branch landable. A drainer that found a red had
// two legal exits: land it, or say nothing.
//
// It said nothing. So the row went on reading gating=true for the full fifteen
// minutes after the run had died, the red existed only as a file named
// red-<row>-<tip> on the box that ran it, and any second drainer - or a person -
// would gate the same broken tree again because nothing here knew.
//
// These fields are the third case. RedTipField names THE TIP THAT WAS MEASURED
// AND FOUND BROKEN, which is what it holds - not the run, which gate_run
// already names, and not "the gate failed", which is a sentence rather than a
// fact. Beside gated_base, which the declaration stamped, the pair (red_tip,
// gated_base) is the whole subject of the verdict: this tree, from that base.
const RedTipField = "red_tip"

// RedAtField is when the red was recorded, so a reader can tell a verdict from
// this pass from one three landings ago without opening the log.
const RedAtField = "red_at"

// RedNoteField is what the run said about it in one line - a count, a check
// name, where the log is. It is not the log: the evidence stays where it was
// written, and a note that tried to be the log would be a copy that rots.
const RedNoteField = "red_note"

// RedTipOf, RedAtOf and RedNoteOf read the verdict a row carries.
func RedTipOf(a *Artifact) string { return normalizeTip(artifactString(a, RedTipField)) }

func RedAtOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, RedAtField)) }

func RedNoteOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, RedNoteField)) }

// GateRefOf and GateActorOf read what a declaration recorded. They live here
// and not beside GatedTipOf in mergequeue.go on purpose: that file is one
// admission opinion under active change by another hand, and these are facts
// about the gate, which is this file's subject.
func GateRefOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, GateRefField)) }

func GateActorOf(a *Artifact) string { return strings.TrimSpace(artifactString(a, GateActorField)) }

// GateBelievedFor is how long a declaration is taken seriously.
//
// A gate on this project runs four to six minutes. Fifteen is long enough that a
// slow one is never called dead, and short enough that a dead one stops blocking
// the queue within one coffee. The failure mode is "believed slightly too long"
// rather than "blocks everybody until a human notices", which is the trade this
// is choosing on purpose.
const GateBelievedFor = 15 * time.Minute

// GatingAt reports whether a declaration should still be believed, given the
// row's fields and the time now.
//
// Absent stamp reads as NOT gating rather than as gating forever: every row
// written before this field exists has no stamp, and the safe reading of "I do
// not know when this started" is not "block the queue indefinitely".
func GatingAt(a *Artifact, now time.Time) bool {
	if GateRunOf(a) == "" || GatedTipOf(a) != "" {
		return false
	}
	at := artifactString(a, GateAtField)
	if at == "" {
		return false
	}
	started, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return false
	}
	return now.Sub(started) < GateBelievedFor
}

// applyGate writes one gate moment onto a request's fields, and reports whether
// that moment was a DECLARATION rather than a verdict.
//
// It is split out of SetMergeGate, and takes `now` rather than reading the clock,
// because the whole content of this verb is which fields end up set - and that is
// worth testing without a database or a live run, exactly as GatingAt is. The
// defect below was invisible for a day precisely because this logic could only be
// exercised through a door that needed both.
func applyGate(fields map[string]any, run, tip string, now time.Time) bool {
	fields[GateRunField] = run
	if tip = strings.TrimSpace(tip); tip != "" {
		fields[GatedTipField] = normalizeTip(tip)
		// The verdict is in, so the declaration has nothing left to say. Clearing
		// the stamp rather than leaving it is what makes "gating" a fact about
		// now instead of a fact about the past that nobody swept up.
		delete(fields, GateAtField)
		return false
	}
	// Stamped HERE, by the node. A caller that sets its own expiry can set it to
	// never, which is the whole failure this replaces.
	fields[GateAtField] = now.Format(time.RFC3339Nano)
	// AND THE OLD VERDICT GOES, because gated_tip means "the tip the CURRENT
	// evidence measured", and declaring a run is a statement that the old evidence
	// is being replaced. Leaving it broke the verb in both directions. A re-gate
	// never read as gating, since GatingAt refuses a row that already carries a
	// tip - so on a moving master, where nearly every gate is a re-gate, the
	// queue's only collision guard was silently off. And a re-gate of a flaky run
	// on the SAME tip left the superseded verdict admitting the branch while its
	// replacement was still measuring.
	delete(fields, GatedTipField)
	// AND SO DOES THE RED, for exactly that reason and no other. A declaration
	// says the old evidence is being replaced; a red left behind would outlive
	// the run that found it and describe a tree this one is not measuring.
	delete(fields, RedTipField)
	delete(fields, RedAtField)
	delete(fields, RedNoteField)
	return true
}

// applyRed writes a verdict that says no.
//
// It is applyGate's third case, kept as its own function because it is the one
// that must NOT write gated_tip: MergeAdmissible reads a written tip as
// evidence for landing, so a red recorded there would make the broken branch
// landable, which is why this case did not exist rather than existing wrongly.
// With the tip left alone, MergeAdmissible needs no change at all - it answers
// "no gate has measured it", which is the honest thing to say about a branch
// that has no green.
//
// It clears gate_at like a green verdict does, and that is the whole of the
// gating=true fix: the run is over, so the declaration is over, whichever way it
// went.
func applyRed(fields map[string]any, run, tip, note string, now time.Time) {
	fields[GateRunField] = run
	fields[RedTipField] = normalizeTip(tip)
	fields[RedAtField] = now.Format(time.RFC3339Nano)
	if note = strings.TrimSpace(note); note != "" {
		fields[RedNoteField] = note
	} else {
		delete(fields, RedNoteField)
	}
	delete(fields, GateAtField)
}

// SetMergeGate declares that a run is measuring a merge request, or records the
// tip it measured, and records WHO said so.
//
// It is a verb rather than a field write for the reason the category and the
// assignment are: a gate declaration is a claim about the work with two parties
// and a time, and a field records that a write happened rather than what changed
// or who said it. It is also the reason this cannot go through UpsertArtifact -
// that path's update branch fires only for the OWNER, and a gate is queue
// metadata: any principal who can read the request may declare a run against it,
// exactly as any of them may move its status. A door that silently worked only
// for the author is the bug that put nine unowned todos on the board.
//
// The two calls are one verb because they are one fact at two moments - this run
// is about this branch - and splitting them would let somebody record a verdict
// for a run nobody ever declared, which is the invisible window the whole thing
// exists to close.
//
// A declaration with no tip moves the request to active: something is happening
// to it, and a board that keeps offering it is how two agents gate one branch.
//
// A declaration also TAKES THE LANDING LOCK on the request's target, before
// anything is written - see mergelock.go for why a refusal alone reserved
// nothing. The taker who loses is refused here, naming the holder, which means
// the run they were about to start never starts: the lock is taken at the door
// the protocol walks through first, not announced once the VM is booting.
//
// ref names where the evidence LIVES when that is not the row's own branch - an
// integration branch carrying this row's branch alongside others. It rides the
// declaration because it is a fact about the measurement, not about the request:
// the lander is handed the tree that went green, not the branch the row was
// filed about, and the row that never says so is the one that lands one commit
// of a sixteen-commit union and calls it done.
func (d *DB) SetMergeGate(
	ctx context.Context, p *Principal, id, run, tip, ref string,
) (*Artifact, *Event, error) {
	return d.setMergeGate(ctx, p, id, gateMoment{Run: run, Tip: tip, Ref: ref})
}

// SetMergeRed records that a run measured a tip and it did not pass.
//
// It is the same verb as SetMergeGate and shares its whole path - the same
// refusals, the same lock rule, the same one-transaction write - because a red
// and a green are one fact at one moment reported two ways, and two
// implementations of "what a run found" would drift about the thing that
// decides landing.
//
// THE TIP IS REQUIRED. A red is a statement about a TREE: "this one is broken",
// with the base the declaration stamped beside it. Without a tip it would be
// "something went wrong at some point", which is what the file on one box
// already said and which nothing can act on - a second drainer would not know
// whether it is about to measure the same tree.
//
// It takes the VERDICT branch of the lock rule, not the declaring branch: renew
// and refuse when there is nothing to renew. A red from somebody who never held
// the target is the same forgery as a green from them, and it is worse in one
// way - it tells the queue a branch is broken on the word of a run nobody
// declared.
//
// IT DOES NOT TOUCH THE ROW'S STATUS, for the reason abandon does not: this
// records a measurement, not a lifecycle move. The branch still wants to land
// and whoever is carrying it still is.
func (d *DB) SetMergeRed(
	ctx context.Context, p *Principal, id, run, tip, ref, note string,
) (*Artifact, *Event, error) {
	if strings.TrimSpace(tip) == "" {
		return nil, nil, fmt.Errorf("store: a red verdict names the tip it measured - " +
			"a red with no tree is a rumour, and the next run cannot tell whether it is " +
			"about to measure the same one")
	}
	return d.setMergeGate(ctx, p, id, gateMoment{Run: run, Tip: tip, Ref: ref, Red: true, Note: note})
}

// gateMoment is one report from a run: a declaration, a green verdict, or a red
// one. It is a struct rather than four more parameters because the three cases
// differ by which fields are set, and a positional call site cannot say which
// case it means.
type gateMoment struct {
	Run  string
	Tip  string
	Ref  string
	Red  bool
	Note string
}

func (d *DB) setMergeGate(
	ctx context.Context, p *Principal, id string, g gateMoment,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "merge.gate")
	defer span.End()
	run, tip, ref := g.Run, g.Tip, g.Ref

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, fmt.Errorf("store: this token resolves to nobody, so it cannot declare a gate")
	}
	run = strings.TrimSpace(run)
	if run == "" {
		return nil, nil, fmt.Errorf("store: a gate declaration names the run doing the measuring")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	if art.Kind != MergeKind {
		// Same answer as an id that is not here. This verb is about merge
		// requests; an id naming something else is an id this door does not
		// have, and saying which would tell a caller about a row they did not
		// ask about.
		return nil, nil, ErrNotFound
	}

	// THE LOCK GOES ON FIRST, and only for a declaration: a verdict is the end
	// of a run the declarer already holds the target for, and re-taking it here
	// would let a verdict from a principal who never declared steal the target
	// from whoever is actually measuring.
	declaring := strings.TrimSpace(tip) == "" && !g.Red
	if ref = strings.TrimSpace(ref); declaring {
		if _, err := d.TakeMergeLock(ctx, p, TargetOf(art), art.ID); err != nil {
			return nil, nil, err
		}
	}

	// WHAT THE TARGET WAS WHEN THE RUN STARTED, stamped at declare and never
	// at verdict. Stamping it later would record where the target had got to
	// by the time somebody reported, which is exactly the drift the field
	// exists to detect - the value has to be read at the moment the
	// measurement begins or it measures nothing.
	//
	// A target nothing has landed on yet has no base to record. That is not an
	// error: the row simply carries no base and MergeAdmissible judges it the
	// old way, which is the same fallback every pre-existing row gets.
	var base string
	if declaring {
		if landed, err := d.LandedTipOf(ctx, TargetOf(art)); err == nil && landed != nil {
			base = normalizeTip(landed.Tip)
		}
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	status := art.Status
	switch {
	case g.Red:
		applyRed(fields, run, tip, g.Note, time.Now().UTC())
	case applyGate(fields, run, tip, time.Now().UTC()):
		status = ActiveStatus
	}
	if declaring {
		// A re-declaration after a rebase is a NEW measurement from wherever
		// the target is now, so the base is rewritten rather than kept. Keeping
		// the first one would leave the row claiming it measured from a tip its
		// current run never saw.
		if base != "" {
			fields[GatedBaseField] = base
		} else {
			delete(fields, GatedBaseField)
		}
	}
	// The ref rides both moments, because it describes the measurement rather
	// than either half of it: a verdict recorded without it would leave the
	// lander holding a green tip and no name for the tree it came from.
	if ref != "" {
		fields[GateRefField] = ref
	}
	fields[GateActorField] = actor

	// A VERDICT RENEWS THE WINDOW IT WAS MEASURED IN.
	//
	// Recording one is the strongest liveness signal this system gets - the
	// holder is alive, still on this item, and has just finished the run the
	// lock was taken for. Until now it extended nothing, so the common case
	// (declare, gate for eight minutes, record, land) raced a clock that
	// started before the measurement did, and lost.
	//
	// Renew, never take: a verdict from somebody who does not hold the target
	// must not acquire it. RenewMergeLock is an UPDATE for exactly that reason,
	// and a false here means the window had already gone - which is the
	// caller's problem to hear about at the land door, not a reason to fail
	// recording a measurement that really happened.
	if !declaring {
		// A VERDICT NEEDS THE LOCK IT WAS MEASURED UNDER.
		//
		// The declare branch takes the lock and the record branch deliberately
		// does not, so that a verdict cannot STEAL a target from whoever is
		// measuring. That reasoning is right about taking, and it silently
		// assumed a verdict could only follow a declaration by the same holder.
		// Nothing enforced it: on 2026-08-18 a declare 409'd because somebody
		// else held master, and the verdict that followed was written anyway -
		// a green sitting on a row that never held the target.
		//
		// The renew below is what hid it. It is an UPDATE matching holder and
		// item, so for a non-holder it matches nothing, changes nothing and
		// returns false, and the verdict landed regardless. Built not to take,
		// and not-taking silently is exactly the gap.
		//
		// So: renew, and refuse when there was nothing to renew. Refusing here
		// cannot steal anything - it is the opposite verb.
		held, err := d.RenewMergeLock(ctx, p, TargetOf(art), art.ID)
		if err != nil {
			return nil, nil, err
		}
		if !held {
			lock, lockErr := d.MergeLockOf(ctx, TargetOf(art))
			if lockErr != nil {
				return nil, nil, lockErr
			}
			return nil, nil, &ErrTargetHeld{Target: TargetOf(art), Held: lock, Now: time.Now().UTC()}
		}
	}
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: declare gate on %s: %w", art.ID, err)
	}

	// THE LOG IS THE RECORD AND THE FIELDS ARE ITS PROJECTION, so a red says on
	// the entry which tree it was about and what the run said, in the same shape
	// a green says which tree it measured. A field can be superseded by the next
	// declaration; the entry cannot, which is what makes "has this tree ever
	// been measured" answerable after the row has moved on.
	verdict := map[string]string{
		GateRunField:  run,
		GatedTipField: normalizeTip(tip),
		GateRefField:  ref,
		"actor_kind":  actorKind,
		"actor_user":  p.UserID,
		BranchField:   BranchOf(art),
	}
	if g.Red {
		// The tip does NOT ride the gated_tip key on a red. Every reader of this
		// log treats that key as the tree that passed - the queue, the lander,
		// the drainer - and a red arriving under it would be read as a green by
		// anything that looked at the meta rather than the result.
		delete(verdict, GatedTipField)
		verdict["result"] = "red"
		verdict[RedTipField] = normalizeTip(tip)
		verdict[GatedBaseField] = GatedBaseOf(art)
		if note := strings.TrimSpace(g.Note); note != "" {
			verdict[RedNoteField] = note
		}
	}
	meta, err := json.Marshal(verdict)
	if err != nil {
		return nil, nil, fmt.Errorf("store: declare gate on %s: %w", art.ID, err)
	}
	body := fmt.Sprintf("run %s is measuring %s", run, BranchOf(art))
	if ref != "" {
		body = fmt.Sprintf("run %s is measuring %s through %s", run, BranchOf(art), ref)
	}
	if tip != "" {
		body = fmt.Sprintf("run %s measured %s on %s", run, BranchOf(art), normalizeTip(tip))
		if ref != "" {
			body = fmt.Sprintf("run %s measured %s through %s on %s",
				run, BranchOf(art), ref, normalizeTip(tip))
		}
	}
	if g.Red {
		body = fmt.Sprintf("run %s measured %s on %s and it did not pass",
			run, BranchOf(art), normalizeTip(tip))
		if note := strings.TrimSpace(g.Note); note != "" {
			body += ": " + note
		}
	}
	entry := &Event{
		Type:     EventMergeGate,
		Project:  art.Project,
		Room:     categoryRoom(art),
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     body,
		Meta:     meta,
	}

	// One transaction, one clock reading, both rows or neither - the rule the
	// category write follows. A verdict on a row with no entry behind it is a
	// claim of green nobody can trace back to a run.
	if err := d.SetArtifactFields(ctx, art, column, entry); err != nil {
		return nil, nil, err
	}
	art.Status = status
	span.SetArtifact(art.ID)
	return art, entry, nil
}
