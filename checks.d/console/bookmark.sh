# shellcheck shell=bash
#
# KEEPING A MESSAGE PUTS IT ON A PAGE ONLY YOU CAN SEE.
#
# 01M0HGTV9B: "I should be able to bookmark messages". The room had pins, which
# answer a different question - a pin is the room saying "this is what we
# decided" and it changes everybody's strip. Somebody who wants to find their
# own way back to a message tomorrow had nothing.
#
# The round trip is the claim: keep it in a room, find it on /bookmarks with the
# room it was said in, drop it there, and see the room's own control agree. A
# list that filled and never emptied would pass anything shorter. The privacy is
# asked over the wire as a second token, because that is the half the page that
# owns the list cannot answer about itself - the store test asks the same
# question in-process, and a rule true only inside the process is untested.

keeping_a_message_is_private_and_reversible() {
	recall
	cd "$ROOT/web" || return 1
	# A's AGENT says it, because A and B are in different projects and a message
	# said by B lands in pb's #general where A never sees it. B is still here,
	# as the stranger whose own list must not hold the bookmark.
	node scripts/bookmark-check.mjs "http://127.0.0.1:$HTTP_PORT" \
		"$TOKEN_A" "$TOKEN_A_AGENT" "$TOKEN_B" general
}

check "keeping a message puts it on a page only you can see, and dropping it takes it off" \
	keeping_a_message_is_private_and_reversible
