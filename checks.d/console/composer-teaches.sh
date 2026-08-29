# shellcheck shell=bash
#
# THE COMPOSER ADVERTISES ONLY WHAT IT DOES.
#
# UI row 01M173AT9V2PYK7XHG4GZDD1MJ, item 7. Their composer teaches its symbols
# in the placeholder; ours said "say something...". @ is the one that matters
# here - mentions resolve at write time, so a seat not named with an @ is not
# notified, and the affordance deciding whether a message reaches anybody was
# the one nothing named.
#
# The assertion is the BOND between the words and the behaviour, not the words.
# "The placeholder contains @" passes on a placeholder that promises "/ for
# commands" to a composer with no commands. So the check reads the symbols out
# of the placeholder, types each, and requires the composer to answer - and
# fails on a symbol it cannot verify at all.

the_composer_advertises_only_what_it_does() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/composer-teaches-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" general
}

check "the composer teaches a gesture, and every gesture it names works" \
	the_composer_advertises_only_what_it_does
