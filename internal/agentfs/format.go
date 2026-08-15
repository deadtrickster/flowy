package agentfs

// What a memory item looks like when it is a file.
//
// The body of the artifact is the body of the file, with a short front matter
// header above it for the things a body cannot carry: the title, the scope, the
// kind and the tags. It is the header every agent already writes at the top of
// a note, so an agent that knows nothing about this filesystem writes something
// this can read.
//
// Two of the fields are printed and never read back. id is there so a person
// looking at a file knows which row it is, and updated so they know how old it
// is; a write that names a different id in the header does not write to that
// id, because the row a file writes is the row its path names. Anything else
// would make the header a way to reach a row from a directory that has no
// business reaching it, which is exactly the check the path is doing.

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// fence opens and closes the front matter.
const fence = "---"

// maxTitle caps a title derived from a body. A title is one line meant for a
// list, and a file whose first line is a paragraph should not turn into a
// paragraph-long title.
const maxTitle = 200

// doc is a file's content taken apart: the header fields, and the prose.
type doc struct {
	Title string
	// Scope is what the header said, verbatim and unvalidated - "" when the
	// header said nothing, which means "whatever the path decides, and for an
	// edit, whatever the row already has".
	Scope string
	Kind  string
	Tags  []string
	Body  string
}

// render turns an artifact into the bytes a read of its file returns.
func render(a *store.Artifact) []byte {
	var b strings.Builder
	b.WriteString(fence + "\n")
	line(&b, "title", a.Title)
	line(&b, "id", a.ID)
	line(&b, "scope", store.ScopeForVisibility(a.Visibility))
	if a.Kind != "" {
		line(&b, "kind", a.Kind)
	}
	if len(a.Tags) > 0 {
		line(&b, "tags", strings.Join(a.Tags, ", "))
	}
	if !a.Updated.IsZero() {
		line(&b, "updated", a.Updated.UTC().Format(time.RFC3339))
	}
	b.WriteString(fence + "\n\n")
	b.WriteString(a.Body)
	// A text file ends in a newline. Editors add one anyway, and a file that
	// gains a byte the moment anything touches it is a file that is written
	// back on every open.
	if !strings.HasSuffix(a.Body, "\n") {
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// line writes one header field, with newlines flattened: the header is
// line-oriented, and a title holding a newline would otherwise close it early
// and turn the rest of the title into prose.
func line(b *strings.Builder, key, value string) {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	fmt.Fprintf(b, "%s: %s\n", key, strings.TrimSpace(value))
}

// parse reads a file back. A file with no header is not an error - it is a note
// somebody wrote - so the title is taken from its first line and the whole of
// it is the body.
func parse(content []byte) doc {
	rest, header, ok := splitFrontMatter(content)
	if !ok {
		body := string(content)
		return doc{Title: titleFrom(body), Body: body}
	}

	var d doc
	for _, raw := range strings.Split(header, "\n") {
		key, value, found := strings.Cut(raw, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "title":
			d.Title = value
		case "scope":
			d.Scope = value
		case "kind":
			d.Kind = value
		case "tags":
			d.Tags = splitTags(value)
		}
		// id and updated are printed for a person and ignored on the way back
		// in - see the comment at the top of this file. Anything else is
		// somebody's own header field and is left alone rather than refused: a
		// file is not a form.
	}
	d.Body = strings.TrimPrefix(string(rest), "\n")
	if d.Title == "" {
		d.Title = titleFrom(d.Body)
	}
	return d
}

// splitFrontMatter cuts a leading --- ... --- block off the front of content.
// It returns what is left, the header between the fences, and whether there was
// one at all.
func splitFrontMatter(content []byte) (rest []byte, header string, ok bool) {
	if !bytes.HasPrefix(content, []byte(fence+"\n")) &&
		!bytes.HasPrefix(content, []byte(fence+"\r\n")) {
		return content, "", false
	}
	_, after, _ := strings.Cut(string(content), "\n")

	var head strings.Builder
	for {
		one, next, more := strings.Cut(after, "\n")
		if strings.TrimRight(one, "\r") == fence {
			return []byte(next), head.String(), true
		}
		if !more {
			// An opening fence and no closing one is not front matter, it is a
			// file that starts with three dashes.
			return content, "", false
		}
		head.WriteString(one)
		head.WriteString("\n")
		after = next
	}
}

func splitTags(value string) []string {
	out := []string{}
	for _, tag := range strings.Split(value, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// titleFrom takes a one-line title off the top of a body: the first line with
// anything on it, with a markdown heading's hashes removed.
func titleFrom(body string) string {
	for _, raw := range strings.Split(body, "\n") {
		text := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), "#"))
		if text == "" {
			continue
		}
		return truncate(text, maxTitle)
	}
	return ""
}

// truncate cuts s to at most n bytes without splitting a rune in half. The
// content came off a filesystem and is checked for being text elsewhere; this
// one is careful anyway, because a half rune in a title is a title that will
// not go into a text column.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut)
}

// ------------------------------------------------------------------ names

// maxName is the longest filename the mount accepts, which is the longest one
// Linux will hand it.
const maxName = 255

// nameError says why a name is not a name here. It is turned into EINVAL by the
// caller, and its text is what goes in the log.
type nameError struct{ why string }

func (e nameError) Error() string { return e.why }

// checkName decides whether a filename may be used at all.
//
// A name off the kernel is bytes. It is not a Go string with a guarantee
// attached, it is not necessarily UTF-8, and it may hold anything but a slash
// and a NUL. So this validates bytes, and it validates them here rather than
// letting the store find out: the name ends up in a text column, and a text
// column in a UTF-8 database refuses invalid UTF-8 with a driver error halfway
// through a transaction. A refusal at the door is a name the agent can fix; the
// other one is an I/O error on a close.
func checkName(name string) error {
	switch {
	case name == "":
		return nameError{"an empty name"}
	case len(name) > maxName:
		return nameError{fmt.Sprintf("a name of %d bytes, longer than %d", len(name), maxName)}
	case name == "." || name == "..":
		return nameError{"a name that is a directory reference"}
	case strings.HasPrefix(name, "."):
		// Editors and agents drop .swp, .tmp and .# files beside the one they
		// are writing and delete them again. A memory item that exists because
		// vim was open is worse than no file at all.
		return nameError{"a hidden or temporary name; memory items are not dotfiles"}
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '/':
			return nameError{"a name holding a slash"}
		case c == 0:
			return nameError{"a name holding a NUL"}
		case c < 0x20 || c == 0x7f:
			return nameError{fmt.Sprintf("a name holding the control byte 0x%02x", c)}
		}
	}
	if !utf8.ValidString(name) {
		return nameError{"a name that is not valid UTF-8, which the store cannot hold as text"}
	}
	return nil
}

// idFromName reads the artifact id out of a filename, for the names that are
// one: a ULID and the .md suffix, which is what the mount calls a row nobody
// gave a name of their own.
func idFromName(name string) (string, bool) {
	base := strings.TrimSuffix(name, ".md")
	if base == name || len(base) != ulid.EncodedSize {
		return "", false
	}
	if _, err := ulid.Parse(base); err != nil {
		return "", false
	}
	return base, true
}

// mountName is what one artifact is called in the mount: the name it was
// written under when it came in through here, and its id otherwise.
//
// file_path is the column that name lives in, and it holds other things for
// rows that came from elsewhere - the source file a bug is about, for one - so
// only a value that is a plain filename is treated as a name. A path with a
// slash in it is about somewhere else.
func mountName(a *store.Artifact) string {
	if a.FilePath != "" && checkName(a.FilePath) == nil {
		return a.FilePath
	}
	return a.ID + ".md"
}

// mountNames names a whole directory at once, which is the only way to do it:
// two rows can carry the same file_path - one written here, one written by
// something that had never heard of this mount - and a directory cannot hold
// the same name twice. The first row in the listing keeps the name and the
// others fall back to their ids, which never collide.
func mountNames(arts []*store.Artifact) []string {
	taken := make(map[string]bool, len(arts))
	out := make([]string, len(arts))
	for i, a := range arts {
		name := mountName(a)
		if taken[name] {
			name = a.ID + ".md"
		}
		taken[name] = true
		out[i] = name
	}
	return out
}
