package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deadtrickster/flowy/internal/repro"
	"github.com/deadtrickster/flowy/internal/store"
)

// newRunQueue builds the thing that actually runs repros.
//
// THIS FUNCTION IS THE ONLY PLACE THAT KNOWS WHICH RUNNER IS BEHIND THE
// PORT, and it is a file of its own for that reason: handoffs 08
// (internal/repro/runner.go - the queue, the worker pool, the build, the
// compose up, and the verdict) was written alongside this binary against a
// seam agreed in the room, and the two halves landed separately. Everything
// else here is written against runQueue, so which runner is behind it is an
// edit to this function and to nothing else.
//
// IT CAN STILL ANSWER "NO", AND SAYS WHY WHEN IT DOES. The refusal that used
// to stand here was about the runner not existing yet; it exists now, and
// the reason a deployment cannot run is a different one - this host has no
// docker command, so every run would fail as a harness error minutes after
// being accepted. That is refused at the door instead, by name, and /runs
// and /version report `linked: false` exactly as they did before: an empty
// run list from a process that cannot run anything is indistinguishable from
// one nobody has asked to do anything yet, and the difference is the whole
// question an operator is asking.
//
// The probe is for the BINARY and not for a live daemon on purpose. Whether
// `docker` is installed is a fact about this deployment that cannot change
// without a redeploy, so caching it at startup is honest. Whether the daemon
// is UP right now is not - it can come back between this check and the next
// run - and a startup probe of it would be a stale answer wearing a fresh
// one's clothes. A daemon that is down at run time is already named
// correctly by the runner's harness-error signatures, which is where that
// belongs.
func newRunQueue(cfg *Config, db *store.DB) (runQueue, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		log.Printf("run queue: not runnable on this host - %v", errNoDocker)
		return unrunnableQueue{err: errNoDocker}, nil
	}

	runner, err := repro.NewRunner(db, cfg.ProjectConfig, repro.Options{
		Workers:             cfg.Workers,
		LogDir:              cfg.LogDir,
		CacheDir:            cfg.CacheDir,
		BuildScript:         cfg.BuildScript,
		RunTimeout:          cfg.RunTimeout.Duration(),
		BuildTimeout:        cfg.BuildTimeout.Duration(),
		PackageBuildTimeout: cfg.PackageBuildTimeout.Duration(),
	})
	if err != nil {
		return nil, err
	}
	// The pool's context is the process's and not any request's: a run
	// outlives the POST that asked for it by design, and Close - which main
	// defers - is what stops it.
	runner.Start(context.Background())
	log.Printf("run queue: linked, %d worker(s), logs in %s", cfg.Workers, cfg.LogDir)
	return &linkedQueue{
		runner: runner, enqueue: runner.Enqueue,
		db: db, cfg: cfg, projects: map[int64]string{},
	}, nil
}

// errNoDocker is what a deployment with no docker command answers with,
// keeping errQueueUnlinked's shape: a refusal that names the missing thing.
var errNoDocker = errors.New(
	"this host has no `docker` command, so nothing here can run a repro: /run, /runs and " +
		"/run/{id}/log refuse rather than accept runs that could only fail. " +
		"GET /version and GET /package work")

// unrunnableQueue is the runQueue a deployment gets when the host cannot run
// containers at all. Every verb refuses with the same sentence, and `linked`
// reads false - see unlinkedQueue, whose reasoning this shares.
type unrunnableQueue struct{ err error }

func (q unrunnableQueue) Enqueue(context.Context, *store.Principal, string, string) (string, error) {
	return "", q.err
}
func (q unrunnableQueue) Run(string) (Run, bool) { return Run{}, false }
func (q unrunnableQueue) Runs() []Run            { return nil }
func (q unrunnableQueue) Close() error           { return nil }
func (q unrunnableQueue) unlinkedBecause() error { return q.err }
func (unlinkedQueue) unlinkedBecause() error     { return errQueueUnlinked }

// linkedQueue is the adapter between this binary's runQueue and the runner:
// string ids out here, int64s in there; unix seconds out here, time.Time in
// there; newest first out here, log order in there.
//
// IT IS ALSO WHERE THE INTERFACE'S TWO REFUSALS LIVE - no repro tree, and a
// project this runner is not configured for. The runner itself deliberately
// validates nothing at Enqueue, because a batch of twenty findings must not
// be refused whole over the third one; but a caller handing over ONE finding
// wants to be told now rather than to poll a run that was only ever going to
// end as an error. Both are right, and this is the seam between them: the
// per-finding answer POST /run gives comes from here, and the run record the
// worker writes is still the record of anything discovered later.
//
// Every check here reads AS THE CALLER, never as this process. mayRead at
// the door has already asked the same question, and asking twice is one
// query against being right by construction rather than by what some other
// file happens to do first.
type linkedQueue struct {
	runner *repro.Runner
	db     *store.DB
	cfg    *Config

	// enqueue is runner.Enqueue, as a field for the reason internal/repro
	// makes its own four steps fields: the checks above are the interesting
	// half of this type and a test that had to stand up a worker pool and a
	// Docker daemon to reach them would not be run.
	enqueue func(p *store.Principal, findingIDs []string, version string) ([]int64, error)

	// projects remembers which project each accepted run belongs to, because
	// repro.Run does not carry one and this binary's Run does. It is written
	// once at Enqueue, from the finding that was just read.
	mu       sync.Mutex
	projects map[int64]string
}

// Enqueue accepts one finding to run at one version, on behalf of p.
func (q *linkedQueue) Enqueue(
	ctx context.Context, p *store.Principal, finding, version string,
) (string, error) {
	art, err := q.db.ReadArtifact(ctx, p, finding, false)
	if err != nil {
		// The store does not distinguish "not there" from "not yours", and
		// neither does this - but it says WHICH finding, because a refusal
		// that reads "store: not found" in a list of per-finding answers
		// tells the caller nothing about which of their findings it is
		// about. NotAFindingError unwraps to the store's own ErrNotFound, so
		// the status is still the store's decision and not a second one.
		if errors.Is(err, store.ErrNotFound) {
			return "", store.NotAFindingError{ID: finding}
		}
		return "", err
	}
	if art.Type != findingType {
		return "", refuse(http.StatusBadRequest, fmt.Sprintf(
			"%s is a %s, not a finding - only a finding carries a repro tree",
			finding, art.Type))
	}
	if art.Project == nil || *art.Project == "" {
		return "", refuse(http.StatusBadRequest, fmt.Sprintf(
			"finding %s belongs to no project, so there is no source checkout, "+
				"base image or cache to run it against", finding))
	}
	project := *art.Project
	if _, ok := q.cfg.ProjectConfig(project); !ok {
		return "", refuse(http.StatusNotFound, fmt.Sprintf(
			"this runner is not configured for project %q - it holds %s",
			project, strings.Join(q.cfg.ProjectNames(), ", ")))
	}
	// The manifest off the row already in hand: whether a finding has a
	// repro tree is answerable without fetching one byte of it, and fetching
	// megabytes to decide whether to accept would make the refusal cost more
	// than the run.
	manifest, files, err := store.FindingRepro(art)
	if err != nil {
		return "", refuse(http.StatusConflict, err.Error()+" - there is nothing to run")
	}
	if len(files) == 0 {
		return "", refuse(http.StatusConflict, fmt.Sprintf(
			"finding %s has no repro tree recorded - there is nothing to run", finding))
	}
	if err := checkIsolation(manifest.Isolation); err != nil {
		return "", err
	}

	ids, err := q.enqueue(p, []string{finding}, version)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", errors.New("the run queue accepted the finding and named no run")
	}
	q.mu.Lock()
	q.projects[ids[0]] = project
	q.mu.Unlock()
	return strconv.FormatInt(ids[0], 10), nil
}

// Run is one run by id. An id that is not a number is not a run this process
// ever handed out, and answers false rather than an error: the door turns
// that into the same 404 it gives for a run it never had.
func (q *linkedQueue) Run(id string) (Run, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return Run{}, false
	}
	run, ok := q.runner.RunByID(n)
	if !ok {
		return Run{}, false
	}
	return q.record(run), true
}

// Runs is every run this process knows about, NEWEST FIRST - runQueue's
// order, not the runner's, which is log order.
func (q *linkedQueue) Runs() []Run {
	runs := q.runner.Runs()
	out := make([]Run, 0, len(runs))
	for _, run := range runs {
		out = append(out, q.record(run))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Close stops the workers and waits for the run in flight.
func (q *linkedQueue) Close() error {
	q.runner.Stop()
	return nil
}

// record translates one repro.Run into what this binary reports.
//
// The three times go out as unix SECONDS because that is what the console
// reads (web/src/lib/api.ts, ReproRun) - and a zero time goes out as zero
// rather than as the start of the epoch, so `omitempty` drops it and a
// queued run reads as having no start rather than as having started in 1970.
func (q *linkedQueue) record(run repro.Run) Run {
	q.mu.Lock()
	project := q.projects[run.ID]
	q.mu.Unlock()
	return Run{
		ID:        strconv.FormatInt(run.ID, 10),
		Finding:   run.Finding,
		Project:   project,
		Version:   run.Version,
		SHA:       run.SHA,
		Status:    string(run.Status),
		Confirmed: run.Confirmed,
		Note:      run.Note,
		QueuedAt:  unixSeconds(&run.QueuedAt),
		StartedAt: unixSeconds(run.StartedAt),
		EndedAt:   unixSeconds(run.EndedAt),
		LogPath:   run.Log,
	}
}

func unixSeconds(t *time.Time) int64 {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.Unix()
}
