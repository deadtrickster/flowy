package flowy

import (
	"bytes"
	"testing"
)

// The six sequences ONE tmux attach emits, measured on 2026-08-29 with
// script(1) and TERM=xterm-256color. They are the reason this code exists, so
// they are the fixture rather than a set invented to match the parser.
var tmuxAttachQueries = [][]byte{
	[]byte("\x1b]10;?\x07"),
	[]byte("\x1b]11;?\x07"),
	[]byte("\x1b[c"),
	[]byte("\x1b[>c"),
	[]byte("\x1b[?996n"),
	[]byte("\x1b[?2031h"),
}

// Sequences that must SURVIVE. Half of these differ from a query by one byte,
// which is the whole risk in this parser: dropping an instruction corrupts
// every redraw that follows it, silently.
var mustSurvive = [][]byte{
	[]byte("\x1b[0m"),             // reset colour
	[]byte("\x1b[1;31m"),          // bold red
	[]byte("\x1b[2J"),             // clear screen
	[]byte("\x1b[10;20H"),         // move the cursor
	[]byte("\x1b[?1049h"),         // alternate screen - a mode SET, not 2031
	[]byte("\x1b[?25l"),           // hide the cursor
	[]byte("\x1b]0;a title\x07"),  // OSC 0 sets a title
	[]byte("\x1b]11;#1e1e2e\x07"), // OSC 11 with a VALUE sets the background
	[]byte("\x1bM"),               // reverse index, a two-byte escape
	[]byte("plain text\r\n"),
}

func TestTheScrollbackDropsTheQuestionsAndKeepsTheRest(t *testing.T) {
	for _, q := range tmuxAttachQueries {
		var s scrubber
		if got := s.filter(q); len(got) != 0 {
			t.Errorf("query %q survived as %q - a reader replaying it makes its terminal answer into the shell's stdin", q, got)
		}
	}
	for _, keep := range mustSurvive {
		var s scrubber
		if got := s.filter(keep); !bytes.Equal(got, keep) {
			t.Errorf("instruction %q came back as %q - dropping one corrupts every redraw after it", keep, got)
		}
	}
}

// THE BOUNDARY IS THE POINT OF THE PARSER. The pty is read in chunks and a
// query can straddle two of them, so a filter that only works on whole
// sequences works right up until the read lands in the wrong place - which is
// exactly the kind of bug that survives casual use and appears under load.
//
// Every split of every query is driven, not a chosen one.
func TestAQuerySplitAcrossChunksIsStillDropped(t *testing.T) {
	for _, q := range tmuxAttachQueries {
		for cut := 1; cut < len(q); cut++ {
			var s scrubber
			got := append([]byte(nil), s.filter(q[:cut])...)
			got = append(got, s.filter(q[cut:])...)
			if len(got) != 0 {
				t.Errorf("query %q split at %d survived as %q", q, cut, got)
			}
		}
	}
}

// THE PROPERTY THE ROW ASKED FOR, and it is a difference rather than an
// absolute: one replay and two replays of the same session must put the same
// thing on the wire. The old code fails this by construction - it stores the
// queries, so the second replay carries a second copy of every question and the
// answers pile up on the prompt.
func TestReplayingTwiceSaysTheSameAsReplayingOnce(t *testing.T) {
	// A session that looks like a real one: an attach, some output, another
	// attach, more output.
	var session []byte
	for _, q := range tmuxAttachQueries {
		session = append(session, q...)
	}
	session = append(session, []byte("$ ls\r\nfile-a  file-b\r\n")...)
	for _, q := range tmuxAttachQueries {
		session = append(session, q...)
	}
	session = append(session, []byte("$ ")...)

	// Stored once, then handed out twice, which is what attach() does.
	var s scrubber
	stored := s.filter(session)

	once := append([]byte(nil), stored...)
	twice := append(append([]byte(nil), stored...), stored...)

	if bytes.Contains(once, []byte("\x1b[c")) || bytes.Contains(once, []byte("]11;?")) {
		t.Fatalf("the stored scrollback still carries questions: %q", once)
	}
	// Doubling it must not introduce one either - the point being that what is
	// replayed contains nothing that can be answered, however many times it is.
	for _, q := range tmuxAttachQueries {
		if bytes.Contains(twice, q) {
			t.Errorf("replaying twice put %q back on the wire", q)
		}
	}
	if !bytes.Contains(once, []byte("file-a  file-b")) {
		t.Errorf("the output was lost along with the questions: %q", once)
	}
}

// A LONE ESC MUST NOT SWALLOW THE SESSION. Binary output through a pty can
// carry an ESC that begins nothing, and a parser that waits forever for a final
// byte would hold the rest of the scrollback in its pending buffer - which
// reads as a shell that went silent.
func TestAnUnterminatedEscapeDoesNotEatTheScrollback(t *testing.T) {
	var s scrubber
	junk := append([]byte{0x1b, '['}, bytes.Repeat([]byte("0"), maxEscape+64)...)
	got := s.filter(junk)
	if len(got) == 0 {
		t.Fatalf("an unterminated sequence of %d bytes produced nothing - the scrollback would go silently empty", len(junk))
	}
	tail := s.filter([]byte("visible\r\n"))
	if !bytes.Contains(tail, []byte("visible")) {
		t.Errorf("output after an unterminated sequence was swallowed: %q", tail)
	}
}
