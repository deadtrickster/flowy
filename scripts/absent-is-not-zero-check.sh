#!/usr/bin/env bash
# Does deploy.sh tell an unanswerable counter apart from a zero one?
#
#   scripts/absent-is-not-zero-check.sh [deploy.sh]
#
# 25921f6 made a retried deadlock countable at GET /api/node, and deploy.sh reads
# it either side of the migration to report how many reads that migration
# aborted. The whole value of the number is that it can be believed, and the one
# way it silently stops being true is a node that cannot answer being reported as
# a clean zero. That is the same defect the counter itself was built to end -
# three days of "no deadlock 500s" that meant "nobody is counting".
#
# TWO WAYS THE FIELD IS MISSING, and they must not both become 0: a binary older
# than 25921f6 has no such field at all, and a node that refuses or times out
# returns no JSON. Both have to read as unknown.
#
# WHY THIS IS NOT COVERED BY READING THE CODE. The natural pipeline
# `grep -o ... | head -1 | cut -d: -f2` gets this RIGHT while `set -o pipefail`
# is in force, because a grep that matches nothing fails the pipeline. It gets it
# WRONG the moment somebody edits the set line at the top of deploy.sh, because
# `cut` exits 0 on empty input and prints nothing - and an empty string compared
# numerically is where a clean zero comes from. So the property under test is not
# "the code looks right", it is "it is still right with pipefail off", and this
# runs it that way deliberately.
#
# IT PROVES IT CAN FAIL. A stub answer with no field is fired at the function
# first; if that comes back as a number, this refuses rather than going on to
# report the tree clean.
set -uo pipefail

DEPLOY="${1:-scripts/deploy.sh}"

die() {
	printf 'absent-is-not-zero: %s\n' "$*" >&2
	exit 1
}

[ -f "$DEPLOY" ] || die "no deploy script at $DEPLOY"

grep -q '^deadlock_retries() {' "$DEPLOY" ||
	die "$DEPLOY has no deadlock_retries() - if it was renamed, rename it here too rather than deleting this check"

# Lifted out and run with pipefail DELIBERATELY OFF - see the header. The
# function's own explicit validation is what is under test, not the shell option
# that happens to mask its absence.
probe() { # answer -> prints the value, or fails
	local STUB="$1"
	(
		# pipefail OFF, EXPLICITLY. A subshell inherits it from this script's
		# own `set -uo pipefail`, and `set -u` does not clear it - so the first
		# version of this check ran every arm WITH pipefail and passed against
		# the naive pipeline it was written to catch. Turning it off is the
		# entire point: the function must validate the value itself.
		set -u
		set +o pipefail
		# All three are reached only through the eval below, which shellcheck
		# cannot see into: URL and find_token are what the lifted function
		# reads, and curl is stubbed so the probe never leaves this process.
		# shellcheck disable=SC2034
		URL="stub://node"
		# shellcheck disable=SC2329
		find_token() { printf 'stub-token\n'; }
		# shellcheck disable=SC2329
		curl() { printf '%s' "$STUB"; }
		eval "$(sed -n '/^deadlock_retries() {/,/^}/p' "$DEPLOY")"
		deadlock_retries
	)
}

# THE ARM THAT MUST FAIL, first, so a check that cannot catch it refuses.
if out=$(probe '{"node":"n","version":"0.7.0"}'); then
	die "a node with no deadlock_retries field answered [$out] instead of refusing.
A pre-25921f6 binary and a binary that aborted no reads would then look identical,
and a deploy would report a clean migration from a node that never said so."
fi

if out=$(probe ''); then
	die "an empty answer - a node that refused or timed out - was read as [$out].
Unreachable is not zero either."
fi

# AND THE ARMS THAT MUST NOT, because a check that refuses everything is not
# measuring the distinction, it is just refusing.
if ! out=$(probe '{"node":"n","deadlock_retries":0,"version":"0.8.0"}') || [ "$out" != 0 ]; then
	die "a genuine zero was refused or misread as [${out:-}] - the distinction has
collapsed the other way, and a clean deploy can no longer report itself clean."
fi

if ! out=$(probe '{"node":"n","deadlock_retries":42}') || [ "$out" != 42 ]; then
	die "a real count came back as [${out:-}], want 42"
fi

printf 'absent-is-not-zero: absent, empty, zero and a real count are four answers\n'
