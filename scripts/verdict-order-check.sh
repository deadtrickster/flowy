#!/usr/bin/env bash
# Does the suite refuse a dirty tree BEFORE it prints a verdict?
#
#   scripts/verdict-order-check.sh [path/to/run-tests.sh]
#
# MEASURED 2026-08-21 on another seat's gate. The suite printed
#
#	passed: 735 failed: 0
#	FAIL: uncommitted tracked changes
#
# and exited 1. Every number on that line is true and the run is a refusal - the
# checks all passed and then the tree they ran against turned out not to be the
# tree on disk. "Had I read the summary line and filed, I would have filed a
# branch whose own gate refused it."
#
# The summary is the line that gets pasted into a row as evidence, and the
# drainer greps `^passed:` for the note it writes on the row - so the ordering
# is not cosmetic: it decides whether a refusal reaches the reader at all.
#
# It drives the real block out of the real file rather than restating it, for
# the reason stray-cd-check gives about check(): a copy here would be a second
# thing to keep in step.
#
# EXIT 2 IS "THIS CHECK DID NOT RUN", exit 1 is "the suite is wrong".
set -uo pipefail

src=${1:-run-tests.sh}
if [ ! -r "$src" ]; then
	printf 'verdict-order-check: cannot read %s\n' "$src" >&2
	exit 2
fi
src=$(cd "$(dirname "$src")" && pwd)/$(basename "$src")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The tail of the suite: the tree check and the summary, in the order they are
# written. If the marker comment is gone the extraction is empty and this says
# so rather than passing on nothing.
sed -n '/^# THE TREE IS CHECKED BEFORE THE VERDICT/,$p' "$src" >"$work/tail.sh"
if ! grep -q 'uncommitted tracked changes' "$work/tail.sh"; then
	printf 'verdict-order-check: could not find the verdict block in %s\n' "$src" >&2
	printf 'It is extracted by its leading comment - if that was reworded, reword this too\n' >&2
	exit 2
fi
# EVERY GUARD THE VERDICT CALLS, not just the first one.
#
# This lifted one function and the verdict grew a second: 2026-08-21 added
# a_filter_that_selected_nothing_is_not_a_pass beside it, and the extracted
# block then ran with that name undefined. Bash returns 127, `if ! <127>` is
# TRUE, and the refusal branch fired - so this check reported "a clean tree was
# refused" and printed the OTHER guard's sentence about a filter matching
# nothing, on a run with no filter at all.
#
# A missing function reading as a failed test is the worst arrangement
# available: the extraction is what broke and the message accuses the suite. So
# the names are a list, each one required, and a guard added to the verdict
# without being added here fails LOUDLY at extraction rather than quietly at run
# time.
: >"$work/defs.sh"
for guard in a_run_that_measured_nothing_is_not_a_pass \
	a_filter_that_selected_nothing_is_not_a_pass; do
	sed -n "/^$guard()/,/^}/p" "$src" >>"$work/defs.sh"
	grep -q "^$guard()" "$work/defs.sh" || {
		printf 'verdict-order-check: %s is not in %s - the verdict calls it and this could not lift it\n' \
			"$guard" "$src" >&2
		exit 2
	}
done

# A REPOSITORY OF ITS OWN, built with `git -c` for every setting the block
# reads. Without that this inherits the caller's git config: the first draft
# hung for two minutes on a gpg pinentry prompt, because commit.gpgsign is on
# for this operator and a temp repo is still a repo.
git() { command git -c commit.gpgsign=false -c user.email=t@t -c user.name=t "$@"; }
cd "$work" || exit 2
git init -q . >/dev/null 2>&1 || exit 2
git commit -q --allow-empty -m "a tree to test against" >/dev/null 2>&1 || exit 2

run() {
	bash -c "passed=735; failed=0; $(cat "$work/defs.sh"); $(cat "$work/tail.sh")" 2>&1
	printf 'exit=%d\n' "$?"
}

clean=$(run)
case "$clean" in
*"exit=0"*) ;;
*)
	printf 'a clean tree was refused:\n%s\n' "$clean" >&2
	exit 1
	;;
esac
case "$clean" in
*"passed: 735 failed: 0"*) ;;
*)
	printf 'a clean run printed no verdict:\n%s\n' "$clean" >&2
	exit 1
	;;
esac
case "$clean" in
*REFUSED*)
	printf 'a clean run called itself refused:\n%s\n' "$clean" >&2
	exit 1
	;;
esac

printf 'tracked\n' >tracked.txt
git add tracked.txt >/dev/null 2>&1
git commit -q -m "something to dirty" >/dev/null 2>&1
printf 'changed after the run\n' >tracked.txt

dirty=$(run)
case "$dirty" in
*"exit=1"*) ;;
*)
	printf 'a dirty tree was not refused:\n%s\n' "$dirty" >&2
	exit 1
	;;
esac

# THE ORDER, which is the whole subject. The refusal has to reach the reader
# BEFORE the number, because the number is what gets pasted.
refusal=$(printf '%s\n' "$dirty" | grep -n 'uncommitted tracked changes' | head -1 | cut -d: -f1)
verdict=$(printf '%s\n' "$dirty" | grep -n '^passed:' | head -1 | cut -d: -f1)
if [ -z "$refusal" ] || [ -z "$verdict" ]; then
	printf 'a refused run printed one of the two lines and not the other:\n%s\n' "$dirty" >&2
	exit 1
fi
if [ "$refusal" -gt "$verdict" ]; then
	printf 'the verdict printed BEFORE the refusal, which is the defect:\n%s\n' "$dirty" >&2
	exit 1
fi

# AND THE COUNTS CARRY THE REFUSAL, so the line cannot be pasted as a pass by
# somebody who read one line and stopped - which is exactly how this was found.
case "$dirty" in
*"passed: 735 failed: 0 - REFUSED"*) ;;
*)
	printf 'the counts on a refused run do not say they were refused:\n%s\n' "$dirty" >&2
	exit 1
	;;
esac

printf 'a clean tree prints its verdict and exits 0; a dirty one is refused first, and its counts say REFUSED\n'
