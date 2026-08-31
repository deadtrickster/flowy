package flowy

import (
	"testing"
	"time"
)

// A shell this node is running can be found by a device that did not start it.
//
// 01M1558DPM1HRGZNJGMVW24DHF item 3. The panel remembers its session id in
// localStorage and adopts it on mount, so the browser that STARTED a shell can
// come back to it and no other can - "I started it on the laptop and I am on
// the phone" had no answer. Not for want of a capability: the registry has held
// every session all along and nothing ever asked it for the list.
func TestLiveListsTheShellsAnotherDeviceCouldTake(t *testing.T) {
	now := time.Now()
	shells := newAgentShells()

	mk := func(id, project string, where shellWhere, started time.Time, readers int, active time.Time, done bool) {
		s := &agentSession{
			id: id, project: project, where: where, started: started,
			readers: map[chan []byte]struct{}{}, active: active, done: done,
		}
		for i := 0; i < readers; i++ {
			s.readers[make(chan []byte, 1)] = struct{}{}
		}
		shells.by[id] = s
	}

	mk("older", "flowy", "vm", now.Add(-30*time.Minute), 0, now.Add(-9*time.Minute), false)
	mk("newer", "Lab", "host", now.Add(-5*time.Minute), 2, now.Add(-time.Minute), false)
	// A finished session must not be offered: attaching to one hands back a
	// scrollback and an immediate "it ended", which reads as a shell that died
	// on being opened.
	mk("finished", "flowy", "vm", now.Add(-time.Minute), 0, now, true)

	live := shells.live(now)
	if len(live) != 2 {
		t.Fatalf("expected the two running shells and not the finished one, got %d: %+v", len(live), live)
	}

	// NEWEST FIRST. The shell somebody is looking for is almost always the one
	// they just walked away from.
	if live[0].ID != "newer" {
		t.Errorf("the list does not put the newest first: %+v", live)
	}

	// WATCHERS IS A COUNT, NOT A FLAG, and zero is the interesting value: a
	// shell nobody is holding. Two says somebody else is looking at it, which a
	// person wants to know BEFORE typing into it.
	byID := map[string]LiveShell{}
	for _, l := range live {
		byID[l.ID] = l
	}
	if byID["newer"].Watchers != 2 {
		t.Errorf("a shell with two browsers on it reports %d", byID["newer"].Watchers)
	}
	if byID["older"].Watchers != 0 {
		t.Errorf("a shell nobody is holding reports %d watchers", byID["older"].Watchers)
	}

	// AND HOW LONG IT HAS BEEN QUIET. A list of sessions with no ages is one
	// where the shell you want and the shell you forgot look identical.
	if byID["older"].Idle < 8*60 {
		t.Errorf("a shell quiet for nine minutes reports %ds idle", byID["older"].Idle)
	}
	if byID["newer"].Idle > 2*60 {
		t.Errorf("a shell that spoke a minute ago reports %ds idle", byID["newer"].Idle)
	}

	if byID["older"].Project != "flowy" || byID["newer"].Where != "host" {
		t.Errorf("the list does not say which project and which machine: %+v", live)
	}
}

// An empty node answers an empty list rather than nothing at all.
func TestLiveOnAnIdleNodeIsAnEmptyList(t *testing.T) {
	live := newAgentShells().live(time.Now())
	if live == nil {
		t.Fatal("an idle node answered nil - a reader cannot tell that from a door that failed")
	}
	if len(live) != 0 {
		t.Fatalf("an idle node listed %d sessions", len(live))
	}
}
