package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A REFUSED DIAL AT FIRST CONTACT IS NOT A RESTART.
//
// 01M0EXXNGY. doThroughARestart waited out a connection refusal for the whole
// restart window without asking whether anything had ever answered on that
// address. Measured: `flowy get` with no --url reached 127.0.0.1:8787, where
// nothing lives on this box, and sat through twenty cycles - the exact failure
// the verb was written to remove, reproduced by the verb.
//
// "Restarting" and "there is no node here" are the same errno and different
// facts. Waiting is right when there is a REASON to believe a node is there,
// and there are exactly two: the caller named the address, or something has
// already answered on it in this process.
//
// THE NAMED CASE MUST NOT REGRESS. 2e2e13e exists because a command started
// mid-deploy exited instead of waiting, and the drainer names its address, so
// that path still waits. That is asserted here rather than assumed, because
// this change is one condition away from re-breaking it.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	// Closed immediately: what remains is an address nothing is listening on,
	// which is what a refused dial needs.
	l.Close()
	return addr
}

func refusedRequest(t *testing.T, ctx context.Context, addr string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/node", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return req
}

func TestARefusedDialIsOnlyWaitedOutWhenSomethingMightBeThere(t *testing.T) {
	client := &http.Client{Timeout: time.Second}

	t.Run("unnamed and never answered - refuses at once", func(t *testing.T) {
		addr := freePort(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		started := time.Now()
		_, err := doThroughARestartFrom(ctx, client, refusedRequest(t, ctx, addr), nil, false)
		if err == nil {
			t.Fatal("a refused dial to an address nobody named returned no error")
		}
		if !strings.Contains(err.Error(), "nothing is listening") {
			t.Errorf("the refusal does not say what happened: %v", err)
		}
		if took := time.Since(started); took > 3*time.Second {
			t.Errorf("took %s - it waited, and the whole point is that it must not", took)
		}
	})

	t.Run("named - still waits, which is the deploy window", func(t *testing.T) {
		addr := freePort(t)
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		_, err := doThroughARestartFrom(ctx, client, refusedRequest(t, ctx, addr), nil, true)
		if err == nil {
			t.Fatal("expected the context to end the wait")
		}
		if strings.Contains(err.Error(), "nothing is listening") {
			t.Error("a named address refused instead of waiting - 2e2e13e's case is broken")
		}
	})

	t.Run("unnamed but this host answered earlier - waits", func(t *testing.T) {
		addr := freePort(t)
		hasAnswered.Store(addr, struct{}{})
		defer hasAnswered.Delete(addr)
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		_, err := doThroughARestartFrom(ctx, client, refusedRequest(t, ctx, addr), nil, false)
		if err != nil && strings.Contains(err.Error(), "nothing is listening") {
			t.Error("an address that has answered before was treated as never having answered")
		}
	})
}

func TestAddressWasNamed(t *testing.T) {
	for _, c := range []struct {
		flag, env string
		want      bool
	}{
		{"", "", false},
		{"  ", "  ", false},
		{"http://h", "", true},
		{"", "http://h", true},
	} {
		if got := addressWasNamed(c.flag, c.env); got != c.want {
			t.Errorf("addressWasNamed(%q, %q) = %v, want %v", c.flag, c.env, got, c.want)
		}
	}
}
