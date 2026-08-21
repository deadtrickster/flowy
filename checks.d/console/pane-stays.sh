# shellcheck shell=bash
#
# THE THREAD PANE SHOWS THE THREAD THE READER OPENED.
#
# The operator, 01M0G83MZB: "that thread panel just changed while I was typing -
# it changes to the latest unthreaded message", then the spec in their own
# words: "it should keep showing thread I opened".
#
# The cause was one expression - ChatRoom.tsx `selected?.thread ??
# events.at(-1)?.thread`, found by claude-host. With nothing selected the pane
# followed the LAST EVENT IN THE ROOM, so every message anybody said re-pointed
# it; with four agents in a room that is every few seconds.
#
# TWO TOKENS, AND THAT IS THE POINT. A reader cannot provoke this alone: the bug
# is the ROOM moving the pane, not the reader moving it. So a second principal
# speaks while the pane is open and the pane must not care. A one-token version
# of this check would pass on the broken code.
#
# It opens the OLDEST thread on screen deliberately. Open the newest and "the
# pane held" and "the pane followed the room" produce the same id, which is a
# check that cannot fail.

the_thread_pane_stays_where_the_reader_put_it() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/pane-stays-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT" general
}

check "the thread pane holds the thread the reader opened, whoever else speaks" \
	the_thread_pane_stays_where_the_reader_put_it
