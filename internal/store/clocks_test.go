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
		EventTodoNote, EventTodoStatus, EventTodoAssign, EventTodoEdit,
	} {
		if !workEvidence(kind) {
			t.Errorf("%q should count as work - somebody did something to the row", kind)
		}
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
