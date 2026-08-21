# shellcheck shell=bash
#
# THE BOARD SAYS WHICH ROW THE RAIL IS COUNTING.
#
# The operator, 2026-08-21: "have one unread todo, went to todo list - no idea
# which one, fix". The dot beside `todos` draws mine_todo from /api/nag - rows
# assigned to you and not started - and the list it sends you to marked none of
# them. A count that cannot be answered is a nag, not a signal: the only way to
# clear it was to open rows until it went away.
#
# BOTH ARMS, because the failure has two signs. A board that marks nothing and a
# board that marks everything are the same defect, and the second is worse - a
# mark on every row is a mark on none, and a reader who trusts it stops looking.
# The control row is assigned to the SAME principal and left ACTIVE, so the two
# rows differ only in the property under test; a control owned by somebody else
# would also pass on a board marking "mine" rather than "mine and not started",
# which is a different number from the one on the rail.
#
# The ids come from the node, from the loop that produces the count, so this
# also fails if the console ever re-derives the rule locally and drifts.

the_board_marks_the_row_the_rail_counts() {
	cd "$ROOT/web" || return 1
	node scripts/waiting-row-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the board marks the row the rail is counting, and only that row" \
	the_board_marks_the_row_the_rail_counts
