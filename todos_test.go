package main

import (
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
