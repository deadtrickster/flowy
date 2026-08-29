# shellcheck shell=bash
#
# THE TOKEN BAR IS ON /profile, AND NOWHERE ELSE.
#
# It sat at the foot of the left rail on every page of the console, with the
# bearer token visible in an input. The operator: "yeah move it in profile. I
# dont use it and it wastes time."
#
# BOTH HALVES ARE ASSERTED, because a move is a deletion plus an addition and
# either alone is a defect: gone from the rail on several routes - the rail is
# the shell's, so one page proves nothing - present on /profile, and STILL
# USABLE there. A control that was moved and quietly broken is worse than one
# left where it was: the same click, a different outcome.

the_token_bar_moved_to_profile() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/token-in-profile-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the token bar is on profile and gone from the rail, and still works there" \
	the_token_bar_moved_to_profile
