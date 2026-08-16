package main

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A waiter narrowed to what is addressed to it must still hear a person.
//
// The flaw this closes was invisible from inside the fleet and obvious from
// outside it: agents address each other by habit, so agent traffic matched
// "addressed to me" and forced a turn, while a human writing "who is here?"
// with no name and no addressee did not - so the person's messages were
// structurally the least likely in the room to be answered and the agents' were
// the most. Nobody was ignoring anybody. The filter did it.
//
// These cases are written so that the old predicate - addressee only - FAILS
// them. That is the point: a test that passes before the fix tests nothing.
func TestAWaiterNarrowedToItsOwnMailStillHearsAPerson(t *testing.T) {
	me := &store.Principal{UserID: "u-me", AgentID: "a-me"}
	addressedOnly := inboxFilter{addressed: true}

	person := func(meta string) *store.Event {
		return &store.Event{Actor: "u-someone", Meta: json.RawMessage(meta)}
	}

	t.Run("an unaddressed message from a person wakes it", func(t *testing.T) {
		e := person(`{"actor_kind":"user","actor_user":"u-someone"}`)
		if !wakesFor(me, e, addressedOnly) {
			t.Fatal("a person said something to the room and the waiter slept through it")
		}
	})

	t.Run("an unaddressed message from an agent does not", func(t *testing.T) {
		e := &store.Event{Actor: "a-other", Meta: json.RawMessage(`{"actor_kind":"agent"}`)}
		if wakesFor(me, e, addressedOnly) {
			t.Fatal("--to-me now wakes on every agent message, which is what it exists to avoid")
		}
	})

	t.Run("an agent addressing this waiter still wakes it", func(t *testing.T) {
		e := &store.Event{
			Actor: "a-other", Addressee: "a-me",
			Meta: json.RawMessage(`{"actor_kind":"agent"}`),
		}
		if !wakesFor(me, e, addressedOnly) {
			t.Fatal("the addressee half of the rule stopped working")
		}
	})

	t.Run("this waiter's own messages never wake it", func(t *testing.T) {
		// Even from the person side: an agent acts for a user, so a message
		// written through this principal must not wake the principal, or every
		// thing it says costs it a turn to be told what it just said.
		e := &store.Event{Actor: "u-me", Meta: json.RawMessage(`{"actor_kind":"user"}`)}
		if wakesFor(me, e, addressedOnly) {
			t.Fatal("it woke on its own message")
		}
	})

	t.Run("a client cannot claim to be a person", func(t *testing.T) {
		// The node stamps actor_kind from the principal at write time, so this
		// only checks the shapes a forged or broken meta can take: nothing that
		// is not an object with actor_kind "user" counts as a person.
		for _, meta := range []string{
			``, `null`, `"user"`, `[]`, `{"actor_kind":"USER"}`, `{"actor_kind":""}`,
			`{"actor_kind":"agent","claims":"user"}`, `not json at all`,
		} {
			e := &store.Event{Actor: "a-other", Meta: json.RawMessage(meta)}
			if wakesFor(me, e, addressedOnly) {
				t.Fatalf("meta %q was treated as a person", meta)
			}
		}
	})
}
