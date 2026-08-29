package flowy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheSessionNameIsTheOperatorsOwn pins the naming against the rule in their
// init.el, because the whole value of this is landing in the SAME session their
// editor and an ssh attach use. A name we merely think is reasonable is a
// second session that looks right and nobody else can reach.
//
// THE DOT CASE IS THE ONE THAT MATTERS. tmux silently turns a dot in a session
// name into an underscore, so `-t flowy.dogfood` addresses flowy_dogfood - and
// a client that did not sanitise would either attach to something it did not
// name or create a duplicate. That is why their helper does it and why this
// copies it rather than approximating it.
func TestTheSessionNameIsTheOperatorsOwn(t *testing.T) {
	for _, c := range []struct{ project, want string }{
		{"flowy", "projectile/flowy"},
		{"flowy.dogfood", "projectile/flowy_dogfood"},
		{"a:b", "projectile/a_b"},
		{"two words", "projectile/two_words"},
		{"all.three: here", "projectile/all_three__here"},
		// Their rule replaces THREE characters and no others. A dash and an
		// underscore survive, and so does a slash - projectile/orioledb-ik is a
		// live session on this host, so a slug that ate the dash would miss it.
		{"orioledb-ik-primary", "projectile/orioledb-ik-primary"},
		{"already_under", "projectile/already_under"},
	} {
		if got := byobuSessionFor(c.project); got != c.want {
			t.Errorf("byobuSessionFor(%q) = %q, want %q", c.project, got, c.want)
		}
	}

	// EMPTY IS NOT A SESSION. A blank project would otherwise address
	// "projectile/", which is a name tmux would accept and nobody meant - and
	// every project with no name would share it.
	if got := byobuSessionFor("  "); got != "" {
		t.Errorf("a project with no name got session %q; it must get none", got)
	}
}

// TestANamedSessionIsNotAFlag is the one piece of this that a browser controls.
//
// `mux` comes off a WebSocket message and ends up as an argument to tmux. An
// argument vector is what keeps a name with a backtick or a space from becoming
// a command - that rule is in CLAUDE.md and it is followed here - but it does
// NOT stop a name that looks like a flag: `new-session -A -s -d` is tmux being
// handed an option, not a session called "-d", and argv cannot tell them apart
// because at that point they are the same string.
func TestANamedSessionIsNotAFlag(t *testing.T) {
	shells := newAgentShells()
	high := make(chan []byte, 4)
	low := make(chan []byte, 4)
	s := &server{agents: shells}
	r := httptest.NewRequest(http.MethodGet, "/api/agent/socket", nil)

	a, why := s.attachAgent(context.Background(), r,
		agentControl{Type: "attach", Project: "flowy", Where: string(shellOnHost),
			Mux: "-d", Rows: 24, Cols: 80},
		high, low)
	if a != nil {
		t.Fatal("a session name beginning with a dash was accepted, and tmux would read it as an option")
	}
	if !strings.Contains(why, "dash") {
		t.Fatalf("refused without saying why a leading dash is the problem: %q", why)
	}
	if got := len(shells.by); got != 0 {
		t.Fatalf("the refusal still started %d session(s)", got)
	}
}

// TestNoServerIsAnEmptyHostNotAFailure pins the case a guest found and this
// host cannot reach.
//
// A machine where nothing has ever started tmux answers `list-sessions` with
// exit 1 and "no server running" ON STDERR. That is a true and ordinary
// statement - there are no sessions - and it must come back as an empty list,
// not as an error: a fresh login is not a broken node.
//
// IT WAS WRONG AND THE GATE SAID SO. Every live test here failed in a firecode
// guest with "exit status 1", because the message was read from
// ExitError.Stderr, which Output() only fills when Stderr was nil - a condition
// the code depended on and never stated. The host could not reproduce it: this
// machine always has a server, and TMUX_TMPDIR is ignored from inside a
// session, so there was no way to find it here except by reading the guest.
//
// The command is run against a binary that behaves like tmux with no server,
// rather than against tmux - the point is what THIS code does with that answer,
// and a host with a server cannot produce it.
func TestNoServerIsAnEmptyHostNotAFailure(t *testing.T) {
	// BOTH OF THE THINGS TMUX SAYS, because which one it says depends on
	// whether the socket DIRECTORY exists - and the second is what a machine
	// says when nothing has started a server since it booted, which is the
	// ordinary state of a fresh guest and the one a gate meets first. Matching
	// only the first left every guest run reporting a broken node.
	for _, said := range []string{
		"no server running on /tmp/tmux-1000/default",
		"error connecting to /tmp/tmux-1000/default (No such file or directory)",
	} {
		dir := t.TempDir()
		stand := filepath.Join(dir, "tmux")
		// On stderr, nothing on stdout, exit 1 - exactly what tmux does.
		script := "#!/bin/sh\necho '" + said + "' >&2\nexit 1\n"
		if err := os.WriteFile(stand, []byte(script), 0o755); err != nil {
			t.Fatalf("writing the stand-in: %v", err)
		}
		t.Setenv("PATH", dir)

		got, err := listByobuSessions(context.Background())
		if err != nil {
			t.Fatalf(`a host answering %q came back as an error: %v

That is a machine where nobody has started a session yet, which is an empty
list. Reporting it as a failure puts a red banner in front of a fresh login.`, said, err)
		}
		if len(got) != 0 {
			t.Fatalf("a host with no server answered %d sessions", len(got))
		}
	}
}

// TestAnUnreadableAnswerIsNotAnEmptyHost is the defect that cost a gate, kept
// as a test because it was invisible in exactly the way this repo cares about.
//
// The separator between fields was a unit separator, and tmux ESCAPES
// non-printable bytes in format output - whether it does depends on the
// version. 3.6 on this host emitted a raw 0x1F, 3.4 in a firecode guest emitted
// the four characters \037. So in the guest every row arrived as ONE field,
// every row hit `continue`, and the door answered "no sessions" for a host that
// had several: a wrong answer shaped exactly like a right one, on the door
// whose entire subject is telling those apart.
//
// The separator is a tab now, which both versions pass through untouched. But
// the reason it went unnoticed was the `continue`, so THAT is what this pins: a
// line this cannot read has to be an error. A future tmux that changes its
// output again should turn the panel red, not empty.
func TestAnUnreadableAnswerIsNotAnEmptyHost(t *testing.T) {
	dir := t.TempDir()
	stand := filepath.Join(dir, "tmux")
	// Exactly what tmux 3.4 did with a unit separator: the escape written out
	// as text, so the whole row is one field.
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"list-sessions) echo 'projectile/duckdb\\0370\\0371786396234' ;;\n" +
		"*) : ;;\n" +
		"esac\n"
	if err := os.WriteFile(stand, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stand-in: %v", err)
	}
	t.Setenv("PATH", dir)

	got, err := listByobuSessions(context.Background())
	if err == nil {
		t.Fatalf(`tmux answered a row this cannot read and the door reported %d sessions.

An answer that cannot be parsed is not an empty host. Reporting it as one is how
a version difference becomes a panel that quietly shows nothing.`, len(got))
	}
	if !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("refused without saying the line was unreadable: %v", err)
	}
}

// TestAHostShellDoesNotNeedFirecode is the branch whose whole point is that the
// session belongs to the host rather than to a VM - so needing firecode to open
// one was the wrong requirement in the wrong place.
//
// FOUND IN A GUEST, where firecode is absent: a bad session name came back as
// "this node has no firecode on its PATH", a true sentence about something the
// caller never asked about. Two things were wrong and both are worth keeping
// apart: the workdir was resolved through firecode even for a host shell, and
// the caller's own input was checked after that rather than before.
func TestAHostShellDoesNotNeedFirecode(t *testing.T) {
	// A PATH WITH NEITHER firecode NOR A MULTIPLEXER is the harshest version of
	// the machine this is about, and the refusal must still be about what was
	// asked for.
	t.Setenv("PATH", t.TempDir())

	s := &server{agents: newAgentShells()}
	r := httptest.NewRequest(http.MethodGet, "/api/agent/socket", nil)
	high := make(chan []byte, 4)
	low := make(chan []byte, 4)

	_, why := s.attachAgent(context.Background(), r,
		agentControl{Type: "attach", Project: "flowy", Where: string(shellOnHost),
			Mux: "-d", Rows: 24, Cols: 80},
		high, low)
	if !strings.Contains(why, "dash") {
		t.Fatalf(`a host shell with a bad session name was refused with: %q

It has to be refused for the reason the caller can act on. firecode is not
needed to open a shell on this host, and naming it here sends somebody to
install something that would not have helped.`, why)
	}
}
