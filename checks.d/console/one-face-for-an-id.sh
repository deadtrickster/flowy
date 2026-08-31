# shellcheck shell=bash
#
# A ROW ID LOOKS THE SAME WHEREVER IT IS DRAWN.
#
# The operator, on a screenshot of the room: "check font langage we use". One
# category was drawn two ways depending on the pane - the board draws a row id
# in font-mono, and the room drew the same id in the body face, both as the chip
# beside a message and as a link inside the prose.
#
# MEASURED AS COMPUTED FONT-FAMILY, not as a class name: a check reading
# `font-mono` passes on a build where the class is present and the face is not,
# and the rule was never about the class.
#
# ASSERTED AS AGREEMENT ACROSS TWO SURFACES, not as "the room draws mono".
# "This element is monospace" is also true of a console that draws EVERYTHING in
# monospace, which is a different complaint and one this fleet has actually had.
# So the room and the board are both loaded, the same id is found on each, and
# the faces must be one - AND must differ from the prose beside them, or the
# distinction does no work at all.
#
# IT SEEDS ITS OWN ROW AND ITS OWN MESSAGE, so it can be armed on its own: a
# check that only runs inside a full suite is a check nobody red-proves.

a_row_id_has_one_face() {
	recall
	local row
	api POST "$TOKEN_A" /api/artifacts \
		'{"type": "bug", "title": "one face for an id", "body": "seeded by the font check"}' ||
		return 1
	row="$(jqv .id)"
	# In the room as PROSE - a pasted id the renderer linkifies - and the same
	# id on the board. The chip beside a message is drawn from the row a
	# message raised, which this does not need: prose and board are the two
	# surfaces the complaint was about.
	api POST "$TOKEN_A" /api/chat/faces/say \
		"$(jq -nc --arg b "the row is $row and this sentence is here to be measured beside it" \
			'{body: $b}')" || return 1
	cd "$ROOT/web" || return 1
	node scripts/one-face-for-an-id-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" faces "$row"
}

check "a row id is drawn in one face in the room and on the board, and not the face of the prose" \
	a_row_id_has_one_face
