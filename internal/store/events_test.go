package store

import (
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A MESSAGE TIME IS THE SAME STRING WHATEVER ZONE THE NODE RUNS IN.
//
// createdNow writes UTC, but `created` is a timestamptz and lib/pq hands it
// back in the session's zone - so the same row reads as ...Z from a node whose
// process is in UTC and as ...+02:00 from one that is not. Measured on
// 2026-08-21, from a node started in a CEST shell: one JSON answer carried
//
//	created 2026-08-21T06:10:22.312542+02:00
//	now     2026-08-21T04:10:22Z
//
// which is one instant with two faces, in one body, from one node.
//
// It matters because every renderer of a message time SLICES THE STRING -
// chat-hook.sh takes created[11:16] to print "[06:10]" beside a room name - so
// on a node outside UTC the fleet's signal disagrees with the fleet's logs by
// the offset and nothing says which is right. The zone of the machine a node
// happens to run on is not a fact about the message.
func TestAMessageTimeComesBackInUTC(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "utc")
	p := &Principal{UserID: "u-utc", Project: project}

	room := "clocks"
	e := &Event{
		Type: ChatEventType, Project: &project, Room: room, Thread: ulid.NewString(),
		Actor: p.UserID, Body: "what time is it",
	}
	if err := db.AppendEvent(ctx, e); err != nil {
		t.Fatalf("append: %v", err)
	}

	list, err := db.ListEvents(ctx, p, EventQuery{Type: ChatEventType, Room: room})
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("the message did not come back, so nothing was measured")
	}
	got := list[0].Created

	// THE ZONE, not the instant. A time is correct in any zone - what a slicing
	// renderer reads is the string, and the string is what changes.
	if name, offset := got.Zone(); offset != 0 {
		t.Errorf("a message came back in %s (offset %ds), not UTC: %s",
			name, offset, got.Format(time.RFC3339Nano))
	}
	if s := got.Format(time.RFC3339); s[len(s)-1] != 'Z' {
		t.Errorf("a message time formats as %q, which a reader slicing [11:16] reads as local", s)
	}

	// AND IT IS STILL THE SAME INSTANT. A test that only asserted the zone
	// would pass just as well against a conversion that moved the clock rather
	// than relabelled it, which would be a worse bug than the one being fixed.
	if d := time.Since(got); d < 0 || d > time.Hour {
		t.Errorf("the message reads as %s, which is %v away from now", got.Format(time.RFC3339), d)
	}
}
