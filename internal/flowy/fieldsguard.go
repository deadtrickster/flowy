package flowy

// WHAT EVERY ROUTE MAY DO TO A CHANGE'S LIFECYCLE STATE, which is the
// property the door-only setter stands on.
//
// A change's state (fields.openspec.state) is the transition door's to write.
// Every other write path preserves the held row's state at the store funnel
// (carryOpenspecState, asked of the same three statements checkOpenspecRow
// guards), so a state that changed without a transition event behind it would
// mean a write went around the door - and the tests that keep this table true
// are the arm that would catch the hole BY EXISTING.
//
// The words:
//
//   - transitions: the one door that moves the state. It writes the row and
//     the openspec.transition event in one transaction, and the event type is
//     minted so no other route can forge the trail.
//   - preserves: can rewrite a change's row, and the funnel carries the state
//     from the held row. The status door is in this class on a refusal - it
//     turns a change away (a memory has no issue lifecycle), which is how its
//     preservation happens. The validate door rewrites the row to cache its
//     verdict, and the funnel carries the state on that edit like any other.
//   - replicates: POST /api/sync/push. Incoming rows are owner-signed - the
//     verifier checks canonicalArtifact and the authentic signature before the
//     apply - so a state arriving here was written by the row's owner on
//     another node, through that node's door. That is replication, not a
//     local write path.
//   - tombstones: the row is gone after it, so its state is moot - deletion
//     is its own question, and archive gating is p6's, not this slice's.
//   - na: the route cannot touch a change row's fields at all - it writes
//     other kinds, or events, or rows of another type, and its doors refuse a
//     change wherever one can be named.
//
// THE TABLE IS THE CONTRACT AND THE TEST IS THE WITNESS. TestEveryRoute...
// walks serve.go the way serve_routes_test.go does: a route that is not here
// fails by existing, whichever word it should carry - including a future
// fields-writing door somebody forgets to declare. And a stale entry for a
// route that no longer exists fails too: a declaration about nothing is a
// lie with a coat on.
var openspecStateReach = map[string]string{
	"DELETE /api/artifact/{id}/origins/{origin}": "na",
	"DELETE /api/bookmark/{id}":                  "na",
	"DELETE /api/chat/{room}/pin/{id}":           "na",
	"DELETE /api/inbox/reader/{name}":            "na",
	"DELETE /api/schedules/{signal}":             "na",
	"DELETE /api/thread-unfolded/{id}":           "na",
	"DELETE /api/todo/{id}/deps/{blocker}":       "na",

	"GET /api/activity":                  "na",
	"GET /api/announcement/{id}/quiesce": "na",
	"GET /api/announcements":             "na",
	"GET /api/artifact/{id}":             "na",
	"GET /api/artifact/{id}/history":     "na",
	"GET /api/artifact/{id}/origins":     "na",
	"GET /api/artifacts":                 "na",
	"GET /api/attachment/{id}":           "na",
	"GET /api/bookmarks":                 "na",
	"GET /api/chat/{room}":               "na",
	"GET /api/chat/{room}/pins":          "na",
	"GET /api/chat/{room}/wait":          "na",
	"GET /api/dm":                        "na",
	"GET /api/dm/wait":                   "na",
	"GET /api/events":                    "na",
	"GET /api/finding/{id}/evidence":     "na",
	"GET /api/finding/{id}/runs":         "na",
	"GET /api/finding/{id}/upstream":     "na",
	"GET /api/forge":                     "na",
	"GET /api/forge/status":              "na",
	"GET /api/inbox":                     "na",
	"GET /api/inbox/readers":             "na",
	"GET /api/inbox/tasks":               "na",
	"GET /api/inbox/unread":              "na",
	"GET /api/inbox/wait":                "na",
	"GET /api/instructions":              "na",
	"GET /api/lock":                      "na",
	"GET /api/me":                        "na",
	"GET /api/merge/{id}/admissible":     "na",
	"GET /api/merge-queue":               "na",
	"GET /api/merge-queue/wait":          "na",
	"GET /api/metrics":                   "na",
	"GET /api/metrics/rows":              "na",
	"GET /api/metrics/series":            "na",
	"GET /api/logs/tail":                 "na",
	"GET /api/stacktraces":               "na",
	"GET /api/nag":                       "na",
	"GET /api/nag/wait":                  "na",
	"GET /api/node":                      "na",
	"GET /api/openspec":                  "na",
	"GET /api/openspec/{id}/conflicts":   "na",
	"GET /api/openspec/{id}/todos":       "na",
	"GET /api/peers":                     "na",
	"GET /api/presence":                  "na",
	"GET /api/projects":                  "na",
	"GET /api/proposal/{id}":             "na",
	"GET /api/proposals":                 "na",
	"GET /api/repro/healthz":             "na",
	"GET /api/repro/package":             "na",
	"GET /api/repro/run/{id}/log":        "na",
	"GET /api/repro/runs":                "na",
	"GET /api/repro/version":             "na",
	"GET /api/repudiations":              "na",
	"GET /api/rooms":                     "na",
	"GET /api/schedules":                 "na",
	"GET /api/schedules/resolved":        "na",
	"GET /api/search":                    "na",
	"GET /api/stream":                    "na",
	"GET /api/sync/pull":                 "na",
	"GET /api/task/{id}":                 "na",
	"GET /api/threads-unfolded":          "na",
	"GET /api/todo/{id}/assignee":        "na",
	"GET /api/todo/{id}/category":        "na",
	"GET /api/todo/{id}/deps":            "na",
	"GET /api/todo/{id}/edits":           "na",
	"GET /api/todo/{id}/notes":           "na",
	"GET /api/trace/{id}":                "na",
	"GET /api/traces":                    "na",
	"GET /api/vm/list":                   "na",
	"GET /api/vm/{name}/log":             "na",
	"GET /api/vm/projects":               "na",
	"GET /api/whoami":                    "na",

	"POST /api/activity":                       "na",
	"POST /api/agent/{id}/projects":            "na",
	"POST /api/announcement/{id}/ack":          "na",
	"POST /api/announcement/{id}/resolve":      "na",
	"POST /api/announcements":                  "na",
	"POST /api/artifact/{id}/delete":           "tombstones",
	"POST /api/artifact/{id}/origins":          "na",
	"POST /api/artifact/{id}/status":           "preserves",
	"POST /api/artifacts":                      "preserves",
	"POST /api/assign":                         "na",
	"POST /api/attachment":                     "na",
	"POST /api/bookmark":                       "na",
	"POST /api/chat/{room}/pin":                "na",
	"POST /api/chat/{room}/react":              "na",
	"POST /api/chat/{room}/say":                "na",
	"POST /api/chat/{room}/todo":               "na",
	"POST /api/chat/{room}/todo/{id}/assignee": "na",
	"POST /api/dm/{to}":                        "na",
	"POST /api/events":                         "na",
	"POST /api/finding/{id}/evidence":          "na",
	"POST /api/finding/{id}/upstream":          "na",
	"POST /api/forge/file":                     "na",
	"POST /api/forge/sync":                     "na",
	"POST /api/grants":                         "na",
	"POST /api/inbox/ack":                      "na",
	"POST /api/inbox/reader":                   "na",
	"POST /api/instructions":                   "na",
	"POST /api/join/{id}/approve":              "na",
	"POST /api/join/{id}/refuse":               "na",
	"POST /api/lock":                           "na",
	"POST /api/lock/release":                   "na",
	"POST /api/merge/{id}/abandon":             "na",
	"POST /api/merge/{id}/blocked":             "na",
	"POST /api/merge/{id}/gate":                "na",
	"POST /api/merge/{id}/land":                "na",
	"POST /api/merge/{id}/renew":               "na",
	"POST /api/merge/{id}/unblocked":           "na",
	"POST /api/openspec":                       "preserves",
	"POST /api/openspec/{id}/transition":       "transitions",
	"POST /api/openspec/{id}/validate":         "preserves",
	"POST /api/projects":                       "na",
	"POST /api/projects/{project}/enter":       "na",
	"POST /api/projects/{project}/members":     "na",
	"POST /api/quiesce/hold":                   "na",
	"POST /api/quiesce/release":                "na",
	"POST /api/repro/run":                      "na",
	"POST /api/rooms":                          "na",
	"POST /api/rooms/{room}/invite":            "na",
	"POST /api/rooms/{room}/leave":             "na",
	"POST /api/sync/push":                      "replicates",
	"POST /api/task/{id}/delegate":             "na",
	"POST /api/task/{id}/state":                "na",
	"POST /api/thread-unfolded":                "na",
	"POST /api/todo/{id}/assignee":             "na",
	"POST /api/todo/{id}/category":             "na",
	"POST /api/todo/{id}/deps":                 "na",
	"POST /api/todo/{id}/edit":                 "na",
	"POST /api/todo/{id}/note":                 "na",
	"POST /api/todo/{id}/priority":             "na",
	"POST /api/todo/{id}/waiting-on":           "na",
	"POST /api/user/{id}/role":                 "na",
	"POST /api/vm/{name}/down":                 "na",
	"POST /api/vm/{name}/say":                  "na",
	"POST /api/vm/spawn":                       "na",
	"POST /api/work/{id}/claim":                "na",
	"POST /api/work/{id}/done":                 "na",
	"POST /api/work/{id}/release":              "na",
	"POST /api/worklog":                        "na",

	"PUT /api/me":               "na",
	"PUT /api/me/auto_delegate": "na",
	"PUT /api/schedules":        "na",
}
