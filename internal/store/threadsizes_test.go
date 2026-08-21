package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestAThreadIsCountedFromTheLogAndOnlyItsMessages.
//
// The count exists because the console holds a window of a room and a thread is
// not bounded by it. So the two things worth asserting are the two ways the
// number could be a lie: it must count the WHOLE thread, and it must count only
// the messages - every event in a room carries a thread, so a reaction, a raise
// and a landing announcement would each inflate "8 replies" onto a thread with
// one reply in it.
//
// And a thread nobody has said anything in is ABSENT rather than zero, which is
// the distinction the console draws on: a missing key means the count was not
// taken, and "0 replies" is a claim this cannot make.
func TestAThreadIsCountedFromTheLogAndOnlyItsMessages(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "threadsize")
	p := &Principal{UserID: "u-reader", Project: project}

	room := "counted"
	thread := ulid.NewString()
	say := func(typ, body string) *Event {
		t.Helper()
		e := &Event{
			Type: typ, Project: &project, Room: room, Thread: thread,
			Actor: p.UserID, Body: body,
		}
		if err := db.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append %s: %v", typ, err)
		}
		return e
	}

	say(ChatEventType, "the root of the thread")
	say(ChatEventType, "one reply")
	say(ChatEventType, "another reply")
	// Not a message, and in the same thread: this is what a reaction on the
	// root looks like in the log, and counting it would tell a reader there are
	// four things to go and read when there are three.
	say(EventReactionAdd, "👍")

	quiet := ulid.NewString()
	if err := db.AppendEvent(ctx, &Event{
		Type: ChatEventType, Project: &project, Room: room, Thread: quiet,
		Actor: p.UserID, Body: "nobody answered this one",
	}); err != nil {
		t.Fatalf("append the lone message: %v", err)
	}

	empty := ulid.NewString()
	sizes, err := db.ThreadSizes(ctx, p, []string{thread, quiet, empty, "", thread}, ChatEventType, false)
	if err != nil {
		t.Fatalf("count the threads: %v", err)
	}
	if sizes[thread] != 3 {
		t.Errorf("the thread counted %d, want 3 - the reaction is not a message", sizes[thread])
	}
	if sizes[quiet] != 1 {
		t.Errorf("a thread of one message counted %d, want 1", sizes[quiet])
	}
	// ABSENT, NOT ZERO. The console tells these apart: a thread it was given no
	// number for draws nothing, and one that came back as 1 draws nothing
	// either - but for a different reason, and a store that invented a zero here
	// would make the two indistinguishable at the only place they differ.
	if _, ok := sizes[empty]; ok {
		t.Errorf("a thread with nothing in it came back as %d rather than absent", sizes[empty])
	}
	if _, ok := sizes[""]; ok {
		t.Error("the empty id was answered rather than dropped")
	}

	// A READER WHO CANNOT SEE THE ROOM IS NOT TOLD ITS SIZE. The filter is the
	// event filter every other read uses, and this asserts it is actually
	// applied here rather than that it exists: a count is a fact about messages,
	// and handing one to somebody who may not read them leaks the shape of a
	// conversation they were never shown.
	stranger := &Principal{UserID: "u-stranger", Project: declaredProject(t, ctx, db, "elsewhere")}
	theirs, err := db.ThreadSizes(ctx, stranger, []string{thread}, ChatEventType, false)
	if err != nil {
		t.Fatalf("count as a stranger: %v", err)
	}
	if n, ok := theirs[thread]; ok {
		t.Errorf("a reader outside the project was told the thread holds %d", n)
	}
}
