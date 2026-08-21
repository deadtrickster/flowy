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
	local missing=0 file renderer line block
	# A NAMED SET, because the bug this check could not see was a SECOND
	# renderer. It walked MessageList alone, so when the operator reported that
	# mentions were not ringed in threads (01M0HHFF54) this check was green and
	# correct to be: the thread pane draws a ThreadList, a different component
	# over the same events, which never took `me`.
	#
	# Named rather than inferred - a wildcard over components would have to
	# guess which lists are lists of messages, and would pass anything that
	# happens to mention `me` for another reason. Adding a renderer is one word.
	local renderers="MessageList ThreadList"
	# PER RENDER, NOT PER FILE, and that distinction is the whole check now.
	# ChatRoom.tsx draws BOTH - the room log and the thread pane - so a per-file
	# test is satisfied by either one of them passing `me` and cannot see the
	# other missing it. That is exactly the shape of the bug: two renderers in
	# one file, one told who is reading and one not.
	local seen=0
	for renderer in $renderers; do
		while read -r file; do
			[ -n "$file" ] || continue
			# Each render, from its opening tag to the first line that closes
			# it. JSX here is either self-closing (/>) or a one-line render.
			while read -r line; do
				seen=$((seen + 1))
				block=$(awk -v start="$line" 'NR>=start{print; if (/\/>/ || (NR>start && /^ *>/)) exit}' "$file")
				if ! printf '%s' "$block" | grep -q 'me={'; then
					printf '%s:%s draws a <%s and never passes me\n' \
						"$file" "$line" "$renderer" >&2
					missing=$((missing + 1))
				fi
			done < <(grep -n "<$renderer" "$file" | cut -d: -f1)
		done < <(grep -rl "<$renderer" . || true)
	done
	if [ "$seen" -eq 0 ]; then
		printf 'no message-list renders found at all - this check is measuring nothing,\n' >&2
		printf 'which is what it would report if the components were renamed\n' >&2
		return 1
	fi
	if [ "$missing" -gt 0 ]; then
		printf '\nme is what rings a mention of the reader - see lib/markdown and ThreadList.\n' >&2
		printf 'A list without it draws a mention of YOU exactly like a mention of anybody\n' >&2
		printf 'else, and a second renderer of the same events is where that hides.\n' >&2
		return 1
	fi
	printf '%s message-list render(s) across %s, all told who is reading\n' \
		"$seen" "$(printf '%s' "$renderers" | tr ' ' ',')"
}

check "every message list is told who is reading, so a mention of you is ringed" \
	every_message_list_knows_who_is_reading
