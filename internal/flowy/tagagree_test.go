package flowy

import (
	"os"
	"strings"
	"testing"
)

// The node finds the ignored-rooms note by a tag the CONSOLE writes, and the
// two spellings live in different languages with no build step between them.
//
// A DIVERGENCE HERE IS SILENT AND LOOKS LIKE THE FEATURE NOT WORKING: the
// console would write its note, the node would look for a tag nobody had
// written, find nothing, and deliver everything - which is exactly what a
// reader who has ignored nothing sees. No error anywhere, and the bug reads as
// "ignore does not do anything".
//
// Read out of the file rather than typed here a third time, because a constant
// asserted against a copy of itself is a test that agrees with whoever edited
// it last.
func TestTheIgnoredRoomsTagIsSpeltTheSameOnBothSides(t *testing.T) {
	src, err := os.ReadFile("../../web/src/lib/api.ts")
	if err != nil {
		t.Fatalf("reading the console's api: %v", err)
	}
	want := `export const IGNORED_ROOMS_TAG = "` + IgnoredRoomsTag + `";`
	if !strings.Contains(string(src), want) {
		t.Errorf("the console does not spell the ignored-rooms tag the way this node looks it up.\n"+
			"Go has IgnoredRoomsTag = %q, so web/src/lib/api.ts must contain:\n  %s\n"+
			"A node looking for a tag nobody writes finds nothing and delivers everything,\n"+
			"which is indistinguishable from a reader who has ignored no rooms.",
			IgnoredRoomsTag, want)
	}
}
