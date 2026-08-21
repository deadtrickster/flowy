# shellcheck shell=bash
#
# BOTH COPIES OF THE MERGES PANE SAY WHERE THE TIP CAME FROM, AND AGREE WITH THE
# NODE.
#
# 01M0JZ5VM8. routes/ChatRoom.tsx passed tipFrom="deployed" as a literal while
# the response it already had carried tip_from; routes/Todos.tsx read the real
# value. One component, one endpoint, two claims about the same fact.
#
# The caveat that hangs off it - "the commit this node was built from, not a
# live read of the target" - is worth saying when it is true. This node answers
# "landed", meaning the last sha through the merge door, which is FRESHER than
# the build. Printing the hedge over those verdicts teaches a reader to discount
# a measurement that was correct, and a hedge in the wrong place costs trust in
# the ones that mean something.
#
# The union was short too: five declarations of "stated" | "deployed" | "none"
# against four values in api_mergequeue.go, missing the one the node actually
# sends. Nothing crashed, because the only test anywhere is `=== "deployed"` -
# the type was simply not true, which is a check switched off.
#
# Seeds its own room, so what it asserts does not depend on what anybody else
# filed. Asserts agreement in two directions - each pane against the answer the
# node gave THAT pane - so it keeps working every time something lands.

both_panes_agree_with_the_node_about_the_tip() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/tipfrom-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "both merges panes say where the tip came from" \
	both_panes_agree_with_the_node_about_the_tip
