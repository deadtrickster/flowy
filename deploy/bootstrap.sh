#!/usr/bin/env bash
# From `git clone` to a node you can talk to, in one command.
#
#   deploy/bootstrap.sh [--project NAME] [--handle NAME] [--kind RUNTIME] [--no-publish]
#
# What a fresh machine is missing is not the containers, which compose can bring
# up on its own. It is the three things a node needs before a token means
# anything, and every one of them was previously done by hand from memory:
#
#   1. a database with schema.sql applied, because a node cannot create its own
#      tables and says so at startup by refusing to start;
#   2. a DECLARED project, because minting a seat into an undeclared project is
#      a refusal, and declaring one over HTTP needs a token that does not exist
#      yet - the loop this script breaks by writing the registry row directly,
#      which is what schema.sql's own backfill does and what BackfillProjects
#      adopts and signs at the node's next start;
#   3. a minted seat, which is the first token in existence on this node.
#
# The order matters and is the reason each step is separate here. The project row
# goes in while the node is DOWN, so the node adopts and signs it as it comes up.
# A row inserted after the node started stays unsigned until the next restart,
# and an unsigned registry row cannot replicate to a peer.
#
# It is idempotent. Run it again after a pull and it rebuilds and restarts
# without re-minting: the existing token file is the record that the seat was
# already taken, and minting a second one under the same handle is a refusal.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT="flowy"
HANDLE="operator"
KIND="claude"
PUBLISH=1

usage() {
	cat >&2 <<'EOF'
usage: deploy/bootstrap.sh [options]

  --project NAME   project to declare and mint the seat into (default: flowy)
  --handle NAME    the handle the seat speaks under (default: operator)
  --kind RUNTIME   the runtime it runs on: claude, glm, opencode (default: claude)
  --no-publish     do not publish a host port; the node is then reachable only
                   from the compose `edge` network, which is what a deployment
                   behind a tunnel wants
EOF
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--project)
		PROJECT="${2:-}"
		shift 2 || usage
		;;
	--project=*)
		PROJECT="${1#--project=}"
		shift
		;;
	--handle)
		HANDLE="${2:-}"
		shift 2 || usage
		;;
	--handle=*)
		HANDLE="${1#--handle=}"
		shift
		;;
	--kind)
		KIND="${2:-}"
		shift 2 || usage
		;;
	--kind=*)
		KIND="${1#--kind=}"
		shift
		;;
	--no-publish)
		PUBLISH=0
		shift
		;;
	-h | --help) usage ;;
	*) usage ;;
	esac
done

# A project name is a primary key and reaches SQL as a bound parameter, and a
# handle is the name a room knows. Refuse anything that is not one rather than
# find out later from a constraint.
[[ "$PROJECT" =~ ^[A-Za-z0-9._-]+$ ]] || {
	printf 'bootstrap: %q is not a usable project name\n' "$PROJECT" >&2
	exit 2
}
[[ "$HANDLE" =~ ^[A-Za-z0-9._-]+$ ]] || {
	printf 'bootstrap: %q is not a usable handle\n' "$HANDLE" >&2
	exit 2
}

docker compose version >/dev/null 2>&1 || {
	# shellcheck disable=SC2016  # the backticks are prose, not a substitution
	printf 'bootstrap: `docker compose` is not available; install Docker with the compose plugin\n' >&2
	exit 1
}

FILES=(-f "$HERE/compose.yaml")
if [ "$PUBLISH" = 1 ]; then
	FILES+=(-f "$HERE/compose.loopback.yaml")
fi
compose() { docker compose "${FILES[@]}" "$@"; }

# ------------------------------------------------------------------- the .env
#
# Generated rather than prompted for: the only value that has to be a secret is
# the database password, nothing outside the internal network can reach the
# database anyway, and a password a person invents on a deploy is a password
# that ends up in a shell history.
ENV_FILE="$HERE/.env"
if [ ! -f "$ENV_FILE" ]; then
	pw="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
	umask 077
	sed -e "s|^FLOWY_DB_PASSWORD=.*|FLOWY_DB_PASSWORD=$pw|" \
		"$HERE/.env.example" >"$ENV_FILE"
	printf '>> wrote %s with a generated database password\n' "$ENV_FILE" >&2
fi

read_env() {
	# The value of one key in .env, or the fallback. Read rather than sourced:
	# this file holds a password, and sourcing it would run whatever a stray
	# backtick in it happened to say.
	local key="$1" fallback="$2" line
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

DB_USER="$(read_env FLOWY_DB_USER flowy)"
DB_NAME="$(read_env FLOWY_DB_NAME flowy)"
PORT="$(read_env FLOWY_PORT 8787)"
TOKEN_FILE="$HERE/.flowy-token"

# What `flowy version` will report. Exported rather than written into .env
# because the environment wins over the file in compose, so this is right on
# every run without the file drifting.
if git -C "$HERE" rev-parse --short HEAD >/dev/null 2>&1; then
	FLOWY_BUILD_STAMP="$(git -C "$HERE" rev-parse --short HEAD)"
	export FLOWY_BUILD_STAMP
fi

# ------------------------------------------------------------------ the build
printf '>> building the image (console, then binary)\n' >&2
compose build node

# --------------------------------------------------------------- the database
printf '>> starting Postgres\n' >&2
compose up -d db

# Wait on the container's own health check rather than on a sleep. Postgres
# accepts a connection several seconds before it will answer a query, and the
# gap is exactly where a node that dialled in too early used to die.
wait_healthy() {
	local svc="$1" cid state=""
	for _ in $(seq 1 60); do
		cid="$(compose ps -q "$svc" 2>/dev/null || true)"
		if [ -n "$cid" ]; then
			state="$(docker inspect -f '{{.State.Health.Status}}' "$cid" 2>/dev/null || true)"
			[ "$state" = "healthy" ] && return 0
		fi
		sleep 2
	done
	printf 'bootstrap: %s did not become healthy (last state: %s)\n' "$svc" "${state:-none}" >&2
	# shellcheck disable=SC2016  # the backticks are prose, not a substitution
	printf '           `docker compose -f %s logs %s` says why\n' "$HERE/compose.yaml" "$svc" >&2
	return 1
}

printf '>> waiting for Postgres to answer a query\n' >&2
wait_healthy db

# The registry row, while the node is down, so the node signs it on the way up.
# ON CONFLICT DO NOTHING because this script is run again after every pull, and
# a second declaration of the same name must not disturb the signed row the node
# has since written.
printf '>> declaring project %s\n' "$PROJECT" >&2
#
# The statement arrives on stdin rather than through -c because psql does not
# substitute its variables into a -c command, and :'proj' would reach the server
# verbatim as a syntax error. On stdin it is substituted and quoted by psql,
# which is what keeps the name out of the SQL text.
printf "INSERT INTO projects (id, name, provenance) VALUES (:'proj', :'proj', 'declared') ON CONFLICT (id) DO NOTHING;\n" |
	compose exec -T db psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" \
		-v proj="$PROJECT" -q

# ------------------------------------------------------------------- the node
printf '>> starting the node\n' >&2
compose up -d node

printf '>> waiting for the node to report healthy\n' >&2
wait_healthy node

# ------------------------------------------------------------------- the seat
if [ -s "$TOKEN_FILE" ]; then
	printf '>> a token already exists at %s; not minting a second seat\n' "$TOKEN_FILE" >&2
else
	printf '>> minting the %s seat in %s\n' "$HANDLE" "$PROJECT" >&2
	minted_err="$(mktemp)"
	# stdout is the token and stderr is the description of the seat - that split
	# is `flowy mint`'s contract, and it is what lets this file the secret
	# without also filing the paragraph a person reads.
	token="$(compose --profile bootstrap run --rm -T mint \
		--handle "$HANDLE" --kind "$KIND" --project "$PROJECT" 2>"$minted_err")" || {
		cat "$minted_err" >&2
		rm -f "$minted_err"
		printf 'bootstrap: minting failed\n' >&2
		exit 1
	}
	cat "$minted_err" >&2
	operator="$(awk '/^[[:space:]]*user[[:space:]]/ {print $2; exit}' "$minted_err")"
	rm -f "$minted_err"

	umask 077
	printf '%s\n' "$token" >"$TOKEN_FILE"
	printf '>> token written to %s (mode 600)\n' "$TOKEN_FILE" >&2

	# The operator is local configuration, not a row: it is the single principal
	# ?scope=all obeys, and this node's operator is the seat that bootstrapped
	# it. Recording it needs a restart, which is why it happens here and not
	# before the node was up - the id does not exist until the mint runs.
	if [ -n "$operator" ]; then
		sed -i -e "s|^FLOWY_OPERATOR=.*|FLOWY_OPERATOR=$operator|" "$ENV_FILE"
		printf '>> operator %s recorded; restarting the node\n' "$operator" >&2
		compose up -d node
	fi
fi

# ------------------------------------------------------------------ the proof
# A deploy that says "done" without asking the node anything is a deploy that
# has been wrong before. These two are the whole claim: the node answers, and
# the token it issued reaches the project that was declared.
token="$(cat "$TOKEN_FILE")"
if [ "$PUBLISH" = 1 ]; then
	url="http://127.0.0.1:$PORT"
	fetch() { curl -fsS -H "Authorization: Bearer $token" "$url$1"; }
	curl -fsS "$url/healthz" >/dev/null
else
	url="http://127.0.0.1:8787"
	fetch() {
		compose exec -T node wget -q -O- \
			--header="Authorization: Bearer $token" "$url$1"
	}
	compose exec -T node wget -q -O- "$url/healthz" >/dev/null
fi

projects="$(fetch /api/projects)"
case "$projects" in
*"\"$PROJECT\""*) : ;;
*)
	printf 'bootstrap: the node answered but does not know project %s:\n%s\n' \
		"$PROJECT" "$projects" >&2
	exit 1
	;;
esac

printf '\nnode is up and answering.\n'
if [ "$PUBLISH" = 1 ]; then
	printf '  url      %s   (loopback only; a tunnel on the edge network is the way in from elsewhere)\n' "$url"
else
	printf '  url      no host port published; reach it from the compose edge network\n'
fi
printf '  project  %s\n' "$PROJECT"
printf '  token    %s\n' "$TOKEN_FILE"
# shellcheck disable=SC2016  # the $(cat ...) is the command the reader types, not one to run here
printf '\ntry it:\n  curl -H "Authorization: Bearer $(cat %s)" %s/api/projects\n' \
	"$TOKEN_FILE" "$url"
