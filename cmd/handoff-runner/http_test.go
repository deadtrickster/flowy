package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/repro"
	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// discoveryMarker is planted in the finding's investigation record and
// looked for in everything this binary hands out. It is a literal string so
// that a test failure names the exact thing that leaked.
const discoveryMarker = "CANDID-NOTES-NOT-FOR-UPSTREAM"

// fixture is a runner with one project, one finding with a real repro tree,
// and two tokens: the finding's owner, and somebody else entirely.
type fixture struct {
	svc      *service
	token    string
	stranger string
	finding  string
	project  string
}

// newFixture dials the database the gate started. Without DATABASE_URL there
// is nothing to talk to, so these tests sit out a plain `go test ./...` -
// internal/store's own suite makes the same call for the same reason.
func newFixture(t *testing.T) (context.Context, *fixture) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; run ./run-tests.sh for the live checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	db, err := store.Open(ctx, dsn, "handoff-runner-test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	project := "hr-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	other := "hr-other-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: other}); err != nil {
		t.Fatalf("declare project: %v", err)
	}

	owner := &store.User{Handle: "runner-owner-" + ulid.NewString()}
	stranger := &store.User{Handle: "runner-stranger-" + ulid.NewString()}
	for _, u := range []*store.User{owner, stranger} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	ownerP := &store.Principal{Token: "tok-" + ulid.NewString(), UserID: owner.ID, Project: project}
	strangerP := &store.Principal{Token: "tok-" + ulid.NewString(), UserID: stranger.ID, Project: other}
	for _, p := range []*store.Principal{ownerP, strangerP} {
		if err := db.InsertToken(ctx, p); err != nil {
			t.Fatalf("insert token: %v", err)
		}
	}

	finding := &store.Artifact{
		Type: "finding", Project: &project, OwnerUser: owner.ID,
		Title:     "UPDATE by ctid loses rows",
		Body:      "The write-up that goes upstream.",
		Discovery: "How it was really found: " + discoveryMarker,
	}
	if err := db.UpsertArtifact(ctx, finding); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}
	if _, err := db.WriteFindingRepro(ctx, ownerP, finding.ID,
		[]store.ReproSource{{Path: "repro-01.sh", Content: []byte("#!/bin/bash\nexit 0\n")}},
		store.ReproManifest{Entrypoint: "repro-01.sh", Interp: "bash"}); err != nil {
		t.Fatalf("write repro: %v", err)
	}

	cache := t.TempDir()
	cfg := &Config{
		Addr: defaultAddr, DSN: dsn, CacheDir: cache, Workers: 1,
		Projects: map[string]Project{project: {
			// A source path that is not there on purpose: resolution fails,
			// which is a Note rather than an error, and the package is still
			// rendered against the base image. Nothing in these tests needs
			// git or a Docker daemon.
			Source:    filepath.Join(cache, "no-such-checkout"),
			BaseImage: "example/base:1.0",
		}},
	}
	cfg.fill()

	return ctx, &fixture{
		svc: &service{
			db: db, cfg: cfg, resolver: repro.NewResolver(),
			queue: unlinkedQueue{}, started: time.Now(),
		},
		token: ownerP.Token, stranger: strangerP.Token, finding: finding.ID, project: project,
	}
}

// do makes a request at the service with a token, or without one when the
// token is empty.
func (f *fixture) do(method, target, token string) *httptest.ResponseRecorder {
	return f.send(method, target, token, "{}")
}

// send is do with a body of its own, for the one route that has one.
func (f *fixture) send(method, target, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.svc.routes().ServeHTTP(w, r)
	return w
}

// TestEveryRouteRefusesACallerWithNoToken - including the two that only
// read. The Python service left /api/package and /api/version open because
// they do not write; a package is the finding's repro tree verbatim and a
// finding here can be private, so that reasoning did not survive the move.
func TestEveryRouteRefusesACallerWithNoToken(t *testing.T) {
	_, f := newFixture(t)
	for _, tc := range []struct{ method, target string }{
		{"POST", "/run"},
		{"GET", "/runs"},
		{"GET", "/run/1/log"},
		{"GET", "/package?finding=" + f.finding},
		{"GET", "/version?project=" + f.project},
	} {
		if w := f.do(tc.method, tc.target, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no token answered %d, want 401", tc.method, tc.target, w.Code)
		}
		if w := f.do(tc.method, tc.target, "tok-nobody-ever-minted"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with an unknown token answered %d, want 401", tc.method, tc.target, w.Code)
		}
	}
	// And the one door that has to answer without one, or a health check
	// reports the process down whenever the credential is wrong.
	if w := f.do("GET", "/healthz", ""); w.Code == http.StatusUnauthorized {
		t.Error("/healthz asked for a token")
	}
}

// TestAPackageCarriesTheReproTreeAndNeverTheInvestigationRecord is the
// disclosure guarantee, checked against the bytes that would actually be
// sent. A package is made to be handed to the project the finding is about;
// the investigation record is our own candid notes and must not be in it.
func TestAPackageCarriesTheReproTreeAndNeverTheInvestigationRecord(t *testing.T) {
	_, f := newFixture(t)
	w := f.do("GET", "/package?finding="+f.finding+"&version=latest", f.token)
	if w.Code != http.StatusOK {
		t.Fatalf("/package answered %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("content type was %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("a package must not be cached, got %q", cc)
	}

	files := untar(t, w.Body.Bytes())
	for _, want := range []string{"Dockerfile", "docker-compose.yml", "README.md",
		"MANIFEST.json", "repro/repro-01.sh"} {
		if _, ok := files[want]; !ok {
			t.Errorf("the package has no %s (it has %v)", want, names(files))
		}
	}
	if !strings.Contains(files["repro/repro-01.sh"], "exit 0") {
		t.Error("the repro script did not come across verbatim")
	}
	if !strings.Contains(files["README.md"], "The write-up that goes upstream.") {
		t.Error("the README does not carry the finding's write-up")
	}
	for path, body := range files {
		if strings.Contains(body, discoveryMarker) {
			t.Errorf("%s in the package carries the investigation record", path)
		}
	}
	// MANIFEST.json names the finding, so a package found on somebody's disk
	// months later can still be traced back to the row it came from.
	var manifest map[string]any
	if err := json.Unmarshal([]byte(files["MANIFEST.json"]), &manifest); err != nil {
		t.Fatalf("MANIFEST.json does not parse: %v", err)
	}
	if manifest["finding"] != f.finding {
		t.Errorf("MANIFEST.json names finding %v, want %s", manifest["finding"], f.finding)
	}
}

// TestAFindingOutOfReachIsAnsweredExactlyLikeOneThatIsNotThere. The store
// answers a read the caller may not make and a read of nothing at all the
// same way on purpose; a door that split them again would undo that, and
// would confirm the existence of every finding to anybody with a token.
func TestAFindingOutOfReachIsAnsweredExactlyLikeOneThatIsNotThere(t *testing.T) {
	_, f := newFixture(t)
	outOfReach := f.do("GET", "/package?finding="+f.finding, f.stranger)
	nonexistent := f.do("GET", "/package?finding="+ulid.NewString(), f.stranger)
	if outOfReach.Code != http.StatusNotFound || nonexistent.Code != http.StatusNotFound {
		t.Fatalf("out of reach answered %d, nonexistent answered %d, want 404 for both",
			outOfReach.Code, nonexistent.Code)
	}
	if strings.Contains(outOfReach.Body.String(), discoveryMarker) ||
		strings.Contains(outOfReach.Body.String(), "UPDATE by ctid") {
		t.Errorf("the refusal said something about the finding: %s", outOfReach.Body.String())
	}
}

// TestAVersionGitWouldReadAsAnOptionIsRefused. The resolver runs git with a
// real argv, so there is no shell to inject into - but git reads a leading
// dash as a flag, and `git rev-parse --upload-pack=...` is a different
// program from the one the resolver believes it is running.
func TestAVersionGitWouldReadAsAnOptionIsRefused(t *testing.T) {
	_, f := newFixture(t)
	for _, bad := range []string{
		"--upload-pack=/bin/sh", "-latest", "main;rm -rf /", "main branch", "..",
	} {
		w := f.do("GET", "/version?project="+f.project+"&v="+urlEscape(bad), f.token)
		if w.Code != http.StatusBadRequest {
			t.Errorf("version %q answered %d, want 400", bad, w.Code)
		}
	}
	if w := f.do("GET", "/version?project="+f.project+"&v=26.07.5", f.token); w.Code != http.StatusOK {
		t.Errorf("a release tag answered %d: %s", w.Code, w.Body.String())
	}
}

// TestAProjectThisRunnerDoesNotHoldIsNamedInTheRefusal - and the refusal
// says which ones it does hold, because the set of projects is the whole of
// what this process may touch and an operator debugging a 404 should not
// have to read the config file to find out.
func TestAProjectThisRunnerDoesNotHoldIsNamedInTheRefusal(t *testing.T) {
	_, f := newFixture(t)
	w := f.do("GET", "/version?project=not-configured", f.token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("answered %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), f.project) {
		t.Errorf("the refusal did not say what it holds: %s", w.Body.String())
	}
}

// TestTheQueueRoutesSayWhichPieceIsMissingRatherThanAnsweringEmpty. An empty
// /runs from a process that cannot run anything looks exactly like a process
// nobody has asked to do anything yet, and the difference is the question
// the operator is asking.
func TestTheQueueRoutesSayWhichPieceIsMissingRatherThanAnsweringEmpty(t *testing.T) {
	_, f := newFixture(t)
	w := f.send("POST", "/run", f.token, `{"finding":"`+f.finding+`","version":"latest"}`)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "handoffs 08") {
		t.Errorf("POST /run answered %d: %s", w.Code, w.Body.String())
	}
	// A request naming nothing is a different refusal, and it is the door's
	// own rather than the missing piece's.
	if w := f.do("POST", "/run", f.token); w.Code != http.StatusBadRequest {
		t.Errorf("POST /run naming no finding answered %d, want 400", w.Code)
	}
	// A finding out of the caller's reach never reaches the queue at all -
	// the refusal is the door's, and it comes back before the piece that is
	// missing is even consulted, which is what says the check is the door's
	// and not the runner's to make later.
	w = f.send("POST", "/run", f.stranger, `{"finding":"`+f.finding+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a run of somebody else's finding answered %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "handoffs 08") {
		t.Errorf("the queue was consulted about a finding out of reach: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no such finding") {
		t.Errorf("the refusal was not the store's own: %s", w.Body.String())
	}
	w = f.do("GET", "/run/anything/log", f.token)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /run/{id}/log answered %d: %s", w.Code, w.Body.String())
	}
	w = f.do("GET", "/runs", f.token)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /runs answered %d", w.Code)
	}
	var body struct {
		Runs   []Run `json:"runs"`
		Linked bool  `json:"linked"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("runs body: %v", err)
	}
	if body.Linked || len(body.Runs) != 0 {
		t.Errorf("an unlinked runner reported linked=%v with %d runs", body.Linked, len(body.Runs))
	}
	// /version says the same thing, so one request answers both "what does
	// this resolve to" and "can this deployment run it".
	w = f.do("GET", "/version?project="+f.project, f.token)
	var v versionAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("version body: %v", err)
	}
	if v.Runnable {
		t.Error("an unlinked runner called itself runnable")
	}
	if v.Note == "" {
		t.Error("a resolution that failed said nothing about why")
	}
}

// TestTheVersionAnswerNeverCarriesAHostPath. Version.Binary is a path on
// this machine, and this machine's filesystem is not the caller's business:
// what a caller needs is whether a binary is already there, because that is
// the difference between a run that starts now and one that compiles a
// database first.
func TestTheVersionAnswerNeverCarriesAHostPath(t *testing.T) {
	_, f := newFixture(t)
	w := f.do("GET", "/version?project="+f.project+"&v=latest", f.token)
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d: %s", w.Code, w.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, leaked := raw["binary"]; leaked {
		t.Errorf("the answer carries a host path: %v", raw)
	}
	if _, ok := raw["binary_ready"].(bool); !ok {
		t.Errorf("the answer does not say whether a binary is ready: %v", raw)
	}
	if strings.Contains(w.Body.String(), f.svc.cfg.CacheDir) {
		t.Errorf("the answer names this host's cache directory: %s", w.Body.String())
	}
}

// --------------------------------------------------------------- pure checks

// TestARunLogOutsideTheLogDirectoryIsNotServed. The path comes off this
// process's own record and never out of a request, so this cannot happen
// today - which is exactly why it is worth pinning, because "no handler here
// can serve an arbitrary file" should be true by construction rather than
// true because of what some other file happens to do.
func TestARunLogOutsideTheLogDirectoryIsNotServed(t *testing.T) {
	dir := t.TempDir()
	svc := &service{cfg: &Config{LogDir: filepath.Join(dir, "runs")}}
	if err := os.MkdirAll(svc.cfg.LogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "secret")
	if err := os.WriteFile(outside, []byte("not yours"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.openRunLog(Run{ID: "r1", LogPath: outside}); err == nil {
		t.Error("a log path outside the log directory should be refused")
	}
	if _, err := svc.openRunLog(Run{ID: "r2",
		LogPath: filepath.Join(svc.cfg.LogDir, "..", "secret")}); err == nil {
		t.Error("a log path that climbs out should be refused")
	}
	inside := filepath.Join(svc.cfg.LogDir, "run-1.log")
	if err := os.WriteFile(inside, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := svc.openRunLog(Run{ID: "r3", LogPath: inside})
	if err != nil || f == nil {
		t.Fatalf("a log inside the directory should open: %v", err)
	}
	f.Close()
	// A run whose log has not been written yet is not an error.
	if f, err := svc.openRunLog(Run{ID: "r4",
		LogPath: filepath.Join(svc.cfg.LogDir, "run-9.log")}); err != nil || f != nil {
		t.Errorf("a missing log should be nil, nil: %v %v", f, err)
	}
}

// TestAnIsolationThePackagerCannotBuildIsRefusedRatherThanDowngraded. The
// vocabulary is "dind", "plain" or empty, and store.CheckIsolation now
// refuses anything else at the write - so a finding arriving here with "vm"
// or "container" was written before that rule. It is still refused at the
// door: a repro that needs its own daemon, run without one, fails for a
// reason that has nothing to do with the code under test, and that failure
// would be recorded as a verdict.
func TestAnIsolationThePackagerCannotBuildIsRefusedRatherThanDowngraded(t *testing.T) {
	for _, ok := range []string{"", "dind", "plain"} {
		if err := checkIsolation(ok); err != nil {
			t.Errorf("isolation %q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"vm", "container", "podman"} {
		err := checkIsolation(bad)
		if err == nil {
			t.Fatalf("isolation %q should be refused", bad)
		}
		var f fault
		if !asFault(err, &f) || f.code != http.StatusConflict {
			t.Errorf("isolation %q refused as %v", bad, err)
		}
	}
}

func TestFindingNumIsShortStableAndFilenameSafe(t *testing.T) {
	id := ulid.NewString()
	num := findingNum(id)
	if len(num) != 6 || num != findingNum(id) {
		t.Errorf("findingNum(%s) = %q", id, num)
	}
	if strings.ToLower(num) != num {
		t.Errorf("%q is not lowercase", num)
	}
	// A short id is used whole rather than padded or refused.
	if findingNum("abc") != "abc" {
		t.Errorf("a short id came back as %q", findingNum("abc"))
	}
}

func TestAFilenameCannotSplitAHeader(t *testing.T) {
	got := quoteFilename("repro-x-1\r\nSet-Cookie: a=b; \"evil\".tgz")
	if strings.ContainsAny(got[1:len(got)-1], "\r\n\";") {
		t.Errorf("quoteFilename left something dangerous in %q", got)
	}
}

// --------------------------------------------------------------------- helpers

func asFault(err error, target *fault) bool {
	f, ok := err.(fault)
	if ok {
		*target = f
	}
	return ok
}

func urlEscape(s string) string {
	return strings.NewReplacer(" ", "%20", ";", "%3B", "/", "%2F").Replace(s)
}

// untar reads a gzipped tar into path -> contents, with the package's own
// top-level directory stripped so a test names files the way a person would.
func untar(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	defer zr.Close()
	out := map[string]string{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("untar: %v", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", h.Name, err)
		}
		name := h.Name
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		out[name] = string(content)
	}
	return out
}

func names(files map[string]string) []string {
	out := make([]string, 0, len(files))
	for k := range files {
		out = append(out, k)
	}
	return out
}
