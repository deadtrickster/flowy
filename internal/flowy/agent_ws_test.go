package flowy

// The one claim this design rests on, asserted rather than asserted-in-prose.
//
// The operator's reason for one socket per session was "we can have priorities".
// That is only true if something actually decides what goes out next, and the
// commit message saying so is not evidence. These run in the ordinary suite:
// they need no VM, no browser and no socket, which is the point of having
// pulled the loop out of the handler.

import (
	"context"
	"testing"
	"time"
)

// A control frame goes first even when output was queued long before it.
//
// REPEATED, AND THIS IS THE POINT OF THE TEST RATHER THAN A DETAIL. Go's select
// chooses uniformly at random among ready cases, so a loop with NO priority at
// all sends the control frame first about half the time. A single round passed
// with the priority deleted on purpose - a test that cannot fail, which is
// worse than no test. Requiring control to win every round of many makes the
// odds of a false green 2^-rounds, and turns a coin flip into an assertion.
func TestControlOutranksOutput(t *testing.T) {
	const backlog = 64
	const rounds = 50

	for round := 0; round < rounds; round++ {
		high := make(chan []byte, 4)
		low := make(chan []byte, backlog)
		// The low queue is filled FIRST and to capacity, so a loop that merely
		// drained in arrival order would have 64 frames to get through before
		// reaching the control frame.
		for i := 0; i < backlog; i++ {
			low <- []byte{agentStreamOut, byte(i)}
		}
		high <- []byte{agentStreamControl, 'X'}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		written := make(chan []byte, backlog+1)
		go pumpByPriority(ctx, high, low, func(frame []byte) bool {
			written <- frame
			return true
		})

		select {
		case first := <-written:
			if first[0] != agentStreamControl {
				cancel()
				t.Fatalf("round %d of %d: with %d output frames already queued and one control "+
					"frame, the first frame out was stream %d. The control frame is waiting its "+
					"turn, which is the one thing this design was chosen to avoid.",
					round+1, rounds, backlog, first[0])
			}
		case <-ctx.Done():
			cancel()
			t.Fatalf("round %d: nothing was written at all", round+1)
		}
		cancel()
	}
}

// And output is not starved: once the high queue is empty, low goes out.
//
// The obvious wrong implementation of a priority queue never drains the low one
// while anything at all keeps arriving on the high one. This asserts the other
// half, so "control first" cannot be satisfied by "control only".
func TestOutputStillGoesOutWhenControlIsQuiet(t *testing.T) {
	high := make(chan []byte, 1)
	low := make(chan []byte, 4)
	low <- []byte{agentStreamOut, 'a'}
	low <- []byte{agentStreamOut, 'b'}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	written := make(chan []byte, 4)
	go pumpByPriority(ctx, high, low, func(frame []byte) bool {
		written <- frame
		return true
	})

	for i := 0; i < 2; i++ {
		select {
		case frame := <-written:
			if frame[0] != agentStreamOut {
				t.Fatalf("expected an output frame, got stream %d", frame[0])
			}
		case <-ctx.Done():
			t.Fatal("output was starved with nothing on the control queue")
		}
	}
}

// A write that fails ends the loop rather than spinning on a dead socket.
func TestAFailedWriteStopsThePump(t *testing.T) {
	high := make(chan []byte, 1)
	low := make(chan []byte, 1)
	high <- []byte{agentStreamControl, 'X'}
	low <- []byte{agentStreamOut, 'y'}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	tries := 0
	go func() {
		pumpByPriority(ctx, high, low, func([]byte) bool {
			tries++
			return false
		})
		close(done)
	}()
	select {
	case <-done:
		if tries != 1 {
			t.Fatalf("the pump wrote %d times after the first write failed; a dead socket must "+
				"stop it, not be retried per frame", tries)
		}
	case <-ctx.Done():
		t.Fatal("the pump kept going after a write failed")
	}
}
