package main

import (
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// row builds a presence row with a poll that age old.
func row(name, kind string, attached bool, age time.Duration, now time.Time) *store.PresenceRow {
	at := now.Add(-age)
	return &store.PresenceRow{Reader: name, Kind: kind, Attached: attached, LastPoll: &at, State: "listening"}
}

// THE PROCEDURE AN OPERATOR WAS EXECUTING BY HAND, every fifteen minutes for a
// day. Each clause is checked for its VERDICT and for SAYING WHICH FACT
// DECIDED, because a verb that answers healthy/broken and not why is the pane
// that read "polling 4s ago" for twenty-eight minutes while the seat was deaf.
func TestAWaiterCheckNamesTheClauseThatDecided(t *testing.T) {
	now := time.Now()
	const stale = 10 * time.Minute

	for _, c := range []struct {
		name  string
		rows  []*store.PresenceRow
		ok    bool
		says  string
		notes string
	}{
		{
			name:  "polling and fresh",
			rows:  []*store.PresenceRow{row("me", store.WaiterTracked, true, 12*time.Second, now)},
			ok:    true,
			says:  "polled 12s ago",
			notes: "a healthy line has to carry the facts it was decided on, or nobody can check it",
		},
		{
			// UNKNOWN IS NOT DEAF. Measured 2026-08-17: two monitor-run seats
			// read unknown while delivering in real time, and treating that as
			// broken restarted a healthy listener.
			name:  "kind unknown, polling",
			rows:  []*store.PresenceRow{row("me", store.WaiterUnknown, true, 3*time.Second, now)},
			ok:    true,
			says:  "kind unknown",
			notes: "unknown means the node cannot classify the reader, not that the reader is deaf",
		},
		{
			name:  "forked",
			rows:  []*store.PresenceRow{row("me", store.WaiterForked, true, time.Second, now)},
			ok:    false,
			says:  "kind forked",
			notes: "the one kind that IS deafness: it polls, it is fresh, and it wakes nobody",
		},
		{
			// BETWEEN POLLS IS NOT DEAD, and the gate caught this: a waiter
			// loop polls, returns and polls again - claude-host's sleeps three
			// seconds - so a healthy seat has no poll in flight for part of
			// every cycle. The first cut called that broken, which would have
			// restarted a seat for breathing.
			name:  "fresh, between polls",
			rows:  []*store.PresenceRow{row("me", store.WaiterTracked, false, time.Minute, now)},
			ok:    true,
			says:  "between polls",
			notes: "freshness decides; the in-flight fact is printed, not judged",
		},
		{
			name: "stale and still holding a poll",
			rows: []*store.PresenceRow{row("me", store.WaiterTracked, true, 6*time.Hour, now)},
			ok:   false,
			says: "older than 10m0s",
			notes: "the lost seat: a poll counter left up by a decrement that never ran, " +
				"which read as attached and polling for six hours",
		},
		{
			name: "stale",
			rows: []*store.PresenceRow{row("me", store.WaiterTracked, true, 42*time.Minute, now)},
			ok:   false,
			says: "older than 10m0s",
		},
		{
			name:  "no row at all",
			rows:  []*store.PresenceRow{row("somebody-else", store.WaiterTracked, true, time.Second, now)},
			ok:    false,
			says:  "no reader row",
			notes: "never armed and armed-then-died are different repairs, so they are different sentences",
		},
		{
			// Declared and never polled. Something armed a reader here and
			// nothing has ever called the inbox under it, which is a different
			// repair from a seat that polled and stopped.
			name: "declared and never polled",
			rows: []*store.PresenceRow{{Reader: "me", Kind: store.WaiterTracked, Attached: true, State: "starting"}},
			ok:   false,
			says: "never polled",
		},
	} {
		got, ok, _ := judgeWaiter(c.rows, "me", stale, now)
		if ok != c.ok {
			t.Errorf("%s: verdict %v, want %v (%q)", c.name, ok, c.ok, got)
			continue
		}
		if !strings.Contains(got, c.says) {
			t.Errorf("%s: the line does not say %q: %q", c.name, c.says, got)
		}
		// Healthy or broken, the seat is named. A nag prints this next to four
		// other seats' lines.
		if !strings.Contains(got, "me") {
			t.Errorf("%s: the line does not name the seat: %q", c.name, got)
		}
	}
}

// The process claim is the repair, so it is on the line - and when there is
// none, the line SAYS there is none. A gap where a pid goes reads as "no repair
// needed" and sends somebody back to `pkill -f`, which is the command that
// killed the shell running it twice in one night.
func TestAWaiterCheckNamesTheProcessOrSaysItCannot(t *testing.T) {
	now := time.Now()
	since := now.Add(-time.Hour)

	named := row("me", store.WaiterTracked, true, time.Second, now)
	named.Process = store.WaiterProcess{Pid: 4321, Since: &since, Host: "dead-XMG"}
	got, ok, _ := judgeWaiter([]*store.PresenceRow{named}, "me", time.Minute, now)
	if !ok {
		t.Fatalf("a fresh polling seat read as broken: %q", got)
	}
	if !strings.Contains(got, "pid 4321") || !strings.Contains(got, "dead-XMG") {
		t.Errorf("the line does not name the process to act on: %q", got)
	}

	// A claim missing a part is not a weaker claim - it is a number somebody
	// might act on believing it names something it does not.
	half := row("me", store.WaiterTracked, true, time.Second, now)
	half.Process = store.WaiterProcess{Pid: 4321}
	got, _, _ = judgeWaiter([]*store.PresenceRow{half}, "me", time.Minute, now)
	if strings.Contains(got, "4321") {
		t.Errorf("half an identity was printed as one: %q", got)
	}
	if !strings.Contains(got, "unnamed") {
		t.Errorf("a seat that claimed no process does not say so: %q", got)
	}
}

// TWO ROWS UNDER ONE NAME IS A DOUBLED WAITER. They share a cursor, so each
// hears part of the room while both look healthy - and it is invisible from
// every other surface a person has. The verdict comes from the newest poll,
// which is what somebody would act on; the doubling is said either way.
func TestADoubledWaiterIsSaidOutLoud(t *testing.T) {
	now := time.Now()
	rows := []*store.PresenceRow{
		row("me", store.WaiterTracked, true, 40*time.Minute, now),
		row("me", store.WaiterTracked, true, 5*time.Second, now),
	}
	got, ok, _ := judgeWaiter(rows, "me", 10*time.Minute, now)
	if !ok {
		t.Fatalf("the newest poll is five seconds old and the seat read as broken: %q", got)
	}
	if !strings.Contains(got, "2 rows wear this name") {
		t.Errorf("the doubling is not said: %q", got)
	}
}
