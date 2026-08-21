# shellcheck shell=bash
#
# AN IMAGE IN THE ROOM CAN BE SEEN WHOLE.
#
# The operator, one minute after we agreed screenshots belong in the room rather
# than in a terminal only one seat reads: "hmm when i tap on the image it stays
# small preview". It was capped at 256 pixels, the image was not clickable, and
# the attachment door answers JSON rather than bytes so there was no link to
# open either. A console screenshot at 256px does not contain the thing it was
# taken to show, which made the agreement worthless.
#
# THE ASSERTION IS THE RENDERED SIZE, not the presence of an overlay: a lightbox
# drawing the same 256 pixels would pass any check that looked for the element.
# Negative-controlled by capping the overlay at max-h-64, which reports
# "256px against a 256px preview - that is not seeing it whole".
#
# It builds its own 900x600 PNG rather than fetching one, so the dimensions are
# known and the check does not depend on a fixture somebody else maintains.

an_image_in_the_room_can_be_seen_whole() {
	cd "$ROOT/web" || return 1
	# A ROOM OF ITS OWN, not general: this check posts a message and an
	# attachment, and #general is counted by other checks. That is the fixture
	# collision that reddened the DM row tonight - a counting fixture does not
	# share a room any more than it shares a word.
	node scripts/attachment-whole-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" attachwhole
}

check "an image opens from its preview to full size, and Escape closes it" \
	an_image_in_the_room_can_be_seen_whole
