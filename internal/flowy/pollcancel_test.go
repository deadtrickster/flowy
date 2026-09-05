package flowy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A poll cancelled during its query is the client going, not a server fault.
//
// 01M17K62JD1JGTQM2BRRTND18H. pollUntil already turned a cancelled context into
// errClientGone when it was waiting BETWEEN looks. A poll cancelled DURING a
// look returned the query's own error, and the handler turned that into 500 -
// so every deploy answered its blocked waiters with "500 internal error".
//
// 6046 of those in syslog since 2026-08-23, in bursts of 24 to 34 inside a
// single second: every waiter on the node failing together, which is what a
// restart looks like from the inside. Each one exits a listener, so shipping a
// change deafened every seat and reported it as the node being broken.
func TestACancelledPollIsNotAServerError(t *testing.T) {
	t.Run("a query error after cancellation is the client going", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		err := pollUntil(ctx, nil, time.Second, func() (bool, error) {
			cancel() // the server begins shutting down mid-query
			return false, context.Canceled
		})
		if !errors.Is(err, errClientGone) {
			t.Fatalf("a poll cancelled during its look answered %v - the handler turns that into a 500, and a restart is not a fault", err)
		}
	})

	// THE ERROR IS NOT INSPECTED, THE CONTEXT IS. A driver may wrap or rename
	// its cancellation; what makes this not-a-fault is that the request is over.
	t.Run("a wrapped or renamed cancellation is still the client going", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		err := pollUntil(ctx, nil, time.Second, func() (bool, error) {
			cancel()
			return false, fmt.Errorf("driver: %w", errors.New("connection closed mid-query"))
		})
		if !errors.Is(err, errClientGone) {
			t.Fatalf("a cancelled request whose driver spelled the error its own way answered %v", err)
		}
	})

	// AND A REAL FAULT MUST STILL BE ONE. This is the half that would make the
	// fix worse than the defect: swallowing every query error would turn a
	// broken database into a waiter that quietly reports nothing to say.
	t.Run("a real error on a live request is still an error", func(t *testing.T) {
		boom := errors.New("the store is on fire")
		err := pollUntil(context.Background(), nil, time.Second, func() (bool, error) {
			return false, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("a genuine store failure was reported as %v - a waiter told 'nothing to say' by a broken node is the silence this fleet keeps paying for", err)
		}
	})

	t.Run("a poll that finds something still says so", func(t *testing.T) {
		if err := pollUntil(context.Background(), nil, time.Second, func() (bool, error) {
			return true, nil
		}); err != nil {
			t.Fatalf("a successful poll answered %v", err)
		}
	})
}
