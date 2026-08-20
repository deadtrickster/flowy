package store

import (
	"context"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// presenceOf finds one reader row by principal and name. "waiter" is a label,
// not an identity - a database that has been used before holds several, owned
// by users this principal is not.
func presenceOf(t *testing.T, ctx context.Context, db *DB, p *Principal, name string) *PresenceRow {
	t.Helper()
	rows, err := db.Presence(ctx)
	if err != nil {
		t.Fatalf("presence: %v", err)
	}
	for _, r := range rows {
		if r.Reader == name && r.Principal == readerKey(p) {
			return r
		}
	}
	t.Fatalf("presence does not list %s for this principal", name)
	return nil
}

// TestAWaiterSaysWhichProcessItIs is the repair the pid exists for: presence
// answers which process holds the reader, so a dead waiter is killed by number
// instead of by pattern.
//
// MEASURED, four times in one night across three seats: `pkill -9 -f 'flowy
// inbox --as NAME'` killed the shell that ran it, exit 144, because the pattern
// matched the process evaluating the pattern. Every one of those was done by
// somebody who had already written the lesson down.
func TestAWaiterSaysWhichProcessItIs(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "waiterproc")
	project := "waiterproc-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &Principal{UserID: u.ID, Project: project}
	if _, err := db.DeclareInboxReader(ctx, p, "waiter", ""); err != nil {
		t.Fatalf("declare reader: %v", err)
	}

	// A WAITER THAT HAS NOT SAID answers nothing, not a zero. The rosters of
	// every node that predates this column are full of these, and a pid of 0
	// rendered beside them would be a number an operator could act on.
	if got := presenceOf(t, ctx, db, p, "waiter"); got.Process.Complete() {
		t.Fatalf("a reader that has claimed no process answered %+v", got.Process)
	}

	started := time.Now().Add(-3 * time.Hour).UTC()
	claim := WaiterProcessOf("4321", started.Format(time.RFC3339Nano), "dead-XMG")
	if !claim.Complete() {
		t.Fatalf("the fixture claim is not complete: %+v", claim)
	}
	db.PollStartAs(ctx, p, "waiter", WaiterTracked, claim)

	got := presenceOf(t, ctx, db, p, "waiter")
	if !got.Process.Complete() {
		t.Fatalf("presence lost the process claim: %+v", got.Process)
	}
	if got.Process.Pid != 4321 || got.Process.Host != "dead-XMG" {
		t.Errorf("presence answered pid %d on %q, want 4321 on dead-XMG",
			got.Process.Pid, got.Process.Host)
	}
	// The start time is what tells this process from whatever reuses its
	// number, so it has to survive the round trip to the second at least.
	if got.Process.Since == nil || got.Process.Since.Sub(started).Abs() > time.Second {
		t.Errorf("presence answered start time %v, want %v", got.Process.Since, started)
	}
	// The claim rides along with the poll rather than replacing it.
	if !got.Attached {
		t.Error("a poll carrying a process claim did not read as attached")
	}
	if got.Kind != WaiterTracked {
		t.Errorf("the kind came back %q, want %q", got.Kind, WaiterTracked)
	}

	// AND A POLL THAT CLAIMS NOTHING CLEARS WHAT THE LAST ONE CLAIMED.
	//
	// This is the arm that matters most and it is the one a straightforward
	// implementation gets wrong: an UPDATE that only writes the columns when
	// they are given leaves the DEAD process's pid on the row, and the next
	// operator kills that number. A pid nobody is claiming any more names
	// whatever inherited it - which is the pkill failure with a database in
	// front of it. The reader is still live and still polling here; only the
	// claim is gone.
	db.PollStartAs(ctx, p, "waiter", WaiterTracked, WaiterProcess{})
	got = presenceOf(t, ctx, db, p, "waiter")
	if got.Process.Complete() || got.Process.Pid != 0 {
		t.Fatalf("an unclaimed poll left the last claim on the row: %+v", got.Process)
	}
	if !got.Attached {
		t.Error("a poll that claimed no process stopped counting as a poll")
	}

	// A PARTIAL CLAIM IS AN UNCLAIMED ONE, at the door as in the store: the
	// pid arrives on a query parameter, so a caller that sends pid and forgets
	// since must not be able to put half an identity on the row.
	db.PollStartAs(ctx, p, "waiter", WaiterTracked, WaiterProcess{Pid: 4321})
	if got := presenceOf(t, ctx, db, p, "waiter"); got.Process.Pid != 0 {
		t.Fatalf("a pid with no start time was stored: %+v", got.Process)
	}
}
