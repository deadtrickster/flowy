package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// TestAnAnswerSaysWhichProjectItIsAbout, at the helper the five doors share.
//
// One helper rather than five spellings is the whole point of api_scope.go: the
// fleet has been bitten twice this week by two readers rebuilding one shape, and
// five doors each stamping their own idea of "which project" is that defect
// waiting to happen.
func TestAnAnswerSaysWhichProjectItIsAbout(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)

	// An ordinary seat: the one project its token carries.
	got := answerScopeOf(req, &store.Principal{UserID: "u", Project: "flowy"})
	if got.Project != "flowy" || got.All {
		t.Fatalf("a seat in flowy is scoped %+v", got)
	}
	body := stampScope(map[string]any{"artifacts": []string{}}, got)
	if body["project"] != "flowy" {
		t.Errorf("the answer does not carry the project: %v", body)
	}
	if _, ok := body["all_projects"]; ok {
		t.Errorf("a single-project answer claims to span all of them: %v", body)
	}

	// A seat with no project names none rather than an empty string, because
	// "" and "everything" are different answers and a reader must not have to
	// guess which one a blank is.
	none := stampScope(map[string]any{}, answerScopeOf(req, &store.Principal{UserID: "u"}))
	if _, ok := none["project"]; ok {
		t.Errorf("a projectless seat's answer names a project: %v", none)
	}

	// This node's own administration reads everything, and says so.
	all := answerScopeOf(req, nil)
	if !all.All || all.Project != "" {
		t.Fatalf("the node's own read is scoped %+v", all)
	}
	body = stampScope(map[string]any{}, all)
	if body["all_projects"] != true {
		t.Errorf("an all-projects answer does not say so: %v", body)
	}
	if _, ok := body["project"]; ok {
		t.Errorf("an all-projects answer also names one project: %v", body)
	}
}

// TestStampingAScopeDoesNotChangeWhatWasAlreadyThere.
//
// It writes fields rather than nesting an object so that adding it to a door
// does not change the shape of anything on that answer: a reader that ignores
// the new field sees exactly what it saw before, which is what makes this safe
// to put on five doors at once.
func TestStampingAScopeDoesNotChangeWhatWasAlreadyThere(t *testing.T) {
	before := map[string]any{"artifacts": []int{1, 2}, "withheld": "something"}
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	after := stampScope(map[string]any{"artifacts": []int{1, 2}, "withheld": "something"},
		answerScope{Project: "flowy"})
	for k := range before {
		if _, ok := after[k]; !ok {
			t.Errorf("stamping dropped %q", k)
		}
	}
	if len(after) != len(before)+1 {
		t.Errorf("stamping added %d fields, want 1: %v", len(after)-len(before), after)
	}
	// And the untouched half still marshals to what it did.
	stripped := map[string]any{}
	for k, v := range after {
		if k != "project" {
			stripped[k] = v
		}
	}
	again, err := json.Marshal(stripped)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Errorf("the rest of the answer changed:\n before %s\n after  %s", raw, again)
	}
}

// TestARoomsProjectComesFromItsRowsAndNotItsReader.
//
// From the rows deliberately: a page can carry a message with no project at all
// - a direct message is exactly that - and stamping the reader's project onto a
// page of those would say something the rows do not.
func TestARoomsProjectComesFromItsRowsAndNotItsReader(t *testing.T) {
	pa, empty := "pa", ""
	if got := roomProjectOf([]*store.Event{{Project: &pa}}); got != "pa" {
		t.Errorf("a page of pa's rows is %q", got)
	}
	// A page with nothing on it names no project rather than guessing: a room
	// nobody has spoken in and a room this reader cannot see answer the same
	// way, and neither is evidence about a project.
	if got := roomProjectOf(nil); got != "" {
		t.Errorf("an empty page names %q", got)
	}
	if got := roomProjectOf([]*store.Event{{Project: nil}, {Project: &empty}}); got != "" {
		t.Errorf("a page of projectless rows names %q", got)
	}
	// And the first row that HAS one decides, so a direct message riding beside
	// a room's messages does not blank the answer.
	if got := roomProjectOf([]*store.Event{{Project: nil}, {Project: &pa}}); got != "pa" {
		t.Errorf("a page whose first row is projectless names %q", got)
	}
}
