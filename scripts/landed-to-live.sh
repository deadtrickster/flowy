#!/usr/bin/env bash
# Carry a merge row from LANDED to LIVE, and say what was measured.
#
#   scripts/landed-to-live.sh [--once]
#
# The operator: "any loops to code?" - 01M0HGN578. Every row closed tonight went
# through the same four steps by hand: read the drainer's "landed X as <sha>",
# wait for the deploy, curl /healthz and compare, then read the row and close it
# with what was measured. The first three are mechanical and every fact is
# already on the node.
#
# WHAT IT MUST NOT DO, and this is the whole design: it does not CLOSE anything.
# "The node serves a sha that contains the commit" is not "the thing works".
# Measured on three rows tonight where those differ - a password confirm that
# needed a browser, a priorities ordering that landed invisible, a mention ring
# on a surface with no traffic. A loop that closed rows on a version string
# would have closed all three and been wrong about two. So it moves a row to
# LIVE and states the evidence; the judgement stays with a person or a seat.
#
# ANCESTRY, NOT EQUALITY. /healthz reports the one sha it was built from, and by
# the time anybody looks two more rows have landed on top. A landing is live
# when its tip is an ANCESTOR of what is serving - equality would report the
# most recent landing live and every one before it as still waiting.
#
# IT SAYS EACH THING ONCE. The note is keyed on (row, serving sha) in $STATE, so
# a loop running every minute does not write a row a note a minute. A cleared
# cache re-states a fact that is still true, which is the harmless direction.
set -uo pipefail

NODE=${FLOWY_ADDR:-http://192.168.1.55:8787}
REPO=${FLOWY_REPO:-/home/dead/Projects/flowy}
STATE=${FLOWY_LIVE_STATE:-$HOME/.cache/flowy-live}
EVERY=${FLOWY_LIVE_EVERY:-120}
TOKEN=${FLOWY_TOKEN:-}
[ -n "$TOKEN" ] || TOKEN=$(cat "$HOME/.config/flowy/agents/orchestrator" 2>/dev/null || true)
[ -n "$TOKEN" ] || {
	printf 'landed-to-live: no token - set FLOWY_TOKEN\n' >&2
	exit 2
}
mkdir -p "$STATE" || exit 2

once=no dry=no
for arg in "$@"; do
	case "$arg" in
	--once) once=yes ;;
	# A PASS THAT WRITES NOTHING, because the first thing anybody wants from a
	# loop that writes on other people's rows is to see what it would say.
	--dry-run) dry=yes once=yes ;;
	*)
		printf 'usage: %s [--once] [--dry-run]\n' "$0" >&2
		exit 2
		;;
	esac
done

api() { # method path [body]
	if [ -n "${3:-}" ]; then
		curl -sS -m 20 -X "$1" -H "Authorization: Bearer $TOKEN" \
			-H 'Content-Type: application/json' -d "$3" "$NODE$2"
	else
		curl -sS -m 20 -H "Authorization: Bearer $TOKEN" "$NODE$2"
	fi
}

pass() {
	# WHAT THE NODE IS SERVING, as the sha after the +. A node that cannot be
	# read is not a node serving nothing: the pass ends rather than reporting
	# every landing as still waiting.
	local serving rows
	serving=$(curl -sS -m 10 "$NODE/healthz" 2>/dev/null |
		sed -n 's/.*"version":"[^+]*+\([^"]*\)".*/\1/p')
	if [ -z "$serving" ]; then
		printf 'landed-to-live: %s did not answer /healthz - nothing measured this pass\n' "$NODE" >&2
		return 1
	fi

	rows=$(api GET "/api/artifacts?kind=merge&limit=60" |
		python3 -c '
import json, sys
try:
    page = json.load(sys.stdin)
except Exception:
    raise SystemExit
for a in page.get("artifacts") or []:
    if (a.get("status") or "") != "done":
        continue
    tip = ((a.get("fields") or {}).get("landed_tip") or "").strip()
    if not tip:
        continue
    print(a["id"], tip, (a.get("fields") or {}).get("branch") or "-")
') || return 1

	# A WATCHER THAT STARTS MID-HISTORY MUST NOT REPLAY IT.
	#
	# Every landing this node has ever made is still "in what is serving", so a
	# first pass with an empty state would write a LIVE note on forty rows that
	# people closed by hand hours ago. That is not a loop finding something, it
	# is a loop shouting the past at everybody.
	#
	# So the first pass SEEDS: it marks what is already live and says how many,
	# and writes nothing on a row. From the second pass on, a marker missing
	# means the landing is new.
	local seeded=0
	local seeding=no
	if [ -z "$(ls -A "$STATE" 2>/dev/null)" ]; then
		seeding=yes
	fi

	local id tip branch marker
	while read -r id tip branch; do
		[ -n "$id" ] || continue
		marker="$STATE/live-$id-$serving"
		[ -e "$marker" ] && continue
		# IS THIS LANDING IN WHAT IS SERVING? Ancestry, asked of git rather than
		# of the strings: a short sha that is a prefix of another is not the
		# same commit, and two landings a minute apart share no prefix at all.
		git -C "$REPO" merge-base --is-ancestor "$tip" "$serving" 2>/dev/null || continue
		if [ "$seeding" = yes ]; then
			[ "$dry" = yes ] || : >"$marker"
			seeded=$((seeded + 1))
			continue
		fi
		if [ "$dry" = yes ]; then
			printf 'would note %s: %s landed as %s, serving %s\n' "$id" "$branch" "$tip" "$serving"
			continue
		fi
		api POST "/api/todo/$id/note" \
			"$(python3 -c '
import json, sys
print(json.dumps({"note": sys.argv[1]}))
' "LIVE: $branch landed as $tip and the node is serving $serving, which contains it. Measured $(date -u +%Y-%m-%dT%H:%M:%SZ) by landed-to-live.sh. This is a version fact and not a verdict - whether the change WORKS is still somebody's to check, and this row stays open until they say so.")" >/dev/null || continue
		: >"$marker"
		printf 'noted %s live in %s\n' "$id" "$serving"
	done <<<"$rows"
	if [ "$seeding" = yes ]; then
		printf 'landed-to-live: first pass - %d landing(s) already live in %s, seeded and not announced\n' \
			"$seeded" "$serving"
	fi
	return 0
}

if [ "$once" = yes ]; then
	pass
	exit $?
fi
while :; do
	pass || true
	sleep "$EVERY"
done
