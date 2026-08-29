# shellcheck shell=bash
#
# WHO SPOKE, WITHOUT READING.
#
# UI row 01M173AT9V2PYK7XHG4GZDD1MJ, item 4: "the two speakers look identical -
# ours are the same rectangle with a different word in a pill". In a room where
# four agents and one person talk past each other, telling one from the other
# cost a read of a five-character word.
#
# IT ASSERTS A DIFFERENCE BETWEEN TWO ROWS ON ONE PAGE. "A person's message has
# a background" passes on a page where every message has the same background,
# and that page is the defect - so one message is posted as a person and one as
# an agent and their resolved styles must disagree.
#
# It also asserts the pill still says which in words. Shape is the fast channel
# and words are the exact one; swapping one for the other is a regression
# wearing a redesign's clothes.

a_person_and_an_agent_are_not_one_rectangle() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/who-spoke-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT" general
}

check "a person's turn and an agent's turn are not the same rectangle" \
	a_person_and_an_agent_are_not_one_rectangle
