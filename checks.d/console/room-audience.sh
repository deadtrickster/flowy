# shellcheck shell=bash
#
# A ROOM SAYS ITS PROJECT, THE @ LIST OFFERS ONLY WHO CAN HEAR IT, AND A NAME
# THAT CANNOT IS SAID BEFORE THE SEND.
#
# The operator, 01M0X22ECZ4: "we should be clear who is in the rooms". Two
# projects both have a room called general, so the @ list fed from everywhere
# the caller can read offered people who are not in this one - and a mention
# RESOLVES at write time, so the message looked addressed while it reached
# nobody in the room.
#
# WHAT THE PAGE MUST DO, in the row's own order: the room says which project it
# belongs to (a page that names neither tells nobody which one it is), the
# roster splits "in this project" from "elsewhere on this node", and the @ list
# offers only names that can hear THIS room. The worth-deciding case - a name
# typed in full that cannot hear - is said at compose time, in the box, rather
# than refused: the row judged refusing the send wrong ("I mean @bob" is a thing
# a person can know and the node cannot), so the check also asserts that Enter
# stays a send, by COUNTING THE ROOM the way an_at_offers does - a send is a row
# on the node, and the page is never asked to swear to one.
#
# THE FIXTURE EARNS ITS NEGATIVES WITHOUT WRITING A GRANT. Alice (pa) and bob
# (pb) each speak in a room called audroom - one room name, two rooms, the
# failure the row describes. The browser reads as BOB, and bob can see alice
# because the suite's own grant checks have already opened pa to pb by the time
# console checks run. A check that earned its own grant leaves the grant table
# changed for every later permission check - measured, the hard way: a pa->pb
# grant of the check's own turned six later checks red. The check also deletes
# the console reader rows its shell load declares, checked, so the suite's own
# reader-row check keeps reading exactly one mark per principal.

a_room_says_its_project_and_offers_only_who_can_hear_it() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/room-audience-check.mjs "http://127.0.0.1:$HTTP_PORT" \
		"$TOKEN_A" "$TOKEN_B" "$USER_A" "$USER_B" "$PROJECT_A" "$PROJECT_B"
}

check "a room says its project, @ offers only names that can hear it, and an elsewhere name is said before the send" \
	a_room_says_its_project_and_offers_only_who_can_hear_it
