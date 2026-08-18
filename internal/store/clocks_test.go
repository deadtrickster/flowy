package store

import "testing"

// WHAT COUNTS AS EVIDENCE OF WORK, stated once so a new verb is a decision
// rather than an accident.
//
// The list is short on purpose. `updated` moves on any write and became useless
// for this question precisely because a rename, a tag edit or a category change
// all looked like progress - so counting "any event" here would rebuild the
// problem the columns exist to solve.
func TestOnlyRealWorkMovesTheClock(t *testing.T) {
	for _, kind := range []string{
		EventMergeGate, EventMergeLand, EventMergeAbandon,
		EventTodoNote, EventTodoStatus, EventTodoAssign,
	} {
		if !workEvidence(kind) {
			t.Errorf("%q should count as work - somebody did something to the row", kind)
		}
	}
}

// AND RENAMING IS NOT WORKING EITHER. todo.edit records a change to the row's
// WORDS, which is the case that made `updated` useless - if an edit moved this
// clock, "evidence of work" would widen back to "any write" one case at a time.
//
// claude-host caught this in review: my first list counted todo.edit, so the
// test asserted the bug.
func TestRenamingARowIsNotWorkingIt(t *testing.T) {
	if workEvidence(EventTodoEdit) {
		t.Error("todo.edit is a change to what the row SAYS, not progress on what it asks for")
	}
}

// Reading is not working. If a delivery or a presence poll moved last_worked,
// a row would look freshly worked because somebody LOOKED at it, and the nag
// built on this column would go quiet exactly when nothing is happening.
func TestLookingAtARowIsNotWorkingIt(t *testing.T) {
	for _, kind := range []string{"chat", "inbox.ack", "presence.poll", "artifact.read", ""} {
		if workEvidence(kind) {
			t.Errorf("%q must not count as work - it is somebody reading, not somebody doing", kind)
		}
	}
}
