package flowy

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// EVERY "no such artifact" SAYS WHERE IT LOOKED.
//
// 01M17RVV777776424HGXJZC46M measured the defect on one door: a row this
// credential cannot reach answers "no such todo: <id>", the same sentence a
// deleted row and a typo give, and the reader concludes the row is gone. The
// operator's own shell token reads a backfill fixture project, so every row
// this fleet files was invisible to it and said so as though nothing were
// there.
//
// scopeNote fixed the sentence and notFoundNote is how a door says it. What
// nothing kept true is WHICH DOORS CALL IT: when this test was written five
// artifact 404s existed and one of them carried half the note - the typo
// diagnosis without the scope one - which is what a half-finished sweep looks
// like from outside.
//
// AN ARTIFACT IS THE PROJECT-FILTERED THING, which is why this walk is that
// family and not every "no such X" in the package. A merge request, an
// announcement and a join request each answer their own 404s and there are
// about 35 of them; whether each is scope-filtered is a question per door, and
// a walk that swept them all in would be asserting a property nobody has
// checked. Named here rather than left silent: this covers artifacts, and the
// rest of that pile is unguarded.
//
// IT READS THE SOURCE, in the family of TestEveryRegisteredAPIRouteIsAdvertised
// and TestEveryEventDoorNamesItsAddressee: a door that answers 404 without the
// note is a door answering 200-shaped nothing, and no request exercises the
// difference.
func TestEveryNoSuchArtifactSaysWhereItLooked(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}
	// errorBody("no such artifact" ... ) - the refusal and whatever it appends.
	refusal := regexp.MustCompile(`errorBody\("no such artifact"([^)]*)`)

	var bare []string
	var found int
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			m := refusal.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			found++
			if !strings.Contains(m[1], "notFoundNote") {
				bare = append(bare, file+":"+itoa(i+1)+" "+strings.TrimSpace(line))
			}
		}
	}

	// A WALK THAT MATCHED NOTHING PROVES NOTHING - the failure mode of every
	// source-reading test, and the one that makes them go quietly green when
	// the thing they read gets renamed.
	if found == 0 {
		t.Fatal(`no "no such artifact" refusal was found, so this test measured nothing. ` +
			"The wording or errorBody's shape changed and this walk did not.")
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d of %d artifact 404s do not say which projects they searched, so each is "+
			"the sentence a DELETED row gives on a row that is merely out of reach:\n  %s\n"+
			"Append s.notFoundNote(r, <the id>), which carries the typo diagnosis too.",
			len(bare), found, strings.Join(bare, "\n  "))
	}
}
