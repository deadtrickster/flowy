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
#   This script used to REFUSE when it noticed schema.sql had changed and tell
#   you to go and apply it by hand. That is what it was doing on the day the
#   refusal-ledger table landed: the deploy was done without a stamp to compare
#   against, so nothing was noticed, nothing was applied, and every
#   /api/artifacts read was a 500 for four minutes against a database with no
#   `refused_authorship` in it. A step that lives in a document is a step that
#   gets skipped. So it applies the schema itself now, through
#   scripts/migrate.sh, against the DSN the unit will actually open - see the
#   migration section below.
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
# WHAT IS DEPLOYED, as a ref rather than as whatever the checkout happens to
# have out. Everything below is resolved from this once and never re-read.
DEPLOY_REF=${FLOWY_DEPLOY_REF:-master}
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

[ -d "$REPO/.git" ] || [ -f "$REPO/.git" ] || die "no repo at $REPO"

# --------------------------------------------------- the tree that is built
#
# THE SHARED CHECKOUT IS NOT A BUILD DIRECTORY. It used to be: this script cd'd
# into $REPO and refused when `git status` was dirty, because deploying somebody
# else's half-finished edit ships whatever they happen to be typing. The refusal
# was right and the premise was wrong. Four agents edit that checkout, so ONE
# agent mid-edit refused every deploy in the fleet, including the drainer's -
# measured at 20:52 on 18 Aug, from three modified files that belonged to a seat
# who had nothing to do with the deploy that failed. It cleared itself when they
# committed, which is what made it dangerous: intermittent, named after files
# rather than a person, and the deploy it broke was somebody else's.
#
# It is the same lesson the gate learned when it stopped measuring the shared
# checkout and moved to worktrees. A build reads a COMMIT, so it should be given
# one.
#
# DETACHED, NOT ON A BRANCH. A worktree holding a branch locks its owner out of
# their own branch - wt-drain did exactly that to a seat an hour before this was
# written, and the next drain pass skipped their row saying it was checked out
# elsewhere. Detached at a sha pins nothing.
#
# CREATED AND REMOVED IN ONE RUN, with the removal on the exit handler, and the
# path carries the sha so two runs cannot land in one directory. A worktree left
# behind is the same defect one level up: a directory nobody owns, pinning
# something, that the next tool trips over.
# MASTER, OR SOMEBODY SAYING OUT LOUD THAT THEY MEAN OTHERWISE.
#
# The refusal this replaces was "you are on branch X, and master is the only
# deploy source", and it was there because a deploy from a branch ships whatever
# that branch happens to be - a spawned agent could put its own unlanded work on
# the node everyone uses. Reading a commit instead of a checkout removes the
# accident: whatever anybody has checked out, this builds master.
#
# It does not remove the INTENT, which is why this is here. FLOWY_DEPLOY_REF is
# a way to deploy something else on purpose - a rollback to a known sha is the
# case it exists for - and a way to deploy something else on purpose is a way to
# deploy unlanded work. So anything but master needs a second variable, which is
# not something anybody sets by accident and which says in the log what was
# meant.
#
# Asked BEFORE the lock, so the refusal costs no network and cannot be confused
# with "the target is held" - the confusion that made the old check unreadable
# on every drain pass.
if [ "$DEPLOY_REF" != master ] && [ "${FLOWY_DEPLOY_ANY_REF:-no}" != yes ]; then
	die "asked to deploy '$DEPLOY_REF' and master is the only deploy source -
set FLOWY_DEPLOY_ANY_REF=yes to mean it, which is for a rollback to a known sha and
is recorded in this shell rather than in the log"
fi

commit=$(git -C "$REPO" rev-parse --short "$DEPLOY_REF" 2>/dev/null) ||
	die "no ref '$DEPLOY_REF' in $REPO - that is what would be deployed"
full=$(git -C "$REPO" rev-parse "$DEPLOY_REF")
TREE=${FLOWY_DEPLOY_TREE:-$HOME/Projects/flowy-deploy-$commit}

# ---------------------------------------------------------------- the lock

# TWO DEPLOYS AT ONCE ARE TWO BUILDS OVER EACH OTHER.
#
# This runs npm and go build in the shared checkout and then installs the
# binary. Two of them interleave: one is still writing web/dist while the other
# embeds it, or one installs a binary built from the other's half-finished
# bundle. Nothing prevented that, and the only reason it has not happened is
# that one agent has been doing all the deploying.
#
# It is the same exclusion landing takes, so it is the same lock - see
# api_lock.go. A second mechanism would be two locks that cannot see each other,
# which is the shape of the bug rather than the fix.
#
# NO TOKEN IS A REFUSAL, not a warning that scrolls past. A guard you can skip
# by not having a credential is the opt-in guard this fleet spent the morning
# removing: it protects the careful path and leaves the default one as it was.
find_token() {
	local candidate
	# NAMED FIRST, like the URL and the database are. A deploy is run against a
	# node, and which credential it uses is part of that - a script whose only
	# credential is a fixed path in $HOME cannot be exercised against a scratch
	# node without borrowing the operator's own token, so it was not exercised.
	if [ -n "${FLOWY_LOCK_TOKEN:-}" ]; then
		printf '%s\n' "$FLOWY_LOCK_TOKEN"
		return 0
	fi
	# THE SEAT RUNNING THIS, BEFORE THE OPERATOR'S OWN TOKEN.
	#
	# Every deploy until now took the lock with ~/.config/flowy/token, which is
	# the OPERATOR's. So the lock said "held by operator" whoever ran it, and
	# the one question a held lock exists to answer - who is doing this, can I
	# wait or is it mine - had the same answer for every agent on the box.
	# Measured on 2026-08-18: two of us read "held by operator" and neither
	# could tell whether that was a human at the console or a drainer three
	# minutes into a build.
	#
	# $FLOWY_AGENT names the seat, and its token lives beside the others. A
	# human running this by hand has no FLOWY_AGENT and falls through to the
	# operator's token exactly as before.
	if [ -n "${FLOWY_AGENT:-}" ] &&
		[ -r "$HOME/.config/flowy/agents/$FLOWY_AGENT" ]; then
		cat "$HOME/.config/flowy/agents/$FLOWY_AGENT"
		return 0
	fi
	for candidate in "$HOME/.config/flowy/token-flowy" "$HOME/.config/flowy/token"; do
		if [ -r "$candidate" ]; then
			cat "$candidate"
			return 0
		fi
	done
	return 1
}

# lock_holder_name is which credential this deploy will hold the lock under, for
# saying out loud rather than for deciding anything.
#
# IT IS SAID BEFORE THE LOCK IS TAKEN, because the failure it addresses is an
# identity nobody notices until somebody else reads the lock and cannot act on
# it. A deploy that announces "as claude-host" is one where a wrong seat is
# visible at the moment it happens rather than fifteen minutes later in a
# refusal somebody else is reading.
lock_holder_name() {
	if [ -n "${FLOWY_LOCK_TOKEN:-}" ]; then
		# shellcheck disable=SC2016  # naming the variable, not reading it
		printf 'the token in $FLOWY_LOCK_TOKEN\n'
	elif [ -n "${FLOWY_AGENT:-}" ] && [ -r "$HOME/.config/flowy/agents/$FLOWY_AGENT" ]; then
		printf '%s\n' "$FLOWY_AGENT"
	elif [ -r "$HOME/.config/flowy/token-flowy" ]; then
		printf 'the operator (token-flowy)\n'
	else
		printf 'the operator (token)\n'
	fi
}

lock_token=$(find_token) || die "no token in ~/.config/flowy/agents/\$FLOWY_AGENT,
~/.config/flowy/token-flowy or ~/.config/flowy/token - deploy takes a lock, and it
cannot ask for one without a credential"
say "    holding the deploy lock as $(lock_holder_name)"

# A DIFFERENT RESOURCE, SO A DIFFERENT TARGET.
#
# This took "master", the landing lock, and the first time that was exercised it
# refused a deploy because somebody was GATING: "the target is held by
# flowy-claude until 20:02 (14m40s left)". Master was two landings ahead of the
# node and stayed there for fifteen minutes, including a fix the operator was
# waiting on.
#
# Landing needs exclusion on the TARGET BRANCH - one writer moving master. A
# deploy needs exclusion on the CHECKOUT AND THE UNIT - one npm build, one
# binary install, one restart. Those are different resources and they never
# conflict, so putting both on one string bought nothing and cost a deploy per
# gate.
#
# Same table, same holder rules, same expiry - only the name of the thing being
# held is honest now. Two deploys still exclude each other, which is the whole
# reason this lock exists.
lock_target=deploy
# THE COMMIT BEING DEPLOYED, not whatever the shared checkout has out. Deploy
# resolves a ref and builds it in its own worktree, so HEAD here names a tree
# nobody is deploying - and the lock's item is what another agent reads to see
# what is going on.
lock_item="deploy $commit"
lock_body=$(printf '{"target":"%s","item":"%s"}' "$lock_target" "$lock_item")
lock_answer=$(curl -sS -m 10 -w '\n%{http_code}' -X POST \
	-H "Authorization: Bearer $lock_token" -H 'Content-Type: application/json' \
	-d "$lock_body" "$URL/api/lock" 2>/dev/null) || die "could not reach $URL to take the lock"
lock_code=$(printf '%s' "$lock_answer" | tail -1)
case "$lock_code" in
200) : ;;
404)
	# A NODE THAT PREDATES THE DOOR, which is the node this change is deployed
	# TO the first time. Refusing here would make the deploy that installs the
	# lock impossible, so this says so loudly and goes on. It is a bootstrap,
	# not an opt-out: every node deployed after this one answers the route, and
	# a 404 from a node that should have it is a sentence in the log rather
	# than a silence.
	say "    NO LOCK: this node has no /api/lock, so nothing stops a second deploy."
	say "    That is expected exactly once - the deploy that installs the door."
	lock_body=""
	;;
409)
	printf '%s\n' "$lock_answer" | head -n -1 >&2
	die "another deploy holds this node - wait for it, do not race. A GATE does not block a deploy"
	;;
*)
	printf '%s\n' "$lock_answer" | head -n -1 >&2
	die "taking the lock answered $lock_code"
	;;
esac

# GIVEN BACK ON EVERY EXIT, including a die four steps down. A deploy that
# fails holding the lock freezes landing for the full expiry, and the person
# who has to wait is not the one who broke it.
release_lock() {
	# Nothing to give back when the node had no door to take it from.
	[ -n "$lock_body" ] || return 0
	curl -sS -m 10 -o /dev/null -X POST \
		-H "Authorization: Bearer $lock_token" -H 'Content-Type: application/json' \
		-d "$lock_body" "$URL/api/lock/release" 2>/dev/null || true
}

# ONE EXIT HANDLER, BECAUSE BASH HAS ONLY ONE.
#
# The first cut of this set `trap release_lock EXIT` here and a later step set
# `trap 'rm -f "$tmp"' EXIT` for its temp file. The second REPLACED the first -
# a later trap on the same signal is not an addition - so the release never ran,
# and a `trap - EXIT` in the dry-run path cleared what was left.
#
# Measured, on the deploy of 227d627: the lock was taken at 18:29, the script
# exited 0 at 18:29, and at 18:30 the queue still read "held by operator for
# deploy 227d627 until 18:44". Every landing in that window would have been
# refused for a deploy that had finished.
#
# So there is one handler and everything that must happen on exit goes in it.
# on_exit is defined before anything registers work with it, and $tmp is empty
# until the build step makes one.
tmp=""
tree_made=""
on_exit() {
	release_lock
	[ -n "$tmp" ] && rm -f "$tmp"
	# THE WORKTREE GOES BACK, always, including on a die four steps down. One
	# left behind is a directory nobody owns that the next tool trips over -
	# and `git worktree list` on this repository is already long enough that
	# nobody reads it.
	if [ -n "$tree_made" ]; then
		git -C "$REPO" worktree remove --force "$TREE" >/dev/null 2>&1 ||
			rm -rf "$TREE"
		git -C "$REPO" worktree prune >/dev/null 2>&1 || true
	fi
	return 0
}
trap on_exit EXIT
say "    took the landing lock for \"$lock_item\""

# THE TREE, now that the lock says nobody else is building.
[ -e "$TREE" ] && die "$TREE is already there - a deploy of $commit is either running or left it behind"
git -C "$REPO" worktree add --detach --quiet "$TREE" "$full" ||
	die "could not check $commit out into $TREE"
tree_made=yes
cd "$TREE" || die "could not enter $TREE"
say "    building $commit in $TREE, detached, and it is removed on exit"

# BY CONSTRUCTION, not by hope. A fresh worktree at a commit cannot be dirty,
# so this says nothing on any ordinary run - it is here because the whole point
# of the change is WHICH tree gets built, and a check that never fires is the
# cheapest way to notice the day that stops being true.
if [ -n "$(git status --porcelain)" ]; then
	git status --short >&2
	die "the fresh worktree is dirty, which should be impossible"
fi

# ------------------------------------------------------- schema comes first

previous=""
[ -f "$STAMP" ] && previous=$(cat "$STAMP")
if [ -n "$previous" ] && git cat-file -e "$previous^{commit}" 2>/dev/null; then
	if ! git diff --quiet "$previous" HEAD -- schema.sql; then
		say "SCHEMA CHANGED between $previous and $commit:"
		git diff --stat "$previous" HEAD -- schema.sql
	else
		say "schema.sql unchanged since $previous"
	fi
else
	say "note: no usable deploy stamp, so this cannot say whether schema.sql moved."
fi
# Either way the migration below runs. The stamp comparison is a report, not a
# gate: it was the gate, and the deploy that took the node down had no stamp to
# compare against, so it reported nothing and applied nothing. What the database
# actually holds is a question for the database, and scripts/migrate.sh asks it.

# WHICH DATABASE. It has to be the one the unit will open, not one this script
# guessed, or the migration lands somewhere the node never looks and the deploy
# reports success. The unit's own environment file is therefore the first place
# asked - that file IS what the node connects to.
find_dsn() {
	local line value candidate
	if [ -n "${FLOWY_DATABASE_URL:-}" ]; then
		printf '%s\n' "$FLOWY_DATABASE_URL"
		return 0
	fi
	for candidate in "$LIVE_DIR/serve.env" "$LIVE_DIR/.env"; do
		[ -r "$candidate" ] || continue
		line=$(grep -m1 -E '^[[:space:]]*(export[[:space:]]+)?DATABASE_URL=' "$candidate") || continue
		value=${line#*=}
		# systemd EnvironmentFile permits either quote around the value.
		value=${value#\"}
		value=${value%\"}
		value=${value#\'}
		value=${value%\'}
		[ -n "$value" ] && {
			printf '%s\n' "$value"
			return 0
		}
	done
	if [ -r "$LIVE_DIR/PG_DSN" ]; then
		value=$(tr -d '\r' <"$LIVE_DIR/PG_DSN" | head -1)
		[ -n "$value" ] && {
			printf '%s\n' "$value"
			return 0
		}
	fi
	return 1
}

dsn=$(find_dsn) || die "no database URL: set FLOWY_DATABASE_URL, or put DATABASE_URL= in $LIVE_DIR/serve.env, or a DSN in $LIVE_DIR/PG_DSN - a deploy that cannot reach the database cannot migrate it, and restarting onto an unmigrated schema is the outage this script exists to stop"
# Print the host and database, never the DSN: it can carry a password, and this
# script's output goes into logs and into chat.
say "database: $(printf '%s' "$dsn" | sed -E 's#^[^:]*://([^@/]*@)?#\1#; s#^[^@]*@##; s#\?.*$##')"

# ------------------------------------------------------------------- build

# THE MODULES ARE THIS TREE'S OWN. A fresh worktree has no node_modules, and
# the one thing that must not happen is borrowing another worktree's: eight
# symlinks into one directory cost this fleet an hour when somebody emptied it.
# `npm ci` from the lockfile, against the shared npm CACHE, which is what that
# cache is for.
say "==> installing the console's modules (a fresh tree has none)"
(cd web && npm ci --prefer-offline --no-audit --no-fund) >/dev/null 2>&1 ||
	die "npm ci failed in $TREE/web"

say "==> building the console (this is the step that gets skipped)"
(cd web && npm run build) >/dev/null 2>&1 || die "npm run build failed"

# shellcheck disable=SC2012 # vite writes index-<hash>.js and the hash is
# [A-Za-z0-9_-], so these names cannot contain a space or a newline for `ls` to
# mangle - and we want them ordered by mtime, which `find` does not do directly.
bundle=$(basename "$(ls -t web/dist/assets/index-*.js 2>/dev/null | head -1)")
[ -n "$bundle" ] || die "no bundle in web/dist/assets after building"
say "    fresh bundle: $bundle"

# YESTERDAY'S DRY RUNS GO, because a kept file with no expiry is a leak with a
# good reason.
#
# The binary below is kept on purpose - see the dry-run branch, where clearing
# the whole EXIT trap once stranded the landing lock, so emptying $tmp is the
# cheaper of two bad options. What was missing is the other half: nobody is
# coming back for a 42MB build they asked for an hour ago.
#
# Measured 2026-08-19: nine of them in /tmp, 16 to 42MB each, oldest from
# 2026-08-17, on a tmpfs sitting at 82% of its inodes - the one that reached
# 100% three times in twelve hours and took every VM with it.
#
# So a deploy sweeps the ones older than a day at the start of its own run. Not
# on exit, because the file this run is about to keep is the one somebody is
# still looking at; and not a fixed count, because the question is whether
# anybody could still want it, and a day answers that.
find "${TMPDIR:-/tmp}" -maxdepth 1 -name 'flowy-deploy-*' -type f -mtime +0 \
	-delete 2>/dev/null || true
say "==> building the binary"
tmp=$(mktemp -t flowy-deploy-XXXXXX) || die "cannot make a temp file"
# STAMP THE COMMIT IN. main.go has carried a buildStamp ldflag for exactly this
# since it was written - "a version that is only the release is a version that
# cannot answer the question" - and nothing ever set it, so every node has
# reported a bare release and no way to tell which commit it is running. Today
# that question came up repeatedly and the only answers were shell access to
# the host or comparing bundle hashes by hand.
go build -ldflags "-X main.buildStamp=$commit" -o "$tmp" . || die "go build failed"

# THE CHECK THAT WOULD HAVE CAUGHT IT. Read the asset name back out of the
# binary rather than trusting that the build order was right, because a binary
# built from a stale dist looks identical from the outside.
embedded=$(strings "$tmp" | grep -o 'assets/index-[A-Za-z0-9_-]*\.js' | sort -u | head -1)
say "    binary embeds: ${embedded:-NOTHING}"
[ "$embedded" = "assets/$bundle" ] ||
	die "the binary embeds '${embedded:-nothing}' but the fresh bundle is 'assets/$bundle' - go:embed took a stale dist"

if [ "$DRY" = yes ]; then
	say ""
	say "DRY RUN: built and verified, nothing deployed and NOTHING MIGRATED."
	say "  commit  $commit"
	say "  bundle  $bundle"
	say "  binary  $tmp (kept, and swept by the next deploy after a day)"
	# The binary is kept on purpose, and the lock is NOT: clearing the whole
	# EXIT trap here is what used to strand it. Emptying $tmp keeps the file
	# and leaves the release to run.
	tmp=""
	exit 0
fi

# ----------------------------------------------------------------- migrate
#
# Here, and not earlier: a failed build must not leave a migrated database
# behind. The two orderings are not symmetric. An OLD binary on a NEW schema is
# fine - every change in schema.sql is additive, so the old binary simply does
# not read the new column. A NEW binary on an OLD schema is the four minutes of
# 500s. So the schema goes first within the deploy, and the deploy only starts
# once there is a verified binary to install.

# From the TREE, not from $REPO: the schema that is applied has to be the one
# the binary was built against, and $REPO's copy is whatever anybody has edited
# into it since.
if ! "$TREE/scripts/migrate.sh" "$dsn"; then
	die "the migration failed - nothing was installed and the node is still serving the old binary"
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
token=$(find_token)
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

# And ask the node WHICH COMMIT IT THINKS IT IS. The stamp file above records
# what this script deployed; this reads back what the running binary says, so
# the two can disagree out loud instead of silently. Answering "is the node on
# master?" should not require shell access to the host.
if [ -n "$token" ]; then
	reported=$(curl -sS -m 10 -H "Authorization: Bearer $token" "$URL/api/node" 2>/dev/null |
		grep -o '"version":"[^"]*"' | head -1 | cut -d'"' -f4)
	say "    node reports version: ${reported:-unknown}"
	case "$reported" in
	*"+$commit") : ;;
	*)
		printf 'the node reports version "%s", which does not carry this deploy\x27s commit %s\n' \
			"${reported:-unknown}" "$commit" >&2
		rollback
		;;
	esac
fi

printf '%s\n' "$commit" >"$STAMP"
say ""
say "DEPLOYED AND VERIFIED: $commit serving assets/$bundle"
