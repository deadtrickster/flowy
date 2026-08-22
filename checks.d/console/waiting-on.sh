# shellcheck shell=bash
#
# A ROW WAITING ON SOMEBODY LOOKS DIFFERENT FROM A ROW NOBODY HAS TOUCHED.
#
# The field landed in 088bef0 and the verbs in dc90107, and the console drew
# neither: a row blocked on an answer and a row nobody has picked up rendered
# identically, which is one appearance for two states and the thing 01M0K4MENH
# was raised on.
#
# WHO THIS SURFACE IS FOR. Every agent has the nag and the verbs. The person who
# is OWED answers reads the board in a browser, and until this they were told
# nothing there - the half of "answers owed" no agent can act on for them.
#
# ASSERTED AS A DIFFERENCE, twice. Two rows seeded in one room, alike but for
# the pointer, and the panel has to draw them apart; then the control writes and
# the NODE is asked what it holds. A check that only read the chip would pass on
# a console that renders its own optimism, and one that only read the node would
# pass on a console that draws nothing.
#
# AND THE ASSIGNEE IS ASSERTED UNCHANGED, which is the arm that matters most: a
# control that quietly reassigned would put back exactly the confusion the field
# exists to end, and it would look like success.
#
# IN ITS OWN ROOM, per 01M0JR5K0D - it counts rows in a panel, so #general's
# traffic would be measuring the suite as much as the feature.

a_row_says_whose_move_it_is() {
	cd "$ROOT/web" || return 1
	node scripts/waiting-on-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" waitingroom "$HANDLE_A"
}

check "the panel says whose move it is, and asking from it does not move the carrier" \
	a_row_says_whose_move_it_is
