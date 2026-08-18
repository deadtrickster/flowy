package main

// THE WRITE DEADLINE, proved rather than asserted.
//
// serve.go runs the API on an http.Server with WriteTimeout set. Go stamps that
// deadline onto the connection when the request starts, and a response still
// being written when it expires is cut - which for a server-sent event stream
// means every console loses its connection on a fixed clock, forever, with
// nothing on either side saying why. A stream that dies looks EXACTLY like a
// room where nothing is happening, which is the defect the stream was built to
// end, reintroduced one layer down.
//
// So this is two arms and the assertion is the DIFFERENCE between them. One
// stream clears the deadline through the same function the door calls; the
// other does not. A single arm could not tell "the deadline was cleared" from
// "the test finished before the deadline did", which is a check that cannot go
// red - and a green light nobody rechecks is worse than no check.
//
// The fixture's timeout is milliseconds, so this proves the mechanism rather
// than the number 60 and costs about a second to run.

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// deadlineFixtureTimeout is the stand-in for serve.go's WriteTimeout. Short
// enough to measure quickly, long enough that a loaded machine still gets
// several writes in before it.
const deadlineFixtureTimeout = 200 * time.Millisecond

// streamFor runs a server whose WriteTimeout is deadlineFixtureTimeout and
// whose handler writes a line every 20ms for a second, and answers how many
// lines the client managed to read.
//
// `clear` says whether the handler calls clearWriteDeadline first - the one
// difference between the two arms.
func streamFor(t *testing.T, clear bool) int {
	t.Helper()

	const every = 20 * time.Millisecond
	const lines = 50 // one second of writing, five times the deadline

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		if clear {
			if err := clearWriteDeadline(rc); err != nil {
				t.Errorf("clearing the write deadline: %v", err)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < lines; i++ {
			if _, err := fmt.Fprintf(w, "data: %d\n\n", i); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
			time.Sleep(every)
		}
	}))
	srv.Config.WriteTimeout = deadlineFixtureTimeout
	srv.Start()
	defer srv.Close()

	res, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer res.Body.Close()

	read := 0
	scan := bufio.NewScanner(res.Body)
	for scan.Scan() {
		if scan.Text() != "" {
			read++
		}
	}
	return read
}

func TestAStreamOutlivesTheServersWriteTimeoutOnlyWhenTheDeadlineIsCleared(t *testing.T) {
	// How many lines a stream gets out before the fixture's deadline. Anything
	// at or below this is a stream that was cut by the timeout.
	const beforeTheDeadline = int(deadlineFixtureTimeout / (20 * time.Millisecond))

	cut := streamFor(t, false)
	if cut > beforeTheDeadline+4 {
		t.Fatalf(`a stream that did NOT clear the write deadline read %d lines.

That is more than the %dms fixture timeout allows, so the deadline is not being
enforced here and this check cannot tell a cleared deadline from an absent one.
The other arm's result means nothing without this one failing to survive.`,
			cut, deadlineFixtureTimeout.Milliseconds())
	}

	whole := streamFor(t, true)
	if whole <= cut {
		t.Fatalf(`clearing the write deadline changed nothing: %d lines cleared against %d not cleared.

serve.go sets WriteTimeout on the API server, so without clearWriteDeadline every
SSE connection on this node is cut on that clock - and a cut stream is
indistinguishable from a quiet one, which is the exact failure /api/stream exists
to end.`, whole, cut)
	}
	t.Logf("write deadline cleared: %d lines; not cleared: %d lines (fixture timeout %s)",
		whole, cut, deadlineFixtureTimeout)
}
