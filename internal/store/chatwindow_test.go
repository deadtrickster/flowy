package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestEventsBeforeWalksTheLogBackwardsWithoutRepeatingOrSkipping is the whole
// claim EventsBefore makes, and the part that carries it is the TIE.
//
// A room opens on its last screenful and pages backwards as somebody scrolls,
// so the correctness question is not "does the first page look right" - it is
// whether walking the whole log one window at a time hands over every message
// exactly once. A descending page cuts at its OLD end, and two events written
// in the same instant on two nodes carry the same seq_hlc: a page that stopped
// between two of those and handed back its last row's reading would have the
// caller ask for "strictly below that" and step over the rest of them for good.
// The messages are not late, they never arrive, and nothing says so.
//
// So the seed puts three messages at ONE reading and places them where a limit
// of 5 has to cut through the middle of them. The assertion is the whole room,
// in order, reassembled out of the pages - not a count, because a count passes
// while two pages both hand over the same message and one is lost.
func TestEventsBeforeWalksTheLogBackwardsWithoutRepeatingOrSkipping(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "before-px")
	user := &User{Handle: "before-reader-" + ulid.NewString()}
	if err := db.InsertUser(ctx, user); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	reader := &Principal{UserID: user.ID, Project: project}
	room := "before-room-" + ulid.NewString()

	// The room, oldest first, so `want` is the transcript a reader should end up
	// with however it was fetched.
	want := make([]string, 0, 12)
	say := func(body string, at int64) {
		e := &Event{
			Type: ChatEventType, Project: &project, Room: room,
			Actor: user.ID, Body: body, SeqHLC: at,
		}
		if err := db.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append %q: %v", body, err)
		}
		want = append(want, e.ID)
	}

	for i := 1; i <= 6; i++ {
		say("older than the window", 0)
	}
	// THE TIE, three wide. With a window of 5 the newest page reaches its limit
	// on the middle of these, which is the cut the tie completion exists for.
	tie := packed(t, db)
	for i := 0; i < 3; i++ {
		say("said in the same instant as two others", tie)
	}
	for i := 1; i <= 3; i++ {
		say("in the window a room opens on", 0)
	}

	// Walking it: the newest window, then the window before that, until the room
	// runs out - exactly what the console does as somebody scrolls up.
	const window = 5
	got := []string{}
	before := int64(0)
	for page := 0; ; page++ {
		if page > len(want) {
			t.Fatalf("paging backwards did not terminate: %d pages over a room of %d",
				page, len(want))
		}
		list, err := db.EventsBefore(ctx, reader, EventQuery{
			Type: ChatEventType, Room: room, Before: before, Limit: window,
		})
		if err != nil {
			t.Fatalf("events before %d: %v", before, err)
		}
		if len(list) == 0 {
			break
		}
		// In log order, so a caller prepends a page and never sorts it.
		for i := 1; i < len(list); i++ {
			if list[i-1].SeqHLC > list[i].SeqHLC {
				t.Fatalf("page before %d came back out of log order at %d", before, i)
			}
		}
		ids := make([]string, len(list))
		for i, e := range list {
			ids[i] = e.ID
		}
		got = append(ids, got...)
		before = list[0].SeqHLC
	}

	if len(got) != len(want) {
		t.Fatalf("walking backwards gave %d messages, want the %d in the room",
			len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message %d of the reassembled room is %s, want %s\n"+
				"got  %v\nwant %v", i, got[i], want[i], got, want)
		}
	}

	// And the newest window on its own is the END of the room rather than its
	// beginning, which is the difference between this read and ListEvents - the
	// bug being fixed is a room that opened on its oldest page.
	newest, err := db.EventsBefore(ctx, reader, EventQuery{
		Type: ChatEventType, Room: room, Limit: window,
	})
	if err != nil {
		t.Fatalf("newest window: %v", err)
	}
	if len(newest) == 0 || newest[len(newest)-1].ID != want[len(want)-1] {
		t.Fatalf("the newest window does not end on the newest message in the room")
	}
}
