#!/usr/bin/env bash
#
# Put the land guard into a checkout's hooks, or say what is already there.
#
#   scripts/install-land-guard.sh [CHECKOUT]   install into CHECKOUT (default: this one)
#   scripts/install-land-guard.sh --check      say whether it is installed, change nothing
#
# A hook is not version-controlled, which is the whole reason this script
# exists: the guard can land in the repository and still be absent from the one
# tree that matters. That gap has a name here - LANDED IS NOT RUNNING - and it
# cost the fleet a morning in three separate places today.
#
# Installing into a WORKTREE installs into the shared .git, because git keeps
# hooks per repository and not per worktree. That is the behaviour we want:
# there is one shared checkout and the guard belongs to it.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
guard="$here/land-guard.sh"

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
hook="$hooks/reference-transaction"

if $check; then
	if [ -x "$hook" ] && grep -q 'land-guard' "$hook" 2>/dev/null; then
		printf 'the land guard is installed at %s\n' "$hook"
		exit 0
	fi
	# NOT INSTALLED IS A FAILURE, not a note. This is the check a suite runs,
	# and "the guard is absent" answered with exit 0 is the silent success it
	# was written to stop.
	printf 'THE LAND GUARD IS NOT INSTALLED at %s - master can be moved by anybody\n' "$hook" >&2
	exit 1
fi

[ -x "$guard" ] || {
	printf '%s is missing or not executable\n' "$guard" >&2
	exit 2
}
mkdir -p "$hooks"

# A SHIM AND NOT A COPY. A copied hook is a snapshot that stops tracking the
# repository the moment either changes, and then the tree is guarded by
# whatever was true on the day somebody ran this.
cat >"$hook" <<EOF
#!/usr/bin/env bash
# Installed by scripts/install-land-guard.sh - edit that, not this.
exec "$guard" "\$@"
EOF
chmod +x "$hook"
printf 'land guard installed: %s -> %s\n' "$hook" "$guard"
