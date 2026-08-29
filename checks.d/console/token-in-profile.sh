# shellcheck shell=bash
#
# THE PASTE-A-TOKEN BOX IS ON /profile - AND THE WAY OUT STAYS IN THE RAIL.
#
# It sat at the foot of the left rail on every page with the bearer token
# visible in an input. The operator: "yeah move it in profile. I dont use it and
# it wastes time."
#
# THE FIRST VERSION OF THIS CHECK ASSERTED THE WRONG THING - that the whole bar
# was gone from the rail. The bar also holds the LOG-IN LINK and the LOG-OUT
# BUTTON, so the move passed this check and took the way in and the way out off
# every page of the console. Three other checks failed and said so; this one was
# green. TokenBar's own note had already written the rule: "being unable to
# LEAVE is the same defect one step later".
#
# So it asserts what MOVED and what STAYED, in both places, on several routes -
# the rail is the shell's, so one page proves nothing - and that the box still
# works where it landed. A control moved and quietly broken is worse than one
# left where it was.

the_token_bar_moved_to_profile() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/token-in-profile-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the paste-a-token box moved to profile, the way out stayed, and both still work" \
	the_token_bar_moved_to_profile
