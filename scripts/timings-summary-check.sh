#!/usr/bin/env bash
# Can the timings summary kill the run that produced the verdict?
#
#   scripts/timings-summary-check.sh [path/to/run-tests.sh]
#
# MEASURED 2026-08-21 (01M0JV52WR): a full suite ran 757 checks, failed only the
# two known image reds, and then printed no tally at all -
#
#   sort: write failed: 'standard output': Broken pipe
#   sort: write error
#   SUITE-EXIT 2
#
# `sort -rn "$timings" | head -10`. head closes the pipe after ten lines while
# sort is still writing hundreds; sort takes SIGPIPE and pipefail promotes it.
# The run had already measured everything and lost the ability to say so.
#
# Exit 2 with no count is NEITHER of the two answers the drainer reads. That is
# what makes this worth a check rather than a one-line fix: the failure mode is
# a run that is neither green nor red.
#
# IT IS A RACE, so the hazard is not reproducible by running the suite and
# hoping. This drives the lifted function against a timings file big enough that
# the writer is certainly still writing when a reader would close, and it proves
# the hazard is real by running the OLD form against the same file first.
#
# IT LIFTS THE REAL FUNCTION out of the suite rather than restating it, the same
# method only-filter-check and stray-cd-check use, so it cannot pass while the
# file it is about says something else.
set -uo pipefail

src=${1:-run-tests.sh}
if [ ! -r "$src" ]; then
	printf 'timings-summary-check: cannot read %s\n' "$src" >&2
	exit 2
fi
src=$(cd "$(dirname "$src")" && pwd)/$(basename "$src")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# BIG ENOUGH THAT THE WRITER IS STILL WRITING. The measured failure had 757
# lines; this uses five thousand so the race is not a race.
timings=$work/timings
for i in $(seq 1 5000); do
	printf '%s.%s\ta check with a long enough name to fill the pipe buffer %s\n' \
		"$((i % 90))" "$((i % 10))" "$i"
done >"$timings"

if ! grep -q '^timings_summary() {' "$src"; then
	printf 'timings-summary-check: %s has no timings_summary function to lift.\n' "$src" >&2
	printf '       Either it was renamed, or the summary went back to being inline -\n' >&2
	printf '       and inline is how it came to be able to fail the run.\n' >&2
	exit 2
fi

{
	printf 'set -euo pipefail\ntimings=%q\nSECONDS=42\n' "$timings"
	sed -n '/^timings_summary() {/,/^}/p' "$src"
	printf 'timings_summary || true\n'
} >"$work/probe.sh"

out=$work/out
if ! bash "$work/probe.sh" >"$out" 2>&1; then
	printf 'FAIL: the timings summary exited non-zero on a %s-line timings file.\n' 5000 >&2
	printf '      It is a report about a verdict that is already decided and it must not\n' >&2
	printf '      be able to change it.\n' >&2
	indent=$(sed 's/^/        /' "$out")
	printf '%s\n' "$indent" >&2
	exit 1
fi

lines=$(grep -c '^  ' "$out" || true)
if [ "$lines" -lt 10 ]; then
	printf 'FAIL: the summary printed %s indented line(s), want at least the ten slowest.\n' "$lines" >&2
	sed 's/^/        /' "$out" >&2
	exit 1
fi
if [ "$(grep -c 'in 5000 checks' "$out" || true)" -ne 1 ]; then
	printf 'FAIL: the summary did not account for all 5000 checks, so it stopped early.\n' >&2
	sed 's/^/        /' "$out" >&2
	exit 1
fi

# THE NEGATIVE CONTROL, and it is the point of the file: the form this replaced
# must still fail against the same input. Without it this check passes on a
# machine where the race simply does not happen, and reports that as a guard.
{
	printf 'set -euo pipefail\ntimings=%q\n' "$timings"
	# shellcheck disable=SC2016  # deliberately unexpanded - this is the probe's
	# source and $timings is evaluated when the probe runs.
	printf 'sort -rn "$timings" | head -10 | while IFS=$'"'"'\\t'"'"' read -r secs name; do printf '"'"'  %%6ss  %%s\\n'"'"' "$secs" "$name"; done\n'
} >"$work/old.sh"

if bash "$work/old.sh" >/dev/null 2>&1; then
	printf 'timings-summary-check: the OLD piped form survived %s lines on this machine,\n' 5000 >&2
	printf '       so this check cannot tell a fixed summary from a lucky one. Raise the\n' >&2
	printf '       line count or find another way to prove the hazard before trusting it.\n' >&2
	exit 2
fi

printf 'the timings summary cannot fail the run: %s lines in, %s slowest out, and the piped form it replaced still dies on the same input\n' \
	5000 "$lines"
