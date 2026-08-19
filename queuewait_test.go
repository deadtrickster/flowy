package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// GONE IS NOT LANDED, and the three ways a row can leave the queue print three
// different sentences. This is the half I got wrong by hand twice in one
// session: I read a disappearance as a landing and had to go back for the sha.
//
// The landed arm is here rather than against a live node because a landing
// needs a target tip, which needs a previous landing - a fresh node cannot
// reach the state at all. The other two are reachable and were also measured
// live.
func TestWhatLeavingTheQueueMeans(t *testing.T) {
	answer := func(t *testing.T, body string, status int) string {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer t-1" {
				t.Errorf("the row was read without the caller's token: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
		defer srv.Close()

		out, err := os.CreateTemp(t.TempDir(), "out")
		if err != nil {
			t.Fatalf("temp: %v", err)
		}
		old := os.Stdout
		os.Stdout = out
		gerr := queueWaitGone(srv.Client(), srv.URL, "t-1", "01ROW")
		os.Stdout = old
		if gerr != nil {
			t.Fatalf("queueWaitGone: %v", gerr)
		}
		if _, err := out.Seek(0, 0); err != nil {
			t.Fatalf("seek: %v", err)
		}
		said, err := io.ReadAll(out)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(said)
	}

	// A LANDING NAMES THE SHA, which is the whole reason to read the row rather
	// than report the disappearance.
	landed := answer(t, `{"status":"done","fields":{"branch":"feat/x","landed_tip":"abc1234def567"}}`, 200)
	if !strings.Contains(landed, "landed feat/x as abc1234def56") {
		t.Errorf("a landed row said %q", landed)
	}

	// LEFT WITHOUT LANDING SAYS SO, and says which status it left as. Silence
	// here would read as a landing to anybody skimming.
	closed := answer(t, `{"status":"done","fields":{"branch":"feat/y"}}`, 200)
	// The negative is the LANDING SENTENCE, not the word: "no landed tip on it"
	// contains "landed", and asserting on the word made this fail against correct
	// output. A substring is not a claim.
	if strings.Contains(closed, "landed feat/y as") || !strings.Contains(closed, "no landed tip") {
		t.Errorf("a row that left without landing said %q - it must not read as a landing", closed)
	}

	// AND A ROW THIS SEAT CANNOT READ IS NOT A LANDING EITHER. A 404 here means
	// the scope changed or the id was wrong, and reporting either as a landing
	// is the failure this function exists for.
	hidden := answer(t, `{"error":"no such artifact"}`, 404)
	if strings.Contains(hidden, "landed 01ROW as") || !strings.Contains(hidden, "cannot read it") {
		t.Errorf("an unreadable row said %q", hidden)
	}
}
