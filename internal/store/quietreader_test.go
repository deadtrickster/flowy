package store

import (
	"testing"
	"time"
)

// A SEAT CANNOT REPORT ITS OWN DEATH, so the node reports it - and the rule for
// what counts as quiet is worth pinning exactly, because both errors are bad in
// different ways. Naming a reader that is merely slow trains everybody to
// ignore the field; missing one that is gone is the whole failure.
//
// Pure: quietFrom is the reading, and a check that needed a database to
// establish which rows it names is one nobody runs while changing the rule.
func TestAQuietReaderIsAttachedAndNotPolling(t *testing.T) {
	now := time.Date(2026, 8, 19, 21, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	rows := []*PresenceRow{
		// Polling now: the ordinary case, and the one that must never be named.
		{Reader: "busy", Attached: true, LastPoll: at(15 * time.Second)},
		// Just past the deadline of its own loop, twice over: gone.
		{Reader: "gone", Attached: true, LastPoll: at(11 * time.Minute), Kind: "tracked"},
		// A reader that HOLDS the name and wakes nobody. It polls, so it is not
		// quiet by this rule - the kind is what says it is deaf, and that is a
		// different finding with a different fix.
		{Reader: "forked", Attached: true, LastPoll: at(20 * time.Second), Kind: "forked"},
		// Detached: it said it was going. Not quiet, just not here.
		{Reader: "left", Attached: false, LastPoll: at(2 * time.Hour)},
		// Attached with no poll ever recorded - nothing to measure silence
		// from, and inventing one would name a reader for the node's own gap.
		{Reader: "never", Attached: true, LastPoll: nil},
		// Exactly at the threshold is not past it: the boundary belongs to the
		// side that says nothing, because a reader mid-cycle is the common case
		// and a false name costs more than a late one.
		{Reader: "edge", Attached: true, LastPoll: at(QuietAfter)},
	}

	got := quietFrom(rows, now)
	names := map[string]QuietReader{}
	for _, q := range got {
		names[q.Reader] = q
	}
	if len(got) != 1 {
		t.Fatalf("named %d readers %v, want only the one that stopped polling", len(got), names)
	}
	q, ok := names["gone"]
	if !ok {
		t.Fatalf("the reader that stopped polling was not named: %v", names)
	}
	// The DURATION, because the question is always "how long", and a reader
	// doing that subtraction itself gets the clock skew for free.
	if q.Silent != int((11 * time.Minute).Seconds()) {
		t.Fatalf("silent for %ds, want %d", q.Silent, int((11 * time.Minute).Seconds()))
	}
	if q.Kind != "tracked" {
		t.Fatalf("kind %q did not ride along", q.Kind)
	}

	// Nothing quiet is ABSENT, not an empty answer dressed as a finding - the
	// shape withheld, refused and unreadable all use here.
	if quiet := quietFrom(rows[:1], now); quiet != nil {
		t.Fatalf("a healthy node named %v", quiet)
	}
}
