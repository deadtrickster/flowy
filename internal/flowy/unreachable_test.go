package flowy

import (
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
)

// UNREACHABLE IS NOT BROKEN, and a waiter decides what to do next on the
// difference.
//
// 01M1TVXD941TRAM4B07N54JFJB. The node's database crash-looped on a full disk
// twice in one morning. Every door answered 500, every waiter in the fleet
// stood down, and each seat reported LISTENER REFUSED - which reads as the node
// being at fault, over a dependency that came back on its own.
//
// The error the store actually produced, from the node's log:
//
//	store: resolve token: dial tcp 127.0.0.1:5433: connect: connection refused
//
// so the fixture below is that error's real shape - a *net.OpError with
// Op "dial", wrapped twice - rather than a sentinel invented for the test.
func TestAnUnreachableDependencyIsNotAnInternalError(t *testing.T) {
	dialFailed := fmt.Errorf("store: resolve token: %w", &net.OpError{
		Op:   "dial",
		Net:  "tcp",
		Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5433},
		Err:  os.NewSyscallError("connect", syscall.ECONNREFUSED),
	})

	t.Run("a dial failure answers 503 and says retry is worth it", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/inbox/wait", nil)
		w := httptest.NewRecorder()

		serverError(w, r, dialFailed)

		if w.Code != 503 {
			t.Fatalf("a dependency that was not listening answered %d - 500 tells a waiter the node broke, and it stands down over an outage that ends by itself", w.Code)
		}
		if body := w.Body.String(); !strings.Contains(body, "unreachable") {
			t.Fatalf("the 503 body does not say what happened: %q", body)
		}
	})

	// THE ARM THAT KEEPS IT HONEST. If everything became a 503, a genuine bug
	// in this node would be reported as somebody else's outage and retried
	// forever instead of being fixed.
	t.Run("a real fault is still a 500", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/api/inbox/wait", nil)
		w := httptest.NewRecorder()

		serverError(w, r, errors.New("store: read artifact: pq: syntax error at or near \"slect\""))

		if w.Code != 500 {
			t.Fatalf("a genuine fault answered %d - a node that reports its own bugs as a dependency outage never gets them fixed", w.Code)
		}
	})

	// AND THE BODY STILL LEAKS NOTHING. internalError exists because the store
	// wraps schema detail into its errors; the 503 must not become the hole
	// that the 500 was closed to prevent.
	t.Run("neither body carries the error text", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			err  error
		}{
			{"unreachable", dialFailed},
			{"fault", errors.New("store: read artifact: pq: relation \"secrets\" does not exist")},
		} {
			r := httptest.NewRequest("GET", "/api/node", nil)
			w := httptest.NewRecorder()
			serverError(w, r, tc.err)
			for _, leak := range []string{"pq:", "5433", "secrets", "resolve token"} {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("%s body leaked %q: %s", tc.name, leak, w.Body.String())
				}
			}
		}
	})
}
