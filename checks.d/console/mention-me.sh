# shellcheck shell=bash
#
# EVERY MESSAGE LIST KNOWS WHO IS READING IT.
#
# MessageList takes `me` and hands it to renderChat, which rings a mention chip
# when the resolved id is the reader. Three call sites passed it and the fourth
# did not, so the handoff view drew every mention unringed for everybody -
# including a mention of YOU, on the one surface where one seat asks another for
# something and the ring is the only thing that says the ask is yours. Found by
# flowy-claude while measuring 01M0GGSM99 with a browser; it is a missing prop,
# and a browser was a lot of machinery to discover a missing prop with.
#
# SO THIS IS A SOURCE CHECK, and deliberately not a browser one. What broke is
# not a behaviour that needs a page to observe - it is a call site that forgot
# an argument, and the next one will be too. A browser check would need a task,
# a mention and a second principal to prove one prop is present, and it would
# only ever cover the surface somebody remembered to write it for.
#
# It is in checks.d/console because its subject is the console, not because it
# opens one.

every_message_list_knows_who_is_reading() {
	cd "$ROOT/web/src" || return 1
	local sites missing=0 file
	sites=$(grep -rl '<MessageList' . || true)
	if [ -z "$sites" ]; then
		printf 'no MessageList call sites found at all - this check is measuring nothing,\n' >&2
		printf 'which is what it would report if the component were renamed\n' >&2
		return 1
	fi
	# Per FILE rather than per occurrence: a render can span several lines - the
	# one this check was written for now does - so grepping a single line for
	# both would have to know how the file is formatted. Every file that draws
	# a MessageList must also name `me` somewhere in that render, and no file
	# here draws two.
	while read -r file; do
		# The prop, in either shape: `me={{...}}` on a multi-line render or
		# `me={me}` if a caller ever holds it in a variable.
		if ! grep -q 'me={' "$file"; then
			printf '%s draws a MessageList and never passes me\n' "$file" >&2
			missing=$((missing + 1))
		fi
	done <<<"$sites"
	if [ "$missing" -gt 0 ]; then
		printf '\nme is what rings a mention of the reader - see lib/markdown. A list without\n' >&2
		printf 'it draws a mention of YOU exactly like a mention of anybody else.\n' >&2
		return 1
	fi
	printf '%s message list call site(s), all of them told who is reading\n' "$(printf '%s\n' "$sites" | wc -l)"
}

check "every message list is told who is reading, so a mention of you is ringed" \
	every_message_list_knows_who_is_reading
