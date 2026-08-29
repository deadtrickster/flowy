package flowy

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// A SHELL IS A BYOBU SESSION, PER PROJECT, AND IT IS THE OPERATOR'S OWN.
//
// The operator: "look at how emacs integrates with byobu, i want the same here
// - per project byobu session i can attach to just over ssh, so your stuff is
// just byobu management." And: "all is byobu."
//
// So flowy does not have a session concept. It attaches to the session their
// editor already uses, which is also the one `ssh host; byobu attach -t NAME`
// reaches, and the panel is a client of it like any other.
//
// THE NAME IS THEIRS, COPIED RATHER THAN INVENTED. init.el:1568:
//
//	(defun my-byobu--session (project)
//	  "tmux session name for PROJECT, sanitized like `bb' (./: -> _)."
//	  (concat "projectile/"
//	          (replace-regexp-in-string "[.: ]" "_" (projectile-project-name project))))
//
// A DOT IS NOT COSMETIC. firecode's own source carries the reason (bin/firecode
// near session_name): tmux will not keep a dot in a session name - it silently
// becomes an underscore - so `-t my.project` addresses something that is not
// what was asked for, and a client that did not sanitise would attach to the
// wrong session or create a second one nobody can find.

// byobuSessionPrefix is the operator's, and every seat that wants their session
// has to spell it the same way.
const byobuSessionPrefix = "projectile/"

// byobuSessionFor is the session name a project's shell lives in.
//
// It takes the project NAME, not a path: the name is what firecode's roster and
// this console both address a project by, and it is what their own helper
// passes to projectile-project-name.
func byobuSessionFor(project string) string {
	name := strings.TrimSpace(project)
	if name == "" {
		return ""
	}
	// Exactly their three: dot, colon and space. Not a general slug - a rule
	// that replaced more would produce a name their editor never makes, and the
	// whole point is landing in the SAME session.
	name = strings.Map(func(r rune) rune {
		switch r {
		case '.', ':', ' ':
			return '_'
		}
		return r
	}, name)
	return byobuSessionPrefix + name
}

// byobuBin is byobu when it is there and tmux otherwise.
//
// The same order firecode picks, and for the same reason: byobu IS tmux
// underneath and keeps its status line, so a session made by one is addressable
// by the other. Measured on this host: byobu run with the node's own
// environment lands on the DEFAULT tmux socket, which is where the operator's
// projectile/* sessions live - so the panel and their editor really are in one
// session rather than two that look alike.
func byobuBin() (string, error) {
	if bin, err := exec.LookPath("byobu"); err == nil {
		return bin, nil
	}
	return exec.LookPath("tmux")
}

// byobuSession is one session on this host, as tmux reports it.
//
// EVERY SESSION, NOT ONLY OURS. The operator's projectile/* sessions are the
// point - the panel is a client of the same ones their editor uses - so a list
// that showed only what flowy had started would be a list of the wrong thing.
// `ours` says which are this convention's rather than hiding the rest.
type byobuSession struct {
	Name     string        `json:"name"`
	Windows  []byobuWindow `json:"windows"`
	Attached int           `json:"attached"`
	Created  string        `json:"created"`
	Ours     bool          `json:"ours"`
}

// byobuWindow is one window in a session: what it is called and what is in it.
type byobuWindow struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Command string `json:"command"`
	Panes   int    `json:"panes"`
}

// listByobuSessions asks tmux what exists.
//
// TWO CALLS, NOT ONE PER SESSION. list-sessions and list-windows -a each answer
// in one go with a format string, so this costs two processes however many
// sessions there are - a per-session call would make a busy host slower to look
// at than a quiet one, which is backwards.
//
// A TAB BETWEEN FIELDS, and it was a unit separator until a guest proved that
// wrong. tmux ESCAPES non-printable bytes in format output, and whether it does
// depends on the version: 3.6 on this host emitted a raw 0x1F and 3.4 in a
// firecode guest emitted the four characters `\037`. So every row came back as
// one field there, every row was dropped, and the door answered "no sessions"
// on a host that had several - a wrong answer shaped exactly like a right one.
//
// Both versions pass a tab through untouched, measured with od on each.
//
// THE FREE-FORM FIELDS GO LAST and are taken with SplitN, so a name holding a
// tab lands in the last field whole instead of shifting every column after it.
func listByobuSessions(ctx context.Context) ([]byobuSession, error) {
	mux, err := byobuBin()
	if err != nil {
		return nil, errNoByobu
	}
	const sep = "\t"

	// EXPLICIT BUFFERS, not Output(). Output() only captures stderr into
	// ExitError.Stderr when Stderr was nil, and "no server running" arrives on
	// stderr - so whether the message is in hand at all depended on a condition
	// this code did not state. It cost a gate: every one of these tests failed
	// in a guest with "exit status 1" while the message that would have made it
	// an ordinary empty answer was somewhere this code never looked.
	asking := exec.CommandContext(ctx, mux, "list-sessions", "-F",
		strings.Join([]string{"#{session_attached}", "#{session_created}", "#{session_name}"}, sep))
	var said, complained bytes.Buffer
	asking.Stdout = &said
	asking.Stderr = &complained
	if err := asking.Run(); err != nil {
		// NO SERVER IS NOT AN ERROR. tmux exits non-zero with "no server
		// running" when nothing has ever been started, and that is a true and
		// ordinary answer: no sessions. Reporting it as a failure would put a
		// red banner in front of somebody whose only crime is a fresh login.
		//
		// Both streams are read, because which one carries it is a tmux
		// version's business and not a thing worth being wrong about twice.
		whole := complained.String() + said.String()
		if strings.Contains(whole, "no server running") || strings.Contains(whole, "no server") {
			return []byobuSession{}, nil
		}
		if trimmed := strings.TrimSpace(whole); trimmed != "" {
			return nil, errors.New(trimmed)
		}
		return nil, err
	}
	out := said.Bytes()

	byName := map[string]*byobuSession{}
	var order []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, sep, 3)
		if len(f) < 3 {
			// A ROW THIS CANNOT READ IS AN ERROR, NOT A SKIP, and that is the
			// whole reason the escaping went unnoticed: `continue` turned an
			// unreadable answer into an empty host, which is a state the caller
			// has no way to question. Saying so makes the next version
			// difference a red instead of a quiet zero.
			return nil, errors.New("tmux answered a session line this cannot read: " +
				strconv.Quote(line))
		}
		s := &byobuSession{
			Attached: atoiOr(f[0], 0),
			Created:  f[1],
			Name:     f[2],
			Ours:     strings.HasPrefix(f[2], byobuSessionPrefix),
			Windows:  []byobuWindow{},
		}
		byName[s.Name] = s
		order = append(order, s.Name)
	}

	wins, err := exec.CommandContext(ctx, mux, "list-windows", "-a", "-F",
		strings.Join([]string{
			"#{window_index}", "#{window_active}", "#{window_panes}",
			"#{session_name}", "#{window_name}", "#{pane_current_command}",
		}, sep),
	).Output()
	// A FAILURE HERE LEAVES THE SESSIONS WITH NO WINDOWS RATHER THAN LOSING
	// THEM. The sessions are already known; windows are the detail.
	if err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(wins), "\n"), "\n") {
			f := strings.SplitN(line, sep, 6)
			if len(f) < 6 {
				continue
			}
			s := byName[f[3]]
			if s == nil {
				continue
			}
			s.Windows = append(s.Windows, byobuWindow{
				Index:   atoiOr(f[0], 0),
				Active:  f[1] == "1",
				Panes:   atoiOr(f[2], 0),
				Name:    f[4],
				Command: f[5],
			})
		}
	}

	list := make([]byobuSession, 0, len(order))
	for _, name := range order {
		list = append(list, *byName[name])
	}
	return list, nil
}

var errNoByobu = errors.New(
	"this node has no byobu or tmux on its PATH, so it cannot hold a shell " +
		"session anybody else could attach to")

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fallback
	}
	return n
}

// openByobuWindow makes a new window in a session that already exists.
//
// A WINDOW, NOT A SESSION, and that is the whole shape the operator asked for:
// "all is byobu" and "fcvms are ok to be attached to byobu windows too". One
// session per project, and what runs in it - a login shell, a VM, a build - is
// a window somebody opened.
//
// THE SESSION MUST ALREADY BE THERE. new-window against a name that does not
// exist creates nothing and fails, and that is the right answer rather than
// quietly making a session as a side effect of asking for a window: a typo
// would otherwise leave a session nobody meant behind, with the panel showing
// it as though it had been asked for.
func openByobuWindow(ctx context.Context, session string, command []string) error {
	mux, err := byobuBin()
	if err != nil {
		return errNoByobu
	}
	if strings.TrimSpace(session) == "" {
		return errors.New("which session the window goes in has to be said")
	}
	// A LEADING DASH IS AN OPTION, NOT A NAME. Argument vectors keep a name
	// with a space or a backtick from becoming a command; they do not stop tmux
	// from reading "-d" as a flag, because by then it is the same string.
	if strings.HasPrefix(session, "-") {
		return errors.New("a session name may not begin with a dash")
	}

	args := []string{"new-window", "-t", session}
	if len(command) > 0 {
		// -- so everything after it is the command, however it begins. Without
		// it a command starting with a dash is tmux's option again, one layer
		// further in.
		args = append(args, "--")
		args = append(args, command...)
	}
	out, err := exec.CommandContext(ctx, mux, args...).CombinedOutput()
	if err != nil {
		said := strings.TrimSpace(string(out))
		if said == "" {
			said = err.Error()
		}
		return errors.New(said)
	}
	return nil
}

// killByobuSession ends a session and everything running in it.
//
// THIS IS THE DANGEROUS ONE AND IT IS TREATED THAT WAY. A session here is not
// flowy's: projectile/* is where the operator's editor lives, and killing one
// takes down whatever they left running in it - a build, an agent, an ssh they
// walked away from. There is no undo and no confirmation prompt at this layer,
// so the caller has to say what it believes it is killing and be right.
//
// EXPECT IS THE GUARD. The caller passes the window count it saw, and this
// refuses if the session has changed since - the same shape as `todo claim
// --expect`, and for the same reason: a list a person read ten seconds ago is
// not the state of the machine now, and "kill projectile/duckdb, which had one
// window" is a claim that can be checked where "kill projectile/duckdb" is not.
//
// A NEGATIVE EXPECT MEANS THE CALLER DID NOT LOOK, which is refused rather than
// waved through: an unchecked kill is exactly what this exists to prevent.
func killByobuSession(ctx context.Context, name string, expectWindows int) error {
	mux, err := byobuBin()
	if err != nil {
		return errNoByobu
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("which session to end has to be said")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("a session name may not begin with a dash")
	}
	if expectWindows < 0 {
		return errors.New(
			"say how many windows the session had when you looked, so this can refuse if it has changed")
	}

	list, err := listByobuSessions(ctx)
	if err != nil {
		return err
	}
	var found *byobuSession
	for i := range list {
		if list[i].Name == name {
			found = &list[i]
			break
		}
	}
	if found == nil {
		// GONE IS NOT AN ERROR TO ACT ON, but it is not success either: the
		// caller asked to end something that is not there, and saying so is
		// what stops "it worked" from meaning two different things.
		return errors.New("no session called " + name + " on this host")
	}
	if len(found.Windows) != expectWindows {
		return errors.New(
			"that session has " + strconv.Itoa(len(found.Windows)) +
				" windows now and you were looking at " + strconv.Itoa(expectWindows) +
				" - read it again before ending it")
	}

	out, err := exec.CommandContext(ctx, mux, "kill-session", "-t", name).CombinedOutput()
	if err != nil {
		said := strings.TrimSpace(string(out))
		if said == "" {
			said = err.Error()
		}
		return errors.New(said)
	}
	return nil
}
