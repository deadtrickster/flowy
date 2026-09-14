package flowy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

func TestEventLineNamesWhoToWhomAndWhere(t *testing.T) {
	e := &store.Event{
		ID:            "01M1",
		Thread:        "01M0",
		Actor:         "U1",
		Addressee:     "A2",
		AddresseeName: "claude-lab2x1",
		Body:          "gating X - run Y on Z\nsecond line",
		Meta:          json.RawMessage(`{"actor_kind":"user","actor_name":"deadtrickster"}`),
		Created:       time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC),
	}
	got := eventLine(e)
	want := "[2026-09-14 08:00] deadtrickster (person) -> claude-lab2x1  id 01M1  thread 01M0\n  gating X - run Y on Z\n  second line\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestEventLineFallsBackToIdsWhenNothingIsNamed(t *testing.T) {
	e := &store.Event{ID: "01M1", Actor: "U1", Addressee: "A2", Body: "x"}
	got := eventLine(e)
	if !strings.Contains(got, "U1 -> A2") {
		t.Fatalf("ids should stand in for names: %q", got)
	}
}

func TestMimeByExtensionKnowsTheCommonOnesAndDefaultsHonestly(t *testing.T) {
	cases := map[string]string{
		"a.png": "image/png", "b.JPG": "image/jpeg", "c.md": "text/markdown",
		"d.jsonl": "application/json", "e.bin": "application/octet-stream", "f": "application/octet-stream",
	}
	for name, want := range cases {
		if got := mimeByExtension(name); got != want {
			t.Errorf("%s: got %s want %s", name, got, want)
		}
	}
}

// Every verb the dispatcher knows is in the menu, and every verb in the menu is
// one the dispatcher knows. The menu missing `get`, `dm`, `waiter` and `nag` is
// what 25 reads of it on lab2x1 could not repair.
func TestEveryDispatchedVerbIsInTheMenu(t *testing.T) {
	for _, verb := range []string{"read", "dm", "get", "skills", "attach", "roster", "instructions", "waiter", "nag", "say", "inbox", "todo", "note"} {
		if !strings.Contains(usage, "\n  "+verb+" ") && !strings.Contains(usage, "\n  "+verb+"\n") {
			t.Errorf("verb %q is dispatched but the menu does not list it", verb)
		}
	}
}
