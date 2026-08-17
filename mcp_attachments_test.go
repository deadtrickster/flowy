package main

// The refusals attachment_write makes before it touches a row, which is why
// they can be tested here rather than in the gate: a ceiling, an empty payload
// and two strings a client made up about its own bytes are all decided from the
// arguments alone. The rest of the surface - what a second principal gets, and
// whether the bytes survive the database - is asserted over the wire by
// run-tests.sh, because that is the only place those questions are real.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// writeAttachment runs the tool with no database behind it. Everything asserted
// here answers before the first query, and a nil store is how that is kept
// honest: a check that slipped past these would panic rather than pass.
func writeAttachment(t *testing.T, args map[string]any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	m := &mcpServer{node: "test"}
	p := &store.Principal{UserID: "u_test", Project: "pa"}
	return attachmentWrite(context.Background(), m, p, raw)
}

// The ceiling refusal has to name the ceiling. A refusal that says only "too
// big" leaves the caller to bisect their own payload against a number nobody
// wrote down, and the number is the whole of what they need.
func TestAnAttachmentOverTheCeilingIsRefusedWithTheNumber(t *testing.T) {
	oversize := make([]byte, maxAttachment+1)
	for i := range oversize {
		oversize[i] = byte(i)
	}
	_, err := writeAttachment(t, map[string]any{
		"title":          "one byte too many",
		"content_base64": base64.StdEncoding.EncodeToString(oversize),
	})
	if err == nil {
		t.Fatal("an attachment over the ceiling was accepted")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(maxAttachment)) {
		t.Errorf("the refusal is %q and never names the %d ceiling", err, maxAttachment)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(len(oversize))) {
		t.Errorf("the refusal is %q and never says how big the payload was", err)
	}
	// And it says what it did NOT do. Truncation is the failure this surface
	// exists to avoid, so the refusal states that nothing was kept.
	if !strings.Contains(err.Error(), "truncat") {
		t.Errorf("the refusal is %q and never says the bytes were not truncated", err)
	}
}

// Empty is not legal, and it is refused rather than stored as a zero-length
// attachment. An attachment is bytes; a row that says "here is the log" and
// carries none is the same lie as a truncated one, told at a moment when the
// writer could still have fixed it.
func TestAnEmptyAttachmentIsRefused(t *testing.T) {
	for _, arg := range []string{"", "   ", base64.StdEncoding.EncodeToString(nil)} {
		_, err := writeAttachment(t, map[string]any{
			"title":          "nothing at all",
			"content_base64": arg,
		})
		if err == nil {
			t.Fatalf("an empty attachment was accepted for %q", arg)
		}
		if !strings.Contains(err.Error(), "no bytes") {
			t.Errorf("the refusal for %q is %q and does not say there were no bytes", arg, err)
		}
	}
}

// The bytes go in encoded, and something that is not base64 is refused rather
// than stored as the literal characters somebody typed - which would be a
// silent corruption of exactly the payload this surface promises to keep.
func TestContentThatIsNotBase64IsRefused(t *testing.T) {
	_, err := writeAttachment(t, map[string]any{
		"title":          "raw text, not encoded",
		"content_base64": "panic: nil pointer dereference !!!",
	})
	if err == nil {
		t.Fatal("a payload that is not base64 was accepted")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Errorf("the refusal is %q and does not say the content was not base64", err)
	}
}

// Wrapped and unpadded base64 decode to the same bytes rather than being
// refused: a client that wrapped its lines at 76 columns is not making a
// mistake, and neither habit changes a byte.
func TestWrappedAndUnpaddedBase64DecodeToTheSameBytes(t *testing.T) {
	want := []byte("a log line\n\x00and a NUL\n")
	std := base64.StdEncoding.EncodeToString(want)
	for name, arg := range map[string]string{
		"wrapped":  std[:4] + "\n" + std[4:],
		"unpadded": base64.RawStdEncoding.EncodeToString(want),
		"spaced":   std[:4] + " " + std[4:],
	} {
		got, err := decodeAttachment(arg)
		if err != nil {
			t.Fatalf("%s base64 was refused: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s base64 decoded to %q, want %q", name, got, want)
		}
	}
}

// What the bytes are is decided from the bytes. The claim rides along under a
// name that says it is a claim - see the field names in mcp_attachments.go -
// and this is the half of that rule which can be asserted without a store: a
// payload of markup is text whatever the client called it.
func TestTheTypeIsDecidedFromTheBytesAndNotFromTheClaim(t *testing.T) {
	markup := []byte("<html><body><script>alert(1)</script></body></html>")
	if got := sniffType(markup); !strings.HasPrefix(got, "text/") {
		t.Errorf("markup sniffed as %q, want a text type", got)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if got := sniffType(png); got != "image/png" {
		t.Errorf("a png sniffed as %q", got)
	}
	// The claim is recorded and bounded, and nothing about it reaches the
	// sniffer.
	if _, err := claimedType("image/png"); err != nil {
		t.Errorf("an ordinary media type was refused: %v", err)
	}
	if _, err := claimedType("text/plain\r\nX-Injected: yes"); err == nil {
		t.Error("a media type carrying a newline was recorded verbatim")
	}
	if _, err := claimedType(strings.Repeat("x", maxClaimedType+1)); err == nil {
		t.Error("a media type longer than the ceiling was recorded")
	}
}

// filename is a name, not a path. Nothing joins it onto a directory today, and
// the way that becomes a traversal later is somebody assuming a field called
// filename holds one.
func TestAFilenameThatIsAPathIsRefused(t *testing.T) {
	for _, name := range []string{"../../etc/passwd", "logs/build.log", `C:\build.log`, "a\nb"} {
		if _, err := attachmentFilename(name); err == nil {
			t.Errorf("filename %q was accepted", name)
		}
	}
	got, err := attachmentFilename("  build.log  ")
	if err != nil {
		t.Fatalf("an ordinary filename was refused: %v", err)
	}
	if got != "build.log" {
		t.Errorf("filename came back as %q", got)
	}
}

// The surface is offered to clients under names that match the ones already
// here, and the schema says which argument is not optional.
func TestTheAttachmentToolsAreOffered(t *testing.T) {
	offered := map[string]tool{}
	for _, tl := range allTools() {
		offered[tl.Name] = tl
	}
	for _, name := range []string{"attachment_write", "attachment_read", "attachment_list"} {
		tl, ok := offered[name]
		if !ok {
			t.Fatalf("%s is not in tools/list", name)
		}
		if tl.InputSchema["type"] != "object" {
			t.Errorf("%s has no object input schema", name)
		}
	}
	required, _ := offered["attachment_write"].InputSchema["required"].([]string)
	if len(required) != 1 || required[0] != "content_base64" {
		t.Errorf("attachment_write requires %v, want the content", required)
	}
	// The ceiling is in the description, because the first place an agent
	// looks for the limit is the tool it is about to call.
	if !strings.Contains(offered["attachment_write"].Description, strconv.Itoa(maxAttachment)) {
		t.Error("attachment_write's description never names the ceiling")
	}
}
