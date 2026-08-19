#!/usr/bin/env bash
#
# Refuse a commit that does not build, does not vet, or is not gofmt'd.
#
# WHY IT EXISTS. The operator, 2026-08-19: "are you running go vet? add pre
# commit hook to your repos." The honest answer was yes, by hand, every time -
# and that is the shape this fleet spent the day learning is not enough. Three
# of us broke rules we had written that same day, and what worked every time was
# not remembering harder: it was making the wrong thing unavailable.
#
# WHAT IT CHECKS, and each one is here because it has cost a gate run:
#
#   gofmt   on the .go files THIS COMMIT touches. A whole-tree gofmt -l walks
#           .claude/worktrees and every agent's branch, so it reports files that
#           are not in this commit and cannot be fixed from here - the gate's own
#           gofmt check has that bug today.
#   vet     on the module. It is the check that catches a Printf verb that does
#           not match its argument and a lock copied by value, and it is seconds.
#   build   because a commit that does not compile is not a commit anybody can
#           bisect through, and the gate finds it twenty minutes later.
#
# WHAT IT DOES NOT DO: run the suite. That is thirty-five minutes and it belongs
# to the gate; a hook that costs half an hour is a hook everybody passes
# --no-verify to, and then it checks nothing at all.
#
# THE BYPASS IS GIT'S OWN - `git commit --no-verify` - and it is left alone
# deliberately. A guard nobody can get past in a hurry is one that gets
# uninstalled, and this refuses cheap mistakes rather than enforcing policy.
set -uo pipefail

root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$root" || exit 0

# THE FILES IN THIS COMMIT, not the files on disk. --cached is what is staged,
# and `--diff-filter=d` drops deletions: a file being removed has nothing to
# format.
mapfile -t staged < <(git diff --cached --name-only --diff-filter=d -- '*.go' | grep -v '^vendor/' || true)

fail=0
say() { printf 'pre-commit: %s\n' "$*" >&2; }

if [ ${#staged[@]} -gt 0 ]; then
	# Only files that still exist: a rename can stage a path that is gone.
	present=()
	for f in "${staged[@]}"; do
		[ -f "$f" ] && present+=("$f")
	done
	if [ ${#present[@]} -gt 0 ]; then
		unformatted=$(gofmt -l "${present[@]}" 2>/dev/null)
		if [ -n "$unformatted" ]; then
			say "these are staged and not gofmt'd:"
			printf '%s\n' "$unformatted" | sed 's/^/           /' >&2
			say "run: gofmt -w $(printf '%s ' "$unformatted")"
			fail=1
		fi
	fi
fi

# vet and build only when there is Go in the commit at all - a docs-only commit
# should not pay four seconds to be told nothing.
if [ ${#staged[@]} -gt 0 ]; then
	if ! out=$(go build ./... 2>&1); then
		say "it does not build:"
		printf '%s\n' "$out" | head -10 | sed 's/^/           /' >&2
		fail=1
	elif ! out=$(go vet ./... 2>&1); then
		say "go vet says:"
		printf '%s\n' "$out" | head -10 | sed 's/^/           /' >&2
		fail=1
	fi
fi

if [ "$fail" -ne 0 ]; then
	say "nothing was committed. --no-verify skips this if you mean to."
	exit 1
fi
exit 0
