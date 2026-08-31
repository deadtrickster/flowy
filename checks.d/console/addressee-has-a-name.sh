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
# IT SEEDS ITS OWN ROOM. an_addressee_is_named seeds #naming with the same two
# messages, and leaning on that was measured wrong on the first run: under a
# filter the seeder does not execute, the room is empty, and this check refused
# with "a fixture that did not arrive" - correctly, and having measured nothing.
# A check that only works in a full run is a check nobody can arm on its own.
#
# BOTH WAYS OF ADDRESSING SOMEBODY, because before the fix the @ form carried a
# name on the wire and the --to form did not, so one message would have proved
# the wrong half.

the_room_names_who_a_message_is_for() {
	recall
	api POST "$TOKEN_A" /api/chat/naming/say \
		"$(jq -nc --arg t "$USER_B" --arg b "addressed with --to and no mention in the body" \
			'{to: $t, body: $b}')" || return 1
	api POST "$TOKEN_A" /api/chat/naming/say \
		"$(jq -nc --arg b "@$HANDLE_B addressed by naming them in the sentence" '{body: $b}')" ||
		return 1
	cd "$ROOT/web" || return 1
	node scripts/addressee-has-a-name-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$HANDLE_B"
}

check "the room says who a message is for by name, not by a slice of an id" \
	the_room_names_who_a_message_is_for
