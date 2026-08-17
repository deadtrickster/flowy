package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
)

// EventMergeGate is what a gate declaration leaves in the log.
const EventMergeGate = "merge.gate"

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
	fields[GateRunField] = run
	status := art.Status
	if tip = strings.TrimSpace(tip); tip != "" {
		fields[GatedTipField] = normalizeTip(tip)
	} else {
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
