# shellcheck shell=bash
#
# THE PANEL SEES THE HOST'S SESSIONS, INCLUDING ONES IT DID NOT START.
#
# The operator: "per project byobu session i can attach to just over ssh, so
# your stuff is just byobu management." Everything about that rests on flowy
# looking at the SAME sessions their editor uses - and a panel listing only what
# it started looks identical from a screenshot and is useless.
#
# So the check makes a session from outside flowy, named the way init.el names
# one, and asserts the door and the pane both show it. Removed again whatever
# happens: a check that leaves a session behind has changed the host it measured.
#
# The door is asserted operator-only as a difference first, because it names
# what is running on the machine serving this console.

the_panel_sees_the_hosts_sessions() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/shell-sessions-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" "$TOKEN_A"
}

check "the panel lists the host's byobu sessions, including ones it did not start" \
	the_panel_sees_the_hosts_sessions
