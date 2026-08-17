package store

import (
	"strings"
	"testing"
)

// What a reader may do next, asserted from both seats the operator named: a
// person reading POSITION and an agent reading LEGAL NEXT MOVES.

func TestTheMovesFromEachStateAreNamedAndOrdered(t *testing.T) {
	// Likeliest first, because a panel draws them in order and a drainer takes
	// the head without ranking them itself.
	want := map[string][]string{
		TodoStatus:   {ActiveStatus, DoneStatus},
		ActiveStatus: {DoneStatus, TodoStatus},
		DoneStatus:   {TodoStatus, ActiveStatus},
	}
	for from, expect := range want {
		got := NextStatuses("todo", from)
		if strings.Join(got, ",") != strings.Join(expect, ",") {
			t.Errorf("from %q want %v, got %v", from, expect, got)
		}
	}
}

// A row with no status is outstanding work, not an unknown state - the same
// reading TodoStatusOf takes, and the two must not disagree.
func TestAnItemWithNoStatusWalksFromTheBeginning(t *testing.T) {
	got := NextStatuses("todo", "")
	if strings.Join(got, ",") != strings.Join([]string{ActiveStatus, DoneStatus}, ",") {
		t.Errorf("an unstated status starts at todo, got %v", got)
	}
}

// A state this build does not know - written by a peer running newer code, or
// before any of this existed - must not be a dead end. Stranded is worse than
// loose.
func TestAnUnknownStateIsNotADeadEnd(t *testing.T) {
	got := NextStatuses("todo", "blocked")
	if len(got) != len(QueueStatuses) {
		t.Fatalf("an unknown state offers the whole vocabulary, got %v", got)
	}
	for _, s := range got {
		if s == "blocked" {
			t.Errorf("and does not offer itself, got %v", got)
		}
	}
}

func TestTheLegalMovesAreTheOnesPeopleActuallyMadeToday(t *testing.T) {
	legal := [][2]string{
		{TodoStatus, ActiveStatus}, // taken up
		{TodoStatus, DoneStatus},   // finished without ever being claimed
		{ActiveStatus, DoneStatus}, // finished by whoever carried it
		{ActiveStatus, TodoStatus}, // put back down, which must need no apology
		{DoneStatus, TodoStatus},   // reopened - it was not finished after all
		{DoneStatus, ActiveStatus}, // reopened and taken up in one move
	}
	for _, m := range legal {
		if err := CheckTransition("bug", m[0], m[1]); err != nil {
			t.Errorf("%s -> %s must be legal: %v", m[0], m[1], err)
		}
	}
}

// Agreeing with reality is not a move. Two agents closing the same finished
// thing, or one retrying, must not get an error - idempotence is the difference
// between a lifecycle and a tripwire.
func TestMovingSomethingToWhereItAlreadyIsIsAllowed(t *testing.T) {
	for _, s := range QueueStatuses {
		if err := CheckTransition("todo", s, s); err != nil {
			t.Errorf("%s -> %s is a no-op, not an error: %v", s, s, err)
		}
	}
}

// The vocabulary stays closed at this door too - one word checked in one place,
// or a todo written here and one closed through the HTTP surface end up in two
// states no reader can compare.
func TestAWordOutsideTheVocabularyIsRefusedHereToo(t *testing.T) {
	err := CheckTransition("todo", TodoStatus, "in-review")
	if err == nil {
		t.Fatal("a status outside the closed set must be refused by the path as well")
	}
	if !strings.Contains(err.Error(), TodoStatus) {
		t.Errorf("the refusal names the vocabulary, got: %v", err)
	}
}

// The panel's whole answer: where it is, the path in walking order, and what can
// be done from here.
func TestPathOfDrawsPositionAndNextMoves(t *testing.T) {
	p := PathOf(&Artifact{Kind: "bug", Status: ActiveStatus})
	if p.At != ActiveStatus {
		t.Errorf("position is where the item is, got %q", p.At)
	}
	if strings.Join(p.States, ",") != strings.Join([]string{TodoStatus, ActiveStatus, DoneStatus}, ",") {
		t.Errorf("the path reads left to right in walking order, got %v", p.States)
	}
	if strings.Join(p.Next, ",") != strings.Join([]string{DoneStatus, TodoStatus}, ",") {
		t.Errorf("next moves from active, got %v", p.Next)
	}
	if len(p.Moves) != len(QueueStatuses) {
		t.Errorf("the whole table is drawable, got %v", p.Moves)
	}
	// A row with no status reads as the beginning of the path rather than as
	// nowhere, which is what the board could not do this evening.
	if got := PathOf(&Artifact{Kind: "todo"}).At; got != TodoStatus {
		t.Errorf("an item with no status is at the beginning, got %q", got)
	}
}

// Keyed by kind from the start, so a per-kind path later costs a row rather than
// a rewrite. Until one is filled in, every kind walks the same path - which is
// the honest state of it and is what this asserts.
func TestEveryKindWalksTheDefaultPathUntilOneEarnsItsOwn(t *testing.T) {
	base := NextStatuses("", TodoStatus)
	for _, kind := range append([]string{"bug", "feature", "chore", "question"}, WorkKinds...) {
		got := NextStatuses(kind, TodoStatus)
		if strings.Join(got, ",") != strings.Join(base, ",") {
			t.Errorf("kind %q has no path of its own yet, so it walks the default: got %v want %v",
				kind, got, base)
		}
	}
}
