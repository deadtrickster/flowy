# shellcheck shell=bash
#
# THE AGENTS PANE MIRRORS FCTOP, AND SAYS WHEN IT CANNOT.
#
# The operator, asking for the two panes: "and agents tab then essentially
# mirror fc-top". /api/vm/list is the host's view and deliberately unprobed -
# `firecode ps` costs 25s per guest in series - so the readings come from
# fctop, which probes in parallel and was measured at 2.3s for two VMs.
#
# WHAT IS ASSERTED, and the first one is the point:
#
#   A NODE WITHOUT FCTOP ANSWERS 503, NOT AN EMPTY FLEET. The whole subject of
#   that door is the difference between "nothing is running" and "I could not
#   ask", which is what fctop's STATUS column exists to carry. A door that
#   answered [] would destroy exactly the distinction it is there to serve.
#
#   AS A DIFFERENCE, both worlds in one pass: the same request with fctop off
#   PATH and with it on. One reading cannot tell a capability check from a door
#   that always 503s.
#
#   And the pane draws the STATUS WORD, not a colour. "STALE 42s" carries a
#   number no colour can, and a row whose guest never answered must not show
#   zeros beside one that answered zero.
#
# WHAT IS NOT ASSERTED: the readings themselves. Their truth is fctop's, tested
# in fctop's own suite against its --fake and --replay fleets; asserting them
# here would be this repo holding a second opinion about staleness and the two
# would drift. What is asserted is that they arrive and are drawn as given.

the_agents_pane_reads_fctop_or_says_why() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/vm-top-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" "$TOKEN_A"
}

check "the readings door answers 503 without fctop and a frame with it, and the pane draws the status word" \
	the_agents_pane_reads_fctop_or_says_why
