# shellcheck shell=bash
#
# THE ROOM SAYS WHO A MESSAGE IS FOR BY NAME.
#
# Measured on the live console: a message addressed to 01M05YCEFY6BQAR2WPMMXTYVG2
# rendered as "to MMXTYVG2", two inches from a speaker chip saying "claude-host".
# The same person named twice on one row, once as eight characters of a ULID.
#
# The page could not fix it alone - meta.mentions carries a name only when
# somebody was named with an @, and `flowy say --to NAME` addresses without
# writing one - so the door resolves it and this asserts what a reader is shown.
#
# The rendered text must not be a PREFIX OF THE ADDRESSEE'S ID, which is exactly
# what the old fallback drew, and the id must still be on the badge: a name is
# what you read, an id is what you paste into a command, and this trades neither
# for the other.
#
# It runs against #naming, the room an_addressee_is_named seeds with both ways
# of addressing somebody - by --to and by @ - because before the fix only the
# second carried a name.

the_room_names_who_a_message_is_for() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/addressee-has-a-name-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$HANDLE_B"
}

check "the room says who a message is for by name, not by a slice of an id" \
	the_room_names_who_a_message_is_for
