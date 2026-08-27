# shellcheck shell=bash
#
# A MERGE ROW CAN BE RANKED FROM THE MERGE PANE, AND THE QUEUE CARRIES THE WORD
# WITHOUT REORDERING.
#
# The operator, on the board: "add priorities to todos, and merges". The todos
# half landed with priority.sh; the merge half is this. The door stored the
# word on a merge row from the start - the queue projection dropped it, so the
# merge pane had nothing to draw and nothing to set, and a ranking nothing
# shows is a label.
#
# The browser half is not decoration: setting a priority has to reach the NODE,
# or it is a chip that survives until the next poll. The queue half is the
# projection: /api/merge-queue has to CARRY the word. And the order assertion
# is the operator's own policy - "I'm ok with having priorities respected _up
# to_ merge queue which can state FIFO for the time being" - so the ranking
# must not move a row out of its queued place.
#
# It files one merge row and closes it again, per 01M0HADJ2R. No gate run:
# ranking is orthogonal to landing evidence, and declaring one would take the
# landing lock a real drainer needs.

a_merge_row_can_be_ranked_from_the_pane() {
	cd "$ROOT/web" || return 1
	node scripts/merge-priority-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "a merge row is ranked from the merge pane, the node keeps it, the queue carries the word, and FIFO holds" \
	a_merge_row_can_be_ranked_from_the_pane
