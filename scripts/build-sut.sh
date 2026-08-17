#!/usr/bin/env bash
# Build one project's system-under-test binary at a given commit, cached by sha.
#
#   build-sut.sh [--project NAME] [--config FILE] <sha>
#       -> prints the path to the cached binary for <sha> on stdout
#
# THE CONTRACT THE RUNNER DEPENDS ON. The last line of stdout is the path to the binary and nothing
# else goes to stdout; every progress line, and the whole compile, goes to stderr so the caller can
# stream it into a run log while still reading the path back. It is idempotent: if the binary for
# <sha> is already cached it prints it and exits without touching Docker at all, which is what makes
# it safe for the runner to call before every repro run rather than only when it thinks a build is
# needed.
#
# WHY IT REUSES THE PROJECT'S OWN TOOLCHAIN IMAGE AND CI SCRIPT rather than reconstructing a build
# environment here. The project already has both, they are what its CI compiles with, and a second
# build recipe living in the repro service is a second thing to keep in step with the first - the day
# they diverge, a repro that "does not reproduce" is really a binary built differently from the one
# the report came from. So this script prepares source and hands it to the project's own build.
#
# TWO LAYERS OF CACHE, because a cold compile of a real database is measured in hours. A persistent
# ccache Docker volume makes an incremental compile across nearby shas fast, and the sha-keyed output
# binary means a commit is compiled once and reused by every repro run that ever names it.
#
# WHAT IS CONFIGURATION AND WHY. This started as a script with SereneDB's source path, toolchain
# image, build command and submodule list written into it. None of that is true of the next project,
# and the repro service is meant to take findings from more than one, so every project-specific value
# is read from a config file (or the environment) and the script itself knows only the shape of the
# work: prepare a clean worktree at a sha, populate submodules from local checkouts, run the
# project's build in its own toolchain container, cache the artifact it produced. The cache and the
# ccache volume are keyed by project as well as by sha, so two projects whose binaries happen to
# share a name cannot serve each other's builds.
set -euo pipefail

usage() {
	cat >&2 <<'EOF'
usage: build-sut.sh [--project NAME] [--config FILE] <sha>

Builds the configured project's binary at <sha> and prints the cached path.

  --project NAME   project to build; its config is $SUT_CONFIG_DIR/NAME.env
                   (default: $SUT_PROJECT)
  --config FILE    read config from FILE instead of looking it up by project

Config (in the file, or in the environment, which wins over the file):
  SUT_SRC_REPO         required  local clone of the project's source
  SUT_BUILD_IMAGE      required  toolchain image the compile runs in
  SUT_BUILD_CMD        required  build command, run with the source as its cwd
  SUT_ARTIFACT         required  path of the built binary, relative to the source
  SUT_SRC_MOUNT                  where the source is mounted in the container (/src)
  SUT_BUILD_ENV                  KEY=VALUE pairs passed to the build container
  SUT_DOCKER_ARGS                extra arguments for `docker run`
  SUT_REQUIRE_PATHS              paths that must be non-empty after submodules update
  SUT_SCRATCH                    working area for worktrees and the cache
  SUT_CCACHE_VOL                 docker volume for ccache
  SUT_CCACHE_DIR                 where that volume is mounted (/.ccache)
  SUT_BIN_NAME                   cached binary's name (default: basename of SUT_ARTIFACT)
  SUT_MOUNT_GIT_COMMON           mount the repo's git dir read-only into the build (1)
EOF
	exit 2
}

CONFIG="${SUT_CONFIG:-}"
PROJECT="${SUT_PROJECT:-}"
SHA=""

while [ $# -gt 0 ]; do
	case "$1" in
	--project)
		PROJECT="${2:-}"
		shift 2 || usage
		;;
	--project=*) PROJECT="${1#--project=}" && shift ;;
	--config)
		CONFIG="${2:-}"
		shift 2 || usage
		;;
	--config=*) CONFIG="${1#--config=}" && shift ;;
	-h | --help) usage ;;
	-*) usage ;;
	*)
		[ -z "$SHA" ] || usage
		SHA="$1"
		shift
		;;
	esac
done

# REFUSE RATHER THAN GUESS. Every one of these gets substituted into a path or a git command, and a
# build that quietly runs against the wrong commit, or writes its cache outside the scratch area, is
# worse than one that does not start. A sha is a sha, a project name is a path component.
[ -n "$SHA" ] || usage
[[ "$SHA" =~ ^[0-9a-fA-F]{7,40}$ ]] || {
	printf 'build-sut.sh: %q is not a commit sha\n' "$SHA" >&2
	exit 2
}
if [ -z "$CONFIG" ]; then
	[ -n "$PROJECT" ] || {
		printf 'build-sut.sh: no project and no config - pass --project or --config\n' >&2
		exit 2
	}
	SUT_CONFIG_DIR="${SUT_CONFIG_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/build-sut.d}"
	CONFIG="$SUT_CONFIG_DIR/$PROJECT.env"
fi
[[ "${PROJECT:-unnamed}" =~ ^[A-Za-z0-9._-]+$ ]] || {
	printf 'build-sut.sh: %q is not a usable project name\n' "$PROJECT" >&2
	exit 2
}
[ -f "$CONFIG" ] || {
	printf 'build-sut.sh: no config file at %s\n' "$CONFIG" >&2
	exit 2
}

# THE ENVIRONMENT WINS OVER THE FILE, so one project's config can be run with a different scratch
# area or toolchain image for a one-off without editing the file the runner reads. The config file
# is plain assignments, so it would otherwise overwrite whatever the caller exported: remember the
# caller's values first, source the file, then put them back.
SUT_VARS=(
	SUT_SRC_REPO SUT_BUILD_IMAGE SUT_BUILD_CMD SUT_ARTIFACT SUT_SRC_MOUNT
	SUT_BUILD_ENV SUT_DOCKER_ARGS SUT_REQUIRE_PATHS SUT_SCRATCH SUT_CCACHE_VOL
	SUT_CCACHE_DIR SUT_BIN_NAME SUT_MOUNT_GIT_COMMON
)
declare -A from_env=()
for v in "${SUT_VARS[@]}"; do
	if [ -n "${!v+set}" ]; then from_env["$v"]="${!v}"; fi
done
# shellcheck source=/dev/null  # the config is named by the caller and is trusted input
. "$CONFIG"
for v in "${!from_env[@]}"; do printf -v "$v" '%s' "${from_env[$v]}"; done

PROJECT="${PROJECT:-$(basename "$CONFIG" .env)}"
for v in SUT_SRC_REPO SUT_BUILD_IMAGE SUT_BUILD_CMD SUT_ARTIFACT; do
	[ -n "${!v:-}" ] || {
		printf 'build-sut.sh: %s is not set (config %s)\n' "$v" "$CONFIG" >&2
		exit 2
	}
done

SRC_REPO="$SUT_SRC_REPO"
BUILD_IMAGE="$SUT_BUILD_IMAGE"
ARTIFACT="$SUT_ARTIFACT"
SRC_MOUNT="${SUT_SRC_MOUNT:-/src}"
SCRATCH="${SUT_SCRATCH:-/mnt/data/handoff-build}"
CCACHE_VOL="${SUT_CCACHE_VOL:-handoff-ccache-$PROJECT}" # docker volume, persists across builds
CCACHE_DIR="${SUT_CCACHE_DIR:-/.ccache}"
BIN_NAME="${SUT_BIN_NAME:-$(basename "$ARTIFACT")}"
CACHE="$SCRATCH/bin/$PROJECT"
WORKTREE="$SCRATCH/src-$PROJECT-$SHA"
OUT="$CACHE/$BIN_NAME-$SHA"

mkdir -p "$CACHE"

# cache hit
if [ -x "$OUT" ]; then
	echo "cache hit: $BIN_NAME@$SHA already built -> $OUT" >&2
	echo "$OUT"
	exit 0
fi

echo ">> building $PROJECT $BIN_NAME@$SHA (first time; cached after this)" >&2

# toolchain image present?
if ! docker image inspect "$BUILD_IMAGE" >/dev/null 2>&1; then
	echo ">> pulling toolchain image $BUILD_IMAGE (may be multi-GB, one time)" >&2
	docker pull "$BUILD_IMAGE" >&2
fi

# Source at the sha: a CLEAN detached worktree off the local repo. Always recreate - a leftover from
# a failed run keeps a stale admin gitdir (fatal: not a git repository .../worktrees/...). A stale
# worktree's build directory is ROOT-owned (the toolchain container built as root), so a host
# `rm -rf` hits Permission denied under set -e; remove it via a root container instead.
docker run --rm -u 0 -v "$SCRATCH":/s "$BUILD_IMAGE" rm -rf "/s/$(basename "$WORKTREE")" >/dev/null 2>&1 || true
git -C "$SRC_REPO" worktree remove --force "$WORKTREE" >/dev/null 2>&1 || true
rm -rf "$WORKTREE" 2>/dev/null || true
git -C "$SRC_REPO" worktree prune 2>/dev/null || true
git -C "$SRC_REPO" worktree add --detach "$WORKTREE" "$SHA" >&2

if [ -f "$WORKTREE/.gitmodules" ]; then
	# Reuse the submodules already on disk. SRC_REPO's working tree has them checked out at their
	# pinned commits, so point each submodule URL at that local checkout: the fetch becomes a local
	# copy instead of cloning multi-GB dependencies over a slow mirror. The network is used only for
	# anything genuinely missing locally, and the tolerant timeouts plus retry cover that tail.
	git -C "$WORKTREE" config http.lowSpeedLimit 0
	git -C "$WORKTREE" config http.lowSpeedTime 999999
	git -C "$WORKTREE" submodule init >&2
	git -C "$WORKTREE" config -f "$WORKTREE/.gitmodules" --get-regexp 'submodule\..*\.path' |
		while read -r key path; do
			name=${key#submodule.}
			name=${name%.path}
			local_sub="$SRC_REPO/$path"
			if [ -e "$local_sub/.git" ]; then
				git -C "$WORKTREE" config "submodule.$name.url" "$local_sub"
			fi
		done
	# Populate submodules from the local checkouts (top-level only). git emits a benign bookkeeping
	# error for a LINKED worktree ("fatal: not a git repository .../worktrees/...") AFTER the files
	# are checked out, so we do NOT gate on its exit code - we verify instead that the paths the
	# build actually needs have source in them.
	git -C "$WORKTREE" -c protocol.file.allow=always submodule update --jobs 4 >&2 || true
	missing=""
	for req in ${SUT_REQUIRE_PATHS:-}; do
		[ -n "$(ls -A "$WORKTREE/$req" 2>/dev/null)" ] || missing="$missing $req"
	done
	if [ -n "$missing" ]; then
		echo ">> submodules empty after update:$missing" >&2
		exit 1
	fi
	echo ">> submodules populated from local checkouts" >&2
fi

# ccache volume
docker volume inspect "$CCACHE_VOL" >/dev/null 2>&1 || docker volume create "$CCACHE_VOL" >/dev/null

# The worktree's .git is a FILE pointing at the superproject's common git dir. A build script that
# stamps a version by running git needs that dir to exist at the path the file names, so mount it at
# its host path, read-only. It also carries the worktree admin and submodule gitdirs.
docker_git_mount=()
if [ "${SUT_MOUNT_GIT_COMMON:-1}" != "0" ]; then
	COMMON_GIT=$(git -C "$SRC_REPO" rev-parse --git-common-dir)
	case "$COMMON_GIT" in
	/*) ;;
	*) COMMON_GIT="$(cd "$SRC_REPO" && cd "$COMMON_GIT" && pwd)" ;;
	esac
	docker_git_mount=(-v "$COMMON_GIT":"$COMMON_GIT":ro)
fi

# The build environment and the extra docker arguments are configuration, so they arrive as strings
# and are split here on whitespace - a KEY=VALUE with a space in it is not something a build takes.
docker_env=(-e "CCACHE_DIR=$CCACHE_DIR")
for kv in ${SUT_BUILD_ENV:-}; do docker_env+=(-e "$kv"); done
read -r -a docker_extra <<<"${SUT_DOCKER_ARGS:-}"
read -r -a build_cmd <<<"$SUT_BUILD_CMD"

# Compile in the toolchain container, with the source as the working directory, which is the same
# invocation the project's CI uses.
docker run --rm \
	"${docker_extra[@]}" \
	"${docker_env[@]}" \
	-v "$WORKTREE":"$SRC_MOUNT" \
	"${docker_git_mount[@]}" \
	-v "$CCACHE_VOL":"$CCACHE_DIR" \
	-w "$SRC_MOUNT" \
	"$BUILD_IMAGE" "${build_cmd[@]}" >&2

BIN="$WORKTREE/$ARTIFACT"
[ -x "$BIN" ] || {
	echo "build finished but $BIN missing" >&2
	exit 1
}
cp "$BIN" "$OUT"
echo ">> cached $BIN_NAME@$SHA -> $OUT" >&2

# Free the worktree checkout, keeping the cached binary; the ccache volume persists. The build
# container ran as root and left root-owned files behind (nested submodules it populated), so the
# host cannot remove them - do it from a root container. Cleanup NEVER fails the build: the binary
# is already cached and printed, and a leftover directory is the next run's problem to recreate.
docker run --rm -u 0 -v "$SCRATCH":/s "$BUILD_IMAGE" rm -rf "/s/$(basename "$WORKTREE")" >/dev/null 2>&1 || true
git -C "$SRC_REPO" worktree remove --force "$WORKTREE" >/dev/null 2>&1 || true
git -C "$SRC_REPO" worktree prune >/dev/null 2>&1 || true

echo "$OUT"
