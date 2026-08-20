package store

import (
	"encoding/json"
	"errors"
	"testing"
)

// A LANDED ROW IS REFUSED AS LANDED, not as stale.
//
// Measured on the live node 2026-08-20: 01M0ESQD8Q, status done, landed_tip
// e6f1121, master e6f1121, and the door answered "the target moved after its
// gate ran ... so re-gate it on e6f1121". The target did move - this row landed
// on it - so gated_base necessarily differs from the tip and EVERY landed row
// satisfies the moved-target test. The advice was to spend five minutes
// re-gating a closed row, and a reader took it as a stall in the queue.
//
// THE SECOND CASE IS THE ONE THAT WOULD HAVE BEEN MISSED: a done row that never
// recorded a gated tip. Asking the landing question after the gate questions
// would refuse it as ungated - the same wrong answer wearing the other code -
// so the order of the checks is what this asserts, not just the message.
func TestALandedRowIsRefusedAsLandedRatherThanStale(t *testing.T) {
	row := func(status string, fields map[string]string) *Artifact {
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("fields: %v", err)
		}
		return &Artifact{ID: "01M0ROW", Kind: MergeKind, Status: status, Fields: raw}
	}

	landed := row("done", map[string]string{
		"branch": "feat/x", "target": "master",
		"gated_tip": "e6f1121", "gated_base": "19daf10", "landed_tip": "e6f1121",
	})
	err := MergeAdmissible(landed, "e6f1121")
	var refusal *ErrMergeNotAdmissible
	if !errors.As(err, &refusal) {
		t.Fatalf("a landed row was admitted: %v", err)
	}
	if refusal.Code != RefusalMergeLanded {
		t.Errorf("code is %q, want %q - %q sends the caller to re-gate a closed row",
			refusal.Code, RefusalMergeLanded, refusal.Code)
	}

	// Done, and never gated. The landing question has to come first or this is
	// refused as ungated.
	closed := row("done", map[string]string{"branch": "feat/y", "target": "master"})
	err = MergeAdmissible(closed, "e6f1121")
	if !errors.As(err, &refusal) || refusal.Code != RefusalMergeLanded {
		t.Errorf("a closed ungated row answered %v - the landing check must be asked first", err)
	}

	// And the arm that must not move: an OPEN row gated on a base the target
	// has left is still stale, which is the refusal that has cost this fleet
	// whole days.
	stale := row("todo", map[string]string{
		"branch": "feat/z", "target": "master",
		"gated_tip": "aaaaaaa", "gated_base": "19daf10",
	})
	err = MergeAdmissible(stale, "e6f1121")
	if !errors.As(err, &refusal) || refusal.Code != RefusalMergeStaleGate {
		t.Errorf("an open stale row answered %v, want %q", err, RefusalMergeStaleGate)
	}
}
