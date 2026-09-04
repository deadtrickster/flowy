# shellcheck shell=bash
#
# A THREAD PANE OPENS, CLOSES, AND OPENS AGAIN.
#
# The operator, from a Fold 8: "on the phone threads are one time thing - i
# replied to the host once, cloded thread pane and it foesnt come back anymre."
#
# thread-on-a-phone.sh next door presses a thread control and asserts the panel
# comes on screen, and it passes - the FIRST open works. It never closes the
# panel, so a defect that appears only on the second open is outside what it
# looks at. A check that opens once cannot tell "opens" from "opens once".
#
# The second open is the assertion; the first is kept beside it as the control,
# so a run where the first open broke reports the OTHER defect by name instead
# of blaming this one.

a_thread_pane_opens_more_than_once() {
	recall
	local room=threadagain
	want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
		'{"body": "the message a thread hangs from"}' >/dev/null || return 1
	local head
	head=$(printf '%s' "$API_BODY" | jq -r '.id')
	[ -n "$head" ] && [ "$head" != null ] || return 1
	want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
		"{\"body\": \"the reply that makes it a thread\", \"thread\": \"$head\"}" >/dev/null || return 1

	cd "$ROOT/web" || return 1
	node scripts/thread-reopens-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$room"
}

check "a thread pane opens, closes, and opens again on a phone" \
	a_thread_pane_opens_more_than_once
