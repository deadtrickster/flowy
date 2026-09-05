# shellcheck shell=bash
#
# A CARD THAT IS STILL FETCHING SAYS SO.
#
# The operator, on five renders another seat had just posted: "all attachments
# \"not on this node\"". Nothing was wrong with them - 1.5MB and 425KB of
# image/png, on the node and readable with a token. The card said it WHILE
# FETCHING: content was null before the answer arrived, and null is also what
# the node sends when it holds no bytes, so a pending request and an empty
# answer were the same value.
#
# THE DELAY IS THE TEST. A small attachment fills before anybody could read the
# words, so an assertion written against a normal file passes on the bug as
# readily as on the fix. The check holds the attachment route open and asks the
# card what it says while the answer is in flight, which is the only moment the
# defect exists.
#
# Its own room, because it posts a message and an attachment, and a counting
# fixture does not share a room.

a_loading_card_says_it_is_loading() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/attachment-loading-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" attachloading
}

check "an attachment card says it is fetching, and reserves 'not on this node' for an empty answer" \
	a_loading_card_says_it_is_loading
