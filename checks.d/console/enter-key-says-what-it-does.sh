# shellcheck shell=bash
#
# THE RETURN KEY DECLARES WHAT IT ACTUALLY DOES.
#
# The operator, on a Fold 8, 2026-09-04: "enter on the phone sends, there is no
# way to have a newline". Row 01M1PKKC1M0AE6XVTBF1BPWP77.
#
# The composer sends on Enter and offers the newline only on shift-Enter, which
# a soft keyboard cannot press. Whether that should change is the operator's
# decision and is asked on the row. What was wrong independently of it is that
# the key carried no enterKeyHint, so Android drew its return arrow - the
# newline glyph - on a key that commits. The behaviour was fine and the label
# was false, which is this repo's most repeated defect and the reason a person
# on a phone hit it while nobody at a desk ever did.
#
# THE CHECK IS INDIFFERENT TO THE DECISION. It asserts that the declaration and
# the behaviour AGREE, so settling the question either way keeps it green as
# long as both move together - and reds the moment one of them moves alone.
#
# IN ITS OWN ROOM, so a filtered run and the full suite ask the same question
# rather than measuring whatever else the suite said in #general.

an_enter_key_says_what_it_does() {
	recall
	local room=enterkey
	# One message, so the room renders rather than sitting on its empty state.
	want_status 200 POST "$TOKEN_A_AGENT" "/api/chat/$room/say" \
		'{"body": "a room for the return key"}' >/dev/null || return 1

	cd "$ROOT/web" || return 1
	node scripts/enter-key-says-what-it-does-check.mjs \
		"http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$room"
}

check "the composer's return key declares what it actually does" \
	an_enter_key_says_what_it_does
