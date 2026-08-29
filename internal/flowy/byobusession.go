package flowy

import (
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
// A UNIT SEPARATOR BETWEEN FIELDS, never a space or a colon: a window's command
// is an arbitrary command line and a session name may hold anything tmux
// accepts, so any printable delimiter is one somebody's build command contains.
func listByobuSessions(ctx context.Context) ([]byobuSession, error) {
	mux, err := byobuBin()
	if err != nil {
		return nil, errNoByobu
	}
	const sep = "\x1f"

	out, err := exec.CommandContext(ctx, mux, "list-sessions", "-F",
		strings.Join([]string{"#{session_name}", "#{session_attached}", "#{session_created}"}, sep),
	).Output()
	if err != nil {
		// NO SERVER IS NOT AN ERROR. tmux exits non-zero with "no server
		// running" when nothing has ever been started, and that is a true and
		// ordinary answer: no sessions. Reporting it as a failure would put a
		// red banner in front of somebody whose only crime is a fresh login.
		if strings.Contains(string(exitOutput(err)), "no server running") {
			return []byobuSession{}, nil
		}
		return nil, err
	}

	byName := map[string]*byobuSession{}
	var order []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, sep)
		if len(f) < 3 {
			continue
		}
		s := &byobuSession{
			Name:     f[0],
			Attached: atoiOr(f[1], 0),
			Created:  f[2],
			Ours:     strings.HasPrefix(f[0], byobuSessionPrefix),
			Windows:  []byobuWindow{},
		}
		byName[s.Name] = s
		order = append(order, s.Name)
	}

	wins, err := exec.CommandContext(ctx, mux, "list-windows", "-a", "-F",
		strings.Join([]string{
			"#{session_name}", "#{window_index}", "#{window_name}",
			"#{window_active}", "#{pane_current_command}", "#{window_panes}",
		}, sep),
	).Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(wins), "\n"), "\n") {
			f := strings.Split(line, sep)
			if len(f) < 6 {
				continue
			}
			s := byName[f[0]]
			if s == nil {
				continue
			}
			s.Windows = append(s.Windows, byobuWindow{
				Index:   atoiOr(f[1], 0),
				Name:    f[2],
				Active:  f[3] == "1",
				Command: f[4],
				Panes:   atoiOr(f[5], 0),
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

// exitOutput is whatever a failed command said on stderr, which is where tmux
// puts "no server running".
func exitOutput(err error) []byte {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.Stderr
	}
	return nil
}

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
