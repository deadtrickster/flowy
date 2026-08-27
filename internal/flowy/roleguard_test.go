package flowy

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/forge"
)

// EVERY GUARDED ROUTE SAYS WHAT IT NEEDS, and the walk is what makes that true
// rather than intended.
//
// This is paramguard_test.go's completeness check, deliberately: that one
// caught a renew door this morning before it landed, and it is the reason 61
// write doors can be governed by one table without anybody having to remember
// the 62nd. Enforcement lives in the SUITE - the door has nothing to recall.
//
// BOTH DIRECTIONS. A route with no entry is a door nobody has judged; an entry
// with no route is a rule about a door that no longer exists, and it reads as
// coverage while covering nothing.
func TestEveryRouteSaysWhatItNeeds(t *testing.T) {
	// WITH THE MOCK FORGE ON, for paramguard_test.go's reason: six routes are
	// registered inside an `if s.mockForge != nil` block, and a bare server
	// leaves them out - so the check would agree perfectly about a set that
	// silently excluded them.
	s := &server{mockForge: forge.NewMockForge()}
	s.routes()

	// The witness that this measured anything at all: a recorder that captured
	// nothing would report perfect agreement between two empty sets.
	if len(s.apiPatterns) < 80 {
		t.Fatalf("only %d routes were recorded - the recorder is not seeing registrations",
			len(s.apiPatterns))
	}

	registered := map[string]bool{}
	for _, pattern := range s.apiPatterns {
		// The catch-all has no method and answers unknown paths; it is not a
		// door with a permission question - see roleGuard, which skips it for
		// the same reason paramGuard does.
		if !strings.Contains(pattern, " ") {
			continue
		}
		// READS ARE NOT IN THE TABLE, because the guard never consults it for
		// them: a GET is exempt in roleGuard itself. An entry for one would be
		// a rule nothing reads, which is the "coverage that covers nothing"
		// this test refuses one paragraph down.
		//
		// If reading ever becomes role-gated - it is not today, the permission
		// filter decides what a principal may see - this exemption is the line
		// to delete, and the walk will then name every read that has not been
		// judged.
		if strings.HasPrefix(pattern, "GET ") || strings.HasPrefix(pattern, "HEAD ") {
			continue
		}
		registered[pattern] = true
		if _, ok := routeNeeds[pattern]; !ok {
			t.Errorf("%s is behind the guard and not in routeNeeds - it is currently treated as "+
				"needing nothing, which may be right, but nobody has said so. Add an entry: "+
				"needsWrite, needsNothing or needsOperator.", pattern)
		}
	}
	for pattern := range routeNeeds {
		if !registered[pattern] {
			t.Errorf("routeNeeds declares %q, which is not registered on the guarded mux - "+
				"a rule about a door that does not exist reads as coverage and covers nothing",
				pattern)
		}
	}
}

// A READ IS NEVER A WRITE. Every GET is exempt in the guard itself rather than
// by being listed, so a table entry that called one a write would be a rule
// nothing consults - and this asserts the table agrees with the guard.
func TestNoReadIsDeclaredAWrite(t *testing.T) {
	for pattern, needs := range routeNeeds {
		if strings.HasPrefix(pattern, "GET ") && needs == needsWrite {
			t.Errorf("%s is a read declared as a write: the guard exempts GET, so this entry "+
				"says something the node does not do", pattern)
		}
	}
}

// THE ACTS A READER MUST KEEP. If "readonly" stopped somebody acking their own
// inbox, declaring their own reader, entering a project they belong to or
// leaving a room, it would mean "cannot use the tool" - which is not a role
// anybody asked for. Named here so that reclassifying one of them as a write
// has to be an argument somebody makes on purpose.
func TestAReaderCanStillUseTheirOwnSession(t *testing.T) {
	for _, pattern := range []string{
		"POST /api/inbox/ack",
		"POST /api/inbox/reader",
		"DELETE /api/inbox/reader/{name}",
		"POST /api/projects/{project}/enter",
		"POST /api/rooms/{room}/leave",
		"PUT /api/me/auto_delegate",
	} {
		if routeNeeds[pattern] == needsWrite {
			t.Errorf("%s is declared a project write, so a reader cannot do it - "+
				"that makes readonly mean 'cannot use the tool'", pattern)
		}
	}
}
