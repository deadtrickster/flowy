package flowy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// EVERY CLIENT READ SURVIVES A DEPLOY, and the two here did not.
//
// 2e2e13e taught the verbs that go through peerRequest to wait out a refused
// dial. queue.go and nag.go build their own requests and so never got it -
// which orchestrator found by correcting their own landing claim, and I found
// one door over when `flowy queue wait` exited 2 on a refused dial while the row
// it was watching landed perfectly well.
//
// A deploy is about ten seconds of refused connections. These are the two reads
// most likely to be running during one: the queue is what a drainer polls and
// the nag is what an idle seat blocks on.
//
// THE FIXTURE REFUSES FIRST AND SERVES LATER, and nothing may be listening in
// between - a listener created up front and started late makes the connect
// SUCCEED and the request hang, which is a timeout and not a refusal. That
// version passed with the retry removed, which is how I learned it proved
// nothing.
func TestTheQueueAndNagReadsSurviveARestart(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		read func(context.Context, *http.Client, string) error
	}{
		{
			name: "queue",
			body: `{"target":"master","target_tip":"abc","items":[]}`,
			read: func(ctx context.Context, c *http.Client, base string) error {
				_, _, err := readQueue(ctx, c, base, "t-1", "")
				return err
			},
		},
		{
			name: "nag",
			body: `{"mine":0,"unowned":0,"open":0}`,
			read: func(ctx context.Context, c *http.Client, base string) error {
				_, _, err := readNagAnswer(ctx, c, base, "t-1")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			addr := ln.Addr().String()
			if err := ln.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			// THE PORT HAS TO COME BACK, and until this the test gave up on
			// the first refusal and said nothing.
			//
			// The shape is bind, close, wait, bind the SAME port again - which
			// is the whole point, since what is measured is a client living
			// through a server restart. Between the close and the second bind
			// that port belongs to nobody, and on a busy machine somebody else
			// takes it: every package asking for :0, every browser check, every
			// postgres the suite starts.
			//
			// When that happened the goroutine sent nil and stopped, no server
			// ever appeared, and the client retried into a dead port until its
			// own 20-second timeout. So the report was "Client.Timeout exceeded
			// while awaiting headers" - a sentence about the client, on a run
			// where the client was the only thing working. Measured on the gate
			// 2026-08-21, red on a branch that touches none of this code.
			//
			// So: keep trying for as long as the read is allowed to take, and
			// if the port genuinely cannot be reclaimed, SAY THAT rather than
			// letting the timeout speak for it. A test that cannot set its own
			// fixture up must not report a failure of the thing under test.
			type restarted struct {
				srv *httptest.Server
				err error
			}
			done := make(chan restarted, 1)
			go func() {
				time.Sleep(1500 * time.Millisecond)
				srv := httptest.NewUnstartedServer(http.HandlerFunc(
					func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(tc.body))
					}))
				var listener net.Listener
				var lerr error
				for until := time.Now().Add(10 * time.Second); ; {
					listener, lerr = net.Listen("tcp", addr)
					if lerr == nil || time.Now().After(until) {
						break
					}
					time.Sleep(50 * time.Millisecond)
				}
				if lerr != nil {
					done <- restarted{err: lerr}
					return
				}
				srv.Listener = listener
				srv.Start()
				done <- restarted{srv: srv}
			}()
			defer func() {
				got := <-done
				if got.srv != nil {
					got.srv.Close()
				}
				if got.err != nil {
					t.Errorf("the fixture could not take %s back, so nothing was measured: %v",
						addr, got.err)
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			if err := tc.read(ctx, &http.Client{Timeout: 20 * time.Second},
				"http://"+addr); err != nil {
				t.Fatalf("the %s read did not survive a restart: %v", tc.name, err)
			}
		})
	}
}
