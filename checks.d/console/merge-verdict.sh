# shellcheck shell=bash
#
# THE MERGES PANE TELLS THREE STATES APART: waiting, red, and landable.
#
# The operator read "0 may land, 4 refused" off a healthy queue and could not
# tell it from four rejected branches. One of those four had a red; three had
# never been gated against the current master. The node answers admissible:false
# for all of them and the pane drew one word.
#
# IT WENT WRONG TWICE, IN OPPOSITE DIRECTIONS, which is why this asserts three
# states rather than two: first every unmeasured row was drawn "refused", then
# the fix for that drew a RED row as "waiting for the gate" - because applyRed
# never writes gated_tip, so a failed gate refuses with merge.ungated, the same
# token as a row nobody has measured. mergered_test.go:50-59 asserts that code.
#
# So it seeds all three and requires the three badges to DIFFER. Any version
# that collapses a pair, in either direction, fails - and a pane that drew one
# word for everything cannot pass by accident.

the_merges_pane_tells_three_states_apart() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/merge-verdict-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the merges pane tells waiting, red and landable apart" \
	the_merges_pane_tells_three_states_apart
