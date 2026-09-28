package flowy

import (
	"slices"
	"testing"
)

// TestEveryLimitRouteSaysWhetherItCuts keeps truncationSays complete against
// routeParams, which is the file that already knows which doors take a limit.
//
// Asking routeParams rather than a second list is the point: a door that gains
// `limit` gains it there, and this fails the moment it does.
func TestEveryLimitRouteSaysWhetherItCuts(t *testing.T) {
	takesLimit := map[string]bool{}
	for route, params := range routeParams {
		if slices.Contains(params, "limit") {
			takesLimit[route] = true
		}
	}

	// The witness that this measured anything. An empty routeParams, or a
	// rename of the parameter, would make two empty sets agree perfectly.
	if len(takesLimit) < 15 {
		t.Fatalf("only %d routes accept a limit - routeParams is not being read, "+
			"or the parameter was renamed and this check is now about nothing",
			len(takesLimit))
	}

	for route := range takesLimit {
		status, said := truncationSays[route]
		if !said {
			t.Errorf("%s accepts `limit` and truncationSays does not mention it.\n"+
				"A page from it is indistinguishable from a whole answer, and nobody has "+
				"decided whether that is true of this door. Add it as %q, %q or %q - "+
				"absent is not one of the three.",
				route, truncationDisclosed, truncationCannotCut, truncationNotYet)
			continue
		}
		switch status {
		case truncationDisclosed, truncationCannotCut, truncationNotYet:
		default:
			t.Errorf("%s is recorded as %q, which is not one of the three statuses",
				route, status)
		}
	}

	// And the other direction, which is the half a one-directional guard would
	// miss - see 01M3BPE2NME4H6KG76PPMR63FN, where a check that caught the docs
	// denying a real verb could not catch them offering an unreal one. A route
	// here that no longer takes a limit is a stale entry, and a stale entry is
	// how a table stops describing the thing it guards.
	for route := range truncationSays {
		if !takesLimit[route] {
			t.Errorf("truncationSays lists %s, which does not accept `limit` per routeParams.\n"+
				"Either the door dropped the parameter and this entry is stale, or the "+
				"route was renamed and the entry now guards nothing.", route)
		}
	}
}
