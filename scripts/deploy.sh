#!/usr/bin/env bash
# Deploy this checkout to the dogfood node, and PROVE it afterwards.
#
# Every deploy here was done by hand, from memory, by four different agents.
# That is why the same three mistakes kept landing:
#
#   THE CONSOLE IS BAKED IN, NOT SERVED FROM DISK. `go:embed` takes whatever is
#   in web/dist at build time, so `go build` without `npm run build` first ships
#   a binary whose Go half is new and whose console is however old the last
#   build left it. That happened with the attachment card: the route passed
#   every test and the card the person looks at was 35 minutes stale.
#
#   HEALTHZ IS NOT A READ. A missing column served 500s on every chat read for
#   half an hour while /healthz answered 200 the whole time.
#
#   SCHEMA GOES FIRST. schema.sql must be applied BEFORE the new binary starts,
#   or it serves errors for every read against columns that do not exist yet.
#
# So this does the steps in the only order that works, and then checks the
# thing that is actually SERVED rather than the commands that were run. A
# deploy that reports success without fetching the bundle back has not
# established anything: it has only established that `go build` exited 0.
#
#   scripts/deploy.sh [--dry-run]
#
# --dry-run builds and verifies everything it can WITHOUT writing the live
# binary or restarting, which is what an agent gated from touching the running
# node can still usefully do before handing the build over.
#
# Exit 0 deployed and verified, 1 refused or failed, 2 misused.

set -uo pipefail

REPO=${FLOWY_REPO:-$HOME/Projects/flowy}
LIVE_DIR=${FLOWY_LIVE_DIR:-$HOME/Projects/flowy-dogfood}
LIVE_BIN="$LIVE_DIR/flowy"
UNIT=${FLOWY_UNIT:-flowy-dogfood}
URL=${FLOWY_URL:-http://192.168.1.55:8787}
# Which commit is currently deployed, so a schema change between then and now
# can be noticed rather than remembered.
STAMP="$LIVE_DIR/.deployed-commit"

DRY=no
case "${1:-}" in
--dry-run) DRY=yes ;;
"") ;;
*)
	printf 'usage: deploy.sh [--dry-run]\n' >&2
	exit 2
	;;
esac

say() { printf '%s\n' "$*"; }
die() {
	printf 'REFUSED: %s\n' "$*" >&2
	exit 1
}

cd "$REPO" || die "no repo at $REPO"

# ---------------------------------------------------------------- refusals

# A dirty tree is somebody else's work in progress. Four agents share this
# checkout, so deploying it ships whatever anybody happens to be mid-edit on -
# the same reason a spawned VM gates "master plus whoever is typing".
if [ -n "$(git status --porcelain)" ]; then
	git status --short >&2
	die "the tree has uncommitted changes - deploy ships whatever is lying around"
fi

commit=$(git rev-parse --short HEAD)
branch=$(git rev-parse --abbrev-ref HEAD)
[ "$branch" = "master" ] || die "on branch '$branch' - master is the only deploy source"

# ------------------------------------------------------- schema comes first

previous=""
[ -f "$STAMP" ] && previous=$(cat "$STAMP")
if [ -n "$previous" ] && git cat-file -e "$previous^{commit}" 2>/dev/null; then
	if ! git diff --quiet "$previous" HEAD -- schema.sql; then
		say "SCHEMA CHANGED between $previous and $commit:"
		git diff --stat "$previous" HEAD -- schema.sql
		say ""
		say "Apply it to the dogfood database BEFORE restarting, or every read"
		say "returns 500 while healthz keeps answering 200. Then re-run this."
		die "schema.sql changed - apply it first, deliberately"
	fi
else
	say "note: no usable deploy stamp, so a schema change cannot be detected."
	say "      Check schema.sql yourself if this is not a routine deploy."
fi

# ------------------------------------------------------------------- build

say "==> building the console (this is the step that gets skipped)"
(cd web && npm run build) >/dev/null 2>&1 || die "npm run build failed"

# shellcheck disable=SC2012 # vite writes index-<hash>.js and the hash is
# [A-Za-z0-9_-], so these names cannot contain a space or a newline for `ls` to
# mangle - and we want them ordered by mtime, which `find` does not do directly.
bundle=$(basename "$(ls -t web/dist/assets/index-*.js 2>/dev/null | head -1)")
[ -n "$bundle" ] || die "no bundle in web/dist/assets after building"
say "    fresh bundle: $bundle"

say "==> building the binary"
tmp=$(mktemp -t flowy-deploy-XXXXXX) || die "cannot make a temp file"
trap 'rm -f "$tmp"' EXIT
go build -o "$tmp" . || die "go build failed"

# THE CHECK THAT WOULD HAVE CAUGHT IT. Read the asset name back out of the
# binary rather than trusting that the build order was right, because a binary
# built from a stale dist looks identical from the outside.
embedded=$(strings "$tmp" | grep -o 'assets/index-[A-Za-z0-9_-]*\.js' | sort -u | head -1)
say "    binary embeds: ${embedded:-NOTHING}"
[ "$embedded" = "assets/$bundle" ] ||
	die "the binary embeds '${embedded:-nothing}' but the fresh bundle is 'assets/$bundle' - go:embed took a stale dist"

if [ "$DRY" = yes ]; then
	say ""
	say "DRY RUN: built and verified, nothing deployed."
	say "  commit  $commit"
	say "  bundle  $bundle"
	say "  binary  $tmp (kept)"
	trap - EXIT
	exit 0
fi

# ------------------------------------------------------------------ deploy

say "==> installing and restarting"
if [ -f "$LIVE_BIN" ]; then
	cp -f "$LIVE_BIN" "$LIVE_BIN.prev" || die "could not keep a rollback copy"
fi
cp -f "$tmp" "$LIVE_BIN" || die "could not write $LIVE_BIN"
systemctl --user restart "$UNIT" || die "could not restart $UNIT"

# --------------------------------------------------------------- prove it

rollback() {
	printf 'ROLLING BACK to %s.prev\n' "$LIVE_BIN" >&2
	if [ -f "$LIVE_BIN.prev" ]; then
		cp -f "$LIVE_BIN.prev" "$LIVE_BIN" && systemctl --user restart "$UNIT"
	fi
	exit 1
}

say "==> verifying what is SERVED, not what was run"
ok=no
for _ in $(seq 1 15); do
	if [ "$(curl -sS -m 5 -o /dev/null -w '%{http_code}' "$URL/healthz" 2>/dev/null)" = "200" ]; then
		ok=yes
		break
	fi
	sleep 2
done
[ "$ok" = yes ] || rollback
say "    healthz 200"

# A REAL READ, because healthz answers from a handler that touches nothing.
# This is the check that a missing column fails and healthz does not.
token=""
for candidate in "$HOME/.config/flowy/token-flowy" "$HOME/.config/flowy/token"; do
	[ -r "$candidate" ] && {
		token=$(cat "$candidate")
		break
	}
done
if [ -n "$token" ]; then
	code=$(curl -sS -m 10 -o /dev/null -w '%{http_code}' \
		-H "Authorization: Bearer $token" "$URL/api/chat/general?limit=2" 2>/dev/null)
	[ "$code" = "200" ] || {
		printf 'a real read returned %s, not 200 - the node is up and answering errors\n' "$code" >&2
		rollback
	}
	say "    a real read returns 200"
else
	say "    WARNING: no token found, so only healthz was checked."
	say "             healthz stays 200 through exactly the outage this misses."
fi

served=$(curl -sS -m 10 "$URL/" 2>/dev/null | grep -o 'assets/index-[A-Za-z0-9._-]*\.js' | head -1)
say "    served bundle: ${served:-NOTHING}"
[ "$served" = "assets/$bundle" ] || {
	printf 'the node serves %s but this deploy built %s\n' "${served:-nothing}" "assets/$bundle" >&2
	rollback
}

printf '%s\n' "$commit" >"$STAMP"
say ""
say "DEPLOYED AND VERIFIED: $commit serving assets/$bundle"
