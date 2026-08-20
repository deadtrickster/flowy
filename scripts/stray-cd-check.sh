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

sed -n '/^indent() {/,/^}/p' "$src" >"$work/defs.sh"
sed -n '/^check() {/,/^}/p' "$src" >>"$work/defs.sh"
if ! grep -q '^check() {' "$work/defs.sh"; then
	printf 'stray-cd-check: no check() found in %s - this check is measuring nothing\n' "$src" >&2
	exit 2
fi

cd "$work" || exit 2
SUITE_PWD="$PWD"
passed=0
failed=0
# shellcheck source=/dev/null
. "$work/defs.sh"

mkdir -p elsewhere
ok() { return 0; }

out=$(
	check "before the stray" ok
	cd elsewhere || exit 2
	check "after the stray" ok
	check "the one after that" ok
	printf 'TALLY %d %d %s\n' "$passed" "$failed" "$PWD"
)
tally=$(printf '%s\n' "$out" | sed -n 's/^TALLY //p')
read -r got_passed got_failed got_pwd <<<"$tally"

fail() {
	printf '%s\n' "$1" >&2
	printf 'what the harness saw:\n%s\n' "$out" >&2
	exit 1
}

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
