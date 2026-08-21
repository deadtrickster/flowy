#!/usr/bin/env bash
# Does ONLY= refuse when it selects nothing?
#
#   scripts/only-filter-check.sh [path/to/run-tests.sh]
#
# ONLY= narrows to checks whose NAME contains the string, and preflight() checks
# are exempt so the suite can still build. That combination makes a filter which
# matches NO check look like a small clean run: `743 skipped, passed: 11
# failed: 0`, every one of the eleven a preflight.
#
# MEASURED 2026-08-21: three VM runs reported exactly that for
# ONLY="is no wider than the artifact" - a FUNCTION name rather than a check
# title - and were nearly reported as green. What caught it was somebody
# grepping the log for the check's own sentence and not finding it, which is a
# habit and not a guard.
#
# The verdict already refused a run where NOTHING ran (passed+failed=0). It could
# not see this one, because the preflights make the count positive. So the pair:
#
#   a_run_that_measured_nothing_is_not_a_pass       did anything run
#   a_filter_that_selected_nothing_is_not_a_pass    did anything the FILTER NAMED run
#
# IT LIFTS THE REAL FUNCTIONS out of the suite rather than restating them - the
# same method stray-cd-check uses - so this cannot pass while the file it is
# about says something else. A copy here would be a second definition, which is
# the defect class this suite keeps finding.
set -uo pipefail

src=${1:-run-tests.sh}
if [ ! -r "$src" ]; then
	printf 'only-filter-check: cannot read %s\n' "$src" >&2
	exit 2
fi
src=$(cd "$(dirname "$src")" && pwd)/$(basename "$src")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

build() {
	{
		# shellcheck disable=SC2016  # deliberately unexpanded: this text is the
		# probe's source, and $PWD and ${ONLY:-} must be evaluated when the
		# probe runs rather than when it is written.
		printf 'set -uo pipefail\nSUITE_PWD=$PWD\npassed=0 failed=0 skipped=0 matched=0\nonly=${ONLY:-}\nonly_exempt=no\n'
		sed -n '/^preflight() {/,/^}/p' "$src"
		sed -n '/^check() {/,/^}/p' "$src"
		sed -n '/^a_filter_that_selected_nothing_is_not_a_pass() {/,/^}/p' "$src"
		cat <<'EOF'
yes_() { :; }
preflight "npm ci" yes_
check "a room in the sidebar looks like the rest" yes_
if a_filter_that_selected_nothing_is_not_a_pass "$matched"; then printf 'PASS %s\n' "$matched"; else printf 'REFUSE %s\n' "$matched"; fi
EOF
	} >"$work/probe.sh"
}

build
for want in check preflight verdict; do
	case $want in
	check) grep -q '^check() {' "$work/probe.sh" || {
		printf 'only-filter-check: check() was not lifted out of %s - the extraction is broken, not the guard\n' "$src" >&2
		exit 2
	} ;;
	preflight) grep -q '^preflight() {' "$work/probe.sh" || {
		printf 'only-filter-check: preflight() was not lifted - the extraction is broken\n' >&2
		exit 2
	} ;;
	verdict) grep -q '^a_filter_that_selected_nothing_is_not_a_pass() {' "$work/probe.sh" || {
		printf 'only-filter-check: the verdict guard was not lifted - it may have been renamed or removed\n' >&2
		exit 2
	} ;;
	esac
done

# THE LAST LINE IS THE VERDICT, and taking the whole output was this check's own
# first defect: preflight() prints its own name, so `$miss` began "PASS npm ci"
# and the case below matched on the preflight rather than the answer. The check
# caught it on its first run, which is the only reason this comment exists.
run() { ONLY=${1:-} bash "$work/probe.sh" | tail -1; }

unfiltered=$(run "")
hit=$(run "a room in the sidebar")
miss=$(run "is no wider than the artifact")

bad=0
case $unfiltered in PASS*) ;; *)
	printf 'only-filter-check: an UNFILTERED run was refused (%s) - the guard fires when no filter is set\n' "$unfiltered" >&2
	bad=1
	;;
esac
case $hit in PASS*) ;; *)
	printf 'only-filter-check: a filter that MATCHED a check was refused (%s)\n' "$hit" >&2
	bad=1
	;;
esac
case $miss in REFUSE*) ;; *)
	printf 'only-filter-check: a filter that matched NOTHING was accepted (%s) - a run of preflights would be reported as a pass\n' "$miss" >&2
	bad=1
	;;
esac

[ "$bad" = 0 ] || exit 1
printf 'ONLY= refuses when it selects nothing, and passes when it selects something or is unset\n'
