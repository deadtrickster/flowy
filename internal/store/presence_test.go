package store

import (
	"context"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// Presence: the poll is the signal, the reader row is deletable, and the
// rosters come back named. These need the database the gate stands up; without
// DATABASE_URL they sit out - see open().

// presenceUser mints a person to own the rows, so two runs against the same
// database do not collide.
func presenceUser(t *testing.T, ctx context.Context, db *DB, handle string) *User {
	t.Helper()
	u := &User{Handle: handle + "-" + ulid.NewString(), Display: handle}
	if err := db.InsertUser(ctx, u); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return u
}

// TestPresenceTracksPollsNotAcks is the whole point of the columns: a reader
// that has never been handed anything must still read as attached while its
// poll is in flight, and detached the moment it leaves - a quiet room is not a
// dead listener.
func TestPresenceTracksPollsNotAcks(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "presence")
	project := "presence-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &Principal{UserID: u.ID, Project: project}

	if _, err := db.DeclareInboxReader(ctx, p, "waiter"); err != nil {
		t.Fatalf("declare reader: %v", err)
	}

	// Before any poll: present, not attached.
	rows, err := db.Presence(ctx)
	if err != nil {
		t.Fatalf("presence: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Reader == "waiter" && r.UserName == u.Handle {
			found = true
			if r.Attached {
				t.Error("reader reads attached before any poll")
			}
		}
	}
	if !found {
		t.Fatal("presence does not list the declared reader with its user's handle")
	}

	// In flight: attached, whatever the room has said.
	db.PollStart(ctx, p, "waiter", WaiterTracked)
	rows, err = db.Presence(ctx)
	if err != nil {
		t.Fatalf("presence: %v", err)
	}
	for _, r := range rows {
		if r.Reader == "waiter" {
			if !r.Attached {
				t.Error("poll in flight reads as not attached")
			}
			if r.LastPoll == nil {
				t.Error("poll in flight left last_poll_at unset")
			}
		}
	}

	// Left: detached again.
	db.PollEnd(ctx, p, "waiter")
	rows, err = db.Presence(ctx)
	if err != nil {
		t.Fatalf("presence: %v", err)
	}
	for _, r := range rows {
		if r.Reader == "waiter" && r.Attached {
			t.Error("poll ended and the reader still reads attached")
		}
	}
}

// TestPresenceCarriesTheWaiterKind is the half attachment cannot answer: what
// the thing that polled can DO about what it hears.
//
// A forked successor polls exactly like a tracked waiter - attached, fresh,
// indistinguishable in every column this table had - and wakes nobody, because
// only a harness-tracked waiter exiting produces a notification. That is not a
// hypothetical either: an agent sat deaf for 28 minutes with the room, the
// roster and the nag hook all reporting healthy.
func TestPresenceCarriesTheWaiterKind(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "kinds")
	project := "presence-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &Principal{UserID: u.ID, Project: project}

	// kindOf is what the roster would draw for one of these readers, or "" for
	// a reader the roster does not list at all - which is a different failure
	// from the wrong kind and has to read as one.
	kindOf := func(reader string) string {
		t.Helper()
		rows, err := db.Presence(ctx)
		if err != nil {
			t.Fatalf("presence: %v", err)
		}
		for _, r := range rows {
			if r.Reader == reader && r.Principal == readerKey(p) {
				return r.Kind
			}
		}
		return ""
	}

	for _, reader := range []string{"tracked-one", "forked-one", "quiet-one", "never-polled"} {
		if _, err := db.DeclareInboxReader(ctx, p, reader); err != nil {
			t.Fatalf("declare reader %s: %v", reader, err)
		}
	}

	// A row nothing has claimed. Not tracked: absence is not evidence, and the
	// optimistic reading of absence is the whole bug.
	if got := kindOf("never-polled"); got != WaiterUnknown {
		t.Errorf("a reader that never polled reads as %q, want %q", got, WaiterUnknown)
	}

	// Each kind is reported as itself, and a poll that says nothing is unknown
	// rather than either of the two claims.
	db.PollStart(ctx, p, "tracked-one", WaiterTracked)
	db.PollStart(ctx, p, "forked-one", WaiterForked)
	db.PollStart(ctx, p, "quiet-one", "")
	for reader, want := range map[string]string{
		"tracked-one": WaiterTracked,
		"forked-one":  WaiterForked,
		"quiet-one":   WaiterUnknown,
	} {
		if got := kindOf(reader); got != want {
			t.Errorf("%s polling as %q reads as %q", reader, want, got)
		}
	}

	// It outlives the poll, and the next poll does not reset it. A kind that
	// only held while a request was in flight would be blank in exactly the
	// gap somebody is looking at the roster, and blank reads as unknown - so
	// the forked listener would go back to looking like nothing in particular.
	db.PollEnd(ctx, p, "forked-one")
	if got := kindOf("forked-one"); got != WaiterForked {
		t.Errorf("the poll ended and the kind became %q, want %q", got, WaiterForked)
	}
	db.PollStart(ctx, p, "forked-one", WaiterForked)
	db.PollEnd(ctx, p, "forked-one")
	if got := kindOf("forked-one"); got != WaiterForked {
		t.Errorf("a second poll cycle left the kind %q, want %q", got, WaiterForked)
	}
	// And the other rows were not swept along with it: this is per reader, not
	// a property of the node.
	if got := kindOf("tracked-one"); got != WaiterTracked {
		t.Errorf("polling one reader changed another's kind to %q", got)
	}

	// A client that claims something nobody can draw claims nothing. The value
	// arrives on a query parameter, so this is the only thing standing between
	// the roster and a state it has no case for.
	db.PollStart(ctx, p, "tracked-one", "wide-awake-honest")
	if got := kindOf("tracked-one"); got != WaiterUnknown {
		t.Errorf("an invented kind was recorded as %q, want %q", got, WaiterUnknown)
	}
}

// TestPresenceRetiresAReaderThatStoppedMidPoll is the six-hour ghost.
//
// polls_in_flight only comes down when a handler returns, so a waiter killed
// mid-poll - or a decrement issued on a request context that was already
// cancelled - leaves the counter up with nobody behind it. The roster read
// treated any positive counter as attached with no age test at all, so the row
// said "attached, polling" for as long as the table lived. claude-glm sat there
// for six hours and ho-test for thirty while the operator asked twice why an
// agent was not answering.
//
// What is asserted here is not just that it stops saying attached. It is that
// the row STAYS ON THE ROSTER, named as lost, carrying the timestamp that says
// how long ago it went - because the fact somebody needs is that this seat is
// deaf, and a fix that dropped the row would have deleted exactly that.
func TestPresenceRetiresAReaderThatStoppedMidPoll(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "stalled")
	project := "presence-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &Principal{UserID: u.ID, Project: project}

	// rowFor is what the roster would draw for this reader, or nil for a reader
	// it does not list. Absent and present-but-wrong are different failures.
	rowFor := func(reader string) *PresenceRow {
		t.Helper()
		rows, err := db.Presence(ctx)
		if err != nil {
			t.Fatalf("presence: %v", err)
		}
		for _, r := range rows {
			if r.Reader == reader && r.Principal == readerKey(p) {
				return r
			}
		}
		return nil
	}

	// stall ages a reader's last poll by hand and leaves the counter up: the
	// state a killed waiter leaves behind, which is not reachable through
	// PollStart and PollEnd because those two are the pair that never ran.
	stall := func(reader, age string) {
		t.Helper()
		if _, err := db.sql.ExecContext(ctx,
			`UPDATE inbox_readers
			    SET last_poll_at = now() - $3::interval, polls_in_flight = 1
			  WHERE principal = $1 AND reader = $2`,
			readerKey(p), reader, age); err != nil {
			t.Fatalf("stall %s: %v", reader, err)
		}
	}

	for _, reader := range []string{"stopped-six-hours-ago", "stopped-yesterday"} {
		if _, err := db.DeclareInboxReader(ctx, p, reader); err != nil {
			t.Fatalf("declare reader %s: %v", reader, err)
		}
	}

	// A poll in flight right now is the healthy case, and it has to keep
	// reading that way - a window that retires live waiters is worse than the
	// bug it replaces.
	db.PollStart(ctx, p, "stopped-six-hours-ago", WaiterTracked)
	if row := rowFor("stopped-six-hours-ago"); row == nil || !row.Attached ||
		row.State != PresenceListening {
		t.Fatalf("a poll in flight now reads as %+v, want attached and listening", row)
	}

	// Six hours later, with the poll still nominally in flight. The server
	// blocks for twenty-five seconds at a time, so this is not a slow poll.
	stall("stopped-six-hours-ago", "6 hours")
	row := rowFor("stopped-six-hours-ago")
	if row == nil {
		t.Fatal("a reader that stopped mid-poll six hours ago vanished from the roster - " +
			"that is the evidence the operator was asking for, not clutter")
	}
	if row.Attached {
		t.Error("a poll that started six hours ago still reads as in flight")
	}
	if row.State != PresenceLost {
		t.Errorf("a reader that stopped six hours ago reads as %q, want %q", row.State, PresenceLost)
	}
	// The row has to say WHEN, or "lost" is a word with no way to act on it.
	if row.LastPoll == nil {
		t.Error("a lost reader carries no last poll, so nothing can say how long it has been deaf")
	}

	// Past the waiter's own deadline it is no longer news. A waiter that armed
	// a day ago would have gone home on its own budget, so an unfinished poll
	// older than that is a row nobody cleaned up rather than a seat that just
	// went deaf.
	stall("stopped-yesterday", "30 hours")
	if row := rowFor("stopped-yesterday"); row != nil {
		t.Errorf("a poll abandoned 30 hours ago is still on the roster as %+v - "+
			"past the waiter's own deadline it is noise, not an incident", row)
	}

	// And it comes back the moment it polls again: this is a reading of the
	// row, not a retirement written into it.
	db.PollStart(ctx, p, "stopped-yesterday", WaiterTracked)
	if row := rowFor("stopped-yesterday"); row == nil || row.State != PresenceListening {
		t.Errorf("a reader that polled again reads as %+v, want listening - "+
			"the window is a reading and must not have marked the row dead", row)
	}
}

// TestPresenceStartingIsJudgedByTheRowsAge is the other half of "still grows".
//
// The console declares a reader per room to hold a human's unread place. Those
// never poll, and they ack every time somebody reads the room - which moved
// `updated`, which was what the never-polled clause windowed on. So three
// browser bookmarks were permanent residents of the listening pane, kept fresh
// by the act of looking at the page they were cluttering.
//
// A never-polled row is a waiter STARTING, and how long ago it was declared is
// what says whether that is still a plausible reading.
func TestPresenceStartingIsJudgedByTheRowsAge(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "bookmark")
	project := "presence-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &Principal{UserID: u.ID, Project: project}

	listed := func(reader string) *PresenceRow {
		t.Helper()
		rows, err := db.Presence(ctx)
		if err != nil {
			t.Fatalf("presence: %v", err)
		}
		for _, r := range rows {
			if r.Reader == reader && r.Principal == readerKey(p) {
				return r
			}
		}
		return nil
	}

	const bookmark = "console:general"
	if _, err := db.DeclareInboxReader(ctx, p, bookmark); err != nil {
		t.Fatalf("declare reader: %v", err)
	}
	// Freshly declared and not yet polling: a waiter arming itself looks exactly
	// like this, and the roster is where somebody watches it start.
	if row := listed(bookmark); row == nil || row.State != PresenceStarting {
		t.Fatalf("a reader declared a moment ago reads as %+v, want %q", row, PresenceStarting)
	}

	// Declared hours ago and still never polled: whatever it is, it is not
	// starting.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE inbox_readers SET created = now() - interval '3 hours'
		  WHERE principal = $1 AND reader = $2`, readerKey(p), bookmark); err != nil {
		t.Fatalf("age the bookmark: %v", err)
	}
	if row := listed(bookmark); row != nil {
		t.Errorf("a bookmark declared three hours ago that never polled is on the roster as %+v", row)
	}

	// And reading the room does not put it back. This is the exact loop that
	// made the pane grow: the tab acks, the ack stamps the row, and the row
	// re-enters the roster it was just cleared from.
	if _, err := db.AckInbox(ctx, p, bookmark, 1, false); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if row := listed(bookmark); row != nil {
		t.Errorf("acking an unread bookmark put it back on the listening roster as %+v - "+
			"an ack is a place in the log, not an ear on the room", row)
	}
}

// TestDeleteInboxReader is the ghost fix: a test label must be droppable, and
// only by the principal that owns it.
func TestDeleteInboxReader(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "gone")
	other := presenceUser(t, ctx, db, "keeper")
	project := "presence-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &Principal{UserID: u.ID, Project: project}
	po := &Principal{UserID: other.ID, Project: project}

	if _, err := db.DeclareInboxReader(ctx, p, "test-label"); err != nil {
		t.Fatalf("declare reader: %v", err)
	}

	// Somebody else's label is not theirs to delete.
	if gone, err := db.DeleteInboxReader(ctx, po, "test-label"); err != nil || gone {
		t.Fatalf("another principal deleted a reader they do not own: gone=%v err=%v", gone, err)
	}

	if gone, err := db.DeleteInboxReader(ctx, p, "test-label"); err != nil || !gone {
		t.Fatalf("owner could not delete their own reader: gone=%v err=%v", gone, err)
	}
	if _, err := db.InboxReaderAt(ctx, p, "test-label"); err != ErrNoReader {
		t.Fatalf("deleted reader still answers: %v", err)
	}
	if gone, err := db.DeleteInboxReader(ctx, p, "test-label"); err != nil || gone {
		t.Fatalf("deleting a deleted reader says gone=%v err=%v", gone, err)
	}
}

// TestRoomMembersNamesSpeakers is the in-the-room roster: distinct actors of
// chat events the principal may read, named when the registry knows them.
func TestRoomMembersNamesSpeakers(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "speaker")
	project := "presence-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &Principal{UserID: u.ID, Project: project}

	for _, body := range []string{"first", "second"} {
		e := &Event{
			Type: "chat", Project: &project, Room: "general", Actor: u.ID,
			Parents: []string{}, Body: body,
		}
		if err := db.AppendEvent(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	members, err := db.RoomMembers(ctx, p)
	if err != nil {
		t.Fatalf("room members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("two messages from one actor gave %d members, want 1", len(members))
	}
	if members[0].Name != u.Handle {
		t.Errorf("member is named %q, want the user's handle %q", members[0].Name, u.Handle)
	}
	if members[0].Kind != "user" {
		t.Errorf("member kind is %q, want user", members[0].Kind)
	}
}
