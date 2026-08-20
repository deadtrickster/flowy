#!/usr/bin/env bash
#
# Refuse a push that would publish a secret.
#
# WHY IT EXISTS. The operator, 2026-08-20: "write another gate for the push -
# the same scanner you used". db7ec6b reached github that evening only because a
# person remembered to scan by hand, and the FIRST scan that person ran was
# wrong - it covered HEAD and not history, so a token in a commit from six hours
# earlier would have gone out with a clean report attached. Another seat caught
# it by reading over their shoulder. That is not a mechanism, and the fix is not
# to remember harder: it is to put the scan where the push is, so a tired person
# at 19:00 and an unattended drainer get the same answer.
#
# WHAT IT SCANS, and this is the part three seats argued to the same place:
#
#   old..new    the ordinary push. Git already holds the watermark - `old` is
#               the sha the remote has - so nothing already published is ever
#               re-read. The operator's objection, "it will simply stale soon",
#               is answered by git rather than by a table.
#   everything  when `old` is all-zeros. The remote does not have this ref, so
#               there is no range to take and every reachable commit is new
#               over there. This is the FIRST PUSH - the one that makes a repo
#               public - and a range-only implementation passes it silently,
#               which is the exact push that needed a scanner today.
#   nothing     when `new` is all-zeros. That is a deletion; it publishes no
#               content.
#
# ADDED LINES, NOT FILES. A scan of the files a commit touched reports secrets
# that were already there, so the second person to edit a file inherits the
# first one's debt and the gate gets bypassed within the day - the pre-commit
# hook next door has the same reasoning written on it in more detail. Added
# lines are what this push introduces, and a secret added and then removed
# inside the range is still an added line, still in history, still refused.
#
# THE SAME CODE IN BOTH PLACES. A hook protects whoever runs git; the drainer's
# push path calls this with --range and gets the same answer from the same
# patterns. Two spellings of one check is the bug this fleet has already paid
# for once, when "is this row admissible" had four implementations and two of
# them disagreed.
#
# THE BYPASS IS VISIBLE, WHICH IS THE MOST THIS CAN DO. `git push --no-verify`
# skips every pre-push hook and git writes nothing down about it. So the hook is
# the floor and not the ceiling: the automated path must call this script
# directly, where --no-verify does not exist, and FLOWY_PUSH_SCAN_SKIP=1 is
# offered as the honest escape - it announces itself on stderr and names who
# used it, instead of the silent one git already gives everybody.

set -uo pipefail

zero=0000000000000000000000000000000000000000

say() { printf 'pre-push: %s\n' "$*" >&2; }

# THE PATTERNS, each named, because a refusal that does not say WHICH shape it
# matched cannot be argued with and gets bypassed instead of fixed.
#
# flowy-token is first and is the one this fleet actually leaks. The format is
# store/mint.go's own: "t" + the handle lowercased with every non-alphanumeric
# turned into a dash + "-" + a ULID. The ULID's alphabet is Crockford base32
# with I, L, O and U removed, and it is exactly 26 characters - which is what
# makes this pattern precise enough to refuse on rather than warn on.
patterns=(
	'flowy-token|t[a-z0-9-]+-[0-9A-HJKMNP-TV-Z]{26}'
	'private-key|-----BEGIN [A-Z ]*PRIVATE KEY-----'
	'github-pat|gh[pousr]_[A-Za-z0-9]{36}'
	'aws-key|AKIA[0-9A-Z]{16}'
)

# assigned-secret is the fifth shape and it is JUDGED rather than matched, which
# is a decision with a number behind it. The obvious regex -
# (password|secret|token|api_key) = "twelve or more characters" - was run over
# all 527 commits of this repository and returned TEN hits, ALL TEN OF THEM
# WRONG: prose in a ternary, a test fixture reading "correct horse battery", a
# shell variable holding "${FLOWY_TOKEN:-}", and four lines of a vendored
# minified drawio bundle. Zero true positives.
#
# A gate that is wrong ten times out of ten does not get fixed, it gets skipped,
# and then the four precise patterns above stop running too. So the name on the
# left of the assignment only nominates a line; what decides is the VALUE:
#
#   no whitespace   "correct horse battery" is a sentence. Secrets are one word.
#   has a digit     kills a-token-this-runner-does-not-know and ${FLOWY_TOKEN:-}
#   has a capital   kills the same class again from the other side
#   16 or more      shorter than that is a placeholder or a field name
#   not $-shaped    ${VAR} and $VAR are references TO a secret, not a secret
#   line under 200  a minified bundle is one enormous line and is all coincidence
#
# Those six predicates take the same 527 commits to zero. Stated plainly because
# it is a real hole and not a solved problem: a lowercase-only secret with no
# digits passes this, and so does one assigned to a variable named none of those
# words. This pattern is the backstop, not the fence.

usage() {
	printf 'usage: %s                     as a git pre-push hook, reading refs on stdin\n' "$0" >&2
	printf '       %s --range OLD..NEW    scan one range, for the drainer and for a person\n' "$0" >&2
	printf '       %s --commits SHA...    scan named commits\n' "$0" >&2
	exit 2
}

# scan_commits reads commit shas on stdin and prints one line per hit:
# "<sha> <file>:<line> <pattern-name> <the line, trimmed>".
#
# The patch is read with -U0 so there are no context lines to mistake for added
# ones, and --no-color and --no-ext-diff so a person's diff configuration cannot
# change what the gate sees. Binary files produce no + lines and are skipped by
# the shape of the parse rather than by a special case.
scan_commits() {
	local sha
	while read -r sha; do
		[ -n "$sha" ] || continue
		git show --format='%H' --no-color --no-ext-diff -U0 "$sha" |
			awk -v sha="$sha" -v pats="$(printf '%s\n' "${patterns[@]}")" '
			BEGIN {
				n = split(pats, rows, "\n")
				for (i = 1; i <= n; i++) {
					if (rows[i] == "") continue
					p = index(rows[i], "|")
					names[i] = substr(rows[i], 1, p - 1)
					res[i] = substr(rows[i], p + 1)
				}
			}
			/^\+\+\+ /		{ file = substr($0, 7); next }
			/^--- /			{ next }
			/^@@ /			{
				# @@ -a,b +c,d @@ - c is the first added line number.
				split($3, h, ",")
				line = substr(h[1], 2) + 0
				next
			}
			/^\+/ {
				text = substr($0, 2)
				hit = ""
				for (i = 1; i <= n; i++) {
					if (res[i] == "") continue
					if (text ~ res[i]) { hit = names[i]; break }
				}
				if (hit == "") hit = assigned_secret(text)
				if (hit != "")
					printf "%s %s:%d %s %s\n", sha, file, line, hit, substr(text, 1, 120)
				line++
			}
			# The judged pattern. Returns its name on a hit and "" otherwise,
			# so the caller above cannot tell it from a matched one.
			function assigned_secret(t,   v, val) {
				if (length(t) > 200) return ""
				if (!match(t, /(password|passwd|secret|token|api[_-]?key)["'"'"']?[[:space:]]*[:=][[:space:]]*["'"'"'][^"'"'"']{16,}["'"'"']/)) return ""
				v = substr(t, RSTART, RLENGTH)
				if (!match(v, /["'"'"'][^"'"'"']{16,}["'"'"']$/)) return ""
				val = substr(v, RSTART + 1, RLENGTH - 2)
				if (val ~ /[[:space:]]/) return ""
				if (val !~ /[0-9]/) return ""
				if (val !~ /[A-Z]/) return ""
				if (val ~ /\$/) return ""
				return "assigned-secret"
			}
		'
	done
}

# ---------------------------------------------------------------- what to scan
mode=hook
range=""
remote_name=""
commits=()
case "${1:-}" in
--range)
	mode=range
	range=${2:-}
	[ -n "$range" ] || usage
	;;
--commits)
	mode=commits
	shift
	commits=("$@")
	[ ${#commits[@]} -gt 0 ] || usage
	;;
--help | -h) usage ;;
"") ;;
-*) usage ;;
*)
	# GIT HANDS THE HOOK TWO ARGUMENTS - the remote's name and its URL - and
	# puts the refs on stdin. Rejecting them as unknown is what this script did
	# for its first half hour: every one of the four checks that call the script
	# directly passed, and a real `git push` died on "usage:" before a single
	# commit was read. A guard that refuses every push is not a strict guard, it
	# is an uninstalled one, because the first thing anybody does is take it out.
	#
	# The name is deliberately not used to pick a remote. What is being pushed
	# is on stdin, and reading it from anywhere else is how a scan ends up
	# describing a different push than the one happening.
	remote_name=${1:-}
	;;
esac

# THE SKIP IS ANNOUNCED. git's own --no-verify is silent and this one is not,
# which is the whole difference: a bypass nobody can see is indistinguishable
# from a scan that found nothing.
if [ "${FLOWY_PUSH_SCAN_SKIP:-0}" = 1 ]; then
	say "SKIPPED by FLOWY_PUSH_SCAN_SKIP=1, by ${USER:-somebody} on $(hostname). Nothing was scanned."
	exit 0
fi

git rev-parse --git-dir >/dev/null 2>&1 || {
	say "not a git checkout - nothing to scan"
	exit 2
}

hits=""
scanned=0

scan_range() { # first-arg: a rev-list argument list, already split by the caller
	local out
	out=$(git rev-list "$@" 2>/dev/null | scan_commits)
	scanned=$((scanned + 1))
	[ -z "$out" ] || hits="$hits$out"$'\n'
}

case "$mode" in
range) scan_range "$range" ;;
commits) scan_range --no-walk "${commits[@]}" ;;
hook)
	# git hands the hook one line per ref: <local ref> <local sha> <remote ref>
	# <remote sha>. Reading it is the only way to know what is actually being
	# published - the branch that is checked out is not necessarily the one
	# being pushed.
	while read -r _lref lsha _rref rsha; do
		[ -n "$lsha" ] || continue
		if [ "$lsha" = "$zero" ]; then
			continue # a deletion publishes no content
		fi
		if [ "$rsha" = "$zero" ]; then
			# The remote has never seen this ref. --not --remotes keeps this
			# from re-reading commits some OTHER branch already published, so
			# the first push of a second branch is still incremental.
			scan_range "$lsha" --not --remotes
		else
			scan_range "$rsha..$lsha"
		fi
	done
	;;
esac

# A SCAN THAT MEASURED NOTHING IS NOT A PASS. An empty stdin - which is what a
# hook sees when it is wired up wrong - would otherwise exit 0 and read exactly
# like a clean push, which is the failure this whole script exists to end.
if [ "$scanned" -eq 0 ]; then
	say "nothing was scanned - no refs arrived on stdin. This is a broken hook, not a clean push."
	exit 2
fi

if [ -n "$hits" ]; then
	say "REFUSED${remote_name:+ (pushing to $remote_name)}. These added lines look like credentials:"
	printf '%s' "$hits" | while read -r sha loc name text; do
		[ -n "$sha" ] || continue
		printf '           %s %s [%s]\n' "${sha:0:9}" "$loc" "$name" >&2
		printf '             %s\n' "$text" >&2
	done
	say ""
	say "Nothing was pushed. A secret in a commit is published even if a later"
	say "commit removes it, so the fix is to rewrite those commits, not to add"
	say "one on top. If every line above is a false positive, push again with"
	say "FLOWY_PUSH_SCAN_SKIP=1 - which says so on this terminal rather than"
	say "hiding it the way --no-verify does."
	exit 1
fi
exit 0
