package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
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

// waitOutRestart's three answers, which are the whole decision the loops make.
//
// Fast and exact, and it is NOT a substitute for the one below: this proves
// which branch is taken, not that a real restart is survived. Two seats spent
// tonight mistaking the first for the second - a dead port refuses instantly
// and keeps refusing, so twenty seconds of retries fit inside the window and
// the cap never shows.
func TestWaitOutRestartAnswersThreeWays(t *testing.T) {
	refused := &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	future := time.Now().Add(time.Minute)
	past := time.Now().Add(-time.Second)

	if !waitOutRestart(refused, future, "the queue") {
		t.Fatal("a refused dial with time left is a restart to wait out")
	}
	if waitOutRestart(refused, past, "the queue") {
		t.Fatal("a refused dial with the deadline spent is the end of the wait")
	}
	if waitOutRestart(errors.New("the node answered 500"), future, "the queue") {
		t.Fatal("an answer that is not a refused dial is the caller's to report")
	}
}

// AND IT SURVIVES A RESTART LONGER THAN restartWindow, which is the case that
// actually happened.
//
// doThroughARestart rides out a refused dial for restartWindow - twenty
// seconds, sized in 2e2e13e for a ONE-SHOT call that must not hang forever on a
// dead host. A deploy takes longer, so on 2026-08-20 this verb exited 2 mid-
// deploy with fifty-nine minutes left on its deadline, twice, seen by two seats
// independently. Through the same restart, `flowy wait --deploy` rode it out,
// because it polls against its own deadline and never goes near the constant.
//
// THE SERVER COMES BACK AFTER THE WINDOW, ON PURPOSE, and that is why this test
// is slow. A fixture that comes back inside twenty seconds passes with the fix
// removed - which is exactly the instrument that misled me when I first saw
// this and could not reproduce it.
func TestTheQueueWaitOutlivesADeployLongerThanTheRestartWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("this one is slow on purpose: it must outlast restartWindow")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	done := make(chan *httptest.Server, 1)
	go func() {
		time.Sleep(restartWindow + 2*time.Second)
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

	// The caller's own deadline is generous, which is the point: the wait had
	// time and the inner constant ended it anyway.
	give := time.Now().Add(2 * time.Minute)
	client := &http.Client{Timeout: 30 * time.Second}
	var answer mergeQueueAnswer
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		answer, err = waitOnQueue(ctx, client, "http://"+addr, "t-1", "", "", "", 1)
		cancel()
		if err == nil {
			break
		}
		if !waitOutRestart(err, give, "the queue") {
			t.Fatalf("gave up on a restart the caller still had time for: %v", err)
		}
	}
	if answer.Cursor != "c1" {
		t.Fatalf("cursor is %q, want c1", answer.Cursor)
	}
}
