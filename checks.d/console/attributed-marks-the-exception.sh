# shellcheck shell=bash
#
# THE ORDINARY MESSAGE CARRIES NO AUTHORSHIP WORD.
#
# 01M1PA01MXCAXF8Q0N27B68E6F. Every message on this node is "attributed" -
# principals have no keys yet - so the room drew that word on every row,
# including beside the operator's own handle. They asked what it meant, from a
# screenshot of their own console, and then answered the row: "yes, draw nothing
# for common case". A word on everything carries no information and teaches a
# reader past the one case where it does.
#
# THE ABSENCE IS THE ASSERTION, which is why the room needs messages in it
# before this can say anything - an empty room would satisfy "no row says
# attributed" while measuring nothing. The check refuses on zero rows for that
# reason, and this seeds two so the refusal is never the thing that passes.
#
# THE EXCEPTION LIVES IN disowned-check, deliberately: on a disowned message the
# word is not a constant - authored-and-disowned is a stolen key, attributed-and
# -disowned is a forgery, and that check already asserts both readings survive.
# Seeding a disowned message here too would be two checks to keep in step.

the_ordinary_message_carries_no_authorship_word() {
	recall
	local room=attrword
	want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
		'{"body": "an ordinary message in an ordinary room"}' >/dev/null || return 1
	want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
		'{"body": "and a second, so the room is not one row"}' >/dev/null || return 1

	cd "$ROOT/web" || return 1
	node scripts/attributed-marks-the-exception-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$room"
}

check "an ordinary message draws no authorship word, because every message has the same one" \
	the_ordinary_message_carries_no_authorship_word
