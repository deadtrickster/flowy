package main

import (
	"strings"
	"testing"
	"time"
)

// A LOCK READING IS A CLAIM ABOUT THE PAST, and the line says when it was read.
//
// Three agents quoted a minutes-old lock reading as current in one afternoon,
// and every one of those readings was true when taken. The stamp is what makes
// a stale quote visible as stale to whoever reads it next - the script this
// verb replaces added it by hand, and this is the assertion that keeps it.
func TestALockLineSaysWhenItWasRead(t *testing.T) {
	read := time.Date(2026, 8, 19, 12, 0, 20, 0, time.UTC)

	free := lockLine(&mergeQueueLock{Held: false}, read)
	if !strings.Contains(free, "free") || !strings.Contains(free, "12:00:20Z") {
		t.Fatalf("a free lock read %q, want free and the moment it was read", free)
	}
	// Absent and not-held say the same thing, because a caller deciding to
	// declare wants "the target is free" either way rather than the absence of
	// a key they have to know the meaning of.
	if absent := lockLine(nil, read); !strings.Contains(absent, "free") {
		t.Fatalf("an absent lock read %q, want free", absent)
	}

	held := lockLine(&mergeQueueLock{
		Held: true, HolderName: "claude-host", Holder: "01M05T", Item: "01ROW",
		Until: time.Date(2026, 8, 19, 12, 13, 59, 0, time.UTC),
	}, read)
	for _, want := range []string{"claude-host", "01ROW", "12:13:59Z", "12:00:20Z"} {
		if !strings.Contains(held, want) {
			t.Fatalf("a held lock read %q, missing %q", held, want)
		}
	}
	// The name, not the id, when there is one - "held by 01M05T" is a sentence
	// nobody can act on without a second lookup.
	if strings.Contains(held, "01M05T ") {
		t.Fatalf("the line quotes the holder id rather than the name: %q", held)
	}
}

// A ROW SAYS WHY IT IS NOT MOVING. The script printed id and status, so a red
// row, a blocked row and a row nobody has reached yet all read the same - and
// those are exactly the three states somebody asking about the queue is asking
// about.
func TestARowSaysWhyItIsNotMoving(t *testing.T) {
	yes := true
	cases := []struct {
		name string
		it   mergeQueueItem
		want string
	}{
		{"red", mergeQueueItem{ID: "01A", Branch: "b", Red: &mergeQueueRed{Tip: "deadbeef1234567", Note: "passed: 660 failed: 1"}}, "RED"},
		{"blocked", mergeQueueItem{ID: "01B", Branch: "b", Blocked: &mergeQueueBlocked{Why: "checked out in /home/x"}}, "BLOCKED"},
		{"gating", mergeQueueItem{ID: "01C", Branch: "b", Gating: true}, "gating"},
		{"landable", mergeQueueItem{ID: "01D", Branch: "b", Admissible: &yes}, "LANDABLE"},
		{"waiting", mergeQueueItem{ID: "01E", Branch: "b", Status: "todo"}, "todo"},
	}
	for _, c := range cases {
		got := rowLine(c.it)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s row read %q, want it to say %q", c.name, got, c.want)
		}
		if !strings.Contains(got, c.it.ID) {
			t.Errorf("%s row read %q, without its id", c.name, got)
		}
	}
	// A red is louder than a gate, because a row being re-measured after a red
	// is the case where "gating" alone would hide the thing worth knowing.
	both := rowLine(mergeQueueItem{ID: "01F", Branch: "b", Gating: true, Red: &mergeQueueRed{Tip: "abc"}})
	if !strings.Contains(both, "RED") {
		t.Errorf("a row that is red AND gating read %q, hiding the red", both)
	}
	// And a note never breaks the line into two.
	long := rowLine(mergeQueueItem{ID: "01G", Branch: "b", Red: &mergeQueueRed{Tip: "abc", Note: "line one\nline two"}})
	if strings.Contains(long, "\n") {
		t.Errorf("a multi-line note put a newline in one row: %q", long)
	}
}
