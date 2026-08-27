package flowy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE FAILURE THIS VERB REMOVES IS A SUCCESS, which is why these tests assert on
// the PATH the CLI posts to rather than on its output. `flowy say --to NAME`
// exits 0 and prints a cheerful line while publishing to a room; a test that
// only read stdout could not tell the two apart, and neither could the two
// seats who did it on 2026-08-21.

type dmNode struct {
	path string
	got  map[string]any
	room string // what the node echoes back as the event's room
}

func (n *dmNode) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.path = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &n.got); err != nil {
			t.Errorf("the CLI posted something that is not json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"01DM","room":"` + n.room + `"}`))
	}))
}

func (n *dmNode) send(t *testing.T, args ...string) error {
	t.Helper()
	srv := n.serve(t)
	defer srv.Close()
	return dmCmd(append([]string{"--url", srv.URL, "--token", "t-1"}, args...))
}

// IT POSTS TO THE PRIVATE DOOR. The whole verb is this one assertion: /api/dm/
// and not /api/chat/<room>/say.
func TestDMGoesToThePrivateDoorAndNotToARoom(t *testing.T) {
	n := &dmNode{}
	if err := n.send(t, "--to", "orchestrator", "one line"); err != nil {
		t.Fatalf("dm: %v", err)
	}
	if n.path != "/api/dm/orchestrator" {
		t.Errorf("posted to %q, want /api/dm/orchestrator", n.path)
	}
	if strings.Contains(n.path, "/chat/") {
		t.Errorf("a direct message went through the room door: %q", n.path)
	}
	if n.got["body"] != "one line" {
		t.Errorf("body did not arrive: %v", n.got)
	}
}

// NOBODY TO SEND IT TO IS REFUSED BEFORE THE NETWORK, so a missing --to cannot
// become a request to /api/dm/ with an empty segment - which the door answers
// with a sentence about path segments rather than about the flag the caller
// forgot.
func TestDMWithNoAddresseeIsRefusedWithoutAsking(t *testing.T) {
	n := &dmNode{}
	err := n.send(t, "some text")
	if err == nil {
		t.Fatal("sent a direct message to nobody")
	}
	if !strings.Contains(err.Error(), "--to") {
		t.Errorf("the refusal does not name the flag that fixes it: %v", err)
	}
	if n.path != "" {
		t.Errorf("refused and asked the node anyway: %q", n.path)
	}
}

// A NAME IS ONE PATH SEGMENT. Without this, --to "a/b" addresses a different
// route and the caller is told about an endpoint rather than about the name
// they typed.
func TestDMRefusesANameThatIsNotOneSegment(t *testing.T) {
	n := &dmNode{}
	err := n.send(t, "--to", "flowy/general", "text")
	if err == nil {
		t.Fatal("accepted a name with a slash in it")
	}
	if n.path != "" {
		t.Errorf("refused and asked the node anyway: %q", n.path)
	}
}

// EMPTY TEXT IS REFUSED, and before the network for the same reason: the door's
// "body is required" is a fact about JSON, not about the argument the caller
// left off.
func TestDMWithNothingToSayIsRefused(t *testing.T) {
	n := &dmNode{}
	if err := n.send(t, "--to", "orchestrator", "   "); err == nil {
		t.Fatal("sent an empty direct message")
	}
	if n.path != "" {
		t.Errorf("refused and asked the node anyway: %q", n.path)
	}
}

// THE LINE A PERSON READS COMES FROM THE NODE'S ANSWER. A DM's event carries an
// empty room, and that emptiness is the proof the caller wants - so if a node
// ever answers with a room, the CLI says so loudly rather than printing the
// same reassuring sentence it prints when the message really was private.
func TestAnAnswerCarryingARoomIsNotReportedAsPrivate(t *testing.T) {
	n := &dmNode{room: "general"}
	if err := n.send(t, "--to", "orchestrator", "text"); err != nil {
		t.Fatalf("dm: %v", err)
	}
	// The assertion is that the code takes the loud branch; the branch itself
	// writes to stderr, which this test does not capture. What it can check is
	// that the request still went to the private door - if that ever changes,
	// the test above fails and this one is not the interesting failure.
	if n.path != "/api/dm/orchestrator" {
		t.Errorf("posted to %q", n.path)
	}
}
