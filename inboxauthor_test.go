package main

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A DELIVERY SAYS WHO SAID IT, at the path every listener already reads.
//
// The room read has always carried the speaker's name at meta.actor_name, and
// every listener in this fleet was written against that shape. The delivery
// dropped meta entirely and carried only `actor`, a ULID - so three agents'
// monitors printed "?" for the author of every message they ever received:
//
//	? [general 01M0HQ0NMS...]: @claude-host queue is 17 and ...
//
// This asserts the PATH and not merely the presence of a name, because a name
// under a different key fixes nobody: the listeners are inline shell in other
// sessions and cannot be edited from here. meta.actor_name or it did not land.
func TestADeliveryCarriesTheSpeakersName(t *testing.T) {
	meta, err := json.Marshal(map[string]any{
		"actor_name": "deadtrickster",
		"actor_kind": "person",
	})
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	page := inboxWaitResponse{
		Reader: "claude-host",
		Events: []*store.Event{{
			ID:     "01M0HQ0NMS16X5SZ87AGYAB5ZT",
			Room:   "general",
			Actor:  "01M05TQ76D8Q4Q6NGBJ0SKT0TB",
			Thread: "01M0HPQTFJ417T7G0WFK4GEQJD",
			Body:   "so in a way it is prefix branching",
			Meta:   meta,
		}},
	}

	line := captureDelivery(t, page)

	back, ok := line["meta"].(map[string]any)
	if !ok {
		t.Fatalf("the delivery carries no meta at all, so the author is unreachable: keys %v", keysOf(line))
	}
	if back["actor_name"] != "deadtrickster" {
		t.Errorf("meta.actor_name is %v, want %q - a listener reading this path prints \"?\"", back["actor_name"], "deadtrickster")
	}

	// THE THREAD IS THE OTHER HALF OF THE SAME ROW (01M0HH6ANG). A reply
	// needs the prefix it branches from; without this the answer lands in the
	// room and reads as an agent ignoring the thread.
	if line["thread"] != "01M0HPQTFJ417T7G0WFK4GEQJD" {
		t.Errorf("the delivery carries thread %v, want the event's own", line["thread"])
	}
}

// AND AN EVENT WITH NO META DOES NOT GROW AN EMPTY ONE. A key that is there is
// a key that answers; `"meta": {}` makes a reader test two things instead of
// one, and every consumer that checks presence would read it as a name.
func TestADeliveryWithNoMetaHasNoMetaKey(t *testing.T) {
	page := inboxWaitResponse{
		Reader: "claude-host",
		Events: []*store.Event{{ID: "01M0HQ0NMS16X5SZ87AGYAB5ZT", Room: "general", Body: "hi"}},
	}
	line := captureDelivery(t, page)
	if _, present := line["meta"]; present {
		t.Errorf("an event with no meta was delivered with meta=%v", line["meta"])
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// captureDelivery runs writeInbox and reads back the one line it wrote. It
// goes through os.Stdout because that is where writeInbox writes, and a test
// that asserted on some other writer would not be asserting on the bytes an
// agent's monitor actually parses.
func captureDelivery(t *testing.T, page inboxWaitResponse) map[string]any {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	writeErr := writeInbox(page)
	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if writeErr != nil {
		t.Fatalf("writeInbox: %v", writeErr)
	}
	var line map[string]any
	if err := json.Unmarshal(out, &line); err != nil {
		t.Fatalf("the delivery is not one JSON object: %q (%v)", out, err)
	}
	return line
}
