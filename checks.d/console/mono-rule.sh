# shellcheck shell=bash
#
# MONO MEANS A MACHINE STRING YOU COULD COPY, AND NOTHING ELSE.
#
# The operator, on a screenshot of a room: "check font langage we use". One
# message row alternated the two faces four times and the split followed no
# single axis - mono carried identity, machine strings AND inline code, while
# sans carried prose and some of the actions. A face that appears on everything
# is information about nothing.
#
# IT ASSERTS A DIFFERENCE, not an absolute. "The id is mono" passes on a page
# where every glyph is mono, which is the defect itself, so the check reads the
# control and the identifier and requires the two to disagree. It also compares
# the RAIL's id against the ROOM's by value, because the original complaint was
# that the same category changed face between panes.

one_face_means_one_thing() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/mono-rule-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" general
}

check "mono means a machine string, and a verb you press is not one" \
	one_face_means_one_thing
