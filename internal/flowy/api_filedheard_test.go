package flowy

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

// AN ANNOUNCEMENT BELONGS TO THE ROW'S PROJECT, and until this it belonged to
// none.
//
// A nil project is not "unset" at the read: EventFilterSQL treats a projectless
// event as one no project scopes, so it goes to a reader holding no grant on
// the row it is about. The gate found it as six failures with one cause - three
// counting a room ("messages in the room is 3, want 2") and three reading one
// across a project boundary ("pa's messages, read from pc is 1, want 0").
//
// THE EMPTY STRING IS NOT A PROJECT. A row whose project is present but blank
// must leave the event alone rather than scope it to "", which would hide the
// message from everybody instead of from the wrong people - the same
// empty-is-not-missing confusion that broke landing fleet-wide the same night.
func TestAnAnnouncementIsScopedToTheRowsProject(t *testing.T) {
	proj := func(s *string) *store.Artifact { return &store.Artifact{ID: "01ROW", Project: s} }
	str := func(s string) *string { return &s }

	for _, c := range []struct {
		name string
		art  *store.Artifact
		want string // "" means the event must be left unscoped
	}{
		{"a row in a project scopes the message", proj(str("flowy")), "flowy"},
		{"a row naming no project leaves it alone", proj(nil), ""},
		{"an empty project is not a project", proj(str("")), ""},
		{"blank is trimmed, not stamped", proj(str("   ")), ""},
		{"and the value is trimmed when it is real", proj(str(" flowy ")), "flowy"},
		{"no row at all is not a crash", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := heardInProject(&store.Event{Room: "general"}, c.art)
			if c.want == "" {
				if e.Project != nil {
					t.Fatalf("project is %q, want unscoped", *e.Project)
				}
				return
			}
			if e.Project == nil {
				t.Fatalf("project is unscoped, want %q - the message reaches readers with no grant on the row", c.want)
			}
			if *e.Project != c.want {
				t.Fatalf("project is %q, want %q", *e.Project, c.want)
			}
		})
	}

	// A nil event stays nil: three of the four builders return nil when there is
	// no room to say anything in, and this must not turn that into a panic on
	// the write path.
	if heardInProject(nil, proj(str("flowy"))) != nil {
		t.Fatal("a nil announcement came back non-nil")
	}
}
