package flowy

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A RAISER IS WHO THE WORK CAME FROM, AND NOBODY IS AN ANSWER.
//
// This file used to assert the opposite: that POST /api/artifacts stamps the
// CALLER as raiser on any queue row carrying none. That was written to answer
// "todos dont show reporter", and it contradicted a rule the room door already
// had (todos.go:141) - the raiser is taken from the message a todo was raised
// out of, and left empty otherwise, because owner_user already records the seat
// that typed it and a second copy of that answer is a fact invented rather than
// recorded.
//
// MEASURED, 2026-08-19: one seat filed two rows in one hour through two doors.
// `todo file --room general` left the raiser empty; `merge open` stamped
// claude-host. Same field, two meanings - "nobody asked for this" and "I typed
// this" - and no reader could tell which it was holding.
//
// So what is pinned here now is the ABSENCE. There is no raiserDefault to test;
// the assertion is that the create door leaves the field alone, which is a
// property of what this package does NOT contain.
func TestTheCreateDoorInventsNoRaiser(t *testing.T) {
	// The row as a caller sends it, with no raiser: nothing in this package may
	// put one on it. If a future change adds a default, this fails to compile
	// or this assertion stops holding - both are the alarm.
	art := &store.Artifact{Kind: "todo"}
	if got := store.RaiserOf(art); got != "" {
		t.Fatalf("a freshly built work row carries raiser %q before any door has touched it", got)
	}

	// And an EXPLICIT raiser is still the caller's to set - the rule is about
	// what the node invents, not about what a caller may say. This is the arm
	// that keeps the removal from being read as "raiser is gone".
	said := &store.Artifact{Kind: "todo", Fields: fieldsFor(t, map[string]any{
		store.RaiserField: "fish",
	})}
	if got := store.RaiserOf(said); got != "fish" {
		t.Fatalf("an explicitly named raiser read back as %q", got)
	}
}

// fieldsFor is a row's fields as the column holds them, so a test says what it
// means rather than hand-writing JSON it can be wrong about.
func fieldsFor(t *testing.T, f map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	return raw
}
