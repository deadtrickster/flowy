package flowy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAGuestShellIsItsOwnSession runs the START PATH against a stand-in binary
// that prints the argument vector it was given, and reads that back off the pty.
// It is the argv that actually reached exec, not a copy of it assembled beside
// the code under test.
//
// The property is --no-tmux, and it is load-bearing rather than tidy.
// `firecode shell` otherwise wraps itself in `tmux new-session -A -s
// firecode/<project>`, where -A makes the session a singleton per project. A
// relayed shell that joins that session is not a microVM: it is a second client
// on the operator's own terminal, and when the session's VM has already gone,
// attaching returns immediately - which is what "the shell exits immediately"
// was.
func TestAGuestShellIsItsOwnSession(t *testing.T) {
	dir := t.TempDir()
	stand := filepath.Join(dir, "firecode")
	script := "#!/bin/sh\nprintf 'ARGV:%s\\n' \"$*\"\nsleep 1\n"
	if err := os.WriteFile(stand, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stand-in: %v", err)
	}

	shells := newAgentShells()
	sess, err := shells.start("01STANDIN", "flowy", "/home/dead/Projects/flowy", stand,
		shellInGuest, agentSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting a guest shell: %v", err)
	}
	defer sess.finish("test done")

	said := readUntil(t, sess, "ARGV:")
	if !strings.Contains(said, "--no-tmux") {
		t.Fatalf(`the guest shell was started without --no-tmux, so firecode wraps it in the
project's shared tmux session rather than giving it a VM of its own. argv was: %q`, said)
	}
	if !strings.Contains(said, "--project /home/dead/Projects/flowy") {
		t.Fatalf("the workdir did not reach firecode as a directory: %q", said)
	}

	// THE OTHER HALF, so this cannot pass by the flag being unconditional. A
	// HOST shell runs the person's login shell and must not carry firecode's
	// arguments at all.
	host, err := shells.start("01STANDINHOST", "flowy", dir, "", shellOnHost,
		agentSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting a host shell: %v", err)
	}
	defer host.finish("test done")
	if got := host.cmd.Path; strings.Contains(got, "firecode") {
		t.Fatalf("a host shell ran %q, which is firecode - the selector was not honoured", got)
	}
}

// readUntil waits for a marker in what the session has said, so the assertion is
// on output that arrived rather than on a sleep that expired.
func readUntil(t *testing.T, sess *agentSession, marker string) string {
	t.Helper()
	ch, backlog, _ := sess.attach()
	defer sess.detach(ch)
	deadline := time.After(10 * time.Second)
	var seen strings.Builder
	seen.Write(backlog)
	if strings.Contains(seen.String(), marker) {
		return seen.String()
	}
	for {
		select {
		case chunk := <-ch:
			seen.Write(chunk)
			if strings.Contains(seen.String(), marker) {
				return seen.String()
			}
		case <-deadline:
			t.Fatalf("the shell never said %q; it said %q", marker, seen.String())
		}
	}
}

// TestAHostShellJoinsTheProjectsByobuSession is the operator's ask asserted at
// the point it takes effect: the argv that reaches exec.
//
// "per project byobu session i can attach to just over ssh, so your stuff is
// just byobu management." A shell of ours in a pty nobody else can reach is
// exactly what that rules out, and the difference is invisible from the panel -
// both draw a prompt. So it is measured on the command, and against the SESSION
// NAME their editor uses, because a session we named plausibly is one they
// cannot attach to.
func TestAHostShellJoinsTheProjectsByobuSession(t *testing.T) {
	if _, err := byobuBin(); err != nil {
		t.Skip("no byobu or tmux here, and this asserts what is handed to one")
	}

	shells := newAgentShells()
	sess, err := shells.start("01BYOBU", "flowy.dogfood", t.TempDir(), "", shellOnHost,
		agentSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting a host shell: %v", err)
	}
	defer sess.finish("test done")

	got := strings.Join(sess.cmd.Args, " ")
	if !strings.Contains(got, "new-session -A -s projectile/flowy_dogfood") {
		t.Fatalf(`a host shell did not join the project's session. argv: %q

It must be new-session -A on projectile/<project>, sanitised the way init.el
does it - the dot becomes an underscore because tmux silently makes it one, so
a name carrying the dot addresses a different session.`, got)
	}

	// THE OTHER HALF, so this cannot pass by every shell being a byobu client.
	// A project with no name has no session to join and must get a plain shell
	// rather than projectile/, which every unnamed project would share.
	plain, err := shells.start("01NONAME", "", t.TempDir(), "", shellOnHost,
		agentSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting a shell with no project: %v", err)
	}
	defer plain.finish("test done")
	if strings.Contains(strings.Join(plain.cmd.Args, " "), "new-session") {
		t.Fatalf("a shell with no project still joined a session: %q", plain.cmd.Args)
	}
}
