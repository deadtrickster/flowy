package flowy

// THE SCROLLBACK KEEPS THE OUTPUT AND DROPS THE QUESTIONS.
//
// 01M1557QDD6HQQV91KQK36JH4W. A PTY stream carries two kinds of escape
// sequence, and only one of them is safe to replay. Most are instructions -
// move the cursor, set a colour - and replaying those just redraws. Some are
// QUESTIONS: "what colour is your background" (OSC 10/11/12 with ?), "what are
// you" (CSI c), "where is the cursor" (CSI n). A terminal that receives a
// question ANSWERS it, and a terminal's answer goes where everything it types
// goes - the pty's stdin. The shell reads it as though somebody typed it.
//
// So a backlog replayed verbatim on every reattach makes the browser answer
// every question the session ever asked, and those answers land on the prompt
// as junk. It compounds: each reattach adds another round.
//
// MEASURED 2026-08-29, which is why this exists now rather than as a
// precaution. Nothing in a firecode guest asks - a bash login shell, vim and
// top produce zero queries between them, and the guest has no starship, fish or
// zsh. htop on the host asks nothing either. But ONE tmux attach emits six:
//
//	OSC 10;?    foreground colour
//	OSC 11;?    background colour
//	CSI c       primary device attributes
//	CSI >c      secondary device attributes
//	CSI ?996n   colour scheme status
//	CSI ?2031h  colour scheme change notifications
//
// and byobu is tmux. Once a host shell is a byobu session, every attach puts
// those in the stream this replays.
//
// THE LIVE STREAM IS NOT TOUCHED. A reader attached now must see exactly what
// the shell produced, byte for byte, because it is a terminal talking to a
// terminal and the answers belong to that conversation. Only the stored copy -
// the one handed to somebody who arrives later, when the asker is long gone -
// has the questions taken out.
//
// IT IS A STREAM PARSER BECAUSE IT HAS TO BE. The pty is read in 32KB chunks
// and a sequence can straddle two reads, so an incomplete one is carried over
// rather than passed through. That boundary is the whole difficulty of this
// code and it is what the test drives.

// scrubber removes terminal queries from a byte stream, one chunk at a time.
//
// The zero value is ready. It is NOT safe for concurrent use; agentSession
// holds it under the same mutex as the scrollback it feeds.
type scrubber struct {
	// An escape sequence that has begun and not yet ended. It is withheld
	// rather than emitted, because whether it may be kept is not knowable
	// until its final byte arrives - which may be in the next chunk.
	pend []byte
}

// filter returns the bytes of b that are safe to replay.
//
// Concatenating the results of successive calls gives the same answer as one
// call over the concatenated input, which is the property that makes it safe to
// run over whatever sizes the pty happens to hand us.
func (s *scrubber) filter(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(s.pend))
	for _, c := range b {
		if len(s.pend) == 0 {
			if c == 0x1b {
				s.pend = append(s.pend, c)
				continue
			}
			out = append(out, c)
			continue
		}
		s.pend = append(s.pend, c)
		if !sequenceEnded(s.pend) {
			// STILL OPEN, and there is a bound on how long it may stay that
			// way. A stream that goes wrong - a lone ESC in binary output -
			// would otherwise swallow the rest of the session into pend and
			// the scrollback would go silently empty.
			if len(s.pend) > maxEscape {
				out = append(out, s.pend...)
				s.pend = s.pend[:0]
			}
			continue
		}
		if !isQuery(s.pend) {
			out = append(out, s.pend...)
		}
		s.pend = s.pend[:0]
	}
	return out
}

// maxEscape bounds how much is held while a sequence is open. Real ones are
// tens of bytes; an OSC carrying a title can be longer, so this is generous.
const maxEscape = 1024

// sequenceEnded says whether p is a complete escape sequence.
func sequenceEnded(p []byte) bool {
	if len(p) < 2 {
		return false
	}
	switch p[1] {
	case '[':
		// CSI: parameter and intermediate bytes, then a final in 0x40-0x7E.
		if len(p) < 3 {
			return false
		}
		last := p[len(p)-1]
		return last >= 0x40 && last <= 0x7e
	case ']', 'P', 'X', '^', '_':
		// OSC and friends run until BEL or ST (ESC \).
		last := p[len(p)-1]
		if last == 0x07 {
			return true
		}
		return len(p) >= 2 && last == '\\' && p[len(p)-2] == 0x1b
	default:
		// A two-byte escape. ESC [ and the string openers are handled above,
		// so anything else here is complete as it stands.
		return true
	}
}

// isQuery says whether a complete sequence asks the terminal something, and so
// must not be replayed. Anything not recognised is KEPT: dropping an
// instruction corrupts the redraw, while keeping a question we failed to
// recognise is the defect this fixes - but a wrong guess in the other direction
// is worse, because it is silent and it is on every session.
func isQuery(p []byte) bool {
	if len(p) < 3 {
		return false
	}
	switch p[1] {
	case ']':
		// OSC 10/11/12 with a "?" argument asks for a colour. The same
		// sequence with a value SETS it, and that must be kept.
		body := p[2:]
		for _, pre := range [][]byte{[]byte("10;?"), []byte("11;?"), []byte("12;?")} {
			if hasPrefix(body, pre) {
				return true
			}
		}
		return false
	case '[':
		last := p[len(p)-1]
		body := p[2 : len(p)-1]
		switch last {
		case 'c':
			// Device attributes: CSI c, CSI >c, CSI =c. The parameters are
			// digits and the private markers above, never anything else.
			return onlyIn(body, "0123456789;>=?")
		case 'n':
			// Device status report, cursor position report, and the private
			// forms tmux uses (CSI ?996n).
			return onlyIn(body, "0123456789;?")
		case 'p':
			// DECRQM asks whether a mode is set: CSI ? Ps $ p.
			return len(body) > 0 && body[len(body)-1] == '$' && onlyIn(body[:len(body)-1], "0123456789;?")
		case 'h', 'l':
			// Mode 2031 asks the terminal to REPORT colour scheme changes
			// unprompted. Replaying it re-arms that on a terminal nobody
			// asked, and the reports arrive as input.
			return equal(body, "?2031")
		}
		return false
	}
	return false
}

func hasPrefix(b, pre []byte) bool {
	if len(b) < len(pre) {
		return false
	}
	for i := range pre {
		if b[i] != pre[i] {
			return false
		}
	}
	return true
}

func equal(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range b {
		if b[i] != s[i] {
			return false
		}
	}
	return true
}

func onlyIn(b []byte, set string) bool {
	for _, c := range b {
		found := false
		for i := 0; i < len(set); i++ {
			if set[i] == c {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
