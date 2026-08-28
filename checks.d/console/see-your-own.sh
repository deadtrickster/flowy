# shellcheck shell=bash
#
# A PERSON SEES THE MESSAGE THEY JUST POSTED.
#
# 01M14F27WM. The operator, in #general: "I also dont the the message I just
# posted". Both messages were on the node - read back from /api/chat - so the
# write landed and the console did not draw them. Display, not loss.
#
# THE GAP THIS FILLS. person-can-post-check.mjs posts through the composer and
# then asserts the message reached the NODE, by reading /api/chat for its text.
# It never looks at the message list. A console that took the message, cleared
# the box and drew nothing passes it - which is precisely what was reported.
#
# THE ID IS THE WITNESS, not the text: the id the write came back with is looked
# for as data-message on screen, so a room drawing a DIFFERENT message with
# similar words fails, and the check cannot be satisfied by its own canary
# turning up somewhere else on the page.
#
# TWO ARMS, because that is the difference the report turns on. The operator's
# first message was a REPLY into somebody else's thread and the room folds
# replies; the second was a thread root and should have shown regardless. So one
# plain message and one reply, and the author must be able to find both. Inside
# an OPEN fold is fine - behind a closed one, reachable from nowhere, is the
# defect. The fold itself is not the enemy: a busy room is unreadable without
# it, and 01M0X5EZTY is a live row about that control.
#
# The wait is longer than the room's poll window - api.wait blocks up to ~25s -
# because a message that appears only after F5 is the defect and one that
# appears after a poll is not.

a_person_sees_what_they_just_posted() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/see-your-own-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" seeyourown
}

check "a person sees the message they just posted, and their own reply in a thread" \
	a_person_sees_what_they_just_posted
