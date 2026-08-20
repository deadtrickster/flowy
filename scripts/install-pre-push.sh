#!/usr/bin/env bash
#
# Put the push scan into a checkout's hooks, or say what is already there.
#
#   scripts/install-pre-push.sh [CHECKOUT]   install into CHECKOUT (default: this one)
#   scripts/install-pre-push.sh --check      say whether it is installed, change nothing
#
# LANDED IS NOT RUNNING. Hooks are not version-controlled, so scripts/pre-push.sh
# can be on master and absent from every checkout that pushes - which is the gap
# install-pre-commit.sh next door was written for, in the same week, for the same
# reason. This one matters more: the thing pre-commit stops is an unformatted
# commit, and the thing this stops is a credential on the public internet.
#
# Installing into a WORKTREE installs into the shared .git, because git keeps
# hooks per repository and not per worktree. With ninety worktrees on this
# machine that is the behaviour we want: one guard for the checkout they share.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
guard="$here/pre-push.sh"

check=false
target="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
case "${1:-}" in
--check) check=true ;;
"") ;;
*) target="$1" ;;
esac

hooks="$(git -C "$target" rev-parse --git-common-dir 2>/dev/null || true)"
[ -n "$hooks" ] || {
	printf '%s is not a git checkout\n' "$target" >&2
	exit 2
}
case "$hooks" in
/*) ;;
*) hooks="$target/$hooks" ;;
esac
hooks="$hooks/hooks"
hook="$hooks/pre-push"

if $check; then
	if [ -x "$hook" ] && grep -q 'pre-push.sh' "$hook" 2>/dev/null; then
		printf 'the push scan is installed at %s\n' "$hook"
		exit 0
	fi
	# NOT INSTALLED IS A FAILURE, not a note - the same rule as the pre-commit
	# installer, and for a louder reason. A repository with no push scan looks
	# exactly like one whose push scan found nothing.
	printf 'THE PUSH SCAN IS NOT INSTALLED at %s - nothing stops a\n' "$hook" >&2
	printf 'credential reaching the remote except somebody remembering to look\n' >&2
	exit 1
fi

[ -x "$guard" ] || {
	printf '%s is missing or not executable\n' "$guard" >&2
	exit 2
}
# NOT OVER SOMEBODY ELSE'S HOOK. Read the target before writing to it.
if [ -e "$hook" ] && ! grep -q 'pre-push.sh' "$hook" 2>/dev/null; then
	printf 'there is already a pre-push hook at %s and it is not this one:\n\n' "$hook" >&2
	sed 's/^/    /' "$hook" >&2
	printf '\nnot overwritten. Move it aside, or call this from it - the shim is two lines:\n' >&2
	printf '    exec %s "$@"\n' "$guard" >&2
	exit 2
fi

mkdir -p "$hooks"

# A SHIM AND NOT A COPY, so the guard tracks the repository rather than being a
# snapshot of whatever was true the day somebody ran this. Stdin passes through
# `exec` untouched, which is what the hook is actually given: one line per ref.
cat >"$hook" <<EOF
#!/usr/bin/env bash
# Installed by scripts/install-pre-push.sh - edit that, not this.
exec "$guard" "\$@"
EOF
chmod +x "$hook"
printf 'pre-push installed: %s -> %s\n' "$hook" "$guard"
