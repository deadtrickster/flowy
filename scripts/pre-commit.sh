#!/usr/bin/env bash
#
# Refuse a commit that does not build, does not vet, or is not formatted.
#
# WHY IT EXISTS. The operator, 2026-08-19: "are you running go vet? add pre
# commit hook to your repos." The honest answer was yes, by hand, every time -
# and that is the shape this fleet spent the day learning is not enough. Three
# of us broke rules we had written that same day, and what worked every time was
# not remembering harder: it was making the wrong thing unavailable.
#
# WHAT IT CHECKS, and each one is here because it has cost a run somewhere:
#
#   gofmt      on the STAGED CONTENT of the .go files in this commit
#   sh         shellcheck and shfmt, on the staged content of the .sh files
#   go build   because a commit that does not compile is not one anybody can
#              bisect through, and the gate says so twenty minutes later
#   go vet     which is seconds and catches a Printf verb that does not match
#
# THE STAGED CONTENT, NOT THE FILE ON DISK, and this is the arm @orchestrator
# had that mine did not. `gofmt -l path` reads the worktree copy. A partially
# staged file - or one fixed after `git add` - then passes a check about
# something that is not being committed. `git show :path` is what is going in.
#
# ON THE FILES IN THE COMMIT, not the tree. A whole-tree gofmt -l walks
# .claude/worktrees and every agent's branch, reporting files that are not in
# this commit and cannot be fixed from here - the gate's own gofmt check has
# that bug today.
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

fail=0
say() { printf 'pre-commit: %s\n' "$*" >&2; }
staged_of() { git diff --cached --name-only --diff-filter=d -- "$1" | grep -v '^vendor/' || true; }

# ---------------------------------------------------------------------- go fmt
mapfile -t gofiles < <(staged_of '*.go')
if [ ${#gofiles[@]} -gt 0 ]; then
	bad=()
	for f in "${gofiles[@]}"; do
		# The staged blob through gofmt, compared with the staged blob. Both
		# sides come from the index, so nothing here is about the worktree.
		if ! diff -q <(git show ":$f" 2>/dev/null) <(git show ":$f" 2>/dev/null | gofmt) >/dev/null 2>&1; then
			bad+=("$f")
		fi
	done
	if [ ${#bad[@]} -gt 0 ]; then
		say "staged and not gofmt'd:"
		printf '%s\n' "${bad[@]}" | sed 's/^/           /' >&2
		say "run: gofmt -w ${bad[*]} && git add ${bad[*]}"
		fail=1
	fi
fi

# ------------------------------------------------------------------ shell fmt
#
# shfmt takes its style from .editorconfig and finds it by FILENAME, so a blob
# on stdin has to be told what it is called - without --filename it formats to
# its own defaults and reports every file in a repo that uses tabs.
mapfile -t shfiles < <(staged_of '*.sh')
if [ ${#shfiles[@]} -gt 0 ] && command -v shellcheck >/dev/null 2>&1; then
	for f in "${shfiles[@]}"; do
		if ! out=$(git show ":$f" 2>/dev/null | shellcheck --format=gcc --shell=bash - 2>&1); then
			say "shellcheck on $f:"
			printf '%s\n' "$out" | head -6 | sed "s|^-:|           $f:|" >&2
			fail=1
		fi
	done
fi
if [ ${#shfiles[@]} -gt 0 ] && command -v shfmt >/dev/null 2>&1; then
	for f in "${shfiles[@]}"; do
		if ! git show ":$f" 2>/dev/null | shfmt -d --filename "$f" >/dev/null 2>&1; then
			say "staged and not shfmt'd: $f"
			say "run: shfmt -w $f && git add $f"
			fail=1
		fi
	done
fi

# ------------------------------------------------------------- build and vet
#
# GUARDED ON THERE BEING A MODULE, which is @orchestrator's arm and the one that
# had bitten them: in a checkout with no go.mod, `go vet ./...` fails for that
# reason alone, and their hook reported it as the FILE being unformatted - a
# passing arm that proved nothing about the code.
if [ ${#gofiles[@]} -gt 0 ] && [ -n "$(go env GOMOD 2>/dev/null)" ] &&
	[ "$(go env GOMOD 2>/dev/null)" != /dev/null ]; then
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
