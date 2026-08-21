# shellcheck shell=bash
#
# OPENING A THREAD IS NOT QUOTING A MESSAGE.
#
# The operator, 01M0HGRFN5: clicking `thread ...` at the bottom of a message
# opens the thread pane "|ANd cites the message shouldnt cite". The thread
# controls called onSelect, which is what arms a citation, so reading a
# conversation and quoting the message it starts from were one gesture - the
# same collapse the operator had complained about for message clicks two days
# earlier, back in a new control because the control was wired to the handler
# that was already there.
#
# It asserts both halves plus a positive control: the pane shows the thread that
# was opened, nothing is quoted, and `cite` on the same message still quotes it.
# Without that last one the check passes against a console where citing is
# broken everywhere.

opening_a_thread_is_not_a_quote() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/thread-open-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT" general
}

check "opening a thread shows it and quotes nothing" \
	opening_a_thread_is_not_a_quote
