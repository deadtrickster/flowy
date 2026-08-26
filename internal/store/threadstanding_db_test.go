package store

import (
	"encoding/json"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestFillThreadStandingAgainstARealRoom runs the standing queries against a
// real database - the two SQL statements are the part no unit test reaches, and
// a query string that only ever compiled is not one anybody has run.
func TestFillThreadStandingAgainstARealRoom(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "standing")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	say := func(id, thread, name string, parents []string) *Event {
		t.Helper()
		meta, err := json.Marshal(map[string]any{"actor_name": name})
		if err != nil {
			t.Fatalf("meta: %v", err)
		}
		e := &Event{
			ID: id, Type: "chat", Project: &project, Room: "general",
			Thread: thread, Parents: parents, Actor: p.UserID, Body: name + " said something",
			Meta: json.RawMessage(meta),
		}
		if err := db.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append %s: %v", name, err)
		}
		return e
	}

	// A thread this reader started and spoke in.
	mine := ulid.NewString()
	say(mine, mine, "claude-host", nil)
	minesReply := say(ulid.NewString(), mine, "dead-claude", []string{mine})

	// A thread between two OTHER seats, which this reader has never touched.
	theirs := ulid.NewString()
	say(theirs, theirs, "claude-lab2x1", nil)
	theirsReply := say(ulid.NewString(), theirs, "deadtrickster", []string{theirs})

	list := []*Event{minesReply, theirsReply}
	if err := db.FillThreadStanding(ctx, p, "claude-host", list); err != nil {
		t.Fatalf("fill: %v", err)
	}

	// THE READER'S OWN THREAD: spoken, rooted in its own message, and the reply
	// is TO it. This is the case where answering is expected.
	got := list[0].Standing
	if got == nil {
		t.Fatal("no standing filled for the reader's own thread")
	}
	if !got.Spoken || !got.RootMine || !got.RepliesToMe || got.RepliesTo != "claude-host" {
		t.Fatalf("own thread standing is %+v, want spoken/root/replies-to-me all true", got)
	}

	// SOMEBODY ELSE'S THREAD: none of it true. This is the case that cost a
	// morning - a correction in another seat's thread, in a room this seat
	// watches, acted on as though it were addressed here.
	got = list[1].Standing
	if got == nil {
		t.Fatal("no standing filled for the other seats' thread")
	}
	if got.Spoken || got.RootMine || got.RepliesToMe {
		t.Fatalf("another seats' thread reads as %+v, want every fact false - "+
			"this is the delivery that was acted on and should not have been", got)
	}
	if got.RepliesTo != "claude-lab2x1" {
		t.Fatalf("replies_to is %q, want claude-lab2x1 - a listener cannot say who is being answered", got.RepliesTo)
	}

	// AND A READER THAT NEVER SPOKE ANYWHERE gets false, not an error and not a
	// nil standing: "you are in none of these" is an answer.
	fresh := []*Event{minesReply, theirsReply}
	if err := db.FillThreadStanding(ctx, p, "nobody-"+ulid.NewString(), fresh); err != nil {
		t.Fatalf("fill for a silent reader: %v", err)
	}
	for i, e := range fresh {
		if e.Standing == nil || e.Standing.Spoken || e.Standing.RootMine || e.Standing.RepliesToMe {
			t.Fatalf("event %d for a silent reader reads %+v, want a filled standing with every fact false", i, e.Standing)
		}
	}

	// AN EMPTY READER NAME FILLS NOTHING - and must not claim "not your thread".
	none := []*Event{{ID: ulid.NewString(), Thread: mine}}
	if err := db.FillThreadStanding(ctx, p, "", none); err != nil {
		t.Fatalf("fill with no reader: %v", err)
	}
	if none[0].Standing != nil {
		t.Fatalf("an unnamed reader got standing %+v, want nil - absent and false are different", none[0].Standing)
	}
}
