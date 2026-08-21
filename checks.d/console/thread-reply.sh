# shellcheck shell=bash
#
# "REPLY IN THREAD" SAYS WHERE THE WORDS GO.
#
# The operator, 01M0HCXXJB: "like we have now 'reply' this is cited reply in the
# room. and then 'reply in thread' is well in thread". Until this change three
# controls - reply, thread <id>, N replies - all called onSelect, so they did
# one thing and none of them said which.
#
# THE ASSERTION IS THE CARET, and it is here because the first cut failed it.
# requestAnimationFrame after setPane focused nothing - the pane is a route, so
# the composer does not exist for however many frames the navigation takes.
# Measured in a browser, then replaced with an effect. A check that only asserted
# "the pane opened" would have passed on the broken version, and the reader would
# have typed their reply into the room.
#
# IN ITS OWN ROOM. 01M0JR5K0D: this check used to run against #general, where
# what it reads is whatever the rest of the suite happened to raise, so it was
# measuring the room as much as the thing it names. A private room makes the
# filtered and the full suite ask the same question - which is the property
# that was missing, and how one of its siblings came to flip in BOTH directions
# across six runs while a sidebar control was being added.

reply_in_thread_puts_the_caret_in_the_thread() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/thread-reply-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" threadreplyroom
}

check "reply in thread opens the thread and puts the caret in its composer" \
	reply_in_thread_puts_the_caret_in_the_thread
