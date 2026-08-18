package store

import (
	"encoding/json"
	"testing"
	"time"
)

// A declaration is believed for a bounded time. Pure, no database: the whole
// point is the reading, and the reading must not need a run to be alive.

func gateItem(t *testing.T, fields map[string]any) *Artifact {
	t.Helper()
	a := &Artifact{ID: "01MERGE", Kind: MergeKind}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	a.Fields = raw
	return a
}

func TestAFreshDeclarationIsBelieved(t *testing.T) {
	now := time.Now()
	a := gateItem(t, map[string]any{
		GateRunField: "c7856e8f47e3",
		GateAtField:  now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
	})
	if !GatingAt(a, now) {
		t.Fatal("a run declared two minutes ago is still measuring")
	}
}

// The defect this replaces: a run that died left a flag nobody would ever clear,
// and board-nag told the whole room not to land for as long as it sat there.
func TestAStaleDeclarationStopsBlockingOnItsOwn(t *testing.T) {
	now := time.Now()
	a := gateItem(t, map[string]any{
		GateRunField: "c7856e8f47e3",
		GateAtField:  now.Add(-GateBelievedFor - time.Minute).Format(time.RFC3339Nano),
	})
	if GatingAt(a, now) {
		t.Fatal("a declaration older than the belief window must stop blocking without anybody clearing it")
	}
}

// A verdict ends the declaration, whatever the stamp says. Gating is about now.
func TestAVerdictEndsTheDeclaration(t *testing.T) {
	now := time.Now()
	a := gateItem(t, map[string]any{
		GateRunField:  "c7856e8f47e3",
		GatedTipField: "86dd33f",
		GateAtField:   now.Format(time.RFC3339Nano),
	})
	if GatingAt(a, now) {
		t.Fatal("a request with a verdict is not being measured any more")
	}
}

// Absent reads as NOT gating. Every row written before this field has no stamp,
// and the safe reading of "I do not know when this started" is not "block the
// queue forever" - which is the exact mistake being fixed.
func TestAnUnstampedDeclarationDoesNotBlockForever(t *testing.T) {
	now := time.Now()
	a := gateItem(t, map[string]any{GateRunField: "c7856e8f47e3"})
	if GatingAt(a, now) {
		t.Fatal("a declaration with no stamp must not block the queue indefinitely")
	}
	// And nothing declared at all is plainly not gating.
	if GatingAt(gateItem(t, map[string]any{}), now) {
		t.Fatal("a request nobody declared is not being measured")
	}
	if GatingAt(nil, now) {
		t.Fatal("nothing at all is not being measured")
	}
}

// A stamp that does not parse is not a licence to block. Same rule as absent.
func TestAnUnreadableStampDoesNotBlock(t *testing.T) {
	a := gateItem(t, map[string]any{
		GateRunField: "c7856e8f47e3",
		GateAtField:  "half past four",
	})
	if GatingAt(a, time.Now()) {
		t.Fatal("an unparseable stamp must not be believed")
	}
}

// THE RE-GATE. A branch is gated, master moves, the branch is gated again -
// which on a busy day is nearly every gate there is. The second declaration must
// read as gating, and it did not: GatingAt refuses a row that carries a tip, and
// the declaring path only ever wrote gated_tip, never cleared it. So the queue's
// only collision guard was off for every gate after a branch's first, and two
// agents landing on one tip could not have been caught by the check they were
// told to make. Found by @claude-host using the thing.
func TestARegateReadsAsGating(t *testing.T) {
	now := time.Now()
	fields := map[string]any{}

	applyGate(fields, "c7856e8f47e3", "", now.Add(-30*time.Minute))
	applyGate(fields, "c7856e8f47e3", "86dd33f", now.Add(-25*time.Minute))
	applyGate(fields, "13bf29c377a2", "", now.Add(-2*time.Minute))

	a := gateItem(t, fields)
	if !GatingAt(a, now) {
		t.Fatal("the second run on a branch is measuring it exactly as much as the first")
	}
}

// The other half, and the dangerous one: a re-gate of a flaky run on the SAME
// tip. Leaving the superseded verdict in place let the queue keep admitting the
// branch on evidence that was at that moment being re-measured.
func TestADeclarationDropsTheVerdictItSupersedes(t *testing.T) {
	now := time.Now()
	fields := map[string]any{}

	applyGate(fields, "c7856e8f47e3", "86dd33f", now.Add(-10*time.Minute))
	if got := GatedTipOf(gateItem(t, fields)); got != "86dd33f" {
		t.Fatalf("a verdict records its tip: got %q", got)
	}

	applyGate(fields, "13bf29c377a2", "", now)
	if got := GatedTipOf(gateItem(t, fields)); got != "" {
		t.Fatalf("declaring a new run withdraws the verdict it replaces: still holding %q", got)
	}
}

// And the verdict still ends the declaration, in the same fields, in order - the
// stamp must not survive the tip that answers it.
func TestAVerdictAfterARegateClearsTheStamp(t *testing.T) {
	now := time.Now()
	fields := map[string]any{}

	applyGate(fields, "13bf29c377a2", "", now.Add(-4*time.Minute))
	applyGate(fields, "13bf29c377a2", "6e1a599", now)

	a := gateItem(t, fields)
	if GatingAt(a, now) {
		t.Fatal("a request with a verdict is not being measured any more")
	}
	if got := GatedTipOf(a); got != "6e1a599" {
		t.Fatalf("the verdict is the tip it measured: got %q", got)
	}
}
