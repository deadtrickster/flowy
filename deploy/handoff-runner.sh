#!/usr/bin/env bash
# Bring up the repro runner on the trusted host, after checking the four things
# that make it fail obscurely if they are wrong.
#
#   deploy/handoff-runner.sh up -d
#   deploy/handoff-runner.sh logs -f
#   deploy/handoff-runner.sh down
#
# Everything after the first argument is passed straight to docker compose, so
# this is the compose CLI with a preflight in front of it rather than a wrapper
# with its own vocabulary to learn.
#
# The preflight exists because each of these fails somewhere far from its cause:
# a missing Docker socket looks like the runner crashing at the first build; a
# missing store network looks like a DNS failure for `db`; a source repository
# that is not a git clone looks like `git worktree add` refusing halfway through
# a run; and an image with no handoff-runner binary in it looks like an exec
# format error. Each of them is one line to check here and ten minutes to find
# from the other end.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$HERE/handoff-runner.compose.yaml"
ENV_FILE="$HERE/.env"

[ -f "$ENV_FILE" ] || {
	printf 'handoff-runner: no %s - run bootstrap.sh first, or copy .env.example\n' "$ENV_FILE" >&2
	exit 2
}

read_env() {
	# One key out of .env, or the fallback. Read rather than sourced: the file
	# holds the database password and sourcing it would run whatever it says.
	local key="$1" fallback="${2:-}" line
	# An exported value wins over the file, which is what compose itself does
	# with .env - so a one-off run with FLOWY_TAG=x in front of the command sees
	# the same value here as compose does, instead of two halves disagreeing.
	if [ -n "${!key:-}" ]; then
		printf '%s' "${!key}"
		return
	fi
	line="$(grep -E "^${key}=" "$ENV_FILE" | tail -1 || true)"
	line="${line#*=}"
	printf '%s' "${line:-$fallback}"
}

SRC_REPO="$(read_env SUT_SRC_REPO)"
SCRATCH="$(read_env SUT_SCRATCH /mnt/data/handoff-build)"
IMAGE="flowy-handoff-runner:$(read_env FLOWY_TAG local)"

# 1. The Docker socket. This is the privilege the whole separate deployment
#    exists to contain, so its absence is worth naming rather than discovering.
[ -S /var/run/docker.sock ] || {
	printf 'handoff-runner: /var/run/docker.sock is not a socket on this host.\n' >&2
	printf '                This binary drives Docker; there is nothing for it to do here.\n' >&2
	exit 1
}

# 2. The store network, which is the node deployment's internal one. Joining it
#    is how DATABASE_URL reaches `db` without the database publishing a port.
docker network inspect flowy_inner >/dev/null 2>&1 || {
	printf 'handoff-runner: the network flowy_inner does not exist.\n' >&2
	printf '                The node deployment creates it - bring that up first:\n' >&2
	printf '                  %s/bootstrap.sh\n' "$HERE" >&2
	exit 1
}

# 3. The source checkout, which the build checks commits out of with a worktree.
[ -n "$SRC_REPO" ] || {
	printf 'handoff-runner: SUT_SRC_REPO is not set in %s\n' "$ENV_FILE" >&2
	exit 2
}
git -C "$SRC_REPO" rev-parse --git-dir >/dev/null 2>&1 || {
	printf 'handoff-runner: %s is not a git repository.\n' "$SRC_REPO" >&2
	printf '                build-sut.sh checks the commit under test out of it with\n' >&2
	# shellcheck disable=SC2016  # the backticks are prose, not a substitution
	printf '                `git worktree add`, so it has to be a real clone and writable.\n' >&2
	exit 1
}
[ -w "$SRC_REPO" ] || {
	printf 'handoff-runner: %s is not writable; a worktree cannot be created in it\n' "$SRC_REPO" >&2
	exit 1
}
# A LINKED WORKTREE IS NOT A CLONE, and this one was found the hard way: the
# obvious source path on this host is itself a `git worktree`, whose .git is a
# file pointing at the main clone's git directory somewhere else entirely. Bind
# only that path into the container and every git command inside it fails with
# "not a git repository", naming a path that is not mounted. Point at the main
# clone instead - build-sut.sh adds its worktrees off it anyway.
git_common="$(git -C "$SRC_REPO" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
case "$git_common" in
"$SRC_REPO"/*) : ;;
*)
	printf 'handoff-runner: %s is a linked git worktree, not a clone.\n' "$SRC_REPO" >&2
	printf '                Its git directory is %s, which is outside the path\n' "$git_common" >&2
	printf '                bound into the container, so every git command there would fail.\n' >&2
	printf '                Set SUT_SRC_REPO to the main clone: %s\n' "${git_common%/.git}" >&2
	exit 1
	;;
esac
mkdir -p "$SCRATCH" 2>/dev/null || true
[ -d "$SCRATCH" ] && [ -w "$SCRATCH" ] || {
	printf 'handoff-runner: %s is not a writable directory (SUT_SCRATCH)\n' "$SCRATCH" >&2
	exit 1
}

# 4. The binary. cmd/handoff-runner is piece 9 of the handoff migration and may
#    not be in the tree this image was built from. The image is built from
#    ./cmd/... as a directory precisely so that this stays a check rather than a
#    build failure, and this is where that check is made.
if docker image inspect "$IMAGE" >/dev/null 2>&1; then
	if ! docker run --rm --entrypoint sh "$IMAGE" -c '[ -x /usr/local/bin/handoff-runner ]'; then
		printf 'handoff-runner: %s has no handoff-runner binary in it.\n' "$IMAGE" >&2
		printf '                cmd/handoff-runner has not landed in the tree this image was\n' >&2
		printf '                built from. Everything else here is ready for it: rebuild with\n' >&2
		printf '                  docker compose -f %s build\n' "$COMPOSE_FILE" >&2
		printf '                once it is on master, and set HANDOFF_RUNNER_ARGS in %s.\n' "$ENV_FILE" >&2
		exit 1
	fi
fi

# `exec docker compose`, not `exec compose` through a helper function: exec
# replaces the shell with an EXTERNAL command and does not see shell functions,
# so a helper here silently ran /usr/bin/compose, which on this machine is a
# mailcap tool that tried to interpret `up -d` as a file to attach to an email.
exec docker compose -f "$COMPOSE_FILE" "$@"
