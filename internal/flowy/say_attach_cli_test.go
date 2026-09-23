package flowy

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A MESSAGE CARRIES ITS ATTACHMENT. The node has taken `attachments` on a say
// since the file-token shape landed, and the console renders a card for each
// id; the CLI never filled the field, so a seat asked to "send the jpeg here"
// could only write an attachment row and then say its id in prose. Measured
// 2026-09-23: a snake for Nikita landed as a bare row plus a text pointer, and
// the operator answered "guys, you can send messages with attachments".
//
// These tests assert on WHAT THE CLI POSTS, for dm_cli_test's reason: the verb
// exits 0 and prints the same cheerful line whether or not the field went out.

type sayNode struct {
	says        []map[string]any // every POST to a say door, in order
	attachments []map[string]any // every POST to /api/attachment, in order
	paths       []string
	nextAttach  int
}

func (n *sayNode) serve(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.paths = append(n.paths, r.URL.Path)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("the CLI posted something that is not json: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/attachment":
			n.attachments = append(n.attachments, got)
			n.nextAttach++
			id := "01ATTACH" + strings.Repeat("0", 17) + string(rune('0'+n.nextAttach))
			_, _ = w.Write([]byte(`{"item":{"id":"` + id + `"},"size_bytes":3,"digest_sha256":"d"}`))
		case strings.HasSuffix(r.URL.Path, "/say"):
			n.says = append(n.says, got)
			_, _ = w.Write([]byte(`{"id":"01SAID","room":"general"}`))
		default:
			t.Errorf("the CLI posted to %q, a door this test does not know", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func (n *sayNode) say(t *testing.T, args ...string) error {
	t.Helper()
	srv := n.serve(t)
	defer srv.Close()
	return sayCmd(append([]string{"--url", srv.URL, "--token", "t-1"}, args...))
}

func attachmentsOf(t *testing.T, said map[string]any) []string {
	t.Helper()
	raw, ok := said["attachments"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("attachments is not a list: %#v", raw)
	}
	var ids []string
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("an attachment id is not a string: %#v", v)
		}
		ids = append(ids, s)
	}
	return ids
}

// --attach ID names an attachment already on the node, and the message goes
// out carrying it - every id, in the order given, in the field the node reads.
func TestSayAttachCarriesTheIdsOnTheMessage(t *testing.T) {
	n := &sayNode{}
	if err := n.say(t, "--attach", "01AAA", "--attach", "01BBB", "two files, one line"); err != nil {
		t.Fatalf("say: %v", err)
	}
	if len(n.says) != 1 {
		t.Fatalf("posted %d messages, want 1 (paths %v)", len(n.says), n.paths)
	}
	if got := attachmentsOf(t, n.says[0]); strings.Join(got, " ") != "01AAA 01BBB" {
		t.Errorf("the message carried %v, want [01AAA 01BBB]", got)
	}
	if n.says[0]["body"] != "two files, one line" {
		t.Errorf("body did not arrive: %v", n.says[0])
	}
	if len(n.attachments) != 0 {
		t.Errorf("--attach uploaded %d file(s); it names rows, it does not write them", len(n.attachments))
	}
}

// --file PATH is the one-line shape: the bytes land through the attachment
// door first, and the message that follows carries the id the node answered
// with. Two doors, in that order, and nothing said until the bytes are there.
func TestSayFileUploadsThenCarriesTheNewId(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snake.jpg")
	if err := os.WriteFile(path, []byte("jpg"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &sayNode{}
	if err := n.say(t, "--file", path, "--to", "deadtrickster", "a snake for Nikita"); err != nil {
		t.Fatalf("say: %v", err)
	}
	if len(n.paths) != 2 || n.paths[0] != "/api/attachment" || !strings.HasSuffix(n.paths[1], "/say") {
		t.Fatalf("doors in order %v, want [/api/attachment .../say]", n.paths)
	}
	up := n.attachments[0]
	if up["filename"] != "snake.jpg" {
		t.Errorf("filename %v, want snake.jpg", up["filename"])
	}
	if up["content_type"] != "image/jpeg" {
		t.Errorf("content_type %v, want image/jpeg from the extension", up["content_type"])
	}
	if up["content_base64"] != base64.StdEncoding.EncodeToString([]byte("jpg")) {
		t.Errorf("the bytes did not go out as base64: %v", up["content_base64"])
	}
	got := attachmentsOf(t, n.says[0])
	if len(got) != 1 || !strings.HasPrefix(got[0], "01ATTACH") {
		t.Errorf("the message carried %v, want the id the attachment door answered", got)
	}
	if n.says[0]["to"] != "deadtrickster" {
		t.Errorf("--to did not survive the upload: %v", n.says[0])
	}
}

// A file that is not there is refused BEFORE anything is posted: no half a
// message, no orphan upload, and the error names the path.
func TestSayFileMissingIsRefusedBeforeTheNetwork(t *testing.T) {
	n := &sayNode{}
	err := n.say(t, "--file", filepath.Join(t.TempDir(), "absent.png"), "text")
	if err == nil {
		t.Fatal("a missing file was accepted")
	}
	if !strings.Contains(err.Error(), "absent.png") {
		t.Errorf("the refusal does not name the file: %v", err)
	}
	if len(n.paths) != 0 {
		t.Errorf("posted to %v before refusing", n.paths)
	}
}

// WITHOUT EITHER FLAG NOTHING CHANGES: a plain say carries no attachments
// field at all, so a node that predates the field sees the request it always
// saw.
func TestSayWithoutAttachmentsPostsNoneOfThem(t *testing.T) {
	n := &sayNode{}
	if err := n.say(t, "plain"); err != nil {
		t.Fatalf("say: %v", err)
	}
	if _, present := n.says[0]["attachments"]; present {
		t.Errorf("a plain say posted an attachments field: %v", n.says[0])
	}
}

// `flowy attach --message M` hands M to the node as the id of the message the
// row hangs off, and the node refuses an id it cannot read. Its help said M
// was "the text" a room message would be said with, and no message is ever
// said by that door. The usage has to say what the flag does.
func TestAttachUsageSaysMessageIsAnIdAndPromisesNoSpeech(t *testing.T) {
	if strings.Contains(attachUsage, "also say it") || strings.Contains(attachUsage, "with this text") {
		t.Errorf("attach's usage promises to speak in a room, which the door does not do:\n%s", attachUsage)
	}
	if !strings.Contains(attachUsage, "flowy say --attach") && !strings.Contains(attachUsage, "flowy say --file") {
		t.Errorf("attach's usage does not point at the verb that does carry a file into a room:\n%s", attachUsage)
	}
}

// THE FILE MAY COME FIRST, as the usage line has always shown it. The flag
// package stops parsing at the first word that is not a flag, so
// `flowy attach snake.jpg --title T` used to be refused with "which file" -
// the usage promising one order and the parser accepting the other. Measured
// 2026-09-23, two tries before the flags-first order was guessed.
func TestAttachTakesTheFileBeforeTheFlagsAsItsUsageSays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snake.jpg")
	if err := os.WriteFile(path, []byte("jpg"), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &sayNode{}
	srv := n.serve(t)
	defer srv.Close()
	if err := attachCmd([]string{path, "--url", srv.URL, "--token", "t-1", "--title", "a snake"}); err != nil {
		t.Fatalf("attach with the file first: %v", err)
	}
	if len(n.attachments) != 1 {
		t.Fatalf("posted %d attachments, want 1 (paths %v)", len(n.attachments), n.paths)
	}
	if n.attachments[0]["title"] != "a snake" {
		t.Errorf("the flags after the file were dropped: %v", n.attachments[0])
	}
	if err := attachCmd([]string{"--url", srv.URL, "--token", "t-1", path}); err != nil {
		t.Fatalf("attach with the file last: %v", err)
	}
	if len(n.attachments) != 2 {
		t.Errorf("the flags-first order stopped working: %d uploads", len(n.attachments))
	}
}

// THE REFUSAL NAMES WHAT THE CALLER WAS DOING. One error type answers "message
// X is not one you can read" for a todo raised out of a message and for an
// attachment hung off one, and its second half used to be the todo's sentence
// in both cases - which told an attachment writer who had passed text where an
// id goes that they had a permission problem with todos. The attachment door
// now says it wanted an id; the todo door still says what a todo does.
func TestAnUnreadableMessageRefusalSaysWhatWasHangingOffIt(t *testing.T) {
	todo := unreadableMessage{id: "01X"}.Error()
	if !strings.Contains(todo, "a todo is raised") {
		t.Errorf("the todo refusal lost its sentence: %q", todo)
	}
	att := unreadableMessage{id: "01X", hangs: attachmentHangsOff}.Error()
	if !strings.HasPrefix(att, "message 01X is not one you can read; ") {
		t.Errorf("the refusal changed its first half: %q", att)
	}
	if strings.Contains(att, "todo") {
		t.Errorf("the attachment refusal still talks about todos: %q", att)
	}
	if !strings.Contains(att, "ID") || !strings.Contains(att, "not text") {
		t.Errorf("the attachment refusal does not say the field is an id: %q", att)
	}
}
