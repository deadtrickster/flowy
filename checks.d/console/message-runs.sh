# shellcheck shell=bash
#
# A RUN OF ONE SPEAKER SAYS WHO IS SPEAKING ONCE.
#
# The room repeated the whole identity cluster - the agent/human badge, the
# name, the authorship mark, the private and addressed marks - on every message,
# so four consecutive lines from one seat said the same four things four times.
#
# COUNTED, NOT EYEBALLED: the check counts rows that OPEN a run against rows
# that CONTINUE one, and requires both to be non-zero. "Fewer headers than
# messages" alone would pass a surface that drew no headers at all, which is a
# room where nobody can tell who spoke - so the first row of the screen, which
# has nothing above it, must always open one.
#
# THE BREAK ON AUTHORSHIP IS ASSERTED IN GO, not here: "signed" and "attributed"
# are the mark that says whether this node verified a signature of the speaker's
# own, and a room cannot be made to produce both on demand. See
# TestARunBreaksWhereTheHeaderWouldHaveSaidSomethingNew.

a_run_of_one_speaker_says_it_once() {
	recall
	local room=msgruns
	local i
	# Four from one seat, then one from another: a run, and a break.
	for i in 1 2 3 4; do
		want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
			"{\"body\": \"one of a run $i\"}" >/dev/null || return 1
	done
	want_status 200 POST "$TOKEN_A" "/api/chat/$room/say" \
		'{"body": "and somebody else"}' >/dev/null || return 1

	cd "$ROOT/web" || return 1
	node scripts/message-runs-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$room"
}

check "a run of one speaker says who is speaking once, and a break says it again" \
	a_run_of_one_speaker_says_it_once
