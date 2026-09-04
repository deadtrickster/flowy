package flowy

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A NOTE ON A ROW NOBODY HOLDS REACHES THE SEAT THAT RAISED IT.
//
// 01M1PEMY3C9BJHR6GW15A6K1SD. TestANoteWakesTheSeatTheRowIsAssignedTo beside
// this one settled the assigned case, and its rule ended at the assignee: an
// unassigned row's note woke nobody, on the reasoning that "a note on a row
// nobody holds is a note to the board, and the board is read by looking at it".
//
// THE PREMISE FAILED ON 2026-09-04. The operator answered a question by noting
// on an UNOWNED row - unowned because it was parked on THEM, which is what a row
// waiting on a person looks like. Nobody reads the board on a schedule, so the
// seat that had asked found the answer 70 minutes later, by accident, while
// looking for unclaimed work.
//
// wakesFor's own comment had deferred exactly this and said what it wanted: "a
// note on a row you merely RAISED is the obvious second case and is not here...
// it can be added on its own evidence." This is that evidence, and the rule is
// the narrow form of it.
//
// THE PRICE THE COMMENT NAMED IS NOT PAID, and the second test is what proves
// it. Waking the raiser of every note would double what a busy board delivers -
// hundreds of rows, each with a holder AND a raiser. The raiser is consulted
// ONLY when there is no assignee, so an assigned row delivers exactly what it
// delivered before, and what changes is the set that reached nobody at all.
func TestANoteOnAnUnownedRowWakesTheSeatThatRaisedIt(t *testing.T) {
	me := &store.Principal{UserID: "u-me", AgentID: "a-me"}
	mine := inboxFilter{as: "flowy-claude"}

	note := func(meta map[string]string) *store.Event {
		raw, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("building the note: %v", err)
		}
		return &store.Event{Type: store.EventTodoNote, Actor: "u-operator", Meta: raw}
	}

	t.Run("unowned and raised by me wakes me", func(t *testing.T) {
		e := note(map[string]string{
			"actor_kind": "user", "actor_user": "u-operator",
			store.RaiserField: "flowy-claude",
		})
		if !wakesFor(me, e, mine) {
			t.Fatal("the operator answered on a row I raised and nobody holds, and the waiter slept through it - " +
				"which is the whole defect: a question parked on a person is unowned BECAUSE it is parked")
		}
	})

	t.Run("unowned and raised by somebody else does not", func(t *testing.T) {
		e := note(map[string]string{
			"actor_kind": "user", "actor_user": "u-operator",
			store.RaiserField: "claude-host",
		})
		if wakesFor(me, e, mine) {
			t.Fatal("a note on somebody else's unowned row woke me - the raiser rule has to name ONE seat, " +
				"or it is the firewall-less version the old comment refused")
		}
	})

	// THE PRICE, ASSERTED. An ASSIGNED row must not consult the raiser at all,
	// or a busy board delivers every note twice - to its holder and to whoever
	// filed it. This is the case that would make the change a firehose, and it
	// is the reason the raiser is a fallback rather than a second rule.
	t.Run("an assigned row does not also wake its raiser", func(t *testing.T) {
		e := note(map[string]string{
			"actor_kind": "user", "actor_user": "u-operator",
			store.AssigneeField: "claude-host",
			store.RaiserField:   "flowy-claude",
		})
		if wakesFor(me, e, mine) {
			t.Fatal("a note on a row assigned to somebody else woke me because I raised it - " +
				"that doubles what every busy board delivers, which is the cost the old comment refused to pay")
		}
	})

	t.Run("assigned to me still wakes me when somebody else raised it", func(t *testing.T) {
		e := note(map[string]string{
			"actor_kind": "user", "actor_user": "u-operator",
			store.AssigneeField: "flowy-claude",
			store.RaiserField:   "claude-host",
		})
		if !wakesFor(me, e, mine) {
			t.Fatal("adding the fallback broke the case that already worked - a note on MY row must still reach me")
		}
	})

	// NEITHER STAMP IS NOT A WILDCARD. A note carrying no assignee and no raiser
	// is a row this node recorded neither for, and it wakes nobody. Asserted
	// because the failure mode of a fallback chain is that the last link becomes
	// "everybody" - and an empty string compared against an empty filter would
	// do exactly that.
	t.Run("neither stamp wakes nobody", func(t *testing.T) {
		e := note(map[string]string{"actor_kind": "user", "actor_user": "u-operator"})
		if wakesFor(me, e, mine) {
			t.Fatal("a note with no assignee and no raiser woke a seat - the fallback has become a wildcard")
		}
		if wakesFor(me, e, inboxFilter{as: ""}) {
			t.Fatal("a waiter with no name was woken by a note with no stamps - two empty strings matched, " +
				"which is the one way a delivery rule turns into a broadcast")
		}
	})
}
