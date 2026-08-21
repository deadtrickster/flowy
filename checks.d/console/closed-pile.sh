# shellcheck shell=bash
#
# THE PILE OF CLOSED ROOMS HAS A BOTTOM.
#
# The operator: "I hope closed rooms accordeon does lazy loading, paginated by
# scroll". MEASURED before building anything: 29 rooms on the dogfood node, so
# the worst case is 29 buttons - which is not a rendering cost. Virtualising it
# would be work with nothing to show until rooms are in the hundreds.
#
# What IS real is a layout cost, and it is the same complaint they made about
# the todo summary in the same breath: the open list scrolls inside itself and
# the closed pile did not, so opening it pushed the rail's own footer - the
# token box, the log out - below the fold.
#
# So the assertion is that the pile is BOUNDED and scrolls itself, not that it
# renders fewer rooms. Negative-controlled by removing the bound: 800px in an
# 800px window against 320 with it.

the_closed_pile_has_a_bottom() {
	cd "$ROOT/web" || return 1
	node scripts/closed-pile-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the closed-rooms pile is bounded and scrolls inside itself" \
	the_closed_pile_has_a_bottom
