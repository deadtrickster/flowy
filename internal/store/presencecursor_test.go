package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// PRESENCE SAYS HOW FAR EACH READER HAS BEEN HANDED.
//
// The operator, 2026-08-20, after asking why two seats had not answered them:
// "read statuses would be great". The data was already there and had been since
// inbox_readers existed - every row is a cursor, written by every poll, on the
// same sequence every event carries. The presence view was the one thing not
// passing it on, so no reader anywhere could answer "has that seat seen this".
//
// A POSITION, NOT A COUNT. Zero is a real answer - handed nothing yet - and a
// reader that does not exist is absent from the list entirely, which is the
// different answer and the one the roster already gives by omission. That
// distinction is the reason this is an int on the sequence rather than an
// "unread" number, which would have collapsed the two.
func TestPresenceSaysHowFarAReaderGot(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "cursor")
	project := "cursor-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &Principal{UserID: u.ID, Project: project}
	if _, err := db.DeclareInboxReader(ctx, p, "cursorwaiter", ""); err != nil {
		t.Fatalf("declare reader: %v", err)
	}

	mine := func(t *testing.T) *PresenceRow {
		t.Helper()
		rows, err := db.Presence(ctx)
		if err != nil {
			t.Fatalf("presence: %v", err)
		}
		for _, r := range rows {
			// On the principal as well as the name: "cursorwaiter" is a label
			// and every run of this test declares one. Two seats have already
			// measured somebody else's leftovers by matching on the name alone.
			if r.Reader == "cursorwaiter" && r.UserName == u.Handle {
				return r
			}
		}
		t.Fatal("presence does not list the reader this test declared")
		return nil
	}

	// A reader that has been handed nothing sits at zero, and IS ON THE LIST.
	// Zero and absent are the two answers this row exists to keep apart.
	if got := mine(t).Cursor; got != 0 {
		t.Errorf("a reader handed nothing reads cursor %d, want 0", got)
	}

	// Acking moves it, and presence says where it got to.
	if _, err := db.AckInbox(ctx, p, "cursorwaiter", 4242, true); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if got := mine(t).Cursor; got != 4242 {
		t.Errorf("presence says cursor %d after an ack to 4242", got)
	}

	// AND IT IS THE READER'S OWN, not the seat's. Two readers under one
	// principal are two positions - which is the property the console depends
	// on: it keeps one reader per room, so a single shared cursor would clear
	// every room's badge when any one of them was read.
	if _, err := db.DeclareInboxReader(ctx, p, "cursorwaiter-two", ""); err != nil {
		t.Fatalf("declare second reader: %v", err)
	}
	rows, err := db.Presence(ctx)
	if err != nil {
		t.Fatalf("presence: %v", err)
	}
	for _, r := range rows {
		if r.Reader == "cursorwaiter-two" && r.UserName == u.Handle && r.Cursor != 0 {
			t.Errorf("a second reader inherited the first one's cursor: %d", r.Cursor)
		}
	}
}
