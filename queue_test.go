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
		got := rowLine(c.it, "")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s row read %q, want it to say %q", c.name, got, c.want)
		}
		if !strings.Contains(got, c.it.ID) {
			t.Errorf("%s row read %q, without its id", c.name, got)
		}
	}
	// A red is louder than a gate, because a row being re-measured after a red
	// is the case where "gating" alone would hide the thing worth knowing.
	both := rowLine(mergeQueueItem{ID: "01F", Branch: "b", Gating: true, Red: &mergeQueueRed{Tip: "abc"}}, "")
	if !strings.Contains(both, "RED") {
		t.Errorf("a row that is red AND gating read %q, hiding the red", both)
	}
	// And a note never breaks the line into two.
	long := rowLine(mergeQueueItem{ID: "01G", Branch: "b", Red: &mergeQueueRed{Tip: "abc", Note: "line one\nline two"}}, "")
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
// A REASON IS NOT CUT, because no rule over the string can find the part that
// matters.
//
// This replaces TestACutReasonSaysSoAndKeepsBothEnds, whose property - "keeps
// both ends" - is the third cutting rule this line has had and was wrong for
// the case filed as 01M0G3Y16C:
//
//	stored   vm-door is checked out in /home/dead/Projects/flowy-vmdoor, so it cannot be rebased here
//	shown    vm-door is checked out in /hom…so it cannot be rebased here
//
// Keeping the tail lost the counts off a red, keeping the head lost the path
// off a block, and keeping both ends loses a path in the middle. Every rule is
// right for the reason its author had in front of them, which is what says the
// rule is the wrong instrument.
func TestALongReasonGetsItsOwnLineWholeRatherThanBeingCut(t *testing.T) {
	// THE EXACT REASON FROM THE ROW, at 88 characters, with the path in the
	// middle where every cutting rule loses it.
	why := "vm-door is checked out in /home/dead/Projects/flowy-vmdoor, so it cannot be rebased here"
	blocked := rowLine(mergeQueueItem{
		ID: "01H", Branch: "vm-door",
		Blocked: &mergeQueueBlocked{Why: why},
	}, "")
	if !strings.Contains(blocked, why) {
		t.Errorf("the reason did not survive whole:\n%s", blocked)
	}
	if strings.Contains(blocked, "…") {
		t.Errorf("the reason was cut rather than wrapped:\n%s", blocked)
	}
	head, rest, wrapped := strings.Cut(blocked, "\n")
	if !wrapped {
		t.Fatalf("an 88-character reason stayed on the row line:\n%s", blocked)
	}
	// The row still says WHAT IS TRUE at a glance - the state word is on the
	// row, not on the wrapped line - which is what makes a queue scannable and
	// is the thing the wrap must not cost.
	if !strings.Contains(head, "BLOCKED") {
		t.Errorf("the row line lost its state to the wrap: %q", head)
	}
	if !strings.HasPrefix(rest, reasonIndent) {
		t.Errorf("the wrapped reason is not indented under its row: %q", rest)
	}
	if strings.Contains(rest, "\n") {
		t.Errorf("one reason took more than one extra line:\n%s", blocked)
	}

	// A red note wraps the same way and keeps the counts it leads with, which
	// the first cutting rule dropped.
	red := rowLine(mergeQueueItem{
		ID: "01I", Branch: "b",
		Red: &mergeQueueRed{
			Tip:  "c58abd2000",
			Note: "passed: 699 failed: 9 - FAIL the tui, driven headless by the keyboard against the live node",
		},
	}, "")
	if !strings.Contains(red, "passed: 699 failed: 9") {
		t.Errorf("a red row read %q, dropping the counts it leads with", red)
	}
	if !strings.Contains(red, "against the live node") {
		t.Errorf("a red row read %q, dropping the name of what failed", red)
	}

	// A reason that FITS is not moved and not marked. The wrap is for the ones
	// that do not fit; a queue where every row takes two lines is the cost this
	// change is bounded to avoid.
	whole := rowLine(mergeQueueItem{
		ID: "01J", Branch: "b",
		Blocked: &mergeQueueBlocked{Why: "the lock is held"},
	}, "")
	if strings.Contains(whole, "\n") || strings.Contains(whole, "…") {
		t.Errorf("a reason that fits was wrapped or marked: %q", whole)
	}

	// AND THE LAST RESORT IS STILL THERE. A reason too long even for its own
	// line is elided in the middle and says so - the old rule, kept as what it
	// always should have been rather than as the rule.
	// Past reasonWrapWidth, which is 200: this is about 500 characters, and it
	// is deliberately built from the budget rather than typed to a length, so
	// raising the budget again does not silently stop exercising the last
	// resort - which is how the 110 that clipped a real path survived review.
	huge := "x" + strings.Repeat(" and more words about it", (reasonWrapWidth/24)+8) + " END"
	long := rowLine(mergeQueueItem{
		ID: "01K", Branch: "b",
		Blocked: &mergeQueueBlocked{Why: huge},
	}, "")
	if !strings.Contains(long, "…") {
		t.Errorf("a reason past the wrapped budget was not marked as cut:\n%s", long)
	}
	if n := strings.Count(long, "\n"); n != 1 {
		t.Errorf("a very long reason took %d extra lines, want 1:\n%s", n, long)
	}

	// AND A REAL ONE FROM THE LIVE QUEUE, which the first budget clipped. 111
	// characters, a path in the middle, and the branch names in this fleet are
	// longer than the row's example - so the case that was measured on the node
	// is in here rather than only the case that was filed.
	real := "feat/wrap-probe-orch2 is checked out in " +
		"/home/dead/Projects/flowy-wt-orchestrator, so it cannot be rebased here"
	live := rowLine(mergeQueueItem{
		ID: "01L", Branch: "feat/wrap-probe-orch2",
		Blocked: &mergeQueueBlocked{Why: real},
	}, "")
	if !strings.Contains(live, real) {
		t.Errorf("a 111-character reason from the live queue did not survive:\n%s", live)
	}

	// Counted in runes. Slicing a byte index through a multi-byte character
	// leaves a replacement glyph, which is a corrupted reason that still looks
	// like a reason.
	wide := elide(strings.Repeat("é", 200), reasonWidth)
	for _, r := range wide {
		if r == '\uFFFD' {
			t.Errorf("cutting a reason of wide characters produced %q", wide)
			break
		}
	}
}

// A QUEUE LINE SAYS WHAT IS TRUE NOW, not the loudest thing the row remembers.
//
// This is 01M0GAJVEF, and it is written from the response that caused it. One
// row, one read of /api/merge-queue on 2026-08-20, carrying three answers at
// once thirty-four minutes apart:
//
//	red      35e4256, measured from base db7ec6b, at 18:56
//	blocked  "checked out in .../wt-sw3", at 19:30
//	target   f0f0df8
//
// The line printed the red, because the switch tested Red first. That red was
// not merely old - it was measured from a base the target had left, which the
// same object says, and which drain.sh already uses to decide the row is worth
// re-gating. Two seats spent an hour between them on the test that red named
// before either read the field below it.
func TestASpentRedDoesNotOutrankWhatIsTrueNow(t *testing.T) {
	tonight := mergeQueueItem{
		ID: "01M0G34J3N", Branch: "feat/the-console-switches-projects",
		Red:     &mergeQueueRed{Tip: "35e4256", Base: "db7ec6b", Note: "passed: 713 failed: 1"},
		Blocked: &mergeQueueBlocked{Why: "checked out in /tmp/scratchpad/wt-sw3"},
	}
	got := rowLine(tonight, "f0f0df8")
	if strings.Contains(got, "713") {
		t.Errorf("the line still leads with a spent red: %q", got)
	}
	if !strings.Contains(got, "BLOCKED") || !strings.Contains(got, "wt-sw3") {
		t.Errorf("the line reads %q - it must say the reason that is true now", got)
	}

	// A LIVE RED IS UNTOUCHED. Same row, same everything, except the target has
	// not moved since it was measured. Without this the fix is "stop reporting
	// reds", which is worse than what it replaces.
	live := rowLine(tonight, "db7ec6b")
	if !strings.Contains(live, "RED") || !strings.Contains(live, "713") {
		t.Errorf("a red measured from the CURRENT target read %q, and must still lead", live)
	}

	// AN UNKNOWN BASE COUNTS AS LIVE. The rule only ever demotes a red whose
	// base we can see has moved; anything else keeps shouting, because the cost
	// of hiding a live red is a broken landing and the cost of showing a spent
	// one is a sentence.
	noBase := rowLine(mergeQueueItem{
		ID: "01K", Branch: "b",
		Red: &mergeQueueRed{Tip: "abc1234", Note: "passed: 1 failed: 1"},
	}, "f0f0df8")
	if !strings.Contains(noBase, "RED") {
		t.Errorf("a red with no recorded base read %q - it must be treated as live", noBase)
	}

	// AND A SPENT RED SAYS SO rather than vanishing. A row that goes quiet is
	// the other failure: somebody who saw the red an hour ago needs to be told
	// it no longer applies, not left to wonder where it went.
	alone := rowLine(mergeQueueItem{
		ID: "01L", Branch: "b", Status: "todo",
		Red: &mergeQueueRed{Tip: "35e4256", Base: "db7ec6b", Note: "passed: 713 failed: 1"},
	}, "f0f0df8")
	if !strings.Contains(alone, "spent") {
		t.Errorf("a row whose only mark is a spent red read %q, saying nothing about it", alone)
	}
	if !strings.Contains(alone, "db7ec6b") || !strings.Contains(alone, "f0f0df8") {
		t.Errorf("the spent line reads %q without both shas - it must say what moved", alone)
	}
}
