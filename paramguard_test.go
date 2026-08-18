package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/forge"
)

// guardFor wires the guard the way serve.go does: around a mux, resolving the
// route through that same mux.
func guardFor(t *testing.T, patterns ...string) http.Handler {
	t.Helper()
	api := http.NewServeMux()
	for _, p := range patterns {
		api.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
	}
	return paramGuard(api, api)
}

func get(t *testing.T, h http.Handler, url string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	return w
}

// The parameter a route declares reaches the handler. Without this the guard
// could pass its other tests by refusing everything.
func TestParamGuardLetsDeclaredParamsThrough(t *testing.T) {
	old := routeParams
	routeParams = map[string][]string{"GET /api/things": {"tag", "limit"}}
	defer func() { routeParams = old }()

	h := guardFor(t, "GET /api/things")
	for _, url := range []string{"/api/things", "/api/things?tag=x", "/api/things?tag=x&limit=2"} {
		if w := get(t, h, url); w.Code != http.StatusTeapot {
			t.Fatalf("%s: got %d, want the handler to answer", url, w.Code)
		}
	}
}

// The whole point: a parameter nobody reads is refused BY NAME, because the
// typo it exists for is one letter and "bad request" leaves the caller staring
// at their own URL.
func TestParamGuardRefusesUnknownParamByName(t *testing.T) {
	old := routeParams
	routeParams = map[string][]string{"GET /api/things": {"tag"}}
	defer func() { routeParams = old }()

	w := get(t, guardFor(t, "GET /api/things"), "/api/things?tags=ragflow")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
	body := w.Body.String()
	// The body is JSON, so the names arrive quote-escaped - checking for the
	// escaped form is what proves they were quoted rather than run together
	// into the sentence.
	for _, want := range []string{`\"tags\"`, `\"tag\"`, "GET /api/things"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not name %s: %s", want, body)
		}
	}
}

// A route that declares nothing refuses everything, and says so in those words.
// This is the half that makes the guard deny-by-default: a door added next week
// that reads a parameter without declaring it 400s on the first call, instead of
// silently answering with more than was asked for.
func TestParamGuardRouteWithNoEntryTakesNothing(t *testing.T) {
	old := routeParams
	routeParams = map[string][]string{}
	defer func() { routeParams = old }()

	w := get(t, guardFor(t, "GET /api/plain"), "/api/plain?anything=1")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "takes no query parameters") {
		t.Errorf("a route that takes nothing should say so: %s", w.Body.String())
	}
}

// A path no route matches is a 404, and a 400 about parameters would describe
// the wrong thing - the caller's problem is the path, not the query string.
func TestParamGuardLeavesUnmatchedPathsAlone(t *testing.T) {
	old := routeParams
	routeParams = map[string][]string{}
	defer func() { routeParams = old }()

	w := get(t, guardFor(t, "GET /api/things"), "/api/nothing-here?bogus=1")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 - the path is what is wrong", w.Code)
	}
}

// THE TABLE AND THE ROUTER SAY THE SAME THING, in both directions.
//
// A dead entry is invisible: the route it names is gone, so nothing ever fails,
// and the next route that reuses that path inherits permissions nobody granted
// it. A MISSING entry is invisible for the opposite reason - deny-by-default
// means the route still refuses everything, so the omission has no symptom
// until somebody adds a parameter to that handler and it 400s in production
// with the handler code looking correct.
//
// Both are "the table drifted from the routes" and neither announces itself, so
// the check is an equality rather than a containment.
//
// IT ASKS THE ROUTER. The earlier version of this read serve.go and looked for
// api.HandleFunc("..."), which is a spelling and not a fact: the mock forge
// block registers through a local helper, so six routes were governed by the
// guard and invisible to the check. Reading s.apiPatterns cannot miss those,
// and cannot miss the next helper either.
func TestEveryGuardedRouteDeclaresItsParams(t *testing.T) {
	// WITH THE MOCK FORGE ON. Those six routes are registered inside an
	// `if s.mockForge != nil` block, so a bare server leaves them out and the
	// check would pass while six guarded routes went unexamined - the same
	// blindness the recorder was introduced to remove, one level up.
	s := &server{mockForge: forge.NewMockForge()}
	s.routes()

	// The witness that this measured anything. Every assertion below is over
	// s.apiPatterns, so a recorder that captured nothing would report perfect
	// agreement between two empty sets.
	if len(s.apiPatterns) < 80 {
		t.Fatalf("only %d routes were recorded - the recorder is not seeing registrations",
			len(s.apiPatterns))
	}

	registered := map[string]bool{}
	catchAll := false
	for _, pattern := range s.apiPatterns {
		// The catch-all answers unknown paths and has no query contract; the
		// guard skips it for that reason. Its presence is asserted below,
		// because the skip is only harmless while it is a 404 handler.
		if !strings.Contains(pattern, " ") {
			catchAll = true
			continue
		}
		registered[pattern] = true
		if _, ok := routeParams[pattern]; !ok {
			t.Errorf("%s is behind the guard and not in routeParams - it currently refuses every "+
				"query parameter, which may be right, but nobody has said so. Add an entry: {} "+
				"means it takes none.", pattern)
		}
	}
	for pattern := range routeParams {
		if !registered[pattern] {
			t.Errorf("routeParams declares %q, which is not registered on the guarded mux", pattern)
		}
	}
	if !catchAll {
		t.Error("no method-less pattern was registered - the skip above now covers nothing, " +
			"and if a real route ever loses its method the guard will stop governing it silently")
	}
}

// An unknown path under /api/ is a 404, EVEN THOUGH THE CATCH-ALL MATCHES IT.
//
// The fixture registers the catch-all the node registers. The earlier version
// of this test did not, so it measured a router with no catch-all: every
// unknown path there was genuinely unmatched, the guard passed it through for
// the right reason by accident, and the node shipped 400-about-parameters for
// eleven hours.
func TestParamGuardLeavesUnknownPathsToTheCatchAll(t *testing.T) {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/things", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	api.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such endpoint", http.StatusNotFound)
	})

	w := get(t, paramGuard(api, api), "/api/nothing-here?bogus=1")
	if w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 - the path is what is wrong, not the query string: %s",
			w.Code, w.Body.String())
	}
	// And the guard still guards the route beside it.
	if w := get(t, paramGuard(api, api), "/api/things?bogus=1"); w.Code != http.StatusBadRequest {
		t.Errorf("a real route stopped refusing unknown parameters: %d", w.Code)
	}
}

// routes() must reach the recorder. The check above is over what the recorder
// collected, so a paramGuard wired to a bare mux - or a recordingMux whose
// HandleFunc stopped appending - would leave every route ungoverned while the
// completeness check went on passing.
func TestGuardedMuxRecordsWhatItRegisters(t *testing.T) {
	m := &recordingMux{ServeMux: http.NewServeMux()}
	m.HandleFunc("GET /api/one", func(http.ResponseWriter, *http.Request) {})
	m.Handle("GET /api/two", http.NotFoundHandler())

	if got := strings.Join(m.patterns, ","); got != "GET /api/one,GET /api/two" {
		t.Fatalf("recorded %q", got)
	}
	// And still routes: a recorder that swallowed the registration would pass
	// the line above and serve 404s.
	w := get(t, m, "/api/one")
	if w.Code == http.StatusNotFound {
		t.Error("the recorded route did not reach its handler")
	}
}
