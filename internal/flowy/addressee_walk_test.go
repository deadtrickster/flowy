package flowy

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// EVERY DOOR THAT RESOLVES DISOWNED MARKS ON EVENTS ALSO RESOLVES ADDRESSEE
// NAMES ON THEM.
//
// The rule is not "these two functions are friends". It is that both are
// read-time findings the node adds to a page of events before handing it over,
// and the set of doors that hand over a page of events is the thing that keeps
// growing - five of them today, and chat.go's own comment beside one says why
// this test exists: "a second door onto the same messages is where a filter
// gets forgotten."
//
// A door that filled one and not the other would draw an id where every other
// door draws a name, in one pane of one console, and nothing would fail. That
// is the shape of defect this repo's source-walking tests exist for: it cannot
// be caught by exercising a door, because the door answers 200 either way.
//
// IT READS THE SOURCE, in the family of TestEveryRegisteredAPIRouteIsAdvertised
// next door and for the same reason: the alternative is a wrapper every read
// path has to be routed through, which is a refactor of five handlers to catch
// a class a twenty-line test catches from outside.
//
// FillDisowned takes artifacts OR events - FillDisowned(ctx, arts, events) -
// and only the event arm is this test's business, so a call passing nil for
// events is not a door onto messages and is skipped.
func TestEveryEventDoorNamesItsAddressee(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package: %v", err)
	}
	// FillDisowned(ctx, <artifacts>, <events>) - the events argument is the
	// last one, and "nil" there is the artifact-only form.
	disowned := regexp.MustCompile(`FillDisowned\([^,]+,\s*([^,]+),\s*([^)]+)\)`)
	named := regexp.MustCompile(`FillAddresseeNames\(`)

	var missing []string
	var found int
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			m := disowned.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			if strings.TrimSpace(m[2]) == "nil" {
				continue // an artifact page, not a page of messages
			}
			found++
			// The fill is an annotation pass, so it sits within a few lines of
			// its sibling rather than at a fixed offset - the window is wide
			// enough for a comment between them and narrow enough that it is
			// still the same handler.
			window := lines[i:min(i+18, len(lines))]
			if !named.MatchString(strings.Join(window, "\n")) {
				missing = append(missing, file+":"+itoa(i+1)+" "+strings.TrimSpace(line))
			}
		}
	}

	// A WALK THAT FOUND NOTHING PROVES NOTHING. If the call shape changes -
	// renamed, wrapped, moved behind a helper - this test would go green by
	// matching zero doors, which is the failure mode the advertised-routes walk
	// beside it also guards against.
	if found == 0 {
		t.Fatal("no door was found filling disowned marks on a page of events, so this test " +
			"measured nothing. FillDisowned's call shape changed and this walk did not.")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d of %d event doors resolve disowned marks but not addressee names, so they "+
			"answer an id where the others answer a name:\n  %s",
			len(missing), found, strings.Join(missing, "\n  "))
	}
}

// itoa is strconv.Itoa without the import, for one line number in one message.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for ; n > 0; n /= 10 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
	}
	return string(digits)
}
