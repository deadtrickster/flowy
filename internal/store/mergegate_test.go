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
