# shellcheck shell=bash
#
# A PICTURE IN A MESSAGE BODY DRAWS INLINE.
#
# lib/markdown documents `![what it shows](01M0...)` as the way a body refers
# to a file it carries, and ArtifactView and RowNotes draw it. The room did
# not: MessageBody rendered chat without the resolver, so a message written
# exactly as documented drew <img src="01M0..."> - a broken glyph. The
# operator, 2026-09-23, on a snake sent that way: "ha you replying with
# something that renders a broken image tag".
#
# Two readers, because the sentence for a file the reader cannot reach is the
# other half: the operator opens the same room and is told, in words, that a
# picture A wrote into pc cannot be shown - not a broken image, not silence.
# The operator rather than B, because B lives in pb and never sees A's room.
#
# Its own room: it posts two messages and two attachments.

a_picture_in_a_message_draws_inline() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/chat-picture-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_PC" "$TOKEN_OP" chatpicture
}

check "a picture referenced in a chat message body draws inline, and one the reader cannot reach says so" \
	a_picture_in_a_message_draws_inline
