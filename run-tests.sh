#!/usr/bin/env bash
# The gate. Stands up a throwaway Postgres, loads the schema, builds the node,
# runs the unit tests, then runs the live checks against a running `flowy serve`.
#
# Everything it creates lives in one temp directory and is torn down by the trap,
# including the database: no system service is touched.
#
# Phase 1 adds the identity and permission checks. They are driven over HTTP with
# curl and jq rather than from inside the process, because the thing being
# tested is what a second agent holding a second token can and cannot see - and
# that is only true if it is true over the wire.
#
# Phase 2 adds the MCP endpoint: the same store, reached the way an agent reaches
# it. Both transports are exercised - JSON-RPC over POST /mcp and JSON-RPC over
# a subprocess's pipes - and an item written over one is read back over the
# other, which is the whole of the "one shared memory" claim.

set -euo pipefail

cd "$(dirname "$0")"

ROOT="$PWD"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/flowy-gate.XXXXXX")"
PGDATA="$WORK/pgdata"
PGSOCK="$WORK/sock"
PGLOG="$WORK/postgres.log"
SERVE_LOG="$WORK/serve.log"
MCP_LOG="$WORK/mcp.log"
DBNAME="flowy"
readonly ROOT WORK PGDATA PGSOCK PGLOG SERVE_LOG MCP_LOG DBNAME

PG_BIN=""
PGPORT=""
HTTP_PORT=""
MCP_PORT=""
SERVE_PID=""
MCP_PID=""
passed=0
failed=0

cleanup() {
	local status=$?
	set +e
	local pid
	for pid in "$SERVE_PID" "$MCP_PID"; do
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null
			wait "$pid" 2>/dev/null
		fi
	done
	if [ -n "$PG_BIN" ] && [ -d "$PGDATA" ]; then
		"$PG_BIN/pg_ctl" -D "$PGDATA" -m immediate -w stop >/dev/null 2>&1
	fi
	rm -rf "$WORK"
	exit "$status"
}
trap cleanup EXIT INT TERM

say() { printf '\n== %s\n' "$*"; }

indent() { sed 's/^/     /'; }

# check <name> <command...> - runs the command, prints PASS or FAIL with its
# output, and keeps the tally.
check() {
	local name="$1"
	shift
	local out status
	if out="$("$@" 2>&1)"; then
		printf 'PASS %s\n' "$name"
		if [ -n "$out" ]; then
			printf '%s\n' "$out" | indent
		fi
		passed=$((passed + 1))
	else
		status=$?
		printf 'FAIL %s (exit %d)\n' "$name" "$status"
		if [ -n "$out" ]; then
			printf '%s\n' "$out" | indent
		fi
		failed=$((failed + 1))
	fi
}

# find_pg_bin locates initdb and pg_ctl, which Debian keeps off PATH.
find_pg_bin() {
	local candidate
	if candidate="$(command -v pg_ctl 2>/dev/null)"; then
		dirname "$candidate"
		return 0
	fi
	for candidate in /usr/lib/postgresql/*/bin /usr/pgsql-*/bin /usr/local/pgsql/bin; do
		if [ -x "$candidate/pg_ctl" ] && [ -x "$candidate/initdb" ]; then
			printf '%s\n' "$candidate"
			return 0
		fi
	done
	printf 'no initdb/pg_ctl found; install postgresql\n' >&2
	return 1
}

# free_port prints the first port at or above $1 that nothing is listening on.
free_port() {
	local port
	for ((port = $1; port < $1 + 300; port++)); do
		if ! (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
			printf '%s\n' "$port"
			return 0
		fi
	done
	printf 'no free port at or above %s\n' "$1" >&2
	return 1
}

# gofmt_clean checks our own sources; vendor/ is upstream's to format.
gofmt_clean() {
	local out
	local -a files
	mapfile -t files < <(find . -path ./vendor -prune -o -name '*.go' -print)
	out="$(gofmt -l "${files[@]}")"
	if [ -n "$out" ]; then
		printf 'these files are not gofmt clean:\n%s\n' "$out" >&2
		return 1
	fi
	printf 'gofmt clean\n'
}

# psql_counts runs a counting query as a second client and fails when it comes
# back with nothing.
psql_counts() {
	local n
	n="$(psql -v ON_ERROR_STOP=1 -tAc "$1")"
	if [ -z "$n" ] || [ "$n" -lt 1 ]; then
		printf 'query returned %s, want at least one row:\n%s\n' "${n:-<empty>}" "$1" >&2
		return 1
	fi
	printf '%s row(s)\n' "$n"
}

# ------------------------------------------------------- phase 1 http helpers

# api METHOD TOKEN PATH [BODY] - one request against the live node. The status
# lands in API_STATUS and the body in API_BODY. An empty TOKEN sends no
# Authorization header at all, which is how the 401 checks are written.
api() {
	local method=$1 token=$2 path=$3 body=${4-}
	local -a curl_args=(--silent --show-error -X "$method" -w '\n%{http_code}')
	if [ -n "$token" ]; then
		curl_args+=(-H "Authorization: Bearer $token")
	fi
	if [ -n "$body" ]; then
		curl_args+=(-H 'Content-Type: application/json' --data-binary "$body")
	fi
	local out
	out="$(curl "${curl_args[@]}" "http://127.0.0.1:$HTTP_PORT$path")" || return 1
	API_STATUS="${out##*$'\n'}"
	API_BODY="${out%$'\n'*}"
}

# want_status WANT METHOD TOKEN PATH [BODY] - request and assert the status.
want_status() {
	local want=$1
	shift
	api "$@" || return 1
	if [ "$API_STATUS" != "$want" ]; then
		printf '%s %s: want status %s, got %s\n%s\n' "$1" "$3" "$want" "$API_STATUS" "$API_BODY" >&2
		return 1
	fi
	printf '%s %s -> %s\n' "$1" "$3" "$API_STATUS"
}

# jqv EXPR - reads a value out of the last response body.
jqv() { printf '%s' "$API_BODY" | jq -r "$1"; }

# want_eq WHAT GOT WANT - a plain equality assertion with a readable failure.
want_eq() {
	if [ "$2" != "$3" ]; then
		printf '%s is %q, want %q\n' "$1" "$2" "$3" >&2
		return 1
	fi
}

# remember NAME VALUE / recall - the checks run inside a command substitution,
# so anything one check has to hand the next goes through this file.
remember() { printf '%s=%q\n' "$1" "$2" >>"$WORK/ids"; }

recall() {
	# shellcheck source=/dev/null
	. "$WORK/ids"
}

# hits EXPR - how many artifacts the last response returned, optionally
# narrowed by a jq select expression.
hits() {
	if [ $# -eq 0 ]; then
		printf '%s' "$API_BODY" | jq '.artifacts | length'
	else
		printf '%s' "$API_BODY" | jq "[.artifacts[] | select($1)] | length"
	fi
}

# stub_says checks that a subcommand that is still a stub prints its
# placeholder and exits zero. mcp left this list in Phase 2.
stub_says() {
	local sub="$1" out
	out="$(./flowy "$sub")"
	if [ "$out" != "$sub: not yet" ]; then
		printf 'flowy %s printed %q, want %q\n' "$sub" "$out" "$sub: not yet" >&2
		return 1
	fi
	printf '%s\n' "$out"
}

# ------------------------------------------------------------ phase 1 checks
#
# Each function is one assertion, run by `check`, and each starts by recalling
# whatever the earlier ones wrote down.

seeded_ok() {
	recall
	local name
	for name in USER_A TOKEN_A TOKEN_A_AGENT TOKEN_A_PC USER_B TOKEN_B USER_OP TOKEN_OP; do
		if [ -z "${!name:-}" ]; then
			printf 'the seed did not set %s\n' "$name" >&2
			return 1
		fi
	done
	printf 'A=%s in pa, B=%s in pb, operator=%s\n' "$USER_A" "$USER_B" "$USER_OP"
}

# A token resolves to the (user, agent, project) triple everything else is
# decided from.
whoami_is_a() {
	recall
	api GET "$TOKEN_A" /api/whoami || return 1
	want_eq "whoami user" "$(jqv .user)" "$USER_A" || return 1
	want_eq "whoami project" "$(jqv .project)" pa || return 1
	printf 'user %s, project pa\n' "$USER_A"
}

# An agent's token carries no user of its own and has to inherit one, or an
# agent could not read the personal artifacts of the person it works for.
agent_token_inherits() {
	recall
	api GET "$TOKEN_A_AGENT" /api/whoami || return 1
	want_eq "agent token user" "$(jqv .user)" "$USER_A" || return 1
	want_eq "agent token agent" "$(jqv .agent)" "$AGENT_A" || return 1
	want_eq "agent token project" "$(jqv .project)" pa || return 1
	printf 'agent %s acts as user %s in pa\n' "$AGENT_A" "$USER_A"
}

# The word only ever appears in the artifact's discovery, so anything that finds
# it found it there.
a_creates_bug() {
	recall
	api POST "$TOKEN_A" /api/artifacts '{
		"type": "bug",
		"title": "the gate cannot parse a handoff",
		"body": "reported while wiring phase 1",
		"discovery": "the parser chokes on a zorblatt in the header",
		"status": "open", "severity": "high",
		"tags": ["parser", "phase1"]
	}' || return 1
	want_eq "create status" "$API_STATUS" 200 || return 1

	local id
	id="$(jqv .id)"
	want_eq "created project" "$(jqv .project)" pa || return 1
	want_eq "created owner" "$(jqv .owner_user)" "$USER_A" || return 1
	want_eq "created visibility" "$(jqv .visibility)" project || return 1
	if [ "$(jqv .hlc)" -le 0 ]; then
		printf 'the artifact came back with hlc %s, want a stamped clock\n' "$(jqv .hlc)" >&2
		return 1
	fi
	remember BUG "$id"
	remember BUG_HLC "$(jqv .hlc)"
	printf 'bug %s in pa, hlc %s, node %s\n' "$id" "$(jqv .hlc)" "$(jqv .node)"
}

a_reads_bug() {
	recall
	api GET "$TOKEN_A" "/api/artifact/$BUG" || return 1
	want_eq "own read status" "$API_STATUS" 200 || return 1
	want_eq "id" "$(jqv .id)" "$BUG" || return 1
	want_eq "tags" "$(jqv '.tags | join(",")')" parser,phase1 || return 1
	printf '%s: %s\n' "$BUG" "$(jqv .title)"
}

a_searches_discovery_word() {
	recall
	api GET "$TOKEN_A" '/api/search?q=zorblatt' || return 1
	want_eq "search status" "$API_STATUS" 200 || return 1
	want_eq "query echoed" "$(jqv .query)" zorblatt || return 1
	want_eq "hits" "$(hits)" 1 || return 1
	want_eq "the hit" "$(jqv '.artifacts[0].id')" "$BUG" || return 1
	if ! printf '%s' "$API_BODY" | jq -e '.artifacts[0].rank > 0' >/dev/null; then
		printf 'the hit ranked %s, want a positive score\n' "$(jqv '.artifacts[0].rank')" >&2
		return 1
	fi
	printf 'zorblatt appears only in discovery and ranked %s\n' "$(jqv '.artifacts[0].rank')"
}

a_lists_bug() {
	recall
	api GET "$TOKEN_A" '/api/artifacts?type=bug' || return 1
	want_eq "list status" "$API_STATUS" 200 || return 1
	want_eq "the bug is listed once" "$(hits ".id == \"$BUG\"")" 1 || return 1
	printf 'A sees %s artifact(s) of type bug\n' "$(hits)"
}

# Not 403: an id that exists in another project has to look exactly like an id
# that does not exist at all.
b_cannot_read_bug() {
	recall
	want_status 404 GET "$TOKEN_B" "/api/artifact/$BUG"
}

b_list_omits_bug() {
	recall
	api GET "$TOKEN_B" /api/artifacts || return 1
	want_eq "the bug in B's list" "$(hits ".id == \"$BUG\"")" 0 || return 1
	printf "B's list holds %s artifact(s), none of them A's\n" "$(hits)"
}

b_search_misses_bug() {
	recall
	api GET "$TOKEN_B" '/api/search?q=zorblatt' || return 1
	want_eq "B's hits for zorblatt" "$(hits)" 0 || return 1
	printf "B's search for zorblatt returns nothing\n"
}

# A opens pa up to pb. Only a principal of pa can do that, which is why A issues
# it and not B.
a_grants_pb_read_of_pa() {
	recall
	api POST "$TOKEN_A" /api/grants '{"from_project": "pb", "to_project": "pa"}' || return 1
	want_eq "grant status" "$API_STATUS" 200 || return 1
	want_eq "granted by" "$(jqv .granted_by)" "$USER_A" || return 1
	printf 'grant %s: pb may read pa\n' "$(jqv .id)"
}

b_reads_bug_after_grant() {
	recall
	want_status 200 GET "$TOKEN_B" "/api/artifact/$BUG"
}

b_searches_bug_after_grant() {
	recall
	api GET "$TOKEN_B" '/api/search?q=zorblatt' || return 1
	want_eq "B's hits for zorblatt" "$(hits)" 1 || return 1
	want_eq "the hit" "$(jqv '.artifacts[0].id')" "$BUG" || return 1
	printf 'the grant reaches search as well as the direct read\n'
}

a_creates_personal() {
	recall
	api POST "$TOKEN_A" /api/artifacts '{
		"type": "note",
		"title": "not for the project",
		"body": "a quixotron of my own",
		"visibility": "personal",
		"project": null
	}' || return 1
	want_eq "create status" "$API_STATUS" 200 || return 1
	want_eq "personal project" "$(jqv .project)" null || return 1
	want_eq "personal visibility" "$(jqv .visibility)" personal || return 1
	remember NOTE "$(jqv .id)"
	printf 'personal note %s, no project\n' "$(jqv .id)"
}

# The floor: pb already has a grant on pa, and it still does not reach this.
b_cannot_read_personal() {
	recall
	want_status 404 GET "$TOKEN_B" "/api/artifact/$NOTE"
}

b_cannot_search_personal() {
	recall
	api GET "$TOKEN_B" '/api/search?q=quixotron' || return 1
	want_eq "B's hits for quixotron" "$(hits)" 0 || return 1
	printf 'a grant does not reach a personal artifact through search either\n'
}

a_agent_reads_personal() {
	recall
	want_status 200 GET "$TOKEN_A_AGENT" "/api/artifact/$NOTE"
}

# pc is a project nobody holds a project-wide grant into, so a share of one
# artifact in it is the only thing that can be doing the work.
a_creates_two_in_pc() {
	recall
	api POST "$TOKEN_A_PC" /api/artifacts '{
		"type": "note", "title": "shared one",
		"discovery": "a flimberwock lives here"
	}' || return 1
	want_eq "create status" "$API_STATUS" 200 || return 1
	want_eq "project" "$(jqv .project)" pc || return 1
	remember SHARED "$(jqv .id)"

	api POST "$TOKEN_A_PC" /api/artifacts '{
		"type": "note", "title": "kept back",
		"discovery": "a grumbleweed lives here"
	}' || return 1
	want_eq "create status" "$API_STATUS" 200 || return 1
	remember KEPT "$(jqv .id)"
	printf 'two artifacts in pc\n'
}

b_cannot_read_either_pc() {
	recall
	want_status 404 GET "$TOKEN_B" "/api/artifact/$SHARED" || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$KEPT"
}

a_shares_one_artifact() {
	recall
	api POST "$TOKEN_A_PC" /api/grants \
		"{\"artifact\": \"$SHARED\", \"subject\": \"$USER_B\"}" || return 1
	want_eq "share status" "$API_STATUS" 200 || return 1
	want_eq "shared artifact" "$(jqv .artifact)" "$SHARED" || return 1
	want_eq "shared to" "$(jqv .subject)" "$USER_B" || return 1
	printf 'share %s: %s -> %s\n' "$(jqv .id)" "$SHARED" "$USER_B"
}

b_reads_only_the_shared_one() {
	recall
	want_status 200 GET "$TOKEN_B" "/api/artifact/$SHARED" || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$KEPT"
}

b_searches_only_the_shared_one() {
	recall
	api GET "$TOKEN_B" '/api/search?q=flimberwock' || return 1
	want_eq "hits for the shared artifact" "$(hits)" 1 || return 1
	api GET "$TOKEN_B" '/api/search?q=grumbleweed' || return 1
	want_eq "hits for the one kept back" "$(hits)" 0 || return 1
	printf 'a per-artifact share reaches exactly one artifact\n'
}

# A principal writes into the project it is acting in and no other.
cross_project_write_refused() {
	recall
	want_status 403 POST "$TOKEN_A" /api/artifacts '{"type": "note", "project": "pc"}'
}

append_thread() {
	recall
	api POST "$TOKEN_A" /api/events \
		'{"type": "note", "room": "pa/bugs", "body": "opened the thread"}' || return 1
	want_eq "append status" "$API_STATUS" 200 || return 1
	local first thread
	first="$(jqv .id)"
	thread="$(jqv .thread)"
	want_eq "a thread with no head is named after its first event" "$thread" "$first" || return 1
	want_eq "the event landed in pa" "$(jqv .project)" pa || return 1
	remember E1 "$first"
	remember THREAD "$thread"
	remember E1_SEQ "$(jqv .seq_hlc)"

	api POST "$TOKEN_A" /api/events \
		"{\"type\": \"note\", \"room\": \"pa/bugs\", \"thread\": \"$thread\",
		  \"parents\": [\"$first\"], \"body\": \"continued it\"}" || return 1
	want_eq "append status" "$API_STATUS" 200 || return 1
	remember E2 "$(jqv .id)"
	printf 'thread %s: %s then %s\n' "$thread" "$first" "$(jqv .id)"
}

thread_reads_back_in_order() {
	recall
	# THREAD comes from recall, not from the local `thread` in append_thread.
	# shellcheck disable=SC2153
	api GET "$TOKEN_A" "/api/events?thread=$THREAD" || return 1
	want_eq "events status" "$API_STATUS" 200 || return 1
	local n
	n="$(printf '%s' "$API_BODY" | jq '.events | length')"
	want_eq "events in the thread" "$n" 2 || return 1
	want_eq "first" "$(jqv '.events[0].id')" "$E1" || return 1
	want_eq "second" "$(jqv '.events[1].id')" "$E2" || return 1
	want_eq "the second event's parents" "$(jqv '.events[1].parents | join(",")')" "$E1" || return 1
	want_eq "the first event opens the DAG" "$(jqv '.events[0].parents | length')" 0 || return 1
	if [ "$(jqv '.events[1].seq_hlc')" -le "$(jqv '.events[0].seq_hlc')" ]; then
		printf 'the second event did not advance seq_hlc\n' >&2
		return 1
	fi
	printf 'two events, in seq_hlc order, parents intact\n'
}

# since is the cursor peer replication will page by: strictly greater.
since_pages_the_log() {
	recall
	api GET "$TOKEN_A" "/api/events?thread=$THREAD&since=$E1_SEQ" || return 1
	want_eq "events after the first" "$(printf '%s' "$API_BODY" | jq '.events | length')" 1 || return 1
	want_eq "which one" "$(jqv '.events[0].id')" "$E2" || return 1
	printf 'since=%s leaves just the second event\n' "$E1_SEQ"
}

a_deletes_bug() {
	recall
	api POST "$TOKEN_A" "/api/artifact/$BUG/delete" || return 1
	want_eq "delete status" "$API_STATUS" 200 || return 1
	want_eq "tombstone" "$(jqv .tombstone)" true || return 1
	if [ "$(jqv .hlc)" -le "$BUG_HLC" ]; then
		printf 'the delete stamped hlc %s, not past the write at %s\n' "$(jqv .hlc)" "$BUG_HLC" >&2
		return 1
	fi
	printf 'tombstoned %s, hlc %s -> %s\n' "$BUG" "$BUG_HLC" "$(jqv .hlc)"
}

tombstone_leaves_list_and_search() {
	recall
	api GET "$TOKEN_A" /api/artifacts || return 1
	want_eq "the tombstoned bug in the list" "$(hits ".id == \"$BUG\"")" 0 || return 1
	api GET "$TOKEN_A" '/api/search?q=zorblatt' || return 1
	want_eq "the tombstoned bug in search" "$(hits)" 0 || return 1
	printf 'gone from both, for its owner as well as for anyone else\n'
}

# ?scope=all is the operator's view of their own node and nobody else's.
scope_all_ignored_for_others() {
	recall
	api GET "$TOKEN_B" '/api/artifacts?scope=all' || return 1
	want_eq "the artifact B was never granted" "$(hits ".id == \"$KEPT\"")" 0 || return 1
	printf 'B asked for everything and got only what B may see\n'
}

scope_all_works_for_the_operator() {
	recall
	api GET "$TOKEN_OP" '/api/artifacts?scope=all' || return 1
	want_eq "the operator sees the pc artifact" "$(hits ".id == \"$KEPT\"")" 1 || return 1
	api GET "$TOKEN_OP" /api/artifacts || return 1
	want_eq "and not without asking" "$(hits ".id == \"$KEPT\"")" 0 || return 1
	printf 'scope=all opens the whole node to the operator, and only on request\n'
}

# --------------------------------------------------------- phase 2 mcp helpers
#
# The MCP checks are driven over the wire as well, for the same reason the
# Phase 1 ones are: what is being tested is what a second agent, holding a
# second token, gets back from a JSON-RPC call - and that is only true if it is
# true over the transport an agent actually connects with.

# mcp METHOD TOKEN [PARAMS] - one JSON-RPC request against the HTTP transport.
# The whole response lands in MCP_BODY. An empty TOKEN sends no Authorization
# header, which is how the initialize and unauthenticated checks are written.
mcp() {
	local method=$1 token=$2 params=${3-}
	[ -n "$params" ] || params='{}'
	local req
	req="$(jq -nc --arg m "$method" --argjson p "$params" \
		'{jsonrpc: "2.0", id: 1, method: $m, params: $p}')" || return 1
	local -a curl_args=(--silent --show-error -X POST
		-H 'Content-Type: application/json' --data-binary "$req")
	if [ -n "$token" ]; then
		curl_args+=(-H "Authorization: Bearer $token")
	fi
	MCP_BODY="$(curl "${curl_args[@]}" "http://127.0.0.1:$MCP_PORT/mcp")" || return 1
}

# rv EXPR - a value out of the last JSON-RPC response.
rv() { printf '%s' "$MCP_BODY" | jq -r "$1"; }

# tool NAME TOKEN [ARGS] - one tools/call. TOOL_JSON holds the tool's output,
# already unwrapped from the MCP content envelope; TOOL_ERR is empty when the
# call succeeded and holds the message when it did not - protocol error or tool
# error, because to the agent asking they are both "it did not happen".
tool() {
	local name=$1 token=$2 args=${3-}
	[ -n "$args" ] || args='{}'
	local params
	params="$(jq -nc --arg n "$name" --argjson a "$args" '{name: $n, arguments: $a}')" || return 1
	mcp tools/call "$token" "$params" || return 1
	TOOL_ERR="$(printf '%s' "$MCP_BODY" |
		jq -r '.error.message // (if .result.isError then .result.content[0].text else "" end)')"
	TOOL_JSON="$(printf '%s' "$MCP_BODY" |
		jq -r 'if .error or .result.isError then "null" else .result.content[0].text end')"
}

# tv EXPR - a value out of the last tool output.
tv() { printf '%s' "$TOOL_JSON" | jq -r "$1"; }

# want_tool NAME TOKEN ARGS - the call has to succeed.
want_tool() {
	tool "$@" || return 1
	if [ -n "$TOOL_ERR" ]; then
		printf '%s failed: %s\n' "$1" "$TOOL_ERR" >&2
		return 1
	fi
}

# want_tool_fails NAME TOKEN ARGS WANT_SUBSTRING - the call has to fail, with a
# message that says what it is allowed to say.
want_tool_fails() {
	local want=$4
	tool "$1" "$2" "$3" || return 1
	if [ -z "$TOOL_ERR" ]; then
		printf '%s was allowed, want it refused: %s\n' "$1" "$TOOL_JSON" >&2
		return 1
	fi
	case "$TOOL_ERR" in
	*"$want"*) ;;
	*)
		printf '%s failed with %q, want something containing %q\n' "$1" "$TOOL_ERR" "$want" >&2
		return 1
		;;
	esac
	printf '%s refused: %s\n' "$1" "$TOOL_ERR"
}

# ------------------------------------------------------------- phase 2 checks

# initialize is the one call that works without a token: a client has to be able
# to find out what this server is, and read its instructions, before it holds a
# credential.
mcp_handshake() {
	mcp initialize "" \
		'{"protocolVersion": "2024-11-05", "capabilities": {},
		  "clientInfo": {"name": "gate", "version": "0"}}' || return 1
	want_eq "protocol version" "$(rv .result.protocolVersion)" 2024-11-05 || return 1
	want_eq "server name" "$(rv .result.serverInfo.name)" flowy || return 1
	if [ -z "$(rv .result.serverInfo.version)" ] || [ "$(rv .result.serverInfo.version)" = null ]; then
		printf 'initialize returned no server version\n' >&2
		return 1
	fi

	local instructions
	instructions="$(rv .result.instructions)"
	if [ "${#instructions}" -lt 500 ]; then
		printf 'initialize returned %s bytes of instructions, want a real document\n' \
			"${#instructions}" >&2
		return 1
	fi
	# The instructions are the point of the endpoint, not decoration: an agent
	# that reads them has to come away knowing the scopes and the tools.
	local word
	for word in personal project shared mem_write mem_search todos; do
		case "$instructions" in
		*"$word"*) ;;
		*)
			printf 'the instructions never mention %s\n' "$word" >&2
			return 1
			;;
		esac
	done
	printf 'flowy %s, protocol %s, %s bytes of instructions\n' \
		"$(rv .result.serverInfo.version)" "$(rv .result.protocolVersion)" "${#instructions}"
}

mcp_tools_list() {
	mcp tools/list "$TOKEN_A" || return 1
	local name
	for name in mem_write mem_read mem_search mem_list todos; do
		want_eq "$name in tools/list" \
			"$(rv "[.result.tools[] | select(.name == \"$name\")] | length")" 1 || return 1
		if [ "$(rv "[.result.tools[] | select(.name == \"$name\" and (.inputSchema.type == \"object\"))] | length")" != 1 ]; then
			printf '%s has no object input schema\n' "$name" >&2
			return 1
		fi
	done
	printf 'tools: %s\n' "$(rv '[.result.tools[].name] | join(", ")')"
}

# The same text, twice: a client that ignores initialize.instructions can read
# the resource instead, and must not get a different document.
mcp_instructions_resource() {
	mcp initialize "" '{}' || return 1
	local from_initialize from_resource
	from_initialize="$(rv .result.instructions)"

	mcp resources/list "$TOKEN_A" || return 1
	want_eq "flowy://instructions is listed" \
		"$(rv '[.result.resources[] | select(.uri == "flowy://instructions")] | length')" 1 || return 1

	mcp resources/read "$TOKEN_A" '{"uri": "flowy://instructions"}' || return 1
	want_eq "resource uri" "$(rv '.result.contents[0].uri')" flowy://instructions || return 1
	from_resource="$(rv '.result.contents[0].text')"
	if [ "$from_resource" != "$from_initialize" ]; then
		printf 'the resource and initialize.instructions disagree (%s vs %s bytes)\n' \
			"${#from_resource}" "${#from_initialize}" >&2
		return 1
	fi
	printf 'flowy://instructions is the same %s bytes initialize returned\n' "${#from_resource}"
}

# No principal, no tools. This is the whole of the authentication story on the
# MCP surface: the token names a (user, agent, project) triple and without one
# there is nobody to filter the store for.
mcp_unauthenticated() {
	tool mem_list "" '{}' || return 1
	want_eq "error code with no token" "$(rv .error.code)" -32001 || return 1
	case "$(rv .error.message)" in
	*unauthenticated*) ;;
	*)
		printf 'the error says %q, which does not say it was unauthenticated\n' "$(rv .error.message)" >&2
		return 1
		;;
	esac

	tool mem_write no-such-token '{"title": "should not land"}' || return 1
	want_eq "error code with an unknown token" "$(rv .error.code)" -32001 || return 1
	printf 'tools/call is refused without a principal: %s\n' "$(rv .error.message)"
}

mcp_unknown_tool() {
	tool no_such_tool "$TOKEN_A" '{}' || return 1
	want_eq "error code" "$(rv .error.code)" -32602 || return 1
	printf 'unknown tool: %s\n' "$(rv .error.message)"
}

# scope=personal is the default and the floor. flimsyquark appears in the body
# and nowhere else, so anything that finds the item found it by searching the
# body.
a_writes_personal_memory() {
	recall
	want_tool mem_write "$TOKEN_A" '{
		"title": "how the gate stands postgres up",
		"body": "initdb into the work directory, trust auth, a flimsyquark of a port",
		"tags": ["gate", "postgres"]
	}' || return 1
	want_eq "type" "$(tv .item.type)" memory || return 1
	want_eq "kind defaults to note" "$(tv .item.kind)" note || return 1
	want_eq "scope defaults to personal" "$(tv .item.visibility)" personal || return 1
	want_eq "a personal item has no project" "$(tv .item.project)" null || return 1
	want_eq "owner" "$(tv .item.owner_user)" "$USER_A" || return 1
	if [ "$(tv .item.hlc)" -le 0 ]; then
		printf 'the item came back with hlc %s, want a stamped clock\n' "$(tv .item.hlc)" >&2
		return 1
	fi
	remember MEM_PERSONAL "$(tv .item.id)"
	printf 'personal memory %s, hlc %s, node %s\n' "$(tv .item.id)" "$(tv .item.hlc)" "$(tv .item.node)"
}

a_searches_own_memory() {
	recall
	want_tool mem_search "$TOKEN_A" '{"q": "flimsyquark"}' || return 1
	want_eq "hits" "$(tv .count)" 1 || return 1
	want_eq "the hit" "$(tv '.items[0].id')" "$MEM_PERSONAL" || return 1
	if ! printf '%s' "$TOOL_JSON" | jq -e '.items[0].rank > 0' >/dev/null; then
		printf 'the hit ranked %s, want a positive score\n' "$(tv '.items[0].rank')" >&2
		return 1
	fi
	printf 'flimsyquark appears only in the body and ranked %s\n' "$(tv '.items[0].rank')"
}

a_reads_own_memory() {
	recall
	want_tool mem_read "$TOKEN_A" "{\"id\": \"$MEM_PERSONAL\"}" || return 1
	want_eq "id" "$(tv .item.id)" "$MEM_PERSONAL" || return 1
	want_eq "tags" "$(tv '.item.tags | join(",")')" gate,postgres || return 1
	printf '%s: %s\n' "$MEM_PERSONAL" "$(tv .item.title)"
}

a_lists_own_memory() {
	recall
	want_tool mem_list "$TOKEN_A" '{"kind": "note"}' || return 1
	want_eq "the item is listed once" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.id == \"$MEM_PERSONAL\")] | length")" 1 || return 1
	if [ "$(printf '%s' "$TOOL_JSON" | jq '[.items[] | select(.type != "memory")] | length')" != 0 ]; then
		printf 'mem_list returned something that is not a memory item\n' >&2
		return 1
	fi
	printf 'mem_list returns %s memory item(s), newest first\n' "$(tv .count)"
}

# The grant the Phase 1 checks issued is still there; this states it again so
# the shared-memory checks below do not depend on reading the section above.
pb_holds_a_grant_on_pa() {
	recall
	api POST "$TOKEN_A" /api/grants '{"from_project": "pb", "to_project": "pa"}' || return 1
	want_eq "grant status" "$API_STATUS" 200 || return 1
	printf 'pb may read pa (grant %s)\n' "$(jqv .id)"
}

# The floor, on the memory surface: B holds a grant on A's project and it still
# does not reach A's personal memory, by id or by search. The message is the
# same one a missing id gets, so B cannot learn that the item exists.
b_cannot_reach_personal_memory() {
	recall
	want_tool_fails mem_read "$TOKEN_B" "{\"id\": \"$MEM_PERSONAL\"}" "no such memory item" || return 1
	want_tool_fails mem_read "$TOKEN_B_AGENT" "{\"id\": \"$MEM_PERSONAL\"}" "no such memory item" || return 1
	want_tool mem_search "$TOKEN_B" '{"q": "flimsyquark"}' || return 1
	want_eq "B's hits for flimsyquark" "$(tv .count)" 0 || return 1
	printf "a grant reaches neither B's read nor B's search of a personal item\n"
}

# And the other half: an item written at scope=shared is exactly what the grant
# is for. wobblethorn is in the body only.
a_writes_shared_memory() {
	recall
	want_tool mem_write "$TOKEN_A" '{
		"title": "handoff: the parser work is half done",
		"body": "the wobblethorn branch parses headers but not continuations",
		"scope": "shared", "kind": "handoff", "tags": ["parser", "handoff"]
	}' || return 1
	want_eq "visibility" "$(tv .item.visibility)" shared || return 1
	want_eq "project" "$(tv .item.project)" pa || return 1
	want_eq "kind" "$(tv .item.kind)" handoff || return 1
	remember MEM_SHARED "$(tv .item.id)"
	printf 'shared memory %s in pa\n' "$(tv .item.id)"
}

# This is the shared-memory proof: a second agent identity, holding a second
# token, in a second project, reads what the first one wrote - through the same
# store, over the same protocol.
b_agent_reads_shared_memory() {
	recall
	want_tool mem_read "$TOKEN_B_AGENT" "{\"id\": \"$MEM_SHARED\"}" || return 1
	want_eq "id" "$(tv .item.id)" "$MEM_SHARED" || return 1
	want_eq "visibility" "$(tv .item.visibility)" shared || return 1
	printf "B's agent reads A's shared memory: %s\n" "$(tv .item.title)"
}

b_agent_searches_shared_memory() {
	recall
	want_tool mem_search "$TOKEN_B_AGENT" '{"q": "wobblethorn"}' || return 1
	want_eq "hits" "$(tv .count)" 1 || return 1
	want_eq "the hit" "$(tv '.items[0].id')" "$MEM_SHARED" || return 1
	want_tool todos "$TOKEN_B_AGENT" '{}' || return 1
	want_eq "the handoff is outstanding work for B too" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.id == \"$MEM_SHARED\")] | length")" 1 || return 1
	printf "B's search finds it, and it shows up in B's todos as a handoff\n"
}

# A todo is outstanding until it is done, and mem_write with an id is how it
# stops being outstanding - without restating the item.
todos_open_and_done() {
	recall
	want_tool mem_write "$TOKEN_A" '{
		"title": "mount the artifacts over FUSE",
		"body": "phase 3 work, noted so it is not lost",
		"scope": "project", "kind": "todo"
	}' || return 1
	local todo
	todo="$(tv .item.id)"
	want_eq "project" "$(tv .item.project)" pa || return 1

	want_tool todos "$TOKEN_A" '{}' || return 1
	want_eq "the todo is outstanding" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.id == \"$todo\")] | length")" 1 || return 1
	if [ "$(printf '%s' "$TOOL_JSON" | jq '[.items[] | select(.kind == "note")] | length')" != 0 ]; then
		printf 'todos returned a note, which is not outstanding work\n' >&2
		return 1
	fi

	want_tool mem_write "$TOKEN_A" "{\"id\": \"$todo\", \"status\": \"done\"}" || return 1
	want_eq "the title survived an update that did not restate it" \
		"$(tv .item.title)" "mount the artifacts over FUSE" || return 1
	want_eq "kind survived too" "$(tv .item.kind)" todo || return 1

	want_tool todos "$TOKEN_A" '{}' || return 1
	want_eq "the done todo is gone from todos" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.id == \"$todo\")] | length")" 0 || return 1
	remember MEM_TODO "$todo"
	printf 'todo %s: outstanding, then done, then out of the list\n' "$todo"
}

# The other transport, and the same handlers behind it: a client that launches
# `flowy mcp` as a subprocess speaks newline-delimited JSON-RPC over its pipes.
# The token comes from the environment there, because there are no headers.
stdio_transport() {
	recall
	local init notif call out line1 line2
	# One message per line, so every one of these is built compact: a newline
	# inside a message is a framing error on this transport, not whitespace.
	init="$(jq -nc '{jsonrpc: "2.0", id: 1, method: "initialize",
	                 params: {protocolVersion: "2024-11-05", capabilities: {},
	                          clientInfo: {name: "gate", version: "0"}}}')" || return 1
	# A notification has no id and must not be answered at all.
	notif="$(jq -nc '{jsonrpc: "2.0", method: "notifications/initialized"}')" || return 1
	call="$(jq -nc '{jsonrpc: "2.0", id: 2, method: "tools/call",
	                 params: {name: "mem_write",
	                          arguments: {title: "written over the stdio transport",
	                                      body: "this one carries the word splutterfig",
	                                      scope: "personal", tags: ["stdio"]}}}')" || return 1

	out="$(printf '%s\n%s\n%s\n' "$init" "$notif" "$call" |
		FLOWY_TOKEN="$TOKEN_A" ./flowy mcp 2>"$WORK/mcp-stdio.log")" || {
		printf 'flowy mcp exited non-zero:\n' >&2
		cat "$WORK/mcp-stdio.log" >&2
		return 1
	}
	want_eq "one response per request, and none for the notification" \
		"$(printf '%s\n' "$out" | wc -l)" 2 || return 1

	line1="$(printf '%s\n' "$out" | sed -n 1p)"
	line2="$(printf '%s\n' "$out" | sed -n 2p)"
	want_eq "the first response is the handshake" \
		"$(printf '%s' "$line1" | jq -r .result.serverInfo.name)" flowy || return 1
	want_eq "it carries the same protocol version" \
		"$(printf '%s' "$line1" | jq -r .result.protocolVersion)" 2024-11-05 || return 1
	if [ "$(printf '%s' "$line1" | jq -r '.result.instructions | length')" -lt 500 ]; then
		printf 'the stdio handshake returned no instructions\n' >&2
		return 1
	fi

	local item
	item="$(printf '%s' "$line2" | jq -r '.result.content[0].text' | jq -r .item.id)"
	if [ -z "$item" ] || [ "$item" = null ]; then
		printf 'the stdio tools/call returned no item:\n%s\n' "$line2" >&2
		return 1
	fi
	remember MEM_STDIO "$item"
	printf 'stdio: handshake and mem_write %s, over pipes\n' "$item"
}

# One store. The item was written by a process talking over pipes and is read
# back by a client talking JSON-RPC to a socket - which is the whole claim the
# MCP endpoint makes.
one_store_both_transports() {
	recall
	want_tool mem_search "$TOKEN_A" '{"q": "splutterfig"}' || return 1
	want_eq "hits over http for what stdio wrote" "$(tv .count)" 1 || return 1
	want_eq "the hit" "$(tv '.items[0].id')" "$MEM_STDIO" || return 1
	want_tool mem_read "$TOKEN_A" "{\"id\": \"$MEM_STDIO\"}" || return 1
	want_eq "title" "$(tv .item.title)" "written over the stdio transport" || return 1
	printf 'the stdio write is readable over http: %s\n' "$MEM_STDIO"
}

# ---------------------------------------------------------------- environment

say "environment"
for tool in go curl jq; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		printf '%s is not on PATH\n' "$tool" >&2
		exit 1
	fi
done
PG_BIN="$(find_pg_bin)"
printf 'go:       %s\n' "$(go version)"
printf 'postgres: %s\n' "$("$PG_BIN/postgres" --version)"
printf 'work:     %s\n' "$WORK"

# The gate must not reach the network: the module's one dependency is vendored.
if [ -d "$ROOT/vendor" ]; then
	export GOFLAGS="${GOFLAGS:-} -mod=vendor"
fi

# ------------------------------------------------------------------- postgres

say "postgres"
mkdir -p "$PGSOCK"
PGUSER="$(id -un)"
export PGUSER
if ! "$PG_BIN/initdb" -D "$PGDATA" -U "$PGUSER" -A trust -E UTF8 --locale=C --no-sync \
	>"$WORK/initdb.log" 2>&1; then
	cat "$WORK/initdb.log" >&2
	exit 1
fi

PGPORT="$(free_port 54320)"
export PGPORT
export PGHOST=127.0.0.1
if ! "$PG_BIN/pg_ctl" -D "$PGDATA" -l "$PGLOG" -w -t 60 \
	-o "-p $PGPORT -k $PGSOCK -h 127.0.0.1 -c fsync=off -c full_page_writes=off" \
	start >"$WORK/pg_ctl.log" 2>&1; then
	cat "$WORK/pg_ctl.log" >&2
	[ -f "$PGLOG" ] && cat "$PGLOG" >&2
	exit 1
fi

"$PG_BIN/createdb" "$DBNAME"
export PGDATABASE="$DBNAME"
# CREATE ... IF NOT EXISTS is chatty on a reload; warnings still come through.
export PGOPTIONS='-c client_min_messages=warning'
export DATABASE_URL="postgres://$PGUSER@127.0.0.1:$PGPORT/$DBNAME?sslmode=disable"
printf 'started on port %s\nDATABASE_URL=%s\n' "$PGPORT" "$DATABASE_URL"

# --------------------------------------------------------------------- checks

say "build and schema"
check "schema.sql loads" psql -v ON_ERROR_STOP=1 -q -f "$ROOT/schema.sql"
check "schema.sql reloads cleanly" psql -v ON_ERROR_STOP=1 -q -f "$ROOT/schema.sql"
check "go build" go build -o "$ROOT/flowy" .
check "go build ./cmd/smoke" go build -o "$WORK/smoke" ./cmd/smoke
check "gofmt" gofmt_clean
check "go vet" go vet ./...

say "unit tests"
# -count=1 so the live store tests really talk to the database this run rather
# than replaying a cached result from an earlier one.
check "go test ./..." go test -count=1 ./...

say "principals"
# Seeded before the node starts, because who the operator is is a flag on the
# process and the operator's id is minted here.
: >"$WORK/ids"
# The seed prints NAME=value lines: this shell needs them for FLOWY_OPERATOR,
# and the checks - which run in command substitutions of their own - read them
# back out of the same file everything else is remembered in.
if seed_out="$("$WORK/smoke" seed 2>&1)"; then
	printf '%s\n' "$seed_out" >>"$WORK/ids"
	# shellcheck source=/dev/null
	. "$WORK/ids"
else
	printf 'FAIL seed principals\n%s\n' "$seed_out" | indent
	failed=$((failed + 1))
fi
check "two users, their agents and their tokens are seeded" seeded_ok

say "live node"
HTTP_PORT="$(free_port 8787)"
DATABASE_URL="$DATABASE_URL" FLOWY_NODE=gate FLOWY_OPERATOR="${USER_OP:-}" \
	./flowy serve -addr "127.0.0.1:$HTTP_PORT" >"$SERVE_LOG" 2>&1 &
SERVE_PID=$!
printf 'flowy serve pid %s on 127.0.0.1:%s\n' "$SERVE_PID" "$HTTP_PORT"

check "flowy serve answers /healthz" "$WORK/smoke" healthz "http://127.0.0.1:$HTTP_PORT/healthz"
check "healthz reports the spine tables" \
	"$WORK/smoke" healthz "http://127.0.0.1:$HTTP_PORT/healthz?counts=1"
check "spine tables exist" "$WORK/smoke" schema

say "subcommand stubs"
for sub in fuse sync; do
	check "flowy $sub is a stub" stub_says "$sub"
done

say "identifiers and clocks"
check "10000 ulids unique and strictly increasing" "$WORK/smoke" ulid
check "hlc monotonic across 8 goroutines x 5000" "$WORK/smoke" hlc

say "database round trips"
check "user, agent, artifact and event round-trip with parents" "$WORK/smoke" roundtrip
check "personal artifact round-trips with a NULL project" "$WORK/smoke" personal

# A second client, over the wire, sees what the node wrote - including the
# text[] shape of the DAG.
check "psql sees a two-parent event" psql_counts \
	"SELECT count(*) FROM events WHERE array_length(parents, 1) = 2"
check "psql sees the bug artifact in project flowy" psql_counts \
	"SELECT count(*) FROM artifacts WHERE type = 'bug' AND project = 'flowy'"
check "psql sees a personal artifact with no project" psql_counts \
	"SELECT count(*) FROM artifacts WHERE project IS NULL AND visibility = 'personal'"

# ------------------------------------------------------------------- phase 1

say "identity"
check "a request with no token is 401" want_status 401 GET "" /api/artifacts
check "a request with an unknown token is 401" want_status 401 GET no-such-token /api/artifacts
check "an unauthenticated write is 401 too" want_status 401 POST "" /api/artifacts '{"type":"note"}'
check "a token resolves to its user and home project" whoami_is_a
check "an agent token inherits its user and project" agent_token_inherits

say "artifacts, and what the owner sees"
check "A creates a bug in pa" a_creates_bug
check "A reads it back" a_reads_bug
check "A finds it by a word that appears only in the discovery" a_searches_discovery_word
check "A sees it in the list" a_lists_bug

say "the project boundary"
check "B gets 404, not 403, on an artifact in pa" b_cannot_read_bug
check "B's list does not contain it" b_list_omits_bug
check "B's search does not find it" b_search_misses_bug

say "grants"
check "A opens pa up to pb" a_grants_pb_read_of_pa
check "B can now read the bug" b_reads_bug_after_grant
check "B's search now finds it" b_searches_bug_after_grant

say "the personal floor"
check "A creates a personal artifact with no project" a_creates_personal
check "B cannot read it, grant or no grant" b_cannot_read_personal
check "B cannot reach it through search either" b_cannot_search_personal
check "A's own agent can read it" a_agent_reads_personal

say "per-artifact shares"
check "A creates two artifacts in pc, which nobody has a grant into" a_creates_two_in_pc
check "B cannot read either of them" b_cannot_read_either_pc
check "A shares exactly one of them with B" a_shares_one_artifact
check "B reads the shared one and not the other" b_reads_only_the_shared_one
check "B's search finds the shared one and not the other" b_searches_only_the_shared_one
check "a principal cannot write into a project it is not acting in" cross_project_write_refused

say "the event log"
check "A appends an event and then a child of it" append_thread
check "the thread reads back in order with its parents" thread_reads_back_in_order
check "since= pages the log by seq_hlc" since_pages_the_log

say "tombstones"
check "A deletes the bug and the clock moves past the write" a_deletes_bug
check "the tombstoned artifact is gone from the list and from search" tombstone_leaves_list_and_search

say "scope=all"
check "scope=all does nothing for a principal who is not the operator" scope_all_ignored_for_others
check "scope=all shows the operator the whole node" scope_all_works_for_the_operator

# ------------------------------------------------------------------- phase 2
#
# Shared memory, over MCP. The same store and the same permission filter the
# Phase 1 checks just walked, reached the way an agent reaches it.

say "mcp server"
MCP_PORT="$(free_port 8788)"
DATABASE_URL="$DATABASE_URL" FLOWY_NODE=gate \
	./flowy mcp --http "127.0.0.1:$MCP_PORT" >"$MCP_LOG" 2>&1 &
MCP_PID=$!
printf 'flowy mcp pid %s on 127.0.0.1:%s\n' "$MCP_PID" "$MCP_PORT"
check "flowy mcp --http comes up" "$WORK/smoke" healthz "http://127.0.0.1:$MCP_PORT/healthz"

say "the mcp handshake"
check "initialize answers without a token, with serverInfo and instructions" mcp_handshake
check "tools/list offers the five memory tools" mcp_tools_list
check "flowy://instructions serves the same document" mcp_instructions_resource
check "tools/call without a principal is refused" mcp_unauthenticated
check "an unknown tool is refused" mcp_unknown_tool

say "shared memory: writing and recalling"
check "A writes a memory item, personal by default" a_writes_personal_memory
check "A finds it by a word that appears only in the body" a_searches_own_memory
check "A reads it back by id" a_reads_own_memory
check "A lists it, and mem_list returns nothing but memory" a_lists_own_memory

say "shared memory: the scope gate"
check "pb holds a read grant on pa" pb_holds_a_grant_on_pa
check "B cannot read or search A's personal memory, grant or no grant" b_cannot_reach_personal_memory
check "A writes a memory at scope=shared" a_writes_shared_memory
check "a second agent identity reads it" b_agent_reads_shared_memory
check "and finds it by search, and in its todos" b_agent_searches_shared_memory

say "todos"
check "a todo is outstanding until it is done" todos_open_and_done

say "the stdio transport"
check "flowy mcp speaks JSON-RPC over pipes" stdio_transport
check "what stdio wrote, http reads: one store" one_store_both_transports

check "the mcp server survived the run" kill -0 "$MCP_PID"

say "database, as a second client"
check "psql sees the seeded tokens" psql_counts \
	"SELECT count(*) FROM tokens WHERE project IN ('pa', 'pb', 'pc') OR agent_id IS NOT NULL"
check "psql sees the grants the run issued" psql_counts \
	"SELECT count(*) FROM grants WHERE (from_project = 'pb' AND to_project = 'pa') OR artifact IS NOT NULL"
check "psql sees the tombstone still in the table, not deleted" psql_counts \
	"SELECT count(*) FROM artifacts WHERE tombstone = true AND type = 'bug'"
check "psql sees the search vector populated" psql_counts \
	"SELECT count(*) FROM artifacts WHERE search @@ plainto_tsquery('english', 'flimberwock')"
check "psql sees the memory the mcp endpoint wrote" psql_counts \
	"SELECT count(*) FROM artifacts WHERE type = 'memory' AND kind IN ('note', 'todo', 'handoff')"
check "psql sees a shared memory item in pa" psql_counts \
	"SELECT count(*) FROM artifacts WHERE type = 'memory' AND visibility = 'shared' AND project = 'pa'"
check "psql sees the memory writes in the event log" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'memory.write'"

check "node survived the run" kill -0 "$SERVE_PID"

# ------------------------------------------------------------------- verdict

say "result"
if [ "$failed" -ne 0 ]; then
	if [ -f "$SERVE_LOG" ]; then
		printf 'serve log:\n'
		indent <"$SERVE_LOG"
	fi
	if [ -f "$MCP_LOG" ]; then
		printf 'mcp log:\n'
		indent <"$MCP_LOG"
	fi
fi
printf 'passed: %d failed: %d\n' "$passed" "$failed"
[ "$failed" -eq 0 ] || exit 1

# The tree that gets copied out has to be the tree that was tested, so the last
# word is git's. Two ways it can fail: nothing was ever committed, or a tracked
# file was changed after the last commit and the green run above describes a
# tree that is not the one on disk. Untracked files do not count - the harness
# writes its own verify artifacts (.firecode-*) into the project root while this
# script is running, and those are not part of what was tested.
# shellcheck disable=SC2015  # intended: the block runs if any of the three fails
git rev-parse HEAD >/dev/null 2>&1 && git diff --quiet && git diff --cached --quiet || {
	echo "FAIL: uncommitted tracked changes"
	exit 1
}
