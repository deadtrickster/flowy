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
# The shas as well as the ref: a bypass that says only "master moved" is a
# report nobody can check afterwards. From-and-to is what makes it auditable.
moves=""
while read -r old new ref; do
	# A TRANSACTION THAT DOES NOT MOVE THE BRANCH IS NOT A LANDING. git puts
	# the branch in the transaction with old == new for operations that only
	# LOOK at it, and `git stash` is the one everybody hits: it runs a
	# `reset --hard HEAD` internally, which writes refs/heads/master back to
	# the sha it already had. That was refused, so on a protected branch a
	# stash - and a `reset --hard HEAD`, and a `checkout -B` onto the same
	# commit - failed with a message about the landing lock, which is not what
	# any of them are doing.
	#
	# Skipping them is safe by construction rather than by judgement: the ref
	# holds the same commit before and after, so no measurement anybody made
	# against it can be invalidated by something that did not happen.
	[ "$old" = "$new" ] && continue
	for want in $protected; do
		if [ "$ref" = "$want" ]; then
			refs="$refs $ref"
			moves="$moves$ref ${old:0:12} -> ${new:0:12}
"
		fi
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

	# AND THE FLEET IS TOLD, not just the person who typed it. stderr reaches
	# one terminal; everybody else's next measurement is against a master that
	# moved for a reason they cannot see, which is the same silent-success
	# shape this guard was built to end.
	#
	# Two rules here are the OPPOSITE of the ones below, deliberately:
	#
	# THE RECORD GOES FIRST. Announce, then allow. A bypass that dies mid-merge
	# is still on the board; the other order loses exactly the case worth
	# keeping.
	#
	# UNREACHABLE IS NOT A REFUSAL. Below, a node that will not answer means
	# refuse - nothing can say whether you hold the lock. Here it must mean
	# proceed: the hatch exists for when things are broken and a node that is
	# down is one of those things. Blocking an emergency because the emergency
	# is in progress is how a guard gets ripped out for good. So it says
	# loudly that the bypass went unrecorded and gets out of the way.
	hatch_token="${FLOWY_TOKEN:-}"
	if [ -z "$hatch_token" ] && [ -n "${FLOWY_AGENT:-}" ] &&
		[ -r "$HOME/.config/flowy/agents/$FLOWY_AGENT" ]; then
		hatch_token="$(cat "$HOME/.config/flowy/agents/$FLOWY_AGENT")"
	fi
	hatch_reason="${FLOWY_LAND_GUARD_REASON:-no reason given - FLOWY_LAND_GUARD_REASON was not set}"
	recorded=no
	if [ -n "$hatch_token" ]; then
		hatch_body="$(
			FLOWY_HATCH_WHO="${FLOWY_AGENT:-$(id -un)}@$(hostname)" \
			FLOWY_HATCH_MOVES="$moves" \
			FLOWY_HATCH_REASON="$hatch_reason" \
				python3 -c '
import json, os
print(json.dumps({
    "scope": "node",
    "severity": "warning",
    "title": "land guard bypassed: master moved without the lock",
    "body": (
        "FLOWY_LAND_GUARD=off was set, so the reference-transaction hook allowed a "
        "protected branch to move without the landing lock.\n\n"
        "who    " + os.environ["FLOWY_HATCH_WHO"] + "\n"
        "moves  " + os.environ["FLOWY_HATCH_MOVES"].strip() + "\n"
        "reason " + os.environ["FLOWY_HATCH_REASON"] + "\n\n"
        "Anything gated against the previous tip was measured on a tree that is no "
        "longer master. Re-gate before landing on it."
    ),
})) '
		)"
		if curl -sS --max-time 5 -o /dev/null \
			-H "Authorization: Bearer $hatch_token" \
			-H "Content-Type: application/json" \
			-d "$hatch_body" "$node/api/announcements" 2>/dev/null; then
			recorded=yes
		fi
	fi
	# AND A LOCAL TRACE, always, node or no node. The announcement is the half
	# that needs a working network; this half needs nothing, and it is what is
	# still here tomorrow for whoever finds master somewhere they did not put
	# it. It goes in $GIT_DIR rather than the working tree on purpose: a
	# tracked file would turn every bypass into a dirty tree and eventually
	# into a merge conflict, and a bypass log that makes the next landing
	# harder is a log people delete.
	hatch_log="${GIT_DIR:-$(git rev-parse --git-dir 2>/dev/null || echo .git)}/flowy-bypass.log"
	{
		printf '%s\tFLOWY_LAND_GUARD=off\twho=%s\treason=%s\n' \
			"$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
			"${FLOWY_AGENT:-$(id -un)}@$(hostname)" \
			"$hatch_reason"
		printf '%s' "$moves" | sed 's/^/\t/'
	} >>"$hatch_log" 2>/dev/null || true

	if [ "$recorded" = yes ]; then
		printf '  announced to the node - the fleet can see this happened\n' >&2
	else
		printf '  THIS BYPASS WAS NOT RECORDED ON THE NODE - no token, or it did not answer.\n' >&2
		printf '  Say it in the room yourself, or master moved and only this terminal knows.\n' >&2
	fi
	printf '  written to %s\n' "$hatch_log" >&2
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
