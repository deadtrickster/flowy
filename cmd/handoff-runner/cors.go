package main

import (
	"net/http"
	"strings"
)

// The runner is reached from a browser, and a browser on a different origin.
//
// THIS IS NOT A NICETY, IT IS THE DIFFERENCE BETWEEN THE PANEL WORKING AND
// NOT. The console is served by the Flowy node - one origin - and this
// process listens on another, because it is a separate deployable on purpose
// (see routes()'s head comment). Every call web/src/lib/api.ts makes to it is
// therefore cross-origin, and a cross-origin call is subject to two rules
// this file exists to satisfy:
//
//   - POST /run sends a JSON content type and an Authorization header, so it
//     is NOT a "simple" request: the browser sends OPTIONS first and will not
//     send the real request unless that preflight is answered. This mux has
//     no OPTIONS route, so the preflight used to 405 and the run button was
//     dead - it fired, nothing left the browser, and the panel showed a
//     failure to fetch with no status on it.
//   - Even the plain reads are refused to the SCRIPT unless the response
//     names the caller's origin. The request reaches this process and is
//     answered; the browser then discards the answer. So the runner's own log
//     shows 200s while the panel shows nothing, which is the most misleading
//     shape a failure can take.
//
// And one that is easy to miss: a cross-origin response exposes only a short
// list of headers to script, which does not include Content-Disposition. The
// package download reads the filename off it, so without the exposure below
// every package saves under the fallback name.
//
// Nothing here weakens the door. Every route but /healthz still needs a
// bearer token that resolves to a principal in the store, and CORS decides
// only whether a browser will hand the ANSWER to the page that asked. A page
// with no token gets a 401 whatever this file says.

// corsHeaders is the whole policy, in one place so that the preflight and the
// real request cannot answer differently - a preflight that allows a header
// the real response then rejects is a failure that only shows up on the
// second call.
const (
	corsMethods = "GET, POST, OPTIONS"
	// Authorization is the one that matters: it is why the preflight exists
	// at all, and a list that omitted it would pass a test written against
	// the response's presence rather than its content.
	corsHeaders = "Authorization, Content-Type"
	// Content-Disposition is not in the CORS-safelisted response headers, and
	// api.ts's reproPackage reads the package's filename off it.
	corsExpose = "Content-Disposition"
	// Ten minutes: long enough that a session of clicking run does not
	// preflight every time, short enough that changing console_origins is
	// felt within a coffee break rather than a browser restart.
	corsMaxAge = "600"
)

// allowedOrigin answers which origin to echo back, and false when the caller's
// origin is not one this runner serves.
//
// It ECHOES rather than answering "*" even when the config says "*", so that a
// narrowed list and an open one behave identically in every other respect -
// and because "*" is the one value that cannot be used with credentials, which
// a later change might want. Vary: Origin goes with it, always, for the same
// reason: the answer depends on the request header, and a cache that did not
// know that would serve one origin's answer to another.
func (c *Config) allowedOrigin(origin string) (string, bool) {
	if origin == "" {
		return "", false
	}
	for _, allowed := range c.ConsoleOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return origin, true
		}
	}
	return "", false
}

// cors answers preflights and marks every other response with what the
// browser needs to hand it to the page.
//
// A request with no Origin header is left completely alone: that is curl, the
// smoke command, a health check, and adding headers to those would be noise
// that reads as configuration.
func (s *service) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		allowed, ok := s.cfg.allowedOrigin(origin)

		// A preflight is answered here and never reaches the mux: it carries
		// no credential by design - the browser strips them - so passing it
		// to authed() would refuse every preflight as unauthenticated and
		// the real request would never be sent.
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if !ok {
				// Refused BY STATUS, so that this shows in the runner's log
				// as a decision rather than as a 204 nobody can tell from an
				// allowed one. The browser blocks the call either way; the
				// operator reading the log is who this is for.
				writeFault(w, r, refuse(http.StatusForbidden,
					"origin "+origin+" is not in console_origins"))
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Allow-Methods", corsMethods)
			w.Header().Set("Access-Control-Allow-Headers", corsHeaders)
			w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if ok {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Set("Access-Control-Expose-Headers", corsExpose)
		}
		next.ServeHTTP(w, r)
	})
}
