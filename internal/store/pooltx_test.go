package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A write that is inside a transaction must not go back to the pool for a
// second connection.
//
// The pool is finite - SetMaxOpenConns in Open - and a transaction holds one of
// its connections for as long as it is open. So a write that begins a
// transaction and then asks the pool for another connection is holding one
// resource while queueing for the same resource, and once enough writers are in
// flight to have taken every connection, every one of them is waiting for a
// connection that only another one of them can release. Nothing breaks the
// cycle. They sit there until their contexts expire.
//
// The failure does not look like a deadlock from outside, which is how it lived
// here as long as it did. Under the pool size everything is quick; over it the
// writes still finish, because the context eventually cancels the inner query
// and the transaction rolls back - so what an operator sees is a request that
// took its whole timeout and then failed, and what a test suite sees is a slow
// test. Nobody goes looking for a lock cycle when the symptom is latency.
//
// The particular path this was found on: SetArtifactStatus signs the row it is
// about to write, and signing reads this node's key and the author's key. Those
// reads went through d.sql - the pool - even when the caller already had a
// transaction in hand, so MoveArtifactStatus, which is one transaction around a
// status update and the event that records it, took a second connection inside
// the first. Every artifact write signs, so every artifact write did this.
func TestWritesInsideATransactionDoNotTakeASecondConnection(t *testing.T) {
	ctx, db := open(t)

	// The number of writers comes from the pool rather than from a constant, so
	// the test keeps testing the same thing if the pool is ever resized. A
	// bigger pool moves the number at which this deadlocks; it does not remove
	// the deadlock, and a hardcoded 24 against a pool of 64 would quietly stop
	// exercising it.
	pool := db.SQL().Stats().MaxOpenConnections
	if pool <= 0 {
		t.Fatalf("the pool reports no limit (%d), so this test cannot exhaust it", pool)
	}
	writers := pool + 8

	project := declaredProject(t, ctx, db, "pooltx")
	owner := &User{Handle: "owner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// One artifact per writer. They could all move the same row, but then the
	// row lock would be in the picture too and a failure would not say which of
	// the two had caused it.
	arts := make([]*Artifact, writers)
	for i := range arts {
		art := &Artifact{
			Type: "bug", Project: &project, OwnerUser: owner.ID,
			Title: "concurrent mover " + ulid.NewString(), Status: "open",
		}
		if err := db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("upsert artifact %d: %v", i, err)
		}
		arts[i] = art
	}

	// The node's own signing key is read once and then held for the life of the
	// process, so warming it here keeps this test on the defect it is about.
	// Left cold, the first writer would fetch it from inside its transaction and
	// wedge on that instead, which is the same bug reached by a different read
	// and would make the failure harder to read.
	if _, err := db.Identity(ctx); err != nil {
		t.Fatalf("warm the node identity: %v", err)
	}

	// The budget is what turns a hang into a failure. Twenty-odd single-row
	// updates take milliseconds when nothing is queueing, so anything near this
	// is the cycle rather than a slow machine - and on a healthy tree the test
	// returns as fast as the writes do and never spends the budget.
	const budget = 10 * time.Second
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// All the writers are released at once. The cycle needs every connection to
	// be inside a transaction at the same moment, and starting them in a loop
	// without a gate lets the early ones commit before the late ones begin.
	var start sync.WaitGroup
	start.Add(1)

	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			e := &Event{
				ID: ulid.NewString(), Type: "status", Project: &project, Actor: owner.ID,
				Artifact: arts[i].ID, Parents: []string{}, Body: "open->triaged",
			}
			errs[i] = db.MoveArtifactStatus(runCtx, arts[i], "triaged", e)
		}()
	}

	began := time.Now()
	start.Done()
	wg.Wait()
	took := time.Since(began)

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d of %d: %v", i, writers, err)
		}
	}
	if t.Failed() {
		t.Fatalf("%d concurrent status moves against a pool of %d did not all land, after %s: "+
			"a write inside a transaction is going back to the pool for a second connection",
			writers, pool, took.Round(time.Millisecond))
	}
	if took > budget/2 {
		t.Errorf("%d concurrent status moves took %s against a pool of %d: "+
			"they are queueing for connections they are each already holding one of",
			writers, took.Round(time.Millisecond), pool)
	}
}
