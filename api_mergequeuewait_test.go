package main

import "testing"

// The cursor is the whole of what "changed" means, so what it does and does not
// notice is the contract.
//
// IT MUST NOT MOVE ON TIME PASSING. `until` counts down on every read, so a
// cursor that included it would wake every waiter every tick - which is the
// feed that always speaks and therefore is never read. Pure: the digest is a
// function of the answer, and a check that needed a node to establish that
// would not be run.
func TestTheQueueCursorMovesOnWhatACallerActsOn(t *testing.T) {
	answer := func(mutate func(map[string]any, *mergeQueueItem)) map[string]any {
		it := mergeQueueItem{ID: "01ROW", Status: "todo", Gating: false}
		a := map[string]any{
			"target": "master", "target_tip": "abc1234",
			"lock": map[string]any{
				"held": true, "holder": "u-1", "item": "01ROW",
				"until": "2026-08-19T01:00:00Z", "taken_at": "2026-08-19T00:45:00Z",
			},
		}
		if mutate != nil {
			mutate(a, &it)
		}
		a["items"] = []mergeQueueItem{it}
		return a
	}

	base, err := queueCursor(answer(nil))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}

	// TIME PASSING IS NOT A CHANGE.
	ticked, err := queueCursor(answer(func(a map[string]any, _ *mergeQueueItem) {
		a["lock"].(map[string]any)["until"] = "2026-08-19T01:14:00Z"
	}))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if ticked != base {
		t.Error("the cursor moved because a countdown counted down - every waiter " +
			"wakes on every tick, which is the feed nobody reads")
	}

	// THE LOCK BEING GIVEN BACK IS. It is the change the waiter this door was
	// written for is waiting for.
	freed, err := queueCursor(answer(func(a map[string]any, _ *mergeQueueItem) {
		a["lock"] = map[string]any{"held": false}
	}))
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if freed == base {
		t.Error("the target was given back and the cursor did not move")
	}

	// AND SO IS A ROW BECOMING GATED, or a red arriving on it.
	for _, c := range []struct {
		name string
		with func(map[string]any, *mergeQueueItem)
	}{
		{"gating", func(_ map[string]any, it *mergeQueueItem) { it.Gating = true }},
		{"status", func(_ map[string]any, it *mergeQueueItem) { it.Status = "active" }},
		{"a red", func(_ map[string]any, it *mergeQueueItem) {
			it.Red = &mergeQueueRed{Tip: "deadbee"}
		}},
		{"a skip", func(_ map[string]any, it *mergeQueueItem) {
			it.Blocked = &mergeQueueBlocked{Why: "checked out elsewhere"}
		}},
	} {
		moved, err := queueCursor(answer(c.with))
		if err != nil {
			t.Fatalf("cursor: %v", err)
		}
		if moved == base {
			t.Errorf("%s arrived on the row and the cursor did not move", c.name)
		}
	}
}
