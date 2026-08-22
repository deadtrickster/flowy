# shellcheck shell=bash
#
# THE BOARD CAN SAY WHO IS CARRYING A ROW, INCLUDING A ROW IN NO ROOM.
#
# 01M0KXZ6VT, the operator: "i cannot reassign / assign todos". Right twice:
# every caller of assignTodo was inside a room's todo pane, so the page you open
# to see everything was the one you could not act on - and the room-scoped door
# cannot be built at all for a row that is in no room, which 3 of 26 open rows
# were. `git log -S'assignTodo' -- routes/Todos.tsx` is empty, so it was a gap
# and never a regression.
#
# The seeded row carries NO ROOM on purpose: it is the case that had no path
# anywhere, and a check that seeded into a room would pass while the reported
# bug survived.
#
# ASSERTED AS A WRITE. The control is typed into and the NODE is asked
# afterwards, because a console that draws an input and posts nothing is the
# likelier bug and would pass a check that only read the screen.
the_board_can_assign_a_row() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/board-assign-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$HANDLE_B"
}

check "the board can say who is carrying a row, including one in no room" \
	the_board_can_assign_a_row
