package agentfs

import (
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

func TestRenderThenParseIsTheSameItem(t *testing.T) {
	project := "pa"
	art := &store.Artifact{
		ID:         "01H0000000000000000000000A",
		Type:       "memory",
		Kind:       "handoff",
		Project:    &project,
		Title:      "the parser chokes on a zorblatt",
		Body:       "Two paragraphs.\n\nThe second one.\n",
		Tags:       []string{"parser", "phase7"},
		Visibility: store.VisibilityShared,
		Updated:    time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC),
	}

	d := parse(render(art))
	if d.Title != art.Title {
		t.Errorf("title round-tripped as %q, want %q", d.Title, art.Title)
	}
	if d.Body != art.Body {
		t.Errorf("body round-tripped as %q, want %q", d.Body, art.Body)
	}
	if d.Kind != "handoff" {
		t.Errorf("kind round-tripped as %q, want handoff", d.Kind)
	}
	if strings.Join(d.Tags, ",") != "parser,phase7" {
		t.Errorf("tags round-tripped as %v", d.Tags)
	}
	// shared is the scope 'shared' is stored as, and it has to come back as the
	// word an agent wrote rather than as the column.
	if d.Scope != "shared" {
		t.Errorf("scope round-tripped as %q, want shared", d.Scope)
	}
}

// A project-only row is the mem_write scope `project`, and the file has to say
// so: the header is what an agent reads to find out what it is looking at, and
// the column name is not one of the three words it was offered.
func TestTheHeaderNamesTheScopeAndNotTheColumn(t *testing.T) {
	out := string(render(&store.Artifact{Visibility: store.VisibilityProjectOnly, Title: "t"}))
	if !strings.Contains(out, "scope: project\n") {
		t.Fatalf("a project-only item renders as:\n%s", out)
	}
}

func TestAFileWithNoHeaderIsANote(t *testing.T) {
	d := parse([]byte("# What we decided\n\nWe went with the queue.\n"))
	if d.Title != "What we decided" {
		t.Errorf("title is %q, want the first line without its hashes", d.Title)
	}
	if !strings.Contains(d.Body, "We went with the queue.") {
		t.Errorf("body is %q, want the whole file", d.Body)
	}
	if d.Scope != "" || d.Kind != "" {
		t.Errorf("a file with no header claimed scope %q kind %q", d.Scope, d.Kind)
	}
}

// The id in the header is printed for a person to read. Writing a different one
// must not make the write go somewhere else - the row a file writes is the row
// its path names, and a header that could redirect it would be a way into a
// directory the writer was refused.
func TestTheIdInTheHeaderIsNotRead(t *testing.T) {
	d := parse([]byte("---\ntitle: t\nid: 01H0000000000000000000000B\n---\nbody\n"))
	if d.Title != "t" || d.Body != "body\n" {
		t.Fatalf("parsed %+v", d)
	}
	// There is nowhere for it to go: doc has no id field at all, which is the
	// point of this test - it is here so that adding one fails it.
	if got := parse([]byte("---\nid: x\n---\n")).Title; got != "" {
		t.Fatalf("a header with only an id produced the title %q", got)
	}
}

func TestAnOpeningFenceWithNoClosingOneIsNotAHeader(t *testing.T) {
	content := "---\ntitle: never closed\nand then prose\n"
	d := parse([]byte(content))
	if d.Body != content {
		t.Errorf("body is %q, want the whole file", d.Body)
	}
	if d.Title != "---" {
		t.Errorf("title is %q, want the first line as written", d.Title)
	}
}

func TestNamesAreCheckedAsBytes(t *testing.T) {
	ok := []string{"decisions.md", "01H0000000000000000000000A.md", "a-b_c.txt", "ünïcode.md"}
	for _, name := range ok {
		if err := checkName(name); err != nil {
			t.Errorf("checkName(%q) = %v, want it accepted", name, err)
		}
	}

	bad := map[string]string{
		"":                       "empty",
		".":                      "dot",
		"..":                     "dotdot",
		".hidden.md":             "a dotfile - editors drop these beside the real file",
		"a/b.md":                 "a slash",
		"tab\there.md":           "a control byte",
		"\xff\xfe.md":            "not valid UTF-8, which a text column cannot hold",
		strings.Repeat("x", 256): "longer than the kernel's own limit",
		"with\x00nul":            "a NUL",
	}
	for name, why := range bad {
		if err := checkName(name); err == nil {
			t.Errorf("checkName(%q) was accepted; it is %s", name, why)
		}
	}
}

func TestOnlyAULIDNamesARowById(t *testing.T) {
	id, ok := idFromName("01H0000000000000000000000A.md")
	if !ok || id != "01H0000000000000000000000A" {
		t.Errorf("idFromName on a ULID gave %q %v", id, ok)
	}
	for _, name := range []string{"decisions.md", "01H0000000000000000000000A", "notaulid.md", "!!!!!!!!!!!!!!!!!!!!!!!!!!.md"} {
		if _, ok := idFromName(name); ok {
			t.Errorf("idFromName(%q) claimed to be an id", name)
		}
	}
}

// Two rows can carry the same file_path - one written through the mount, one
// written by something that never heard of it - and a directory cannot hold one
// name twice.
func TestADirectoryNeverHoldsTheSameNameTwice(t *testing.T) {
	arts := []*store.Artifact{
		{ID: "01H0000000000000000000000A", FilePath: "notes.md"},
		{ID: "01H0000000000000000000000B", FilePath: "notes.md"},
		{ID: "01H0000000000000000000000C"},
		{ID: "01H0000000000000000000000D", FilePath: "src/main.go"},
	}
	names := mountNames(arts)
	want := []string{
		"notes.md",
		"01H0000000000000000000000B.md",
		"01H0000000000000000000000C.md",
		// A file_path with a slash in it is about a source file somewhere else,
		// not a name in this directory.
		"01H0000000000000000000000D.md",
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("name %d is %q, want %q", i, names[i], want[i])
		}
	}
}
