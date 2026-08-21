# shellcheck shell=bash
#
# THE TRANSCRIPT SAYS A MESSAGE HAS REPLIES, HOW MANY, AND OPENS THE THREAD.
#
# The reading half of the operator's thread complaint - "impossible to track
# things here". A thread that can be answered but is invisible in the room is
# still a conversation nobody can follow, which is what #general was: 40
# messages, 40 threads, none of them longer than one.
#
# The number is the node's, over the whole log, and the fixture is built to
# catch the fold that would look right in every ordinary case: the console holds
# a sixty-message window, so the check pushes the start of the thread out of it
# and asserts the count is still four. A count taken from the screen would say
# two, on a page where nothing looks wrong.
#
# THIS FILE IS THE FIRST OF ITS KIND. See the loader in run-tests.sh for why a
# console check lives in its own file instead of on a shared append line.

the_transcript_counts_a_thread() {
	cd "$ROOT/web" || return 1
	node scripts/thread-count-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the transcript says how many replies a thread has, from the node's count" \
	the_transcript_counts_a_thread
