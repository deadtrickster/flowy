package flowy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// NOBODY'S IS NOT THEREBY ANYBODY'S.
//
// 01M1SXMQQ0FV0WZHK5G0RJ7P41. The nag refused to call a blocked row work, but
// only for a row the seat already held. The rule was written inline in that arm
// and simply absent from the unowned one, so a row nobody holds and nobody can
// start was counted as available and offered to every seat, every fifteen
// minutes.
//
// Measured on the board 2026-09-06: both unowned rows were parked on the
// operator - one wanting a git command in a checkout 159 worktrees share, one
// wanting four naming decisions - and /api/nag still answered unowned: 2. The
// naming row had been picked up and put back down that way for sixteen days.
//
// It matters more for unowned than for mine, because of the advice attached:
// mcp_steal.go offers unowned rows as the ones that "need no negotiation -
// todo_assign one to yourself", and a row parked on the operator needs more
// negotiation than anything else on the board.
func TestARowParkedOnSomebodyIsNotAvailableWork(t *testing.T) {
	const me = "claude-host"

	// Built by hand rather than through DB.SetWaitingOn, which needs a
	// database. The rule under test is a pure reading of the row, and a check
	// that skips when DATABASE_URL is unset is a check that cannot fail.
	parked := func(on string) *store.Artifact {
		fields, err := json.Marshal(map[string]any{store.WaitingOnField: on})
		if err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
		return &store.Artifact{ID: "01PARKED", Fields: fields}
	}

	t.Run("parked on somebody else is not startable", func(t *testing.T) {
		if !parkedOnSomebodyElse(parked("deadtrickster"), me, false) {
			t.Fatal("a row waiting on the operator was called startable - this is what offers every seat a row nobody can move")
		}
	})

	// THE ARM THAT KEEPS IT HONEST. If everything with a mark were unavailable,
	// a row parked on THIS seat would vanish from its own board - and that row
	// is the one it can actually clear by answering.
	t.Run("parked on me is still mine to clear", func(t *testing.T) {
		if parkedOnSomebodyElse(parked(me), me, false) {
			t.Fatal("a row waiting on this seat was hidden from it, and answering is exactly what would unblock it")
		}
	})

	// AND AN ANSWER THAT ARRIVED UNPARKS IT. Without this the mark is a
	// one-way door: the question gets answered, the row stays invisible, and
	// nobody ever picks it back up.
	t.Run("an answered row is startable again", func(t *testing.T) {
		if parkedOnSomebodyElse(parked("deadtrickster"), me, true) {
			t.Fatal("the answer arrived and the row was still counted as blocked")
		}
	})

	t.Run("an unmarked row is plain work", func(t *testing.T) {
		if parkedOnSomebodyElse(&store.Artifact{ID: "01PLAIN"}, me, false) {
			t.Fatal("a row with no waiting_on was called blocked, which would empty the board")
		}
	})

	// THE ARM, NOT JUST THE RULE. The subtests above pass with the fix reverted
	// - measured - because they exercise the helper and the defect was that one
	// arm never called it. This counts a real board instead.
	t.Run("the unowned count does not offer a parked row", func(t *testing.T) {
		rows := []*store.Artifact{
			parked("deadtrickster"),
			{ID: "01FREE"},
		}
		var view nagView
		view.UnownedWaitingIDs = []string{}
		view.count(rows, me, map[string]bool{}, time.Now())

		if view.Unowned != 1 {
			t.Errorf("unowned = %d, want 1 - only 01FREE can actually be started", view.Unowned)
		}
		if view.UnownedWaiting != 1 {
			t.Errorf("unowned_waiting = %d, want 1 - the parked row has to stay visible somewhere", view.UnownedWaiting)
		}
		if len(view.UnownedWaitingIDs) != 1 || view.UnownedWaitingIDs[0] != "01PARKED" {
			t.Errorf("unowned_waiting_ids = %v, want [01PARKED] - a count with no ids is a hunt", view.UnownedWaitingIDs)
		}
	})
}
