package flowy

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A note on a row reaches the seat the row is assigned to, and nobody else.
//
// 01M17CVHH9FX3YVTHT9WMFSDDY. The inbox read chat events only, so a note had
// never once woken the agent it was about - while the operator's standing way
// to ask this fleet something is to assign a row and leave a note on it. The
// one channel a person is told to use was the one channel the recipient could
// not hear, and neither end could tell: the writer saw the note on the row, and
// silence from the reader looked like being ignored rather than like not being
// told.
//
// THE NEGATIVE CASES ARE THE POINT. A rule that is nearly right here is not a
// small mistake, it is a firehose: this board carries hundreds of rows, and a
// predicate that woke every seat for every note would be worse than the silence
// it replaced. So each case below fails a different way of being nearly right.
func TestANoteWakesTheSeatTheRowIsAssignedTo(t *testing.T) {
	me := &store.Principal{UserID: "u-me", AgentID: "a-me"}
	mine := inboxFilter{as: "claude-host"}

	note := func(actor string, meta map[string]string) *store.Event {
		raw, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("building the note: %v", err)
		}
		return &store.Event{Type: store.EventTodoNote, Actor: actor, Meta: raw}
	}

	t.Run("a note on my row wakes me", func(t *testing.T) {
		e := note("u-operator", map[string]string{
			"actor_kind": "user", "actor_user": "u-operator", "assignee": "claude-host",
		})
		if !wakesFor(me, e, mine) {
			t.Fatal("the operator left a note on a row assigned to me and the waiter slept through it - which is the whole defect")
		}
	})

	t.Run("a note on somebody else's row does not", func(t *testing.T) {
		e := note("u-operator", map[string]string{
			"actor_kind": "user", "actor_user": "u-operator", "assignee": "flowy-claude",
		})
		if wakesFor(me, e, mine) {
			t.Fatal("a note written to another seat woke this one - that is reading somebody else's mail, and at board scale it is every note on the node")
		}
	})

	t.Run("a note on an unassigned row wakes nobody", func(t *testing.T) {
		e := note("u-operator", map[string]string{"actor_kind": "user"})
		if wakesFor(me, e, mine) {
			t.Fatal("a note on a row nobody holds woke a seat - an unstamped note must not fall through to everybody")
		}
	})

	t.Run("my own note does not wake me", func(t *testing.T) {
		e := note("a-me", map[string]string{"actor_kind": "agent", "assignee": "claude-host"})
		if wakesFor(me, e, mine) {
			t.Fatal("a seat woke itself by writing on its own row")
		}
	})

	// THE NAME IS A NAME AND THE ADDRESSEE IS AN ID, and conflating them is how
	// this rule silently delivers nothing. isOwnActor compares against UserID
	// and AgentID; an assignee is "claude-host". A waiter holding no name must
	// therefore not match a stamped note by accident.
	t.Run("a waiter with no name matches nothing", func(t *testing.T) {
		e := note("u-operator", map[string]string{"assignee": "claude-host"})
		if wakesFor(me, e, inboxFilter{}) {
			t.Fatal("a nameless waiter was handed a note addressed to a name")
		}
	})

	t.Run("an assignee that is this principal's id does not count", func(t *testing.T) {
		e := note("u-operator", map[string]string{"assignee": "u-me"})
		if wakesFor(me, e, mine) {
			t.Fatal("the id matched where the name should have - the two namespaces are not interchangeable")
		}
	})

	// A note is not room traffic and not a mention, so the flags that narrow
	// those must not silently drop it. A seat that asked for mentions only has
	// not asked to stop hearing about its own work.
	t.Run("mentions-only still hears a note on my row", func(t *testing.T) {
		e := note("u-operator", map[string]string{"assignee": "claude-host"})
		if !wakesFor(me, e, inboxFilter{as: "claude-host", mentionsOnly: true}) {
			t.Fatal("a note on my own row is as addressed as a message can be, and mentions-only dropped it")
		}
	})

	// And the rule it must NOT overrule: ignoring is a person saying do not
	// tell me, and it is checked before this.
	t.Run("an ignored room still suppresses a note", func(t *testing.T) {
		e := note("u-operator", map[string]string{"assignee": "claude-host"})
		e.Room = "notes"
		want := inboxFilter{as: "claude-host", ignored: map[string]bool{"notes": true}}
		if wakesFor(me, e, want) {
			t.Fatal("a note arrived in a room the reader had explicitly ignored")
		}
	})

	// Chat is untouched: widening the types must not change who hears a message.
	t.Run("chat still behaves as it did", func(t *testing.T) {
		e := &store.Event{Actor: "a-other", Meta: json.RawMessage(`{"actor_kind":"agent"}`)}
		if !wakesFor(me, e, inboxFilter{as: "claude-host"}) {
			t.Fatal("an ordinary room message stopped waking an ordinary waiter")
		}
	})
}
