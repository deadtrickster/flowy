#!/usr/bin/env bash
#
# Refuse to move a protected branch unless the node says you hold its lock.
#
# INSTALLED AS A reference-transaction HOOK, which is the whole point. Every
# rule this fleet built today - declare-takes-the-lock, gated_tip equality,
# fast-forward only, abandon-with-a-reason - lives inside the land verb, and on
# 18 Aug master moved by a plain `git merge` while another agent held the lock
# for a thirteen-commit batch. Measured afterwards: five merge.gate events on
# the row and zero merge.land, no merge_lands row for the new tip, the row
# closed with an empty landed_tip. Nothing was bypassed, because nothing had to
# be used. A lock that guards a verb nobody is obliged to call guards nothing.
#
# So this sits where the ref actually changes. A pre-commit or pre-push hook is
# skippable with --no-verify and neither sees `git merge`; reference-transaction
# runs inside the transaction that updates the ref, for merge, reset, push,
# checkout -B and branch -f alike, and has no --no-verify.
#
# WHAT IT DOES NOT DO. It does not check the gate, the tip or the shape of the
# commit - the land verb does all three and does them better, because it has
# the row. This answers one question: MAY YOU MOVE THIS BRANCH RIGHT NOW. The
# answer is yes only while you hold the lock, which is exactly the window in
# which the other three checks were computed.
#
# git calls it three times per transaction - prepared, committed, aborted - and
# only "prepared" can refuse. The other two are told what happened and must
# exit 0, or an abort turns into an error about the abort.

set -euo pipefail

# THE PROTECTED BRANCHES. Everything else moves freely: a feature branch is
# nobody's shared state, and a hook that made every commit ask a server would
# be intolerable and would be removed within the hour, which is the failure
# mode of a guard that is too broad.
protected="${FLOWY_LAND_GUARD_REFS:-refs/heads/master}"

node="${FLOWY_ADDR:-http://192.168.1.55:8787}"
case "$node" in
http://* | https://*) ;;
*) node="http://$node" ;;
esac

state="${1:-}"
[ "$state" = "prepared" ] || exit 0

# Read the whole transaction first: refusing needs to name the ref, and stdin
# is gone once it is consumed.
refs=""
while read -r _old _new ref; do
	for want in $protected; do
		[ "$ref" = "$want" ] && refs="$refs $ref"
	done
done
[ -n "$refs" ] || exit 0

deny() {
	printf 'flowy: REFUSED to move%s\n' "$refs" >&2
	printf '  %s\n' "$1" >&2
	printf '\n  The landing lock is how this fleet stops two agents landing on one measurement.\n' >&2
	printf '  Land through the verb, which takes the lock, checks the gate and records what landed:\n' >&2
	printf '    POST %s/api/merge/{row}/gate   {"run": "..."}      declare, and take the lock\n' "$node" >&2
	printf '    POST %s/api/merge/{row}/land   {"sha": "..."}      after the fast-forward\n' "$node" >&2
	printf '    POST %s/api/merge/{row}/abandon {"reason": "..."}  if the gate went red\n' "$node" >&2
	printf '\n  If this is an emergency and you accept moving master under somebody else:\n' >&2
	printf '    FLOWY_LAND_GUARD=off git ...   - it is recorded in this shell, not in the log\n' >&2
	exit 1
}

# THE ESCAPE HATCH IS DELIBERATE AND DELIBERATELY LOUD. A guard with no way
# past it gets uninstalled the first time it is wrong at three in the morning,
# and then it protects nothing ever again. This one costs a conscious
# environment variable, which is not something anybody types by accident.
if [ "${FLOWY_LAND_GUARD:-on}" = "off" ]; then
	printf 'flowy: land guard OFF for%s - moving it without the lock\n' "$refs" >&2
	exit 0
fi

token="${FLOWY_TOKEN:-}"
if [ -z "$token" ] && [ -n "${FLOWY_AGENT:-}" ] && [ -r "$HOME/.config/flowy/agents/$FLOWY_AGENT" ]; then
	token="$(cat "$HOME/.config/flowy/agents/$FLOWY_AGENT")"
fi
# NO TOKEN IS A REFUSAL, not a pass. Somebody moving master with no identity is
# exactly who cannot have taken a lock.
[ -n "$token" ] || deny "no FLOWY_TOKEN, so the node cannot be asked whether you hold the lock"

answer=""
status=0
answer="$(curl -sS --max-time 5 -H "Authorization: Bearer $token" "$node/api/merge-queue" 2>&1)" || status=$?
# UNREACHABLE IS A REFUSAL. The alternative - allow when the node is down - is a
# guard that turns itself off exactly when nobody can see what is happening,
# and every silent-success this fleet found today had that shape.
[ "$status" -eq 0 ] || deny "the node at $node did not answer, so nothing can say whether you hold the lock"

read -r held holder holder_name item < <(
	printf '%s' "$answer" | python3 -c '
import json, sys
try:
    lock = (json.load(sys.stdin) or {}).get("lock") or {}
except Exception:
    print("error - -", "-")
    sys.exit(0)
print(
    "yes" if lock.get("held") else "no",
    lock.get("holder") or "-",
    lock.get("holder_name") or "-",
    lock.get("item") or "-",
)
' 2>/dev/null
) || deny "the node's answer could not be read, so nothing can say whether you hold the lock"

[ "$held" != "error" ] || deny "the node answered something that is not a lock"
[ "$held" = "yes" ] || deny "nobody holds the lock - a landing goes through a declaration, which is what takes it"

# WHOSE lock. Asked of the node rather than guessed from the token, because
# only the node can say which principal a token resolves to - and the lock
# records that same principal, so the two are comparable by construction.
me=""
who="$(curl -sS --max-time 5 -H "Authorization: Bearer $token" "$node/api/whoami" 2>/dev/null || true)"
me="$(printf '%s' "$who" | python3 -c '
import json, sys
try:
    who = json.load(sys.stdin) or {}
except Exception:
    sys.exit(0)
# The lock records voteActor: the agent when a token is an agents, the user
# otherwise. Same order here, or an agent would compare its user against an
# agent id and never match its own lock.
print(who.get("agent") or who.get("user") or "")
' 2>/dev/null || true)"
[ -n "$me" ] || deny "the node would not say who this token is, so it cannot say whether the lock is yours"

if [ "$me" != "$holder" ]; then
	deny "the lock is held by $holder_name for $item, and you are not them"
fi

# SEAT-LEVEL AND NOT SESSION-LEVEL, and it is worth being honest about the gap.
# Every session of a seat shares one principal, so two of my own agents pass
# this identically. The land verb closes that half - it compares the lock's
# item against the row being landed - and this hook cannot, because a ref
# update does not know which row it is for. What this stops is the case that
# actually cost us a batch: somebody else's land under a live holder.

exit 0
