package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A BRANCH FILED FOR THE QUEUE IS HEARD WHERE IT WAS FILED.
//
// Measured by watching what five verbs actually put in a room: `todo file
// --room` says "raised a todo", `todo claim` says "gave X to Y", and `merge
// open --room general` took the room, wrote it onto the row, and said NOTHING.
// A room heard about a new todo and not about a new branch, which is the louder
// of the two - somebody may be about to gate it, land it, or file the same work.
//
// The sentence is checked here rather than only end-to-end because the ID IS
// THE PART THAT MATTERS: two agents once took a thread id out of a raise
// notification, called the row doors with it, and read the 404 as a row that
// had gone away.
func TestAFiledBranchIsHeardInItsRoom(t *testing.T) {

	row := func(fields map[string]string) *store.Artifact {
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("fields: %v", err)
		}
		return &store.Artifact{
			ID: "01ROW", Type: store.MemoryType, Kind: store.MergeKind,
			Title: "land it", Fields: raw,
		}
	}

	room, body, ok := filedSaidFor(row(map[string]string{
		"room": "general", "branch": "feat/x", "target": "master",
	}))
	if !ok {
		t.Fatal("a merge row filed into a room said nothing")
	}
	if room != "general" {
		t.Errorf("it was said in %q", room)
	}
	if !strings.Contains(body, "feat/x") || !strings.Contains(body, "master") {
		t.Errorf("the sentence names neither the branch nor the target: %q", body)
	}
	// THE WHOLE ID, and after a word a reader can copy from. A truncated or
	// absent id sends somebody to the thread id beside it.
	if !strings.Contains(body, "01ROW") {
		t.Errorf("the sentence does not carry the row id: %q", body)
	}

	// A ROW WITH NO ROOM STILL SAYS NOTHING. There is no conversation it belongs
	// to, and inventing one puts a branch in front of people who never saw the
	// work - claimHeardIn's rule.
	if r, b, ok := filedSaidFor(row(map[string]string{"branch": "feat/x"})); ok {
		t.Errorf("a row with no room announced anyway: %q in %q", b, r)
	}

	// AND NEITHER DOES ONE WITH NO BRANCH: there is nothing to say about it yet,
	// and "filed  to land on master" is a sentence with a hole in it.
	if _, b, ok := filedSaidFor(row(map[string]string{"room": "general"})); ok {
		t.Errorf("a row with no branch announced: %q", b)
	}

	// AND NOT FOR A TODO. That door has its own sentence and two would be two
	// announcements of one thing.
	todo := row(map[string]string{"room": "general", "branch": "feat/x"})
	todo.Kind = "todo"
	if _, b, ok := filedSaidFor(todo); ok {
		t.Errorf("a todo was announced by the merge path: %q", b)
	}
}
