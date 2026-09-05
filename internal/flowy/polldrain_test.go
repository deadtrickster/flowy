package flowy

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A DEPLOY IS THE WINDOW ENDING, NOT THE CLIENT LEAVING.
//
// 01M17K62JD1JGTQM2BRRTND18H. errClientGone says "nobody is there to write to".
// On a restart that is false: the waiter is still on the other end and still
// listening, and answering it with nothing drops its connection, which its
// monitor reports as LISTENER REFUSED. Measured 6046 times since 2026-08-23, in
// bursts of 24 to 34 inside one second - every waiter on the node at once.
//
// So the fleet's normal act of shipping a change deafened every seat and told
// them the node was broken.
func TestADrainEndsAPollAsAQuietWindow(t *testing.T) {
	t.Run("a draining node answers, it does not hang up", func(t *testing.T) {
		draining := make(chan struct{})
		go func() {
			time.Sleep(10 * time.Millisecond)
			close(draining)
		}()

		start := time.Now()
		err := pollUntil(context.Background(), draining, time.Minute, func() (bool, error) {
			return false, nil
		})

		if errors.Is(err, errClientGone) {
			t.Fatal("a drain was reported as the client going away - the client is still there, and it gets a severed connection instead of a page")
		}
		if err != nil {
			t.Fatalf("a drain ended the poll with %v - clients handle an empty window, they do not handle a new error", err)
		}
		// The window was a minute. If it actually waited one, Shutdown's ten
		// second grace expires first and the connections are severed anyway,
		// which is the whole defect.
		if waited := time.Since(start); waited > 5*time.Second {
			t.Fatalf("the poll took %v to notice the drain - Shutdown gives up after 10s and severs it", waited)
		}
	})

	// THE HALF THAT KEEPS IT HONEST. If a drain were the only way out, a client
	// that really did hang up would get a page written to a closed connection
	// and a broken pipe in the log - the thing errClientGone exists to prevent.
	t.Run("a client that really left is still errClientGone", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := pollUntil(ctx, make(chan struct{}), time.Second, func() (bool, error) {
			return false, nil
		})

		if !errors.Is(err, errClientGone) {
			t.Fatalf("a cancelled request ended with %v - writing a page to a connection nobody holds logs a broken pipe", err)
		}
	})

	// A server that was never wired for shutdown has a nil channel, and a
	// select on nil blocks forever. That is the correct default: it must not
	// turn every poll into an instant empty answer.
	t.Run("an unwired server never drains", func(t *testing.T) {
		start := time.Now()
		err := pollUntil(context.Background(), nil, 40*time.Millisecond, func() (bool, error) {
			return false, nil
		})
		if err != nil {
			t.Fatalf("a nil drain channel ended the poll with %v", err)
		}
		if waited := time.Since(start); waited < 30*time.Millisecond {
			t.Fatalf("the poll returned after %v, well before its 40ms window - a nil channel fired", waited)
		}
	})
}
