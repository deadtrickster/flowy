#!/usr/bin/env bash
# Does anything in this tree slice a ULID and call the result unique?
#
#   scripts/truncated-ulid-check.sh [root]
#
# A ULID IS A CLOCK FOLLOWED BY RANDOMNESS - ten characters of millisecond
# timestamp, then sixteen of entropy - so a PREFIX contains no entropy at all.
# `ulid.NewString()[:6]` is thirty bits of the clock, which is the same string
# for everybody inside a 2^18 ms window: about four and a half minutes.
#
# Nineteen places in this tree did that to mint "unique" fixture names, and
# 01M0HJ1M25 is what it cost: four store tests that passed on a fresh database
# and went red on the SECOND run against the same one, because the second run
# minted the first run's names. It was explained away as residue twice and sent
# one seat into dev-db.sh for an hour before anybody measured the id.
#
# ulid.Short() returns the random half instead. This check is here so the slice
# cannot come back by being typed again, which is exactly how it spread.
#
# IT PROVES IT CAN FAIL, on every run, against a file written for the purpose.
# A grep guard whose pattern has rotted is indistinguishable from a clean tree -
# the defect class this suite exists to refuse - so the pattern is fired at a
# known offender first, and a check that cannot catch that one refuses instead
# of reporting the tree clean.
set -uo pipefail

root=${1:-.}
if [ ! -d "$root" ]; then
	printf 'truncated-ulid-check: %s is not a directory\n' "$root" >&2
	exit 2
fi

# The pattern, written once and used for both the control and the tree.
pattern='ulid\.(NewString|New)\(\)(\.String\(\))?\[:[0-9]+\]'

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cat >"$work/offender.go" <<'GO'
package fixture

func name() string { return "project-" + ulid.NewString()[:6] }
GO

if ! grep -Eq "$pattern" "$work/offender.go"; then
	printf 'truncated-ulid-check: the pattern no longer matches a known offender.\n' >&2
	printf '  It would report this tree clean whatever the tree contains, so it refuses instead.\n' >&2
	exit 2
fi

found=$(grep -REn --include='*.go' "$pattern" "$root" 2>/dev/null | grep -v '/vendor/')
if [ -n "$found" ]; then
	printf 'truncated-ulid-check: a ULID is sliced for a name, and a prefix is a clock reading:\n\n' >&2
	printf '%s\n' "$found" >&2
	printf '\nUse ulid.Short() - the random half - or the whole id. See internal/ulid/short.go.\n' >&2
	exit 1
fi

printf 'no ULID is sliced for uniqueness\n'
