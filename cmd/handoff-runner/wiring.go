package main

import (
	"log"

	"github.com/deadtrickster/flowy/internal/store"
)

// newRunQueue builds the thing that actually runs repros.
//
// THIS FUNCTION IS THE ONLY PLACE THAT KNOWS WHICH RUNNER IS BEHIND THE
// PORT, and it is a file of its own for that reason: handoffs 08
// (internal/repro/runner.go - the queue, the worker pool, the build, the
// compose up, and the verdict) was written alongside this binary against a
// seam agreed in the room, and the two halves land separately. Everything
// else here is written against runQueue, so wiring the real runner in is an
// edit to this function and to nothing else.
//
// Until it lands, the port is filled by a refusal that says so by name. That
// is deliberately not an empty queue: /runs answering "[]" from a process
// that cannot run anything looks exactly like a process nobody has asked to
// do anything yet, and the difference is the whole question an operator is
// asking. The other three routes - /version, /package and /healthz - need
// nothing from the runner and work fully.
func newRunQueue(cfg *Config, db *store.DB) (runQueue, error) {
	_, _ = cfg, db
	log.Printf("run queue: not linked in this build - %v", errQueueUnlinked)
	return unlinkedQueue{}, nil
}
