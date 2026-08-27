package flowy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A DEPLOY WINDOW IS NOT AN ERROR, and an answer is never retried.
func TestARefusedDialWaitsAndAnAnswerDoesNot(t *testing.T) {
	// Refused is a connection that was never made, so nothing can have
	// happened twice - that is what makes waiting safe here and nowhere else.
	if !isDialRefused(&net.OpError{Err: syscall.ECONNREFUSED}) {
		t.Fatal("a refused connection is not recognised as a restart")
	}
	// A timeout is NOT: the node may have accepted the connection and taken
	// the write, and asking again would be the double write.
	if isDialRefused(context.DeadlineExceeded) {
		t.Fatal("a timeout is treated as a refusal - a write could land twice")
	}
	if isDialRefused(errors.New("some other failure")) {
		t.Fatal("an unrelated error is treated as a restart")
	}
	if isDialRefused(&net.DNSError{Err: "no such host"}) {
		t.Fatal("a name that does not resolve is treated as a restart")
	}
}

// AND A REFUSAL THAT NEVER CLEARS STILL FAILS, rather than hanging until
// somebody kills the command.
func TestARefusalThatNeverClearsGivesUp(t *testing.T) {
	// A port with nothing on it: the OS refuses immediately, forever.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dead+"/healthz", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	start := time.Now()
	if _, err := doThroughARestart(ctx, &http.Client{Timeout: time.Second}, req, nil); err == nil {
		t.Fatal("a port with nothing on it answered")
	}
	if time.Since(start) > restartWindow {
		t.Fatalf("waited %s, past the restart window", time.Since(start))
	}
}

// The one that matters most: a node that comes back is waited for, and the
// request lands exactly once.
//
// A TRANSPORT RATHER THAN A PORT, and the first version of this check is why. It
// took a real port, pre-bound a listener to hold it, and started the server
// late - so the connection was ACCEPTED and then hung, which is a timeout, not
// a refusal. The fixture produced the one state this function deliberately does
// not retry, and the test failed for being right. A refused dial cannot be
// staged with a listener sitting on the port.
type restartingTransport struct {
	refusals int
	sent     int
}

func (t *restartingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.refusals > 0 {
		t.refusals--
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}
	t.sent++
	// The body must be readable on the attempt that lands, or a retry that
	// reused a consumed request would pass this while sending nothing.
	if r.Body != nil {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, errors.New("the request that landed carried an empty body")
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func TestANodeThatComesBackIsWaitedFor(t *testing.T) {
	tr := &restartingTransport{refusals: 3}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body := []byte(`{"text":"a sentence that must not be lost"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://node.invalid/api/chat/general/say",
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := doThroughARestart(ctx, &http.Client{Transport: tr}, req, body)
	if err != nil {
		t.Fatalf("a node that came back was not waited for: %v", err)
	}
	defer resp.Body.Close()
	if tr.sent != 1 {
		t.Fatalf("the node was written to %d times, want exactly 1", tr.sent)
	}
	if tr.refusals != 0 {
		t.Fatalf("%d refusals left unconsumed - it did not wait them out", tr.refusals)
	}
}
