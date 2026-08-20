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
// said is an extra event the caller wants in the same transaction, when the
// claim happens somewhere with something to say about it - the room panel's
// door, which announces its plan changing hands in the room the plan was made
// in. Variadic so the claim a queue takes stays a one-argument decision.
func (d *DB) ClaimTodo(
	ctx context.Context, p *Principal, todo, asked, expect string, said ...*Event,
) (*Artifact, *Event, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, nil, refuseAssign("this token resolves to nobody, so it cannot claim a todo")
	}
	name, err := NormalizeAssignee(asked)
	if err != nil {
		return nil, nil, err
	}
	// "me" IS THE CALLER here too. The claim door is the one every agent uses,
	// so leaving it unresolved is what put a seat called "me" on the board.
	// RESOLVED WHEN IT CAN BE, and left alone when it cannot. A seat with no
	// handle has nothing to resolve to, and refusing there would break every
	// handle-less caller for a defect they do not have - measured as
	// TestRestatingYourOwnClaimIsNotARace, which claims as "me" on a seat that
	// has never had one. The phantom seat this fixes is the case where a handle
	// EXISTS and the door stored the word instead.
	if SelfName(name) {
		if mine := d.seatHandle(ctx, p); mine != "" {
			name = mine
		}
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
	// THE CLAIM RACES THE FIELD, not AssigneeOf, and the difference is the
	// OWNER-line compatibility: a row whose holder lives only in the body's
	// OWNER line holds nothing a claim can race for, and the display that
	// falls back to that line is showing authorship, not a claim anybody
	// made. The cheap answer and the transactional guard below must judge the
	// same reading or a claim that passed one is refused by the other - which
	// is what the first cut of this did, both ways round, measured from a
	// browser: expected the fallback and lost to the guard; expected the field
	// and lost to the cheap answer.
	held := strings.TrimSpace(artifactString(art, AssigneeField))
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
	// NIL-AS-VALUE IS NOT NO-VALUE, and a variadic makes them the same call.
	//
	// said is `...*Event` so that an ordinary claim stays a one-argument
	// decision. handleTodoAssign has one `*Event` to hand over and passes it
	// unconditionally - and claimHeardIn answers nil for a row raised in NO
	// ROOM, because there is no conversation for the handover to be announced
	// in. That nil arrives here as one element rather than as none, and
	// writeArtifactFields dereferences every event it is given.
	//
	// MEASURED on a scratch node, three arms: a roomless row claimed with expect
	// panicked the handler and dropped the connection; the same row raised in a
	// room was fine; the same roomless row assigned WITHOUT expect was fine -
	// AssignTodo takes said as a plain parameter one branch over, where a nil
	// simply means nothing to say. So the compare-and-set claim, which is the
	// whole mechanism stopping two seats taking one row, was dead on every row
	// filed off-board.
	//
	// Dropped here rather than at the door: every caller of a variadic gets to
	// decide whether it has something to pass, and a nil said means the same
	// thing at all of them.
	events := []*Event{entry}
	for _, e := range said {
		if e != nil {
			events = append(events, e)
		}
	}
	// A CLAIM OF NOBODY IS A RELEASE, and it moves both facts for AssignTodo's
	// reason: an unowned row cannot be `active`, so this write takes it back to
	// `todo` and says so in the log. It is here as well as there because a
	// careful caller - one that states what it expected to find - must not get a
	// worse outcome than a careless one. See queuecoherence.go.
	status := putDownStatus(art, name)
	if status != "" {
		back, err := statusEntryEvent(art, p, actor, actorKindOf(p), ActiveStatus, status)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, back)
	}
	// THE GUARD IS THE CLAIM, and it compares against what the caller expected
	// rather than against what this function just read: a row that moved in
	// between is exactly the case worth refusing.
	guard := `coalesce(fields->>'` + AssigneeField + `', '') = '` + sqlLiteral(expect) + `'`
	err = d.SetArtifactFieldsAndStatusIf(ctx, art, column, status, guard, events...)
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
