# shellcheck shell=bash
#
# THE THREAD PANE PUTS BACK THE THREAD YOU LEFT IT ON.
#
# @deadtrickster, 01M0JPYYDZ: "visiting other panel and returned to room resets
# the thread panel. thread panel should restore to the thread I was at when
# leaving room panel".
#
# Switching panes already worked - a pane is a route and ChatRoom stays mounted.
# Leaving the ROOM unmounts it, so `opened` went with the component and the path
# the sidebar returns to names no message.
#
# TWO ROOMS, because the fix is per-room and the opposite failure is worse: a
# thread remembered globally follows the reader into a room where no event
# matches it and draws an empty pane that reads as broken. The negative arm is
# the load-bearing one - "it remembers" is satisfiable by a pane that shows the
# same thread everywhere.
the_thread_pane_puts_back_what_you_left() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/thread-remembers-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT"
}

check "the thread pane puts back the thread you left it on" \
	the_thread_pane_puts_back_what_you_left
