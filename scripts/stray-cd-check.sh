#!/usr/bin/env bash
# Does run-tests.sh notice when something ran outside check()?
#
#   scripts/stray-cd-check.sh [path/to/run-tests.sh]
#
# THIS COST A WHOLE GATE RUN. A test function registered as a BARE CALL rather
# than as an argument to check - written on the line after the CONTINUATION of
# the previous check, where it looks exactly like its neighbours - ran in the
# runner's own shell instead of in check's subshell. Its `cd "$ROOT/web"` moved
# the suite's working directory, nine later tests failed on relative paths, and
# the report named the tui block. None of it was about the change under test.
#
# The guard's whole value is the ratio: ONE honest failure that names the cause,
# instead of nine that point at the wrong file. So that is what this measures -
# not only that a failure happens, but that the failures AFTER it stop.
#
# It lifts check() out of the real file rather than restating it, because a copy
# of the function here would be a second thing to keep in step, which is the
# defect class this suite keeps finding elsewhere.
set -uo pipefail

src=${1:-run-tests.sh}
if [ ! -r "$src" ]; then
	printf 'stray-cd-check: cannot read %s\n' "$src" >&2
	exit 2
fi
src=$(cd "$(dirname "$src")" && pwd)/$(basename "$src")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

sed -n '/^check() {/,/^}/p' "$src" >"$work/defs.sh"
if ! grep -q '^check() {' "$work/defs.sh"; then
	printf 'stray-cd-check: no check() found in %s - this check is measuring nothing\n' "$src" >&2
	exit 2
fi

# EVERY FUNCTION check() CALLS, FOUND RATHER THAN LISTED.
#
# This used to lift check() and indent() by name, which made the extraction a
# hand-maintained list of check()'s dependencies - and on 2026-08-20 check()
# grew `timed` and `secs_since`, the harness died on the first call, and a
# branch was refused for a fault in this file. A list that has to be updated by
# whoever edits a DIFFERENT file is a list that will be out of date.
#
# So the names are read out of check()'s own body: any bare word at the start of
# a command that this suite also defines as a top-level function is pulled in
# with it. Not a parser - it does not need to be. A name it misses shows up as
# "command not found" in the could-not-measure arm below, which says so plainly
# instead of reporting a wrong count.
wanted=$(grep -oE '(^|[;&|(]|\bthen |\bdo |\$\()[[:space:]]*[a-z_][a-z0-9_]*' "$work/defs.sh" |
	grep -oE '[a-z_][a-z0-9_]*$' | sort -u)
for fn in $wanted; do
	[ "$fn" = "check" ] && continue
	grep -q "^$fn() {" "$src" || continue
	sed -n "/^$fn() {/,/^}/p" "$src" >>"$work/defs.sh"
done

cd "$work" || exit 2
SUITE_PWD="$PWD"
passed=0
failed=0
# WHAT check() READS THAT IS NOT DEFINED IN IT.
#
# The extraction lifts check() out of the suite and runs it alone, so every
# variable check() reads from its surroundings has to be supplied here. It is a
# list that grows, and it grew on 2026-08-20 when the ONLY= filter added `only`
# - the harness died on `only: unbound variable`, produced no TALLY, and this
# script reported a COUNT MISMATCH with an empty count. A broken check looked
# exactly like a failing subject, and the branch that added the filter was
# refused for it.
#
# Empty rather than absent, deliberately: an unset ONLY is how the suite runs
# normally, so this reproduces the ordinary path rather than the filtered one.
# shellcheck disable=SC2034  # read by check(), which is sourced from defs.sh
only=""
# The suite's accumulators, which the extracted harness inherits by reading and
# does not otherwise need. Functions are found automatically above; STATE cannot
# be, because a bare `timings+=(...)` names nothing this script could look up.
#
# `timings` is the PATH of the suite's timing file, not a list - read it before
# declaring it, which I did not: an empty array here left `[ -n "$timings" ]`
# unbound under set -u and the harness died in the same place for a new reason.
# Empty string is the value timed() already treats as "nowhere to write", so the
# extracted harness times nothing and returns at its first line.
# shellcheck disable=SC2034  # read by timed(), sourced from defs.sh
timings=""
# shellcheck disable=SC2034  # read by check() when a name is filtered out
skipped=0
# shellcheck source=/dev/null
. "$work/defs.sh"

mkdir -p elsewhere
ok() { return 0; }

# STDERR IS KEPT, because that is where a harness that could not start says why.
# It was going to /dev/null in effect - the subshell's stderr passed straight
# through to the caller's, so the one line naming the unbound variable landed in
# the drainer's log ABOVE the confusing message rather than inside it.
harness_err="$work/harness.err"
out=$(
	{
		check "before the stray" ok
		cd elsewhere || exit 2
		check "after the stray" ok
		check "the one after that" ok
		printf 'TALLY %d %d %s\n' "$passed" "$failed" "$PWD"
	} 2>"$harness_err"
)
tally=$(printf '%s\n' "$out" | sed -n 's/^TALLY //p')
read -r got_passed got_failed got_pwd <<<"$tally"

fail() {
	printf '%s\n' "$1" >&2
	printf 'what the harness saw:\n%s\n' "$out" >&2
	exit 1
}

# COULD NOT MEASURE IS NOT A WRONG ANSWER, and this arm is the whole lesson of
# the night applied to a test rather than to a door.
#
# No TALLY means the harness never reached its last line: it died, and every
# number below is empty. Falling through to the count comparisons turns that
# into "a stray cd produced  failures, want exactly 1" - a sentence about the
# SUBJECT, when the subject was never exercised. Whoever reads it goes looking
# for a bug in run-tests.sh that is not there.
#
# EXIT 2, not 1. One is "the thing under test is wrong"; two is "this check did
# not run" - the same separation the suite's own harness makes, and the reason
# the drainer can tell a red from a broken gate.
if [ -z "$tally" ]; then
	printf 'stray-cd-check: THE EXTRACTED HARNESS DID NOT RUN, so nothing was measured about\n' >&2
	printf 'run-tests.sh. This is a fault in this check, not a failure of the suite.\n\n' >&2
	printf 'Most likely check() now reads a variable this script does not supply - see the\n' >&2
	# shellcheck disable=SC2016  # the backticked text is prose naming a line of
	# this file, not an expansion
	printf 'only="" line above and add the new one beside it.\n\n' >&2
	if [ -s "$harness_err" ]; then
		printf 'what the harness said:\n' >&2
		sed 's/^/  /' "$harness_err" >&2
	else
		printf '(the harness said nothing, which is its own puzzle)\n' >&2
	fi
	exit 2
fi

# One failure, not three: the stray is reported once and the checks after it run
# where they are supposed to.
[ "$got_failed" = 1 ] || fail "a stray cd produced $got_failed failures, want exactly 1"
[ "$got_passed" = 3 ] || fail "only $got_passed of 3 checks passed after a stray cd - the guard did not put the directory back"
[ "$got_pwd" = "$SUITE_PWD" ] || fail "the suite was left in $got_pwd, not $SUITE_PWD"

# And it says WHY, naming the thing to go and look at. A guard that fails with
# "FAIL" and a path is a guard somebody has to already understand.
case $out in
*"ran outside check()"*) ;;
*) fail "the refusal does not say what happened" ;;
esac
case $out in
*"after the stray"*) ;;
*) fail "the refusal does not name the check it precedes" ;;
esac

printf 'a stray cd reports once, names itself, and the suite carries on where it started\n'
