package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The lock's own doors are BEHIND the token and IN the parameter table.
//
// Both halves matter and both are silent when wrong. Outside authenticate,
// anybody who can reach the port could freeze landing for fifteen minutes.
// Missing from routeParams, GET /api/lock?target=x is refused by the guard as
// an undeclared parameter - a read that works everywhere except where it is
// used.
func TestTheLockDoorsAreGuardedAndDeclared(t *testing.T) {
	src := readSource(t, "serve.go")
	for _, pattern := range []string{
		"POST /api/lock",
		"POST /api/lock/release",
		"GET /api/lock",
	} {
		if !strings.Contains(src, `api.HandleFunc("`+pattern+`"`) {
			t.Errorf("serve.go does not register %q on the guarded mux", pattern)
		}
		if _, ok := routeParams[pattern]; !ok {
			t.Errorf("%q is guarded and not in routeParams", pattern)
		}
	}
	// And the read declares the parameters it actually reads. A route that takes
	// one and does not say so answers 400 for its own caller.
	//
	// `item` joined `target` when the lock became per-project: the project a
	// lock belongs to is the ITEM's, because the gate door keys it that way and
	// two paths keying one lock differently is the defect that change closed.
	// So this door has to be told which item to ask about.
	want := map[string]bool{"target": true, "item": true}
	got := routeParams["GET /api/lock"]
	if len(got) != len(want) {
		t.Errorf(`GET /api/lock declares %v, want target and item`, got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("GET /api/lock declares %q, which it does not read", name)
		}
	}
}

// TAKING A LOCK WITHOUT SAYING WHAT FOR IS REFUSED.
//
// item is what a loser is told - "held by X for deploy" against "held by X" -
// and the second sends somebody looking for a landing that is not happening.
// It is also what the release must match, so an empty one would let a deploy
// hand back a landing's lock.
func TestTakingTheLockNeedsToSayWhatFor(t *testing.T) {
	// A nil db on purpose: if the guard is removed, this reaches the store and
	// panics rather than passing quietly.
	s := &server{}
	w := httptest.NewRecorder()
	s.handleTakeLock(w, httptest.NewRequest("POST", "/api/lock",
		strings.NewReader(`{"target":"master"}`)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "item is required") {
		t.Errorf("the refusal does not say what is missing: %s", w.Body.String())
	}
}

// A field the door does not know is a 400 naming it, like every other write
// door here. `targets` for `target` would otherwise silently lock the default.
func TestTheLockDoorRefusesAFieldItDoesNotKnow(t *testing.T) {
	s := &server{}
	w := httptest.NewRecorder()
	s.handleTakeLock(w, httptest.NewRequest("POST", "/api/lock",
		strings.NewReader(`{"targets":"master","item":"deploy"}`)))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "targets") {
		t.Errorf("the refusal does not name the field: %s", w.Body.String())
	}
}
