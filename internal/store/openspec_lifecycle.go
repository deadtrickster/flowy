package store

// The openspec lifecycle: the line a change moves along, and the one door that
// moves it.
//
// A change is proposed -> in-progress -> complete -> archived. Linear, no
// skips, no backward edges - a status that can jump is a status nobody trusts,
// the same reason the issue workflow refuses skips (lifecycle.go). The state
// lives in fields.openspec.state, not the artifact status column: the column
// carries the issue and queue vocabularies, and two lifecycle machines
// sharing one column is how a word comes to mean two things.
//
// THE STATE IS THE TRANSITION DOOR'S TO WRITE, and this is the invariant this
// file exists for: nothing else may move it. Every generic write path - the
// artifact doors' update branch, the mount's view writes, the drainer -
// carries the held row's state forward (carryOpenspecState, asked of the same
// three statements checkOpenspecRow guards), and MoveOpenspecState is the only
// statement that sets it. A state with no event behind it is a lifecycle
// nobody can audit, and a door that let one in would be the hole in every
// arm written since.
//
// Each transition appends one event of type openspec.transition, minted so no
// client can forge the audit trail (api.go), carrying the same clock reading
// as the row - the pair travels as one fact. The event chains on the previous
// one, so a change's trail reads as a thread in the event log.
//
// The complete arm is two checks: the derived todos off tasks.md must all be
// done, and the change must validate - ValidateChange reads the verdict the
// validate door cached on the row (openspec_validate.go, p4).
//
// The archived arm is the archive gate (p6, the last ungated edge): the
// change must name its merge in fields.openspec.merge, and that row must be
// decided - landed or decided-rejected, both terminal. An open merge row is
// not a decision, and a name that matches no row is not one either. The gate
// is asked at transition time only: archived is terminal, and a merge row
// that later re-gates does not pull the change back onto the line.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// OpenspecState is the lifecycle state of a change.
type OpenspecState = string

// The line.
const (
	OpenspecProposed   OpenspecState = "proposed"
	OpenspecInProgress OpenspecState = "in-progress"
	OpenspecComplete   OpenspecState = "complete"
	OpenspecArchived   OpenspecState = "archived"
)

// EventOpenspecTransition is the entry a lifecycle move leaves in the log,
// minted for the reason the comment on it in sync.go gives. The door writes
// the same word (openspecTransitionEventType), and the test in the server
// package holds the two together.
const EventOpenspecTransition = "openspec.transition"

// openspecFlow is each state and the one that follows it. Archived is absent
// from the map: it is terminal, and the check says so in its own words rather
// than by a map lookup failing quietly.
var openspecFlow = map[OpenspecState]OpenspecState{
	OpenspecProposed:   OpenspecInProgress,
	OpenspecInProgress: OpenspecComplete,
	OpenspecComplete:   OpenspecArchived,
}

// OpenspecStateOf reads a change's state off its fields. Absent means
// proposed - a fresh change starts at the start of the line, and rows written
// before the lifecycle existed are at the start by the same rule. Unparsable
// fields is an error, not "no state": that is a row this code cannot read.
func OpenspecStateOf(a *Artifact) (OpenspecState, error) {
	if a == nil || len(a.Fields) == 0 {
		return OpenspecProposed, nil
	}
	s, err := openspecStateInFields(a.Fields)
	if err != nil {
		return "", err
	}
	if s == "" {
		return OpenspecProposed, nil
	}
	return s, nil
}

// openspecStateInFields reads the state off a raw fields blob: the empty
// string when absent, an error when the blob is not JSON.
func openspecStateInFields(raw []byte) (OpenspecState, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var outer struct {
		Openspec *struct {
			State string `json:"state"`
		} `json:"openspec"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return "", fmt.Errorf("fields is not JSON: %w", err)
	}
	if outer.Openspec == nil {
		return "", nil
	}
	return outer.Openspec.State, nil
}

// SetOpenspecState puts the state on a change row's fields, keeping the files
// map and every other key. It is the write-side sibling of OpenspecStateOf,
// and its only caller is MoveOpenspecState - nothing else may set the state.
func SetOpenspecState(a *Artifact, state OpenspecState) error {
	fields, err := ArtifactFields(a)
	if err != nil {
		return err
	}
	os, _ := fields["openspec"].(map[string]any)
	if os == nil {
		os = map[string]any{}
	}
	os["state"] = state
	fields["openspec"] = os
	raw, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("store: openspec fields of %s: %w", a.ID, err)
	}
	a.Fields = raw
	return nil
}

// carryOpenspecState overwrites whatever state the incoming row carries with
// the state the held row has - or, for a create, strips it so a fresh change
// always starts at proposed. It is asked of the same statements as
// checkOpenspecRow (prepareChangeWrite is the one place, called by all three),
// which is the whole of the door-only setter: a caller who sends a fields blob
// naming a state they like does not move the lifecycle, they edit content, and
// the stored row keeps the state the lifecycle actually holds.
//
// A held row with no state - written before the lifecycle existed - carries
// "", and the strip branch is what lands: the change reads as proposed, which
// is where the line starts.
func (d *DB) carryOpenspecState(ctx context.Context, q execer, a *Artifact) error {
	if !IsEntityType(a, ChangeKind) {
		return nil
	}
	var held []byte
	err := q.QueryRowContext(ctx,
		`SELECT fields FROM artifacts WHERE id = $1 AND coalesce(tombstone, false) = false`,
		a.ID).Scan(&held)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		// A create: there is no held row, so there is no held state. The strip
		// below removes whatever the caller tried to be born with.
		held = nil
	default:
		return fmt.Errorf("store: held fields of %s: %w", a.ID, err)
	}
	state, err := openspecStateInFields(held)
	if err != nil {
		return err
	}
	return carryOpenspecStateInto(a, state)
}

// carryOpenspecStateInto is the fields edit: the state key is set to the held
// value, or removed when there is none to carry.
func carryOpenspecStateInto(a *Artifact, state OpenspecState) error {
	fields, err := ArtifactFields(a)
	if err != nil {
		return err
	}
	os, _ := fields["openspec"].(map[string]any)
	if state == "" {
		// A fresh change is proposed, which is the absence of the key. The
		// openspec map itself stays - the files map lives in it.
		if os != nil {
			delete(os, "state")
			fields["openspec"] = os
		}
	} else {
		if os == nil {
			os = map[string]any{}
		}
		os["state"] = state
		fields["openspec"] = os
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("store: openspec fields of %s: %w", a.ID, err)
	}
	a.Fields = raw
	return nil
}

// OpenspecNextStates is everywhere a change at from may go: the one next state
// or nothing, so a refusal can say what it refused and a console can draw the
// one choice the line offers.
func OpenspecNextStates(from OpenspecState) []OpenspecState {
	next, ok := openspecFlow[from]
	if !ok {
		return []OpenspecState{}
	}
	return []OpenspecState{next}
}

// CheckOpenspecTransition is the lifecycle's whole rule, asked of a proposed
// move: the edge must be on the line, and a move into complete must pass both
// its arms. The caller - the transition door - refuses with the sentence
// verbatim: the store owns the rules, the door owns the HTTP (the
// checkQueueRow split).
func (d *DB) CheckOpenspecTransition(
	ctx context.Context, a *Artifact, to OpenspecState,
) error {
	from, err := OpenspecStateOf(a)
	if err != nil {
		return fmt.Errorf("cannot move to %s: %w", to, err)
	}
	next, ok := openspecFlow[from]
	if !ok {
		return fmt.Errorf("cannot move %s -> %s: %s is terminal - an archived change left the line",
			from, to, from)
	}
	if to != next {
		return fmt.Errorf("cannot move %s -> %s: from %s the lifecycle allows %s",
			from, to, from, next)
	}
	if to == OpenspecComplete {
		if err := d.checkOpenspecTasksDone(ctx, a); err != nil {
			return err
		}
		if err := ValidateChange(d, ctx, a); err != nil {
			return fmt.Errorf("cannot move %s -> %s: %w", from, to, err)
		}
	}
	if to == OpenspecArchived {
		if err := CheckOpenspecMerge(d, ctx, a); err != nil {
			return fmt.Errorf("cannot move %s -> %s: %w", from, to, err)
		}
	}
	return nil
}

// CheckOpenspecMerge is the archive gate: archived asks the merge the change
// names in fields.openspec.merge, and the answer has to be a decision. It is
// a package var for the same reason ValidateChange is - a pure test swaps it
// and proves the arm is ASKED, not just written.
var CheckOpenspecMerge = func(d *DB, ctx context.Context, a *Artifact) error {
	return d.checkOpenspecMergeDecided(ctx, a)
}

// checkOpenspecMergeDecided is the gate's rule. The named row must exist,
// must be a merge request, and must be terminal - status done is the one
// word every surface uses for a finished row (DoneAt). Landed and
// decided-rejected are not distinguished here: both are decisions, and a
// dead change must still be archivable. The sentences say what to do about
// each refusal rather than only that it refused.
func (d *DB) checkOpenspecMergeDecided(ctx context.Context, a *Artifact) error {
	name, err := openspecMergeInFields(a.Fields)
	if err != nil {
		return err
	}
	if name == "" {
		return errors.New("the change names no merge - set fields.openspec.merge to its merge row id, then archive")
	}
	var typ, kind, status string
	err = d.sql.QueryRowContext(ctx,
		`SELECT type, kind, status FROM artifacts WHERE id = $1 AND coalesce(tombstone, false) = false`,
		name).Scan(&typ, &kind, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("its merge names no row - %s is not on this node", name)
	case err != nil:
		return fmt.Errorf("store: merge row of %s: %w", name, err)
	}
	if !IsEntityType(&Artifact{Type: typ, Kind: kind}, MergeKind) {
		return fmt.Errorf("its merge names %s, which is not a merge request", name)
	}
	if status != DoneStatus {
		return fmt.Errorf("its merge %s is not decided - an open merge request is not a decision: "+
			"land it, or close it with `todo done`", name)
	}
	return nil
}

// openspecMergeInFields reads the merge name off a raw fields blob: the
// empty string when absent, an error when the blob is not JSON. The sibling
// of openspecStateInFields, same shape.
func openspecMergeInFields(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var outer struct {
		Openspec *struct {
			Merge string `json:"merge"`
		} `json:"openspec"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return "", fmt.Errorf("fields is not JSON: %w", err)
	}
	if outer.Openspec == nil {
		return "", nil
	}
	return outer.Openspec.Merge, nil
}

// ValidateChange is the validate arm: complete asks the verdict the validate
// door cached on the row (fields.openspec.validation, p4). Absent means never
// validated, a hash mismatch means the change was edited since, and a red
// verdict says what is wrong - each in its own sentence, pointed at the door
// that fixes it.
var ValidateChange = func(d *DB, ctx context.Context, a *Artifact) error {
	files, err := OpenspecFilesOf(a)
	if err != nil {
		return err
	}
	cached, err := OpenspecValidationOf(a)
	if err != nil {
		return err
	}
	switch {
	case cached == nil:
		return errors.New("the change has not been validated - run POST /api/openspec/{id}/validate")
	case cached.FilesHash != openspecFilesHash(files):
		return errors.New("the change has been edited since it was validated - " +
			"run POST /api/openspec/{id}/validate")
	case !cached.Ok:
		return fmt.Errorf("the change does not validate - %s", strings.Join(cached.Problems, "; "))
	}
	return nil
}

// checkOpenspecTasksDone is the todo arm: every todo the change derived off
// its tasks.md must be done. The markers in the file are the LINE identities
// (annotateTasks), and the derived row is the one whose origin.line carries
// the same id - a row has its own ulid, so the arm looks the row up by the
// identity rather than by id. A task removed from tasks.md tombstones its row
// and drops off the list, which is the derivation's rule (p2) and this arm
// reads it rather than restating it.
func (d *DB) checkOpenspecTasksDone(ctx context.Context, a *Artifact) error {
	files, err := OpenspecFilesOf(a)
	if err != nil {
		return err
	}
	known, err := d.derivedTodosOf(ctx, d.sql, a.ID)
	if err != nil {
		return err
	}
	byLine := map[string]*Artifact{}
	for _, todo := range known {
		if line := originLineOf(todo); line != "" {
			byLine[line] = todo
		}
	}
	var open []string
	for _, line := range parseTasks(files["tasks.md"]) {
		if line.id == "" {
			continue
		}
		todo, ok := byLine[line.id]
		if !ok {
			return fmt.Errorf("cannot move to complete: its task %s names no todo - "+
				"a marker has to mark a todo", line.id)
		}
		if TodoStatusOf(todo) != DoneStatus {
			open = append(open, line.id)
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("cannot move to complete: its tasks are not all done - %s",
			strings.Join(open, ", "))
	}
	return nil
}

// MoveOpenspecState moves a change's state and writes the event that records
// the move, in one transaction and under one clock reading - the same shape
// as MoveArtifactStatus and for the same reason: a state with no entry behind
// it is a lifecycle nobody can audit, and two statements with a gap between
// them meant the two could disagree. The transition door has already checked
// the move; this statement is the writer, not the judge.
func (d *DB) MoveOpenspecState(
	ctx context.Context, art *Artifact, to OpenspecState, events ...*Event,
) error {
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: move openspec state of %s: %w", art.ID, err)
	}
	for _, e := range events {
		e.SeqHLC = at
	}

	return d.inTx(ctx, "move openspec state of "+art.ID, func(tx *sql.Tx) error {
		if err := SetOpenspecState(art, to); err != nil {
			return err
		}
		art.HLC = at
		art.Node = d.node
		// The row this node is about to have is the row it signs, and the state
		// is inside the signature - a peer accepting the row accepts the state.
		if err := d.signArtifact(ctx, tx, art); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE artifacts
			    SET fields = $2, hlc = $3, node = $4, sig = $5, updated = now()
			  WHERE id = $1 AND coalesce(tombstone, false) = false`,
			art.ID, art.Fields, art.HLC, art.Node, art.Sig)
		if err != nil {
			return fmt.Errorf("store: move openspec state of %s: %w", art.ID, err)
		}
		n, err := affectedRows(res)
		if err != nil {
			return fmt.Errorf("store: move openspec state of %s: %w", art.ID, err)
		}
		if n == 0 {
			return fmt.Errorf("store: move openspec state: %w: artifact %s", ErrNotFound, art.ID)
		}
		for _, e := range events {
			if err := d.appendEvent(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}
