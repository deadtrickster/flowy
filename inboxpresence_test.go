package main

import (
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// The two windows the roster is read through are answers about the WAITER, and
// the waiter's own numbers live here rather than in the store. So this is where
// they can be checked against each other, and drift between them is silent
// everywhere else: nothing fails, the roster just starts giving a different
// answer than the waiter's behaviour justifies.

// A listening waiter re-polls as soon as the server's window returns, however
// long its total deadline is and however quiet the room is. So the listening
// window is a multiple of the SERVER WINDOW, not of the deadline - a waiter with
// eight hours left on its budget is still expected to be seen every twenty-five
// seconds, and reading its silence any other way is what let a dead seat pass
// for a quiet one.
func TestPresenceWindowIsManyServerWindowsWide(t *testing.T) {
	if store.PresenceWindow <= waitWindow {
		t.Fatalf("the listening window %s is not wider than one server poll window %s: "+
			"a waiter that is merely between polls would read as gone",
			store.PresenceWindow, waitWindow)
	}
	if store.PresenceWindow < 10*waitWindow {
		t.Errorf("the listening window %s is only %.0f poll windows wide - a slow node "+
			"or one retry would retire a live waiter",
			store.PresenceWindow, store.PresenceWindow.Seconds()/waitWindow.Seconds())
	}
}

// And the lost window is the waiter's own budget. Past it, a waiter that armed
// itself would have hit its deadline and exited on purpose, so an unfinished
// poll older than that is a row nobody cleaned up rather than a seat that went
// deaf while somebody was waiting on it. Before it, it is the answer to "why is
// this agent not replying", which is the question that got asked twice with
// nothing anywhere able to answer it.
func TestPresenceLostWindowFollowsTheWaitersDeadline(t *testing.T) {
	budget := time.Duration(defaultInboxDeadline) * time.Second
	if store.PresenceLostWindow != budget {
		t.Errorf("a reader that stopped mid-poll is kept for %s while a waiter's own "+
			"deadline is %s - shorter and a seat that just went deaf disappears before "+
			"anybody asks, longer and a waiter that simply finished its budget is "+
			"reported as an incident",
			store.PresenceLostWindow, budget)
	}
}
