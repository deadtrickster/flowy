# shellcheck shell=bash
#
# A THREAD MESSAGE SHOWS WHAT A ROOM MESSAGE SHOWS.
#
# The operator, 01M0HP4N06: "messages in threads dont show attachements". The
# cause was bigger than attachments - the thread pane is a SECOND renderer of
# the same events and its whole message was one span of event.body, so it had no
# markdown, no mentions, no citation and no cards. Every feature the room grew
# since that pane was written had to be added to it by hand, and none had been.
#
# It asserts the PAIR rather than the pane: the same message, drawn in the room
# and drawn in the thread, and what the room shows the thread must show. A check
# that looked only at the pane would pass on a console where the room had lost
# the feature too, and would need rewriting every time a fifth thing is added to
# a message.

a_thread_message_shows_what_a_room_message_shows() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/thread-renders-check.mjs "http://127.0.0.1:$HTTP_PORT" \
		"$TOKEN_A" "$TOKEN_A_AGENT" general
}

check "a thread message shows what a room message shows" \
	a_thread_message_shows_what_a_room_message_shows
