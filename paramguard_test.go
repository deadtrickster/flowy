package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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

// Every route in the table must still be registered under exactly that pattern.
// A renamed or retired route would otherwise leave a dead entry behind, and a
// dead entry is invisible: the route it names is gone, so nothing ever fails.
// The next route that reuses the path inherits permissions nobody granted it.
//
// It reads serve.go rather than the mux because http.ServeMux will not say what
// it has registered, and a pattern is exactly the string written there.
func TestRouteParamsNameRoutesThatExist(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	for pattern := range routeParams {
		if !strings.Contains(string(src), `"`+pattern+`"`) {
			t.Errorf("routeParams declares %q, which serve.go does not register", pattern)
		}
	}
}
