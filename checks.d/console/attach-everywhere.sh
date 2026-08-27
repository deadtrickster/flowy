# shellcheck shell=bash
#
# A FILE RIDES ANY MEMORY TYPE, IS DRAWN WHERE THE PROSE REFERS TO IT, AND IS
# STILL LISTED AT THE FOOT.
#
# Asked for directly: "we must be able to attach screenshots to all memory
# types... put right into the body as <img>, plus listed in attachment, JIRA
# style." Attachments already existed - POST /api/attachment, the cards, the
# space-separated id field - and what was missing was which surfaces honour
# them. A note or a report could carry a file that nothing drew, and a body
# could not refer to one at all.
#
# ASSERTED AS DIFFERENCES, because one reading cannot tell a feature from a
# panel that draws everything unconditionally:
#
#   a note WITH a file draws a card and one WITHOUT draws no card and no empty
#   heading - the arm that fails on master, where a note draws neither
#
#   a body referring to a file renders an <img> whose naturalWidth is non-zero,
#   which is the difference between a picture and a broken reference. A check
#   that asserted the tag existed would pass on a document full of broken
#   glyphs.
#
#   the same file is in the list at the foot as well, because inline and listed
#   answer different questions and JIRA style is both
#
#   and a reference the reader cannot follow says so BY NAME. store.ErrNoBytes
#   exists precisely because "you may not read this" and "there is nothing
#   here" are different, and a dead <img> draws them identically.

a_file_rides_any_memory_type() {
	cd "$ROOT/web" || return 1
	node scripts/attach-everywhere-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

check "a file rides any memory type, draws where the prose refers to it, and is still listed" \
	a_file_rides_any_memory_type
