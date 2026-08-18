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
	return true
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
func (d *DB) SetMergeGate(
	ctx context.Context, p *Principal, id, run, tip string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "merge.gate")
	defer span.End()

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

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	status := art.Status
	if applyGate(fields, run, tip, time.Now().UTC()) {
		status = ActiveStatus
	}
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: declare gate on %s: %w", art.ID, err)
	}

	meta, err := json.Marshal(map[string]string{
		GateRunField:  run,
		GatedTipField: normalizeTip(tip),
		"actor_kind":  actorKind,
		"actor_user":  p.UserID,
		BranchField:   BranchOf(art),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: declare gate on %s: %w", art.ID, err)
	}
	body := fmt.Sprintf("run %s is measuring %s", run, BranchOf(art))
	if tip != "" {
		body = fmt.Sprintf("run %s measured %s on %s", run, BranchOf(art), normalizeTip(tip))
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
