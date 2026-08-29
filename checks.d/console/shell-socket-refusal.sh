# shellcheck shell=bash
#
# "LOG IN" AND "YOUR SHELL DIED" WERE THE SAME SCREEN.
#
# 01M154DRDCD3P2M8FKNG0RCC30. A browser cannot put an Authorization header on a
# websocket handshake, so /api/agent/socket can only authenticate a browser by
# its session cookie - and a console holding a token in localStorage, which
# every other panel on the page accepts, is refused. The panel rendered that as
# the shell ending, with the words "the VM may still be running", about a socket
# that never opened and a guest that was never asked for.
#
# The suite's console checks authenticate exactly that way - the operator's
# token in localStorage and no cookie - so this reaches the refusal
# deterministically rather than needing a second kind of user to exist.
#
# THE OTHER HALF OF THAT ROW IS NOT HERE. Letting a token open the socket needs
# a short-lived ticket door, which is a new credential kind on the widest route
# this node has. That is the operator's call and is left alone; this is the half
# the row says is correct whatever is decided.

a_refused_handshake_is_not_a_dead_shell() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/shell-socket-refusal-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP"
}

check "a console holding only a token is refused the shell socket and told why" \
	a_refused_handshake_is_not_a_dead_shell
