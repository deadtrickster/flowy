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
#
# IN ITS OWN ROOM AND NOT #general. 01M0JR5K0D.
#
# The check opens a room's todo panel, ranks one row and then clicks another,
# and the summary card of the first is an OVERLAY - deliberately, and "a chat
# todo opens a popup at its own row" asserts the rows below it do not move. So
# the card hangs over whatever is drawn beneath it, and WHICH control that is
# depends on how many rows are above it and how their titles wrap.
#
# In #general that row count is whatever the rest of the suite happened to
# raise, so this check was measuring the room as much as the ranking. Measured
# across six runs while a sidebar control was being added: it flipped in BOTH
# directions between the filtered and full suites, which is a boundary moving
# with the layout rather than a race.
#
# A private room makes the panel hold exactly the three rows this check raises,
# whatever else has run, and makes the filtered and full answers the same
# question. It is a pure geometry check, so that is unambiguously right here -
# the other four rooms on 01M0JR5K0D are not the same argument, because two of
# them assert over a LIST and a list of one may not exercise what they are for.

a_row_can_be_told_to_happen_first() {
	cd "$ROOT/web" || return 1
	node scripts/priority-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" priorityroom
}

check "a row is ranked from the panel, the node keeps it, and the queue reorders" \
	a_row_can_be_told_to_happen_first
