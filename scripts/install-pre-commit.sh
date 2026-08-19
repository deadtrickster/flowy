#!/usr/bin/env bash
#
# Put the pre-commit check into a checkout's hooks, or say what is already there.
#
#   scripts/install-pre-commit.sh [CHECKOUT]   install into CHECKOUT (default: this one)
#   scripts/install-pre-commit.sh --check      say whether it is installed, change nothing
#
# A hook is not version-controlled, which is the whole reason this script
# exists: the check can land in the repository and still be absent from the one
# tree that matters. That gap has a name here - LANDED IS NOT RUNNING - and it
# cost the fleet a morning in three separate places today.
#
# Installing into a WORKTREE installs into the shared .git, because git keeps
# hooks per repository and not per worktree. That is the behaviour we want:
# there is one shared checkout and the guard belongs to it.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
guard="$here/pre-commit.sh"

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
hook="$hooks/pre-commit"

if $check; then
	if [ -x "$hook" ] && grep -q 'pre-commit.sh' "$hook" 2>/dev/null; then
		printf 'the pre-commit check is installed at %s\n' "$hook"
		exit 0
	fi
	# NOT INSTALLED IS A FAILURE, not a note. This is the check a suite runs,
	# and "it is absent" answered with exit 0 is the silent success this whole
	# file was written to stop.
	printf 'THE PRE-COMMIT CHECK IS NOT INSTALLED at %s - gofmt, vet and build\n' "$hook" >&2
	printf 'are a habit here rather than a mechanism until it is\n' >&2
	exit 1
fi

[ -x "$guard" ] || {
	printf '%s is missing or not executable\n' "$guard" >&2
	exit 2
}
# NOT OVER SOMEBODY ELSE'S HOOK. `pre-commit` is the most contested name in
# .git/hooks - husky, lefthook, pre-commit.com and half the linters all want it -
# and a script that overwrites one silently is a script that deletes work whose
# author is not here to notice. @orchestrator's arm, and it is the same rule this
# fleet learned three ways today: read the target before writing to it.
if [ -e "$hook" ] && ! grep -q 'pre-commit.sh' "$hook" 2>/dev/null; then
	printf 'there is already a pre-commit hook at %s and it is not this one:\n\n' "$hook" >&2
	sed 's/^/    /' "$hook" >&2
	printf '\nnot overwritten. Move it aside, or call this from it - the shim is two lines:\n' >&2
	printf '    exec %s "$@"\n' "$guard" >&2
	exit 2
fi

mkdir -p "$hooks"

# A SHIM AND NOT A COPY. A copied hook is a snapshot that stops tracking the
# repository the moment either changes, and then the tree is guarded by
# whatever was true on the day somebody ran this.
cat >"$hook" <<EOF
#!/usr/bin/env bash
# Installed by scripts/install-pre-commit.sh - edit that, not this.
exec "$guard" "\$@"
EOF
chmod +x "$hook"
printf 'pre-commit installed: %s -> %s\n' "$hook" "$guard"
