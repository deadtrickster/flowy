package flowy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// TestTheStaleCountNamesItsRows.
//
// `stale` was the only count on /api/nag carrying no ids. The four beside it -
// mine_todo, mine_waiting, answers_owed, unowned_waiting - all name their rows.
//
// The asymmetry is not cosmetic. nagLines suppresses mine_waiting's ids on the
// stated ground that the caller can list its own rows, and that ground does not
// extend here: a row is stale because nagStaleAfter elapsed against Updated,
// and that threshold lives in this file. A listing shows Updated and not the
// rule applied to it, so a consumer holding only the number must re-derive the
// 20 minutes to find the row - which is the duplication the workload probe was
// moved out of four bash scripts to end.
//
// Measured before the fix: board-nag fired on stale=1 and the seat could not
// tell which of ten active rows it meant.
func TestTheStaleCountNamesItsRows(t *testing.T) {
	const me = "claude-host"
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	mine := func(id string, age time.Duration, waitingOn string) *store.Artifact {
		attrs := map[string]any{store.AssigneeField: me}
		if waitingOn != "" {
			attrs[store.WaitingOnField] = waitingOn
		}
		fields, err := json.Marshal(attrs)
		if err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
		return &store.Artifact{
			ID: id, Status: "active", Fields: fields, Updated: now.Add(-age),
		}
	}

	var view nagView
	view.MineTodoIDs = []string{}
	view.MineWaitingIDs = []string{}
	view.AnswersOwedIDs = []string{}
	view.UnownedWaitingIDs = []string{}
	view.StaleIDs = []string{}
	view.count([]*store.Artifact{
		mine("01STALE", time.Hour, ""),
		mine("01FRESH", time.Minute, ""),
		mine("01BLOCKED", time.Hour, "deadtrickster"),
	}, me, map[string]bool{}, now)

	if view.Stale != 1 {
		t.Fatalf("stale = %d, want 1 - only 01STALE is an unblocked active row past the threshold", view.Stale)
	}
	// THE ACCEPTED CALL, not only the refusals. A test that asserted just the
	// two exclusions below would pass against a stale_ids that is never
	// appended to at all.
	if len(view.StaleIDs) != 1 || view.StaleIDs[0] != "01STALE" {
		t.Fatalf("stale_ids = %v, want [01STALE] - a count with no ids is a hunt", view.StaleIDs)
	}
	// AND THE TWO ROWS THAT ARE NOT STALE ARE NOT NAMED, which is what makes
	// the assertion above a difference rather than a list of everything mine.
	for _, no := range []string{"01FRESH", "01BLOCKED"} {
		for _, got := range view.StaleIDs {
			if got == no {
				t.Errorf("stale_ids names %s, which is not stale: %v", no, view.StaleIDs)
			}
		}
	}
}

// TestTheStaleLineSaysWhichRowWentQuiet.
//
// The count already printed; the row did not. "1 of your active row(s) have had
// no write for over 20 minutes" sends a seat holding ten of them to read all
// ten.
func TestTheStaleLineSaysWhichRowWentQuiet(t *testing.T) {
	out := nagLines(nagView{
		Open: 11, Mine: 3, Stale: 1, StaleIDs: []string{"01QUIETROW"},
	})
	if !strings.Contains(out, "01QUIETROW") {
		t.Errorf("the nag says a row went quiet and not which:\n%s", out)
	}
	// A BOARD WITH NOTHING STALE PRINTS NO IDS. Without this the assertion
	// above is satisfied by a line that always prints.
	clean := nagLines(nagView{Open: 11, Mine: 3})
	if strings.Contains(clean, "01QUIETROW") {
		t.Errorf("a board with nothing stale still named a row:\n%s", clean)
	}
}
