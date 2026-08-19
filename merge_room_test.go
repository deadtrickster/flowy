package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A MERGE ROW WITH NO ROOM LANDS SILENTLY, and until 2026-08-19 this verb had no
// --room flag at all - so it was not that nobody typed one, it was that nobody
// could. Four of the six rows that landed that day carry no room, and the
// landing announcement therefore covered two of six.
//
// The check is on the PAYLOAD the CLI posts, because that is where the fact
// either exists or does not. A check that asked the store afterwards would pass
// on a node that defaulted the field for its own reasons.
func TestAMergeRowCarriesTheRoomItsLandingIsAnnouncedIn(t *testing.T) {
	filed := func(t *testing.T, extra ...string) map[string]any {
		t.Helper()
		var got map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Errorf("the CLI posted something that is not json: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"01ROW","visibility":"project-only"}`))
		}))
		defer srv.Close()

		// THE BODY GOES LAST, and this cost an arm to notice: Go's flag package
		// stops parsing at the first non-flag argument, so `--branch B "body"
		// --room handoffs` files a row in general and says nothing. The test
		// caught it in itself, which is the same trap a caller can fall into.
		args := []string{"--branch", "feat/x", "--url", srv.URL, "--token", "t-1"}
		args = append(args, extra...)
		args = append(args, "what changed and what was measured")
		if err := mergeOpen(args); err != nil {
			t.Fatalf("merge open: %v", err)
		}
		return got
	}

	roomOf := func(t *testing.T, payload map[string]any) string {
		t.Helper()
		raw, ok := payload["fields"]
		if !ok {
			t.Fatal("the row was posted with no fields at all")
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("fields: %v", err)
		}
		var fields map[string]string
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatalf("fields are not a string map: %v", err)
		}
		return fields[store.RoomField]
	}

	// SAYING NOTHING PUTS IT IN #general RATHER THAN NOWHERE. This is the arm
	// that was failing in production: the row went out with branch, target and
	// assignee and no room, and nothing downstream could invent one.
	if got := roomOf(t, filed(t)); got != mergeRoomDefault {
		t.Errorf("a row filed with no --room carries room %q, want %q - a row with "+
			"no room lands silently", got, mergeRoomDefault)
	}

	// AND A ROOM THAT WAS TYPED IS THE ROOM. A default that quietly won would be
	// the same defect one turn later.
	if got := roomOf(t, filed(t, "--room", "handoffs")); got != "handoffs" {
		t.Errorf("--room handoffs filed a row in %q", got)
	}
}
