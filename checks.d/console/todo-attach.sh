# shellcheck shell=bash
#
# A TODO RAISED FROM A ROOM CARRIES THE FILE IT IS ABOUT.
#
# The operator, 01M0GGQ8D4: "no way to attach a file to todo from the chat
# todo". The message box beside the panel has taken files since attachments
# landed; the panel next to it took a sentence and nothing else, so work raised
# out of a screenshot pointed at a title and the evidence stayed in the
# transcript.
#
# A BROWSER CHECK because the upload is browser code - the ceiling, the chunked
# base64, the picker - and a unit test of the door would pass on a console that
# never sends the ids. It settles the claim against the NODE: it reads the row
# the raise minted and the message that announced it, so a card drawn in a panel
# that lost the file cannot pass this.

a_todo_raised_from_a_room_carries_its_file() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/todo-attach-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" general
}

check "a todo raised from a room carries the file it is about" \
	a_todo_raised_from_a_room_carries_its_file
