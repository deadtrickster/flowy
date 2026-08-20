package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// THE WAIT VERB IS THE ONE THAT MEETS A DEPLOY, and it did not survive one.
//
// It is a long blocking call by design - up to a minute per request, repeated
// until the row moves - so a deploy inside its window is expected rather than
// unlucky. I found this by using it for real on the first row after it landed:
// the node deployed, the verb exited 2 on "connection refused", and the row it
// was watching landed perfectly well without it.
//
// 2e2e13e taught six verbs to wait a refused dial out. This one wrote its own
// request and so did not get it. The fix is to go through the same helper; this
// asserts that it does, by serving the request from a listener that is not
// there yet.
func TestTheQueueWaitSurvivesARestart(t *testing.T) {
	// A port nothing is listening on, then a server that appears on it a moment
	// later - which is what a deploy looks like from a client's side.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// NOTHING IS LISTENING YET, and that is the whole fixture. My first version
	// created the listener up front and only called Start() late - so the TCP
	// connect SUCCEEDED and the request merely hung, which is a timeout and not
	// a refusal. It passed with the retry removed, which is how I found out.
	done := make(chan *httptest.Server, 1)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"target":"master","target_tip":"abc","items":[],"cursor":"c1"}`))
		}))
		listener, lerr := net.Listen("tcp", addr)
		if lerr != nil {
			done <- nil
			return
		}
		srv.Listener = listener
		srv.Start()
		done <- srv
	}()
	defer func() {
		if srv := <-done; srv != nil {
			srv.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	answer, err := waitOnQueue(ctx, &http.Client{Timeout: 20 * time.Second},
		"http://"+addr, "t-1", "", "", "", 1)
	if err != nil {
		t.Fatalf("the wait did not survive a restart: %v", err)
	}
	if answer.Cursor != "c1" {
		t.Errorf("it survived and read %q", answer.Cursor)
	}
}

// AND A REFUSAL THAT IS NOT A RESTART STILL FAILS. A helper that waited out
// every refused dial would turn a wrong address into a twenty-second hang and
// then the same error, which is worse than answering at once.
func TestAnAnswerThatIsNotAnErrorIsNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := waitOnQueue(context.Background(), srv.Client(), srv.URL, "t-1", "", "", "", 1)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a 401 should be answered at once, got %v", err)
	}
}
