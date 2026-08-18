package main

// THE RUNNER, FROM THE CONSOLE'S SIDE OF THE WALL.
//
//	POST /api/repro/run            {finding, version}
//	GET  /api/repro/runs           ?finding=
//	GET  /api/repro/run/{id}/log
//	GET  /api/repro/version        ?project=&v=
//	GET  /api/repro/package        ?finding=&version=
//	GET  /api/repro/healthz
//
// The operator's words, 2026-08-18: "i dont want to type address of the runner,
// that python console had none", and then "there shouldnt be any runner - the
// console should do it."
//
// The second one cannot be had literally and the first one is the real ask. A
// browser cannot run Docker, so something on a machine has to, and that
// something is deliberately not this process: cmd/handoff-runner holds a Docker
// daemon, a source checkout and enough disk to compile a database, while the
// node is meant to run with none of them - see that command's head comment,
// where the boundary is a test rather than a comment.
//
// What the operator should never see is the seam. Before this, the console held
// the runner's address in localStorage and every repro call went from the
// browser to a SECOND origin - which meant a box to fill in, a CORS
// configuration to get right on the runner, and a console that silently did
// nothing on any deployment where nobody had typed one.
//
// So the node forwards. One origin, one token, one door. The address becomes
// what it always was - a fact about this machine, like the operator id and the
// forge repositories beside it - and is configured here rather than in a
// browser.
//
// WHAT IS FORWARDED AND WHAT IS NOT. An allowlist, not a blanket proxy: this
// process would otherwise be a way to reach any path on a trusted host that
// holds Docker, which is the one thing the two-binary split exists to prevent.
// Six routes, each one the panel actually calls.
//
// THE CALLER'S OWN TOKEN goes through unchanged, and nothing of this node's is
// added. The runner resolves it against the same store and answers for that
// principal - so a reader who cannot see a finding cannot run it either, and
// the answer is the same 404 it would get here. A proxy that authenticated as
// the node would quietly widen every one of those checks.

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// reproTimeout bounds a forwarded call. Generous because building a package
// renders a docker-compose tree and can take a moment, and short of the
// runner's own build timeout because a run is QUEUED by POST /run rather than
// performed by it - nothing here waits for a repro to finish.
const reproTimeout = 2 * time.Minute

// reproClient is the one client this file uses. Its timeout is the whole
// request, which is what makes a runner that accepted a connection and then
// stopped answering a 504 here rather than a request this node holds open.
var reproClient = &http.Client{Timeout: reproTimeout}

// reproRoute is one forwarded door: the method and path on this node, the path
// on the runner, and which query parameters travel with it.
//
// The query is an allowlist for the same reason the routes are: a parameter
// this node passes through blind is a parameter the runner's own door has to
// defend against a caller it did not expect, and "finding" and "version" are
// the whole vocabulary the panel speaks.
type reproRoute struct {
	method string
	target string
	query  []string
}

// reproRouteFor reads the door out of the request itself rather than out of
// r.Pattern, which needs a newer Go than this module is built with. The path
// under /api/repro/ is a closed set, so this is a switch and not a parser.
func reproRouteFor(r *http.Request) (reproRoute, string, bool) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/repro/")
	parts := strings.Split(rest, "/")
	switch {
	case r.Method == "POST" && rest == "run":
		return reproRoute{method: "POST", target: "/run"}, "", true
	case r.Method == "GET" && rest == "runs":
		return reproRoute{method: "GET", target: "/runs", query: []string{"finding"}}, "", true
	case r.Method == "GET" && rest == "version":
		return reproRoute{method: "GET", target: "/version", query: []string{"project", "v"}}, "", true
	case r.Method == "GET" && rest == "package":
		return reproRoute{method: "GET", target: "/package", query: []string{"finding", "version"}}, "", true
	case r.Method == "GET" && rest == "healthz":
		return reproRoute{method: "GET", target: "/healthz"}, "", true
	case r.Method == "GET" && len(parts) == 3 && parts[0] == "run" && parts[2] == "log" && parts[1] != "":
		return reproRoute{method: "GET", target: "/run/{id}/log"}, parts[1], true
	}
	return reproRoute{}, "", false
}

// handleRepro forwards one call to the runner this node was configured with.
func (s *server) handleRepro(w http.ResponseWriter, r *http.Request) {
	route, id, ok := reproRouteFor(r)
	if !ok {
		// Unreachable through the router, which only registers what the switch
		// above answers for. It is here so that adding a route to routes() and
		// forgetting to add it there fails as a refusal rather than as a
		// request to "".
		writeJSON(w, http.StatusNotFound, errorBody("no such repro door: "+r.Method+" "+r.URL.Path))
		return
	}
	if s.reproBase == "" {
		// SAID, NOT SWALLOWED. The panel draws a different thing for "this
		// deployment has no runner" than for "the runner is broken", and it can
		// only do that if the two answers differ.
		writeJSON(w, http.StatusServiceUnavailable, errorBody(
			"this node has no repro runner configured, so nothing here can run a finding - "+
				"start cmd/handoff-runner and pass its address with -repro or $FLOWY_REPRO"))
		return
	}

	target := route.target
	if id != "" {
		target = strings.ReplaceAll(target, "{id}", url.PathEscape(id))
	}
	values := url.Values{}
	for _, name := range route.query {
		if v := r.URL.Query().Get(name); v != "" {
			values.Set(name, v)
		}
	}
	dest := s.reproBase + target
	if len(values) > 0 {
		dest += "?" + values.Encode()
	}

	var body io.Reader
	if r.Body != nil {
		body = r.Body
	}
	out, err := http.NewRequestWithContext(r.Context(), route.method, dest, body)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The caller's credential and nothing of this node's. See the head comment.
	if auth := r.Header.Get("Authorization"); auth != "" {
		out.Header.Set("Authorization", auth)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		out.Header.Set("Content-Type", ct)
	}

	answer, err := reproClient.Do(out)
	if err != nil {
		// A runner that is not there is not a fault in this node, and saying so
		// as a 500 sends whoever reads it to the wrong logs.
		writeJSON(w, http.StatusBadGateway, errorBody(
			fmt.Sprintf("the repro runner at %s did not answer: %v", s.reproBase, err)))
		return
	}
	defer answer.Body.Close()

	// Content-Disposition travels because /package is a file download and the
	// name it comes back with is the finding and version it was built for.
	for _, header := range []string{"Content-Type", "Content-Disposition"} {
		if v := answer.Header.Get(header); v != "" {
			w.Header().Set(header, v)
		}
	}
	w.WriteHeader(answer.StatusCode)
	_, _ = io.Copy(w, answer.Body)
}
