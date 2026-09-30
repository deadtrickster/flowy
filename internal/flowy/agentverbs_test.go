package flowy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

func TestEventLineNamesWhoToWhomAndWhere(t *testing.T) {
	e := &store.Event{
		ID:            "01M1",
		Thread:        "01M0",
		Actor:         "U1",
		Addressee:     "A2",
		AddresseeName: "claude-lab2x1",
		Body:          "gating X - run Y on Z\nsecond line",
		Meta:          json.RawMessage(`{"actor_kind":"user","actor_name":"deadtrickster"}`),
		Created:       time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC),
	}
	got := eventLine(e)
	want := "[2026-09-14 08:00] deadtrickster (person) -> claude-lab2x1  id 01M1  thread 01M0\n  gating X - run Y on Z\n  second line\n"
	if got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestEventLineFallsBackToIdsWhenNothingIsNamed(t *testing.T) {
	e := &store.Event{ID: "01M1", Actor: "U1", Addressee: "A2", Body: "x"}
	got := eventLine(e)
	if !strings.Contains(got, "U1 -> A2") {
		t.Fatalf("ids should stand in for names: %q", got)
	}
}

func TestMimeByExtensionKnowsTheCommonOnesAndDefaultsHonestly(t *testing.T) {
	cases := map[string]string{
		"a.png": "image/png", "b.JPG": "image/jpeg", "c.md": "text/markdown",
		"d.jsonl": "application/json", "e.bin": "application/octet-stream", "f": "application/octet-stream",
	}
	for name, want := range cases {
		if got := mimeByExtension(name); got != want {
			t.Errorf("%s: got %s want %s", name, got, want)
		}
	}
}

// Every verb the dispatcher knows is in the menu, and every verb in the menu is
// one the dispatcher knows. The menu missing `get`, `dm`, `waiter` and `nag` is
// what 25 reads of it on lab2x1 could not repair.
// TestEveryDispatchedVerbIsInTheMenu reads the verbs out of Run's own switch
// rather than listing them here.
//
// It listed them here until 2026-09-30, and a hand-kept list is a guard that
// covers what somebody remembered to add: `search` went in with no menu entry
// and this test stayed green, which is the one thing it exists to catch. The
// file's header counts what an unlisted verb costs - 25 reads of a top-level
// menu that did not name `get`, `dm`, `waiter` or `nag`, then hand-built curls
// to the doors behind them.
//
// Aliases are read too, and deliberately: `skill` and `find` are dispatched, so
// a reader can type them, so the menu has to account for them. An alias earns
// its place in the entry for the verb it aliases - `  skills, skill  ` - rather
// than a line of its own.
func TestEveryDispatchedVerbIsInTheMenu(t *testing.T) {
	verbs := dispatchedVerbs(t)
	menu := menuVerbs()
	// The three that are not agent verbs and are documented as their own
	// sections rather than menu rows.
	notMenuRows := map[string]bool{"help": true, "-h": true, "--help": true, "version": true, "--version": true, "-v": true}
	listed := 0
	for _, verb := range verbs {
		if notMenuRows[verb] {
			continue
		}
		listed++
		if !menu[verb] {
			t.Errorf("verb %q is dispatched but the menu does not list it", verb)
		}
	}
	// A guard that read no verbs would pass every assertion above. The count is
	// the guard on the guard: Run dispatched 30 verbs when this was written, so
	// a parser that silently stopped matching answers 0 or 1 and fails here
	// instead of reporting a clean sweep of nothing.
	if listed < 20 {
		t.Fatalf("only %d verbs parsed out of Run's switch - the parser has stopped matching, so this test proved nothing", listed)
	}
}

// dispatchedVerbs reads the case labels of Run's switch out of main.go. Parsed
// from the source because the switch is the only statement of what the CLI
// accepts: a second list in Go would drift from it exactly the way the list
// this replaced did.
func dispatchedVerbs(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	inSwitch := false
	var verbs []string
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "switch cmd := args[0]; cmd {") {
			inSwitch = true
			continue
		}
		if !inSwitch {
			continue
		}
		if line == "\t}" {
			break
		}
		// Exactly one tab: the switch's OWN cases. A case inside a nested
		// switch - `case errors.Is(err, errWaitedOut):` in the wait verb - is
		// indented deeper, and reading those as verb names is how the first
		// version of this parser reported `errors.Is(err` as an unlisted verb.
		if !strings.HasPrefix(line, "\tcase ") || !strings.HasSuffix(trimmed, ":") {
			continue
		}
		for _, part := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(trimmed, "case "), ":"), ",") {
			part = strings.TrimSpace(part)
			// Only quoted literals. A case label that is an expression is not
			// a verb somebody can type.
			if len(part) < 3 || part[0] != '"' || part[len(part)-1] != '"' {
				continue
			}
			if v := strings.Trim(part, `"`); v != "" {
				verbs = append(verbs, v)
			}
		}
	}
	return verbs
}

// menuVerbs reads the names off the left column of the usage menu, so a verb
// that shares an entry with its aliases - `  roster, presence, who` - counts as
// listed for each of them. Matching the rendered line with a suffix test needed
// a case per punctuation mark and got the comma wrong first time.
func menuVerbs() map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(usage, "\n") {
		if !strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "   ") {
			continue
		}
		head := strings.TrimSpace(line)
		// Two shapes of entry, told apart by whether anything on the line has a
		// space in it once the commas are taken out.
		//
		//   `search, find`            names only, prose indented on the lines below
		//   `projects which project`  one name, then the prose on the same line
		//
		// A fixed-width slice of the name column was tried first and got
		// `projects` and `identity` wrong: both names are long enough to leave
		// only one space before their prose, so the slice cut into the sentence.
		parts := strings.Split(head, ", ")
		namesOnly := true
		for _, part := range parts {
			if strings.ContainsAny(strings.TrimSpace(part), " \t") {
				namesOnly = false
				break
			}
		}
		if !namesOnly {
			parts = parts[:1]
			if i := strings.IndexAny(parts[0], " \t"); i >= 0 {
				parts[0] = parts[0][:i]
			}
		}
		for _, name := range parts {
			if n := strings.TrimSpace(strings.TrimSuffix(name, ",")); n != "" {
				out[n] = true
			}
		}
	}
	return out
}

// TestSearchLineMarksASupersededHit is the arm that matters on a search result.
//
// replaced_by is derived at read time and only on the doors that carry the
// permission filter, so a hit is the main place a reader meets it - and a
// stale document ranks against the words that matched it exactly as well as
// the document that replaced it. A line that dropped the marker would sort
// the two together and read identically.
//
// Three arms, because the field has three states and they print differently:
// an address to send the reader to, a bare id when the replacement is personal
// and has no address, and nothing at all.
func TestSearchLineMarksASupersededHit(t *testing.T) {
	hit := func(mut func(*store.Artifact)) store.Ranked {
		project := "Lab"
		a := &store.Artifact{
			ID: "01MOLD", Type: "memory", Kind: "note", Title: "five instances",
			Project: &project, Created: time.Date(2026, 9, 30, 17, 24, 0, 0, time.UTC),
		}
		if mut != nil {
			mut(a)
		}
		return store.Ranked{Artifact: a, Rank: 0.5}
	}

	withRef := searchLine(hit(func(a *store.Artifact) {
		a.ReplacedBy = "01MNEW"
		a.ReplacedByRef = "Lab/memory/01MNEW"
	}))
	if !strings.Contains(withRef, "SUPERSEDED by Lab/memory/01MNEW") {
		t.Errorf("a superseded hit should send the reader to the address: %q", withRef)
	}

	// The ref is preferred over the bare id, because project and type are not
	// guaranteed to carry over from the row that was replaced - the reason
	// ReplacedByRef exists at all.
	if strings.Contains(withRef, "SUPERSEDED by 01MNEW") {
		t.Errorf("the bare id should not stand in when an address is known: %q", withRef)
	}

	bareID := searchLine(hit(func(a *store.Artifact) { a.ReplacedBy = "01MNEW" }))
	if !strings.Contains(bareID, "SUPERSEDED by 01MNEW") {
		t.Errorf("with no address the id is all the truth there is: %q", bareID)
	}

	// And the arm that keeps the marker meaningful: an unreplaced hit must not
	// carry it. A line that always said SUPERSEDED would satisfy both arms
	// above.
	if plain := searchLine(hit(nil)); strings.Contains(plain, "SUPERSEDED") {
		t.Errorf("an unreplaced hit must not be marked: %q", plain)
	}
}

// TestSearchLineNamesTheRowAndWhereItLives keeps the columns a reader acts on:
// the id, because the next thing they do is fetch one, and the project, because
// a hit from a project they were not thinking about is the common case on a
// node holding several.
func TestSearchLineNamesTheRowAndWhereItLives(t *testing.T) {
	project := "flowy"
	line := searchLine(store.Ranked{Artifact: &store.Artifact{
		ID: "01MROW", Type: "memory", Kind: "todo", Title: "no search verb",
		Status: "todo", Project: &project,
		Created: time.Date(2026, 9, 30, 18, 30, 0, 0, time.UTC),
	}})
	for _, want := range []string{"2026-09-30", "memory/todo", "01MROW", "no search verb", "flowy todo"} {
		if !strings.Contains(line, want) {
			t.Errorf("a hit line should carry %q: %q", want, line)
		}
	}
}

// TestSearchLineSurvivesANilArtifact because store.Ranked embeds a pointer and
// a decode of a malformed page yields one that is nil. Printing is not worth a
// panic in a read-only verb.
func TestSearchLineSurvivesANilArtifact(t *testing.T) {
	if got := searchLine(store.Ranked{}); got != "" {
		t.Errorf("a nil artifact prints nothing, not %q", got)
	}
}

// TestNarrowHintOnlyFiresWhereNarrowingIsTheLikelyCause pins both directions,
// because a hint that always fired would be advice to widen a query that is
// already one word - and that is how a reader is sent looking for a broader
// spelling of a thing the fabric genuinely does not hold.
func TestNarrowHintOnlyFiresWhereNarrowingIsTheLikelyCause(t *testing.T) {
	if got := narrowHint(1); got != "" {
		t.Errorf("a one-word miss is the fabric's answer, not a narrowing problem: %q", got)
	}
	if got := narrowHint(0); got != "" {
		t.Errorf("no terms is refused before the call; the hint stays quiet: %q", got)
	}
	got := narrowHint(4)
	if !strings.Contains(got, "4 terms") {
		t.Errorf("the hint names how many terms had to match: %q", got)
	}
	if !strings.Contains(got, "one word") {
		t.Errorf("the hint says what to do next, not just what went wrong: %q", got)
	}
}
