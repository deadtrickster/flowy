# shellcheck shell=bash
#
# OPENING A THREAD SHOWS THE THREAD, ON A PHONE TOO.
#
# Below lg the room panel is a drawer translated off the right edge, not a
# column. Nothing opened it when a thread was opened, so pressing "N replies"
# on a phone changed the URL, selected the thread pane, and drew the room with
# no visible change whatsoever. Reported from a Fold 8 - both its widths are
# under lg, which is why neither the folded nor the unfolded screen worked.
#
# THE ARM RUNS AT NARROW WIDTHS because that is the only place it fails. A
# desktop-only check was green through the whole life of the defect, and the
# desktop width is kept here as the control: the panel is a column there and
# must stay one, so this cannot pass by forcing a drawer open everywhere.
#
# GEOMETRY, NOT STATE. data-room-panel-state is set by the same code a fix
# touches, so a check reading it would pass on a drawer still sitting off the
# edge. What is asserted is that the panel's box overlaps the viewport.

opening_a_thread_shows_it_on_a_phone() {
	recall
	local room=threadphone
	# want_status leaves the body in API_BODY - there is no want_body, and a
	# helper invented here would be a green that never ran.
	want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
		'{"body": "the message a thread hangs from"}' >/dev/null || return 1
	local head
	head=$(printf '%s' "$API_BODY" | jq -r '.id')
	[ -n "$head" ] && [ "$head" != null ] || return 1
	want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
		"{\"body\": \"the reply that makes it a thread\", \"thread\": \"$head\"}" >/dev/null || return 1

	cd "$ROOT/web" || return 1
	node scripts/thread-on-a-phone-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$room"
}

check "opening a thread shows it on a phone, and the panel is still a column on a desktop" \
	opening_a_thread_shows_it_on_a_phone
