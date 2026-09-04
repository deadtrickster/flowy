# shellcheck shell=bash
#
# THE NAV SEPARATOR KEEPS A TOUCH GESTURE, AND THE COLUMN CAN BE PUT AWAY.
#
# The operator, from a Fold 8: "i try to drag the separator and it does follow
# for some pixels and then stops, so i have to do it multiple times... also
# please make it collapsible".
#
# NOT POINTER CAPTURE, which is what the handle already had and why it read as
# correct. With the default touch-action a touch browser spends the opening
# pixels of a gesture deciding whether it is a scroll, and when it decides yes
# it takes the gesture and fires pointercancel - which the handle's own
# onPointerCancel turns into "stop dragging". Capture cannot help: it routes
# events this element would receive and the browser has stopped sending any.
#
# A MOUSE NEVER SAW IT. There is no scroll to lose the gesture to, so every
# desktop drag worked and the defect lived exactly where nobody was testing.
# That is why both arms here run with hasTouch.
#
# THE ARM IS THE SWITCH, NOT A MIMED FLING. Whether a browser steals a gesture
# is its own scroll heuristic against real touch input, and a CDP-dispatched
# sequence does not reproduce that arbitration - a check that mimed a drag and
# passed would say nothing about the device the report came from. So what is
# asserted is the computed touch-action of the element the finger lands on,
# which cannot be "none" while the default is in force.

the_separator_keeps_the_gesture() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/separator-keeps-the-gesture-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "the nav separator keeps a touch gesture, and the column collapses and comes back" \
	the_separator_keeps_the_gesture
