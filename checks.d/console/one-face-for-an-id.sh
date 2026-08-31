# shellcheck shell=bash
#
# ONE MESSAGE ROW, ONE FACE FOR AN ID.
#
# The operator, on a screenshot of the room: "check font langage we use".
# Measured as computed font-family inside a single [data-message]:
#
#   SPAN[data-msg-id]                the message's own id    ui-monospace
#   SPAN < BUTTON[data-thread-open]  its thread id           ui-monospace
#   A < P < DIV[data-body]           a row id in the prose   ui-sans-serif
#
# Three identifiers on one row, drawn two ways - and the odd one out is the one
# a person pastes by hand.
#
# THE FACE AND NOT THE CLASS: reading `font-mono` passes on a build where the
# class is present and the face is not.
#
# AND AGAINST THE PROSE BESIDE IT, because agreement alone is satisfied by a
# console that draws everything in one face - a different complaint, and one
# this fleet has had. The two assertions together say ids agree AND are
# distinguishable from a sentence.
#
# IT SEEDS ITS OWN ROOM so it can be armed on its own: a message with a real row
# id pasted into a sentence long enough to be measured as prose beside it.

a_row_id_has_one_face() {
	recall
	local row
	api POST "$TOKEN_A" /api/artifacts \
		'{"type": "memory", "kind": "todo",
		  "title": "one face for an id", "body": "seeded by the font check"}' ||
		return 1
	# ASSERTED, because the door REFUSES an unknown field and `api` does not
	# fail on a 4xx. An earlier version sent "scope" where this door takes
	# "visibility", and the refusal arrived four steps later wearing a
	# rendering complaint: "the row id null is drawn nowhere".
	want_eq "the seeded row was created" "$API_STATUS" 200 || return 1
	row="$(jqv .id)"
	if [ -z "$row" ] || [ "$row" = null ]; then
		printf 'the create answered 200 with no id: %s\n' "$API_BODY" >&2
		return 1
	fi
	api POST "$TOKEN_A" /api/chat/faces/say \
		"$(jq -nc --arg b "the row is $row and this sentence is here so there is prose to measure the id against" \
			'{body: $b}')" || return 1
	want_eq "the seeded message was said" "$API_STATUS" 200 || return 1
	cd "$ROOT/web" || return 1
	node scripts/one-face-for-an-id-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" faces "$row"
}

check "a row id pasted in a room is drawn in the same face as the ids around it" \
	a_row_id_has_one_face
