package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// TestPeerAnswerRefusesAnOversizedPage is the difference between a peer that
// sent too much and a peer that sent nonsense.
//
// The read used to be a LimitReader on its own, so an answer over the limit was
// cut mid-JSON and came back from the decoder as a syntax error with no cause
// in it. The cursor only moves on a page that decoded, so the next run asked
// for the same page and was cut in the same place - a sync that never advances
// and never says why.
func TestPeerAnswerRefusesAnOversizedPage(t *testing.T) {
	const limit = 1 << 20

	over := strings.Repeat("a", limit+1)
	_, err := peerAnswer(strings.NewReader(over), limit)
	if err == nil {
		t.Fatal("an answer over the limit was read as if it fitted")
	}
	if !strings.Contains(err.Error(), "exceeds 1 MB") {
		t.Fatalf("the refusal does not name the limit: %v", err)
	}

	// One byte past is over; the limit itself is not, and is read whole.
	at := strings.Repeat("b", limit)
	got, err := peerAnswer(strings.NewReader(at), limit)
	if err != nil {
		t.Fatalf("an answer at the limit was refused: %v", err)
	}
	if string(got) != at {
		t.Fatalf("an answer at the limit came back %d bytes long, want %d", len(got), limit)
	}
}

// TestPullFromAPeerThatAnswersTooMuchSaysSo drives the same thing through the
// driver's own request, against a peer answering with more than maxSyncBody:
// the error names the limit and the peer, and is not a parse error.
func TestPullFromAPeerThatAnswersTooMuchSaysSo(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), 1<<20)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A well-formed page it never finishes writing: what the decoder makes
		// of it is the whole question, so the prefix has to be real JSON.
		if _, err := w.Write([]byte(`{"artifacts": [`)); err != nil {
			return
		}
		for written := 0; written <= maxSyncBody; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer peer.Close()

	var into store.SyncSet
	err := peerRequest(context.Background(), peer.Client(), http.MethodGet,
		peer.URL+"/api/sync/pull", "token", nil, &into)
	if err == nil {
		t.Fatal("a peer answering with more than the limit was accepted")
	}
	if !strings.Contains(err.Error(), "exceeds 64 MB") {
		t.Fatalf("the refusal does not say the answer was too large: %v", err)
	}
	if !strings.Contains(err.Error(), peer.URL) {
		t.Fatalf("the refusal does not name the peer: %v", err)
	}
	if strings.Contains(err.Error(), "which is not the expected JSON") {
		t.Fatalf("an oversized answer was reported as a parse error: %v", err)
	}
}
