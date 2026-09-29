package flowy

// WHETHER A LISTING SAYS IT WAS CUT. 01M3MYAZE8809HVAF0DDG4PBHT.
//
// A door that takes `limit` can hand back a page, and a page is byte-for-byte
// indistinguishable from the whole answer. Two readers counted rows inside
// /api/artifacts?kind=metric&limit=N on 2026-09-28 and reported the count as a
// fact about a series, in opposite directions, off the same door. Neither was
// careless: there was nothing in the response to be careful about.
//
// This table says, for every route that accepts `limit`, whether its answer
// carries the disclosure - see discloseTruncation in api.go. It has NO DEFAULT
// on purpose, the same rule routeNeeds keeps: a route missing from here fails
// the test rather than being assumed either way, because "nobody decided" and
// "decided it cannot truncate" are different and only one of them is a
// decision.
//
// The statuses are closed, and the test rejects anything else:
//
//	truncationDisclosed   the answer carries `truncated` when the page filled
//	truncationCannotCut   the set is bounded by something other than `limit`,
//	                      so a full page is the whole answer and a flag would
//	                      be noise
//	truncationNotYet      it can cut and does not say so - an open instance of
//	                      the defect, named here rather than left invisible
//
// truncationNotYet is not a resting place. It is here so the gap is enumerated
// and so a NEW door cannot be added without somebody choosing; the remaining
// ones are follow-up work on the row above.
const (
	truncationDisclosed = "discloses"
	truncationCannotCut = "cannot-cut"
	truncationNotYet    = "not-yet"
)

// truncationSays maps a route to one of the three statuses above.
//
// TestEveryLimitRouteSaysWhetherItCuts keeps it complete, and asks routeParams
// rather than this file for the list of doors that take a limit.
var truncationSays = map[string]string{
	// Done in this change: the query is named, the page size that RAN is asked
	// of it, and a full page sets the flag.
	"GET /api/activity":    truncationDisclosed,
	"GET /api/artifacts":   truncationDisclosed,
	"GET /api/events":      truncationDisclosed,
	"GET /api/inbox/tasks": truncationDisclosed,
	"GET /api/openspec":    truncationDisclosed,
	"GET /api/search":      truncationDisclosed,
	"GET /api/traces":      truncationDisclosed,

	// The dashboard three. metrics/rows is hedged - it runs no count over the
	// whole set - and the other two are EXACT, because their counts are taken
	// over every matching row before the limit, so they can say "showing 400 of
	// 1913" rather than "there may be more".
	//
	// Reads whose rows come from a shared reader used by several doors, so the
	// page size that ran is not in the handler's hands yet. Untouched here
	// rather than half-done.
	"GET /api/chat/{room}":  truncationNotYet,
	"GET /api/dm":           truncationNotYet,
	"GET /api/inbox":        truncationNotYet,
	"GET /api/merge-queue":  truncationNotYet,
	"GET /api/metrics/rows": truncationDisclosed,
	"GET /api/logs/tail":    truncationDisclosed,
	"GET /api/stacktraces":  truncationDisclosed,
	"GET /api/proposals":    truncationNotYet,
	"GET /api/ready":        truncationNotYet,
	"GET /api/sync/pull":    truncationNotYet,

	// The long polls. A wait answers with what arrived inside its window, and a
	// caller that gets a full page has a cursor to carry on from - which is the
	// one case where paging forward is what the caller was already doing. Still
	// not a disclosure, and listed so that stays a decision.
	"GET /api/chat/{room}/wait": truncationNotYet,
	"GET /api/dm/wait":          truncationNotYet,
	"GET /api/inbox/wait":       truncationNotYet,
	"GET /api/merge-queue/wait": truncationNotYet,
}
