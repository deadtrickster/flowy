package store

import "testing"

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
