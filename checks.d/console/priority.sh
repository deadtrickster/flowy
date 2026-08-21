# shellcheck shell=bash
#
# A ROW CAN BE TOLD TO HAPPEN FIRST, AND THE QUEUE BELIEVES IT.
#
# The operator, on the board: "add priorities to todos, and merges" - filed with
# sixteen unowned rows on it and nothing on any of them saying which they wanted
# first. A board with no order is one where every reader picks by their own
# taste, which is what this fleet did all night.
#
# The browser half is not decoration: setting a priority has to reach the NODE,
# or it is a chip that survives until the next poll. And the ORDER has to change
# with it - a ranking nothing sorts by is a label.
#
# It raises three rows and closes them again, per 01M0HADJ2R.

a_row_can_be_told_to_happen_first() {
	cd "$ROOT/web" || return 1
	node scripts/priority-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" general
}

check "a row is ranked from the panel, the node keeps it, and the queue reorders" \
	a_row_can_be_told_to_happen_first
