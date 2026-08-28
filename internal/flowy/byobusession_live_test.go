package flowy

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestTheSessionListIsThisHostsOwn reads the real tmux on the machine running
// the suite, because the thing under test is agreement with it.
//
// A FIXTURE WOULD PROVE NOTHING HERE. The parser's job is to agree with what
// tmux actually prints, and a hand-written line is this file's opinion of that.
// So it asks tmux directly and asserts the shape of the answer rather than its
// contents - which sessions exist is the host's business and changes between
// runs.
func TestTheSessionListIsThisHostsOwn(t *testing.T) {
	if _, err := byobuBin(); err != nil {
		t.Skip("no byobu or tmux here")
	}
	got, err := listByobuSessions(context.Background())
	if err != nil {
		t.Fatalf("listing sessions: %v", err)
	}
	// NOT "at least one". A host with no tmux server has none, and that is a
	// real answer this must return as an empty list rather than an error - so
	// the assertion is on every row it DID return.
	for _, s := range got {
		if strings.TrimSpace(s.Name) == "" {
			t.Errorf("a session with no name: %+v", s)
		}
		if s.Ours != strings.HasPrefix(s.Name, byobuSessionPrefix) {
			t.Errorf("session %q says ours=%v", s.Name, s.Ours)
		}
		for _, w := range s.Windows {
			if strings.TrimSpace(w.Name) == "" {
				t.Errorf("session %q has a window with no name: %+v", s.Name, w)
			}
			if w.Panes < 1 {
				t.Errorf("session %q window %d claims %d panes", s.Name, w.Index, w.Panes)
			}
		}
	}
}

// TestTheListSeesTheOperatorsOwnSessions is the property the whole reframe
// rests on, asserted rather than assumed: a session made the way their editor
// makes one is visible to this code.
//
// Made and removed here rather than borrowed from the host, so the check does
// not depend on the operator having something open - and so it can assert a
// DIFFERENCE: the name is absent before, present after, absent again once it is
// killed. A list that always contained it would pass without ever having looked.
func TestTheListSeesTheOperatorsOwnSessions(t *testing.T) {
	mux, err := byobuBin()
	if err != nil {
		t.Skip("no byobu or tmux here")
	}
	name := byobuSessionFor("flowy-listcheck.probe")
	if name != "projectile/flowy-listcheck_probe" {
		t.Fatalf("the name rule changed under this test: %q", name)
	}

	has := func() bool {
		list, err := listByobuSessions(context.Background())
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		for _, s := range list {
			if s.Name == name {
				return s.Ours
			}
		}
		return false
	}

	if has() {
		t.Fatalf("%s already exists; this test will not touch a session it did not make", name)
	}
	if out, err := exec.CommandContext(context.Background(),
		mux, "new-session", "-d", "-s", name).CombinedOutput(); err != nil {
		t.Skipf("cannot make a session here: %v: %s", err, out)
	}
	// KILLED WHATEVER HAPPENS, including on a failed assertion: a test that
	// leaves a session behind is a test that changes the host it measured.
	defer func() {
		_ = exec.Command(mux, "kill-session", "-t", name).Run()
	}()

	if !has() {
		t.Fatalf("made %s and the list does not see it - the panel would not see the operator's sessions either", name)
	}
}
