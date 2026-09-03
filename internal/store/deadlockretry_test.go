package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"sync/atomic"
	"testing"

	"github.com/lib/pq"
)

// A READ ABORTED AS A DEADLOCK VICTIM IS RETRIED, NOT RAISED.
//
// 01M17K62JD1JGTQM2BRRTND18H. On 2026-08-31 at 17:22:12 a listener blocked in
// /api/inbox/wait for 20.998 seconds was aborted with "store: list events: pq:
// deadlock detected" and answered 500, refusing an agent's inbox - on a node
// running 8370573, eleven hours after the lock_timeout fix.
//
// WHY THIS USES A FAKE DRIVER RATHER THAN A REAL DEADLOCK, which is a decision
// worth defending because a real one is more convincing when it works.
//
// A real deadlock IS reproducible: two psql sessions against a throwaway
// database, deterministically, both ways round - the side that blocks last is
// the side Postgres aborts. That is how the premise of this fix was
// established, and it is written up on the row.
//
// It cannot be driven from a Go test against the code under test. The psql
// version makes the reader hold locks ACROSS statements inside a transaction;
// the real read path has none - ListEvents runs straight on the pool
// (chat.go:955), readPage is one statement per call, and pageOf's two calls do
// not share a connection. A single statement takes all its table locks during
// planning, near-atomically, so it cannot be made to block between them on
// demand. I wrote two versions that tried and BOTH PASSED WITH THE RETRY
// REMOVED - checks that could not fail.
//
// So this tests the thing the change actually adds: that a 40P01 on the first
// attempt is retried and the caller gets the rows. The driver returns the
// deadlock once and then succeeds, which is the real-world shape - the other
// side of a cycle commits, and the field is clear.
type deadlockOnceDriver struct{ attempts atomic.Int32 }

type deadlockOnceConn struct{ d *deadlockOnceDriver }

type oneIntRows struct{ done bool }

func (d *deadlockOnceDriver) Open(string) (driver.Conn, error) { return &deadlockOnceConn{d: d}, nil }

func (c *deadlockOnceConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *deadlockOnceConn) Close() error                        { return nil }
func (c *deadlockOnceConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c *deadlockOnceConn) QueryContext(
	_ context.Context, _ string, _ []driver.NamedValue,
) (driver.Rows, error) {
	if c.d.attempts.Add(1) == 1 {
		// Exactly what lib/pq hands up when Postgres breaks a cycle.
		return nil, &pq.Error{Code: "40P01", Message: "deadlock detected"}
	}
	return &oneIntRows{}, nil
}

func (r *oneIntRows) Columns() []string { return []string{"n"} }
func (r *oneIntRows) Close() error      { return nil }
func (r *oneIntRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}

// REGISTERED ONCE PER PROCESS, NOT PER TEST. sql.Register panics on a repeated
// name, so registering inside the test would blow up under `-count=2` - which is
// how somebody chasing a flake runs it. The counter is reset instead.
var fakeDeadlockDriver = &deadlockOnceDriver{}

const fakeDeadlockName = "flowy-deadlock-once"

func init() { sql.Register(fakeDeadlockName, fakeDeadlockDriver) }

func TestADeadlockedReadIsRetried(t *testing.T) {
	fake := fakeDeadlockDriver
	fake.attempts.Store(0)

	pool, err := sql.Open(fakeDeadlockName, "")
	if err != nil {
		t.Fatalf("opening the fake pool: %v", err)
	}
	defer pool.Close()
	// One connection, so the retry cannot quietly land on a second one that has
	// its own attempt counter - the point is that the SAME reader tries again.
	pool.SetMaxOpenConns(1)

	db := &DB{sql: pool}
	out, err := readPage(context.Background(), db, "deadlock probe",
		func(_ *args) string { return `SELECT 1` },
		func(sc scanner) (int, error) {
			var n int
			return n, sc.Scan(&n)
		})

	if err != nil {
		t.Fatalf("a read aborted as a deadlock victim reached the caller as an error: %v\n"+
			"Every door above readPage turns that into a 500, for a condition Postgres defines "+
			"as retryable and which clears the moment the other side of the cycle commits. A "+
			"read here is one statement on the pool with no transaction of its own, so running "+
			"it again is identical to running it.", err)
	}
	if len(out) != 1 {
		t.Fatalf("the retry returned %d rows, want 1 - it recovered from the deadlock but lost the answer", len(out))
	}
	if got := fake.attempts.Load(); got != 2 {
		t.Fatalf("the query ran %d time(s), want exactly 2: one deadlocked and one retry. "+
			"More would mean this retries in a loop, which would smooth away a second deadlock "+
			"that deserves to be seen.", got)
	}
}

// A RETRY THAT NOTHING COUNTS IS A MEASUREMENT DELETED, so the count is the
// thing under test here rather than a detail of it. The retry above was landed
// silent, and that removed the only signal two open rows had: with no counter, a
// retried deadlock and a deadlock that never happened produce identical output,
// so a quiet week says nothing about whether the cause is fixed.
//
// ASSERTS A DIFFERENCE, NOT AN ABSOLUTE. deadlockRetryTotal is package-level and
// lives for the process, so any other test in this package that trips a retry
// moves it. Reading it before and after and asserting the delta is a question
// this test can own; asserting it equals 1 would pass alone and fail in a suite,
// blaming whatever ran first.
func TestARetriedDeadlockIsCounted(t *testing.T) {
	fake := fakeDeadlockDriver
	fake.attempts.Store(0)

	const what = "deadlock counter probe"
	beforeTotal, beforeBy := DeadlockRetries()

	pool, err := sql.Open(fakeDeadlockName, "")
	if err != nil {
		t.Fatalf("opening the fake pool: %v", err)
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)

	db := &DB{sql: pool}
	if _, err := readPage(context.Background(), db, what,
		func(_ *args) string { return `SELECT 1` },
		func(sc scanner) (int, error) {
			var n int
			return n, sc.Scan(&n)
		}); err != nil {
		t.Fatalf("the retried read failed: %v", err)
	}

	afterTotal, afterBy := DeadlockRetries()
	if d := afterTotal - beforeTotal; d != 1 {
		t.Fatalf("the total count moved by %d, want 1. A retry nothing counts is why "+
			"\"no deadlock 500s since 2026-09-01\" could not be told apart from the rate "+
			"being unchanged and hidden.", d)
	}
	if d := afterBy[what] - beforeBy[what]; d != 1 {
		t.Fatalf("the count for %q moved by %d, want 1. The read's NAME is the half that "+
			"matters: finding the cause needed to know which statement was retried, and "+
			"without it that meant reading the database's own log.", what, d)
	}
}
