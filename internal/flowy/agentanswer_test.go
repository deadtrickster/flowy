package flowy

import (
	"bytes"
	"testing"
)

// The shell answers what this relay knows, and refuses what it does not.
//
// 01M1558DPM1HRGZNJGMVW24DHF item 2. Measured on this fleet: one tmux attach
// emits six queries, and byobushells put tmux on the host path - so a session
// whose browser has gone gets six questions and, until now, silence.
//
// THE REFUSALS ARE THE POINT OF THIS TEST. Answering a question you cannot
// answer is worse than silence, because the asker cannot tell a guess from a
// measurement and uses it either way.
func TestTheShellAnswersOnlyWhatItKnows(t *testing.T) {
	answered := map[string]string{
		"the background, because we choose it": "\x1b]11;?\x07",
		"the foreground, likewise":             "\x1b]10;?\x07",
		"primary device attributes":            "\x1b[c",
		"secondary device attributes":          "\x1b[>c",
	}
	for why, q := range answered {
		if got := answerFor([]byte(q)); len(got) == 0 {
			t.Errorf("%s: %q went unanswered, so a shell with no browser waits on its own timeout", why, q)
		}
	}

	// THE CURSOR IS THE ONE THAT MATTERS. This relay forwards bytes and does
	// not model the screen, so it does not know where the cursor is. A program
	// asking uses the answer to place what it draws next: a wrong position
	// corrupts the layout, confidently, and looks like the program's own bug.
	refused := map[string]string{
		"cursor position - not tracked":       "\x1b[6n",
		"device status - nothing to report":   "\x1b[5n",
		"colour scheme status":                "\x1b[?996n",
		"DECRQM mode 2031 - not implemented":  "\x1b[?2031$p",
		"setting mode 2031 is not a question": "\x1b[?2031h",
	}
	for why, q := range refused {
		if got := answerFor([]byte(q)); got != nil {
			t.Errorf("%s: answered %q with %q - a guessed answer is used as though it were measured", why, q, got)
		}
	}

	// A colour SET is telling us something, not asking. Replying to it would
	// put bytes on the shell's stdin that nothing asked for.
	if got := answerFor([]byte("\x1b]11;rgb:1e1e/2e2e/3a3a\x07")); got != nil {
		t.Errorf("answered a colour SET with %q - that sequence is a statement, not a question", got)
	}
}

// The reply reaches the pty exactly once, and only for a query.
func TestTheScrubberCollectsOneAnswerPerQuery(t *testing.T) {
	var s scrubber
	s.filter([]byte("\x1b]11;?\x07some output\x1b[c"))
	owed := s.takeAnswers()
	if !bytes.Contains(owed, []byte("rgb:")) {
		t.Fatalf("the background query was not answered: %q", owed)
	}
	if !bytes.Contains(owed, []byte("\x1b[?62;22c")) {
		t.Fatalf("the device attributes query was not answered: %q", owed)
	}
	if again := s.takeAnswers(); again != nil {
		t.Fatalf("the same answers were owed twice: %q - a shell would read them as input arriving from nowhere", again)
	}

	// Ordinary output owes nothing at all.
	var quiet scrubber
	quiet.filter([]byte("just a prompt $ "))
	if owed := quiet.takeAnswers(); owed != nil {
		t.Fatalf("plain output produced a reply: %q", owed)
	}
}
