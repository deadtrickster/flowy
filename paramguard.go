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
// So the guard is DENY BY DEFAULT and lives in one place. A route that takes no
// parameters needs no entry and refuses all of them; a route that takes some
// declares them here, next to every other route's, where the whole policy can be
// read at once. Adding a parameter to a handler without adding it here is a 400
// on the first call rather than a filter that silently does nothing - the same
// trade listParams already makes, applied node-wide.
var routeParams = map[string][]string{
	"GET /api/activity":              {"kind", "limit", "order", "q", "room", "scope", "since", "thread"},
	"GET /api/announcements":         {"scope", "status"},
	"GET /api/artifact/{id}":         {"scope"},
	"GET /api/artifact/{id}/history": {"scope"},
	"GET /api/artifacts":             {"category", "kind", "limit", "project", "room", "scope", "status", "tag", "type"},
	"GET /api/chat/{room}":           {"before", "limit", "order", "since", "thread"},
	"GET /api/chat/{room}/wait":      {"cursor", "limit", "thread", "window"},
	"GET /api/dm":                    {"limit", "since", "thread"},
	"GET /api/dm/wait":               {"cursor", "limit", "thread", "window"},
	"GET /api/events":                {"limit", "room", "scope", "since", "thread", "type"},
	"GET /api/forge/status":          {"artifact"},
	"GET /api/inbox":                 {"limit", "room", "scope", "since"},
	"GET /api/inbox/tasks":           {"limit", "state"},
	"GET /api/inbox/unread":          {"as", "room"},
	"GET /api/inbox/wait":            {"addressed", "as", "kind", "limit", "room", "window"},
	"GET /api/merge-queue":           {"limit", "project", "room", "scope", "target", "target_tip"},
	"GET /api/metrics":               {"scope"},
	"GET /api/projects":              {"scope"},
	"GET /api/proposal/{id}":         {"scope"},
	"GET /api/proposals":             {"limit", "room", "scope", "status"},
	"GET /api/ready":                 {"limit", "ready", "room", "scope"},
	"GET /api/search":                {"kind", "limit", "project", "q", "scope", "status", "type"},
	"GET /api/sync/pull":             {"limit", "since"},
	"GET /api/trace/{id}":            {"scope"},
	"GET /api/traces":                {"limit", "scope", "since"},
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
		if pattern == "" {
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
