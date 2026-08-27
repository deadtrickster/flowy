package flowy

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

// A PERSON WHO NAMES ONE AGENT HAS NOT ADDRESSED THE ROOM.
//
// wakesFor's addressed clause used to end `|| saidByAPerson(e)`, which asked
// only whether a person wrote it and never whether they said who they meant.
// So the operator writing "@dead-claude can continue grinding thru todos" woke
// claude-host's addressed-only listener too, and a seat went looking at work
// that had another seat's name on it. It is the same complaint as "square size
// wasnt addressed to you", one layer down from where it kept being noticed.
//
// The half that must NOT regress is in the third case: a person who names
// nobody still reaches everybody. That clause was itself a fix - without it a
// human's "who is here?" matched nothing and their messages were structurally
// the least likely in the room to be answered.
func TestAPersonNamingAnotherAgentDoesNotWakeThisWaiter(t *testing.T) {
	me := &store.Principal{UserID: "u-me", AgentID: "a-me"}
	addressedOnly := inboxFilter{addressed: true}

	byAPerson := func(addressee, body string) *store.Event {
		return &store.Event{
			Actor:     "u-operator",
			Addressee: addressee,
			Body:      body,
			Meta:      json.RawMessage(`{"actor_kind":"user"}`),
		}
	}

	t.Run("addressed to another agent, it sleeps", func(t *testing.T) {
		if wakesFor(me, byAPerson("a-somebody-else", "@somebody-else take the todos"), addressedOnly) {
			t.Fatal("--to-me woke on a person's message addressed to a different agent")
		}
	})

	t.Run("addressed to this waiter, it wakes", func(t *testing.T) {
		if !wakesFor(me, byAPerson("a-me", "take the todos"), addressedOnly) {
			t.Fatal("a person addressed this waiter by name and it slept")
		}
	})

	t.Run("addressed to nobody, it still wakes", func(t *testing.T) {
		if !wakesFor(me, byAPerson("", "who is here?"), addressedOnly) {
			t.Fatal("a person's broadcast must still reach everybody - that clause was its own fix")
		}
	})
}

// A WAITER FOCUSED ON ONE PROJECT hears all of it, and hears the others only
// when they name it.
//
// 01M125..., the operator: "I wanted to have you as a hause/flowy master. But
// you pick things up and participate in Labs and Oracle ... I cant reconfigure
// your watch to deliver only mentioned on your non-home projects". A seat that
// reaches two projects was handed both in full, and inboxFilter had no field
// that could say otherwise.
//
// The two halves are separate cases because they fail separately: a focus that
// silenced the home project would be a seat that stopped hearing its own room,
// and a focus that let unaddressed foreign traffic through would not have
// fixed anything.
func TestAFocusedWaiterHearsItsOwnProjectAndOnlyMentionsElsewhere(t *testing.T) {
	me := &store.Principal{UserID: "u-me", AgentID: "a-me"}
	focused := inboxFilter{focus: "flowy"}

	project := func(name string) *string { return &name }
	said := func(p *string, addressee, body string) *store.Event {
		return &store.Event{
			Actor:     "a-other",
			Addressee: addressee,
			Project:   p,
			Body:      body,
			Meta:      json.RawMessage(`{"actor_kind":"agent"}`),
		}
	}

	t.Run("its own project arrives unaddressed", func(t *testing.T) {
		if !wakesFor(me, said(project("flowy"), "", "the drainer is red again"), focused) {
			t.Fatal("a focused waiter stopped hearing the project it is focused on")
		}
	})

	t.Run("another project does not", func(t *testing.T) {
		if wakesFor(me, said(project("Lab"), "", "sweep is at 40%"), focused) {
			t.Fatal("unaddressed traffic from another project woke a focused waiter")
		}
	})

	t.Run("another project does when it names this waiter", func(t *testing.T) {
		if !wakesFor(me, said(project("Lab"), "a-me", "@thatname what is the n here?"), focused) {
			t.Fatal("a mention in another project did not reach a focused waiter, which is the one thing it must do")
		}
	})

	t.Run("a projectless message needs to name it too", func(t *testing.T) {
		if wakesFor(me, said(nil, "", "no project at all"), focused) {
			t.Fatal("a projectless message is not in the focus and must not arrive unaddressed")
		}
		if !wakesFor(me, said(nil, "a-me", "no project, but yours"), focused) {
			t.Fatal("a projectless message that names this waiter did not arrive")
		}
	})

	t.Run("no focus is every project, as before", func(t *testing.T) {
		if !wakesFor(me, said(project("Lab"), "", "sweep is at 40%"), inboxFilter{}) {
			t.Fatal("an unfocused waiter stopped hearing another project - this flag must be opt-in")
		}
	})
}

// MENTIONS-ONLY IS THE STRICT MODE, and its whole point is the case --to-me
// lets through: a person's message that names nobody.
//
// The operator, 2026-08-27: "we need another mode - only deliver explicit
// mentions". --to-me passes those deliberately - a human writing "who is here?"
// names no one, and before that clause their messages were the least likely in
// the room to be answered. A seat asked to stay out of a room needs the other
// answer, so this is a second setting rather than a change to the first.
func TestMentionsOnlyDeliversNothingThatDoesNotNameYou(t *testing.T) {
	me := &store.Principal{UserID: "u-me", AgentID: "a-me"}
	strict := inboxFilter{mentionsOnly: true}
	loose := inboxFilter{addressed: true}

	byAPerson := func(addressee, body string) *store.Event {
		return &store.Event{
			Actor: "u-operator", Addressee: addressee, Body: body,
			Meta: json.RawMessage(`{"actor_kind":"user"}`),
		}
	}

	t.Run("a person's broadcast reaches --to-me but not --mentions", func(t *testing.T) {
		broadcast := byAPerson("", "who is here?")
		if !wakesFor(me, broadcast, loose) {
			t.Fatal("--to-me stopped passing a person's broadcast, which is a separate rule and must not move")
		}
		if wakesFor(me, broadcast, strict) {
			t.Fatal("--mentions delivered a message that names nobody, which is the one thing it exists to refuse")
		}
	})

	t.Run("a message that names this principal arrives", func(t *testing.T) {
		if !wakesFor(me, byAPerson("a-me", "take this row"), strict) {
			t.Fatal("--mentions dropped a message addressed to this principal")
		}
	})

	t.Run("a message naming somebody else does not", func(t *testing.T) {
		if wakesFor(me, byAPerson("a-else", "@somebody take this row"), strict) {
			t.Fatal("--mentions delivered somebody else's mail")
		}
	})

	t.Run("strict wins when both are set", func(t *testing.T) {
		both := inboxFilter{addressed: true, mentionsOnly: true}
		if wakesFor(me, byAPerson("", "who is here?"), both) {
			t.Fatal("passing both flags widened the filter - the stricter one must decide")
		}
	})
}
