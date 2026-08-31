# shellcheck shell=bash
#
# A SHELL STARTED ON ANOTHER DEVICE.
#
# 01M1558DPM1HRGZNJGMVW24DHF item 3. The panel remembers its session id in
# localStorage and adopts it on mount, so the only shell a person could return
# to was one that browser had started. Start something on the laptop, pick up
# the phone, and the node was still running it with no way to reach it.
#
# The door landed first (3922ef0, GET /api/agent/sessions, operator-only). This
# asserts the panel half: a session this browser did not start is OFFERED, and
# pressing take carries THAT id into the adopt path rather than minting a second
# shell beside the one the person was reaching for.
#
# THE DOOR IS STUBBED IN THE BROWSER AND THAT IS THE POINT. Listing a session
# needs a live session, and VmShell's stop closes the socket without stopping
# the shell - so an end-to-end version would strand a host shell on every gate
# run. The rule under test is the panel's, and it does not depend on where the
# list came from. agentsessions_test.go owns the door's own behaviour, including
# that finished sessions are excluded.
#
# IT NEEDS THE OPERATOR'S TOKEN. Both /api/vm/* and /api/agent/sessions are
# operatorOnly; with an agent seat no panel is drawn at all.

a_shell_this_browser_did_not_start_can_be_taken() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/shell-take-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP"
}

check "a shell started on another device is offered, and taking it adopts that session" \
	a_shell_this_browser_did_not_start_can_be_taken
