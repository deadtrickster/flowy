# shellcheck shell=bash
#
# A MENTION OF YOU WEARS A RING, AND A MENTION OF SOMEBODY ELSE DOES NOT.
#
# The operator: "@operator is not highlighted in the chat". 01M0GGSM99 listed
# three candidates for it - the reader unknown in that render path, the node
# never resolving the name, or a ring too faint to see - and measuring them
# found a fourth: the ring works, the reader is known, and @operator was simply
# not a name this node answered to. Four seats wrote the word at the operator
# daily and every one of them was prose.
#
# BOTH ARMS ON ONE PAGE, because a console that rings EVERY chip passes a check
# that only looks for a ring, and is worse than one that rings none: a ring that
# means nothing is a signal a reader learns to ignore, and then misses the
# message that was for them.
#
# The role arm asserts the node's own resolution rather than the screen, since
# that is the half that was missing, and it skips rather than fails on a node
# whose roster reports no operator or several - a name nobody holds correctly
# resolves to nobody, and a name two people hold resolves to neither.

a_mention_of_the_reader_is_ringed() {
	cd "$ROOT/web" || return 1
	node scripts/mention-ring-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$HANDLE_B"
}

check "a mention of the reader is ringed, one of somebody else is not, and @operator resolves" \
	a_mention_of_the_reader_is_ringed
