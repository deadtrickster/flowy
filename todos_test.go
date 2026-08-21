package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A room is one path segment on the chat routes, and a write that names one has
// to clear the same bar a URL does - or a todo lands in a room no client can
// ever ask for, and it is unreachable from the panel it was raised in.
func TestARoomIsOnePathSegmentOrItIsRefused(t *testing.T) {
	for _, ok := range []string{"", "general", "build", "  general  "} {
		if _, err := roomArg(ok); err != nil {
			t.Fatalf("roomArg(%q) was refused: %v", ok, err)
		}
	}
	if got, _ := roomArg("  general  "); got != "general" {
		t.Fatalf("a room came back as %q rather than trimmed", got)
	}
	for _, bad := range []string{"a/b", "two words", "a\tb"} {
		if _, err := roomArg(bad); err == nil {
			t.Fatalf("roomArg(%q) was allowed", bad)
		}
	}
}

// What an update keeps. A todo raised in #build and later marked done is still
// #build's: the room and the message it came out of are provenance, and an
// update that does not restate them says nothing about them - the rule as_of
// and supersedes follow on a report.
func TestAnUpdateKeepsTheRoomItWasRaisedIn(t *testing.T) {
	raised := withRoom(nil, "build", "01HMESSAGE")
	if raised[store.RoomField] != "build" || raised[store.MessageField] != "01HMESSAGE" {
		t.Fatalf("the raise did not carry where it came from: %v", raised)
	}

	// An edit that mentions neither.
	kept := withRoom(raised, "", "")
	if kept[store.RoomField] != "build" || kept[store.MessageField] != "01HMESSAGE" {
		t.Fatalf("an update that said nothing about the room dropped it: %v", kept)
	}

	// And an item that never had one does not grow an empty one: a key written
	// as "" is a room nothing is in, which is not the same as no room at all -
	// the filter matches on the value, so it would leave the item out of every
	// room's panel and out of nothing else.
	if fields := withRoom(nil, "", ""); fields != nil {
		t.Fatalf("a write that named no room invented fields: %v", fields)
	}
	if _, found := withRoom(nil, "build", "")[store.MessageField]; found {
		t.Fatal("a todo raised out of no message was given a message anyway")
	}
}

// What a name has to clear, and the words that mean nobody. They collapse to
// the empty assignee so that every surface says one word for one state - the
// panel showed "unowned" and "unassigned" side by side and they read as two.
//
// The rule moved into the store when the assignment verb landed - every door and
// the verb itself normalise through one function now - so this asks the store's
// name for it. The assertions are the ones it has always made.
func TestAnAssigneeIsAHandleOrItIsNobody(t *testing.T) {
	for _, nobody := range []string{"", "  ", "?", "-", "none", "Nobody", "TBD", "unassigned", "n/a"} {
		got, err := store.NormalizeAssignee(nobody)
		if err != nil {
			t.Fatalf("NormalizeAssignee(%q) was refused: %v", nobody, err)
		}
		if got != "" {
			t.Fatalf("NormalizeAssignee(%q) came back as %q rather than nobody", nobody, got)
		}
	}
	if got, err := store.NormalizeAssignee("  a-bench  "); err != nil || got != "a-bench" {
		t.Fatalf("a name came back as %q, %v", got, err)
	}
	for _, bad := range []string{"two\nlines", "a\tb", strings.Repeat("n", store.MaxAssigneeName+1)} {
		if _, err := store.NormalizeAssignee(bad); err == nil {
			t.Fatalf("NormalizeAssignee(%q) was allowed", bad)
		}
	}
}

// WHO IS CARRYING A TODO IS THE FIELD, AND A BODY'S OWNER LINE IS NOT A CLAIM.
//
// This test used to assert the opposite order - field first, body's `OWNER:`
// line second - which was the compatibility with a queue written before the
// field existed. The fallback is gone and this is its replacement, because the
// case worth pinning is no longer "which wins" but "the line is not an answer
// at all".
//
// It was removed on a measurement, not on taste: of 192 todos on the live node,
// 28 carry no field and an OWNER line, and every one of them is DONE - the
// single open row with no field carries no line either. What the fallback
// answered on those 28 is the AUTHOR, which the assign and done events say
// properly with a seat and a moment attached, and reading it as a claim is how
// three rows nobody was carrying were read as held.
func TestAnOwnerLineIsNotAClaim(t *testing.T) {
	fields := func(m map[string]any) json.RawMessage {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	// The line, wherever it sits in the body, says nothing about who is
	// carrying the row.
	old := &store.Artifact{Body: "OWNER: a-bench\nDEPENDS ON: nothing"}
	if got := assigneeOf(old); got != "" {
		t.Fatalf("a body's OWNER line was read as a claim: %q", got)
	}
	if got := assigneeOf(&store.Artifact{Body: "DEPENDS ON: x\nOWNER: not-really"}); got != "" {
		t.Fatalf("an OWNER further down the body was taken as the assignee: %q", got)
	}
	// Fields that say nothing about the assignee do not change that.
	old.Fields = fields(map[string]any{store.RoomField: "build"})
	if got := assigneeOf(old); got != "" {
		t.Fatalf("fields with no assignee in them answered %q", got)
	}

	// The field is the answer.
	old.Fields = fields(map[string]any{store.RoomField: "build", store.AssigneeField: "a-writer"})
	if got := assigneeOf(old); got != "a-writer" {
		t.Fatalf("the assignee field reads as %q", got)
	}
	// Including empty, which is the case a truthiness test gets wrong:
	// somebody put the work down on purpose and the row says so.
	old.Fields = fields(map[string]any{store.AssigneeField: ""})
	if got := assigneeOf(old); got != "" {
		t.Fatalf("unassigning a todo left it on %q", got)
	}
}

// What the room reads when somebody picks work up, puts it down, or hands it
// over. The previous holder is in the sentence because a handover is two names.
func TestTheRoomIsToldWhoTookIt(t *testing.T) {
	for _, c := range []struct{ was, now, want string }{
		{"", "a-bench", "gave the gearbox to a-bench"},
		{"a-bench", "a-writer", "moved the gearbox from a-bench to a-writer"},
		{"a-bench", "", "took the gearbox off a-bench"},
		{"", "", "left the gearbox unassigned"},
	} {
		if got := assignmentSaid("the gearbox", c.was, c.now); got != c.want {
			t.Fatalf("%q -> %q reads as %q, want %q", c.was, c.now, got, c.want)
		}
	}
}

// What a raise carries, and the two things the list has to survive.
//
// The field is space joined, which is the message convention and the reason the
// console has one splitter rather than two - so a blank entry is not a value
// this encoding can hold, and an id repeated is a second card for one file. The
// tidy happens BEFORE the ids are checked against the reader, so what is stored
// is what was validated.
func TestWhatARaiseCarriesIsTidiedBeforeItIsChecked(t *testing.T) {
	got := carriedFiles([]string{" 01HONE ", "", "01HTWO", "01HONE", "   "})
	want := []string{"01HONE", "01HTWO"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v - order is what the cards are drawn in", got, want)
		}
	}
	// And a raise that named no file grows no field: a key written as "" is a
	// row that carries an attachment with no id, which every reader that splits
	// on a space would then have to special-case.
	if files := carriedFiles(nil); files != nil {
		t.Fatalf("a raise that attached nothing produced %v", files)
	}
	if files := carriedFiles([]string{"", "  "}); files != nil {
		t.Fatalf("a raise that attached blanks produced %v", files)
	}
}
