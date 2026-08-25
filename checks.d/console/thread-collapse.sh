# shellcheck shell=bash
#
# A THREAD'S REPLIES FOLD INTO ITS HEAD ROW, PER READER, IN THE ROOM STREAM.
#
# The operator, 01M0WF2T2: "hiding followup messages in threads from main room
# stream". The contract, answered on the row: the root stays in the stream for
# everyone and its replies collapse into it with a count and the latest one's
# words; a reply addressed to the reader still surfaces as its own row, because
# this fleet's recurring failure is silence reading as absence; collapsing is
# per-reader state stored on the node; opening the thread shows everything.
#
# THREE ARMS, of which the third is the one a component test would miss:
#
#   1. a room holding a threaded conversation shows one row rather than eight;
#   2. opening the thread shows all of them;
#   3. as the addressee of one reply, that reply is in the stream while the
#      rest stay collapsed.
#
# Plus the stored half of per-reader: an unfold survives a reload and hide puts
# it back. Two threads in one room, one addressed reply seeded newest, so the
# fold block's snippet must skip a visible reply and summarise the latest
# hidden one.
#
# TWO TOKENS, AND THAT IS THE POINT. The addressed arm needs a reply from
# somebody other than the reader, and the reader's agent speaks the thread B
# replies - a check with one token could not prove "addressed to me, sent by
# somebody else, stays in the stream".

a_thread_collapses_in_the_stream_per_reader() {
	cd "$ROOT/web" || return 1
	node scripts/thread-collapse-check.mjs "http://127.0.0.1:$HTTP_PORT" \
		"$TOKEN_A" "$TOKEN_A_AGENT"
}

check "a thread's replies fold into its head row, per reader, and an addressed reply stays in the stream" \
	a_thread_collapses_in_the_stream_per_reader
