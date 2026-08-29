# shellcheck shell=bash
#
# A LINE OF PROSE IS A LINE SOMEBODY CAN READ.
#
# Measured on the live room before the cap existed: 118 characters per line at a
# leading of 1.43. The comfortable measure is 45 to 75. That is what made a room
# of ordinary paragraphs read as a wall, and no amount of heading hierarchy or
# markdown rhythm fixes a line twice as long as a line should be.
#
# THE CHECK MEASURES DRAWN TEXT, never a class. `max-width: 72ch` in a
# stylesheet is a fact about a file: a class check passes on a rule that is
# overridden later, on a container that never receives it, and on an empty body.
# This reads the width of a real paragraph and divides by the real glyph advance
# of the font it is set in.
#
# Both bounds are asserted, because a rule that capped at 20ch would satisfy
# "not too wide" and be worse than what it replaced.

prose_is_readable_at_a_glance() {
	recall
	local room=measure
	want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
		'{"body": "A paragraph long enough to wrap several times, so the width of a drawn line can be measured rather than inferred from a stylesheet that may or may not apply to the element a person is reading on this page."}' >/dev/null || return 1

	cd "$ROOT/web" || return 1
	node scripts/measure-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$room"
}

check "a line of prose is inside the comfortable measure, at both widths" \
	prose_is_readable_at_a_glance
