package flowy

// The one claim this design rests on, asserted rather than asserted-in-prose.
//
// The operator's reason for one socket per session was "we can have priorities".
// That is only true if something actually decides what goes out next, and the
// commit message saying so is not evidence. These run in the ordinary suite:
// they need no VM, no browser and no socket, which is the point of having
// pulled the loop out of the handler.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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

// A burst of output keeps every byte, in order, and does not cost the shell.
//
// WHY THIS SHAPE. The first version of this test drove the producer and the
// consumer against the same channel and called a coalescer that drained it -
// which is the race the production code had. It passed in a VM and the drainer
// failed it, correctly. A test that reproduces the bug it is guarding is not a
// guard.
//
// So the queue has exactly ONE reader, as it does in the node: the outbox holds
// what will not fit and offers it again with the next chunk. What is asserted
// is that every byte comes out, in order - a version that dropped the oldest
// bytes would also keep the session alive and would pass a check that only
// counted survival.
func TestABurstOfOutputKeepsEveryByte(t *testing.T) {
	const frames = 500
	low := make(chan []byte, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var want []byte
	for i := 0; i < frames; i++ {
		want = append(want, byte(i%251), byte((i/251)%251), '.')
	}

	// One reader, started first, draining as the producer works - the send
	// loop's role.
	got := make(chan []byte, 1)
	go func() {
		var seen []byte
		for frame := range low {
			seen = append(seen, frame[2:]...)
		}
		got <- seen
	}()

	out := &agentOutbox{slot: 7}
	for i := 0; i < frames; i++ {
		out.add(ctx, low, []byte{byte(i % 251), byte((i / 251) % 251), '.'})
	}
	// Whatever is still owed at the end is flushed the way the node does it:
	// by offering it again until the reader takes it.
	for len(out.pending) > 0 {
		out.add(ctx, low, nil)
	}
	close(low)

	select {
	case seen := <-got:
		if !bytes.Equal(seen, want) {
			t.Fatalf("%d chunks through a queue of 8 came out as %d bytes, want %d - a burst "+
				"must cost frames, never bytes, and never their order",
				frames, len(seen), len(want))
		}
	case <-ctx.Done():
		t.Fatal("the reader never finished")
	}
}

// And one terminal's bytes never end up in another's frame.
//
// Two slots share the queue once a socket carries more than one terminal, and
// the failure this rules out is silent: half a build pasted into somebody's
// editor reads as a corrupted terminal, not as a bug in a queue.
func TestTwoTerminalsDoNotMixOnOneQueue(t *testing.T) {
	low := make(chan []byte, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan map[byte][]byte, 1)
	go func() {
		bySlot := map[byte][]byte{}
		for frame := range low {
			bySlot[frame[1]] = append(bySlot[frame[1]], frame[2:]...)
		}
		done <- bySlot
	}()

	a := &agentOutbox{slot: 1}
	b := &agentOutbox{slot: 2}
	var wantA, wantB []byte
	for i := 0; i < 300; i++ {
		a.add(ctx, low, []byte{'a', byte(i % 251)})
		b.add(ctx, low, []byte{'b', byte(i % 251)})
		wantA = append(wantA, 'a', byte(i%251))
		wantB = append(wantB, 'b', byte(i%251))
	}
	for len(a.pending) > 0 {
		a.add(ctx, low, nil)
	}
	for len(b.pending) > 0 {
		b.add(ctx, low, nil)
	}
	close(low)

	select {
	case bySlot := <-done:
		if !bytes.Equal(bySlot[1], wantA) {
			t.Fatalf("slot 1 got %d bytes, want %d", len(bySlot[1]), len(wantA))
		}
		if !bytes.Equal(bySlot[2], wantB) {
			t.Fatalf("slot 2 got %d bytes, want %d", len(bySlot[2]), len(wantB))
		}
	case <-ctx.Done():
		t.Fatal("the reader never finished")
	}
}

// TestAdoptDoesNotStartAnything is the reattach rule, asserted as a difference
// rather than an absolute: the SAME absent session id, asked for two ways.
//
// It exists because the panel now attaches on mount so that navigating away and
// back does not lose a running shell. That is only safe if a reattach can come
// back empty-handed - otherwise merely opening the page boots a microVM, which
// is a much worse bug than the one it fixes.
//
// The registry count is what is asserted, not the error string. A refusal that
// started a VM first and then apologised would pass a message check.
func TestAdoptDoesNotStartAnything(t *testing.T) {
	s := &server{agents: newAgentShells()}
	r := httptest.NewRequest(http.MethodGet, "/api/agents/socket", nil)
	high := make(chan []byte, 4)
	low := make(chan []byte, 4)

	before := len(s.agents.by)

	a, why := s.attachAgent(context.Background(), r,
		agentControl{Type: "attach", Session: "01ABSENTSESSION", Adopt: true,
			Project: "flowy", Rows: 24, Cols: 80},
		high, low)
	if a != nil {
		t.Fatal("adopting a session that is not running handed back an attachment")
	}
	if why == "" {
		t.Fatal("refused without saying why, which a panel cannot report")
	}
	if got := len(s.agents.by); got != before {
		t.Fatalf("adopt started %d session(s); it must start none", got-before)
	}

	// THE OTHER HALF OF THE DIFFERENCE. The same absent id without adopt is a
	// request to START one, and must fail for a different reason - it gets as
	// far as looking for firecode or a project directory. If both paths gave
	// the same answer this test would pass against a node that ignored the
	// field entirely.
	_, whyStart := s.attachAgent(context.Background(), r,
		agentControl{Type: "attach", Session: "01ABSENTSESSION", Adopt: false,
			Project: "flowy", Rows: 24, Cols: 80},
		high, low)
	if whyStart == why {
		t.Fatalf("adopt and start refused identically (%q), so adopt was not read", why)
	}
}

// AN EXIT NOTICE MUST SURVIVE THE CANCEL THAT FOLLOWS IT.
//
// 01M14HN1VX1CXY8RAH314PQWA8 item 2, "shells that do not randomly exit". The
// row's lead was that the shell is not dying arbitrarily - the REASON is not
// arriving, so a normal exit and an unexplained one look identical on screen.
//
// This is the mechanism. When a session ends, agentshell.finish sets the reason
// and closes the reader channels; the reader goroutine sees the closed channel
// and queues an "exited" control frame carrying that reason. The frame goes on
// the high queue. Then the context ends - the same event that ended the session
// is what tears the socket down - and pumpByPriority selected ctx.Done() and
// returned, leaving the frame sitting in a channel nobody will read again. The
// handler's `defer conn.CloseNow()` is abrupt by design, so nothing flushes it
// either.
//
// The client then sees a close with no exited frame and prints its generic
// fallback, "the connection closed". The shell had a reason and said so; the
// reason died one hop from the wire.
//
// WHY A DRAIN IS SAFE HERE AND NOT A LEAK. Only `high` is drained, and only
// what is ALREADY queued - a non-blocking receive until empty. Control frames
// are hello, resize and exited: bounded, small, and never the megabytes that
// `low` can hold. Output is deliberately NOT drained, because a cancelled
// session's remaining scrollback is exactly what the caller no longer wants.
func TestAnExitNoticeSurvivesTheCancel(t *testing.T) {
	high := make(chan []byte, 4)
	low := make(chan []byte, 4)

	// The shape at the moment a shell exits: the notice is queued, and the
	// context that governed the session is already over.
	high <- []byte{agentStreamControl, 'E'}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	written := make(chan []byte, 4)
	done := make(chan struct{})
	go func() {
		pumpByPriority(ctx, high, low, func(frame []byte) bool {
			written <- frame
			return true
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pumpByPriority did not return after its context was cancelled")
	}

	select {
	case frame := <-written:
		if frame[0] != agentStreamControl {
			t.Fatalf("expected the queued control frame, got stream %d", frame[0])
		}
	default:
		t.Fatal("the exit notice was queued before the context ended and was never written. " +
			"That is what makes a shell look like it exited for no reason: the node knows why " +
			"and the panel is never told, so it falls back to \"the connection closed\" and a " +
			"clean exit is indistinguishable from a crash.")
	}
}
