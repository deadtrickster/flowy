package flowy

// THE SHELL ANSWERS WHAT IT KNOWS, AND NOTHING ELSE.
//
// 01M1558DPM1HRGZNJGMVW24DHF item 2. A program that wants to know the terminal's
// background colour, or what it is talking to, ASKS - and until now nobody was
// listening. The queries went out of the pty, through the relay, and were
// answered only if a browser happened to be attached at that moment.
//
// MEASURED 2026-08-29 on this fleet: a bash login shell, vim and top emit no
// queries at all, and htop on the host emits none either. ONE tmux attach emits
// six - OSC 10;? and 11;? for the colours, CSI c and CSI >c for device
// attributes, CSI ?996n, and CSI ?2031h. byobushells put tmux on the host path,
// so those six are now the common case rather than a hypothetical.
//
// WHAT IS ANSWERED IS WHAT THIS NODE ACTUALLY KNOWS, and the list is short on
// purpose:
//
//   OSC 10 / OSC 11 - the foreground and background. We know these because WE
//   choose them: the panel draws on black. Answering is telling the truth about
//   our own palette.
//
//   CSI c, CSI >c - what this terminal is. We know that too.
//
// WHAT IS DELIBERATELY NOT ANSWERED, which is the more important half:
//
//   CSI 6n, THE CURSOR POSITION. This relay does not track where the cursor is -
//   it forwards bytes and does not model the screen. An invented position is
//   worse than silence by a long way: a program that asks where the cursor is
//   uses the answer to place the next thing it draws, so a wrong answer
//   corrupts the layout, and it does it CONFIDENTLY. Silence leaves the program
//   to its own timeout and its own fallback, which is the behaviour it has
//   today and which nobody has complained about.
//
//   DECRQM, and mode 2031 in particular. Answering "supported" would promise
//   unsolicited colour-scheme reports that this relay never sends, and
//   answering "not supported" is a claim about a mode nothing here implements.
//   Neither is knowledge, so neither is sent.
//
// The rule is one sentence: a terminal that guesses an answer is worse than one
// that says nothing, because a program cannot tell a guess from a measurement.
// That is the same rule this fleet keeps rediscovering in its own doors.

import "bytes"

// The palette this relay actually draws on. VmShell's screen box is bg-black
// and the frame renderer paints #101318, so black is the honest answer for the
// background and white for the foreground. If the panel ever gets a light
// theme, these move with it - they are the answer to "what do you look like",
// not a preference.
const (
	answerForeground = "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"
	answerBackground = "\x1b]11;rgb:0000/0000/0000\x1b\\"

	// COLORFGBG says the same thing WITHOUT being asked, which is why it is
	// worth setting even though the queries are now answered: a program that
	// reads the environment never has to ask, and one that asks anyway gets the
	// same answer. 15;0 is light-on-dark.
	answerColorFGBG = "15;0"

	// A VT220 with colour, which is what the browser terminal emulates closely
	// enough to claim. The secondary answer names no firmware version worth
	// lying about.
	answerPrimaryDA   = "\x1b[?62;22c"
	answerSecondaryDA = "\x1b[>0;0;0c"
)

// answerFor returns what to write back to the pty for one COMPLETE escape
// sequence, or nil when this relay has nothing truthful to say about it.
//
// It takes a complete sequence rather than a stream because the caller already
// has one: agentscrub's parser assembles them, and asking two parsers to agree
// about where a sequence ends is how they drift apart.
func answerFor(seq []byte) []byte {
	if len(seq) < 3 || seq[0] != 0x1b {
		return nil
	}
	switch seq[1] {
	case ']':
		// A colour QUERY ends in "?" before its terminator; a colour SET
		// carries a value and must not be answered - it is telling us
		// something, not asking.
		body := seq[2:]
		switch {
		case bytes.HasPrefix(body, []byte("10;?")):
			return []byte(answerForeground)
		case bytes.HasPrefix(body, []byte("11;?")):
			return []byte(answerBackground)
		}
		return nil
	case '[':
		last := seq[len(seq)-1]
		body := seq[2 : len(seq)-1]
		if last != 'c' {
			// Everything else that reaches here - CSI n for the cursor, DECRQM
			// for a mode - is a question this relay cannot answer truthfully.
			// See the head of this file.
			return nil
		}
		switch {
		case len(body) == 0, string(body) == "0":
			return []byte(answerPrimaryDA)
		case body[0] == '>':
			return []byte(answerSecondaryDA)
		}
		return nil
	}
	return nil
}
