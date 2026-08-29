# shellcheck shell=bash
#
# A ROW ON THE BOARD SAYS WHEN IT LAST MOVED.
#
# The board carried no time anywhere: a row raised this morning and one nobody
# had touched since June rendered identically, on a list hundreds long. The only
# way to tell them apart was to open each.
#
# `updated` rather than `created`, because the question a board is asked is "has
# this moved" - a row raised in June and answered today is not stale. Both are
# on the title.
#
# ASSERTED AGAINST THE NODE'S OWN VALUE. A regex for something-time-shaped
# passes on a row showing a DIFFERENT row's stamp, or last week's, or the word
# "now" - so the check raises a row, reads what the door recorded, and requires
# the page to be showing that instant.
#
# AND IN THE CONSOLE'S ONE FORMAT: clock(), 01M10Y3JBD, "all time labels must
# show the date 'if not today'". A row raised today shows a time and no date,
# exactly as the room does. A second time format on one page is the defect that
# rule was written against.

the_board_says_when_a_row_moved() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/todo-when-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "a row on the board says when it last moved, in the console's own time format" \
	the_board_says_when_a_row_moved
