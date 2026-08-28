# shellcheck shell=bash
#
# EVERY PAGE IS IN THE NAV, OR SOMEBODY DECIDED IT IS NOT.
#
# /vms shipped with a page, a panel and a shell relay and was not in the left
# rail. It was linked from the home page, so it was not unreachable - but every
# other page in this console is in the rail, so the rail is where a person
# looks. The operator asked for it after using the panel for an evening.
#
# The walk is a SOURCE walk with no browser, because "is it in the rail" is a
# fact about the whole app and a browser flow would have to guess where the link
# might be - and a page nobody put in the nav is exactly the page a guess does
# not visit. Same shape as paramguard and the advertised-route walk.
#
# The browser arm is the other half and asks the question a walk cannot: that
# the entry a person can see actually takes them there and the page draws.

every_page_is_in_the_rail() {
	cd "$ROOT/web" || return 1
	node scripts/route-reachable-check.mjs
}

the_rail_entry_opens_the_page() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/rail-vms-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP"
}

check "every page is in the nav, or the check says who decided it is not" \
	every_page_is_in_the_rail
check "the nav's shells entry opens the page, in a browser" \
	the_rail_entry_opens_the_page
