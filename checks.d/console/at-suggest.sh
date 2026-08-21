# shellcheck shell=bash
#
# TYPING @ OFFERS THE NAMES THE NODE WOULD RESOLVE.
#
# The operator, 01M0GGSMBD: "no suggestions when I type @". More than a
# convenience here, and the row says why: a mention only becomes a mention if
# the name RESOLVES AT WRITE TIME. mentions.go parses the body when the message
# is said and records the resolved pairs in meta.mentions; a name that resolves
# to nobody is drawn as prose and addresses no one. So a typo is not a cosmetic
# miss - it is a message that looks addressed to its author and reaches nobody.
#
# THE ARM THIS EXISTS FOR IS THE ENTER KEY, and it is a browser check for that
# reason rather than out of habit. This composer SENDS on Enter. A suggestion
# list that let Enter through would send the half-typed name the instant
# somebody tried to accept one - shipping precisely the defect the feature was
# built to prevent. That cannot be seen in the source; it is an interaction
# between two handlers and only a keypress settles it.
#
# It asserts the send by COUNTING THE ROOM, not by reading the DOM: a send is a
# row on the node, so the check never has to trust the page about whether one
# happened.

an_at_offers_names_and_enter_takes_one() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/at-suggest-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" general
}

check "typing @ offers names, and Enter takes one instead of sending" \
	an_at_offers_names_and_enter_takes_one
