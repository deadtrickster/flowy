package main

import (
	"net/http"
	"sort"
	"strings"
)

// A door that reads a parameter it does not know about answers with more than
// was asked for, and says nothing about it. api.go's listParams closed that on
// GET /api/artifacts. This closes it on every other door, because one hardened
// door is not one seventh of a fix - it is worse than none.
//
// Measured on the deployed node 0.8.0+ba60dee, one bogus parameter per list:
//
//	/api/artifacts      400  names the parameter
//	/api/merge-queue    200  ignored
//	/api/presence       200  ignored
//	/api/projects       200  ignored
//	/api/inbox          200  ignored, 244K of rows
//	/api/search?q=x     200  ignored, 118K of rows
//	/api/announcements  200  ignored
//
// A caller who learns that /api/artifacts refuses a typo will reasonably assume
// the rest do. Six doors that quietly accept anything are six places where a
// misspelt filter returns a plausible answer forever, and the caller's only
// clue is a row count they have no independent way to check.
//
// So the guard is DENY BY DEFAULT and lives in one place. Adding a parameter to
// a handler without adding it here is a 400 on the first call rather than a
// filter that silently does nothing - the same trade listParams already makes,
// applied node-wide.
//
// EVERY GUARDED ROUTE IS LISTED, INCLUDING THE ONES THAT TAKE NOTHING. That is
// what the empty braces are: "asked, and the answer is none". Deny-by-default
// makes an absent route behave identically to one declared empty, which is
// exactly why the absence has to be impossible - otherwise the table cannot
// distinguish a route somebody considered from a route nobody has looked at
// since it was written, and both read as policy. The 17 routes that were absent
// when this was made complete had all been the second kind.
//
// TestEveryGuardedRouteDeclaresItsParams keeps it complete, and asks the router
// rather than this file for the list.
var routeParams = map[string][]string{
	"DELETE /api/artifact/{id}/origins/{origin}": {},
	"DELETE /api/chat/{room}/pin/{id}":           {},
	"DELETE /api/inbox/reader/{name}":            {},
	"DELETE /api/todo/{id}/deps/{blocker}":       {},
	"GET /api/activity":                          {"kind", "limit", "order", "q", "room", "scope", "since", "thread"},
	"GET /api/announcement/{id}/quiesce":         {},
	"GET /api/announcements":                     {"scope", "status"},
	"GET /api/artifact/{id}":                     {"scope"},
	"GET /api/artifact/{id}/history":             {"scope"},
	"GET /api/artifact/{id}/origins":             {},
	"GET /api/artifacts":                         {"assignee", "category", "kind", "limit", "project", "room", "scope", "status", "tag", "type"},
	"GET /api/attachment/{id}":                   {},
	"GET /api/chat/{room}":                       {"before", "limit", "order", "since", "thread"},
	"GET /api/chat/{room}/pins":                  {},
	"GET /api/chat/{room}/wait":                  {"cursor", "limit", "thread", "window"},
	"GET /api/dm":                                {"limit", "since", "thread"},
	"GET /api/dm/wait":                           {"cursor", "limit", "thread", "window"},
	"GET /api/events":                            {"limit", "room", "scope", "since", "thread", "type"},
	"GET /api/finding/{id}/evidence":             {},
	"GET /api/finding/{id}/runs":                 {},
	"GET /api/forge":                             {},
	"GET /api/forge/mock/issue":                  {"number", "repo"},
	"GET /api/forge/status":                      {"artifact"},
	"GET /api/instructions":                      {},
	"POST /api/instructions":                     {},
	"GET /api/inbox":                             {"limit", "room", "scope", "since"},
	"GET /api/inbox/readers":                     {},
	"GET /api/inbox/tasks":                       {"limit", "state"},
	"GET /api/inbox/unread":                      {"as", "room"},
	"GET /api/inbox/wait":                        {"addressed", "as", "kind", "limit", "room", "window"},
	"GET /api/lock":                              {"target"},
	"POST /api/artifact/{id}/origins":            {},
	"POST /api/lock":                             {},
	"POST /api/lock/release":                     {},
	"GET /api/me":                                {},
	"PUT /api/me":                                {},
	"GET /api/merge-queue":                       {"limit", "project", "room", "scope", "target", "target_tip"},
	"GET /api/metrics":                           {"scope"},
	"GET /api/node":                              {},
	"GET /api/peers":                             {},
	"GET /api/presence":                          {},
	"GET /api/projects":                          {"scope"},
	"GET /api/proposal/{id}":                     {"scope"},
	"GET /api/proposals":                         {"limit", "room", "scope", "status"},
	"GET /api/ready":                             {"limit", "ready", "room", "scope"},
	"GET /api/repro/healthz":                     {},
	"GET /api/repro/package":                     {"finding", "version"},
	"GET /api/repro/run/{id}/log":                {},
	"GET /api/repro/runs":                        {"finding"},
	"GET /api/repro/version":                     {"project", "v"},
	"POST /api/repro/run":                        {},
	"GET /api/rooms":                             {},
	"GET /api/search":                            {"kind", "limit", "project", "q", "scope", "status", "type"},
	"GET /api/stream":                            {"since", "topics"},
	"GET /api/sync/pull":                         {"limit", "since"},
	"GET /api/task/{id}":                         {},
	"GET /api/todo/{id}/assignee":                {},
	"GET /api/todo/{id}/category":                {},
	"GET /api/todo/{id}/deps":                    {},
	"GET /api/todo/{id}/edits":                   {},
	"GET /api/todo/{id}/notes":                   {},
	"GET /api/trace/{id}":                        {"scope"},
	"GET /api/traces":                            {"limit", "scope", "since"},
	"GET /api/whoami":                            {},
	"POST /api/activity":                         {},
	"POST /api/announcement/{id}/ack":            {},
	"POST /api/announcement/{id}/resolve":        {},
	"POST /api/announcements":                    {},
	"POST /api/artifact/{id}/delete":             {},
	"POST /api/artifact/{id}/status":             {},
	"POST /api/artifacts":                        {},
	"POST /api/assign":                           {},
	"POST /api/chat/{room}/pin":                  {},
	"POST /api/chat/{room}/say":                  {},
	"POST /api/chat/{room}/todo":                 {},
	"POST /api/chat/{room}/todo/{id}/assignee":   {},
	"POST /api/dm/{to}":                          {},
	"POST /api/events":                           {},
	"POST /api/finding/{id}/evidence":            {},
	"POST /api/forge/file":                       {},
	"POST /api/forge/mock/comment":               {},
	"POST /api/forge/mock/fail":                  {},
	"POST /api/forge/mock/login":                 {},
	"POST /api/forge/mock/on-file":               {},
	"POST /api/forge/mock/state":                 {},
	"POST /api/forge/sync":                       {},
	"POST /api/grants":                           {},
	"POST /api/inbox/ack":                        {},
	"POST /api/inbox/reader":                     {},
	"POST /api/join/{id}/approve":                {},
	"POST /api/join/{id}/refuse":                 {},
	"POST /api/merge/{id}/abandon":               {},
	"POST /api/merge/{id}/gate":                  {},
	"POST /api/merge/{id}/land":                  {},
	"POST /api/projects":                         {},
	"POST /api/quiesce/hold":                     {},
	"POST /api/quiesce/release":                  {},
	"POST /api/rooms":                            {},
	"POST /api/rooms/{room}/invite":              {},
	"POST /api/rooms/{room}/leave":               {},
	"POST /api/sync/push":                        {},
	"POST /api/task/{id}/delegate":               {},
	"POST /api/task/{id}/state":                  {},
	"POST /api/todo/{id}/assignee":               {},
	"POST /api/todo/{id}/category":               {},
	"POST /api/todo/{id}/deps":                   {},
	"POST /api/todo/{id}/edit":                   {},
	"POST /api/todo/{id}/note":                   {},
	"POST /api/user/{id}/role":                   {},
	"POST /api/work/{id}/claim":                  {},
	"POST /api/work/{id}/done":                   {},
	"POST /api/work/{id}/release":                {},
	"POST /api/worklog":                          {},
	"PUT /api/me/auto_delegate":                  {},
}

// paramGuard refuses any query parameter the matched route does not declare.
//
// It resolves the route through the api mux itself rather than by matching
// paths here, so the guard and the router can never disagree about which
// handler a URL reaches. A request that matches no route falls through
// untouched: its answer is a 404, and a 400 about parameters would be a worse
// description of what is wrong.
func paramGuard(api *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if len(q) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		_, pattern := api.Handler(r)
		// A PATTERN WITHOUT A METHOD IS A PREFIX CATCH-ALL, NOT A ROUTE.
		//
		// api registers "/api/" as its 404 handler, so every unknown path under
		// /api/ resolves to a pattern and stops looking unmatched. The guard
		// then judged the catch-all's parameter contract - which is none - and
		// answered 400 about a query string when the caller's actual problem
		// was the path. Measured on the deployed node 0.8.0+1db9b83:
		//
		//	/api/nothing-here          404  no such endpoint
		//	/api/nothing-here?bogus=1  400  "/api/ does not honour ... \"bogus\""
		//
		// naming a pattern the caller never wrote and cannot act on. The test
		// that was supposed to cover this used a fixture mux with no catch-all,
		// so it asserted the behaviour of a router the node does not run.
		if pattern == "" || !strings.Contains(pattern, " ") {
			next.ServeHTTP(w, r)
			return
		}
		known := map[string]bool{}
		for _, name := range routeParams[pattern] {
			known[name] = true
		}
		if msg := refuseUnknownRouteParams(q, known, pattern); msg != "" {
			writeJSON(w, http.StatusBadRequest, errorBody(msg))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refuseUnknownRouteParams names what was refused and what the route does take,
// or "" when there is nothing to refuse.
//
// It names them because the typo this exists for - `tags` for `tag` - is one
// letter, and "bad request" leaves the caller re-reading their own URL for it.
// A route that takes nothing says so outright: "takes no query parameters" is a
// fact the caller can act on, where an empty list of accepted names reads like
// the server failed to answer.
func refuseUnknownRouteParams(q map[string][]string, known map[string]bool, pattern string) string {
	unknown := []string{}
	for name := range q {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	sort.Strings(unknown)
	what := "query parameter"
	if len(unknown) > 1 {
		what = "query parameters"
	}
	msg := pattern + " does not honour the " + what + " " +
		strings.Join(quoted(unknown), ", ") +
		", and a parameter that is not read would answer with more than was asked for. "
	if len(known) == 0 {
		return msg + "It takes no query parameters"
	}
	accepted := make([]string, 0, len(known))
	for name := range known {
		accepted = append(accepted, name)
	}
	sort.Strings(accepted)
	return msg + "It takes " + strings.Join(quoted(accepted), ", ")
}

// recordingMux is an http.ServeMux that remembers what was registered on it.
//
// It exists so the completeness check below can ask the ROUTER what routes the
// guard governs, instead of reading serve.go and matching the shape of a
// registration call. That distinction is the whole point: the mock forge block
// registers through a local `mock` helper rather than by calling api.HandleFunc
// directly, so a check that greps for the call shape misses six routes and
// reports the table complete. The next helper somebody writes would be missed
// the same way, and silently - the check would go on passing.
//
// Only the guarded mux is wrapped. POST /api/join is registered on the OUTER
// mux, deliberately outside the token and outside this guard, so it is not
// governed here and does not belong in the table.
type recordingMux struct {
	*http.ServeMux
	patterns []string
}

func (m *recordingMux) HandleFunc(pattern string, h http.HandlerFunc) {
	m.patterns = append(m.patterns, pattern)
	m.ServeMux.HandleFunc(pattern, h)
}

func (m *recordingMux) Handle(pattern string, h http.Handler) {
	m.patterns = append(m.patterns, pattern)
	m.ServeMux.Handle(pattern, h)
}
