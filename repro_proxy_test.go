package main

// WHAT THE NODE FORWARDS, AND WHAT IT WILL NOT.
//
// The runner here is an httptest server: this is about the door, not about
// Docker. What it has to establish is that the console can reach the runner
// without holding its address, and that the node has not become a way to reach
// anything else on a host that holds a Docker socket.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRunner records what reached it and answers with something recognisable.
type fakeRunner struct {
	server *httptest.Server
	path   string
	query  string
	method string
	auth   string
	body   string
}

func newFakeRunner(t *testing.T) *fakeRunner {
	t.Helper()
	f := &fakeRunner{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.path, f.query, f.method = r.URL.Path, r.URL.RawQuery, r.Method
		f.auth = r.Header.Get("Authorization")
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			f.body = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"from":"the runner"}`)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// through calls the proxy the way the console does, and hands back what the
// caller would see.
func through(t *testing.T, s *server, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, reader)
	r.Header.Set("Authorization", "Bearer the-callers-own")
	w := httptest.NewRecorder()
	s.handleRepro(w, r)
	return w
}

func TestTheProxyCarriesTheCallersTokenAndNothingOfTheNodes(t *testing.T) {
	runner := newFakeRunner(t)
	s := &server{reproBase: runner.server.URL}

	w := through(t, s, "POST", "/api/repro/run", `{"finding":"01F","version":"latest"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/repro/run answered %d: %s", w.Code, w.Body.String())
	}
	if runner.path != "/run" || runner.method != "POST" {
		t.Errorf("the runner saw %s %s", runner.method, runner.path)
	}
	if runner.auth != "Bearer the-callers-own" {
		t.Errorf("the runner saw Authorization %q - the caller's own token is what makes a run "+
			"belong to whoever asked for it", runner.auth)
	}
	if !strings.Contains(runner.body, "01F") {
		t.Errorf("the body did not arrive: %q", runner.body)
	}
	if !strings.Contains(w.Body.String(), "the runner") {
		t.Errorf("the runner's answer did not come back: %s", w.Body.String())
	}
}

func TestTheProxyPassesOnlyTheParametersItNames(t *testing.T) {
	runner := newFakeRunner(t)
	s := &server{reproBase: runner.server.URL}

	// finding is named; project is not. A proxy that forwarded the query blind
	// would hand the runner's door a parameter nobody there expects from a
	// caller, which is the shape of every filter defect this fleet has hit.
	w := through(t, s, "GET", "/api/repro/runs?finding=01F&project=../../etc", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/repro/runs answered %d", w.Code)
	}
	if runner.query != "finding=01F" {
		t.Errorf("the runner saw query %q, and only finding was declared", runner.query)
	}
}

func TestThePathParameterIsAPathParameterAndNotAPath(t *testing.T) {
	runner := newFakeRunner(t)
	s := &server{reproBase: runner.server.URL}

	if w := through(t, s, "GET", "/api/repro/run/17/log", ""); w.Code != http.StatusOK {
		t.Fatalf("a run log answered %d", w.Code)
	}
	if runner.path != "/run/17/log" {
		t.Errorf("the runner saw %q", runner.path)
	}
}

func TestTheProxyIsAnAllowlistAndNotATunnel(t *testing.T) {
	runner := newFakeRunner(t)
	s := &server{reproBase: runner.server.URL}

	for _, call := range []struct{ method, path string }{
		{"GET", "/api/repro/anything"},
		{"POST", "/api/repro/runs"},
		{"GET", "/api/repro/run/17/kill"},
		{"DELETE", "/api/repro/run"},
	} {
		w := through(t, s, call.method, call.path, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s answered %d, not 404 - this node holds the address of a host with "+
				"Docker on it, and a door it will forward blind is that host's problem",
				call.method, call.path, w.Code)
		}
	}
	if runner.path != "" {
		t.Errorf("a refused call still reached the runner at %q", runner.path)
	}
}

func TestNoRunnerConfiguredSaysSo(t *testing.T) {
	s := &server{}
	w := through(t, s, "GET", "/api/repro/healthz", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("with no runner configured the door answered %d, not 503", w.Code)
	}
	// The panel draws "this deployment runs no repros" differently from "the
	// runner is broken", and it can only do that if the answer says which.
	if !strings.Contains(w.Body.String(), "-repro") {
		t.Errorf("the refusal does not name what an operator would have to do: %s", w.Body.String())
	}
}
