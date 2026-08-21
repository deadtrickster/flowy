# shellcheck shell=bash
#
# A DEAD CREDENTIAL SAYS SO, INSTEAD OF DRAWING AN EMPTY CONSOLE.
#
# 01M0K76WY4. The operator reported "ui stopped working" and it was working:
# every read answered 401, every pane drew its own empty state, and the frame
# looked exactly as it always does. Measured against the live node with a
# rejected credential - /memory went from 11521 characters to 454, the sidebar's
# thirty rooms to none, and not one word said why.
#
# NO AGENT COULD SEE IT. Every other check here authenticates with a bearer
# token; a person's browser authenticates with a session, and perm.go scopes
# those differently. It took four agents an hour and a read of the sessions
# table - which the console cannot consult - to find.
#
# THE FIXTURE IS FREE: this needs a broken CREDENTIAL, not a broken session. A
# 401 is a 401 whether the cookie was swept, the session expired, or the string
# is nonsense.
#
# TWO ARMS. The second is the one that matters as much: a console that shows the
# banner permanently would pass a single-arm check and be worse than the bug,
# because a warning nobody can clear is one everybody learns to ignore.

a_dead_credential_says_so() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/credential-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "a dead credential says so rather than emptying the console" \
	a_dead_credential_says_so
