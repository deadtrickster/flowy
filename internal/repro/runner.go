package repro

// The run queue: what actually executes a finding's repro, and the one
// distinction the whole file exists to protect.
//
// Ported from hands-off's tools/handoff-service/runner.py - enqueue, _worker
// and _run_one (lines 301-412) - keeping its state machine, its worker pool
// and its timeouts, and dropping everything that was a property of that
// service's filesystem rather than of running a repro (findings as
// directories, verdicts as a runs.jsonl next to them, marks written back
// into RESULT.md frontmatter). Those live in the store now: the repro tree
// is a set of attachments (findingrepro.go) and a verdict is an append-only
// signed event (findingruns.go).
//
// BROKEN IS NOT THE SAME ANSWER AS DID-NOT-REPRODUCE, and telling them apart
// is the value this file adds over "shell out and read the exit code". A
// staged package's `docker compose up` exits nonzero for two completely
// different reasons: the repro ran and its assertion did not fire (the
// finding did not reproduce against this version - a real verdict, worth
// recording), or the harness never got far enough to have an opinion (no
// disk, no daemon, an image that could not be pulled, an inner dockerd that
// never came up). Reporting the second as the first writes a green verdict
// against a version nobody actually tested, and since a run log is what a
// human reads to decide a finding is fixed, that is a finding silently
// declared fixed. So a nonzero exit is checked against the harness-failure
// signatures below BEFORE it is believed, and a run that matches one ends as
// StatusError with no verdict recorded at all - the version table stays
// blank for that version rather than gaining a lie.
//
// WHAT IS RECORDED AND WHAT IS NOT. Only a run that reached a verdict -
// confirmed or not-confirmed - calls RecordFindingRun. An error run exists
// in this process's run list and in its log file, where a human can read
// what broke, and nowhere else. That asymmetry is deliberate: the event log
// is the record of what the code did, and "the sandbox was broken" is not a
// fact about the code.
//
// THE POOL. Every run is staged into its own directory and brought up under
// its own compose project name, and a dind package gives the repro its own
// inner Docker daemon, so two runs cannot collide over the hardcoded ports
// and container names a repro script is full of. That isolation is what
// makes a pool safe, so Workers defaults low rather than to NumCPU: each
// concurrent run is a privileged dind container plus a build, and the limit
// is the box's disk and memory, not its cores.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// Status is where a run is in its life. queued -> running -> (building ->)
// one of the three terminal states, and no other order: a caller polling
// this sees the same sequence runner.py's console did.
type Status string

const (
	// StatusQueued is accepted and waiting for a worker.
	StatusQueued Status = "queued"
	// StatusRunning is a worker's, from the first store read to the verdict.
	StatusRunning Status = "running"
	// StatusBuilding is the long tail inside running: the version named a
	// commit whose binary is not built yet, and a cold build of a real
	// system under test is measured in hours, so it gets a status of its own
	// rather than looking like a hung run.
	StatusBuilding Status = "building"
	// StatusConfirmed is the repro exiting 0: the finding still reproduces
	// against this version.
	StatusConfirmed Status = "confirmed"
	// StatusNotConfirmed is the repro exiting nonzero for its own reasons:
	// it ran, and the finding did not reproduce. A verdict, and recorded.
	StatusNotConfirmed Status = "not-confirmed"
	// StatusError is everything that is not a verdict - no repro tree, an
	// unresolvable version, a failed build, a timeout, or a harness failure
	// matching one of the signatures below. Never recorded as a verdict; see
	// the head of this file.
	StatusError Status = "error"
)

// Terminal reports whether a run in this status is finished.
func (s Status) Terminal() bool {
	switch s {
	case StatusConfirmed, StatusNotConfirmed, StatusError:
		return true
	default:
		return false
	}
}

// Run is one repro run: which finding against which version, where it got
// to, and where its log is. The JSON names are runner.py's own record keys,
// so a console written against the Python service reads this unchanged.
//
// Confirmed is three-valued on purpose, exactly as it was there: true, false,
// and "no verdict" (nil). A run that ended in StatusError has nil here, and
// a reader that flattened nil to false would be making the mistake this file
// is about.
type Run struct {
	ID      int64  `json:"id"`
	Finding string `json:"finding"`
	// Project is which project's checkout, image and cache this run is
	// against, named by the caller that asked for it. It is on the record
	// rather than in a map beside it because a map beside it is a second
	// index with the same failure as the first: it did not survive a
	// restart either, and a run that comes back knowing everything except
	// which project it was for is not a run that came back.
	Project   string     `json:"project,omitempty"`
	Version   string     `json:"version"`
	Status    Status     `json:"status"`
	SHA       string     `json:"sha,omitempty"`
	Confirmed *bool      `json:"confirmed"`
	Note      string     `json:"note,omitempty"`
	QueuedAt  time.Time  `json:"queued_at"`
	StartedAt *time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	// Log is the absolute path of this run's log file. It is written from
	// the first line to the last, including the build, so a reader tailing
	// it sees the run as it happens - which is what the trusted-host
	// binary's GET /run/{id}/log serves.
	Log string `json:"log"`

	// failed records that this run has already lost, without yet ending it.
	// Unexported and not on the wire: it exists only between fail() and
	// finish(), so that a run stays non-terminal until its containers are
	// down. A reader outside sees the status, which is the truth it needs.
	failed bool

	// reached is the verdict this run arrived at, held back until finish().
	//
	// It is failed's other half and exists for the same reason, which fail()
	// already states and verdict() did not honour: a run is not finished while
	// its containers are still up. execute() decides confirmed-or-not, then
	// returns to runOne, which tears the compose project down and only then
	// records the verdict on the finding. Setting Status to a TERMINAL value at
	// the moment the verdict is known - which is what verdict() used to do -
	// publishes "this run is done" while `down -v` has not run and while the
	// finding's run log has not been written.
	//
	// Measured 2026-08-20 as a drainer red that would not reproduce:
	// TestVerdictIsSignedByWhoeverAsked read [agent-one] where it wanted
	// [agent-one agent-two], because the second run went terminal before its
	// record landed and the test - correctly waiting on terminal - looked in
	// between. That is the small version. The large version is a caller that
	// waits for terminal and starts the next run into the previous one's
	// containers and volumes, which is the exact collision fail()'s comment
	// warns about.
	//
	// Unexported and not on the wire, like failed: outside readers see Status,
	// and Status does not go terminal until everything behind it is true.
	reached Status

	// principal is WHO THIS RUN IS PERFORMED ON BEHALF OF, carried from the
	// enqueue that asked for it to the store calls that read the finding and
	// record the verdict. It is unexported and has no JSON name on purpose:
	// a principal is a capability, and a run record is served to callers.
	//
	// It is per run rather than per runner because a verdict is evidence and
	// evidence with no author is worth less. The trusted-host binary's whole
	// boundary argument (cmd/handoff-runner/http.go) is that a run enqueued
	// through its door is recorded against the principal that asked for it,
	// so the finding's run log names a person or an agent rather than a
	// daemon - and a runner holding ONE identity at construction could not
	// honour that, whatever the door claimed.
	principal *store.Principal
}

// clone returns a copy safe to hand outside the lock: the two pointer fields
// are copied by value, not shared, so a caller cannot see a verdict change
// under it and cannot change one.
func (r Run) clone() Run {
	out := r
	if r.Confirmed != nil {
		v := *r.Confirmed
		out.Confirmed = &v
	}
	if r.StartedAt != nil {
		t := *r.StartedAt
		out.StartedAt = &t
	}
	if r.EndedAt != nil {
		t := *r.EndedAt
		out.EndedAt = &t
	}
	return out
}

// Findings is the store, narrowed to the three calls a run makes. It is an
// interface so this package's tests can run the whole state machine without
// a Postgres - not because a second implementation is expected. The runner
// persists through the store DIRECTLY (same process, same database): a run's
// verdict does not go back out through HTTP to reach the row it is about.
type Findings interface {
	ReadArtifact(ctx context.Context, p *store.Principal, id string, scopeAll bool) (*store.Artifact, error)
	ReadFindingRepro(ctx context.Context, p *store.Principal, findingID string) (store.ReproManifest, []store.ReproFileBytes, error)
	RecordFindingRun(ctx context.Context, p *store.Principal, findingID string, run store.FindingRun) (*store.Event, error)
}

// Projects answers which checkout, image and cache a project's repros run
// against - see ProjectConfig in version.go. A finding whose project has no
// config cannot be run, and says so rather than being run against some other
// project's source: that hardcoded default is the exact bug the port exists
// to remove.
type Projects func(project string) (ProjectConfig, bool)

// Options are the runner's dials. Every zero value takes the default beside
// it, so a caller that only knows where its scratch space is can leave the
// rest alone.
type Options struct {
	// Workers is how many runs execute at once (default 2). See the head of
	// this file on why this is low: each run is a privileged dind container,
	// so the ceiling is disk and memory rather than cores.
	Workers int
	// QueueDepth is how many runs may wait (default 256). A full queue is
	// refused at Enqueue rather than blocking the caller - a POST that hangs
	// because a queue is full tells an operator nothing, and an error naming
	// the depth tells them what to raise.
	QueueDepth int
	// LogDir holds run logs, one file per run. Required.
	LogDir string
	// CacheDir is where packages are staged and SUT tars cached - the
	// packager's cacheDir. Required.
	CacheDir string
	// BuildScript is scripts/build-sut.sh, which builds a project's binary
	// at a commit and prints its cached path. Empty means no build is
	// possible here: a version that needs one ends as an error saying so
	// rather than running against a binary from a different commit.
	BuildScript string
	// RunTimeout caps `compose up` - the repro itself (default 25m).
	RunTimeout time.Duration
	// BuildTimeout caps a source build (default 3h). A cold build of a real
	// database is hours; this is not a value to tighten without measuring.
	BuildTimeout time.Duration
	// PackageBuildTimeout caps `compose build` - the wrapper image, not the
	// system under test (default 30m).
	PackageBuildTimeout time.Duration
	// TeardownTimeout caps `compose down -v` (default 5m).
	TeardownTimeout time.Duration
}

const (
	defaultWorkers             = 2
	defaultQueueDepth          = 256
	defaultRunTimeout          = 25 * time.Minute
	defaultBuildTimeout        = 3 * time.Hour
	defaultPackageBuildTimeout = 30 * time.Minute
	defaultTeardownTimeout     = 5 * time.Minute
)

func (o Options) withDefaults() Options {
	if o.Workers <= 0 {
		o.Workers = defaultWorkers
	}
	if o.QueueDepth <= 0 {
		o.QueueDepth = defaultQueueDepth
	}
	if o.RunTimeout <= 0 {
		o.RunTimeout = defaultRunTimeout
	}
	if o.BuildTimeout <= 0 {
		o.BuildTimeout = defaultBuildTimeout
	}
	if o.PackageBuildTimeout <= 0 {
		o.PackageBuildTimeout = defaultPackageBuildTimeout
	}
	if o.TeardownTimeout <= 0 {
		o.TeardownTimeout = defaultTeardownTimeout
	}
	return o
}

// composeFunc runs one `docker compose` invocation against a staged package,
// streaming everything it prints into log, and answers with the process's
// exit code.
//
// THE EXIT CODE IS NOT AN ERROR, and that separation is the seam the whole
// verdict rests on: `up --exit-code-from repro` exiting 1 is the repro's
// answer, not a failure of this call, so it comes back as (1, nil). A real
// error - docker missing, the directory gone - comes back as (-1, err) and
// is never read as a verdict.
type composeFunc func(ctx context.Context, dir, project string, log io.Writer, args ...string) (int, error)

// buildFunc builds project's binary at sha and returns the path it was
// cached at, writing its progress into log.
type buildFunc func(ctx context.Context, script, project, sha string, log io.Writer) (string, error)

// Runner is the queue, its workers, and the run records they produce. The
// zero value is not usable: build one with NewRunner.
type Runner struct {
	findings Findings
	projects Projects
	opt      Options

	// The four steps a run is made of, as fields for the reason version.go
	// gives for its own run field: a test drives the whole state machine -
	// including the harness-error branch, which is the branch that matters -
	// without a Docker daemon, a checkout or an hour of compiling. Two of
	// them are the resolver's and the packager's own methods, bound here
	// rather than reimplemented: this file decides WHEN a version is
	// resolved and a package staged, never how.
	resolve func(ctx context.Context, cfg ProjectConfig, version string) Version
	stage   func(ctx context.Context, cacheDir string, in RenderInput) (Result, error)
	build   buildFunc
	compose composeFunc

	mu   sync.Mutex
	runs map[int64]*Run
	next int64
	// indexErr is the last failure to write the run index beside the logs -
	// see index.go, which is where every word about it lives.
	indexErr error

	queue   chan int64
	wg      sync.WaitGroup
	started bool
	stopped bool
	cancel  context.CancelFunc
	ctx     context.Context
}

// NewRunner builds a runner that shells to the real git, docker and build
// script.
//
// IT TAKES NO PRINCIPAL, and that is the decision this file was rewritten
// for. It used to take one at construction and make every store call as it,
// which made the runner a daemon that read findings and signed verdicts
// under one name no matter who asked. A principal now arrives with each
// Enqueue and stays on the run record: see Run.principal.
func NewRunner(findings Findings, projects Projects, opt Options) (*Runner, error) {
	if findings == nil {
		return nil, errors.New("repro: a runner needs a store to read findings from")
	}
	if projects == nil {
		return nil, errors.New("repro: a runner needs a project config lookup")
	}
	opt = opt.withDefaults()
	if opt.LogDir == "" || opt.CacheDir == "" {
		return nil, errors.New("repro: a runner needs a log directory and a cache directory")
	}
	if err := os.MkdirAll(opt.LogDir, 0o755); err != nil {
		return nil, fmt.Errorf("repro: run log directory: %w", err)
	}
	if err := os.MkdirAll(opt.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("repro: package cache directory: %w", err)
	}
	r := &Runner{
		findings: findings,
		projects: projects,
		opt:      opt,
		resolve:  NewResolver().Resolve,
		stage:    NewPackager().StageForRun,
		build:    runBuildScript,
		compose:  dockerCompose,
		runs:     map[int64]*Run{},
		queue:    make(chan int64, opt.QueueDepth),
	}
	// The runs this binary did before it was restarted, so that GET /runs
	// answers the same thing after a restart that it answered before one.
	// See index.go on why an unreadable index refuses the runner.
	if err := r.loadIndex(); err != nil {
		return nil, err
	}
	return r, nil
}

// Start brings the worker pool up. Calling it twice is a no-op rather than a
// second pool.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return
	}
	r.started = true
	r.ctx, r.cancel = context.WithCancel(ctx)
	for i := 0; i < r.opt.Workers; i++ {
		r.wg.Add(1)
		go r.worker()
	}
}

// Stop cancels the runs in flight, waits for their workers to finish
// tearing down, and closes the queue.
//
// A run that was still QUEUED when this was called is marked an error
// naming the shutdown, not left sitting at "queued" forever: a caller
// polling a run id after a restart deserves an answer that says what
// happened, and "queued" on a runner that no longer exists is not one.
func (r *Runner) Stop() {
	r.mu.Lock()
	if !r.started || r.stopped {
		r.stopped = true
		r.mu.Unlock()
		return
	}
	r.stopped = true
	cancel := r.cancel
	r.mu.Unlock()

	cancel()
	r.wg.Wait()

	close(r.queue)
	for id := range r.queue {
		r.update(id, func(run *Run) {
			if run.Status != StatusQueued {
				return
			}
			run.Status = StatusError
			run.Note = "the runner stopped before this run started"
			run.EndedAt = nowPtr()
		})
	}
}

// ErrQueueFull is what Enqueue answers when the queue is at QueueDepth. See
// Options.QueueDepth on why this is refused rather than waited on.
var ErrQueueFull = errors.New("repro: the run queue is full")

// ErrStopped is what Enqueue answers after Stop.
var ErrStopped = errors.New("repro: the runner is stopped")

// Enqueue accepts one run per finding, all against the same version, ON
// BEHALF OF p, and returns their ids in the order the findings were given -
// runner.py's enqueue, which took a list because the console's "rerun
// everything against this version" button is one click over many findings.
//
// p IS REQUIRED AND IS NOT A DEFAULT. Every store call these runs make - the
// finding read, the repro tree read, the verdict write - is made as p and
// with no rights beyond p's, minutes later, on a worker goroutine. A nil
// here is refused rather than filled in from somewhere, because the one
// thing that must never happen quietly is a verdict signed by a name that
// did not ask for the run.
//
// The ids are handed out under the lock and are unique for the life of the
// process. Nothing else is validated here: a finding that does not exist, or
// has no repro tree, is a fact about the run, discovered by the worker and
// reported on the run record - not a reason to refuse the whole batch and
// leave the caller unable to see why.
func (r *Runner) Enqueue(
	p *store.Principal, project string, findingIDs []string, version string,
) ([]int64, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "latest"
	}
	if p == nil {
		return nil, errors.New("repro: a run is performed on behalf of somebody - " +
			"enqueue names the principal it reads and records as")
	}
	if len(findingIDs) == 0 {
		return nil, errors.New("repro: a run names at least one finding")
	}

	ids := make([]int64, 0, len(findingIDs))
	for _, fid := range findingIDs {
		fid = strings.TrimSpace(fid)
		if fid == "" {
			return ids, errors.New("repro: a run names a finding")
		}
		id, err := r.accept(p, project, fid, version)
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// accept mints one run record and puts it on the queue, both under the one
// lock. The send is inside the lock and not after it because Stop closes the
// queue under that same lock: a send that had passed the stopped check and
// then waited for the scheduler would be a send on a closed channel, which
// is a panic rather than an error. The send never blocks - the queue is
// buffered and the send is a non-blocking select - so holding the lock
// across it cannot deadlock.
func (r *Runner) accept(p *store.Principal, project, findingID, version string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return 0, ErrStopped
	}
	r.next++
	id := r.next
	run := &Run{
		ID: id, Finding: findingID, Project: project, Version: version, Status: StatusQueued,
		QueuedAt:  time.Now().UTC(),
		Log:       filepath.Join(r.opt.LogDir, fmt.Sprintf("run-%d.log", id)),
		principal: p,
	}
	r.runs[id] = run

	select {
	case r.queue <- id:
		r.saveIndex()
		return id, nil
	default:
		// The record stays, saying why: a caller who got ids back for the
		// findings before this one can still see what became of the one that
		// did not fit.
		run.Status = StatusError
		run.Note = fmt.Sprintf("the run queue is full (%d waiting)", r.opt.QueueDepth)
		run.EndedAt = nowPtr()
		r.saveIndex()
		return 0, ErrQueueFull
	}
}

// RunByID is one run's current record, or false if no such run. The record
// is a copy: reading it never races a worker writing it.
func (r *Runner) RunByID(id int64) (Run, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return Run{}, false
	}
	return run.clone(), true
}

// Runs is every run this process knows about, oldest id first - log order,
// which is the order they were asked for. A caller wanting newest-first (a
// drawer, say) reverses it; this does not decide that for them.
func (r *Runner) Runs() []Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Run, 0, len(r.runs))
	for _, run := range r.runs {
		out = append(out, run.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// update mutates one run under the lock. Every write to a run record goes
// through here, so no worker ever holds a *Run across a call that could
// block.
func (r *Runner) update(id int64, fn func(*Run)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[id]; ok {
		fn(run)
		r.saveIndex()
	}
}

func (r *Runner) worker() {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case id := <-r.queue:
			r.runOne(r.ctx, id)
		}
	}
}

// runOne is one run, start to verdict - runner.py's _run_one.
//
// It never returns an error: everything that can go wrong is a fact about
// this run, recorded on the run, because the caller that asked for it is a
// worker with nobody to report to. What a human reads is the record and the
// log, and both are written before this returns.
func (r *Runner) runOne(ctx context.Context, id int64) {
	run, ok := r.RunByID(id)
	if !ok {
		return
	}
	r.update(id, func(run *Run) {
		run.Status = StatusRunning
		run.StartedAt = nowPtr()
	})

	// Truncate rather than append. Run ids no longer start again in a new
	// process - the index carries the counter - so this is no longer about
	// two runs sharing a file; it is that a run writes its log from its own
	// first line, and a retry of an id would otherwise read as one long run.
	log, err := os.Create(run.Log)
	if err != nil {
		r.failNow(id, fmt.Sprintf("could not open the run log: %v", err))
		return
	}
	defer log.Close()

	in, err := r.RenderInputFor(ctx, run.principal, run.Finding, run.Version)
	if err != nil {
		fmt.Fprintf(log, "# run %d: %v\n", id, err)
		r.failNow(id, err.Error())
		return
	}
	r.update(id, func(run *Run) {
		run.SHA = in.Version.SHA
		run.Note = in.Version.Note
	})

	// A VERSION THAT DID NOT RESOLVE STOPS HERE, whatever kind it was. This
	// check used to live inside the source-build branch below, keyed on
	// Buildable, and so it never saw a release: Buildable is false for every
	// published release, and SourceBuild is false too, so a release whose
	// image could not be pulled walked straight past it and burned a full
	// compose build before failing as "package build failed" - a note that
	// blames the package for a version that was never obtainable. The answer
	// was already known at resolve time; this is where it gets read.
	if in.Version.Unresolved {
		fmt.Fprintf(log, "# run %d: %s\n", id, in.Version.Note)
		r.failNow(id, in.Version.Note)
		return
	}

	// A SOURCE BUILD WITHOUT A BINARY NEEDS ONE BUILT. This is the one place
	// the port deliberately reads differently from runner.py: there, ANY
	// version with no binary was either built or refused, because its only
	// release path always extracted one. Here a published release runs from
	// its own image and needs no local binary at all (see sutImage in
	// packager.go), so only a source build is gated on having one.
	if in.Version.SourceBuild && in.Version.Binary == "" {
		if !in.Version.Buildable {
			fmt.Fprintf(log, "# run %d: %s\n", id, in.Version.Note)
			r.failNow(id, in.Version.Note)
			return
		}
		binary, err := r.buildBinary(ctx, id, in, log)
		if err != nil {
			fmt.Fprintf(log, "# run %d: %v\n", id, err)
			r.failNow(id, err.Error())
			return
		}
		in.Version.Binary = binary
	}

	// PARITY: what runs here is the artifact we hand upstream. StageForRun
	// renders the same package BuildPackage does, plus the resolved binary
	// and a marker; the verdict is that package's own exit code, so a run
	// that passes here and a run someone else does from the tgz are the same
	// run.
	staged, err := r.stage(ctx, r.opt.CacheDir, in)
	if err != nil {
		fmt.Fprintf(log, "# run %d: %v\n", id, err)
		r.failNow(id, err.Error())
		return
	}

	project := fmt.Sprintf("flowy-repro-%d", id)
	fmt.Fprintf(log, "\n# run %d: %s against %s\n", id, run.Finding, in.Version.Note)
	fmt.Fprintf(log, "# what runs here is the artifact handed upstream - %s (compose project %s)\n\n",
		staged.Dir, project)

	r.execute(ctx, id, staged.Dir, project, log)

	// The teardown gets a context of its own, deliberately not derived from
	// the run's: a cancelled or timed-out run is exactly the run whose
	// containers and volumes are still up, and a `down -v` that inherited
	// the dead context would leave them there.
	down, cancel := context.WithTimeout(context.Background(), r.opt.TeardownTimeout)
	defer cancel()
	if _, err := r.compose(down, staged.Dir, project, log, "down", "-v"); err != nil {
		fmt.Fprintf(log, "\n# teardown failed: %v\n", err)
	}

	r.finish(ctx, id)
}

// execute is the two docker steps and the reading of what they meant. It
// writes the verdict onto the run record and nothing else - the teardown and
// the recording are runOne's, so that both happen on every path out of here.
func (r *Runner) execute(ctx context.Context, id int64, dir, project string, log *os.File) {
	// Build and up are two invocations rather than `up --build` for one
	// reason: a wrapper image that fails to build is not a verdict, and
	// folding it into the run would surface as the repro exiting nonzero -
	// the exact confusion this file exists to prevent.
	buildCtx, cancelBuild := context.WithTimeout(ctx, r.opt.PackageBuildTimeout)
	defer cancelBuild()
	code, err := r.compose(buildCtx, dir, project, log, "build")
	switch {
	case errors.Is(buildCtx.Err(), context.DeadlineExceeded):
		fmt.Fprintf(log, "\n# package build TIMEOUT after %s\n", r.opt.PackageBuildTimeout)
		r.fail(id, fmt.Sprintf("package build timeout after %s", r.opt.PackageBuildTimeout))
		return
	case err != nil:
		fmt.Fprintf(log, "\n# package build could not run: %v\n", err)
		r.fail(id, cancelledOr(buildCtx, fmt.Sprintf("package build could not run: %v", err)))
		return
	case code != 0:
		fmt.Fprintf(log, "\n# package build failed (exit %d)\n", code)
		r.fail(id, "package build failed")
		return
	}

	runCtx, cancelRun := context.WithTimeout(ctx, r.opt.RunTimeout)
	defer cancelRun()
	code, err = r.compose(runCtx, dir, project, log,
		"up", "--abort-on-container-exit", "--exit-code-from", "repro")
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(log, "\n# TIMEOUT after %s\n", r.opt.RunTimeout)
		r.fail(id, fmt.Sprintf("timeout after %s", r.opt.RunTimeout))
		return
	}
	if err != nil {
		fmt.Fprintf(log, "\n# the run could not start: %v\n", err)
		r.fail(id, cancelledOr(runCtx, fmt.Sprintf("the run could not start: %v", err)))
		return
	}
	if code == 0 {
		fmt.Fprint(log, "\n# repro exit 0 -> CONFIRMED\n")
		r.verdict(id, true, StatusConfirmed)
		return
	}

	// THE CHECK THIS FILE IS FOR. A nonzero exit is not believed until the
	// log has been read for a harness failure, because a broken sandbox and
	// a finding that no longer reproduces exit the same way.
	if err := log.Sync(); err != nil {
		fmt.Fprintf(log, "\n# could not flush the log before reading it: %v\n", err)
	}
	if why, ok := exitMeansHarness[code]; ok {
		fmt.Fprintf(log, "\n# repro exit %d -> ERROR (%s); not a verdict\n", code, why)
		r.fail(id, why)
		return
	}
	if herr := harnessError(log.Name()); herr != "" {
		fmt.Fprintf(log, "\n# repro exit %d -> ERROR (%s); not a verdict\n", code, herr)
		r.fail(id, herr)
		return
	}
	fmt.Fprintf(log, "\n# repro exit %d -> not confirmed\n", code)
	r.verdict(id, false, StatusNotConfirmed)
}

// buildBinary runs the build script for this version's commit, with the run
// marked StatusBuilding for as long as it takes.
func (r *Runner) buildBinary(ctx context.Context, id int64, in RenderInput, log *os.File) (string, error) {
	if r.opt.BuildScript == "" {
		return "", fmt.Errorf("%s needs a source build and no build script is configured, "+
			"so there is no binary for this commit and nothing that could make one", in.Version.Note)
	}
	r.update(id, func(run *Run) { run.Status = StatusBuilding })
	fmt.Fprintf(log, "# run %d: building %s@%s before running %s\n\n",
		id, in.Finding.Project, shortSHA(in.Version.SHA), in.Finding.ID)

	buildCtx, cancel := context.WithTimeout(ctx, r.opt.BuildTimeout)
	defer cancel()
	binary, err := r.build(buildCtx, r.opt.BuildScript, in.Finding.Project, in.Version.SHA, log)
	if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("build timeout after %s", r.opt.BuildTimeout)
	}
	if err != nil {
		if note := cancelledOr(buildCtx, ""); note != "" {
			return "", errors.New(note)
		}
		return "", fmt.Errorf("build failed: %w", err)
	}
	if binary == "" || !fileExists(binary) {
		return "", errors.New("build failed: the build script named no binary")
	}
	r.update(id, func(run *Run) { run.Status = StatusRunning })
	return binary, nil
}

// RenderInputFor assembles everything one package render needs for a finding
// at a version, READ AS p: the finding's own row, its repro tree read back
// out of its attachments, its project's config, and what the version string
// resolves to.
//
// It is exported because the trusted-host binary's package endpoint needs
// the identical assembly to call BuildPackage - the downloadable tgz and the
// run must describe the same finding at the same commit, and two places
// building this struct independently is how they would come to disagree.
//
// p is the run's own principal and not the runner's, for the reason
// Run.principal gives. A nil p is refused here too rather than read as
// "anybody": the store's filtered read is the only thing standing between a
// run and a finding its asker could not open.
func (r *Runner) RenderInputFor(
	ctx context.Context, p *store.Principal, findingID, version string,
) (RenderInput, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "latest"
	}
	if p == nil {
		return RenderInput{}, errors.New("repro: no principal on this run, so there is " +
			"nobody to read the finding as")
	}
	finding, err := r.findings.ReadArtifact(ctx, p, findingID, false)
	if err != nil {
		return RenderInput{}, err
	}
	if finding.Project == nil || *finding.Project == "" {
		return RenderInput{}, fmt.Errorf("finding %s names no project, so there is no source, "+
			"image or cache to run its repro against", findingID)
	}
	cfg, ok := r.projects(*finding.Project)
	if !ok {
		return RenderInput{}, fmt.Errorf("project %s has no repro configuration on this runner, "+
			"so there is nothing to resolve %s against", *finding.Project, version)
	}
	manifest, files, err := r.findings.ReadFindingRepro(ctx, p, findingID)
	if err != nil {
		return RenderInput{}, err
	}
	if manifest.Isolation == "" {
		manifest.Isolation = cfg.isolation()
	}
	return RenderInput{
		Finding: Finding{
			ID:      finding.ID,
			Project: *finding.Project,
			Num:     findingNum(finding),
			Title:   finding.Title,
			Report:  findingReport(finding),
		},
		Requested: version,
		Version:   r.resolve(ctx, cfg, version),
		Cfg:       cfg,
		Manifest:  manifest,
		Files:     files,
	}, nil
}

// verdict writes a run's answer onto its record. Both callers are in
// execute; recording it in the store is finish's, once, on the way out.
// verdict records what the repro decided, WITHOUT ending the run - the same
// contract fail() has, and for the same reason it gives. finish() publishes it
// once the teardown has happened and the finding's run log has the verdict on
// it. See Run.reached.
func (r *Runner) verdict(id int64, confirmed bool, status Status) {
	r.update(id, func(run *Run) {
		c := confirmed
		run.Confirmed = &c
		run.reached = status
	})
}

// fail ends a run without a verdict. Note carries the reason a human reads
// in the runs list; the log carries the detail.
// failNow is a failure with nothing left running - a log that would not open,
// an unresolvable version, a package that could not be staged. There are no
// containers to tear down, so the run ends where it failed.
func (r *Runner) failNow(id int64, note string) {
	r.fail(id, note)
	r.update(id, func(run *Run) {
		run.Status = StatusError
		run.EndedAt = nowPtr()
	})
}

func (r *Runner) fail(id int64, note string) {
	// THE NOTE AND THE VERDICT, NOT THE ENDING. A run is not finished while its
	// containers are still up, and teardown happens after this returns - so
	// marking it terminal here publishes a run that anything watching will treat
	// as done while `down -v` has not run yet. That is not only a racy test: a
	// caller that waits for terminal and starts the next run collides with the
	// previous one's containers and volumes.
	//
	// So this records WHY it failed and finish() ends it, once the teardown it
	// was waiting for has happened.
	r.update(id, func(run *Run) {
		run.Confirmed = nil
		if note != "" {
			run.Note = note
		}
		run.failed = true
	})
}

// finish stamps the end time and, ONLY if the run reached a verdict, records
// it on the finding's append-only run log.
//
// The recording goes through the store in this same process - no HTTP, no
// second node - so the verdict a human sees on the finding is the one this
// worker produced, and it is signed as the principal that ASKED for the run
// rather than as this process - which is what makes the finding's run log
// name a person or an agent. A store that
// refuses the write (a projectless finding, a principal that may not read it)
// leaves the run's own record standing and the reason in its note: the run
// happened, and losing the fact that it happened would be worse than
// recording it in one place instead of two.
func (r *Runner) finish(ctx context.Context, id int64) {
	run, ok := r.RunByID(id)
	if !ok {
		return
	}
	// THE RECORD FIRST, THE TERMINAL STATUS AFTER IT.
	//
	// This used to be the other way round, and the ordering was the bug rather
	// than a detail of it: anything waiting for this run - the console, a
	// caller, a test - sees Status go terminal and reads the finding's run log
	// in the same breath. Between those two moments the log did not have this
	// run's verdict in it yet, so the answer was "the run is done and there is
	// no verdict", which is empty-vs-absent wearing a timestamp.
	//
	// It failed roughly one gate run in a hundred and never on the box that
	// looked. See Run.reached for the measurement.
	//
	// The write is attempted for a run that reached a verdict and skipped for
	// one that did not - a failed run has nothing to record, which is not the
	// same as failing to record it.
	note := ""
	if run.Confirmed != nil {
		// The status the run ARRIVED at, which is what the log should carry.
		// run.Status is still non-terminal here by design, so reading it would
		// write "running" into an append-only record of finished runs.
		if _, err := r.findings.RecordFindingRun(ctx, run.principal, run.Finding, store.FindingRun{
			Version:   run.Version,
			SHA:       run.SHA,
			Confirmed: *run.Confirmed,
			Status:    string(run.reached),
		}); err != nil {
			note = " (verdict not recorded: " + err.Error() + ")"
		}
	}
	r.update(id, func(run *Run) {
		// The ending is stamped HERE, after teardown and after the recording,
		// for every path out. A failed run reaches its terminal status at the
		// same moment a confirmed one does: when there is nothing of it left
		// running and nothing left to write down.
		switch {
		case run.failed:
			run.Status = StatusError
		case run.reached != "":
			run.Status = run.reached
		}
		if run.EndedAt == nil {
			run.EndedAt = nowPtr()
		}
		if note != "" {
			run.Note = strings.TrimSpace(run.Note + note)
		}
	})
}

// cancelledOr answers the shutdown note when this run's context was
// cancelled, and otherwise the note the caller had.
//
// It exists because "the run could not start: context canceled" reads like a
// Docker fault to the operator who then goes looking for one, when what
// actually happened is that the runner was asked to stop while this run was
// in flight. A cancelled run is not a verdict either way - it is an error
// like any other here - but it deserves to say which error it was.
func cancelledOr(ctx context.Context, note string) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "the runner stopped while this run was in flight"
	}
	return note
}

// harnessSignatures are the strings that mean the harness failed rather than
// the repro answering - runner.py's _HARNESS_ERR, carried over verbatim
// because every one of them was added after a run that reported a
// not-reproduced it had no business reporting.
//
// The two spellings of "cannot connect to the Docker daemon" are both here
// on purpose: the match is case-sensitive, and Docker prints it both ways
// depending on which client said it.
var harnessSignatures = []string{
	"Error response from daemon",
	"OCI runtime create failed",
	"failed to create shim",
	"cannot connect to the Docker daemon",
	"Cannot connect to the Docker daemon",
	"inner dockerd did not come up",
	"is not present - build or pull",
	"manifest unknown",
	"pull access denied",
	"no space left on device",
	// The repro's own interpreter missing from the image. A tree whose run.sh
	// calls pip, python3 or bash and finds none of them exits 127 with this on
	// stderr, and without it here the run reads as "the finding no longer
	// reproduces" - which is the same as declaring it fixed. Measured: the
	// tiktoken tree, run.sh line 5, `pip: command not found`, recorded
	// confirmed=false on the finding's log.
	"command not found",
	"executable file not found",
	"no such file or directory",
}

// exitMeansHarness are exit codes that cannot be a verdict whatever the log
// says.
//
// 127 is the shell's "I could not find the command", and a repro that never
// ran cannot have failed to reproduce. It is listed as a CODE rather than left
// to the string match because the message is the shell's and varies - sh, bash
// and dash word it differently, and a busybox image words it differently again.
// The code is the fact; the message is one rendering of it.
//
// The asymmetry decides the doubt: a false harness error says "we could not
// tell", which costs a re-run. A false verdict says "this finding is fixed",
// which closes real work on evidence that never existed.
var exitMeansHarness = map[int]string{
	127: "the repro's interpreter or command was not in the image (exit 127)",
	126: "the repro was found but could not be executed - not executable, or a bad interpreter line (exit 126)",
}

// harnessErrorTail is how much of the end of a log is searched. The Python
// read the last 6000 bytes for the reason this does: a harness failure is
// the last thing printed before the exit, while a repro's own output can be
// megabytes of server log, and searching all of it would let a signature
// quoted inside the system under test's own output convict a run that was
// perfectly fine.
const harnessErrorTail = 6000

// harnessError answers the reason a run was broken rather than negative, or
// "" when nothing in the log's tail says the harness failed. A log that
// cannot be read answers "" - an unreadable log is not evidence of a
// harness failure, and treating it as one would turn every real
// not-reproduced into an error the moment the disk hiccuped.
func harnessError(path string) string {
	tail, err := readTail(path, harnessErrorTail)
	if err != nil {
		return ""
	}
	for _, sig := range harnessSignatures {
		if bytes.Contains(tail, []byte(sig)) {
			return "docker/setup error - not a verdict (" + sig + ")"
		}
	}
	return ""
}

// readTail reads at most n bytes from the end of a file.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if size := info.Size(); size > n {
		if _, err := f.Seek(size-n, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

// dockerCompose is the real composeFunc: `docker compose -p <project> <args>`
// in the staged directory, everything it prints going straight into the run
// log so a reader tailing it sees the run as it happens.
func dockerCompose(ctx context.Context, dir, project string, log io.Writer, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-p", project}, args...)...)
	cmd.Dir = dir
	cmd.Stdout = log
	cmd.Stderr = log
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// A process killed by the context reports -1 here; the caller checks
		// the context's own error before reading this as a verdict.
		return exit.ExitCode(), nil
	}
	return -1, err
}

// runBuildScript is the real buildFunc: scripts/build-sut.sh --project NAME
// <sha>.
//
// THE CONTRACT IS THE SCRIPT'S OWN, stated at the head of build-sut.sh: the
// last line of stdout is the path to the cached binary and nothing else goes
// to stdout, while every progress line and the whole compile go to stderr.
// So stdout is captured and stderr is streamed into the run log, and a
// caller reads the path back without having to parse an hour of compiler
// output for it.
func runBuildScript(ctx context.Context, script, project, sha string, log io.Writer) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, script, "--project", project, sha)
	cmd.Stdout = &out
	cmd.Stderr = log
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return lastLine(out.String()), nil
}

// lastLine is the last non-empty line of s, trimmed - build-sut.sh's answer.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// findingNum is the short, filename-safe token the package name carries for
// this finding - see Finding.Num in packager.go on why the packager does not
// derive one. A flowy id is an opaque ULID with no number in it, so this is
// its last six characters, lowercased: stable for the life of the finding,
// short enough to read in a package name, and distinct enough between two
// findings of the same project that their packages never collide.
func findingNum(finding *store.Artifact) string {
	id := strings.ToLower(finding.ID)
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}

// findingReport is the README's body: the finding's own write-up, or a
// pointer at the script when it has none, because a package whose README
// says nothing about what it is proving is a package nobody upstream can
// act on.
func findingReport(finding *store.Artifact) string {
	if body := strings.TrimSpace(finding.Body); body != "" {
		return body
	}
	if d := strings.TrimSpace(finding.Discovery); d != "" {
		return d
	}
	return "_(no written report for " + finding.ID + "; the assertion is encoded in the repro script.)_"
}

func nowPtr() *time.Time {
	t := time.Now().UTC()
	return &t
}
