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

// A CUT REASON SAYS IT WAS CUT, AND KEEPS BOTH ENDS.
//
// The queue printed this for a row that then sat blocked for eleven minutes:
//
//	BLOCKED feat/a-person-belongs-to-projects is checked out in /tmp/cla
//
// Nothing there says the sentence stopped early, so it reads as a complete
// reason about a directory that does not exist, and the path - the only part
// anybody can act on - is gone.
//
// Both ends, because the two reasons this prints put their meaning at opposite
// ends. A blocked reason finishes with the path. A red note begins with the
// counts and the first failing test. Keeping either end alone fixes one of them
// and breaks the other, which is what the first version of this fix did.
func TestACutReasonSaysSoAndKeepsBothEnds(t *testing.T) {
	blocked := rowLine(mergeQueueItem{
		ID: "01H", Branch: "feat/a-person-belongs-to-projects",
		Blocked: &mergeQueueBlocked{
			Why: "feat/a-person-belongs-to-projects is checked out in /tmp/claude-1000/scratchpad/wt-member",
		},
	})
	if !strings.Contains(blocked, "…") {
		t.Errorf("a cut reason read %q, with nothing saying it was cut", blocked)
	}
	// The tail is the whole point of this one: a path a person can go and free.
	if !strings.Contains(blocked, "wt-member") {
		t.Errorf("a blocked row read %q, dropping the path it is blocked on", blocked)
	}

	red := rowLine(mergeQueueItem{
		ID: "01I", Branch: "b",
		Red: &mergeQueueRed{
			Tip:  "c58abd2000",
			Note: "passed: 699 failed: 9 - FAIL the tui, driven headless by the keyboard against the live node",
		},
	})
	// And the head is the whole point of this one: what failed and how much.
	if !strings.Contains(red, "passed: 699 failed: 9") {
		t.Errorf("a red row read %q, dropping the counts it leads with", red)
	}
	if !strings.Contains(red, "…") {
		t.Errorf("a cut note read %q, with nothing saying it was cut", red)
	}

	// A reason that fits is not touched - no ellipsis on a whole sentence, which
	// would be the same lie in the other direction.
	whole := rowLine(mergeQueueItem{
		ID: "01J", Branch: "b",
		Blocked: &mergeQueueBlocked{Why: "the lock is held"},
	})
	if strings.Contains(whole, "…") {
		t.Errorf("a reason that fits read %q, marked as cut", whole)
	}

	// Counted in runes. Slicing a byte index through a multi-byte character
	// leaves a replacement glyph, which is a corrupted reason that still looks
	// like a reason.
	wide := firstLine(strings.Repeat("é", 200))
	for _, r := range wide {
		if r == '�' {
			t.Errorf("cutting a reason of wide characters produced %q", wide)
			break
		}
	}
}
