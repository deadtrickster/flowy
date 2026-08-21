package store

import (
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// reader mints a reader name unique to one test, so two tests never share a
// stamp - the same lesson the fleet-scope rows taught an hour earlier.
func reader(t *testing.T) string {
	t.Helper()
	return "r-" + ulid.NewString()
}

// A READER THAT WAS AWAY FOR A DAY GETS ONE FIRING, NOT A DAY'S WORTH. This is
// the arm the whole file exists for: an hourly signal missed twenty-six times
// comes back as one.
func TestAReaderThatMissedTwentySixFiringsGetsOne(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fired")
	who := reader(t)

	if _, err := db.PutSchedule(ctx, Schedule{
		Scope: ProjectScope(project), Signal: "board", Cron: "@hourly",
	}, "operator"); err != nil {
		t.Fatalf("put: %v", err)
	}

	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	if err := db.MarkFired(ctx, who, "board", now.Add(-26*time.Hour)); err != nil {
		t.Fatalf("mark: %v", err)
	}

	due, err := db.Due(ctx, who, project, "", now)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("twenty-six missed hours produced %d firings, want 1: %+v", len(due), due)
	}
	if due[0].Signal != "board" || due[0].First {
		t.Errorf("the firing is %+v", due[0])
	}

	// AND THE STAMP MOVES TO NOW, NOT TO THE BOUNDARY. Marking at the
	// boundary would leave the same firing due again immediately.
	if err := db.MarkFired(ctx, who, "board", now); err != nil {
		t.Fatalf("mark: %v", err)
	}
	due, err = db.Due(ctx, who, project, "", now)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("the signal was still due immediately after being marked: %+v", due)
	}

	// An hour later it is due once more, because the clock carries on.
	due, err = db.Due(ctx, who, project, "", now.Add(61*time.Minute))
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("the next hour produced %d firings, want 1", len(due))
	}
}

// A reader with no stamp is due immediately, and says which it is. The flag is
// the point: "you have missed one" and "you have never had one" are different
// things to tell a seat.
func TestANewReaderIsDueAndSaysSo(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fired")
	who := reader(t)

	if _, err := db.PutSchedule(ctx, Schedule{
		Scope: ProjectScope(project), Signal: "board", Cron: "0 9 * * *",
	}, "operator"); err != nil {
		t.Fatalf("put: %v", err)
	}

	due, err := db.Due(ctx, who, project, "", time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 || !due[0].First {
		t.Fatalf("a reader that has never fired got %+v", due)
	}
	if due[0].Cron != "0 9 * * *" || due[0].FromKind != SchedProject {
		t.Errorf("the firing does not carry what it resolved to: %+v", due[0])
	}
}

// A SIGNAL WITH NO CLOCK IS NEVER DUE HERE. Realtime is delivered by the fact
// changing, which is a different mechanism - and a reader that received chat
// through this door would get it twice.
func TestARealtimeOnlySignalIsNeverDueOnTheClock(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fired")
	who := reader(t)

	if _, err := db.PutSchedule(ctx, Schedule{
		Scope: ProjectScope(project), Signal: "chat", Realtime: true,
	}, "operator"); err != nil {
		t.Fatalf("put: %v", err)
	}
	// And board explicitly off, so this project has no clock at all.
	if _, err := db.PutSchedule(ctx, Schedule{
		Scope: ProjectScope(project), Signal: "board",
	}, "operator"); err != nil {
		t.Fatalf("put: %v", err)
	}

	due, err := db.Due(ctx, who, project, "", time.Now())
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("a project with no clock produced %d firings: %+v", len(due), due)
	}
}

// A room that turns a signal off stops its clock, and the reader in that room
// stops being due - the resolution rule reaching all the way through to
// delivery, which is the only place it matters.
func TestARoomThatTurnsASignalOffStopsItsClock(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fired")
	who := reader(t)

	if _, err := db.PutSchedule(ctx, Schedule{
		Scope: ProjectScope(project), Signal: "board", Cron: "@hourly",
	}, "operator"); err != nil {
		t.Fatalf("put project: %v", err)
	}
	if due, err := db.Due(ctx, who, project, "general", time.Now()); err != nil || len(due) != 1 {
		t.Fatalf("before the room said no: %d firings, err %v", len(due), err)
	}

	if _, err := db.PutSchedule(ctx, Schedule{
		Scope: RoomScope(project, "general"), Signal: "board",
	}, "operator"); err != nil {
		t.Fatalf("put room: %v", err)
	}
	due, err := db.Due(ctx, who, project, "general", time.Now())
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("the room turned board off and the clock still fired: %+v", due)
	}
	// The same reader elsewhere in the project is unaffected, so the off is
	// the room's and not the reader's.
	if elsewhere, err := db.Due(ctx, who, project, "hallway", time.Now()); err != nil || len(elsewhere) != 1 {
		t.Errorf("the room's off followed the reader out of the room: %d firings, err %v", len(elsewhere), err)
	}
}

// Two readers do not share a stamp, which is what makes it per reader.
func TestStampsAreOnePerReader(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fired")
	a, b := reader(t), reader(t)

	if _, err := db.PutSchedule(ctx, Schedule{
		Scope: ProjectScope(project), Signal: "board", Cron: "@hourly",
	}, "operator"); err != nil {
		t.Fatalf("put: %v", err)
	}

	now := time.Now()
	if err := db.MarkFired(ctx, a, "board", now); err != nil {
		t.Fatalf("mark: %v", err)
	}

	if due, err := db.Due(ctx, a, project, "", now); err != nil || len(due) != 0 {
		t.Fatalf("the reader that was just marked is still due: %d, err %v", len(due), err)
	}
	if due, err := db.Due(ctx, b, project, "", now); err != nil || len(due) != 1 {
		t.Fatalf("a second reader inherited the first one's stamp: %d, err %v", len(due), err)
	}

	at, seen, err := db.LastFired(ctx, a, "board")
	if err != nil || !seen {
		t.Fatalf("LastFired: seen=%v err=%v", seen, err)
	}
	if at.Sub(now).Abs() > time.Second {
		t.Errorf("stamp is %s, want about %s", at, now)
	}
	if _, seen, err := db.LastFired(ctx, b, "board"); err != nil || seen {
		t.Errorf("a reader that never fired reports a stamp: seen=%v err=%v", seen, err)
	}
}

// A reader whose next firing is still ahead is not due. Without this arm every
// test above passes on a Due that returns everything.
func TestASignalNotYetRoundIsNotDue(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fired")
	who := reader(t)

	if _, err := db.PutSchedule(ctx, Schedule{
		Scope: ProjectScope(project), Signal: "board", Cron: "0 9 * * *",
	}, "operator"); err != nil {
		t.Fatalf("put: %v", err)
	}

	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	// Fired at 09:00 today; the next is 09:00 tomorrow.
	if err := db.MarkFired(ctx, who, "board", now.Add(-3*time.Hour)); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if due, err := db.Due(ctx, who, project, "", now); err != nil || len(due) != 0 {
		t.Fatalf("a signal three hours after its firing is due again: %d, err %v", len(due), err)
	}
	// And it IS due tomorrow, so the arm above is not passing because Due
	// never returns anything.
	if due, err := db.Due(ctx, who, project, "", now.Add(22*time.Hour)); err != nil || len(due) != 1 {
		t.Fatalf("tomorrow's firing did not arrive: %d, err %v", len(due), err)
	}
}
