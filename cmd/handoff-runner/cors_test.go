package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The runner is reached from a browser on another origin, and until this file
// existed nothing in the repository said so.
//
// EVERY CLAIM HERE HAS TWO ARMS, because one reading cannot tell a policy
// being applied from a policy that does not exist. "The preflight was
// answered" passes on a handler that answers every preflight; it is the
// refusal of an origin nobody configured that says the list is consulted.
//
// No database and no Docker: s.cors reads nothing but s.cfg, so these run in a
// plain `go test ./...` rather than only under the gate. That is deliberate -
// the browser-side half (web/scripts/run-journey-check.mjs) needs a whole
// console and cannot be cheap, so the half that can be cheap is.

// corsFixture is a service with only the configuration cors() reads, wrapped
// around a handler that records having been reached. Whether the request
// REACHES the mux is half of what is under test: CORS is enforced by the
// browser, not here, so a disallowed origin's ordinary request must still be
// served - it is the answer the browser throws away, and a runner that
// refused it outright would be a different (and wrong) design that no test
// here would otherwise notice.
func corsFixture(origins ...string) (http.Handler, *bool) {
	reached := false
	svc := &service{cfg: &Config{ConsoleOrigins: origins}}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Header().Set("Content-Disposition", `attachment; filename="repro.tgz"`)
		w.WriteHeader(http.StatusOK)
	})
	return svc.cors(inner), &reached
}

func preflight(t *testing.T, h http.Handler, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, "/run", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The preflight is what makes POST /run possible at all. It carries no
// credential - the browser strips them - so it must be answered before
// authed() ever sees the request, and it must name Authorization or the real
// call is never sent.
func TestThePreflightIsAnsweredForAConfiguredOriginAndRefusedForAnother(t *testing.T) {
	h, reached := corsFixture("http://console.example:8787")

	yes := preflight(t, h, "http://console.example:8787")
	if yes.Code != http.StatusNoContent {
		t.Errorf("preflight from the configured origin answered %d, want 204", yes.Code)
	}
	if got := yes.Header().Get("Access-Control-Allow-Origin"); got != "http://console.example:8787" {
		t.Errorf("preflight allow-origin is %q, want the caller's own origin echoed", got)
	}
	if got := yes.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("preflight allow-methods is %q, and POST is the method /run needs", got)
	}
	// The one that decides whether a run can ever be asked for: the console
	// sends the reader's own bearer token, and a preflight that does not
	// allow the header means the browser never sends the request that has it.
	allowHeaders := yes.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(strings.ToLower(allowHeaders), "authorization") {
		t.Errorf("preflight allow-headers is %q and does not name Authorization, "+
			"so the browser would never send the call that carries the token", allowHeaders)
	}
	if *reached {
		t.Error("the preflight reached the mux; it must be answered before authed(), " +
			"which would refuse it as unauthenticated because a preflight carries no credential")
	}

	// THE OTHER ARM. Same request, an origin the operator did not configure.
	no := preflight(t, h, "http://somewhere.else")
	if no.Code != http.StatusForbidden {
		t.Errorf("preflight from an unconfigured origin answered %d, want 403 - "+
			"a 204 here would be indistinguishable from the allowed case in the log", no.Code)
	}
	if got := no.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("preflight from an unconfigured origin still allowed %q", got)
	}
}

// An ordinary GET, which is the case the panel spends most of its time in:
// /runs every 2.5s and /run/{id}/log every 1.2s. These are answered whatever
// the origin - the browser is what discards them - so the assertion is on the
// header that decides whether the page is handed the answer.
func TestAnOrdinaryAnswerNamesTheCallerOnlyForAConfiguredOrigin(t *testing.T) {
	h, reached := corsFixture("http://console.example:8787")

	yes := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/package", nil)
	req.Header.Set("Origin", "http://console.example:8787")
	h.ServeHTTP(yes, req)
	if got := yes.Header().Get("Access-Control-Allow-Origin"); got != "http://console.example:8787" {
		t.Errorf("answer to the configured origin allows %q, want that origin", got)
	}
	// api.ts's reproPackage reads the download's filename off
	// Content-Disposition, which is not on the CORS-safelisted response
	// header list: without this the header is present on the wire and absent
	// to the script, and every package saves under the fallback name.
	if got := yes.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Content-Disposition") {
		t.Errorf("expose-headers is %q and does not name Content-Disposition, "+
			"so a cross-origin package download loses its filename", got)
	}
	if !*reached {
		t.Error("the request never reached the handler")
	}

	// THE OTHER ARM.
	no := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/package", nil)
	req.Header.Set("Origin", "http://somewhere.else")
	h.ServeHTTP(no, req)
	if got := no.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("answer to an unconfigured origin allows %q, want nothing", got)
	}
}

// A request with no Origin is curl, the smoke command, a health probe. It gets
// no CORS headers at all, so that their presence stays a signal about a
// browser rather than decoration on every line.
func TestACallWithNoOriginIsLeftAlone(t *testing.T) {
	h, reached := corsFixture("*")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("a request with no Origin was answered with allow-origin %q", got)
	}
	if got := rec.Header().Get("Vary"); strings.Contains(got, "Origin") {
		t.Errorf("a request with no Origin varied on it: %q", got)
	}
	if !*reached {
		t.Error("the request never reached the handler")
	}
}

// The default. A deployment nobody configured has to work, because the way it
// fails is invisible: a CORS refusal is reported to the browser's own console
// and the page is told only that the fetch failed. Two arms again - the star
// allows an origin the narrowed list refuses.
func TestTheDefaultAllowsAnyConsoleAndANarrowedListDoesNot(t *testing.T) {
	cfg, err := LoadConfig(write(t, minimal), noEnv)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := cfg.allowedOrigin("http://anything.at.all:9999"); !ok {
		t.Errorf("console_origins defaulted to %v, which refuses an unconfigured console",
			cfg.ConsoleOrigins)
	}
	narrowed := &Config{ConsoleOrigins: []string{"http://console.example:8787"}}
	if _, ok := narrowed.allowedOrigin("http://anything.at.all:9999"); ok {
		t.Error("a narrowed console_origins allowed an origin that is not in it, " +
			"so the list is not consulted and the default proves nothing")
	}
	// Case, because Origin is compared as a string by the browser but hosts
	// are not case-sensitive, and an operator who typed a capital would
	// otherwise get the silent failure this whole file exists to remove.
	if _, ok := narrowed.allowedOrigin("http://CONSOLE.example:8787"); !ok {
		t.Error("a configured origin was refused for its case")
	}
}

// A near-miss origin is refused at LOAD, because it cannot be refused
// anywhere a person would see it: the mismatch shows up as a fetch that fails
// in a browser nobody is looking at, with the runner's own log showing
// nothing wrong.
func TestAnOriginWithAPathIsRefusedWhenTheConfigIsRead(t *testing.T) {
	body := strings.Replace(minimal, `"dsn"`,
		`"console_origins": ["http://console.example:8787/"], "dsn"`, 1)
	_, err := LoadConfig(write(t, body), noEnv)
	if err == nil {
		t.Fatal("a console origin with a trailing slash loaded; " +
			"a browser sends no trailing slash, so it would match nothing")
	}
	if !strings.Contains(err.Error(), "console_origins") {
		t.Errorf("the refusal does not name the key: %v", err)
	}

	// THE OTHER ARM: the same value without the slash loads, so the refusal
	// above is about the shape and not about the key being present at all.
	ok := strings.Replace(minimal, `"dsn"`,
		`"console_origins": ["http://console.example:8787"], "dsn"`, 1)
	if _, err := LoadConfig(write(t, ok), noEnv); err != nil {
		t.Fatalf("a well-formed console origin was refused: %v", err)
	}
}

// The environment's comma-separated form, which is how a deployment moves this
// without editing the file the runner reads.
func TestTheEnvironmentCarriesTheOriginList(t *testing.T) {
	env := map[string]string{
		"HANDOFF_RUNNER_CONSOLE_ORIGINS": "http://a.example, http://b.example:8787,",
	}
	cfg, err := LoadConfig(write(t, minimal), func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.ConsoleOrigins) != 2 {
		t.Fatalf("read %v from the environment, want two origins with the empty piece dropped",
			cfg.ConsoleOrigins)
	}
	if _, ok := cfg.allowedOrigin("http://b.example:8787"); !ok {
		t.Errorf("the second entry does not allow its own origin: %v", cfg.ConsoleOrigins)
	}
	if _, ok := cfg.allowedOrigin("http://c.example"); ok {
		t.Error("an origin that is in neither entry was allowed")
	}
}
