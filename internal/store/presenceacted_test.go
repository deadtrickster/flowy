package store

import (
	"strings"
	"testing"
)

// ATTACHED IS NOT ABLE. The roster answers "is a waiter alive" from
// last_poll_at, and that cannot say whether the agent behind it can act: a
// rate-limited seat polls on time and does nothing, and a seat mid-run may be
// silent for forty minutes while being the busiest thing on the node.
//
// So presence carries two clocks, and this asserts the second is taken from the
// place that knows - the log - rather than from the roster's own columns, which
// can only ever describe the waiter.
func TestPresenceReadsTheActedClockFromTheLog(t *testing.T) {
	src := readStoreSource(t, "inbox.go")
	q := src[indexFold(src, "func (d *DB) Presence"):]
	q = q[:indexFold(q, "rows.Scan")]

	if !containsFold(q, "max(e.created) FROM events") {
		t.Error("last_acted must come from the events log - the roster's own columns only describe the waiter")
	}
	// Matched on the ACTOR, and the actor is a seat. Two seats of one person
	// act separately and the roster is per seat, so matching the user would
	// make one busy agent hide an idle sibling.
	if !containsFold(q, "e.actor") {
		t.Error("the acted clock must match the actor")
	}
}

// The two clocks answer different questions and must both survive. A change
// that dropped either would leave the roster unable to tell a live waiter with
// a blocked agent from a working agent whose waiter died - which are the two
// failures this fleet actually had.
func TestPresenceKeepsBothClocks(t *testing.T) {
	src := readStoreSource(t, "inbox.go")
	row := src[indexFold(src, "type PresenceRow"):]
	row = row[:indexFold(row, "\n}")]
	for _, field := range []string{"LastPoll", "LastActed"} {
		if !strings.Contains(row, field) {
			t.Errorf("PresenceRow must carry %s - the poll is the waiter's pulse, acted is the agent's", field)
		}
	}
}
