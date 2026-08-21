# shellcheck shell=bash
#
# A DIRECT MESSAGE SAYS IT IS WAITING, AND STOPS SAYING IT ONCE IT IS READ.
#
# 01M0GP1S0K: the private log had no read mark anywhere. /api/dm takes a raw
# cursor and the only thing holding it was the open tab, so nothing on the node
# knew which private messages a person had read - and the rail's direct row, the
# one place in this console where what you write is read by ONE NAMED PERSON,
# was shipped silent because the only number available was "how many DMs exist".
#
# The assertion is not that a badge appears. It is that it CLEARS when the
# message is read and STAYS CLEARED ACROSS A RELOAD, which is the whole
# difference between a mark on the node and a cursor in a tab - and the only
# part of this that no component test could tell you.
#
# Two tokens, because a message you sent yourself does not wake you: the inbox
# has never counted your own writing, and a check that sent to itself would
# measure a badge that was never going to appear.

a_direct_message_says_it_is_waiting_until_it_is_read() {
	cd "$ROOT/web" || return 1
	node scripts/dm-unread-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_B"
}

check "a direct message raises the rail, clears when read, and stays cleared across a reload" \
	a_direct_message_says_it_is_waiting_until_it_is_read
