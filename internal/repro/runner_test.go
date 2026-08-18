package repro

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// fakeStore is the three store calls a run makes, with the answers a test
// wants and a record of what was written back. It is deliberately not a
// database: every property this file tests is about what the runner DECIDES,
// and a Postgres would only make those decisions slower to observe.
type fakeStore struct {
	art      *store.Artifact
	manifest store.ReproManifest
	files    []store.ReproFileBytes
	readErr  error
	reproErr error
	recErr   error

	mu       sync.Mutex
	recorded []store.FindingRun
	// asWho is the agent id of the principal each of the three calls was
	// made as, in call order. It is recorded because "the run is performed
	// on behalf of somebody" is a claim about WHO the store was asked as,
	// and nothing else in a run record can be read as evidence of it.
	readAs   []string
	reproAs  []string
	recordAs []string
}

func (f *fakeStore) note(list *[]string, p *store.Principal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p == nil {
		*list = append(*list, "<nil>")
		return
	}
	*list = append(*list, p.AgentID)
}

func (f *fakeStore) ReadArtifact(_ context.Context, p *store.Principal, id string, _ bool) (*store.Artifact, error) {
	f.note(&f.readAs, p)
	if f.readErr != nil {
		return nil, f.readErr
	}
	art := *f.art
	art.ID = id
	return &art, nil
}

func (f *fakeStore) ReadFindingRepro(_ context.Context, p *store.Principal, _ string) (store.ReproManifest, []store.ReproFileBytes, error) {
	f.note(&f.reproAs, p)
	if f.reproErr != nil {
		return store.ReproManifest{}, nil, f.reproErr
	}
	return f.manifest, f.files, nil
}

func (f *fakeStore) RecordFindingRun(_ context.Context, p *store.Principal, _ string, run store.FindingRun) (*store.Event, error) {
	f.note(&f.recordAs, p)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recErr != nil {
		return nil, f.recErr
	}
	f.recorded = append(f.recorded, run)
	return &store.Event{}, nil
}

func (f *fakeStore) verdicts() []store.FindingRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.FindingRun(nil), f.recorded...)
}

// whoRecorded is the agent ids the verdicts were signed as, in order.
func (f *fakeStore) whoRecorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.recordAs...)
}

func (f *fakeStore) whoRead() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.readAs...)
}

// composeCall is one docker-compose invocation the runner made.
type composeCall struct {
	Project string
	Args    []string
}

// harness is a runner with all four of its steps faked, plus the recording of
// what they were asked to do.
type harness struct {
	*Runner
	store *fakeStore

	mu     sync.Mutex
	calls  []composeCall
	staged []RenderInput
	builds []string
}

func (h *harness) composeCalls() []composeCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]composeCall(nil), h.calls...)
}

func (h *harness) stagedInputs() []RenderInput {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]RenderInput(nil), h.staged...)
}

func (h *harness) builtSHAs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.builds...)
}

const testProject = "serenedb"

// asker is the principal a test enqueues on behalf of. Every Enqueue below
// names one, because there is no longer anywhere for a runner to get one
// from if a caller does not give it one.
func asker(agent string) *store.Principal { return &store.Principal{AgentID: agent} }

var testAsker = asker("01M05TQ6Z3YDMP88TYZX1N1Z4R")

// newHarness builds a runner whose version resolves to a published release
// (no build needed) and whose compose always succeeds, so each test changes
// only the one thing it is about.
func newHarness(t *testing.T, opt Options) *harness {
	t.Helper()
	project := testProject
	fs := &fakeStore{
		art: &store.Artifact{
			ID: "01M08YNY9ZFD7089CKAGM6HMA3", Type: "finding", Project: &project,
			Title: "serened crashes on nested parens", Body: "the report body",
		},
		manifest: store.ReproManifest{Entrypoint: "repro-01.sh", Interp: "bash"},
		files:    []store.ReproFileBytes{{Path: "repro-01.sh", Content: []byte("exit 0\n")}},
	}
	if opt.LogDir == "" {
		opt.LogDir = t.TempDir()
	}
	if opt.CacheDir == "" {
		opt.CacheDir = t.TempDir()
	}
	if opt.RunTimeout == 0 {
		opt.RunTimeout = 5 * time.Second
	}
	if opt.PackageBuildTimeout == 0 {
		opt.PackageBuildTimeout = 5 * time.Second
	}
	r, err := NewRunner(fs, func(name string) (ProjectConfig, bool) {
		if name != testProject {
			return ProjectConfig{}, false
		}
		return ProjectConfig{Source: "/src", BaseImage: "base:1", Registry: "serenedb/serenedb",
			BinaryPath: "/usr/bin/serened", DefaultIsolation: "dind"}, true
	}, opt)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	h := &harness{Runner: r, store: fs}

	r.resolve = func(_ context.Context, _ ProjectConfig, version string) Version {
		return Version{SHA: "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032",
			Image: "serenedb/serenedb@sha256:abc", Binary: "/cache/serened-bc07c51d4b8d",
			Note: "release " + version + " @ commit bc07c51d4b8d"}
	}
	r.stage = func(_ context.Context, cacheDir string, in RenderInput) (Result, error) {
		h.mu.Lock()
		h.staged = append(h.staged, in)
		h.mu.Unlock()
		dir := filepath.Join(cacheDir, "run-"+Name(in.Finding, in.Requested, in.Version))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Result{}, err
		}
		return Result{Dir: dir, Name: filepath.Base(dir), SHA: in.Version.SHA, SUT: in.Version.Image}, nil
	}
	r.build = func(_ context.Context, _, _, sha string, _ io.Writer) (string, error) {
		h.mu.Lock()
		h.builds = append(h.builds, sha)
		h.mu.Unlock()
		t.Error("nothing in this test should have needed a source build")
		return "", errors.New("no build expected in this test")
	}
	r.compose = func(_ context.Context, _, project string, _ io.Writer, args ...string) (int, error) {
		h.mu.Lock()
		h.calls = append(h.calls, composeCall{Project: project, Args: args})
		h.mu.Unlock()
		return 0, nil
	}
	r.Start(context.Background())
	t.Cleanup(r.Stop)
	return h
}

// onCompose replaces the compose step, keeping the call recording.
func (h *harness) onCompose(fn func(ctx context.Context, args []string, log io.Writer) (int, error)) {
	h.Runner.compose = func(ctx context.Context, _, project string, log io.Writer, args ...string) (int, error) {
		h.mu.Lock()
		h.calls = append(h.calls, composeCall{Project: project, Args: args})
		h.mu.Unlock()
		return fn(ctx, args, log)
	}
}

// await polls one run to a terminal status - the same way the trusted-host
// binary's callers will.
func (h *harness) await(t *testing.T, id int64) Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, ok := h.RunByID(id)
		if ok && run.Status.Terminal() {
			return run
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run %d never reached a terminal status", id)
	return Run{}
}

func (h *harness) runOnce(t *testing.T, version string) Run {
	t.Helper()
	ids, err := h.Enqueue(testAsker, []string{"01M08YNY9ZFD7089CKAGM6HMA3"}, version)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return h.await(t, ids[0])
}

// TestRunConfirmed: the repro exits 0, so the finding still reproduces and
// the verdict is recorded on its run log.
func TestRunConfirmed(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	run := h.runOnce(t, "26.07.5")

	if run.Status != StatusConfirmed {
		t.Fatalf("status = %q, want confirmed (note %q)", run.Status, run.Note)
	}
	if run.Confirmed == nil || !*run.Confirmed {
		t.Fatalf("confirmed = %v, want true", run.Confirmed)
	}
	if run.SHA != "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032" {
		t.Errorf("sha = %q, want the resolved commit", run.SHA)
	}
	if run.StartedAt == nil || run.EndedAt == nil {
		t.Errorf("run has no start/end stamp: %+v", run)
	}
	verdicts := h.store.verdicts()
	if len(verdicts) != 1 {
		t.Fatalf("recorded %d verdicts, want 1", len(verdicts))
	}
	if verdicts[0].Version != "26.07.5" || !verdicts[0].Confirmed ||
		verdicts[0].Status != string(StatusConfirmed) ||
		verdicts[0].SHA != "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032" {
		t.Errorf("recorded verdict = %+v", verdicts[0])
	}
}

// TestRunNotConfirmed: the repro ran and its assertion did not fire. That IS
// a verdict and is recorded as one - the finding no longer reproduces here.
func TestRunNotConfirmed(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.onCompose(func(_ context.Context, args []string, log io.Writer) (int, error) {
		if args[0] == "up" {
			fmt.Fprint(log, "repro-01.sh: expected a crash, got a clean exit\n")
			return 1, nil
		}
		return 0, nil
	})
	run := h.runOnce(t, "latest")

	if run.Status != StatusNotConfirmed {
		t.Fatalf("status = %q, want not-confirmed (note %q)", run.Status, run.Note)
	}
	if run.Confirmed == nil || *run.Confirmed {
		t.Fatalf("confirmed = %v, want false", run.Confirmed)
	}
	verdicts := h.store.verdicts()
	if len(verdicts) != 1 || verdicts[0].Confirmed || verdicts[0].Status != string(StatusNotConfirmed) {
		t.Fatalf("recorded = %+v, want one not-confirmed verdict", verdicts)
	}
}

// TestHarnessFailureIsNotAVerdict is the property the whole file exists for:
// a nonzero exit whose log says the harness broke is an error, and NOTHING
// is written to the finding's run log. A verdict here would be a finding
// silently declared fixed.
func TestHarnessFailureIsNotAVerdict(t *testing.T) {
	for _, sig := range harnessSignatures {
		t.Run(sig, func(t *testing.T) {
			h := newHarness(t, Options{Workers: 1})
			h.onCompose(func(_ context.Context, args []string, log io.Writer) (int, error) {
				if args[0] == "up" {
					fmt.Fprintf(log, "starting the repro\n%s\n", sig)
					return 1, nil
				}
				return 0, nil
			})
			run := h.runOnce(t, "latest")

			if run.Status != StatusError {
				t.Fatalf("status = %q, want error for a harness failure", run.Status)
			}
			if run.Confirmed != nil {
				t.Errorf("confirmed = %v, want no verdict at all", *run.Confirmed)
			}
			if !strings.Contains(run.Note, sig) {
				t.Errorf("note = %q, want it to name the signature %q", run.Note, sig)
			}
			if v := h.store.verdicts(); len(v) != 0 {
				t.Fatalf("recorded %+v; a broken sandbox is not a verdict", v)
			}
		})
	}
}

// TestHarnessErrorReadsOnlyTheTail: a signature printed early, then buried
// under the repro's own output, is not what killed this run - and convicting
// on it would turn a genuine not-reproduced into an error. The tail is what
// is searched, exactly as runner.py read the last 6000 bytes.
func TestHarnessErrorReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	body := "Error response from daemon: an old, recovered-from hiccup\n" +
		strings.Repeat("server: query ok\n", 1000)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := harnessError(path); got != "" {
		t.Fatalf("harnessError = %q, want none - the signature is far above the tail", got)
	}

	// The same signature at the end IS what this run died of.
	if err := os.WriteFile(path, []byte(body+"Error response from daemon: no such image\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := harnessError(path); !strings.Contains(got, "Error response from daemon") {
		t.Fatalf("harnessError = %q, want the signature named", got)
	}
}

// TestHarnessErrorOnMissingLog: an unreadable log is not evidence that the
// harness failed. Answering "broken" here would turn every real
// not-reproduced into an error the moment the disk hiccuped.
func TestHarnessErrorOnMissingLog(t *testing.T) {
	if got := harnessError(filepath.Join(t.TempDir(), "nope.log")); got != "" {
		t.Fatalf("harnessError on a missing log = %q, want none", got)
	}
}

// TestPackageBuildFailureIsNotAVerdict: the wrapper image never built, so the
// repro never ran. Not a verdict, and `up` is never reached.
func TestPackageBuildFailureIsNotAVerdict(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.onCompose(func(_ context.Context, args []string, log io.Writer) (int, error) {
		if args[0] == "build" {
			fmt.Fprint(log, "failed to solve: dockerfile parse error\n")
			return 2, nil
		}
		return 0, nil
	})
	run := h.runOnce(t, "latest")

	if run.Status != StatusError || run.Note != "package build failed" {
		t.Fatalf("run = %+v, want an error saying the package build failed", run)
	}
	if v := h.store.verdicts(); len(v) != 0 {
		t.Fatalf("recorded %+v, want nothing", v)
	}
	for _, c := range h.composeCalls() {
		if c.Args[0] == "up" {
			t.Fatalf("ran `up` after the package build failed: %+v", h.composeCalls())
		}
	}
}

// TestRunTimeoutIsNotAVerdict: a repro that never finished did not answer.
func TestRunTimeoutIsNotAVerdict(t *testing.T) {
	h := newHarness(t, Options{Workers: 1, RunTimeout: 20 * time.Millisecond})
	h.onCompose(func(ctx context.Context, args []string, _ io.Writer) (int, error) {
		if args[0] == "up" {
			<-ctx.Done()
			return -1, ctx.Err()
		}
		return 0, nil
	})
	run := h.runOnce(t, "latest")

	if run.Status != StatusError || !strings.Contains(run.Note, "timeout after") {
		t.Fatalf("run = %+v, want a timeout error", run)
	}
	if v := h.store.verdicts(); len(v) != 0 {
		t.Fatalf("recorded %+v, want nothing", v)
	}
}

// TestTeardownRunsAfterAFailedRun: the containers and volumes of a run that
// timed out are exactly the ones still up, so `down -v` runs on every path
// out - with a context of its own, not the run's dead one.
func TestTeardownRunsAfterAFailedRun(t *testing.T) {
	h := newHarness(t, Options{Workers: 1, RunTimeout: 20 * time.Millisecond})
	h.onCompose(func(ctx context.Context, args []string, _ io.Writer) (int, error) {
		switch args[0] {
		case "up":
			<-ctx.Done()
			return -1, ctx.Err()
		case "down":
			if err := ctx.Err(); err != nil {
				t.Errorf("teardown inherited the dead run context: %v", err)
			}
		}
		return 0, nil
	})
	h.runOnce(t, "latest")

	var down bool
	for _, c := range h.composeCalls() {
		if c.Args[0] == "down" && len(c.Args) > 1 && c.Args[1] == "-v" {
			down = true
		}
	}
	if !down {
		t.Fatalf("no `down -v` after a timed-out run: %+v", h.composeCalls())
	}
}

// TestSourceBuildBuildsTheBinaryFirst: a commit with no built binary goes
// through the build script, and the path it prints is what the package
// stages.
func TestSourceBuildBuildsTheBinaryFirst(t *testing.T) {
	built := filepath.Join(t.TempDir(), "serened-bc07c51d4b8d")
	if err := os.WriteFile(built, []byte("ELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, Options{Workers: 1, BuildScript: "/scripts/build-sut.sh"})
	h.Runner.resolve = func(_ context.Context, _ ProjectConfig, _ string) Version {
		return Version{SHA: "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032", Image: "base:1",
			Buildable: true, SourceBuild: true, Note: "latest @ bc07c51d4b8d - builds from source"}
	}
	h.Runner.build = func(_ context.Context, script, project, sha string, _ io.Writer) (string, error) {
		if script != "/scripts/build-sut.sh" || project != testProject {
			t.Errorf("build called as (%q, %q)", script, project)
		}
		h.mu.Lock()
		h.builds = append(h.builds, sha)
		h.mu.Unlock()
		return built, nil
	}
	run := h.runOnce(t, "latest")

	if run.Status != StatusConfirmed {
		t.Fatalf("status = %q, want confirmed (note %q)", run.Status, run.Note)
	}
	if got := h.builtSHAs(); len(got) != 1 || got[0] != "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032" {
		t.Fatalf("built %v, want the resolved commit built once", got)
	}
	staged := h.stagedInputs()
	if len(staged) != 1 || staged[0].Version.Binary != built {
		t.Fatalf("staged with binary %q, want the freshly built %q", staged[0].Version.Binary, built)
	}
}

// TestBuildFailureIsAnError: no binary, so nothing ran - and a run against a
// binary from some other commit is exactly what refusing here prevents.
func TestBuildFailureIsAnError(t *testing.T) {
	h := newHarness(t, Options{Workers: 1, BuildScript: "/scripts/build-sut.sh"})
	h.Runner.resolve = func(_ context.Context, _ ProjectConfig, _ string) Version {
		return Version{SHA: "bc07c51d4b8d", Image: "base:1", Buildable: true, SourceBuild: true,
			Note: "latest @ bc07c51d4b8d - builds from source"}
	}
	h.Runner.build = func(context.Context, string, string, string, io.Writer) (string, error) {
		return "", errors.New("exit status 1")
	}
	run := h.runOnce(t, "latest")

	if run.Status != StatusError || !strings.Contains(run.Note, "build failed") {
		t.Fatalf("run = %+v, want a build failure", run)
	}
	if len(h.stagedInputs()) != 0 {
		t.Fatal("staged a package for a version whose binary never built")
	}
}

// TestSourceBuildWithNoBuildScript says so rather than running against
// whatever binary happens to be lying around.
func TestSourceBuildWithNoBuildScript(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.Runner.resolve = func(_ context.Context, _ ProjectConfig, _ string) Version {
		return Version{SHA: "bc07c51d4b8d", Image: "base:1", Buildable: true, SourceBuild: true,
			Note: "latest @ bc07c51d4b8d - builds from source"}
	}
	run := h.runOnce(t, "latest")

	if run.Status != StatusError || !strings.Contains(run.Note, "no build script is configured") {
		t.Fatalf("run = %+v, want the missing build script named", run)
	}
}

// TestUnresolvableVersionIsAnError: the resolver never errors, it reports in
// the note - so an unbuildable source version is read off Buildable and the
// note is the reason a human gets.
func TestUnresolvableVersionIsAnError(t *testing.T) {
	h := newHarness(t, Options{Workers: 1, BuildScript: "/scripts/build-sut.sh"})
	h.Runner.resolve = func(_ context.Context, _ ProjectConfig, version string) Version {
		return Version{SHA: version, SourceBuild: true, Note: "could not resolve git ref " + version}
	}
	run := h.runOnce(t, "no-such-branch")

	if run.Status != StatusError || !strings.Contains(run.Note, "could not resolve git ref") {
		t.Fatalf("run = %+v, want the resolver's note as the reason", run)
	}
	if len(h.stagedInputs()) != 0 {
		t.Fatal("staged a package for a version that does not resolve")
	}
}

// TestFindingWithoutAProjectIsRefused: there is no source, image or cache to
// run against, and picking one would be the hardcoded default this port
// exists to remove.
func TestFindingWithoutAProjectIsRefused(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.store.art.Project = nil
	run := h.runOnce(t, "latest")

	if run.Status != StatusError || !strings.Contains(run.Note, "names no project") {
		t.Fatalf("run = %+v, want a refusal naming the missing project", run)
	}
}

// TestUnconfiguredProjectIsRefused for the same reason, one level up.
func TestUnconfiguredProjectIsRefused(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	other := "some-other-project"
	h.store.art.Project = &other
	run := h.runOnce(t, "latest")

	if run.Status != StatusError || !strings.Contains(run.Note, "no repro configuration") {
		t.Fatalf("run = %+v, want a refusal naming the unconfigured project", run)
	}
}

// TestNoReproTreeIsAnError: the store's own refusal reaches the run record.
func TestNoReproTreeIsAnError(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.store.reproErr = errors.New("finding X has no repro tree recorded")
	run := h.runOnce(t, "latest")

	if run.Status != StatusError || !strings.Contains(run.Note, "no repro tree recorded") {
		t.Fatalf("run = %+v, want the store's refusal on the record", run)
	}
}

// TestVerdictNotRecordedIsNotedOnTheRun: the run happened, and losing the
// fact that it happened would be worse than it being recorded in one place
// instead of two.
func TestVerdictNotRecordedIsNotedOnTheRun(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.store.recErr = errors.New("finding has no project")
	run := h.runOnce(t, "latest")

	if run.Status != StatusConfirmed {
		t.Fatalf("status = %q, want the verdict to stand", run.Status)
	}
	if !strings.Contains(run.Note, "verdict not recorded") {
		t.Fatalf("note = %q, want it to say the recording failed", run.Note)
	}
}

// TestRenderInputCarriesTheFinding: what the package says it is proving comes
// off the finding's own row, and the manifest's isolation falls back to the
// project's default rather than to "plain" for a project whose repros spin
// their own containers.
func TestRenderInputCarriesTheFinding(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.runOnce(t, "26.07.5")

	staged := h.stagedInputs()
	if len(staged) != 1 {
		t.Fatalf("staged %d packages, want 1", len(staged))
	}
	in := staged[0]
	if in.Finding.Project != testProject || in.Finding.Title != "serened crashes on nested parens" {
		t.Errorf("finding = %+v", in.Finding)
	}
	if in.Finding.Report != "the report body" {
		t.Errorf("report = %q, want the finding's body", in.Finding.Report)
	}
	if in.Finding.Num != "6hma3" && in.Finding.Num != "m6hma3" {
		t.Errorf("num = %q, want the tail of the finding id", in.Finding.Num)
	}
	if in.Requested != "26.07.5" {
		t.Errorf("requested = %q", in.Requested)
	}
	if in.Manifest.Isolation != "dind" {
		t.Errorf("isolation = %q, want the project default", in.Manifest.Isolation)
	}
}

// TestReportFallsBackToTheScript: a package whose README says nothing about
// what it proves is one nobody upstream can act on.
func TestReportFallsBackToTheScript(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.store.art.Body = ""
	h.store.art.Discovery = "found while fuzzing the parser"
	h.runOnce(t, "latest")
	if got := h.stagedInputs()[0].Finding.Report; got != "found while fuzzing the parser" {
		t.Errorf("report = %q, want the discovery note", got)
	}

	h2 := newHarness(t, Options{Workers: 1})
	h2.store.art.Body = ""
	h2.runOnce(t, "latest")
	if got := h2.stagedInputs()[0].Finding.Report; !strings.Contains(got, "no written report") {
		t.Errorf("report = %q, want the placeholder", got)
	}
}

// TestPoolRunsInParallel: two runs are in flight at once with two workers,
// which is safe precisely because each run is staged and composed under its
// own project name - checked here too.
func TestPoolRunsInParallel(t *testing.T) {
	h := newHarness(t, Options{Workers: 2})
	both := make(chan struct{})
	var once sync.Once
	var inFlight int
	var mu sync.Mutex
	h.onCompose(func(ctx context.Context, args []string, _ io.Writer) (int, error) {
		if args[0] != "up" {
			return 0, nil
		}
		mu.Lock()
		inFlight++
		reached := inFlight == 2
		mu.Unlock()
		if reached {
			once.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return 0, nil
	})

	ids, err := h.Enqueue(testAsker, []string{"finding-a", "finding-b"}, "latest")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	for _, id := range ids {
		if run := h.await(t, id); run.Status != StatusConfirmed {
			t.Fatalf("run %d = %q (%s)", id, run.Status, run.Note)
		}
	}
	projects := map[string]bool{}
	for _, c := range h.composeCalls() {
		projects[c.Project] = true
	}
	if len(projects) != 2 {
		t.Fatalf("compose projects %v, want one per run", projects)
	}
}

// TestQueueFullIsRefused rather than waited on: a POST that hangs because a
// queue is full tells an operator nothing.
func TestQueueFullIsRefused(t *testing.T) {
	fs := &fakeStore{art: &store.Artifact{}}
	r, err := NewRunner(fs, func(string) (ProjectConfig, bool) {
		return ProjectConfig{}, true
	}, Options{Workers: 1, QueueDepth: 1, LogDir: t.TempDir(), CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Never started, so nothing drains the queue.
	ids, err := r.Enqueue(testAsker, []string{"a", "b"}, "latest")
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v, want ErrQueueFull", err)
	}
	if len(ids) != 1 {
		t.Fatalf("ids = %v, want the one that fit", ids)
	}
	// The refused run still has a record saying why.
	refused, ok := r.RunByID(2)
	if !ok || refused.Status != StatusError || !strings.Contains(refused.Note, "queue is full") {
		t.Fatalf("refused run = %+v, want a record naming the full queue", refused)
	}
}

// TestStopMarksQueuedRunsError: "queued" on a runner that no longer exists is
// not an answer.
func TestStopMarksQueuedRunsError(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	release := make(chan struct{})
	h.onCompose(func(ctx context.Context, args []string, _ io.Writer) (int, error) {
		if args[0] == "up" {
			select {
			case <-release:
			case <-ctx.Done():
				return -1, ctx.Err()
			}
		}
		return 0, nil
	})
	ids, err := h.Enqueue(testAsker, []string{"a", "b", "c"}, "latest")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Wait until the first is actually in flight, so the others are queued.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if run, _ := h.RunByID(ids[0]); run.Status == StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first run never started")
		}
		time.Sleep(time.Millisecond)
	}
	// Stop while that first run is still blocked: it is cancelled mid-run,
	// and the two behind it never start at all.
	h.Stop()
	close(release)

	if first, _ := h.RunByID(ids[0]); first.Status != StatusError ||
		!strings.Contains(first.Note, "the runner stopped") {
		t.Fatalf("the in-flight run = %+v, want an error naming the shutdown", first)
	}

	// Whichever of the two the shutdown caught - still queued, or picked up
	// and cancelled mid-compose - the record says the runner stopped, not
	// something about Docker the operator would go looking for.
	for _, id := range ids[1:] {
		run, _ := h.RunByID(id)
		if run.Status == StatusQueued {
			t.Fatalf("run %d is still queued on a stopped runner", id)
		}
		if !strings.Contains(run.Note, "the runner stopped") {
			t.Errorf("run %d note = %q, want the shutdown named", id, run.Note)
		}
		if run.Confirmed != nil {
			t.Errorf("run %d has a verdict after a shutdown: %v", id, *run.Confirmed)
		}
	}
	if _, err := h.Enqueue(testAsker, []string{"d"}, "latest"); !errors.Is(err, ErrStopped) {
		t.Fatalf("enqueue after stop = %v, want ErrStopped", err)
	}
	h.Stop() // idempotent
}

// TestEnqueueDefaultsAndRefusals.
func TestEnqueueDefaultsAndRefusals(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	ids, err := h.Enqueue(testAsker, []string{"a"}, "  ")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	run, _ := h.RunByID(ids[0])
	if run.Version != "latest" {
		t.Errorf("version = %q, want the latest default", run.Version)
	}
	if _, err := h.Enqueue(testAsker, nil, "latest"); err == nil {
		t.Error("enqueued a batch naming no finding")
	}
	if _, err := h.Enqueue(testAsker, []string{" "}, "latest"); err == nil {
		t.Error("enqueued a run naming no finding")
	}
}

// TestRunsAreCopies: a reader cannot see a verdict change under it, and
// cannot change one.
func TestRunsAreCopies(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	run := h.runOnce(t, "latest")
	*run.Confirmed = false
	run.Status = StatusError

	again, _ := h.RunByID(run.ID)
	if again.Status != StatusConfirmed || again.Confirmed == nil || !*again.Confirmed {
		t.Fatalf("a caller's edit reached the runner's own record: %+v", again)
	}
	all := h.Runs()
	if len(all) != 1 || all[0].ID != run.ID {
		t.Fatalf("Runs() = %+v", all)
	}
	if !all[0].Status.Terminal() {
		t.Errorf("terminal status %q not reported terminal", all[0].Status)
	}
}

// TestLogIsWrittenAndNamed: the trusted-host binary serves this file, so it
// has to exist and carry both the header and the verdict line.
func TestLogIsWrittenAndNamed(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	run := h.runOnce(t, "latest")
	body, err := os.ReadFile(run.Log)
	if err != nil {
		t.Fatalf("run log: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "handed upstream") || !strings.Contains(text, "CONFIRMED") {
		t.Fatalf("run log does not read as one:\n%s", text)
	}
}

// TestLastLine is the build script's contract: the path is the last non-empty
// line of stdout, however much came before it.
func TestLastLine(t *testing.T) {
	cases := map[string]string{
		"/cache/serened-abc\n":            "/cache/serened-abc",
		"noise\n/cache/serened-abc\n\n\n": "/cache/serened-abc",
		"  /cache/serened-abc  ":          "/cache/serened-abc",
		"":                                "",
		"\n\n":                            "",
	}
	for in, want := range cases {
		if got := lastLine(in); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVerdictIsSignedByWhoeverAsked is the whole of the principal-per-enqueue
// decision, checked at the only place it is observable: WHO the store was
// called as.
//
// It enqueues the same finding twice on behalf of two different agents and
// asserts that the two verdicts were recorded under those two names. A test
// that only asserted the runner accepts a principal argument would pass with
// the argument ignored, which is the version of this that was already true.
func TestVerdictIsSignedByWhoeverAsked(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	one, two := asker("agent-one"), asker("agent-two")

	first, err := h.Enqueue(one, []string{"01M08YNY9ZFD7089CKAGM6HMA3"}, "26.07.5")
	if err != nil {
		t.Fatalf("Enqueue as one: %v", err)
	}
	h.await(t, first[0])
	second, err := h.Enqueue(two, []string{"01M08YNY9ZFD7089CKAGM6HMA3"}, "26.07.5")
	if err != nil {
		t.Fatalf("Enqueue as two: %v", err)
	}
	h.await(t, second[0])

	if got, want := h.store.whoRecorded(), []string{"agent-one", "agent-two"}; !equalStrings(got, want) {
		t.Errorf("verdicts recorded as %v, want %v - a run's author is whoever asked for it", got, want)
	}
	if got, want := h.store.whoRead(), []string{"agent-one", "agent-two"}; !equalStrings(got, want) {
		t.Errorf("findings read as %v, want %v - a run reads with the asker's reach, not the daemon's",
			got, want)
	}
}

// TestEnqueueRefusesWithNoPrincipal: there is no default to fall back to, so
// a run with nobody behind it is refused at the door rather than performed
// under some other name.
func TestEnqueueRefusesWithNoPrincipal(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	if _, err := h.Enqueue(nil, []string{"01M08YNY9ZFD7089CKAGM6HMA3"}, "latest"); err == nil {
		t.Fatal("enqueued a run on behalf of nobody")
	}
	if runs := h.Runs(); len(runs) != 0 {
		t.Errorf("a refused enqueue left %d run records", len(runs))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNewRunnerRefusesAnUnusableConfiguration.
func TestNewRunnerRefusesAnUnusableConfiguration(t *testing.T) {
	fs := &fakeStore{art: &store.Artifact{}}
	projects := func(string) (ProjectConfig, bool) { return ProjectConfig{}, true }
	dir := t.TempDir()
	if _, err := NewRunner(nil, projects, Options{LogDir: dir, CacheDir: dir}); err == nil {
		t.Error("built a runner with no store")
	}
	if _, err := NewRunner(fs, nil, Options{LogDir: dir, CacheDir: dir}); err == nil {
		t.Error("built a runner with no project lookup")
	}
	if _, err := NewRunner(fs, projects, Options{CacheDir: dir}); err == nil {
		t.Error("built a runner with nowhere to write logs")
	}
	r, err := NewRunner(fs, projects, Options{LogDir: dir, CacheDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if r.opt.Workers != defaultWorkers || r.opt.RunTimeout != defaultRunTimeout ||
		r.opt.QueueDepth != defaultQueueDepth || r.opt.BuildTimeout != defaultBuildTimeout ||
		r.opt.PackageBuildTimeout != defaultPackageBuildTimeout ||
		r.opt.TeardownTimeout != defaultTeardownTimeout {
		t.Errorf("defaults not applied: %+v", r.opt)
	}
}

// A REPRO THAT NEVER RAN CANNOT HAVE FAILED TO REPRODUCE. Measured in the wild:
// the tiktoken tree's run.sh calls pip, the SUT image has no pip, the shell
// exits 127 with "command not found" - and that was recorded as confirmed=false
// on the finding's log. A finding silently declared fixed on evidence that never
// existed is the exact outcome this file's header says it exists to prevent.
func TestAMissingInterpreterIsAHarnessErrorNotAVerdict(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.onCompose(func(_ context.Context, args []string, log io.Writer) (int, error) {
		if args[0] == "up" {
			fmt.Fprint(log, "run.sh: line 5: pip: command not found\n")
			return 127, nil
		}
		return 0, nil
	})
	run := h.runOnce(t, "latest")

	if run.Status != StatusError {
		t.Fatalf("status = %q, want error - exit 127 is the shell saying it could not find the command", run.Status)
	}
	if run.Confirmed != nil {
		t.Fatalf("confirmed = %v, want nothing recorded: there is no verdict here", *run.Confirmed)
	}
	// The finding's log must stay untouched. A verdict written from a run that
	// never executed is worse than no run at all, because it looks like evidence.
	if v := h.store.verdicts(); len(v) != 0 {
		t.Fatalf("recorded %+v on the finding, want nothing", v)
	}
}

// 126 is the neighbouring case: the command was found and could not be run -
// not executable, or a bad interpreter line. Also not a verdict.
func TestAnUnexecutableReproIsAHarnessErrorNotAVerdict(t *testing.T) {
	h := newHarness(t, Options{Workers: 1})
	h.onCompose(func(_ context.Context, args []string, log io.Writer) (int, error) {
		if args[0] == "up" {
			fmt.Fprint(log, "docker: permission denied\n")
			return 126, nil
		}
		return 0, nil
	})
	run := h.runOnce(t, "latest")
	if run.Status != StatusError {
		t.Fatalf("status = %q, want error", run.Status)
	}
	if v := h.store.verdicts(); len(v) != 0 {
		t.Fatalf("recorded %+v, want nothing", v)
	}
}
