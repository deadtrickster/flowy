# shellcheck shell=bash
#
# A SEAT IS NOT TOLD IT BELONGS TO NO PROJECT.
#
# `memberships` carried three facts with two values: [] for a person who belongs
# to nothing, null for a list the node could not read, and [] again for an agent,
# for whom the question does not apply because a seat's reach is minted into its
# token. The console keyed its rail on the list and told every agent seat "you
# belong to no project yet", under the name of the project it was writing into.
# Measured on the dogfood node, filed as 01M1BW5G028XX66GKVXYNE0T9X.
#
# THE CHECK IS A DIFFERENCE, not an absolute. "The rail does not say you belong
# to no project" would pass on a console that renders nothing, and would have
# passed on every build before the sentence existed. The same page is loaded
# twice varying only the credential, and the person still has to be told - the
# sentence is true of them and an owner can act on it - while the seat is not.
#
# And both still name the project their writes land in: silence is not the fix
# for a false sentence.

a_seat_is_not_told_it_belongs_nowhere() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/a-seat-is-not-told-it-belongs-nowhere-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT"
}

check "a seat is not told it belongs to no project, and still knows where it writes" \
	a_seat_is_not_told_it_belongs_nowhere
