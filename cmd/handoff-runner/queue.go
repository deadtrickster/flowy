package main

import (
	"context"
	"errors"

	"github.com/deadtrickster/flowy/internal/store"
)

// runQueue is the seam between this binary and the runner itself (handoffs
// 08, internal/repro/runner.go): the queue, the worker pool, the build, the
// docker compose up, and the verdict it writes.
//
// IT IS DECLARED HERE, BY THE CALLER, and that is deliberate rather than
// idiomatic tidiness. Two agents built these two halves at the same time
// against a seam agreed in the room; an interface the consumer owns is one
// where the details of the other half can differ from what was agreed
// without this half being wrong. Only these three verbs are load-bearing.
//
// THE CALLER'S PRINCIPAL IS CARRIED INTO EVERY CALL, and where it is not
// honoured underneath, the door checks first. This process runs where the
// repro actually executes, with a Docker socket and a source checkout that
// no agent in the fleet has; what it must NOT also have is a wider view of
// the findings than the caller does, or authenticating here becomes a way to
// read a finding you could not read through Flowy.
//
// The runner behind this port is a daemon and reads as ONE principal of its
// own, minutes after the request that caused the work - which is defensible
// for the reading a run does, and is not an answer to "was this caller
// allowed to ask". So handleRun asks the store that question itself before
// it enqueues anything, and handleRuns and handleRunLog ask it again on the
// way back out. The principal in the signature below is what an
// implementation should use where it can; the door does not rely on it.
type runQueue interface {
	// Enqueue accepts one finding to run at one version and returns the id
	// of the run it queued. It refuses rather than queues when the finding
	// has no repro tree, or when its project is not one this runner is
	// configured for.
	Enqueue(ctx context.Context, p *store.Principal, finding, version string) (string, error)
	// Run is one run by id, and false when there is no such run.
	Run(id string) (Run, bool)
	// Runs is every run the process knows about, newest first.
	Runs() []Run
	// Close stops the workers and waits for the run in flight.
	Close() error
}

// Run is one repro run as this binary reports it.
//
// STATUS IS THE WHOLE VALUE OF THE RECORD, and the reason it has five states
// rather than two: `error` is not a red verdict. runner.py separated a
// harness failure - the inner Docker daemon not coming up, an image that
// could not be pulled, no space left on the device - from a genuine
// non-reproduction, because a broken sandbox reported as not-confirmed is a
// finding silently declared fixed. Confirmed is nil for exactly the states
// that are not a verdict.
type Run struct {
	ID      string `json:"id"`
	Finding string `json:"finding"`
	Project string `json:"project"`
	// Version is the version string as asked for - "latest", a branch, a
	// release tag - and SHA is what it resolved to. Both are kept: "latest"
	// is a different fact from the commit it happened to be at.
	Version string `json:"version"`
	SHA     string `json:"sha,omitempty"`
	// Status is queued|building|running|confirmed|not-confirmed|error.
	Status string `json:"status"`
	// Confirmed is the verdict, and nil while there is not one yet or when
	// the run ended in error. A false here means the repro really did not
	// reproduce; a nil means nobody knows.
	Confirmed *bool  `json:"confirmed"`
	Note      string `json:"note,omitempty"`
	QueuedAt  int64  `json:"queued_at,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
	EndedAt   int64  `json:"ended_at,omitempty"`
	// LogPath is where the run's log is on this host. It is never in the
	// JSON: a host path is this machine's business, and GET /run/{id}/log is
	// how a caller reads the log without learning where it lives.
	LogPath string `json:"-"`
}

// errQueueUnlinked is what the three queue-backed routes answer with when
// this binary was built before handoffs 08 landed. It is a refusal with a
// name in it rather than a 500 or an empty list, because an empty /runs from
// a runner that cannot run anything is indistinguishable from a runner that
// has simply not been asked to do anything yet.
var errQueueUnlinked = errors.New(
	"this build has no run queue linked: /run, /runs and /run/{id}/log arrive with " +
		"handoffs 08 (internal/repro/runner.go). GET /version and GET /package work")

// unlinkedQueue is the runQueue a build without handoffs 08 gets. Every verb
// refuses with the same sentence.
type unlinkedQueue struct{}

func (unlinkedQueue) Enqueue(context.Context, *store.Principal, string, string) (string, error) {
	return "", errQueueUnlinked
}
func (unlinkedQueue) Run(string) (Run, bool) { return Run{}, false }
func (unlinkedQueue) Runs() []Run            { return nil }
func (unlinkedQueue) Close() error           { return nil }

// linked reports whether the queue behind this binary can actually run
// anything - what /version's answer says about itself, so an operator can
// see from one request whether this deployment runs repros or only packages
// them.
func linked(q runQueue) bool {
	_, unlinked := q.(unlinkedQueue)
	return !unlinked
}
