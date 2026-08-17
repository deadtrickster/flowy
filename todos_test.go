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

// The order the field and the body are read in, which is the whole of the
// compatibility with a queue written before the field existed.
func TestTheAssigneeFieldOutranksTheOwnerLine(t *testing.T) {
	fields := func(m map[string]any) json.RawMessage {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	// No field at all: the body is the answer, as it has always been.
	old := &store.Artifact{Body: "OWNER: a-bench\nDEPENDS ON: nothing"}
	if got := assigneeOf(old); got != "a-bench" {
		t.Fatalf("a todo written with an OWNER line reads as %q", got)
	}
	// Not the first line, so not a claim about this item.
	if got := assigneeOf(&store.Artifact{Body: "DEPENDS ON: x\nOWNER: not-really"}); got != "" {
		t.Fatalf("an OWNER further down the body was taken as the assignee: %q", got)
	}
	// A room on the item and no assignee is still the body's.
	old.Fields = fields(map[string]any{store.RoomField: "build"})
	if got := assigneeOf(old); got != "a-bench" {
		t.Fatalf("fields with no assignee in them changed the answer to %q", got)
	}

	// The field wins.
	old.Fields = fields(map[string]any{store.RoomField: "build", store.AssigneeField: "a-writer"})
	if got := assigneeOf(old); got != "a-writer" {
		t.Fatalf("the assignee field did not outrank the OWNER line: %q", got)
	}
	// And it wins EMPTY, which is the case a truthiness test gets wrong:
	// somebody put the work down on purpose, and falling back to the OWNER
	// line still in the body would hand it straight back to them.
	old.Fields = fields(map[string]any{store.AssigneeField: ""})
	if got := assigneeOf(old); got != "" {
		t.Fatalf("unassigning a todo whose body names an owner left it on %q", got)
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
