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
#
# Phase 5 adds federation, and it needs two of everything: two Postgres clusters,
# two `flowy serve` processes, two node names. Nothing about replication can be
# tested inside one database - a merge that is only ever asked to merge a row
# with itself is not a merge - so the gate stands up a second node beside the
# first and drives the real `flowy sync` between them.
#
# Phase 6 adds the forge bridge, and runs it against MockForge: there is no
# GitHub in here, no credential and no network, and a gate that needed one would
# be a gate that leaves issues in somebody's repository. So the node is started
# with FLOWY_FORGE=mock and the checks drive the fake through the mock's own
# control routes. A `gh` that records being run is put on PATH first, so that
# selecting the mock is a choice rather than the only option - and so that
# "GhClient was not invoked" is a fact the gate can check rather than assume.

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
# Phase 5 runs two more nodes, each with a cluster of its own.
PGDATA5A="$WORK/pgdata5a"
PGDATA5B="$WORK/pgdata5b"
PGSOCK5A="$WORK/sock5a"
PGSOCK5B="$WORK/sock5b"
NODE5A_LOG="$WORK/nodeA.log"
NODE5B_LOG="$WORK/nodeB.log"
# Phase 6: where the gate's fake gh writes when something runs it.
GH_CANARY="$WORK/gh-invoked"
readonly ROOT WORK PGDATA PGSOCK PGLOG SERVE_LOG MCP_LOG DBNAME
readonly PGDATA5A PGDATA5B PGSOCK5A PGSOCK5B NODE5A_LOG NODE5B_LOG GH_CANARY

PG_BIN=""
PGPORT=""
HTTP_PORT=""
MCP_PORT=""
SERVE_PID=""
MCP_PID=""
NODE5A_PID=""
NODE5B_PID=""
passed=0
failed=0

cleanup() {
	local status=$?
	set +e
	local pid data
	for pid in "$SERVE_PID" "$MCP_PID" "$NODE5A_PID" "$NODE5B_PID"; do
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null
			wait "$pid" 2>/dev/null
		fi
	done
	for data in "$PGDATA" "$PGDATA5A" "$PGDATA5B"; do
		if [ -n "$PG_BIN" ] && [ -d "$data" ]; then
			"$PG_BIN/pg_ctl" -D "$data" -m immediate -w stop >/dev/null 2>&1
		fi
	done
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

# ------------------------------------------------------------ phase 3 helpers
#
# Chat is the event log seen from the side, so these checks are written the same
# way the Phase 1 log checks are: over the wire, as two principals, asserting
# what each of them gets back rather than what the store holds.

# chat_len [SELECT] - how many chat events the last response returned,
# optionally narrowed by a jq select expression.
chat_len() {
	if [ $# -eq 0 ]; then
		printf '%s' "$API_BODY" | jq '.events | length'
	else
		printf '%s' "$API_BODY" | jq "[.events[] | select($1)] | length"
	fi
}

# say_in_background ROOM TOKEN BODY - posts a message after a second, from a
# subshell, so a long poll has something to wake up for. Prints the pid.
say_in_background() {
	local room=$1 token=$2 body=$3
	(
		sleep 1
		curl --silent --show-error -X POST \
			-H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
			--data-binary "$(jq -nc --arg b "$body" '{body: $b}')" \
			"http://127.0.0.1:$HTTP_PORT/api/chat/$room/say" >/dev/null
	) &
	printf '%s\n' "$!"
}

# ------------------------------------------------------------- phase 3 checks

# A human posts as themselves, and a reply names the message it answers: that
# edge is the whole DAG, and it is what a branch is made of.
a_says_two_messages() {
	recall
	api POST "$TOKEN_A" /api/chat/general/say '{"body": "first thing said in the room"}' || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "type" "$(jqv .type)" chat || return 1
	want_eq "room" "$(jqv .room)" general || return 1
	want_eq "the message landed in pa" "$(jqv .project)" pa || return 1
	want_eq "a human posts as the user" "$(jqv .actor)" "$USER_A" || return 1
	want_eq "and is marked as one" "$(jqv .meta.actor_kind)" user || return 1
	want_eq "an opening message has no parents" "$(jqv '.parents | length')" 0 || return 1
	local first thread
	first="$(jqv .id)"
	thread="$(jqv .thread)"
	if [ -z "$thread" ] || [ "$thread" = null ]; then
		printf 'the message got no thread\n' >&2
		return 1
	fi
	remember CHAT_M1 "$first"
	remember CHAT_THREAD "$thread"
	remember CHAT_M1_SEQ "$(jqv .seq_hlc)"

	api POST "$TOKEN_A" /api/chat/general/say \
		"{\"body\": \"a reply that names the first\", \"parents\": [\"$first\"]}" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "the reply's parent" "$(jqv '.parents | join(",")')" "$first" || return 1
	want_eq "a reply stays in the thread it answers" "$(jqv .thread)" "$thread" || return 1
	remember CHAT_M2 "$(jqv .id)"
	remember CHAT_M2_SEQ "$(jqv .seq_hlc)"
	printf 'thread %s: %s <- %s\n' "$thread" "$first" "$(jqv .id)"
}

room_reads_back_in_order() {
	recall
	api GET "$TOKEN_A" /api/chat/general || return 1
	want_eq "room status" "$API_STATUS" 200 || return 1
	want_eq "room" "$(jqv .room)" general || return 1
	want_eq "messages in the room" "$(chat_len)" 2 || return 1
	want_eq "first" "$(jqv '.events[0].id')" "$CHAT_M1" || return 1
	want_eq "second" "$(jqv '.events[1].id')" "$CHAT_M2" || return 1
	want_eq "the edge survived the round trip" \
		"$(jqv '.events[1].parents | join(",")')" "$CHAT_M1" || return 1
	if [ "$(jqv '.events[1].seq_hlc')" -le "$(jqv '.events[0].seq_hlc')" ]; then
		printf 'the reply did not advance seq_hlc\n' >&2
		return 1
	fi
	want_eq "the cursor to ask with next" "$(jqv .cursor)" "$CHAT_M2_SEQ" || return 1
	printf 'two messages, in seq_hlc order, cursor %s\n' "$(jqv .cursor)"
}

# The same room, the same project, a different kind of speaker.
an_agent_says_one() {
	recall
	api POST "$TOKEN_A_AGENT" /api/chat/general/say \
		'{"body": "the agent answering in the same room"}' || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "an agent posts as the agent" "$(jqv .actor)" "$AGENT_A" || return 1
	want_eq "and is marked as one" "$(jqv .meta.actor_kind)" agent || return 1
	want_eq "with the person it works for still on the message" \
		"$(jqv .meta.actor_user)" "$USER_A" || return 1
	remember CHAT_M3 "$(jqv .id)"
	remember CHAT_M3_SEQ "$(jqv .seq_hlc)"
	printf 'agent %s said %s\n' "$AGENT_A" "$(jqv .id)"
}

human_and_agent_are_distinguishable() {
	recall
	api GET "$TOKEN_A" /api/chat/general || return 1
	want_eq "messages in the room" "$(chat_len)" 3 || return 1
	want_eq "said by the human" "$(chat_len '.meta.actor_kind == "user"')" 2 || return 1
	want_eq "said by the agent" "$(chat_len '.meta.actor_kind == "agent"')" 1 || return 1
	want_eq "the agent's message is not attributed to the person" \
		"$(chat_len ".meta.actor_kind == \"agent\" and .actor == \"$USER_A\"")" 0 || return 1
	printf 'two human messages and one agent message, told apart by actor and meta\n'
}

wait_returns_only_what_is_newer() {
	recall
	api GET "$TOKEN_A" "/api/chat/general/wait?cursor=$CHAT_M2_SEQ" || return 1
	want_eq "wait status" "$API_STATUS" 200 || return 1
	want_eq "messages after the cursor" "$(chat_len)" 1 || return 1
	want_eq "which one" "$(jqv '.events[0].id')" "$CHAT_M3" || return 1
	want_eq "the cursor moved to it" "$(jqv .cursor)" "$CHAT_M3_SEQ" || return 1
	printf 'cursor %s leaves exactly the agent message\n' "$CHAT_M2_SEQ"
}

# The watcher contract, first half: a poll that is caught up blocks, and returns
# as soon as somebody says something.
wait_blocks_until_something_is_said() {
	recall
	local poster start elapsed
	poster="$(say_in_background general "$TOKEN_A" "said while the watcher was waiting")"
	start=$SECONDS
	api GET "$TOKEN_A" "/api/chat/general/wait?cursor=$CHAT_M3_SEQ" || return 1
	elapsed=$((SECONDS - start))
	wait "$poster" 2>/dev/null || true

	want_eq "wait status" "$API_STATUS" 200 || return 1
	want_eq "messages the watcher woke up for" "$(chat_len)" 1 || return 1
	want_eq "what it heard" "$(jqv '.events[0].body')" "said while the watcher was waiting" || return 1
	if [ "$elapsed" -ge 20 ]; then
		printf 'the poll took %ss, so it timed out rather than woke up\n' "$elapsed" >&2
		return 1
	fi
	remember CHAT_M4_SEQ "$(jqv .cursor)"
	printf 'woke after %ss with the message that was posted mid-poll\n' "$elapsed"
}

# The other half: the window is finite and the poll returns empty rather than
# hanging on the client forever.
wait_returns_on_timeout() {
	recall
	local start elapsed
	start=$SECONDS
	api GET "$TOKEN_A" '/api/chat/quiet/wait?cursor=0&window=2' || return 1
	elapsed=$((SECONDS - start))
	want_eq "wait status" "$API_STATUS" 200 || return 1
	want_eq "messages in a room nobody wrote to" "$(chat_len)" 0 || return 1
	want_eq "the cursor is handed straight back" "$(jqv .cursor)" 0 || return 1
	if [ "$elapsed" -lt 1 ] || [ "$elapsed" -gt 15 ]; then
		printf 'a 2s window returned after %ss\n' "$elapsed" >&2
		return 1
	fi
	printf 'empty after %ss, which is the window, not an error\n' "$elapsed"
}

# An inbox is what you have not seen, so it is everything you may read that you
# did not write.
inbox_excludes_the_callers_own() {
	recall
	api GET "$TOKEN_A" '/api/inbox?since=0' || return 1
	want_eq "inbox status" "$API_STATUS" 200 || return 1
	want_eq "A's own messages in A's inbox" "$(chat_len ".actor == \"$USER_A\"")" 0 || return 1
	want_eq "the agent's message in A's inbox" "$(chat_len ".id == \"$CHAT_M3\"")" 1 || return 1
	want_eq "everything in an inbox is chat" "$(chat_len '.type != "chat"')" 0 || return 1
	printf 'A hears the agent and not themselves\n'
}

# An agent token is the person and the agent at once, so its inbox drops both.
agent_inbox_excludes_both_identities() {
	recall
	api GET "$TOKEN_A_AGENT" '/api/inbox?since=0' || return 1
	want_eq "inbox status" "$API_STATUS" 200 || return 1
	want_eq "the agent's own message" "$(chat_len ".id == \"$CHAT_M3\"")" 0 || return 1
	want_eq "its user's messages" "$(chat_len ".actor == \"$USER_A\"")" 0 || return 1
	printf "the agent's inbox holds none of its own work\n"
}

# A room is scoped by project, not by name and not by person: the same user, in
# a project with no grant into pa, gets nothing - and their own room called
# general is a different room.
another_project_sees_none_of_the_room() {
	recall
	api GET "$TOKEN_A_PC" /api/chat/general || return 1
	want_eq "room status" "$API_STATUS" 200 || return 1
	want_eq "pa's messages, read from pc" "$(chat_len)" 0 || return 1

	api POST "$TOKEN_A_PC" /api/chat/general/say '{"body": "a different room of the same name"}' || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "it landed in pc" "$(jqv .project)" pc || return 1
	# Local, not recalled: remember only writes the file the next check reads.
	local pc_message
	pc_message="$(jqv .id)"
	remember CHAT_PC "$pc_message"

	api GET "$TOKEN_A_PC" /api/chat/general || return 1
	want_eq "what pc's general holds" "$(chat_len)" 1 || return 1
	want_eq "which is only its own" "$(jqv '.events[0].id')" "$pc_message" || return 1

	api GET "$TOKEN_A" /api/chat/general || return 1
	want_eq "and pa cannot see it either" "$(chat_len ".id == \"$pc_message\"")" 0 || return 1
	printf 'two rooms called general, one per project, neither reading the other\n'
}

# The positive control: B is in another project too, but holds the grant Phase 1
# issued, so the room is readable exactly as far as the grant reaches.
a_granted_project_does_see_the_room() {
	recall
	api GET "$TOKEN_B" /api/chat/general || return 1
	want_eq "room status" "$API_STATUS" 200 || return 1
	if [ "$(chat_len)" -lt 3 ]; then
		printf "B holds a grant into pa and sees %s of pa's messages\n" "$(chat_len)" >&2
		return 1
	fi
	want_eq "and not pc's room of the same name" "$(chat_len ".id == \"$CHAT_PC\"")" 0 || return 1
	printf 'the grant reaches the room, and stops at the project it names\n'
}

# ---------------------------------------------------- phase 3 console helpers

# The three steps of the frontend build, each its own check so a failure names
# which one. They run in the command substitution `check` puts them in, so the
# cd is theirs alone.
npm_ci() {
	cd "$ROOT/web" || return 1
	npm ci --no-audit --no-fund --prefer-offline --loglevel=error
	printf 'installed %s packages from package-lock.json\n' \
		"$(find node_modules -maxdepth 2 -name package.json | wc -l)"
}

biome_check() {
	cd "$ROOT/web" || return 1
	npx --no-install @biomejs/biome check .
}

npm_build() {
	cd "$ROOT/web" || return 1
	npm run build
}

# The built bundle, mounted in a dom. An index that is served and a bundle that
# throws on mount look identical from the outside, so this one runs the app.
console_mounts() {
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs
}

# The same mount, signed in, against the live node: the console fetches the room
# over the API and renders what Phase 3's chat checks put in it.
console_renders_the_room() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"first thing said in the room"
}

# www PATH - a plain browser-style GET of the console, with no token: the app
# has to load before it can ask for one. Status lands in WWW_STATUS, body in
# WWW_BODY and the content type in WWW_TYPE.
www() {
	local out
	out="$(curl --silent --show-error -w '\n%{http_code} %{content_type}' \
		"http://127.0.0.1:$HTTP_PORT$1")" || return 1
	local tail="${out##*$'\n'}"
	WWW_BODY="${out%$'\n'*}"
	WWW_STATUS="${tail%% *}"
	WWW_TYPE="${tail#* }"
}

# bundle_ref prints the hashed script the index loads.
bundle_ref() {
	printf '%s' "$1" | grep -o '/assets/[A-Za-z0-9_.-]*\.js' | head -n 1
}

# ------------------------------------------------------ phase 3 console checks

# The build is a real one: an index that loads a hashed bundle, not a shell that
# would still be there if vite had produced nothing.
console_build_is_hashed() {
	local index="$ROOT/web/dist/index.html" ref
	if [ ! -f "$index" ]; then
		printf 'web/dist/index.html does not exist; the frontend build did not run\n' >&2
		return 1
	fi
	if ! grep -q 'id="root"' "$index"; then
		printf 'web/dist/index.html has no app root\n' >&2
		return 1
	fi
	ref="$(bundle_ref "$(cat "$index")")"
	if [ -z "$ref" ]; then
		printf 'web/dist/index.html references no hashed js asset:\n' >&2
		cat "$index" >&2
		return 1
	fi
	if [ ! -f "$ROOT/web/dist$ref" ]; then
		printf 'the index references %s, which is not in the build\n' "$ref" >&2
		return 1
	fi
	printf 'index.html -> %s (%s bytes)\n' "$ref" "$(wc -c <"$ROOT/web/dist$ref")"
}

serves_the_console_at_root() {
	www / || return 1
	want_eq "status" "$WWW_STATUS" 200 || return 1
	case "$WWW_TYPE" in
	text/html*) ;;
	*)
		printf '/ came back as %q, want text/html\n' "$WWW_TYPE" >&2
		return 1
		;;
	esac
	if ! printf '%s' "$WWW_BODY" | grep -q 'id="root"'; then
		printf '/ served something without the app root:\n%s\n' "$WWW_BODY" >&2
		return 1
	fi
	local ref
	ref="$(bundle_ref "$WWW_BODY")"
	if [ -z "$ref" ]; then
		printf '/ served an index that loads no bundle:\n%s\n' "$WWW_BODY" >&2
		return 1
	fi
	remember BUNDLE "$ref"
	printf '/ -> 200 %s, loading %s\n' "$WWW_TYPE" "$ref"
}

# The deep link is the point of routing by path: /chat/general is a route in the
# app, and a reload of it has to come back as the app.
spa_fallback_serves_the_same_index() {
	recall
	local root_body
	www / || return 1
	root_body="$WWW_BODY"
	www /chat/general || return 1
	want_eq "status" "$WWW_STATUS" 200 || return 1
	want_eq "body" "$WWW_BODY" "$root_body" || return 1
	www /p/pa/bug/01H || return 1
	want_eq "a deep artifact link is the app too" "$WWW_STATUS" 200 || return 1
	www /metrics || return 1
	want_eq "and so is a route that only renders a stub" "$WWW_STATUS" 200 || return 1
	printf 'deep links survive a reload\n'
}

console_bundle_is_served() {
	recall
	www "$BUNDLE" || return 1
	want_eq "status" "$WWW_STATUS" 200 || return 1
	case "$WWW_TYPE" in
	*javascript*) ;;
	*)
		printf '%s came back as %q, want a javascript type\n' "$BUNDLE" "$WWW_TYPE" >&2
		return 1
		;;
	esac
	printf '%s -> 200 %s\n' "$BUNDLE" "$WWW_TYPE"
}

# The fallback stops at the API. A client that asked for JSON and got the app
# back with a 200 would have to parse HTML to find out it had a typo.
unknown_api_paths_still_404() {
	recall
	want_status 404 GET "$TOKEN_A" /api/does-not-exist || return 1
	if [ "$(jqv .error)" = null ]; then
		printf 'the API 404 was not JSON:\n%s\n' "$API_BODY" >&2
		return 1
	fi
	want_status 401 GET "" /api/does-not-exist || return 1
	printf 'unknown api paths 404 as json, and 401 first without a token\n'
}

# ------------------------------------------------------------ phase 4 helpers
#
# Assignment, delegation and the issue lifecycle, driven over HTTP the way the
# permission checks are: what is being tested is what the other side of a
# handoff can see and do, and that is only true if it is true over the wire.

# new_artifact TOKEN TYPE TITLE - creates one and prints its id.
new_artifact() {
	local token=$1 type=$2 title=$3
	api POST "$token" /api/artifacts \
		"$(jq -nc --arg t "$type" --arg ti "$title" '{type: $t, title: $ti, body: $ti}')" || return 1
	if [ "$API_STATUS" != 200 ]; then
		printf 'creating a %s came back %s:\n%s\n' "$type" "$API_STATUS" "$API_BODY" >&2
		return 1
	fi
	jqv .id
}

# assign_as TOKEN ARTIFACT TO_USER [MESSAGE] - one assignment. The response
# lands in API_BODY like any other.
assign_as() {
	local token=$1 artifact=$2 to=$3 msg=${4-}
	local body
	body="$(jq -nc --arg a "$artifact" --arg u "$to" --arg n "$msg" \
		'{artifact: $a, to_user: $u} + (if $n == "" then {} else {note: $n} end)')"
	api POST "$token" /api/assign "$body"
}

# move_status TOKEN ARTIFACT STATUS - one lifecycle transition.
move_status() {
	api POST "$1" "/api/artifact/$2/status" "$(jq -nc --arg s "$3" '{status: $s}')"
}

# task_state TOKEN TASK STATE - one task state move.
task_state() {
	api POST "$1" "/api/task/$2/state" "$(jq -nc --arg s "$3" '{state: $s}')"
}

# ------------------------------------------------------------- phase 4 checks

# The artifact the handoff is about. It lives in pc, which nobody holds a grant
# into - the pb -> pa grant Phase 1 issued would otherwise be what lets B read
# it, and then the assignment would prove nothing.
a_creates_the_gearbox_bug() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the gearbox whines under load")" || return 1
	remember GEARBOX "$id"
	printf 'bug %s in pc\n' "$id"
}

b_cannot_read_the_gearbox() {
	recall
	want_status 404 GET "$TOKEN_B" "/api/artifact/$GEARBOX"
}

# One request writes three rows: the share, the task, and the message that opens
# the thread. All three come back, so a client knows what it just created.
a_assigns_the_gearbox_to_b() {
	recall
	assign_as "$TOKEN_A_PC" "$GEARBOX" "$USER_B" "please take the gearbox" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the task is about the gearbox" "$(jqv .artifact)" "$GEARBOX" || return 1
	want_eq "handed over by A" "$(jqv .from_user)" "$USER_A" || return 1
	want_eq "handed to B" "$(jqv .to_user)" "$USER_B" || return 1
	want_eq "in the artifact's project" "$(jqv .project)" pc || return 1
	want_eq "the share names the artifact" "$(jqv .grant.artifact)" "$GEARBOX" || return 1
	want_eq "and is subject to B" "$(jqv .grant.subject)" "$USER_B" || return 1
	want_eq "the opening message is chat" "$(jqv .opening.type)" chat || return 1
	# One operation: the three rows carry the same clock reading.
	want_eq "share and task share a reading" "$(jqv .grant.hlc)" "$(jqv .hlc)" || return 1
	want_eq "and so does the opening message" "$(jqv .opening.seq_hlc)" "$(jqv .hlc)" || return 1
	remember TASK1 "$(jqv .id)"
	remember THREAD1 "$(jqv .thread)"
	printf 'task %s in thread %s\n' "$(jqv .id)" "$(jqv .thread)"
}

# The share landed: B reads an artifact in a project B is not in and holds no
# project-wide grant into.
b_reads_the_gearbox_now() {
	recall
	api GET "$TOKEN_B" "/api/artifact/$GEARBOX" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "title" "$(jqv .title)" "the gearbox whines under load" || return 1
	printf 'B reads %s across the project boundary\n' "$GEARBOX"
}

the_task_is_in_bs_inbox() {
	recall
	api GET "$TOKEN_B" /api/inbox/tasks || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	local mine
	mine="$(printf '%s' "$API_BODY" | jq --arg id "$TASK1" '[.tasks[] | select(.id == $id)]')"
	want_eq "the task is there once" "$(printf '%s' "$mine" | jq 'length')" 1 || return 1
	want_eq "with its artifact" "$(printf '%s' "$mine" | jq -r '.[0].artifact')" "$GEARBOX" || return 1
	want_eq "its thread" "$(printf '%s' "$mine" | jq -r '.[0].thread')" "$THREAD1" || return 1
	want_eq "its state" "$(printf '%s' "$mine" | jq -r '.[0].state')" delegated || return 1
	want_eq "and the title of the work" \
		"$(printf '%s' "$mine" | jq -r '.[0].artifact_title')" "the gearbox whines under load" || return 1
	printf 'inbox: %s\n' "$(printf '%s' "$mine" | jq -rc '.[0] | {state, artifact_title}')"
}

# The thread is the conversation, and both sides are in it - B is in another
# project and reads it anyway, because the task names it.
the_thread_opened_with_a_message() {
	recall
	api GET "$TOKEN_B" "/api/chat/handoffs?thread=$THREAD1" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "one message so far" "$(jqv '.events | length')" 1 || return 1
	want_eq "said by A" "$(jqv '.events[0].actor')" "$USER_A" || return 1
	want_eq "and it says what it is" "$(jqv '.events[0].body')" "please take the gearbox" || return 1
	want_eq "it names the task" "$(jqv '.events[0].meta.task')" "$TASK1" || return 1

	# B answers in the same thread, and A sees the answer: one thread, two
	# projects, no grant between them.
	api POST "$TOKEN_B" /api/chat/handoffs/say \
		"$(jq -nc --arg t "$THREAD1" '{body: "on it", thread: $t}')" || return 1
	want_eq "B could say something" "$API_STATUS" 200 || return 1
	api GET "$TOKEN_A" "/api/events?thread=$THREAD1" || return 1
	want_eq "A sees both halves" "$(jqv '[.events[].body] | join(",")')" "please take the gearbox,on it" || return 1
	printf 'thread %s carries both sides\n' "$THREAD1"
}

# auto_delegate defaults to true, so the task arrived already handed on.
the_new_task_arrived_delegated() {
	recall
	api GET "$TOKEN_B" "/api/task/$TASK1" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "state" "$(jqv .state)" delegated || return 1
	want_eq "assignee agent" "$(jqv .assignee_agent)" "$AGENT_B" || return 1
	printf 'task %s went straight to agent %s\n' "$TASK1" "$AGENT_B"
}

b_turns_auto_delegate_off() {
	recall
	api PUT "$TOKEN_B" /api/me/auto_delegate '{"on":false}' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "auto_delegate" "$(jqv .auto_delegate)" false || return 1
	want_eq "it is B's own row" "$(jqv .id)" "$USER_B" || return 1
	printf 'B now decides case by case\n'
}

# With the policy off the next assignment waits for the person.
the_next_assignment_waits_for_b() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the sprocket rattles")" || return 1
	remember SPROCKET "$id"
	assign_as "$TOKEN_A_PC" "$id" "$USER_B" "and this one too" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "state" "$(jqv .state)" open || return 1
	want_eq "no agent on it" "$(jqv .assignee_agent)" null || return 1
	remember TASK2 "$(jqv .id)"
	remember THREAD2 "$(jqv .thread)"
	printf 'task %s waits for B\n' "$(jqv .id)"
}

b_delegates_it_by_hand() {
	recall
	api POST "$TOKEN_B" "/api/task/$TASK2/delegate" '{}' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "state" "$(jqv .task.state)" delegated || return 1
	want_eq "agent" "$(jqv .task.assignee_agent)" "$AGENT_B" || return 1
	want_eq "the move is in the log" "$(jqv .event.body)" "open->delegated" || return 1
	want_eq "in the task's own thread" "$(jqv .event.thread)" "$THREAD2" || return 1
	want_eq "as a child of what was there" "$(jqv '.event.parents | length')" 1 || return 1
	printf 'B handed it to %s\n' "$AGENT_B"
}

# Delegation is the receiver's call. The sender is a party to the task and can
# still read it, so this is 403 rather than 404 - the refusal is about the verb.
only_the_assignee_delegates() {
	recall
	want_status 403 POST "$TOKEN_A" "/api/task/$TASK2/delegate" '{}' || return 1
	want_status 404 POST "$TOKEN_OP" "/api/task/$TASK2/delegate" '{}' || return 1
	printf 'the sender cannot delegate, and a stranger cannot see it to try\n'
}

# An agent token resolves to its user, so B's agent moves B's task.
bs_agent_finishes_the_task() {
	recall
	task_state "$TOKEN_B_AGENT" "$TASK2" "done" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "state" "$(jqv .task.state)" "done" || return 1
	want_eq "the move is in the log" "$(jqv .event.body)" "delegated->done" || return 1
	want_eq "written by the agent" "$(jqv .event.actor)" "$AGENT_B" || return 1
	api GET "$TOKEN_A" "/api/task/$TASK2" || return 1
	want_eq "and the sender sees it closed" "$(jqv .state)" "done" || return 1
	printf 'agent %s closed it\n' "$AGENT_B"
}

an_unknown_state_is_refused() {
	recall
	want_status 400 POST "$TOKEN_B" "/api/task/$TASK1/state" '{"state":"frozen"}' || return 1
	printf 'state must be one of open, delegated, done\n'
}

# A handoff is between two people. A third - here the node's own operator, who
# is the most privileged principal there is - cannot see it or touch it.
a_third_party_sees_no_task() {
	recall
	want_status 404 GET "$TOKEN_OP" "/api/task/$TASK1" || return 1
	want_status 404 POST "$TOKEN_OP" "/api/task/$TASK1/state" '{"state":"done"}' || return 1
	api GET "$TOKEN_OP" /api/inbox/tasks || return 1
	want_eq "neither task is in a stranger's inbox" \
		"$(printf '%s' "$API_BODY" | jq --arg a "$TASK1" --arg b "$TASK2" \
			'[.tasks[] | select(.id == $a or .id == $b)] | length')" 0 || return 1
	# And the state it could not move is the state it was.
	api GET "$TOKEN_B" "/api/task/$TASK1" || return 1
	want_eq "the task did not move" "$(jqv .state)" delegated || return 1
	printf 'the operator gets 404 on a handoff it is not party to\n'
}

assigning_a_personal_artifact_is_refused() {
	recall
	api POST "$TOKEN_A" /api/artifacts \
		'{"type": "note", "title": "a note to self", "visibility": "personal"}' || return 1
	want_eq "create status" "$API_STATUS" 200 || return 1
	want_status 400 POST "$TOKEN_A" /api/assign \
		"$(jq -nc --arg a "$(jqv .id)" --arg u "$USER_B" '{artifact: $a, to_user: $u}')" || return 1
	printf 'a personal artifact has no project to share it into\n'
}

assigning_something_unreadable_is_404() {
	recall
	# B has a share on the gearbox but not on the sprocket's project as a whole,
	# and neither on an artifact that does not exist.
	want_status 404 POST "$TOKEN_B" /api/assign \
		"$(jq -nc --arg u "$USER_A" '{artifact: "01NOSUCHARTIFACT", to_user: $u}')" || return 1
	printf 'you cannot hand on what you cannot read\n'
}

# ------------------------------------------------------------ the lifecycle

gearbox_walks_the_workflow() {
	recall
	local from=open to
	for to in triaged in-progress in-review "done"; do
		move_status "$TOKEN_A_PC" "$GEARBOX" "$to" || return 1
		want_eq "moving to $to" "$API_STATUS" 200 || return 1
		want_eq "the artifact says $to" "$(jqv .artifact.status)" "$to" || return 1
		want_eq "the event records the move" "$(jqv .event.body)" "$from->$to" || return 1
		want_eq "as a status event" "$(jqv .event.type)" status || return 1
		want_eq "naming the artifact" "$(jqv .event.artifact)" "$GEARBOX" || return 1
		want_eq "and the actor" "$(jqv .event.actor)" "$USER_A" || return 1
		from="$to"
	done
	printf 'open -> triaged -> in-progress -> in-review -> done\n'
}

history_reads_in_order() {
	recall
	api GET "$TOKEN_A_PC" "/api/artifact/$GEARBOX/history" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the trail" "$(jqv '[.events[].body] | join(",")')" \
		"open->triaged,triaged->in-progress,in-progress->in-review,in-review->done" || return 1
	want_eq "each move names the one before it" \
		"$(printf '%s' "$API_BODY" | jq '[range(1; (.events | length)) as $i
			| .events[$i].parents[0] == .events[$i - 1].id] | all')" true || return 1
	want_eq "the first opens the trail" "$(jqv '.events[0].parents | length')" 0 || return 1
	want_eq "current status" "$(jqv .status)" "done" || return 1
	want_eq "and done is terminal" "$(jqv '.next | length')" 0 || return 1
	printf '%s\n' "$(jqv '[.events[].body] | join(" ")')"
}

nothing_moves_out_of_a_terminal_status() {
	recall
	move_status "$TOKEN_A_PC" "$GEARBOX" triaged || return 1
	want_eq "a move out of done" "$API_STATUS" 409 || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$GEARBOX" || return 1
	want_eq "and the artifact did not move" "$(jqv .status)" "done" || return 1
	printf 'done is where it stops\n'
}

the_workflow_has_no_shortcuts() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the belt slips")" || return 1
	move_status "$TOKEN_A_PC" "$id" in-review || return 1
	want_eq "open straight to in-review" "$API_STATUS" 409 || return 1
	move_status "$TOKEN_A_PC" "$id" nowhere || return 1
	want_eq "and a status nobody has heard of" "$API_STATUS" 400 || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$id/history" || return 1
	want_eq "neither wrote an event" "$(jqv '.events | length')" 0 || return 1
	want_eq "an artifact with no status reads as open" "$(jqv .status)" open || return 1
	printf 'a status that can jump is a status nobody trusts\n'
}

a_wont_fix_is_a_terminal_exit() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" feature "dark mode for the console")" || return 1
	move_status "$TOKEN_A_PC" "$id" triaged || return 1
	want_eq "triaged" "$API_STATUS" 200 || return 1
	move_status "$TOKEN_A_PC" "$id" wont-fix || return 1
	want_eq "and out of the line" "$API_STATUS" 200 || return 1
	want_eq "the move" "$(jqv .event.body)" "triaged->wont-fix" || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$id/history" || return 1
	want_eq "the trail" "$(jqv '[.events[].body] | join(",")')" "open->triaged,triaged->wont-fix" || return 1
	want_eq "nowhere left to go" "$(jqv '.next | length')" 0 || return 1
	printf 'wont-fix is an exit, not a step\n'
}

# The assignee moves the status of work that is not in their project, because
# the share the assignment wrote is what makes them a participant.
the_assignee_moves_the_status() {
	recall
	move_status "$TOKEN_B" "$SPROCKET" triaged || return 1
	want_eq "B moves a bug in pa" "$API_STATUS" 200 || return 1
	want_eq "the actor is B" "$(jqv .event.actor)" "$USER_B" || return 1
	api GET "$TOKEN_B" "/api/artifact/$SPROCKET/history" || return 1
	want_eq "and B reads the trail back" "$(jqv '[.events[].body] | join(",")')" "open->triaged" || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$SPROCKET/history" || return 1
	want_eq "so does A, the same trail" "$(jqv '[.events[].body] | join(",")')" "open->triaged" || return 1
	printf 'one trail, both sides\n'
}

a_stranger_cannot_move_a_status() {
	recall
	# Another artifact in pc, shared with nobody: B has a share on the gearbox
	# and none on this, and a status you cannot read is a status you cannot move.
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the shaft is bent")" || return 1
	want_status 404 POST "$TOKEN_B" "/api/artifact/$id/status" '{"status":"triaged"}' || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$id/history" || return 1
	printf 'an artifact you cannot read has no status you can move\n'
}

only_the_types_with_a_lifecycle_have_one() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" transcript "a session")" || return 1
	want_status 400 POST "$TOKEN_A_PC" "/api/artifact/$id/status" '{"status":"triaged"}' || return 1
	printf 'a transcript has no status to move\n'
}

# ---------------------------------------------------- phase 4 console checks

# The inbox, mounted against the live node as B: the task the assignment wrote
# has to be on the screen, with its state, or the view is a shell.
console_renders_the_inbox() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_B" \
		"the gearbox whines under load" /inbox
}

serves_the_inbox_route() {
	recall
	local root_body
	www / || return 1
	root_body="$WWW_BODY"
	www /inbox || return 1
	want_eq "status" "$WWW_STATUS" 200 || return 1
	want_eq "the inbox is the app" "$WWW_BODY" "$root_body" || return 1
	www "/task/$TASK1" || return 1
	want_eq "and so is a task link" "$WWW_STATUS" 200 || return 1
	printf '/inbox -> 200, and a task deep link with it\n'
}

# ------------------------------------------------------------ phase 5 helpers
#
# Two nodes. Everything below takes the node it is talking to as an argument,
# because the whole point of the phase is that there are two of them and that
# what one of them holds is not automatically what the other one holds.

# start_pg5 LABEL PGDATA SOCKET PORT - a throwaway cluster for one federated
# node: its own PGDATA, its own port, its own copy of the schema.
start_pg5() {
	local label=$1 data=$2 sock=$3 port=$4
	mkdir -p "$sock"
	if ! "$PG_BIN/initdb" -D "$data" -U "$PGUSER" -A trust -E UTF8 --locale=C --no-sync \
		>"$WORK/initdb5$label.log" 2>&1; then
		cat "$WORK/initdb5$label.log" >&2
		return 1
	fi
	if ! "$PG_BIN/pg_ctl" -D "$data" -l "$WORK/postgres5$label.log" -w -t 60 \
		-o "-p $port -k $sock -h 127.0.0.1 -c fsync=off -c full_page_writes=off" \
		start >"$WORK/pg_ctl5$label.log" 2>&1; then
		cat "$WORK/pg_ctl5$label.log" >&2
		[ -f "$WORK/postgres5$label.log" ] && cat "$WORK/postgres5$label.log" >&2
		return 1
	fi
	"$PG_BIN/createdb" -h 127.0.0.1 -p "$port" "$DBNAME" || return 1
	psql -v ON_ERROR_STOP=1 -q -d "postgres://$PGUSER@127.0.0.1:$port/$DBNAME?sslmode=disable" \
		-f "$ROOT/schema.sql" || return 1
	printf 'cluster %s: port %s, schema loaded\n' "$label" "$port"
}

# copy_principals FROM_DSN TO_DSN - hands the second node the same users, agents
# and tokens as the first.
#
# This is a copy rather than a sync on purpose. Tokens are local credentials and
# not fabric state - the schema says so, and nothing replicates them - so two
# nodes that are meant to authenticate the same people have to be told who those
# people are out of band, exactly as two machines are handed the same key.
copy_principals() {
	local from=$1 to=$2 table
	for table in users agents tokens; do
		psql -v ON_ERROR_STOP=1 -q -d "$from" -c "\\copy $table to '$WORK/$table.csv' csv" || return 1
		psql -v ON_ERROR_STOP=1 -q -d "$to" -c "\\copy $table from '$WORK/$table.csv' csv" || return 1
	done
	printf 'users, agents and tokens: %s row(s) each side\n' \
		"$(psql -tA -d "$to" -c 'SELECT count(*) FROM tokens')"
}

# napi PORT METHOD TOKEN PATH [BODY] - api(), against one of the two federated
# nodes. Each check runs in a subshell of its own, so pointing the helpers at
# another node cannot leak into the next check.
napi() {
	local port=$1
	shift
	HTTP_PORT="$port"
	api "$@"
}

# want_napi WANT PORT METHOD TOKEN PATH [BODY] - want_status, against one node.
want_napi() {
	local want=$1 port=$2
	shift 2
	HTTP_PORT="$port"
	want_status "$want" "$@"
}

# remember5 / recall5 - the Phase 5 ids live in a file of their own, so the two
# federated nodes' principals cannot be confused with the single node's.
remember5() { printf '%s=%q\n' "$1" "$2" >>"$WORK/ids5"; }

recall5() {
	# shellcheck source=/dev/null
	. "$WORK/ids5"
}

# scalar5 DSN QUERY - one value straight out of one of the two databases.
scalar5() { psql -v ON_ERROR_STOP=1 -tA -d "$1" -c "$2"; }

# psql5_counts DSN QUERY - psql_counts, against one of the two databases.
psql5_counts() {
	local n
	n="$(psql -v ON_ERROR_STOP=1 -tA -d "$1" -c "$2")"
	if [ -z "$n" ] || [ "$n" -lt 1 ]; then
		printf 'query returned %s, want at least one row:\n%s\n' "${n:-<empty>}" "$2" >&2
		return 1
	fi
	printf '%s row(s)\n' "$n"
}

# sync5 DSN NODE PEER_PORT TOKEN - one run of the real driver, with its report
# left in SYNC_REPORT.
sync5() {
	local dsn=$1 node=$2 port=$3 token=$4
	SYNC_REPORT="$(DATABASE_URL="$dsn" FLOWY_NODE="$node" \
		"$ROOT/flowy" sync --peer "http://127.0.0.1:$port" --token "$token")" || return 1
}

# moved5 - how many rows the last report moved, in either direction.
moved5() { printf '%s' "$SYNC_REPORT" | jq '[.pulled[], .pushed[]] | add'; }

# sync_round - a full exchange: A syncs with B, then B syncs with A. The count
# of rows that moved lands in SYNC_MOVED. Callers recall5 first.
#
# Both runs authenticate as the same principal, which is what federation is: two
# nodes holding the work of the people they have in common, and nothing else.
sync_round() {
	local total=0 n
	sync5 "$N5_DSN_A" nodeA "$N5_PORT_B" "$N5_TOKEN_B" || return 1
	n="$(moved5)" || return 1
	total=$((total + n))
	sync5 "$N5_DSN_B" nodeB "$N5_PORT_A" "$N5_TOKEN_B" || return 1
	n="$(moved5)" || return 1
	SYNC_MOVED=$((total + n))
}

# ------------------------------------------------------------- phase 5 checks

both_nodes_are_up_and_apart() {
	recall5
	napi "$N5_PORT_A" GET "" /healthz || return 1
	want_eq "node A's name" "$(jqv .node)" nodeA || return 1
	want_eq "node A's database" "$(jqv .db)" up || return 1
	napi "$N5_PORT_B" GET "" /healthz || return 1
	want_eq "node B's name" "$(jqv .node)" nodeB || return 1
	want_eq "node B's database" "$(jqv .db)" up || return 1
	want_eq "rows B holds that A wrote" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts")" 0 || return 1
	want_eq "rows A holds that B wrote" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM artifacts")" 0 || return 1
	printf 'nodeA on %s and nodeB on %s, two databases, both empty of artifacts\n' \
		"$N5_PORT_A" "$N5_PORT_B"
}

the_same_token_authenticates_on_both() {
	recall5
	napi "$N5_PORT_A" GET "$N5_TOKEN_B" /api/whoami || return 1
	local user
	user="$(jqv .user)"
	want_eq "the principal on node A" "$user" "$N5_USER_B" || return 1
	napi "$N5_PORT_B" GET "$N5_TOKEN_B" /api/whoami || return 1
	want_eq "the principal on node B" "$(jqv .user)" "$user" || return 1
	want_eq "and its home project" "$(jqv .project)" pb || return 1
	printf 'the replication token is %s in pb on both nodes\n' "$user"
}

a_opens_pa_up_to_pb_on_node_a() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/grants \
		'{"from_project":"pb","to_project":"pa"}' || return 1
	printf 'grant %s: pb may read pa, which is what lets pa replicate\n' "$(jqv .id)"
}

a_writes_a_shared_artifact() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"note","title":"the shared one","body":"zibbleflax is the word"}' || return 1
	want_eq "the node that wrote it" "$(jqv .node)" nodeA || return 1
	want_eq "the project it landed in" "$(jqv .project)" pa || return 1
	remember5 SHARED_ID "$(jqv .id)"
	remember5 SHARED_HLC "$(jqv .hlc)"
	printf 'artifact %s at hlc %s, on nodeA\n' "$(jqv .id)" "$(jqv .hlc)"
}

a_writes_what_the_peer_may_not_see() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"note","title":"the personal one","visibility":"personal","body":"quibblenock"}' ||
		return 1
	want_eq "no project at all" "$(jqv .project)" null || return 1
	remember5 PERSONAL_ID "$(jqv .id)"
	# And one in pc, a project the peer principal holds no grant into.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_PC" /api/artifacts \
		'{"type":"note","title":"the ungranted one","body":"thrumbleaxe"}' || return 1
	remember5 UNGRANTED_ID "$(jqv .id)"
	printf 'a personal artifact and one in pc, neither of them the peer principals business\n'
}

a_appends_a_thread() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/events \
		'{"type":"chat","room":"general","body":"first, on node A"}' || return 1
	local first thread
	first="$(jqv .id)"
	thread="$(jqv .thread)"
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/events \
		"{\"type\":\"chat\",\"room\":\"general\",\"thread\":\"$thread\",\"parents\":[\"$first\"],\"body\":\"second, on node A\"}" ||
		return 1
	remember5 THREAD5 "$thread"
	remember5 THREAD5_FIRST "$first"
	remember5 THREAD5_SECOND "$(jqv .id)"
	printf 'thread %s: %s then %s, which names it as its parent\n' "$thread" "$first" "$(jqv .id)"
}

b_writes_one_of_its_own() {
	recall5
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"note","title":"the one from B","body":"wobblethorn"}' || return 1
	want_eq "the node that wrote it" "$(jqv .node)" nodeB || return 1
	want_eq "the project it landed in" "$(jqv .project)" pb || return 1
	remember5 B_ID "$(jqv .id)"
	remember5 B_HLC "$(jqv .hlc)"
	printf 'artifact %s at hlc %s, on nodeB\n' "$(jqv .id)" "$(jqv .hlc)"
}

the_first_sync() {
	recall5
	sync5 "$N5_DSN_A" nodeA "$N5_PORT_B" "$N5_TOKEN_B" || return 1
	want_eq "the node that ran it" \
		"$(printf '%s' "$SYNC_REPORT" | jq -r .node)" nodeA || return 1
	want_eq "the node it reached" \
		"$(printf '%s' "$SYNC_REPORT" | jq -r .peer_node)" nodeB || return 1
	local moved
	moved="$(moved5)"
	if [ "$moved" -lt 1 ]; then
		printf 'the first sync moved nothing: %s\n' "$SYNC_REPORT" >&2
		return 1
	fi
	# And the other way, which is the same command with the ends swapped.
	sync5 "$N5_DSN_B" nodeB "$N5_PORT_A" "$N5_TOKEN_B" || return 1
	printf 'A -> B moved %s rows; B -> A: %s\n' "$moved" "$SYNC_REPORT"
}

the_shared_artifact_is_on_b() {
	recall5
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/artifact/$SHARED_ID" || return 1
	want_eq "the id" "$(jqv .id)" "$SHARED_ID" || return 1
	want_eq "the clock reading" "$(jqv .hlc)" "$SHARED_HLC" || return 1
	want_eq "the node that wrote it" "$(jqv .node)" nodeA || return 1
	want_eq "the title" "$(jqv .title)" "the shared one" || return 1
	# The search vector is rebuilt on the way in rather than shipped, so a
	# replicated artifact has to be findable on the node that received it.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/search?q=zibbleflax" || return 1
	want_eq "B finds it by a word from its body" "$(hits ".id == \"$SHARED_ID\"")" 1 || return 1
	printf '%s is on nodeB at the same hlc %s, and searchable there\n' "$SHARED_ID" "$SHARED_HLC"
}

bs_artifact_is_on_a() {
	recall5
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" "/api/artifact/$B_ID" || return 1
	want_eq "the id" "$(jqv .id)" "$B_ID" || return 1
	want_eq "the clock reading" "$(jqv .hlc)" "$B_HLC" || return 1
	want_eq "the node that wrote it" "$(jqv .node)" nodeB || return 1
	printf '%s is on nodeA at the same hlc %s, still stamped nodeB\n' "$B_ID" "$B_HLC"
}

the_thread_is_on_b_with_its_parents() {
	recall5
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/events?thread=$THREAD5" || return 1
	want_eq "events in the thread on B" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 2 || return 1
	want_eq "the first" "$(jqv '.events[0].id')" "$THREAD5_FIRST" || return 1
	want_eq "the second" "$(jqv '.events[1].id')" "$THREAD5_SECOND" || return 1
	want_eq "the edge between them" "$(jqv '.events[1].parents[0]')" "$THREAD5_FIRST" || return 1
	want_eq "the node that appended it" "$(jqv '.events[1].node')" nodeA || return 1
	printf 'thread %s replicated with its DAG intact\n' "$THREAD5"
}

the_personal_artifact_did_not_replicate() {
	recall5
	want_eq "copies of it on B" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$PERSONAL_ID'")" 0 || return 1
	# Not even for its owner, because it never crossed at all.
	want_napi 404 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/artifact/$PERSONAL_ID" || return 1
	printf 'the personal artifact is not on nodeB in any form\n'
}

the_ungranted_project_did_not_replicate() {
	recall5
	want_eq "copies of it on B" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$UNGRANTED_ID'")" 0 || return 1
	want_napi 404 "$N5_PORT_B" GET "$N5_TOKEN_A_PC" "/api/artifact/$UNGRANTED_ID" || return 1
	printf 'pc has no grant into it, so pc does not replicate\n'
}

the_delta_offers_only_what_the_peer_may_read() {
	recall5
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" "/api/sync/pull?since=0" || return 1
	local ids
	ids="$(printf '%s' "$API_BODY" | jq -r '.artifacts[].id')"
	if printf '%s\n' "$ids" | grep -qx "$PERSONAL_ID"; then
		printf 'the pull endpoint offered a personal artifact to a peer\n' >&2
		return 1
	fi
	if printf '%s\n' "$ids" | grep -qx "$UNGRANTED_ID"; then
		printf 'the pull endpoint offered an artifact in a project with no grant\n' >&2
		return 1
	fi
	if ! printf '%s\n' "$ids" | grep -qx "$SHARED_ID"; then
		printf 'the pull endpoint withheld an artifact the peer holds a grant on\n%s\n' \
			"$API_BODY" >&2
		return 1
	fi
	printf 'the delta holds the granted artifact and neither of the two it may not read\n'
}

the_same_artifact_is_edited_on_both_nodes() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		"{\"id\":\"$SHARED_ID\",\"type\":\"note\",\"title\":\"edited on A\"}" || return 1
	want_eq "the edit on A is A's" "$(jqv .node)" nodeA || return 1
	local h1 h2
	h1="$(jqv .hlc)"
	# A moment apart, so the two readings cannot tie. Two writes in the same
	# millisecond on two nodes are two logical counters that know nothing of
	# each other, and last-writer-wins has no last writer to pick.
	sleep 0.2
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/artifacts \
		"{\"id\":\"$SHARED_ID\",\"type\":\"note\",\"title\":\"edited on B\"}" || return 1
	want_eq "the edit on B is B's" "$(jqv .node)" nodeB || return 1
	h2="$(jqv .hlc)"
	if [ "$h2" -le "$h1" ]; then
		printf 'the second edit read %s, which does not order after %s\n' "$h2" "$h1" >&2
		return 1
	fi
	remember5 CONFLICT_H1 "$h1"
	remember5 CONFLICT_H2 "$h2"
	printf 'edited on A at %s and on B at %s, with no sync in between\n' "$h1" "$h2"
}

both_nodes_converge_on_the_later_edit() {
	recall5
	sync_round || return 1
	local title_a reading_a author_a title_b reading_b author_b
	napi "$N5_PORT_A" GET "$N5_TOKEN_A" "/api/artifact/$SHARED_ID" || return 1
	title_a="$(jqv .title)"
	reading_a="$(jqv .hlc)"
	author_a="$(jqv .node)"
	napi "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/artifact/$SHARED_ID" || return 1
	title_b="$(jqv .title)"
	reading_b="$(jqv .hlc)"
	author_b="$(jqv .node)"
	want_eq "node A's copy" "$title_a" "edited on B" || return 1
	want_eq "node B's copy" "$title_b" "edited on B" || return 1
	want_eq "node A's reading" "$reading_a" "$CONFLICT_H2" || return 1
	want_eq "node B's reading" "$reading_b" "$CONFLICT_H2" || return 1
	want_eq "the author on A" "$author_a" nodeB || return 1
	want_eq "the author on B" "$author_b" nodeB || return 1
	want_eq "rows for it on A" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM artifacts WHERE id = '$SHARED_ID'")" 1 || return 1
	want_eq "rows for it on B" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$SHARED_ID'")" 1 || return 1
	printf 'both nodes hold one row, at %s, carrying the later edit\n' "$CONFLICT_H2"
}

a_delete_on_a_reaches_b() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" "/api/artifact/$SHARED_ID/delete" || return 1
	want_eq "tombstoned" "$(jqv .tombstone)" true || return 1
	local h
	h="$(jqv .hlc)"
	if [ "$h" -le "$CONFLICT_H2" ]; then
		printf 'the delete read %s, which does not order after the edit at %s\n' \
			"$h" "$CONFLICT_H2" >&2
		return 1
	fi
	remember5 TOMB_HLC "$h"
	sync_round || return 1
	printf 'deleted on nodeA at %s, and synced\n' "$h"
}

the_tombstone_is_on_b_too() {
	recall5
	# A tombstone is a row and reads back as one, on both nodes alike: what the
	# delete removes is the artifact from the views, not the fact from the table.
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_A" "/api/artifact/$SHARED_ID" || return 1
	want_eq "the delete on A" "$(jqv .tombstone)" true || return 1
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/artifact/$SHARED_ID" || return 1
	want_eq "the delete on B" "$(jqv .tombstone)" true || return 1
	want_eq "at the same reading" "$(jqv .hlc)" "$TOMB_HLC" || return 1
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" /api/artifacts || return 1
	want_eq "in B's list" "$(hits ".id == \"$SHARED_ID\"")" 0 || return 1
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/search?q=zibbleflax" || return 1
	want_eq "in B's search" "$(hits ".id == \"$SHARED_ID\"")" 0 || return 1
	want_eq "still a row in B's table" \
		"$(scalar5 "$N5_DSN_B" "SELECT tombstone FROM artifacts WHERE id = '$SHARED_ID'")" t || return 1
	want_eq "at the delete's reading" \
		"$(scalar5 "$N5_DSN_B" "SELECT hlc FROM artifacts WHERE id = '$SHARED_ID'")" \
		"$TOMB_HLC" || return 1
	printf 'gone from nodeB list and search, still there as a tombstone at %s\n' "$TOMB_HLC"
}

an_assignment_replicates_as_a_task() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"bug","title":"the federated bug","body":"gribbleflint in the gearbox","status":"open"}' ||
		return 1
	local art task thread
	art="$(jqv .id)"
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/assign \
		"{\"artifact\":\"$art\",\"to_user\":\"$N5_USER_B\",\"note\":\"over to you, on the other node\"}" ||
		return 1
	task="$(jqv .id)"
	thread="$(jqv .thread)"
	sync_round || return 1

	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/task/$task" || return 1
	want_eq "the task on B" "$(jqv .id)" "$task" || return 1
	want_eq "who it is for" "$(jqv .to_user)" "$N5_USER_B" || return 1
	want_eq "and where it came from" "$(jqv .from_user)" "$N5_USER_A" || return 1
	# The share it wrote and the thread it opened crossed with it.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/artifact/$art" || return 1
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/events?thread=$thread" || return 1
	local n
	n="$(printf '%s' "$API_BODY" | jq '.events | length')"
	if [ "$n" -lt 1 ]; then
		printf 'the assignment thread reached B empty\n' >&2
		return 1
	fi
	printf 'task %s, its share and its %s-event thread all reached nodeB\n' "$task" "$n"
}

a_sync_with_nothing_new_moves_nothing() {
	recall5
	# Settle first. A write takes one exchange to reach the other node and one
	# more for each side's cursor to catch up with what came back.
	sync_round || return 1
	sync_round || return 1
	sync_round || return 1
	want_eq "rows moved by a sync with nothing new" "$SYNC_MOVED" 0 || return 1
	printf 'caught up, and it costs nothing to say so: %s\n' "$SYNC_REPORT"
}

pushing_the_same_delta_twice_applies_it_once() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"note","title":"pushed by hand","body":"flimflammery"}' || return 1
	local id hlc delta
	id="$(jqv .id)"
	hlc="$(jqv .hlc)"

	# Take the delta out of A as the peer principal, then hand it to B twice.
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" "/api/sync/pull?since=$((hlc - 1))" || return 1
	want_eq "the delta holds the one new row" \
		"$(printf '%s' "$API_BODY" | jq '.artifacts | length')" 1 || return 1
	delta="$(printf '%s' "$API_BODY" | jq -c '{artifacts, events, tasks, grants}')"

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the first push applies it" "$(jqv '.applied.artifacts')" 1 || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the second receives it" "$(jqv '.received.artifacts')" 1 || return 1
	want_eq "and applies nothing" "$(jqv '.applied.artifacts')" 0 || return 1
	want_eq "B holds one row for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$id'")" 1 || return 1
	want_eq "at the reading A wrote" \
		"$(scalar5 "$N5_DSN_B" "SELECT hlc FROM artifacts WHERE id = '$id'")" "$hlc" || return 1
	printf 'the same delta pushed twice, applied once: %s\n' "$id"
}

both_nodes_know_the_other_as_a_peer() {
	recall5
	local where a b
	where="pull_cursor > 0 AND pushed_cursor > 0 AND last_seen IS NOT NULL"
	a="$(scalar5 "$N5_DSN_A" \
		"SELECT count(*) FROM peers WHERE peer = 'http://127.0.0.1:$N5_PORT_B' AND $where")"
	b="$(scalar5 "$N5_DSN_B" \
		"SELECT count(*) FROM peers WHERE peer = 'http://127.0.0.1:$N5_PORT_A' AND $where")"
	want_eq "nodeA's bookmark for nodeB" "$a" 1 || return 1
	want_eq "nodeB's bookmark for nodeA" "$b" 1 || return 1
	printf 'each node holds one peers row for the other, both cursors moved\n'
}

the_bookmarks_are_the_operators_view() {
	recall5
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_OP" /api/peers || return 1
	want_eq "the peer it names" "$(jqv '.peers[0].peer')" "http://127.0.0.1:$N5_PORT_B" || return 1
	want_napi 403 "$N5_PORT_A" GET "$N5_TOKEN_A" /api/peers || return 1
	printf 'peers answers the operator and refuses everyone else\n'
}

sync_refuses_a_peer_it_cannot_name() {
	local out
	if out="$("$ROOT/flowy" sync 2>&1)"; then
		printf 'flowy sync with no peer succeeded: %s\n' "$out" >&2
		return 1
	fi
	case "$out" in
	*"no peer"*) ;;
	*)
		printf 'want a complaint about the peer, got: %s\n' "$out" >&2
		return 1
		;;
	esac
	# Replication runs as a principal on both sides, so a token this node cannot
	# resolve is refused before anything is sent anywhere.
	if out="$("$ROOT/flowy" sync --peer http://127.0.0.1:1 --token no-such-token 2>&1)"; then
		printf 'flowy sync with an unknown token succeeded: %s\n' "$out" >&2
		return 1
	fi
	case "$out" in
	*"does not resolve"*) ;;
	*)
		printf 'want a complaint about the token, got: %s\n' "$out" >&2
		return 1
		;;
	esac
	printf 'no peer and no principal are both refused, and nothing is sent\n'
}

the_two_databases_agree_row_for_row() {
	recall5
	local rows="SELECT id || '|' || hlc || '|' || node || '|' || tombstone FROM artifacts ORDER BY 1"
	scalar5 "$N5_DSN_A" "$rows" | sort >"$WORK/rows5a"
	scalar5 "$N5_DSN_B" "$rows" | sort >"$WORK/rows5b"

	local common differing
	common="$(join -t'|' -j 1 "$WORK/rows5a" "$WORK/rows5b" | wc -l)"
	differing="$(join -t'|' -j 1 "$WORK/rows5a" "$WORK/rows5b" |
		awk -F'|' '$2 != $5 || $3 != $6 || $4 != $7' | wc -l)"
	if [ "$common" -lt 3 ]; then
		printf 'only %s artifact(s) are on both nodes; the two never met\n' "$common" >&2
		return 1
	fi
	want_eq "rows that disagree" "$differing" 0 || return 1
	printf '%s artifacts on both nodes, every one of them at the same reading, author and state\n' \
		"$common"
}

# ------------------------------------------------------------ phase 6 helpers
#
# The forge bridge, driven over HTTP against the mock forge the node selected at
# startup. The mock is in that process, so the gate plays the other side of the
# conversation - the reviewer who closes an issue and the reviewer who comments
# on it - through the mock's own control routes, which exist only when the mock
# is what FLOWY_FORGE picked.

# forge_file TOKEN ARTIFACT REPO - file an artifact as an issue.
forge_file() {
	api POST "$1" /api/forge/file "$(jq -nc --arg a "$2" --arg r "$3" '{artifact: $a, repo: $r}')"
}

# forge_status TOKEN ARTIFACT - refresh the issue's state.
forge_status() { api GET "$1" "/api/forge/status?artifact=$2"; }

# forge_sync TOKEN ARTIFACT - one turn of the reviewer loop, both ways.
forge_sync() {
	api POST "$1" /api/forge/sync "$(jq -nc --arg a "$2" '{artifact: $a}')"
}

# mock_state TOKEN REPO NUMBER STATE - the reviewer closes the issue.
mock_state() {
	api POST "$1" /api/forge/mock/state \
		"$(jq -nc --arg r "$2" --argjson n "$3" --arg s "$4" '{repo: $r, number: $n, state: $s}')"
}

# mock_comment TOKEN REPO NUMBER AUTHOR BODY - the reviewer says something.
mock_comment() {
	api POST "$1" /api/forge/mock/comment \
		"$(jq -nc --arg r "$2" --argjson n "$3" --arg a "$4" --arg b "$5" \
			'{repo: $r, number: $n, author: $a, body: $b}')"
}

# mock_issue TOKEN REPO NUMBER - what the forge holds, comments and all.
mock_issue() { api GET "$1" "/api/forge/mock/issue?repo=$2&number=$3"; }

# say_in_thread TOKEN BODY THREAD - a reply in the issue's thread, posted
# through the ordinary chat endpoint. Nothing about it is forge-aware: that is
# the point of threading an issue into the chat.
say_in_thread() {
	api POST "$1" /api/chat/forge/say "$(jq -nc --arg b "$2" --arg t "$3" '{body: $b, thread: $t}')"
}

# ------------------------------------------------------------- phase 6 checks

the_node_selected_the_mock() {
	recall
	api GET "$TOKEN_A" /api/forge || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the forge" "$(jqv .forge)" mock || return 1
	want_eq "and why" "$(jqv .why)" "FLOWY_FORGE=mock" || return 1
	# The choice is a choice: gh is installed and was not picked.
	want_eq "gh is on PATH" "$(jqv .available.gh)" true || return 1
	want_eq "the mock's control surface is up" "$(jqv .mock)" true || return 1
	api GET "$TOKEN_A" /api/node || return 1
	want_eq "the node reports the phase" "$(jqv .phase)" 6 || return 1
	want_eq "and its forge" "$(jqv .forge)" mock || return 1
	printf 'FLOWY_FORGE=mock selected MockForge, with gh installed beside it\n'
}

an_unknown_forge_is_refused_at_startup() {
	local out
	if out="$(DATABASE_URL="$DATABASE_URL" ./flowy serve -forge bitbucket -addr 127.0.0.1:1 2>&1)"; then
		printf 'flowy serve -forge bitbucket started anyway:\n%s\n' "$out" >&2
		return 1
	fi
	printf '%s\n' "$out" | tail -1
}

# The bug lives in pc, which nobody holds a grant into - the pb -> pa grant
# Phase 1 issued would otherwise be what lets B read it, and then the permission
# check below would be testing the grant rather than the bridge.
a_files_the_carburettor_bug() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the carburettor floods on a cold start")" || return 1
	remember FORGEBUG "$id"
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the forge" "$(jqv .external.forge)" mock || return 1
	want_eq "the repo" "$(jqv .external.repo)" o/r || return 1
	want_eq "the issue is numbered" "$(jqv '.external.number > 0')" true || return 1
	want_eq "and has a url" \
		"$(jqv '.external.url | test("^https://.*/o/r/issues/[0-9]+$")')" true || return 1
	want_eq "it is open" "$(jqv .external.state)" open || return 1
	want_eq "the artifact is reported" "$(jqv .artifact.reported)" true || return 1
	want_eq "the filing is an event" "$(jqv .event.type)" forge || return 1
	want_eq "that says what it did" "$(jqv .event.body)" "filed o/r#$(jqv .external.number)" || return 1
	remember ISSUE "$(jqv .external.number)"
	remember FORGETHREAD "$(jqv .external.thread)"
	printf 'filed %s as o/r#%s in thread %s\n' "$id" "$(jqv .external.number)" "$(jqv .external.thread)"
}

# The ref is on the artifact, not in the response: an ordinary read of the
# artifact carries it, which is how anything else finds out where a bug went.
the_link_is_on_the_artifact() {
	recall
	api GET "$TOKEN_A_PC" "/api/artifact/$FORGEBUG" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "reported" "$(jqv .reported)" true || return 1
	want_eq "the repo" "$(jqv .external.repo)" o/r || return 1
	want_eq "the number" "$(jqv .external.number)" "$ISSUE" || return 1
	want_eq "the state" "$(jqv .external.state)" open || return 1
	printf '%s -> o/r#%s\n' "$FORGEBUG" "$ISSUE"
}

filing_the_same_artifact_twice_is_refused() {
	recall
	forge_file "$TOKEN_A_PC" "$FORGEBUG" o/r || return 1
	want_eq "a second filing" "$API_STATUS" 409 || return 1
	want_eq "and it hands back the issue there already is" "$(jqv .external.number)" "$ISSUE" || return 1
	printf 'already filed as o/r#%s\n' "$ISSUE"
}

a_repo_that_is_not_a_repo_is_refused() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the wiper motor stalls")" || return 1
	forge_file "$TOKEN_A_PC" "$id" "not-a-repo" || return 1
	want_eq "a repo with no owner" "$API_STATUS" 400 || return 1
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "and an artifact nobody filed has nothing to sync" "$API_STATUS" 400 || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$id" || return 1
	want_eq "neither wrote a link" "$(jqv '.external == null')" true || return 1
	want_eq "nor marked it reported" "$(jqv .reported)" false || return 1
	printf 'nothing was filed\n'
}

# The permission story is the ordinary one: reading the artifact is what makes
# somebody a participant, and B cannot read this one.
a_stranger_cannot_file_or_sync_it() {
	recall
	forge_file "$TOKEN_B" "$FORGEBUG" o/r || return 1
	want_eq "file" "$API_STATUS" 404 || return 1
	forge_status "$TOKEN_B" "$FORGEBUG" || return 1
	want_eq "status" "$API_STATUS" 404 || return 1
	forge_sync "$TOKEN_B" "$FORGEBUG" || return 1
	want_eq "sync" "$API_STATUS" 404 || return 1
	printf 'B gets 404 on all three, the same as on the artifact itself\n'
}

the_issue_is_closed_on_the_forge() {
	recall
	mock_state "$TOKEN_A_PC" o/r "$ISSUE" closed || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the issue is closed" "$(jqv .state)" closed || return 1
	printf 'o/r#%s closed by the reviewer\n' "$ISSUE"
}

a_closed_issue_moves_the_artifact_to_done() {
	recall
	forge_status "$TOKEN_A_PC" "$FORGEBUG" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the state read back" "$(jqv .state)" closed || return 1
	want_eq "the artifact moved" "$(jqv .moved)" true || return 1
	want_eq "to done" "$(jqv .status)" "done" || return 1
	want_eq "and the link remembers the state" "$(jqv .external.state)" closed || return 1

	# The move is in the same trail every other move is in, and it says where it
	# came from - it is the one transition the workflow itself would refuse.
	api GET "$TOKEN_A_PC" "/api/artifact/$FORGEBUG/history" || return 1
	want_eq "one move" "$(jqv '.events | length')" 1 || return 1
	want_eq "open->done" "$(jqv '.events[0].body')" "open->done" || return 1
	want_eq "via the forge" "$(jqv '.events[0].meta.via')" forge || return 1
	want_eq "naming the issue" "$(jqv '.events[0].meta.number')" "$ISSUE" || return 1
	printf 'closed on the forge -> done here, recorded as open->done via forge\n'
}

refreshing_a_closed_issue_moves_nothing() {
	recall
	api GET "$TOKEN_A_PC" "/api/artifact/$FORGEBUG" || return 1
	local before
	before="$(jqv .hlc)"
	forge_status "$TOKEN_A_PC" "$FORGEBUG" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "nothing moved" "$(jqv .moved)" false || return 1
	want_eq "and it is still done" "$(jqv .status)" "done" || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$FORGEBUG" || return 1
	want_eq "the artifact was not even touched" "$(jqv .hlc)" "$before" || return 1
	printf 'a poll that finds nothing new writes nothing\n'
}

the_reviewer_comments_on_the_issue() {
	recall
	mock_comment "$TOKEN_A_PC" o/r "$ISSUE" reviewer "does it flimberwock at 3000rpm?" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "by the reviewer" "$(jqv .author)" reviewer || return 1
	remember REVIEWCOMMENT "$(jqv .id)"
	printf 'comment %s by reviewer\n' "$(jqv .id)"
}

the_comment_becomes_an_event_in_the_thread() {
	recall
	forge_sync "$TOKEN_A_PC" "$FORGEBUG" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "one comment came in" "$(jqv .pulled)" 1 || return 1
	want_eq "and nothing went out" "$(jqv .pushed)" 0 || return 1
	want_eq "it is chat, like everything else in the thread" "$(jqv '.events[0].type')" chat || return 1
	want_eq "said by a synthetic external principal" "$(jqv '.events[0].actor')" "forge:reviewer" || return 1
	want_eq "carrying what was said" "$(jqv '.events[0].body')" "does it flimberwock at 3000rpm?" || return 1
	want_eq "in the artifact's thread" "$(jqv '.events[0].thread')" "$FORGETHREAD" || return 1
	want_eq "naming the comment it came from" \
		"$(jqv '.events[0].meta.forge_comment')" "$REVIEWCOMMENT" || return 1
	want_eq "and the artifact it is about" "$(jqv '.events[0].artifact')" "$FORGEBUG" || return 1

	# It is an ordinary chat event, so the ordinary chat endpoint reads it.
	api GET "$TOKEN_A_PC" "/api/chat/forge?thread=$FORGETHREAD" || return 1
	want_eq "the room has the reviewer in it" \
		"$(jqv '[.events[] | select(.actor == "forge:reviewer")] | length')" 1 || return 1
	printf 'forge:reviewer said it, in thread %s\n' "$FORGETHREAD"
}

a_reply_in_the_thread_reaches_the_forge() {
	recall
	say_in_thread "$TOKEN_A_PC" "yes, and it flimberwocks louder above 4000rpm" "$FORGETHREAD" || return 1
	want_eq "the reply was said" "$API_STATUS" 200 || return 1

	forge_sync "$TOKEN_A_PC" "$FORGEBUG" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "nothing new came in" "$(jqv .pulled)" 0 || return 1
	want_eq "and the reply went out" "$(jqv .pushed)" 1 || return 1

	mock_issue "$TOKEN_A_PC" o/r "$ISSUE" || return 1
	want_eq "the forge received it, posted as the node" \
		"$(jqv '[.comments[] | select(.author == "flowy"
		         and (.body | test("flimberwocks louder above 4000rpm")))] | length')" 1 || return 1
	want_eq "attributed to whoever wrote it here" \
		"$(jqv '[.comments[] | select(.author == "flowy" and (.body | test("alice")))] | length')" 1 || return 1
	printf 'the reply is on o/r#%s\n' "$ISSUE"
}

# The comment the node pushed must not come back in as a comment to thread, or
# the loop would echo forever.
a_pushed_reply_does_not_come_back() {
	recall
	forge_sync "$TOKEN_A_PC" "$FORGEBUG" || return 1
	want_eq "our own comment is not new" "$(jqv .pulled)" 0 || return 1
	api GET "$TOKEN_A_PC" "/api/chat/forge?thread=$FORGETHREAD" || return 1
	want_eq "nobody said it twice" \
		"$(jqv '[.events[] | select(.body | test("flimberwocks louder"))] | length')" 1 || return 1
	printf 'the loop does not echo\n'
}

syncing_with_nothing_new_is_a_no_op() {
	recall
	api GET "$TOKEN_A_PC" "/api/chat/forge?thread=$FORGETHREAD" || return 1
	local events comments hlc
	events="$(jqv '.events | length')"
	mock_issue "$TOKEN_A_PC" o/r "$ISSUE" || return 1
	comments="$(jqv '.comments | length')"
	api GET "$TOKEN_A_PC" "/api/artifact/$FORGEBUG" || return 1
	hlc="$(jqv .hlc)"

	local i
	for i in 1 2; do
		forge_sync "$TOKEN_A_PC" "$FORGEBUG" || return 1
		want_eq "sync $i pulled" "$(jqv .pulled)" 0 || return 1
		want_eq "sync $i pushed" "$(jqv .pushed)" 0 || return 1
	done

	api GET "$TOKEN_A_PC" "/api/chat/forge?thread=$FORGETHREAD" || return 1
	want_eq "no new events" "$(jqv '.events | length')" "$events" || return 1
	mock_issue "$TOKEN_A_PC" o/r "$ISSUE" || return 1
	want_eq "no new comments" "$(jqv '.comments | length')" "$comments" || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$FORGEBUG" || return 1
	want_eq "and the artifact was not written at all" "$(jqv .hlc)" "$hlc" || return 1
	printf 'twice over: %s events, %s comments, the same clock reading\n' "$events" "$comments"
}

gh_was_never_invoked() {
	if [ -s "$GH_CANARY" ]; then
		printf 'gh was run, and the mock should have been the only forge in this run:\n' >&2
		cat "$GH_CANARY" >&2
		return 1
	fi
	printf 'gh is on PATH and was never invoked\n'
}

# ------------------------------------------------- the security fixes, checked
#
# One check per defect the review found, and each of them fails on the code as
# it was. They run last, against the nodes the earlier phases left standing, so
# nothing here can change what those phases were asserting.
#
# The two low ones are Go tests rather than HTTP checks: what they are about -
# a driver that will not count the rows it changed, a list that forgets the
# entry it needed - has no request that reaches it.

# empty_delta is a push that carries nothing, which is enough to find out
# whether this node would have taken a delta from this token at all.
empty_delta='{"artifacts":[],"events":[],"tasks":[],"grants":[],"hwm":0}'

# CRITICAL 1a. Pushing is not reading: it writes rows of the caller's choosing
# into this database. Any token used to be enough.
sync_push_is_only_for_a_peer() {
	recall5
	want_napi 403 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/sync/push "$empty_delta" || return 1
	# Not even the agent identity of the peer's own user, which is a token
	# somebody else's process holds.
	want_napi 403 "$N5_PORT_B" POST "$N5_TOKEN_A_AGENT" /api/sync/push "$empty_delta" || return 1
	# And the peer this node was told about is let through, so what is being
	# tested is the gate rather than the endpoint being broken.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$empty_delta" || return 1
	printf 'push answers the peer and refuses every other token\n'
}

# CRITICAL 1b. A grant is a capability, and a pushed one used to be applied as
# it stood: a peer could write itself a read of any project by naming it.
a_forged_grant_is_refused() {
	recall5
	napi "$N5_PORT_B" GET "" /healthz || return 1
	local hlc id delta
	hlc="$(jqv .hlc)"
	id="forged-$(date +%s)-$$"
	# pb may read pa. It has no business in pc, and this is it saying otherwise.
	delta="$(printf '{"artifacts":[],"events":[],"tasks":[],"grants":[{"id":"%s","from_project":"pb","to_project":"pc","cap":"read","granted_by":"%s","hlc":%d,"node":"forger","tombstone":false}],"hwm":0}' \
		"$id" "$N5_USER_B" "$((hlc + 65536))")"

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "grants refused" "$(jqv '.refused.grants')" 1 || return 1
	want_eq "grants applied" "$(jqv '.applied.grants')" 0 || return 1
	want_eq "rows in B's grants table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM grants WHERE id = '$id'")" 0 || return 1
	printf 'the forged grant was received, refused and not written: %s\n' "$(jqv '.reasons[0]')"
}

# CRITICAL 1c. A reading of MaxInt64 packs to a negative int64 and lifts the
# node's clock past anything it can ever write again. The delta is refused
# whole, and the node goes on writing readings that order.
a_poisoned_clock_reading_is_refused() {
	recall5
	napi "$N5_PORT_B" GET "" /healthz || return 1
	local before delta
	before="$(jqv .hlc)"
	delta="$(printf '{"artifacts":[{"id":"poison-%s","type":"note","project":"pb","owner_user":"%s","title":"poison","body":"","visibility":"project","hlc":9223372036854775807,"node":"forger","tombstone":false}],"events":[],"tasks":[],"grants":[],"hwm":0}' \
		"$$" "$N5_USER_B")"
	want_napi 400 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "rows it wrote" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE node = 'forger'")" 0 || return 1

	# The clock still works: two writes, both positive, strictly increasing and
	# above where the node was before the push.
	local first second
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"note","title":"after the poison","body":"the clock still counts"}' || return 1
	first="$(jqv .hlc)"
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"note","title":"after the poison, again","body":"and it still counts up"}' || return 1
	second="$(jqv .hlc)"
	if [ "$first" -le "$before" ] || [ "$second" -le "$first" ]; then
		printf 'readings went %s -> %s -> %s, which is not strictly increasing\n' \
			"$before" "$first" "$second" >&2
		return 1
	fi

	# And paging by the cursor still finds them, which is what a poisoned clock
	# takes away: everything after it sorts below what is already stored.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/sync/pull?since=$((first - 1))" || return 1
	want_eq "the newest two rows are above the cursor" \
		"$(printf '%s' "$API_BODY" | jq '[.artifacts[] | select(.hlc >= '"$first"')] | length')" 2 ||
		return 1
	printf 'the poisoned delta was refused and the clock still runs: %s -> %s -> %s\n' \
		"$before" "$first" "$second"
}

# CRITICAL 2. An id is a guess anybody can make, and the upsert used to replace
# every column of whatever it landed on - owner included.
an_unreadable_id_cannot_be_taken_over() {
	recall
	# NOTE is A's personal artifact. B cannot read it, and a write must not tell
	# B it is there either.
	api POST "$TOKEN_B" /api/artifacts \
		"$(jq -nc --arg id "$NOTE" '{id: $id, type: "note", title: "mine now", body: "taken over"}')" ||
		return 1
	want_eq "B writing over A's personal artifact" "$API_STATUS" 404 || return 1

	api GET "$TOKEN_A" "/api/artifact/$NOTE" || return 1
	want_eq "A can still read it" "$API_STATUS" 200 || return 1
	want_eq "the owner" "$(jqv .owner_user)" "$USER_A" || return 1
	want_eq "the project" "$(jqv .project)" null || return 1
	want_eq "the visibility" "$(jqv .visibility)" personal || return 1
	want_eq "the title" "$(jqv .title)" "not for the project" || return 1
	want_eq "the body" "$(jqv .body)" "a quixotron of my own" || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$NOTE" || return 1
	printf 'the write was refused as a read of it would be, and the row is untouched\n'
}

# HIGH 3. The actor used to be whatever the body said, which is a log anybody
# can sign with anybody's name.
an_event_carries_the_callers_name() {
	recall
	api POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$USER_B" '{type: "note", room: "pa/bugs", actor: $a, body: "not mine"}')" ||
		return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the actor" "$(jqv .actor)" "$USER_A" || return 1

	# An agent posts as itself, not as anybody it was told to be.
	api POST "$TOKEN_A_AGENT" /api/events \
		"$(jq -nc --arg a "$USER_B" '{type: "note", room: "pa/bugs", actor: $a, body: "nor mine"}')" ||
		return 1
	want_eq "the agent's event" "$(jqv .actor)" "$AGENT_A" || return 1

	# And the types this node's own handlers mint are not for hand-writing: a
	# forged status move would be a lifecycle transition nobody made.
	local minted
	for minted in status task forge; do
		api POST "$TOKEN_A" /api/events \
			"$(jq -nc --arg t "$minted" '{type: $t, room: "pa/bugs", body: "open->done"}')" || return 1
		want_eq "a hand-written $minted event" "$API_STATUS" 403 || return 1
	done
	printf 'events are signed by the token that wrote them, and the minted types are refused\n'
}

# HIGH 4. Filing publishes an artifact's body over the node's own forge
# credential. Reading it was enough to do that, to any repository named in the
# request.
only_the_owner_files_to_an_allowed_repo() {
	recall
	# B can read the gearbox - it was handed to B - and does not own it.
	forge_file "$TOKEN_B" "$GEARBOX" o/r || return 1
	want_eq "a reader who is not the owner" "$API_STATUS" 403 || return 1
	api GET "$TOKEN_B" "/api/artifact/$GEARBOX" || return 1
	want_eq "and nothing was filed" "$(jqv '.external == null')" true || return 1

	# The owner, into a repository the operator never named.
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the radiator sighs")" || return 1
	forge_file "$TOKEN_A_PC" "$id" "somebody/else" || return 1
	want_eq "a repo that is not on the node's list" "$API_STATUS" 403 || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$id" || return 1
	want_eq "still unfiled" "$(jqv '.external == null')" true || return 1

	# The owner, into the one repository it may file into.
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "the owner, into an allowed repo" "$API_STATUS" 200 || return 1
	want_eq "the repo it went to" "$(jqv .external.repo)" o/r || return 1

	# Pushing replies out is the same publication, so it is the same rule: B is
	# handed the artifact, can read the thread, and still cannot push it.
	assign_as "$TOKEN_A_PC" "$id" "$USER_B" "have a look at the radiator" || return 1
	want_eq "the handoff" "$API_STATUS" 200 || return 1
	api GET "$TOKEN_B" "/api/artifact/$id" || return 1
	want_eq "B reads it now" "$API_STATUS" 200 || return 1
	forge_sync "$TOKEN_B" "$id" || return 1
	want_eq "B syncing it to the forge" "$API_STATUS" 403 || return 1
	printf 'only the owner files, and only into the operator list\n'
}

# MEDIUM 5. The push cursor used to be raised to the highest event it had looked
# at whether or not the forge took the comment, and the ref was thrown away on
# the error - so a refusal halfway through lost the replies that had not gone
# out and sent the ones that had a second time.
a_refused_reply_is_not_posted_twice() {
	recall
	local id num thread i
	id="$(new_artifact "$TOKEN_A_PC" bug "the alternator sings at idle")" || return 1
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filed" "$API_STATUS" 200 || return 1
	num="$(jqv .external.number)"
	thread="$(jqv .external.thread)"

	for i in 1 2 3; do
		say_in_thread "$TOKEN_A_PC" "reply number $i about the alternator" "$thread" || return 1
		want_eq "reply $i" "$API_STATUS" 200 || return 1
	done

	# The forge accepts one comment and refuses the next.
	api POST "$TOKEN_A_PC" /api/forge/mock/fail '{"after": 1}' || return 1
	want_eq "the mock is armed to refuse" "$API_STATUS" 200 || return 1
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the sync reports the refusal" "$API_STATUS" 502 || return 1
	mock_issue "$TOKEN_A_PC" o/r "$num" || return 1
	want_eq "comments on the issue" \
		"$(jqv '[.comments[] | select(.author == "flowy")] | length')" 1 || return 1

	# The forge is up again: the one that was refused and the one behind it go
	# out, and the one that arrived does not arrive again.
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the second sync" "$API_STATUS" 200 || return 1
	want_eq "replies it sent" "$(jqv .pushed)" 2 || return 1
	mock_issue "$TOKEN_A_PC" o/r "$num" || return 1
	for i in 1 2 3; do
		want_eq "copies of reply $i" \
			"$(jqv "[.comments[] | select(.body | test(\"reply number $i about\"))] | length")" 1 ||
			return 1
	done
	printf 'three replies, one refusal, three comments on o/r#%s\n' "$num"
}

# MEDIUM 6. A share is newer than the peer's cursor; the artifact it shares is
# older. Paging by "newer than the cursor" alone stepped over it for good, and
# the peer held a grant on something it never received.
a_late_grant_still_replicates() {
	recall5
	local id
	# pc is a project the peer principal holds no grant into, so this crosses
	# only if the share below carries it.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_PC" /api/artifacts \
		'{"type":"note","title":"the old one in pc","body":"snorkelwhump"}' || return 1
	id="$(jqv .id)"

	# Settle, so the artifact is well below both cursors before it is shared.
	sync_round || return 1
	sync_round || return 1
	sync_round || return 1
	want_eq "copies of it on B before the share" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$id'")" 0 || return 1

	# Now share it, which is a new row about an old one.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_PC" /api/grants \
		"$(jq -nc --arg a "$id" --arg s "$N5_USER_B" '{artifact: $a, subject: $s}')" || return 1
	sync_round || return 1

	want_eq "copies of it on B after the share" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$id'")" 1 || return 1
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/artifact/$id" || return 1
	want_eq "and it reads back on B" "$(jqv .title)" "the old one in pc" || return 1
	printf 'the share carried the artifact it shares: %s\n' "$id"
}

# MEDIUM 7. mem_write's update path did not check what it was updating, so an
# owned bug could be rewritten into a note and leave the lifecycle it was in.
mem_write_stays_in_its_namespace() {
	recall
	want_tool_fails mem_write "$TOKEN_A_PC" \
		"$(jq -nc --arg id "$GEARBOX" '{id: $id, title: "not a memory item", scope: "project"}')" \
		"no such memory item" || return 1
	api GET "$TOKEN_A_PC" "/api/artifact/$GEARBOX" || return 1
	want_eq "the bug is still a bug" "$(jqv .type)" bug || return 1
	want_eq "with its title" "$(jqv .title)" "the gearbox whines under load" || return 1
	printf 'an artifact that is not memory is not memory to write either\n'
}

# LOW 8. A reply used to inherit its thread from a parent read without the
# permission filter, which puts the speaker in a conversation they cannot see.
a_reply_does_not_adopt_an_unreadable_thread() {
	recall
	# CHAT_PC is a message in pc, and the conversation it is in is pc's. B holds
	# a grant into pa and none into pc, so B cannot read either.
	api GET "$TOKEN_A_PC" /api/chat/general || return 1
	local hidden
	hidden="$(jqv "[.events[] | select(.id == \"$CHAT_PC\")][0].thread")"
	if [ -z "$hidden" ] || [ "$hidden" = null ]; then
		printf 'could not find the thread of %s in pc\n' "$CHAT_PC" >&2
		return 1
	fi
	api GET "$TOKEN_B" "/api/events?thread=$hidden" || return 1
	want_eq "what B can read of that thread" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 0 || return 1

	api POST "$TOKEN_B" /api/chat/general/say \
		"$(jq -nc --arg p "$CHAT_PC" '{body: "answering something I cannot see", parents: [$p]}')" ||
		return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	local reply thread
	reply="$(jqv .id)"
	thread="$(jqv .thread)"
	if [ "$thread" = "$hidden" ]; then
		printf 'the reply joined thread %s, which the speaker cannot read\n' "$hidden" >&2
		return 1
	fi

	# And pc's conversation is as it was: the reply is not in it.
	api GET "$TOKEN_A_PC" "/api/chat/general?thread=$hidden" || return 1
	want_eq "the reply in pc's thread" "$(chat_len ".id == \"$reply\"")" 0 || return 1
	printf 'the unreadable parent was ignored and the reply opened its own thread %s\n' "$thread"
}

# ------------------------------------------ the second round of security fixes
#
# The first round gated POST /api/sync/push on being a peer, and checked the
# rows a peer handed over table by table. The checks it added are above; these
# are the ones the re-review found that those did not cover: events went through
# with no check at all, and the three tables that were checked were checked
# against the wrong question - what the pusher may read, rather than what the
# pusher owns.
#
# They run last, against the nodes the earlier phases left standing, so nothing
# here can change what those phases were asserting. The federation one goes at
# the end of them, because it opens a project up for good.

# psql_do runs a statement against the first node's database. It is how a check
# is something the node cannot do to itself: a link a peer replicated in, a log
# that refuses a row halfway through a sync.
psql_do() { psql -v ON_ERROR_STOP=1 -q -c "$1"; }

# forged_delta_hlc - a clock reading a peer would plausibly have written: one
# millisecond ahead of what the node last handed out, which is believable and
# beats anything already stored.
forged_hlc() {
	napi "$1" GET "" /healthz || return 1
	printf '%s' "$(($(jqv .hlc) + 65536))"
}

# HIGH 1. Artifacts, tasks and grants were checked when they arrived; events
# were not checked at all. A peer could write the log under anybody's name, and
# every node it reached then held the forgery - including the minted types the
# API refuses by hand, which is a lifecycle move nobody made.
a_pushed_event_is_signed_by_the_pusher() {
	recall5
	local hlc forged mine delta
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	forged="forged-ev-$$-$(date +%s)"
	mine="own-ev-$$-$(date +%s)"
	delta="$(jq -nc --arg f "$forged" --arg m "$mine" --arg a "$N5_USER_A" --arg b "$N5_USER_B" \
		--argjson h1 "$hlc" --argjson h2 "$((hlc + 65536))" '
		{artifacts: [], tasks: [], grants: [], hwm: 0, events: [
		  {id: $f, type: "status", project: "pb", room: "pb/bugs", thread: $f, parents: [],
		   actor: $a, artifact: "", seq_hlc: $h1, node: "forger", body: "open->done"},
		  {id: $m, type: "chat", project: "pb", room: "pb/bugs", thread: $m, parents: [],
		   actor: $b, artifact: "", seq_hlc: $h2, node: "forger", body: "this one really is mine"}]}')"

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "events received" "$(jqv '.received.events')" 2 || return 1
	want_eq "events refused" "$(jqv '.refused.events')" 1 || return 1
	want_eq "events applied" "$(jqv '.applied.events')" 1 || return 1
	want_eq "rows in B's log for the forged one" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE id = '$forged'")" 0 || return 1
	want_eq "rows for the one the pusher signed" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE id = '$mine'")" 1 || return 1
	printf 'the forged status event was refused and the pusher own message applied: %s\n' \
		"$(jqv '.reasons[0]')"
}

# HIGH 2. A pushed artifact that was already here was let through if the pusher
# could read it - and applying it replaces every column, owner_user included. A
# read-share was therefore a way to take the artifact over and share it on.
a_read_share_is_not_a_write() {
	recall5
	local id hlc delta
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"note","title":"A owns this one","body":"quibblesworth"}' || return 1
	id="$(jqv .id)"
	# One read-share, which is all the pusher ever gets here.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/grants \
		"$(jq -nc --arg a "$id" --arg s "$N5_USER_B" '{artifact: $a, subject: $s}')" || return 1
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/artifact/$id" || return 1
	want_eq "who owns it" "$(jqv .owner_user)" "$N5_USER_A" || return 1

	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	delta="$(jq -nc --arg i "$id" --arg b "$N5_USER_B" --argjson h "$hlc" '
		{events: [], tasks: [], grants: [], hwm: 0, artifacts: [
		  {id: $i, type: "note", project: "pb", owner_user: $b, title: "mine now",
		   body: "taken over by a reader", visibility: "project", hlc: $h, node: "forger",
		   tombstone: false, reported: false}]}')"
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "artifacts refused" "$(jqv '.refused.artifacts')" 1 || return 1
	want_eq "artifacts applied" "$(jqv '.applied.artifacts')" 0 || return 1

	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "the owner" "$(jqv .owner_user)" "$N5_USER_A" || return 1
	want_eq "the project" "$(jqv .project)" pa || return 1
	want_eq "the title" "$(jqv .title)" "A owns this one" || return 1
	printf 'a reader could not rewrite %s; the owner, the project and the title are as they were\n' \
		"$id"
}

# HIGH 3. A pushed share was checked against what it claimed - that the
# artifact's owner is whoever it says granted it - and never against who was
# handing it over. Anybody could write themselves a share of anything by
# putting the owner's name in the granted_by field.
a_share_is_only_the_owners_to_hand_over() {
	recall5
	local id gid hlc delta
	# A's artifact, in A's project, owned by A and shared with nobody.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"note","title":"A owns this one too","body":"thrimblewick"}' || return 1
	id="$(jqv .id)"
	want_eq "who owns it" "$(jqv .owner_user)" "$N5_USER_A" || return 1
	gid="forged-grant-$$-$(date +%s)"
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	delta="$(jq -nc --arg g "$gid" --arg i "$id" --arg s "$N5_USER_B" --arg o "$N5_USER_A" \
		--argjson h "$hlc" '
		{artifacts: [], events: [], tasks: [], hwm: 0, grants: [
		  {id: $g, from_project: "pa", to_project: "pa", subject: $s, artifact: $i,
		   cap: "read", granted_by: $o, hlc: $h, node: "forger", tombstone: false}]}')"

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "grants refused" "$(jqv '.refused.grants')" 1 || return 1
	want_eq "grants applied" "$(jqv '.applied.grants')" 0 || return 1
	want_eq "rows in B's grants table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM grants WHERE id = '$gid'")" 0 || return 1
	printf 'a share nobody who owns it wrote was refused: %s\n' "$(jqv '.reasons[0]')"
}

# HIGH 4. A task row is a read capability: the tasks clause in EventFilterSQL
# lets the parties to a task read the thread it names. A new task was accepted
# from anybody, so a peer could name itself on both sides of a handoff, name a
# conversation it cannot see, and read it from then on.
a_pushed_task_cannot_name_a_thread_it_cannot_read() {
	recall5
	local thread art tid hlc delta
	# A conversation in pc, which the peer holds no grant into.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A_PC" /api/events \
		'{"type":"note","room":"pc/quiet","body":"the thing nobody outside pc may read"}' || return 1
	thread="$(jqv .thread)"
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/events?thread=$thread" || return 1
	want_eq "what the peer can read of it to begin with" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 0 || return 1

	# An artifact the pusher really may hand over, so that the only thing left
	# to refuse the task is the thread it names.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"note","title":"the peer own note","body":"snickersnee"}' || return 1
	art="$(jqv .id)"
	tid="forged-task-$$-$(date +%s)"
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	delta="$(jq -nc --arg t "$tid" --arg a "$art" --arg u "$N5_USER_B" --arg th "$thread" \
		--argjson h "$hlc" '
		{artifacts: [], events: [], grants: [], hwm: 0, tasks: [
		  {id: $t, artifact: $a, from_user: $u, to_user: $u, project: "pb", state: "open",
		   assignee_agent: "", thread: $th, hlc: $h, node: "forger"}]}')"

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "tasks refused" "$(jqv '.refused.tasks')" 1 || return 1
	want_eq "tasks applied" "$(jqv '.applied.tasks')" 0 || return 1
	want_eq "rows in B's tasks table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM tasks WHERE id = '$tid'")" 0 || return 1

	# And the conversation is still pc's.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/events?thread=$thread" || return 1
	want_eq "what the peer can read of it now" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 0 || return 1
	printf 'the task naming thread %s was refused, and the thread reads back empty still\n' "$thread"
}

# HIGH/MED 5. Filing and syncing check the operator's repository list; the
# status refresh did not - and the repository it uses comes off the artifact's
# link, which is a replicated column. A peer that pushes an artifact carrying a
# link therefore chose which repository this node's credential talked to.
forge_status_obeys_the_repo_list() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the tailgate rattles over cobbles")" || return 1
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filed" "$API_STATUS" 200 || return 1
	forge_status "$TOKEN_A_PC" "$id" || return 1
	want_eq "a status refresh against the allowed repo" "$API_STATUS" 200 || return 1

	# What a replicated link looks like: the same row, pointing somewhere the
	# operator never named.
	psql_do "UPDATE artifacts SET external = jsonb_set(external, '{repo}', '\"somebody/else\"')
	          WHERE id = '$id'" || return 1
	forge_status "$TOKEN_A_PC" "$id" || return 1
	want_eq "a status refresh against a repo that is not on the list" "$API_STATUS" 403 || return 1
	case "$API_BODY" in
	*FLOWY_FORGE_REPOS*) ;;
	*)
		printf 'the refusal does not say whose list it is: %s\n' "$API_BODY" >&2
		return 1
		;;
	esac
	# Nothing was asked of the forge, so nothing about the artifact moved.
	api GET "$TOKEN_A_PC" "/api/artifact/$id" || return 1
	want_eq "the state on the link" "$(jqv .external.state)" open || return 1
	want_eq "and the artifact was not moved" "$(jqv '.status == "done"')" false || return 1
	printf 'status went through for o/r and was refused for somebody/else\n'
}

# MED 7. The push half of the reviewer loop was fixed to write its cursor before
# reporting a refusal; the pull half was not. A pull that died halfway had
# already threaded the comments before the failure into the log, and threw away
# the record of having done so - so the next sync threaded them in again.
a_half_threaded_pull_is_not_threaded_twice() {
	recall
	local id num thread i
	id="$(new_artifact "$TOKEN_A_PC" bug "the sunroof drips on the passenger")" || return 1
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filed" "$API_STATUS" 200 || return 1
	num="$(jqv .external.number)"
	thread="$(jqv .external.thread)"
	for i in 1 2 3 4 5; do
		mock_comment "$TOKEN_A_PC" o/r "$num" reviewer "inbound $i about the sunroof" || return 1
		want_eq "the reviewer comment $i" "$API_STATUS" 200 || return 1
	done

	# The log refuses the third one, which is a pull dying with two comments
	# already threaded in.
	psql_do "ALTER TABLE events ADD CONSTRAINT gate_no_third_inbound
	          CHECK (body NOT LIKE '%inbound 3 about the sunroof%')" || return 1
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the sync reports the refusal" "$API_STATUS" 502 || return 1
	psql_do "ALTER TABLE events DROP CONSTRAINT gate_no_third_inbound" || return 1

	api GET "$TOKEN_A_PC" "/api/chat/forge?thread=$thread" || return 1
	want_eq "comments that got in before it died" \
		"$(jqv '[.events[] | select(.body | test("inbound [12] about the sunroof"))] | length')" 2 ||
		return 1

	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the sync after it" "$API_STATUS" 200 || return 1
	want_eq "comments it threaded" "$(jqv .pulled)" 3 || return 1
	api GET "$TOKEN_A_PC" "/api/chat/forge?thread=$thread" || return 1
	for i in 1 2 3 4 5; do
		want_eq "copies of comment $i in the thread" \
			"$(jqv "[.events[] | select(.body | test(\"inbound $i about the sunroof\"))] | length")" 1 ||
			return 1
	done
	printf 'five comments, one refusal halfway, five events in thread %s\n' "$thread"
}

# MED 8. The login the node's own comments arrive under was the mock's name for
# itself, written onto every link whatever forge was behind it. On a real gh the
# node posts as whoever the machine is logged in as, so its own replies came
# back as a stranger comments, were threaded in, and were pushed out again.
the_node_asks_the_forge_who_it_is() {
	recall
	local id num
	api POST "$TOKEN_A_PC" /api/forge/mock/login '{"login":"flowy-bot"}' || return 1
	want_eq "the forge is logged in as" "$(jqv .login)" flowy-bot || return 1

	id="$(new_artifact "$TOKEN_A_PC" bug "the horn sticks in the cold")" || return 1
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filed" "$API_STATUS" 200 || return 1
	want_eq "the login the link records" "$(jqv .external.author)" flowy-bot || return 1
	num="$(jqv .external.number)"

	mock_comment "$TOKEN_A_PC" o/r "$num" flowy-bot "this one came from this node" || return 1
	mock_comment "$TOKEN_A_PC" o/r "$num" reviewer "and this one did not" || return 1
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the sync" "$API_STATUS" 200 || return 1
	want_eq "comments it threaded in" "$(jqv .pulled)" 1 || return 1
	want_eq "and who said the one it took" "$(jqv '.events[0].actor')" "forge:reviewer" || return 1

	# Back to the name the rest of the run uses.
	api POST "$TOKEN_A_PC" /api/forge/mock/login '{"login":"flowy"}' || return 1
	want_eq "the forge is itself again" "$(jqv .login)" flowy || return 1
	printf 'the node posts as flowy-bot here, and knows its own comments by it\n'
}

# MED/LOW 10. The comment cursor was read after the filing round trip returned,
# so anything said between the request going out and the answer coming back was
# already behind the cursor when it was written - and ListComments never offers
# it again.
a_comment_made_while_filing_is_not_lost() {
	recall
	local id num
	api POST "$TOKEN_A_PC" /api/forge/mock/on-file \
		'{"author":"reviewer","body":"answered while the issue was being opened"}' || return 1
	want_eq "the forge is armed" "$(jqv .armed)" true || return 1

	id="$(new_artifact "$TOKEN_A_PC" bug "the indicator ticks out of time")" || return 1
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filed" "$API_STATUS" 200 || return 1
	num="$(jqv .external.number)"
	mock_issue "$TOKEN_A_PC" o/r "$num" || return 1
	want_eq "the comment is on the issue" \
		"$(jqv '[.comments[] | select(.body | test("while the issue was being opened"))] | length')" 1 ||
		return 1

	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the sync" "$API_STATUS" 200 || return 1
	want_eq "comments it threaded in" "$(jqv .pulled)" 1 || return 1
	want_eq "said by" "$(jqv '.events[0].actor')" "forge:reviewer" || return 1
	printf 'the comment made inside the filing window reached the thread\n'
}

# MED 6. A grant that opens a whole project makes every artifact in it readable
# at once, and all of them are below the cursor. The rescan that carries them is
# a page like any other, and what did not fit in it used to be dropped: the peer
# held the grant and a fraction of the project, with nothing to page towards.
#
# It runs last of these, because it opens pc up to pb for good.
a_project_grant_carries_more_than_a_page() {
	recall5
	local first second second_hlc marker cursor
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_PC" /api/artifacts \
		'{"type":"note","title":"the first old one in pc","body":"wimblesnatch"}' || return 1
	first="$(jqv .id)"
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_PC" /api/artifacts \
		'{"type":"note","title":"the second old one in pc","body":"wimblesnatch"}' || return 1
	second="$(jqv .id)"
	second_hlc="$(jqv .hlc)"
	# Something the peer may read, written after them, so that settling leaves
	# the cursor above both - which is what makes them old.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"note","title":"the marker in pa","body":"wimblesnatch"}' || return 1
	marker="$(jqv .id)"
	sync_round || return 1
	sync_round || return 1
	sync_round || return 1

	cursor="$(scalar5 "$N5_DSN_B" \
		"SELECT pull_cursor FROM peers WHERE peer = 'http://127.0.0.1:$N5_PORT_A'")"
	if [ "$cursor" -le "$second_hlc" ]; then
		printf 'the cursor %s is not past the pc rows (%s); they are not old yet\n' \
			"$cursor" "$second_hlc" >&2
		return 1
	fi
	want_eq "copies of the first on B before the grant" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$first'")" 0 || return 1
	want_eq "and of the marker, which the peer could always read" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$marker'")" 1 || return 1

	# One grant opens the whole project.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_PC" /api/grants \
		'{"from_project":"pb","to_project":"pc"}' || return 1

	# Page through it as the driver does, a row at a time, which is what makes
	# the rescan overflow: a page holds one and the grant opened a project.
	local page hwm seen="" pages=0
	for page in $(seq 1 20); do
		want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" "/api/sync/pull?since=$cursor&limit=1" ||
			return 1
		seen="$seen$(printf '%s' "$API_BODY" | jq -r '.artifacts[].id')
"
		pages=$page
		hwm="$(jqv .hwm)"
		if printf '%s' "$seen" | grep -qx "$first" && printf '%s' "$seen" | grep -qx "$second"; then
			break
		fi
		if [ "$hwm" -le "$cursor" ]; then
			break
		fi
		cursor="$hwm"
	done

	if ! printf '%s' "$seen" | grep -qx "$first"; then
		printf 'the first old pc artifact never came over in %s pages\n' "$pages" >&2
		return 1
	fi
	if ! printf '%s' "$seen" | grep -qx "$second"; then
		printf 'the second old pc artifact never came over in %s pages\n' "$pages" >&2
		return 1
	fi
	printf 'both old pc artifacts crossed, in %s pages of one row\n' "$pages"
}

# ---------------------------------------------------------------- environment

say "environment"
for tool in go curl jq node npm; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		printf '%s is not on PATH\n' "$tool" >&2
		exit 1
	fi
done
node_major="$(node -v | sed 's/^v\([0-9]*\).*/\1/')"
if [ "$node_major" -lt 20 ]; then
	printf 'node %s is too old; the console needs node >= 20\n' "$(node -v)" >&2
	exit 1
fi
PG_BIN="$(find_pg_bin)"
printf 'go:       %s\n' "$(go version)"
printf 'node:     %s (npm %s)\n' "$(node -v)" "$(npm -v)"
printf 'postgres: %s\n' "$("$PG_BIN/postgres" --version)"
printf 'work:     %s\n' "$WORK"

# The gate must not reach the network: the module's one dependency is vendored.
if [ -d "$ROOT/vendor" ]; then
	export GOFLAGS="${GOFLAGS:-} -mod=vendor"
fi

# Phase 6 runs against the mock forge, and it has to be the mock because
# FLOWY_FORGE says so rather than because there was nothing else to pick. So the
# gate puts a `gh` on PATH: a script that records having been run and then
# refuses. Every forge check below goes through the mock, and this file staying
# empty at the end is what says GhClient was never invoked.
mkdir -p "$WORK/bin"
cat >"$WORK/bin/gh" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$GH_CANARY"
printf 'the gate has no GitHub and no credential for one\n' >&2
exit 1
EOF
chmod +x "$WORK/bin/gh"
PATH="$WORK/bin:$PATH"
export PATH
printf 'forge:    FLOWY_FORGE=mock, with a refusing gh at %s\n' "$WORK/bin/gh"

# --------------------------------------------------------------------- console
#
# The console is built first, because `flowy serve` embeds web/dist with
# go:embed: the Go build below compiles whatever this leaves behind, so a stale
# or missing build would be baked into the binary the rest of the run tests.

say "console"
check "npm ci" npm_ci
check "biome check web/" biome_check
check "vite build" npm_build
check "the build is an index that loads a hashed bundle" console_build_is_hashed
check "the console mounts in a dom and renders the room view" console_mounts

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
# FLOWY_FORGE_REPOS is the operator's list of repositories this node may file
# into. It is o/r and nothing else, which is what makes "file it into a repo
# nobody said you could" a refusal rather than a filing.
DATABASE_URL="$DATABASE_URL" FLOWY_NODE=gate FLOWY_OPERATOR="${USER_OP:-}" FLOWY_FORGE=mock \
	FLOWY_FORGE_REPOS=o/r \
	./flowy serve -addr "127.0.0.1:$HTTP_PORT" >"$SERVE_LOG" 2>&1 &
SERVE_PID=$!
printf 'flowy serve pid %s on 127.0.0.1:%s\n' "$SERVE_PID" "$HTTP_PORT"

check "flowy serve answers /healthz" "$WORK/smoke" healthz "http://127.0.0.1:$HTTP_PORT/healthz"
check "healthz reports the spine tables" \
	"$WORK/smoke" healthz "http://127.0.0.1:$HTTP_PORT/healthz?counts=1"
check "spine tables exist" "$WORK/smoke" schema

say "subcommand stubs"
# fuse is the last one: mcp left this list in Phase 2 and sync in Phase 5.
check "flowy fuse is a stub" stub_says fuse

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

# ------------------------------------------------------------------- phase 3
#
# Chat, and the console that reads it. The messages are events of type 'chat' in
# the same log Phase 1 walked, so what these checks are really asserting is that
# a room is a view of the log and inherits its cursor, its DAG and its
# permission filter rather than getting its own.

say "chat"
check "A says a message and then a reply that names it" a_says_two_messages
check "the room reads back in order, with the edge intact" room_reads_back_in_order
check "A's agent says one in the same room" an_agent_says_one
check "a human message and an agent message are told apart" human_and_agent_are_distinguishable

say "the watcher"
check "wait?cursor= returns only what is newer" wait_returns_only_what_is_newer
check "a caught-up poll blocks until somebody says something" wait_blocks_until_something_is_said
check "and returns empty when the window runs out" wait_returns_on_timeout

say "the inbox"
check "the inbox excludes the caller's own messages" inbox_excludes_the_callers_own
check "an agent's inbox excludes its user's messages too" agent_inbox_excludes_both_identities

say "rooms are scoped by project"
check "a project with no grant sees none of the room" another_project_sees_none_of_the_room
check "a project that holds a grant does" a_granted_project_does_see_the_room

# ------------------------------------------------------------------- phase 4
#
# Assignment, delegation and the issue lifecycle. An assignment is a share plus
# a task plus a thread written as one operation, so the checks below are mostly
# about what the other side can suddenly do: read the artifact, answer in the
# thread, hand the work to an agent, close it.

say "assignment"
check "A creates a bug in pc, which nobody has a grant into" a_creates_the_gearbox_bug
check "B cannot read it" b_cannot_read_the_gearbox
check "A assigns it to B: a share, a task and a thread in one operation" a_assigns_the_gearbox_to_b
check "the share landed - B reads the artifact now" b_reads_the_gearbox_now
check "the task is in B's inbox, with the work it is about" the_task_is_in_bs_inbox
check "the thread opened with a message, and both sides are in it" the_thread_opened_with_a_message
check "a personal artifact cannot be assigned" assigning_a_personal_artifact_is_refused
check "neither can an artifact the caller cannot read" assigning_something_unreadable_is_404

say "delegation"
check "auto_delegate is on, so the task arrived already delegated" the_new_task_arrived_delegated
check "B turns auto_delegate off" b_turns_auto_delegate_off
check "the next assignment waits for B instead" the_next_assignment_waits_for_b
check "B hands it to their agent" b_delegates_it_by_hand
check "the sender cannot delegate somebody else's work" only_the_assignee_delegates
check "B's agent closes the task" bs_agent_finishes_the_task
check "a state that is not one of the three is refused" an_unknown_state_is_refused

say "a handoff is between two people"
check "a third party gets 404 on the task and cannot move it" a_third_party_sees_no_task

say "the issue lifecycle"
check "open -> triaged -> in-progress -> in-review -> done, each one an event" gearbox_walks_the_workflow
check "history returns the trail in order, chained by parents" history_reads_in_order
check "nothing moves out of a terminal status" nothing_moves_out_of_a_terminal_status
check "the workflow has no shortcuts and no invented statuses" the_workflow_has_no_shortcuts
check "wont-fix is a terminal exit from anywhere in the line" a_wont_fix_is_a_terminal_exit
check "the assignee moves the status of work in another project" the_assignee_moves_the_status
check "an artifact you cannot read has no status you can move" a_stranger_cannot_move_a_status
check "only the types with a lifecycle have one" only_the_types_with_a_lifecycle_have_one

say "the console, served"
check "GET / is the app" serves_the_console_at_root
check "the hashed bundle is served next to it" console_bundle_is_served
check "any non-api path falls back to the same index" spa_fallback_serves_the_same_index
check "unknown api paths are still 404" unknown_api_paths_still_404
check "the console reads the room over the api and renders it" console_renders_the_room
check "GET /inbox is the app, and so is a task link" serves_the_inbox_route
check "the console renders B's inbox with the task in it" console_renders_the_inbox

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
check "psql sees the chat messages in the same log" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'chat' AND room = 'general' AND project = 'pa'"
check "psql sees a reply carrying its parent" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'chat' AND array_length(parents, 1) = 1"
check "psql sees both rooms called general, one per project" psql_counts \
	"SELECT count(DISTINCT project) FROM events WHERE type = 'chat' AND room = 'general'"

check "psql sees the tasks the assignments wrote" psql_counts \
	"SELECT count(*) FROM tasks WHERE project = 'pc' AND from_user <> to_user"
check "psql sees a task delegated to an agent" psql_counts \
	"SELECT count(*) FROM tasks WHERE state = 'delegated' AND assignee_agent IS NOT NULL"
check "psql sees a task that was closed" psql_counts \
	"SELECT count(*) FROM tasks WHERE state = 'done'"
check "psql sees the per-artifact share an assignment writes" psql_counts \
	"SELECT count(*) FROM grants g JOIN tasks t ON t.artifact = g.artifact AND t.to_user = g.subject"
check "psql sees the assignment threads in the chat log" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'chat' AND room = 'handoffs'"
check "psql sees the task moves in the same threads" psql_counts \
	"SELECT count(*) FROM events e JOIN tasks t ON t.thread = e.thread WHERE e.type = 'task'"
check "psql sees the status trail" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'status' AND body = 'open->triaged'"
check "psql sees a status event carrying its predecessor" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'status' AND array_length(parents, 1) = 1"
check "psql sees the artifact ended where the trail says" psql_counts \
	"SELECT count(*) FROM artifacts WHERE status = 'done' AND type = 'bug' AND project = 'pc'"
check "psql sees auto_delegate switched off for one user" psql_counts \
	"SELECT count(*) FROM users WHERE auto_delegate = false"

check "node survived the run" kill -0 "$SERVE_PID"

# ------------------------------------------------------------------- phase 5
#
# Federation. Two nodes, each with its own Postgres cluster, its own name and
# its own copy of the schema, seeded with the same principals so that a peer can
# authenticate - a token is a local credential and is handed over out of band,
# which is why it is copied here rather than replicated.
#
# Everything after the setup is driven through the real `flowy sync`, against
# two real `flowy serve` processes, over the wire.

say "federation: a second node"
PG5A_PORT="$(free_port 54400)"
PG5B_PORT="$(free_port "$((PG5A_PORT + 1))")"
DSN5A="postgres://$PGUSER@127.0.0.1:$PG5A_PORT/$DBNAME?sslmode=disable"
DSN5B="postgres://$PGUSER@127.0.0.1:$PG5B_PORT/$DBNAME?sslmode=disable"
check "a cluster and a schema for node A" start_pg5 A "$PGDATA5A" "$PGSOCK5A" "$PG5A_PORT"
check "a cluster and a schema for node B" start_pg5 B "$PGDATA5B" "$PGSOCK5B" "$PG5B_PORT"

: >"$WORK/ids5"
if seed5_out="$(DATABASE_URL="$DSN5A" "$WORK/smoke" seed 2>&1)"; then
	# The same seed as Phase 1, under names of its own: two people, their
	# agents, their tokens and an operator, on the first of the two nodes.
	printf '%s\n' "$seed5_out" | sed 's/^/N5_/' >>"$WORK/ids5"
	# shellcheck source=/dev/null
	. "$WORK/ids5"
	printf 'PASS principals seeded on node A\n'
	passed=$((passed + 1))
else
	printf 'FAIL seed principals on node A\n%s\n' "$seed5_out" | indent
	failed=$((failed + 1))
fi
check "node B is handed the same principals and tokens" copy_principals "$DSN5A" "$DSN5B"

NODE5A_PORT="$(free_port 8890)"
NODE5B_PORT="$(free_port "$((NODE5A_PORT + 1))")"
remember5 N5_DSN_A "$DSN5A"
remember5 N5_DSN_B "$DSN5B"
remember5 N5_PORT_A "$NODE5A_PORT"
remember5 N5_PORT_B "$NODE5B_PORT"

# FLOWY_PEERS is who may push a delta at each node: the principal replication
# runs as, and nobody else. A pull is filtered to what the token may already
# read; a push writes rows of the caller's choosing into the database and merges
# them last-writer-wins, so it is the operator who says whose token may do it.
DATABASE_URL="$DSN5A" FLOWY_NODE=nodeA FLOWY_OPERATOR="${N5_USER_OP:-}" \
	FLOWY_PEERS="${N5_USER_B:-}" \
	./flowy serve -addr "127.0.0.1:$NODE5A_PORT" >"$NODE5A_LOG" 2>&1 &
NODE5A_PID=$!
DATABASE_URL="$DSN5B" FLOWY_NODE=nodeB FLOWY_OPERATOR="${N5_USER_OP:-}" \
	FLOWY_PEERS="${N5_USER_B:-}" \
	./flowy serve -addr "127.0.0.1:$NODE5B_PORT" >"$NODE5B_LOG" 2>&1 &
NODE5B_PID=$!
printf 'nodeA pid %s on 127.0.0.1:%s\nnodeB pid %s on 127.0.0.1:%s\n' \
	"$NODE5A_PID" "$NODE5A_PORT" "$NODE5B_PID" "$NODE5B_PORT"

check "node A comes up" "$WORK/smoke" healthz "http://127.0.0.1:$NODE5A_PORT/healthz"
check "node B comes up" "$WORK/smoke" healthz "http://127.0.0.1:$NODE5B_PORT/healthz"
check "two nodes, two databases, nothing shared yet" both_nodes_are_up_and_apart
check "the replication token authenticates on both" the_same_token_authenticates_on_both

say "what replicates"
check "A opens pa up to pb, which is what makes pa replicable" a_opens_pa_up_to_pb_on_node_a
check "A writes a shared artifact" a_writes_a_shared_artifact
check "A writes a personal one, and one in a project with no grant" a_writes_what_the_peer_may_not_see
check "A appends a thread of two events" a_appends_a_thread
check "B writes one of its own" b_writes_one_of_its_own
check "sync: A pulls B's delta and pushes its own" the_first_sync
check "A's artifact is on B, same id, same hlc, same author" the_shared_artifact_is_on_b
check "B's artifact is on A the same way" bs_artifact_is_on_a
check "the thread is on B with its parents intact" the_thread_is_on_b_with_its_parents

say "permission-filtered replication"
check "the personal artifact did not cross" the_personal_artifact_did_not_replicate
check "neither did the project the peer has no grant into" the_ungranted_project_did_not_replicate
check "and the pull endpoint does not offer either of them" the_delta_offers_only_what_the_peer_may_read

say "conflict"
check "the same artifact is edited on A, then on B, with no sync between" \
	the_same_artifact_is_edited_on_both_nodes
check "after a sync both nodes hold the later edit, once" both_nodes_converge_on_the_later_edit

say "tombstones travel"
check "A deletes it, and the delete syncs" a_delete_on_a_reaches_b
check "it is gone from B's list and search, and still a row in B's table" the_tombstone_is_on_b_too

say "a handoff across the fabric"
check "an assignment on A arrives on B as a task, a share and a thread" \
	an_assignment_replicates_as_a_task

say "cursors"
check "a sync with nothing new transfers nothing" a_sync_with_nothing_new_moves_nothing
check "the same delta pushed twice is applied once" pushing_the_same_delta_twice_applies_it_once
check "each node bookmarked the other" both_nodes_know_the_other_as_a_peer
check "the bookmarks are the operator's view" the_bookmarks_are_the_operators_view
check "sync refuses a peer it cannot name and a token it cannot resolve" \
	sync_refuses_a_peer_it_cannot_name

say "both databases, as a second client"
check "the two databases agree, row for row" the_two_databases_agree_row_for_row
check "psql sees A's rows on B, stamped with the node that wrote them" psql5_counts "$DSN5B" \
	"SELECT count(*) FROM artifacts WHERE node = 'nodeA'"
check "psql sees B's rows on A" psql5_counts "$DSN5A" \
	"SELECT count(*) FROM artifacts WHERE node = 'nodeB'"
check "psql sees the replicated DAG on B" psql5_counts "$DSN5B" \
	"SELECT count(*) FROM events WHERE node = 'nodeA' AND array_length(parents, 1) = 1"
check "psql sees the tombstone on B, not a missing row" psql5_counts "$DSN5B" \
	"SELECT count(*) FROM artifacts WHERE tombstone = true AND node = 'nodeA'"
check "psql sees the replicated task and its share on B" psql5_counts "$DSN5B" \
	"SELECT count(*) FROM tasks t JOIN grants g ON g.artifact = t.artifact AND g.subject = t.to_user"
check "psql sees the personal artifact on A and only on A" psql5_counts "$DSN5A" \
	"SELECT count(*) FROM artifacts WHERE visibility = 'personal' AND project IS NULL"
check "psql sees both cursors on both nodes" psql5_counts "$DSN5A" \
	"SELECT count(*) FROM peers WHERE pull_cursor > 0 AND pushed_cursor > 0"

check "node A survived the run" kill -0 "$NODE5A_PID"
check "node B survived the run" kill -0 "$NODE5B_PID"

# ------------------------------------------------------------------- phase 6
#
# The forge bridge, on the first node. Everything here runs against MockForge -
# the gate has no GitHub, no credential and no network - so what is being tested
# is the node's half: what filing writes, what a closed issue does to an
# artifact, and that the reviewer loop carries a comment in and a reply out
# exactly once. The gh and glab argv and their parsing are covered by the unit
# tests above; the real path runs on a host that has the CLI and a login.

say "the forge, and which one"
check "FLOWY_FORGE=mock selects the mock, with gh installed beside it" the_node_selected_the_mock
check "a forge nobody has heard of is refused at startup" an_unknown_forge_is_refused_at_startup

say "filing"
check "A files a bug as an issue, and the link lands on the artifact" a_files_the_carburettor_bug
check "an ordinary read of the artifact carries the link" the_link_is_on_the_artifact
check "filing the same artifact twice is a conflict, not a second issue" \
	filing_the_same_artifact_twice_is_refused
check "a repo that is not owner/name files nothing" a_repo_that_is_not_a_repo_is_refused
check "a principal who cannot read the artifact gets 404 on all three" \
	a_stranger_cannot_file_or_sync_it

say "the issue closes"
check "the reviewer closes it on the forge" the_issue_is_closed_on_the_forge
check "a closed issue moves the artifact to done, and the trail says via forge" \
	a_closed_issue_moves_the_artifact_to_done
check "a refresh that finds nothing new writes nothing" refreshing_a_closed_issue_moves_nothing

say "the reviewer loop"
check "the reviewer comments on the issue" the_reviewer_comments_on_the_issue
check "sync threads it into the artifact's thread as an event" \
	the_comment_becomes_an_event_in_the_thread
check "a reply in that thread is pushed out as a comment" a_reply_in_the_thread_reaches_the_forge
check "and does not come back in as one" a_pushed_reply_does_not_come_back
check "a sync with nothing new changes nothing, twice over" syncing_with_nothing_new_is_a_no_op
check "gh was never invoked" gh_was_never_invoked

say "the forge, as a second client"
check "psql sees the artifact filed and marked reported" psql_counts \
	"SELECT count(*) FROM artifacts WHERE reported = true AND external IS NOT NULL"
check "psql sees the issue in the link" psql_counts \
	"SELECT count(*) FROM artifacts WHERE external::text LIKE '%o/r%'"
check "psql sees the filing in the event log" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'forge' AND body LIKE 'filed o/r#%'"
check "psql sees the comment that came in from the forge" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'chat' AND room = 'forge' AND actor = 'forge:reviewer'"
check "psql sees the reply that went out, in the same thread" psql_counts \
	"SELECT count(*) FROM events e
	   JOIN events f ON f.thread = e.thread AND f.actor = 'forge:reviewer'
	  WHERE e.type = 'chat' AND e.room = 'forge' AND e.actor <> 'forge:reviewer'"
check "psql sees the status move the forge caused" psql_counts \
	"SELECT count(*) FROM events WHERE type = 'status' AND body = 'open->done'
	   AND meta::text LIKE '%forge%'"

# --------------------------------------------------------- the security fixes
#
# One check per defect, in the order they were found. Every one of them fails on
# the code as it was: that is what makes it a fix rather than a claim.

say "replication is not a way in"
check "push answers a peer this node names and refuses every other token (CRITICAL 1)" \
	sync_push_is_only_for_a_peer
check "a pushed grant into a project the pusher has no say over is refused (CRITICAL 1)" \
	a_forged_grant_is_refused
check "a pushed reading no clock could have made is refused, and the clock survives (CRITICAL 1)" \
	a_poisoned_clock_reading_is_refused

say "an id is not a capability"
check "writing to an artifact you cannot read is 404, and it is untouched (CRITICAL 2)" \
	an_unreadable_id_cannot_be_taken_over

say "the log says who wrote it"
check "an event is signed by the token that appended it (HIGH 3)" an_event_carries_the_callers_name

say "what leaves the machine"
check "only the owner files, and only into the operator's repositories (HIGH 4)" \
	only_the_owner_files_to_an_allowed_repo
check "a reply the forge refused is sent next time, and the ones that went out are not (MEDIUM 5)" \
	a_refused_reply_is_not_posted_twice

say "federation catches up"
check "an artifact shared after the cursor passed it still replicates (MEDIUM 6)" \
	a_late_grant_still_replicates

say "the remaining three"
check "mem_write will not rewrite something that is not a memory item (MEDIUM 7)" \
	mem_write_stays_in_its_namespace
check "a reply does not join a thread its parent hid from the speaker (LOW 8)" \
	a_reply_does_not_adopt_an_unreadable_thread
check "a driver that will not count its rows is reported, not assumed (LOW 9)" \
	go test -count=1 -run TestAffectedRowsReportsTheDriversError ./internal/store
check "a comment at the cursor is not forgotten and threaded in twice (LOW 10)" \
	go test -count=1 -run 'TestExternalRef(Cursors|KeepsSameSecondCommentsSeen)|TestSeenComment' \
	./internal/store

# ------------------------------------------------- the second round of fixes
#
# The re-review of the first round: push checked three tables and not the
# fourth, and checked those three against readability where the API checks
# ownership. Plus five in the forge bridge. One check each, and every one of
# them fails on the code as it was.

say "a push is what that principal could have written"
check "an event pushed under somebody else's name is refused (HIGH 1)" \
	a_pushed_event_is_signed_by_the_pusher
check "a reader cannot rewrite the artifact it was shown (HIGH 2)" \
	a_read_share_is_not_a_write
check "a share of somebody else's artifact is refused however it is signed (HIGH 3)" \
	a_share_is_only_the_owners_to_hand_over
check "a pushed task cannot name a thread the pusher may not read (HIGH 4)" \
	a_pushed_task_cannot_name_a_thread_it_cannot_read
check "the same rule, in the store, row by row (HIGH 1)" \
	go test -count=1 -run 'TestCheckEventIsWhatTheAPIWouldHaveAllowed|TestSyncApplyAsRefusesAForgedEvent' \
	./internal/store
check "the endpoint's minted types and the store's are one list (HIGH 1)" \
	go test -count=1 -run TestMintedTypesAgreeWithTheStore .

say "the forge, again"
check "a status refresh obeys the operator's repository list (HIGH 5)" \
	forge_status_obeys_the_repo_list
check "a pull that died halfway does not thread those comments in twice (MED 7)" \
	a_half_threaded_pull_is_not_threaded_twice
check "the node asks the forge which login it posts as (MED 8)" \
	the_node_asks_the_forge_who_it_is
check "a CLI failure names the call and not what it carried (MED 9)" \
	go test -count=1 -run 'TestRunCommandDoesNotEchoTheArgv|TestDescribeArgs|TestSelfLoginArgv|TestMockForgeAnswersItsOwnLogin' \
	./internal/forge
check "a comment made while the issue was being filed is not lost (MED 10)" \
	a_comment_made_while_filing_is_not_lost

say "federation, again"
check "a grant that opens a project carries more of it than one page (MED 6)" \
	a_project_grant_carries_more_than_a_page

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
	for log in "$NODE5A_LOG" "$NODE5B_LOG"; do
		if [ -f "$log" ]; then
			printf '%s:\n' "$(basename "$log")"
			indent <"$log"
		fi
	done
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
