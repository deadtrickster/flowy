package flowy

import (
	"os/exec"
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
