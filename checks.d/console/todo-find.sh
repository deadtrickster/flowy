# shellcheck shell=bash
#
# THE TODO PANEL CAN BE ASKED WHETHER A ROW ALREADY EXISTS.
#
# The operator, 06:47: "might be duplicates because of your stulls and the fact
# that it is impossible to filter by author or seaech on the todo pane of room".
#
# The consequence arrived within the hour and it is the reason this is a check
# and not a preference: two agents filed the SAME ROW seconds apart that morning
# - 01M0HPQASA and 01M0HPPY7G, one closed as the other's duplicate - each having
# decided independently that nothing covered it. 32 open rows in a list with no
# search. Neither could have found out.
#
# A BROWSER CHECK, because what is being asserted is what a person can find out
# by typing, and the failure it guards is a panel that answers three different
# questions with the same empty list. It seeds through the API and then reads
# the PANEL, so what passes is the panel reading a queue it did not create.
#
# The people arm is seeded with a raiser that appears in no title, which is what
# makes it unforgeable: a box that quietly matched titles for "@name" too would
# hide both rows and fail before it got there.
#
# IN ITS OWN ROOM. 01M0JR5K0D: this check used to run against #general, where
# what it reads is whatever the rest of the suite happened to raise, so it was
# measuring the room as much as the thing it names. A private room makes the
# filtered and the full suite ask the same question - which is the property
# that was missing, and how one of its siblings came to flip in BOTH directions
# across six runs while a sidebar control was being added.

the_todo_panel_can_be_searched() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/todo-find-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" todofindroom
}

check "the todo panel searches by word and by person, and says what it withholds" \
	the_todo_panel_can_be_searched
