# shellcheck shell=bash
#
# A RUN PAINTS ONE FRAME, NOT A STACK OF CARDS.
#
# The operator, 2026-09-04, with a screenshot: "merging messages works, but
# round borders do not merge". Row 01M1P8W4HR3V6N4VVN97AZW80V.
#
# Both halves of that sentence are exact, which is why it took a screenshot to
# say it. The HEADER merges - message-runs.sh counts that a run says who is
# speaking once - and the FRAME did not, so three lines from one seat read as
# three cards that had lost their headers rather than as one block.
#
# WHY THE CODE COULD NOT HAVE BEEN RIGHT: MessageList computed `runs` from the
# row BEFORE it and nothing else, so a row could know it CONTINUED a run and
# never that it was the LAST of one. Two of the four cases had no expression,
# and both of the visible faults were those two - a rounded bottom on the opener
# meeting a square continuation, and a square bottom where the run ended.
#
# FOUR ROWS, FOUR ANSWERS. A run of three plus a lone message produces every
# case the component has, and the lone one is the arm that matters most: a fix
# that squared every corner in the room satisfies "the run merges" perfectly and
# is worse than what it replaced.
#
# IN ITS OWN ROOM, so the filtered and the full suite ask the same question
# rather than measuring whatever else the suite happened to say in #general.

a_run_paints_one_frame() {
	recall
	local room=runframe
	local i
	# Three from one seat - an opener, a middle and a closer - then one from
	# another, which is the lone row.
	for i in 1 2 3; do
		want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
			"{\"body\": \"one of a run $i\"}" >/dev/null || return 1
	done
	want_status 200 POST "$TOKEN_A" "/api/chat/$room/say" \
		'{"body": "and somebody else, alone"}' >/dev/null || return 1

	cd "$ROOT/web" || return 1
	node scripts/run-frame-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$room"
}

check "a run of one speaker paints one frame, and a lone message keeps its own" \
	a_run_paints_one_frame
