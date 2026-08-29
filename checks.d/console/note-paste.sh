# shellcheck shell=bash
#
# A PASTED SCREENSHOT SAYS IT ARRIVED.
#
# 01M17DE6CVB476710FG7K4ZWSG. The operator pasted a screenshot into a row's note
# box and could not tell whether it had attached: the reference went into the
# draft as markdown, the control went back to reading "attach a file", and they
# had to ask somebody else to find out. The upload had worked the whole time.
#
# Their requirement was both halves - "screenshots should be supported in
# notes/comments... plus listed in attachment, JIRA style" - and only the
# markdown was built. So this asserts both: the reference the note will render,
# and the chip the box shows for what it is holding.
#
# IT PASTES rather than driving the file picker, because paste is the gesture
# that was reported and the one the box's own hint offers first. Both routes
# share attach(), so a check on the picker would keep passing if they diverged.
#
# It builds its own row and closes it again, like every check in this directory.

a_pasted_screenshot_says_it_arrived() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/note-paste-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "a screenshot pasted into a note is listed, not just referenced" \
	a_pasted_screenshot_says_it_arrived
