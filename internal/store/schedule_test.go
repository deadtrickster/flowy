package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The chain is the hierarchy, and its ORDER is the resolution rule. This needs
// no database, so it runs everywhere the parser does.
func TestScopesAreLeastSpecificFirst(t *testing.T) {
	got := Scopes("p1", "general")
	if len(got) != 3 {
		t.Fatalf("chain is %d long, want 3: %v", len(got), got)
	}
	if got[0].Kind != SchedFleet || got[1].Kind != SchedProject || got[2].Kind != SchedRoom {
		t.Fatalf("chain out of order: %v", got)
	}
	if n := len(Scopes("p1", "")); n != 2 {
		t.Errorf("a reader with no room has a %d-scope chain, want 2 - they are still in the fleet", n)
	}
	if n := len(Scopes("", "")); n != 1 {
		t.Errorf("a reader with no project has a %d-scope chain, want 1", n)
	}
	// A room named like a project id must not resolve as one.
	if RoomScope("p1", "general").ID == ProjectScope("p1general").ID {
		t.Error("a room scope id collides with a project scope id")
	}
}

func TestScopeValidation(t *testing.T) {
	for _, tc := range []struct {
		scope Scope
		wants string
	}{
		{Scope{Kind: SchedFleet, ID: "p1"}, "takes no id"},
		{Scope{Kind: SchedProject}, "needs a project"},
		{Scope{Kind: SchedProject, ID: "p1\x1fgeneral"}, "room scope written wrongly"},
		{Scope{Kind: SchedRoom, ID: "p1"}, "needs a project and a room"},
		{Scope{Kind: "team", ID: "x"}, "is not a scope"},
	} {
		err := validScope(tc.scope)
		if err == nil {
			t.Errorf("%v was accepted", tc.scope)
			continue
		}
		if !strings.Contains(err.Error(), tc.wants) {
			t.Errorf("%v refused with %q, want mention of %q", tc.scope, err, tc.wants)
		}
	}
	for _, ok := range []Scope{FleetScope(), ProjectScope("p1"), RoomScope("p1", "general")} {
		if err := validScope(ok); err != nil {
			t.Errorf("%v was refused: %v", ok, err)
		}
	}
}

// fleetRow writes a fleet-scope row and removes it when the test ends.
//
// FLEET SCOPE IS GLOBAL STATE - that is what it is for, and it is also why a
// test that leaves one behind changes what every later test resolves. The
// package's tests share one database; the first version of these left a fleet
// chat row standing and the "nothing is configured" test then inherited it and
// failed for a reason that had nothing to do with the code under test.
func fleetRow(t *testing.T, ctx context.Context, db *DB, s Schedule) {
	t.Helper()
	s.Scope = FleetScope()
	if _, err := db.PutSchedule(ctx, s, "operator"); err != nil {
		t.Fatalf("put fleet %s: %v", s.Signal, err)
	}
	t.Cleanup(func() {
		if _, err := db.DeleteSchedule(context.WithoutCancel(ctx), FleetScope(), s.Signal); err != nil {
			t.Errorf("fleet %s row left behind: %v", s.Signal, err)
		}
	})
}

// THE ARM THIS TABLE'S PRIMARY KEY EXISTS FOR: a room that turns a signal OFF
// must beat a fleet that turned it on. Absent means inherit; present-and-off
// means off. If those two ever share a code path a room can never go quiet.
func TestAnOffRowBeatsAnInheritedOnRow(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "sched")

	fleetRow(t, ctx, db, Schedule{Signal: "chat", Realtime: true})

	got, err := db.ResolveSchedule(ctx, project, "general", "chat")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Realtime || got.FromKind != SchedFleet {
		t.Fatalf("inherited answer is %+v, want realtime from fleet", got)
	}
	if got.Defaulted {
		t.Error("a fleet row was written and the answer still says nothing is configured")
	}

	// The room says no.
	if _, err := db.PutSchedule(ctx, Schedule{Scope: RoomScope(project, "general"), Signal: "chat"}, "operator"); err != nil {
		t.Fatalf("put room: %v", err)
	}
	got, err = db.ResolveSchedule(ctx, project, "general", "chat")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Never() {
		t.Fatalf("the room turned chat off and got %+v", got)
	}
	if got.FromKind != SchedRoom {
		t.Errorf("the off answer came from %s, want the room that wrote it", got.FromKind)
	}

	// Another room in the same project still inherits, so the override is
	// the room's own and not the project's.
	other, err := db.ResolveSchedule(ctx, project, "hallway", "chat")
	if err != nil {
		t.Fatalf("resolve other room: %v", err)
	}
	if !other.Realtime || other.FromKind != SchedFleet {
		t.Errorf("a second room saw the first room's override: %+v", other)
	}

	// Deleting the room row is the ONLY way back to inheriting.
	removed, err := db.DeleteSchedule(ctx, RoomScope(project, "general"), "chat")
	if err != nil || !removed {
		t.Fatalf("delete: removed=%v err=%v", removed, err)
	}
	got, err = db.ResolveSchedule(ctx, project, "general", "chat")
	if err != nil {
		t.Fatalf("resolve after delete: %v", err)
	}
	if !got.Realtime || got.FromKind != SchedFleet {
		t.Errorf("after deleting its own row the room did not inherit again: %+v", got)
	}
}

// Resolution is WHOLE-ROW. A project row with no cron does not leave the
// fleet's cron standing - that would be a resolution nobody could predict from
// reading the table.
func TestResolutionIsWholeRow(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "sched")

	fleetRow(t, ctx, db, Schedule{Signal: "board", Realtime: true, Cron: "0 9 * * *"})
	if _, err := db.PutSchedule(ctx, Schedule{Scope: ProjectScope(project), Signal: "board", Cron: "*/30 * * * *"}, "operator"); err != nil {
		t.Fatalf("put project: %v", err)
	}

	got, err := db.ResolveSchedule(ctx, project, "general", "board")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Cron != "*/30 * * * *" {
		t.Errorf("cron is %q, want the project's", got.Cron)
	}
	if got.Realtime {
		t.Error("realtime survived from the fleet row the project replaced")
	}
}

// Nothing configured is its own answer, distinct from a deliberate off.
func TestNothingConfiguredSaysSo(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "sched")

	// A fresh project inherits nothing only if the FLEET is also clear -
	// fleet scope is global, so this asserts the precondition rather than
	// assuming it. Without this the test reads another test's leftovers.
	if rows, err := db.ListSchedules(ctx, FleetScope()); err != nil {
		t.Fatalf("list fleet: %v", err)
	} else if len(rows) != 0 {
		t.Fatalf("the fleet carries %d row(s), so nothing here is unconfigured: %+v", len(rows), rows)
	}

	got, err := db.ResolveSchedule(ctx, project, "general", "chat")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Defaulted {
		t.Fatalf("an unconfigured signal did not say so: %+v", got)
	}
	if !got.Realtime {
		t.Error("chat's built-in default is not realtime, and a message is only useful now")
	}
	if got.Never() {
		t.Error("an unconfigured signal reads as never, which is a deliberate state somebody chose")
	}

	// A written row that happens to match the default is NOT defaulted.
	if _, err := db.PutSchedule(ctx, Schedule{Scope: ProjectScope(project), Signal: "chat", Realtime: true}, "operator"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err = db.ResolveSchedule(ctx, project, "general", "chat")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Defaulted {
		t.Error("a row somebody wrote is being reported as an untouched default")
	}
}

// The save door is where a dead cron stops. Nothing is stored, so the console
// cannot show a saved schedule that will never fire.
func TestASaveWithADeadCronStoresNothing(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "sched")

	_, err := db.PutSchedule(ctx, Schedule{Scope: ProjectScope(project), Signal: "board", Cron: "0 0 30 2 *"}, "operator")
	if err == nil {
		t.Fatal("February 30th was saved as a schedule")
	}
	if !strings.Contains(err.Error(), "February") {
		t.Errorf("the refusal does not carry the parser's reason: %v", err)
	}

	rows, err := db.ListSchedules(ctx, ProjectScope(project))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("the refused save left %d row(s) behind", len(rows))
	}

	// The negative control: a real crontab line saves.
	if _, err := db.PutSchedule(ctx, Schedule{Scope: ProjectScope(project), Signal: "board", Cron: "0 9 * * 1-5"}, "operator"); err != nil {
		t.Fatalf("a weekday 09:00 schedule was refused: %v", err)
	}
	rows, err = db.ListSchedules(ctx, ProjectScope(project))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].Cron != "0 9 * * 1-5" {
		t.Fatalf("stored rows are %+v", rows)
	}
	if rows[0].UpdatedBy != "operator" {
		t.Errorf("the row does not say who wrote it: %+v", rows[0])
	}
}

func TestUnknownSignalIsRefusedOnBothDoors(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "sched")

	_, err := db.PutSchedule(ctx, Schedule{Scope: ProjectScope(project), Signal: "chatt", Realtime: true}, "operator")
	if !errors.Is(err, ErrUnknownSignal) {
		t.Errorf("put accepted an unknown signal: %v", err)
	}
	if _, err := db.ResolveSchedule(ctx, project, "general", "chatt"); !errors.Is(err, ErrUnknownSignal) {
		t.Errorf("resolve accepted an unknown signal: %v", err)
	}
	// The refusal lists what IS a signal, because a typo needs the right
	// spelling and not just a no.
	if err != nil && !strings.Contains(err.Error(), "chat") {
		t.Errorf("the refusal does not list the signals: %v", err)
	}
}

// A resolved schedule with a clock can say when it next fires; one without a
// clock answers false rather than failing, because no clock is a real state.
func TestNextFiring(t *testing.T) {
	r := Resolved{Signal: "board", Cron: "0 9 * * *"}
	at := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	next, ok, err := r.NextFiring(at)
	if err != nil || !ok {
		t.Fatalf("NextFiring: ok=%v err=%v", ok, err)
	}
	if want := time.Date(2026, time.August, 22, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next firing %s, want %s", next, want)
	}

	if _, ok, err := (Resolved{Signal: "chat", Realtime: true}).NextFiring(at); err != nil || ok {
		t.Errorf("a realtime-only schedule reported a clock firing: ok=%v err=%v", ok, err)
	}
}
