# shellcheck shell=bash
#
# THE LAYER A PROJECT DECLARES IS EDITABLE FROM THE CONSOLE.
#
# 01M0G8AM6R2BGPCWZQMV6321DR: "i should be able to manage them from the flowy
# ui... also must be available for agent to edit".
#
# THE ASSERTION IS THE ROUND TRIP. A textarea that renders, takes typing and
# posts nothing is indistinguishable from one that works - including to the
# page, which is holding the text it just typed - until a VM boots without the
# dependency. So the text is read back from the NODE after the save, in a
# separate request, and that is the value asserted.
#
# It drives the same door with both tokens first: these are operator-only, and
# a check that only ever plays the operator cannot tell a guard that works from
# one that is not there.

the_layer_is_editable_from_the_console() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/layer-editor-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" "$TOKEN_A"
}

check "a project's declared layer is editable in the console, and the node keeps it" \
	the_layer_is_editable_from_the_console
