# shellcheck shell=bash
#
# THE RAIL SAYS THE PROJECT ONCE, AND ITS LABELS SHARE A LEFT EDGE.
#
# The operator screenshotted this corner twice, six hours apart, the second time
# with "still see this": the app name, the project line and the picker, all
# reading "flowy" down three rows. claude-host traced it to eac0319 - the
# sidebar work added the line and the picker under an app name already there.
#
# The count is SCOPED TO THE PROJECT CONTROL, not to the rail. The app title
# says the product's name, and on this node the product and the project share a
# string; asserting "flowy appears once" would fail a correct rail for any other
# project and pass a broken one with a unique name. The claim is about the
# control.
#
# The alignment is measured off the RENDERED TEXT with a Range. A check reading
# class names would pass on a rail whose labels were displaced by anything other
# than the icon box - and the defect the operator saw was 4px of ragged left
# edge, which the eye reads as breakage before it reads as hierarchy.

the_rail_says_the_project_once() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/rail-says-it-once-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the rail names its project once, and its labels share a left edge" \
	the_rail_says_the_project_once
