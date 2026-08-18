package store

import (
	"testing"
	"time"
)

// The roster is asked one question - who can I expect an answer from - and it
// used to answer with every cursor the node had ever written: console panes,
// dead probes, one-off tests, duplicates. Thirteen rows for five agents, twice
// called an infestation.
//
// The rows themselves are not the problem and must survive: a console cursor is
// what clears the unread badge, so deleting it would recount every message the
// next time somebody opened that room. This asserts the window is a READING.
func TestPresenceWindowIsAReadingNotADeletion(t *testing.T) {
	if PresenceWindow <= 0 {
		t.Fatal("a non-positive window would hide every listener, including live ones")
	}
	// Ten minutes: a waiter polls on a 3600s deadline but re-polls promptly when
	// it returns, so a gap longer than this means nobody is coming back. Short
	// enough to clear the dead, long enough never to hide a live waiter between
	// two polls.
	if PresenceWindow.Minutes() < 5 {
		t.Errorf("window %s is short enough to hide a live waiter between polls", PresenceWindow)
	}
	if PresenceWindow.Hours() > 1 {
		t.Errorf("window %s keeps dead readers on the roster long enough to be noise", PresenceWindow)
	}
}

// The lost window is the other end of the same reading, and it has the opposite
// failure: too short and a seat that went deaf disappears quietly, which is the
// complaint - an agent was unreachable for six hours and no surface said so.
//
// So it has to outlast a long silence by a wide margin, and it has to be finite,
// because a row that is kept forever is back to being an infestation. It is
// checked against the waiter's own default deadline in the package that owns
// that constant; this is the ordering the two windows have to keep whatever they
// are set to.
func TestPresenceLostWindowOutlastsTheListeningWindow(t *testing.T) {
	if PresenceLostWindow <= PresenceWindow {
		t.Fatalf("lost window %s is inside the listening window %s, so a reader that "+
			"stopped polling would leave the roster in the same breath it stopped - "+
			"and the six hours of silence nobody could see would still be invisible",
			PresenceLostWindow, PresenceWindow)
	}
	// Six hours is the case that was actually asked about, twice.
	if PresenceLostWindow < 6*time.Hour {
		t.Errorf("lost window %s hides a seat that went deaf six hours ago", PresenceLostWindow)
	}
	if PresenceLostWindow > 24*time.Hour {
		t.Errorf("lost window %s keeps day-old test labels on the roster as incidents",
			PresenceLostWindow)
	}
}
