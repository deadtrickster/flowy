package main

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// The wake-up is the whole point of the feature, so it is checked as a wake-up
// rather than as a parse.
//
// A waiter narrowed with --to-me has to be woken by "@thatname hello" the way
// it is woken by --to, and left alone by "@somebodyelse hello". Everything in
// between - the parse, the resolution, the addressee column - is only worth
// having if it lands in the first clause of wakesFor, so this exercises the
// path end to end from the body an agent typed to the decision to interrupt.
//
// It is written to FAIL before the mention parse existed: with nothing filling
// the addressee in, a message from an agent that names this waiter in prose
// left the addressee empty, wakesFor fell through to saidByAPerson, an agent is
// not a person, and the waiter slept through a message that had its name in it.
func TestAWaiterNarrowedToItsOwnMailWakesOnAMentionOfIt(t *testing.T) {
	me := &store.Principal{UserID: "u-me", AgentID: "a-me"}
	addressedOnly := inboxFilter{addressed: true}

	// From an agent, deliberately: a person's message wakes this waiter
	// whatever it says, so only an agent's can show that the mention did it.
	said := func(t *testing.T, body string) *store.Event {
		t.Helper()
		found, err := resolveMentions(body, testRoster)
		if err != nil {
			t.Fatalf("resolve the mentions in %q: %v", body, err)
		}
		return &store.Event{
			Actor:     "a-other",
			Addressee: mentionAddressee(found),
			Body:      body,
			Meta:      json.RawMessage(`{"actor_kind":"agent"}`),
		}
	}

	t.Run("a mention of this waiter wakes it", func(t *testing.T) {
		if !wakesFor(me, said(t, "@thatname hello"), addressedOnly) {
			t.Fatal("an agent wrote this waiter's name and the waiter slept through it")
		}
	})

	t.Run("a mention of somebody else does not", func(t *testing.T) {
		if wakesFor(me, said(t, "@somebodyelse hello"), addressedOnly) {
			t.Fatal("--to-me woke on a message addressed to another principal")
		}
	})

	t.Run("a mention mid-sentence is the same as one at the front", func(t *testing.T) {
		if !wakesFor(me, said(t, "the deploy looks wrong, @thatname - can you look?"), addressedOnly) {
			t.Fatal("a name inside the sentence did not address anybody, which is the whole feature")
		}
	})

	t.Run("an email address wakes nobody", func(t *testing.T) {
		// The naive-regex case, asserted where it would do damage: a pasted
		// address must not interrupt whoever holds that handle.
		if wakesFor(me, said(t, "write to thatname@example.com about it"), addressedOnly) {
			t.Fatal("an email address in the body woke a waiter")
		}
	})
}
