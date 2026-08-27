# shellcheck shell=bash
#
# A PASTED ROW ID IS A LINK, AND A NEARLY-RIGHT STRING IS NOT ONE.
#
# The operator, 2026-08-25 (row 01M0XDPFSA7M73): "filing ids are not links".
# Four agents refer to work by ULID, every message carries them, and none was
# clickable - so ids get copied into URL bars, or never followed at all, which
# is the usual outcome. The renderer links the strict ULID pattern to the
# resolver route /a/<ulid>, and the resolver 302s to the row's own page.
#
# THE CLICK GOES THROUGH THE CONSOLE for a token seat: a browser navigation
# carries no bearer, so the click handler resolves via GET /api/artifact/{id}
# and opens the row's page itself (web/src/lib/rowlink.ts). The resolver route
# serves cookie seats and is asserted directly by the third arm.
#
# THE PATTERN IS THE NEGATIVE ARM. The row's own words say what a generous one
# costs: "a pattern that is nearly right will turn arbitrary tokens into dead
# links, which is worse than no links at all". So a 26-character string that
# is not a valid row id - an I where no id has one, or a lowercase one - must
# stay text, and the arm proves both stay text rather than asserting the happy
# path twice.
#
# THE RESOLVER'S HONESTY IS THE THIRD ARM. An id that links but names no row
# - valid pattern, absent from the store - answers 404, not a redirect to
# nowhere, and a path that is not even shaped like an id answers the same.
# /a/<ulid> only ever answers 302 or 404.
#
# THE AUTHOR LINE IS THE FOURTH ARM (row 01M10Y4D): the row page names its
# author. The door's resolved name is the truth - a person's handle, an
# agent's person's handle or else their runtime kind - and the page draws
# "by <name>" exactly when the door has one, never the raw id dressed as a
# name, and nothing when the door could not name the owner.
#
# ONE TOKEN, one browser. There is deliberately no scope arm: the resolver
# runs the same permission filter as reading the row, so an out-of-scope id
# is indistinguishable from a missing one by design - there is no second
# state to prove.

a_pasted_row_id_is_a_link_and_a_nearly_right_string_is_not() {
	cd "$ROOT/web" || return 1
	node scripts/rowlink-check.mjs "http://127.0.0.1:$HTTP_PORT" \
		"$TOKEN_A" || return 1
}

check "a pasted row id links to its row, a nearly-right string stays text, the resolver 404s honestly, and the row page names its author" \
	a_pasted_row_id_is_a_link_and_a_nearly_right_string_is_not
