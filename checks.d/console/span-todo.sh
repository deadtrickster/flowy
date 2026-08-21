# shellcheck shell=bash
#
# A MESSAGE, OR THE PART OF IT SOMEBODY SELECTED, BECOMES A ROW.
#
# The operator, 01M0HGVPFN: "I should be able to quickly turn a message or a
# selected part into a rodo". The panel could already raise a row out of the
# selected MESSAGE; what it could not do is carry the WORDS, and a row titled
# after a message id sends its reader back to the conversation.
#
# A BROWSER CHECK because the selection is the feature. The control reads the
# browser's selection at the moment it is pressed and holds no state - storing
# it would re-render the message and destroy the reader's highlight under their
# pointer - so there is nothing to test without a real selection in a real
# document. The claim is settled against the NODE: the row carries the words,
# and it carries the message they came out of.

a_selected_span_becomes_a_todo() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/span-todo-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT" general
}

check "a message, or the part of it somebody selected, becomes a row" \
	a_selected_span_becomes_a_todo
