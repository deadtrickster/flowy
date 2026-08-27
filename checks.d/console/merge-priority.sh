# shellcheck shell=bash
#
# A MERGE ROW CAN BE RANKED FROM THE MERGE PANE, AND THE QUEUE CARRIES THE WORD
# AND REORDERS BY IT.
#
# The operator, on the board: "add priorities to todos, and merges". The todos
# half landed with priority.sh; the merge half is this. The door stored the
# word on a merge row from the start - the queue projection dropped it, so the
# merge pane had nothing to draw and nothing to set, and a ranking nothing
# shows is a label.
#
# The browser half is not decoration: setting a priority has to reach the NODE,
# or it is a chip that survives until the next poll. The queue half is the
# projection: /api/merge-queue has to CARRY the word - and "" for an unjudged
# row, rather than dropping the key, so an unjudged row never reads like an
# older node that does not rank at all. The order assertion is the queue's own
# sort - now, next, UNJUDGED, later, age breaking ties within a rank - which
# is what the drainer already consumes: the newest row filed, ranked now,
# sorts above an older one nobody judged, and one somebody shelved sorts below
# both. The operator's "FIFO for the time being" survives as that age
# tie-break.
#
# It files three rows and closes them again, per 01M0HADJ2R. No gate run:
# ranking is orthogonal to landing evidence, and declaring one would take the
# landing lock a real drainer needs.

a_merge_row_can_be_ranked_from_the_pane() {
	cd "$ROOT/web" || return 1
	node scripts/merge-priority-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "a merge row is ranked from the merge pane, the node keeps it, the queue carries the word, and the queue reorders" \
	a_merge_row_can_be_ranked_from_the_pane
