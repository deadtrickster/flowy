#!/usr/bin/env bash
#
# DOES A CHECK THAT NEVER RETURNS GET ENDED, AND DOES ITS TREE GO WITH IT?
#
# 01M17SYD3XDKS8GVZ2DVMMJ762. On 2026-08-29 one check sat for sixty minutes
# holding a live browser, and the single gate lane stayed held behind it while
# three filed rows waited. The check bounded seven individual waits; none of
# them fired, because the thing it was waiting on was alive and answering.
#
# This lifts bounded() out of the real run-tests.sh rather than restating it -
# a copy would go stale the first time somebody edited the original, and a
# check measuring a copy is measuring nothing. The same trick stray-cd-check.sh
# uses, and for the same reason.
#
# THREE ARMS, and the third is the one that cost an hour:
#
#   a check that returns is untouched, and its output and status survive
#   a check that hangs is ended, and the report NAMES the ceiling
#   the hung check's CHILDREN are dead afterwards - killing the top shell alone
#   left the node, the browser and a flowy serve orphaned on the port the next
#   pass needs, so the kill looked like it worked and the next run could not bind
set -euo pipefail

src=${1:?usage: check-ceiling-check.sh path/to/run-tests.sh}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

sed -n '/^bounded() {/,/^}/p' "$src" >"$work/defs.sh"
if ! grep -q '^bounded() {' "$work/defs.sh"; then
	printf 'check-ceiling-check: no bounded() in %s - this check is measuring nothing\n' "$src" >&2
	exit 2
fi

# shellcheck disable=SC1091  # written just above, by this script
. "$work/defs.sh"

fails=0
report() {
	printf '%s\n' "$1" >&2
	fails=$((fails + 1))
}

# ARM 1: a check that returns is left alone.
quick() {
	printf 'the check said this\n'
	return 3
}
status=0
out=$(CHECK_CEILING=30 bounded quick) || status=$?
[ "$status" = 3 ] || report "a check that returned 3 came back as $status - the ceiling changed a passing run's answer"
case $out in
*"the check said this"*) ;;
*) report "the check's own output did not survive the ceiling: $out" ;;
esac
case $out in
*"did not return within"*) report "a check that returned in no time was reported as timed out" ;;
esac

# ARM 2 and 3: a check that hangs, and the tree under it.
#
# The child writes its pid where this script can read it, so the descendant is
# addressed by IDENTITY rather than by a name pattern - `pgrep -f sleep` would
# match every other sleep on the machine, this run's and other people's.
hangs() {
	sleep 300 &
	printf '%s\n' "$!" >"$work/kid"
	wait
}
started=$(date +%s)
status=0
out=$(CHECK_CEILING=3 bounded hangs) || status=$?
elapsed=$(($(date +%s) - started))

[ "$status" = 124 ] || report "a check that hung came back as $status, not 124"
[ "$elapsed" -lt 60 ] || report "the ceiling took ${elapsed}s to end a check bounded at 3s"
case $out in
*"did not return within 3s"*) ;;
*) report "the report does not name the ceiling it enforced: $out" ;;
esac

kid=$(cat "$work/kid" 2>/dev/null || printf '')
if [ -z "$kid" ]; then
	report "the hung check never recorded its child, so arm 3 measured nothing"
elif kill -0 "$kid" 2>/dev/null; then
	kill -KILL "$kid" 2>/dev/null || true
	report "the hung check was ended and its child $kid was still alive - this is exactly the
orphan that kept holding the port: the kill looks like it worked and the next run cannot bind"
fi

if [ "$fails" -gt 0 ]; then
	printf 'check-ceiling-check: %s arm(s) failed\n' "$fails" >&2
	exit 1
fi
printf 'a returning check is untouched; a hung one is ended at its ceiling in %ss, named, with its tree\n' "$elapsed"
