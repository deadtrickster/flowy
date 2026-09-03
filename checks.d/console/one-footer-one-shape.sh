# shellcheck shell=bash
#
# ONE MESSAGE FOOTER, ONE SHAPE.
#
# UI row 01M173AT9V2PYK7XHG4GZDD1MJ item 1: "every message carries eight
# controls, all the same weight". The half about the MESSAGE is fixed - the
# machinery is 11px muted against 14px body. The half about each OTHER was not,
# and it survived because the first half looked like the whole of it.
#
# Measured on the deployed console, computed styles rather than a screenshot:
#
#   cite / todo / keep / thread <id>   borderless prose, underline on hover
#   row <id> / N replies               bordered chip, resting opacity 1
#   reply / reply in thread            bordered chip, resting opacity 0.6
#
# `reply` whispered and `cite` did not, and both are verbs you press. The verbs
# being borderless is the row above's complaint in a new costume: the FACE
# stopped saying "identifier" and the SHAPE went on saying "prose".
#
# THE ASSERTION IS A COUNT OF DISTINCT SHAPES, not a look. And the number of
# controls is asserted FIRST, because a footer that drew nothing agrees with
# itself perfectly - the same wrong answer shaped like a right one as a badge
# that appears on every row.
#
# IT SEEDS ITS OWN ROOM so it can be armed alone and cannot pass or fail on
# whatever other checks left in #general.

a_message_footer_has_one_shape() {
	recall
	api POST "$TOKEN_A" /api/chat/footer/say \
		'{"body": "a message with enough words in it that its footer draws every control this check counts"}' ||
		return 1
	# ASSERTED, not merely sent: `api` does not fail on a 4xx, so a refused
	# seed would arrive later wearing a rendering complaint.
	want_eq "the seeded message was said" "$API_STATUS" 200 || return 1
	cd "$ROOT/web" || return 1
	node scripts/one-footer-one-shape-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" footer
}

check "every control in a message footer is drawn the same way" \
	a_message_footer_has_one_shape
