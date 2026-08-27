package flowy

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// EVERY DOOR ON THE GUARDED MUX IS ON THE ANSWER /api/node GIVES.
//
// apiRoutes is what a client is told this node can do, and the list carries its
// own reason: a door that is not on the answer is a door nobody finds. Measured
// on 2026-08-19, 34 of them were missing - the whole merge chain, both lock
// doors, /api/me, rooms, todo deps and category, finding evidence, the join
// approvals, worklog and role. Two doors were ADDED to the list that same week
// with that sentence quoted, mine among them, while those 34 sat absent.
//
// So the rule was understood and the list drifted anyway, which means the list
// is not what keeps it true. This is.
//
// IT READS THE SOURCE, which is unusual and is the only form that costs nothing
// elsewhere. Go's ServeMux does not expose its patterns, so the alternative is a
// registry every route line has to go through - a refactor of every registration
// in a file three agents are editing today, to catch a class of bug that a
// twenty-line test catches from outside.
func TestEveryRegisteredAPIRouteIsAdvertised(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	registered := regexp.MustCompile(`api\.HandleFunc\("([^"]+)"`)
	advertised := map[string]bool{}
	for _, r := range apiRoutes {
		advertised[r] = true
	}

	var missing []string
	for _, m := range registered.FindAllStringSubmatch(string(src), -1) {
		pattern := m[1]
		// THE TWO THAT ARE NOT DOORS, excluded by name and with a reason rather
		// than by being quietly absent.
		//
		// "/api/" is the catch-all that turns an unknown path into a 404 instead
		// of letting the param guard answer about a pattern the caller never
		// wrote. "GET /api/ready" answers about the process rather than for a
		// principal, like /healthz beside it, and both are already sent
		// separately in the /api/node answer.
		if pattern == "/api/" || pattern == "GET /api/ready" {
			continue
		}
		if !advertised[pattern] {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d route(s) are registered and not in apiRoutes, so /api/node does not "+
			"mention them and nothing that reads it can find them:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}
