package store

// Claiming a todo, as opposed to being handed one.
//
// AssignTodo is a HANDOVER and is right to be last-write-wins: the operator
// giving somebody work, an agent picking up what another abandoned, somebody
// restating that a row is still theirs. None of those is a race, and refusing
// them would break the thing assignment is for.
//
// A CLAIM is the other verb wearing the same clothes, and it is a race every
// time. Five times on 2026-08-17/18 two agents claimed the same row within a
// minute and both came away believing they held it, because the second write
// overwrote the first and was answered 200. Twice that put two agents on the
// same file; once it put a second agent on a console rewrite somebody was
// halfway through. The claim that silently succeeds twice is worse than no
// claim at all: it manufactures the confidence that makes both of them act.
//
// So a claim states what it EXPECTED to find - empty for a row nobody holds -
// and the write is one guarded UPDATE. The loser touches no rows and is told
// who won, which is the difference between a refusal they can act on and a
// refusal they can only retry.
//
// This is ClaimWork's mechanism (see workqueue.go) applied to the other queue.
// Two spellings of one idea would drift, so the guard is built the same way and
// the refusal carries the same shape.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrHeldBy is a claim that lost. It names the holder, because "no" is a
// refusal and "no, X has it" is information: ask X, or take the next row.
type ErrHeldBy struct {
	Todo     string
	Holder   string
	Expected string
}

func (e ErrHeldBy) Error() string {
	held := e.Holder
	if held == "" {
		held = "nobody"
	}
	expected := e.Expected
	if expected == "" {
		expected = "nobody"
	}
	return fmt.Sprintf("todo %s is carried by %s, and this claim expected %s - "+
		"somebody took it first, so ask them or take another row", e.Todo, held, expected)
}

// depRefusal marks this as the caller's mistake rather than a broken node.
func (e ErrHeldBy) depRefusal() {}

// ClaimTodo takes a todo only if it is still carried by whoever the caller
// thinks it is, and EXACTLY ONE of two racing callers wins.
//
// expect is the holder the caller read before deciding to claim - empty means
// "nobody had it". The guard is on the stored value, so the window between the
// caller's read and this write is closed by the database rather than by hope.
//
// Restating your own claim is allowed and writes nothing new, for AssignTodo's
// reason: an agent re-reading its own queue after a restart is not losing a
// race with itself.
func (d *DB) ClaimTodo(
	ctx context.Context, p *Principal, todo, asked, expect string,
) (*Artifact, *Event, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, nil, refuseAssign("this token resolves to nobody, so it cannot claim a todo")
	}
	name, err := NormalizeAssignee(asked)
	if err != nil {
		return nil, nil, err
	}
	expect = strings.TrimSpace(expect)
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, nil, err
	}
	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	held := strings.TrimSpace(AssigneeOf(art))
	// The cheap answers first, so the common cases carry a better message than
	// a guard failure can. The guard below is still what makes it true.
	switch {
	case held == name && name != "":
		return art, nil, nil // already ours, nothing to write
	case held != expect:
		return nil, nil, ErrHeldBy{Todo: art.ID, Holder: held, Expected: expect}
	}

	fields[AssigneeField] = name
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: claim %s: %w", art.ID, err)
	}
	entry, err := assignEvent(art, p, actor, actorKindOf(p), name, held)
	if err != nil {
		return nil, nil, err
	}
	// THE GUARD IS THE CLAIM, and it compares against what the caller expected
	// rather than against what this function just read: a row that moved in
	// between is exactly the case worth refusing.
	guard := `coalesce(fields->>'` + AssigneeField + `', '') = '` + sqlLiteral(expect) + `'`
	err = d.SetArtifactFieldsIf(ctx, art, column, guard, entry)
	if errors.Is(err, ErrGuardFailed) {
		holder := ""
		if fresh, ferr := d.GetArtifact(ctx, art.ID); ferr == nil {
			holder = strings.TrimSpace(AssigneeOf(fresh))
		}
		return nil, nil, ErrHeldBy{Todo: art.ID, Holder: holder, Expected: expect}
	}
	if err != nil {
		return nil, nil, err
	}
	return art, entry, nil
}

// actorKindOf is voteActor's second return, named for the one caller that wants
// it on its own.
func actorKindOf(p *Principal) string {
	_, kind := voteActor(p)
	return kind
}
