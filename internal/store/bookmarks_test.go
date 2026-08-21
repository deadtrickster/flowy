package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// What a reader's own log folds into. The order is the half worth asserting:
// this list is a pile somebody came back to, so the thing they kept a minute
// ago is the thing they are looking for - and it is the one rule where a
// bookmark deliberately parts company with a pin, whose strip holds its order
// so a reader can keep their place in it.
func TestTheNewestThingKeptIsFirst(t *testing.T) {
	kept := LiveBookmarks([]BookmarkEntry{
		{Message: "01HONE", Verb: EventBookmarkAdd},
		{Message: "01HTWO", Verb: EventBookmarkAdd},
		{Message: "01HTHREE", Verb: EventBookmarkAdd},
	})
	want := []string{"01HTHREE", "01HTWO", "01HONE"}
	if len(kept) != len(want) {
		t.Fatalf("got %v, want %v", kept, want)
	}
	for i := range want {
		if kept[i] != want[i] {
			t.Fatalf("got %v, want %v", kept, want)
		}
	}
}

// KEEPING SOMETHING AGAIN MOVES IT TO THE TOP, which is the rule the ordering
// above exists for and the one the first version of the fold broke: it recorded
// a position on first sight, so a re-keep left the message wherever it had been.
// Found in review rather than by this test, which is why the test is here now.
func TestKeepingSomethingAgainMovesItToTheTop(t *testing.T) {
	kept := LiveBookmarks([]BookmarkEntry{
		{Message: "01HOLD", Verb: EventBookmarkAdd},
		{Message: "01HNEWER", Verb: EventBookmarkAdd},
		{Message: "01HOLD", Verb: EventBookmarkRemove},
		{Message: "01HOLD", Verb: EventBookmarkAdd},
	})
	want := []string{"01HOLD", "01HNEWER"}
	if len(kept) != len(want) || kept[0] != want[0] || kept[1] != want[1] {
		t.Fatalf("got %v, want %v - re-keeping is a reader saying 'this one, again'", kept, want)
	}

	// And keeping one twice without dropping it is the same statement, so it
	// moves too rather than staying put.
	again := LiveBookmarks([]BookmarkEntry{
		{Message: "01HFIRST", Verb: EventBookmarkAdd},
		{Message: "01HSECOND", Verb: EventBookmarkAdd},
		{Message: "01HFIRST", Verb: EventBookmarkAdd},
	})
	if len(again) != 2 || again[0] != "01HFIRST" {
		t.Fatalf("got %v, want 01HFIRST first", again)
	}
}

// Dropping and keeping again works, keeping twice is harmless, and an entry
// with no message is not a bookmark of nothing.
func TestTheLastEntryAboutAMessageWins(t *testing.T) {
	kept := LiveBookmarks([]BookmarkEntry{
		{Message: "01HDROPPED", Verb: EventBookmarkAdd},
		{Message: "01HKEPT", Verb: EventBookmarkAdd},
		{Message: "01HKEPT", Verb: EventBookmarkAdd},
		{Message: "01HDROPPED", Verb: EventBookmarkRemove},
		{Message: "", Verb: EventBookmarkAdd},
		{Message: "01HBACK", Verb: EventBookmarkAdd},
		{Message: "01HBACK", Verb: EventBookmarkRemove},
		{Message: "01HBACK", Verb: EventBookmarkAdd},
	})
	want := map[string]bool{"01HKEPT": true, "01HBACK": true}
	if len(kept) != len(want) {
		t.Fatalf("got %v, want exactly %v", kept, want)
	}
	for _, id := range kept {
		if !want[id] {
			t.Fatalf("got %v, which holds %q", kept, id)
		}
	}
}

// THE FEATURE IS THE PRIVACY, and the assertion that carries it is the second
// principal's.
//
// "The reader can read their own bookmarks" passes under a completely broken
// implementation - the one where everybody can read everybody's - so it proves
// nothing alone. This asserts that bob, who is in the same project as alice and
// reads the very message she kept, does not see that she kept it: not in his
// list, and not through ReadEvent on the entry itself.
//
// There is no new clause in the filter behind this. A bookmark carries no
// project and no room, and perm.go already says what that means: "a projectless
// event is already readable by its author and nobody else". The test is here
// because that sentence is load-bearing for a feature written a year after it.
func TestOneReadersBookmarksAreInvisibleToAnother(t *testing.T) {
	ctx, db := open(t)

	home := declaredProject(t, ctx, db, "bm-project")
	room := declaredProject(t, ctx, db, "bm-room")

	alice := &User{Handle: "bm-alice-" + ulid.NewString()}
	bob := &User{Handle: "bm-bob-" + ulid.NewString()}
	for _, u := range []*User{alice, bob} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	at := home
	said := &Event{
		Type: ChatEventType, Project: &at, Room: room, Actor: bob.ID,
		Body: "the message alice keeps",
	}
	if err := db.AppendEvent(ctx, said); err != nil {
		t.Fatalf("append the message: %v", err)
	}

	aliceReads := &Principal{UserID: alice.ID, Project: home}
	bobReads := &Principal{UserID: bob.ID, Project: home}

	entry, err := db.Bookmark(ctx, aliceReads, said.ID)
	if err != nil {
		t.Fatalf("alice could not keep a message she can read: %v", err)
	}

	kept, err := db.BookmarksOf(ctx, aliceReads)
	if err != nil {
		t.Fatalf("alice's list: %v", err)
	}
	if len(kept) != 1 || kept[0] != said.ID {
		t.Fatalf("alice kept %v, want [%s]", kept, said.ID)
	}

	// THE ONE THAT MATTERS. Bob wrote the message, is in the same project, and
	// reads it - so this is a statement about the bookmark and not about bob
	// having been left out of everything.
	if got, err := db.ReadEvent(ctx, bobReads, said.ID); err != nil || got == nil {
		t.Fatalf("bob cannot read his own message (%v), so the assertion below says nothing", err)
	}
	his, err := db.BookmarksOf(ctx, bobReads)
	if err != nil {
		t.Fatalf("bob's list: %v", err)
	}
	if len(his) != 0 {
		t.Fatalf("bob's own list holds %v - alice's bookmark is in somebody else's list", his)
	}
	seen, err := db.ReadEvent(ctx, bobReads, entry.ID)
	if err == nil && seen != nil {
		t.Fatalf("bob read alice's bookmark entry %s directly. A bookmark is private because it "+
			"carries no project and no room; this one reached another reader.", entry.ID)
	}

	// And dropping it empties the list rather than leaving a stale id: the log
	// keeps both entries, the fold keeps neither.
	if _, err := db.Unbookmark(ctx, aliceReads, said.ID); err != nil {
		t.Fatalf("alice could not drop it: %v", err)
	}
	after, err := db.BookmarksOf(ctx, aliceReads)
	if err != nil {
		t.Fatalf("alice's list after dropping: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("after dropping, alice keeps %v", after)
	}
	log, err := db.BookmarkLog(ctx, aliceReads)
	if err != nil {
		t.Fatalf("alice's log: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("the log holds %d entries, want the keep and the drop - the log is the record", len(log))
	}

	// A message this reader cannot read cannot be kept, which is the only
	// refusal on the verb.
	stranger := &Principal{UserID: ulid.NewString(), Project: declaredProject(t, ctx, db, "bm-elsewhere")}
	if _, err := db.Bookmark(ctx, stranger, said.ID); err == nil {
		t.Fatal("a principal who cannot read the message was allowed to keep it")
	}
}
