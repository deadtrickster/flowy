#!/usr/bin/env bash
# Does the console-check loader notice when a check silently did not run?
#
#   scripts/console-checks-check.sh [path/to/run-tests.sh]
#
# WHY THE LOADER NEEDS ITS OWN CHECK. Moving console checks out of one shared
# list into one file each removes the conflicts, and buys a new failure the
# shared list could not have: a line was either in the file or it was not, but a
# DIRECTORY can be missing, empty, unreadable, or full of files that define a
# function and never register it - and every one of those looks in a summary
# line exactly like a suite where those checks passed.
#
# So the loader is written to fail loudly in all four cases, and this drives it
# through them. It lifts console_checks() out of the real file rather than
# restating it, for the reason stray-cd-check gives about check(): a copy here
# would be a second thing to keep in step.
#
# EXIT 2 IS "THIS CHECK DID NOT RUN", exit 1 is "the loader is wrong". Same
# separation the suite makes, and the reason a drainer can tell a red from a
# broken gate.
set -uo pipefail

src=${1:-run-tests.sh}
if [ ! -r "$src" ]; then
	printf 'console-checks-check: cannot read %s\n' "$src" >&2
	exit 2
fi
src=$(cd "$(dirname "$src")" && pwd)/$(basename "$src")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

sed -n '/^console_checks() {/,/^}/p' "$src" >"$work/loader.sh"
if ! grep -q '^console_checks() {' "$work/loader.sh"; then
	printf 'console-checks-check: no console_checks() in %s - this check is measuring nothing\n' "$src" >&2
	exit 2
fi

# The suite's surroundings that the loader reads: the accumulators it adds to,
# the root it resolves the directory against, and indent, which it pipes its
# explanations through. Supplied rather than stubbed away, so the extracted
# loader takes the same path it takes in the suite.
indent() { sed 's/^/  /'; }
passed=0
failed=0
# shellcheck disable=SC2034  # read by console_checks, sourced from loader.sh
skipped=0
check() {
	printf 'REGISTERED %s\n' "$1"
	passed=$((passed + 1))
}
# shellcheck source=/dev/null
. "$work/loader.sh"

# run drives the loader against one directory layout and prints what it did.
# ROOT is set per call because the loader resolves checks.d/console under it.
#
# THE LOADER IS NOT RUN IN A COMMAND SUBSTITUTION, and that is the whole reason
# this function exists rather than one line at each call site. `out=$(console_checks)`
# runs it in a SUBSHELL, so every passed/failed it counted is discarded when the
# subshell exits and the TALLY reports the parent's zeros - which is what the
# first draft of this file did, and it read as "the loader registered nothing"
# about a loader that had just registered the check named on the line above.
run() {
	# shellcheck disable=SC2034  # ROOT is read by console_checks, from loader.sh
	ROOT="$1"
	passed=0
	failed=0
	console_checks >"$work/out" 2>&1
	printf '%s\nTALLY %d %d\n' "$(cat "$work/out")" "$passed" "$failed"
}

fail() {
	printf 'console-checks-check: %s\n' "$1" >&2
	printf 'what the loader did:\n%s\n' "${2:-}" | sed 's/^/  /' >&2
	exit 1
}

# ---- a directory with a check in it registers exactly that check ----
good="$work/good"
mkdir -p "$good/checks.d/console"
cat >"$good/checks.d/console/one.sh" <<'FILE'
# shellcheck shell=bash
a_check_from_a_file() { return 0; }
check "a check that came out of a file" a_check_from_a_file
FILE
got=$(run "$good")
[ -n "$got" ] || fail "the loader printed nothing at all on a good directory - it did not run" ""
case "$got" in
*"TALLY 1 0"*) ;;
*) fail "a directory holding one check did not register exactly one" "$got" ;;
esac
case "$got" in
*"a check that came out of a file"*) ;;
*) fail "the check in the file was not the one registered" "$got" ;;
esac

# ---- a file that defines a function and never registers it is a FAILURE ----
#
# This is the new way to lose a check, and the one worth the most: the file is
# present, tracked, linted and sourced, and the thing it is named for never
# runs. Nothing else in the suite would say so.
quiet="$work/quiet"
mkdir -p "$quiet/checks.d/console"
cat >"$quiet/checks.d/console/silent.sh" <<'FILE'
# shellcheck shell=bash
a_check_nobody_registered() { return 0; }
FILE
got=$(run "$quiet")
case "$got" in
*"TALLY 0 1"*) ;;
*) fail "a file that registered no check was not reported" "$got" ;;
esac
case "$got" in
*"silent.sh"*) ;;
*) fail "the report does not name the file that registered nothing" "$got" ;;
esac

# ---- an empty directory is a failure, not a quiet pass ----
empty="$work/empty"
mkdir -p "$empty/checks.d/console"
got=$(run "$empty")
case "$got" in
*"TALLY 0 1"*) ;;
*) fail "an empty console directory passed - every check in it is missing and nothing said so" "$got" ;;
esac
# THE SENTENCE, not just the count. Both this case and the missing-directory one
# below end in one failure naming the same path, so a check that asserted only
# the tally could not tell them apart - measured by removing the missing-directory
# arm from the loader and watching this file still pass, because the empty arm
# caught the fall-through and said the wrong thing about it.
case "$got" in
*"holds no checks"*) ;;
*) fail "an empty directory was reported as something other than empty" "$got" ;;
esac

# ---- and a missing one says which path it looked at ----
gone="$work/gone"
mkdir -p "$gone"
got=$(run "$gone")
case "$got" in
*"TALLY 0 1"*) ;;
*) fail "a missing console directory passed" "$got" ;;
esac
case "$got" in
*"$gone/checks.d/console"*) ;;
*) fail "the report does not say which directory was missing" "$got" ;;
esac
case "$got" in
*"directory is missing"*) ;;
*) fail "a missing directory was reported as something else - probably as an empty one, which sends the reader to look at a directory that is not there" "$got" ;;
esac

printf 'the loader registers what is in the directory, and refuses a directory that is missing, empty, or holds a file that registers nothing\n'
