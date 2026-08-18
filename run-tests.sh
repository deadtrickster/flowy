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

# Two things to have right before running it, because getting either wrong fails
# in a place that does not name the real cause:
#
#   - Run it as an ordinary user, not as root. initdb refuses to run as root,
#     and a container or an agent harness that lands you there gets an error
#     about the database that reads like a Postgres problem rather than a wrong
#     user. `sudo -u someone ./run-tests.sh` from a root shell.
#   - node >= 20. The console is built with vite 6, and node 18 fails `npm run
#     build` partway through with a message about the bundler.
#
# Both are checked in the environment section below, which says so and stops
# rather than letting the failure surface later as something else.

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
# The schema-drift section: an older database, brought up to date and made to
# serve. Everything it needs to hand from one check to the next lives here.
UPG="$WORK/upgrade"
readonly ROOT WORK PGDATA PGSOCK PGLOG SERVE_LOG MCP_LOG DBNAME
readonly PGDATA5A PGDATA5B PGSOCK5A PGSOCK5B NODE5A_LOG NODE5B_LOG GH_CANARY UPG

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
	local pid data mountpoint pidfile
	# The fuse mounts first, and by reading /proc rather than by remembering:
	# a mountpoint inside $WORK that is still attached when the rm -rf below
	# runs is a directory that cannot be removed and a server process that
	# outlives this script. Phase 7 mounts under $WORK and nothing else here
	# does, so anything of type fuse under it is ours.
	while read -r mountpoint; do
		fusermount3 -u "$mountpoint" >/dev/null 2>&1 ||
			fusermount -u "$mountpoint" >/dev/null 2>&1
	done < <(awk -v work="$WORK/" '$3 ~ /^fuse\./ && index($2, work) == 1 {print $2}' /proc/self/mounts)
	for pidfile in "$WORK"/*.pid; do
		[ -f "$pidfile" ] || continue
		pid="$(cat "$pidfile" 2>/dev/null)"
		if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
			kill "$pid" 2>/dev/null
		fi
	done
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
#
# Ask it for a base BELOW the kernel's ephemeral range - 32768-60999 on Linux by
# default, `cat /proc/sys/net/ipv4/ip_local_port_range`. It cannot see a port
# that some outbound connection is using as its source: the probe below asks
# whether anything is LISTENING, and an established socket answers no while
# still making bind() fail with "Address already in use". This run makes
# thousands of outbound connections - curl, psql, playwright - so a listener
# started late on a port inside that range is a coin toss, and phase 5's
# postgres was exactly that. It came up on 54400 for months and then failed to
# bind it twice in three runs, taking 40 federation checks down with it and
# reading, from the failure text, like a leftover cluster from the last run.
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

# go_build builds the node the way a release build does: with the commit it was
# built from linked into the version string, so that what /healthz, the MCP
# serverInfo and `flowy version` report names this build and not just the phase
# it belongs to. A tree with no git and no commits still builds; the version
# then says "src", which is what an unstamped build honestly is.
go_build() {
	local stamp
	stamp="$(git -C "$ROOT" rev-parse --short=7 HEAD 2>/dev/null || true)"
	if [ -z "$stamp" ]; then
		go build -o "$ROOT/flowy" .
		return
	fi
	go build -ldflags "-X main.buildStamp=$stamp" -o "$ROOT/flowy" .
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

# scalar QUERY - one value straight out of the single node's database.
scalar() { psql -v ON_ERROR_STOP=1 -tA -c "$1"; }

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

# ------------------------------------------ an older database meets this binary
#
# THE FAILURE THIS SECTION EXISTS FOR, and why nothing above it can see it.
#
# The refusal-ledger work added a table. The live node was redeployed with the
# binary that queries it, onto a database that never got the table, and every
# /api/artifacts read returned 500 for four minutes:
#
#     store: count what was refused: pq: relation "refused_authorship" does not exist
#
# That code had passed the whole gate, twice. It could not have failed here.
# EVERY CHECK ABOVE RUNS AGAINST A DATABASE BUILT FROM schema.sql THIS RUN, and
# a database built from the current schema.sql has, by construction, every table
# the current binary asks for. The live node is the only place an EXISTING
# database meets NEW code, and until now nothing in this repository migrated one
# or tested one. The bug was not that a check was wrong; it was that the whole
# gate was standing in a place from which this class of failure is invisible.
#
# So these checks start somewhere else: from the schema as it was at an earlier
# commit, which is where the live node's database actually is at the moment of a
# deploy.
#
# WHICH earlier commit. The baseline is the newest revision of schema.sql whose
# DDL differs from the working tree's - that is, the schema as it stood
# immediately before the most recent schema change landed. Three reasons over
# the alternatives:
#
#   - It is exactly the state the live database is in when a deploy carrying a
#     schema change arrives. That is the state that broke, and it is the state
#     no other check has ever occupied.
#   - It never goes stale and needs no maintenance. A pinned baseline file
#     checked into the repo would be a second copy of the schema that somebody
#     has to remember to update - and "somebody has to remember" is the whole
#     of the incident being fixed here. A merge-base with a previous release
#     would be better still, but there are no releases to take one against.
#   - It is always genuinely older. Comment-only edits to schema.sql are
#     skipped when picking it, so the baseline is never a database that is
#     already up to date and the checks below are never vacuously green.
#
# FLOWY_BASELINE_REV overrides it with any commit, which is how you point this
# at what the dogfood node is really running:
#
#     FLOWY_BASELINE_REV=$(cat ~/Projects/flowy-dogfood/.deployed-commit) ./run-tests.sh
#
# IT FAILS, IT DOES NOT SKIP. If no baseline can be resolved - no git, no
# history, a squashed tree - the first check FAILS and the rest fail behind it.
# A check that quietly does nothing when it cannot find its fixture reports
# green for a run in which it tested nothing, and this gate has already been
# bitten twice by exactly that.

# upg_dsn NAME - a DSN for one of this section's databases on the gate cluster.
upg_dsn() { printf 'postgres://%s@127.0.0.1:%s/%s?sslmode=disable\n' "$PGUSER" "$PGPORT" "$1"; }

# upg_fingerprint DB FILE - the structure of one database, written sorted.
# scripts/schema-fingerprint.sql is the single definition of what that means;
# scripts/migrate.sh reads the same file, so the gate and the deploy cannot
# drift apart on the question of what a schema is.
upg_fingerprint() {
	psql -v ON_ERROR_STOP=1 -tAq -F'|' -d "$1" -f "$ROOT/scripts/schema-fingerprint.sql" |
		LC_ALL=C sort >"$2"
}

# upg_ddl - schema.sql with the prose taken out, on stdin. Whole-line comments
# and blank lines only: enough that rewording a comment does not read as a
# schema change, without pretending to parse SQL.
upg_ddl() { sed -e 's/[[:space:]]*$//' -e '/^[[:space:]]*--/d' -e '/^[[:space:]]*$/d'; }

# upg_code PATH [TOKEN] - the status of one request against this section's node.
upg_code() {
	local path=$1 token=${2-} port
	port="$(cat "$UPG/port")"
	local -a curl_args=(--silent --show-error -o /dev/null -w '%{http_code}' -m 15)
	[ -n "$token" ] && curl_args+=(-H "Authorization: Bearer $token")
	curl "${curl_args[@]}" "http://127.0.0.1:$port$path"
}

# upg_token - the seeded user token for this section's database.
upg_token() {
	# shellcheck source=/dev/null
	. "$UPG/ids"
	printf '%s' "${TOKEN_A:-}"
}

# The baseline: the newest revision of schema.sql whose DDL differs from the
# working tree's. Fails loudly rather than picking nothing.
upgrade_baseline_is_a_real_older_schema() {
	local rev current
	mkdir -p "$UPG"
	if ! git -C "$ROOT" rev-parse HEAD >/dev/null 2>&1; then
		printf 'no git history here, so there is no older schema to start from.\n' >&2
		printf 'This check does not skip: without it the gate only ever sees a fresh\n' >&2
		printf 'database, which is how the refusal-ledger outage got through twice.\n' >&2
		printf 'Set FLOWY_BASELINE_REV to a commit that has a schema.sql.\n' >&2
		return 1
	fi
	if [ -n "${FLOWY_BASELINE_REV:-}" ]; then
		rev="$FLOWY_BASELINE_REV"
		if ! git -C "$ROOT" show "$rev:schema.sql" >"$UPG/baseline-schema.sql" 2>/dev/null; then
			printf 'FLOWY_BASELINE_REV=%s has no schema.sql\n' "$rev" >&2
			return 1
		fi
	else
		current="$(upg_ddl <"$ROOT/schema.sql")"
		rev=""
		while read -r candidate; do
			[ -n "$candidate" ] || continue
			# Skip revisions whose DDL is what the working tree already has:
			# the newest one usually is, and a comment-only edit would
			# otherwise hand these checks a database that is already current.
			if [ "$(git -C "$ROOT" show "$candidate:schema.sql" 2>/dev/null | upg_ddl)" = "$current" ]; then
				continue
			fi
			rev="$candidate"
			break
		done < <(git -C "$ROOT" log --format=%H -- schema.sql)
		if [ -z "$rev" ]; then
			printf 'every revision of schema.sql in this history has the DDL the working\n' >&2
			printf 'tree has, so there is no older schema to migrate FROM.\n' >&2
			printf 'This check FAILS rather than skipping: a run that cannot build an\n' >&2
			printf 'older database has not tested the migration, and saying so is the\n' >&2
			printf 'whole point of it. Set FLOWY_BASELINE_REV to a commit further back.\n' >&2
			return 1
		fi
		git -C "$ROOT" show "$rev:schema.sql" >"$UPG/baseline-schema.sql" || return 1
	fi
	printf '%s\n' "$rev" >"$UPG/baseline.rev"
	printf 'baseline %s\n' "$(git -C "$ROOT" log -1 --format='%h %ad %s' --date=short "$rev" | cut -c1-100)"
	printf 'schema.sql there is %s lines, here is %s\n' \
		"$(wc -l <"$UPG/baseline-schema.sql")" "$(wc -l <"$ROOT/schema.sql")"
}

# A database at the baseline schema, and a fresh one beside it to compare to.
upgrade_baseline_database_loads() {
	"$PG_BIN/createdb" gate_baseline || return 1
	psql -v ON_ERROR_STOP=1 -q -d gate_baseline -f "$UPG/baseline-schema.sql" || return 1
	upg_fingerprint gate_baseline "$UPG/fp-baseline.txt"
	printf 'gate_baseline holds %s schema objects\n' "$(wc -l <"$UPG/fp-baseline.txt")"
}

# NO FALSE POSITIVES. If the fingerprint reported a difference between two
# databases that are the same schema, every check below it would be noise, and
# the first person to see one would start ignoring them. The gate's own
# database - the one every other check in this run passed against - is compared
# to a fresh load of schema.sql, and they must be byte-identical.
upgrade_fingerprint_agrees_with_the_gates_own_database() {
	"$PG_BIN/createdb" gate_fresh || return 1
	psql -v ON_ERROR_STOP=1 -q -d gate_fresh -f "$ROOT/schema.sql" || return 1
	upg_fingerprint gate_fresh "$UPG/fp-fresh.txt"
	upg_fingerprint "$DBNAME" "$UPG/fp-gate.txt"
	if ! diff -u "$UPG/fp-gate.txt" "$UPG/fp-fresh.txt" >"$UPG/diff-gate-fresh.txt"; then
		printf 'the gate database and a fresh one are not the same schema:\n' >&2
		head -40 "$UPG/diff-gate-fresh.txt" >&2
		return 1
	fi
	printf '%s objects, identical to the database the rest of this run uses\n' \
		"$(wc -l <"$UPG/fp-fresh.txt")"
}

# AND TEETH. The other way a fingerprint can be useless is by seeing nothing
# wherever it looks. A database with no schema in it at all must come back
# empty, and must differ from a fresh one - if this passes vacuously, so does
# every comparison below.
upgrade_fingerprint_sees_an_empty_database() {
	local n
	"$PG_BIN/createdb" gate_empty || return 1
	upg_fingerprint gate_empty "$UPG/fp-empty.txt"
	n="$(wc -l <"$UPG/fp-empty.txt")"
	if [ "$n" -ne 0 ]; then
		printf 'an empty database fingerprinted %s objects, want 0\n' "$n" >&2
		return 1
	fi
	if diff -q "$UPG/fp-empty.txt" "$UPG/fp-fresh.txt" >/dev/null; then
		printf 'an empty database and a fresh one fingerprinted the same - the\n' >&2
		printf 'comparison cannot see a missing table, so nothing below it means anything\n' >&2
		return 1
	fi
	printf 'empty is 0 objects and differs from fresh by %s lines\n' \
		"$(diff "$UPG/fp-empty.txt" "$UPG/fp-fresh.txt" | grep -c '^[<>]')"
}

# The drift itself: what the baseline database is missing, named. This is the
# gap the deploy has to close, and on the day this was written it was one table
# called refused_authorship.
upgrade_baseline_is_behind_the_current_schema() {
	if diff -q "$UPG/fp-baseline.txt" "$UPG/fp-fresh.txt" >/dev/null; then
		printf 'the baseline database already matches the current schema, so these\n' >&2
		printf 'checks would migrate nothing and prove nothing. Pick an older\n' >&2
		printf 'FLOWY_BASELINE_REV.\n' >&2
		return 1
	fi
	printf 'the baseline database is missing %s objects the current schema has:\n' \
		"$(comm -13 "$UPG/fp-baseline.txt" "$UPG/fp-fresh.txt" | wc -l)"
	comm -13 "$UPG/fp-baseline.txt" "$UPG/fp-fresh.txt" | head -20
}

# THE MIGRATION PATH ITSELF - the script scripts/deploy.sh runs, not a second
# implementation of it that happens to agree today.
upgrade_migrate_brings_the_baseline_up() {
	"$ROOT/scripts/migrate.sh" "$(upg_dsn gate_baseline)"
}

# THE CHECK THIS SECTION IS FOR. A database that started at the baseline and had
# the migration applied must be structurally IDENTICAL to one built fresh from
# schema.sql. Anything left over is drift that would reach production, and the
# shape it takes is not hypothetical: schema.sql is CREATE TABLE IF NOT EXISTS
# throughout, so a column added inside a CREATE TABLE body and nowhere else is a
# no-op on every database that already has the table. It works on a fresh
# database. It works in every check above this section. It is a 500 on the node.
upgrade_migrated_matches_a_fresh_database() {
	upg_fingerprint gate_baseline "$UPG/fp-migrated.txt"
	if diff -u "$UPG/fp-fresh.txt" "$UPG/fp-migrated.txt" >"$UPG/diff-migrated.txt"; then
		printf 'the migrated baseline is the same schema as a fresh database (%s objects)\n' \
			"$(wc -l <"$UPG/fp-migrated.txt")"
		return 0
	fi
	printf 'applying schema.sql to an older database did NOT bring it up to date.\n' >&2
	printf 'Lines marked - are in a fresh database and missing after the migration;\n' >&2
	printf '+ are left over in the migrated one. This is drift that reaches the live\n' >&2
	printf 'node, where it is a 500 on whatever reads it first.\n' >&2
	printf 'The usual cause is a column or a constraint added to a CREATE TABLE IF\n' >&2
	printf 'NOT EXISTS body with no matching ALTER TABLE ... IF NOT EXISTS beside it.\n' >&2
	head -60 "$UPG/diff-migrated.txt" >&2
	return 1
}

# And from nothing at all, which is the other end of the same path: a brand new
# node's database is created by running exactly this.
upgrade_migrate_brings_an_empty_database_up() {
	"$ROOT/scripts/migrate.sh" "$(upg_dsn gate_empty)" >"$UPG/migrate-empty.log" 2>&1 || {
		cat "$UPG/migrate-empty.log" >&2
		return 1
	}
	upg_fingerprint gate_empty "$UPG/fp-empty-migrated.txt"
	if ! diff -u "$UPG/fp-fresh.txt" "$UPG/fp-empty-migrated.txt" >"$UPG/diff-empty.txt"; then
		printf 'migrating an empty database did not produce the current schema:\n' >&2
		head -40 "$UPG/diff-empty.txt" >&2
		return 1
	fi
	printf 'an empty database migrates to the same %s objects as a fresh one\n' \
		"$(wc -l <"$UPG/fp-empty-migrated.txt")"
}

# Principals in the older database, so the reads below are real reads with a
# real token rather than a 401 that never reaches the store.
upgrade_seed_the_baseline() {
	DATABASE_URL="$(upg_dsn gate_baseline)" "$WORK/smoke" seed >"$UPG/ids" 2>"$UPG/seed.err" || {
		cat "$UPG/seed.err" >&2
		return 1
	}
	grep -q '^TOKEN_A=' "$UPG/ids" || {
		printf 'the seed wrote no TOKEN_A:\n' >&2
		cat "$UPG/ids" >&2
		return 1
	}
	printf 'seeded %s principals into the older database\n' "$(grep -c '^USER_' "$UPG/ids")"
}

# HEALTHZ IS NOT A READ, demonstrated rather than asserted from memory. The node
# comes up against a database that is behind it and answers 200 on /healthz the
# whole time - which is why the outage ran for four minutes before anybody knew.
# What a real read does here depends on whether the last schema change happened
# to touch a read path, so it is REPORTED and not asserted; the deterministic
# version of that assertion is the missing-relation check further down, which
# does not depend on what changed.
upgrade_node_is_up_on_the_older_database() {
	local health read
	health="$(upg_code /healthz)"
	read="$(upg_code /api/artifacts "$(upg_token)")"
	if [ "$health" != "200" ]; then
		printf 'the node did not come up against the older database: /healthz %s\n' "$health" >&2
		tail -30 "$UPG/serve.log" >&2
		return 1
	fi
	printf '/healthz 200 on a database that is behind the binary\n'
	printf 'a real read there: /api/artifacts -> %s%s\n' "$read" \
		"$([ "$read" = 200 ] || printf ' (this is the outage)')"
}

upgrade_read_serves_after_the_migration() {
	local code
	code="$(upg_code /api/artifacts "$(upg_token)")"
	if [ "$code" != "200" ]; then
		printf '/api/artifacts is %s against the migrated database, want 200\n' "$code" >&2
		tail -30 "$UPG/serve.log" >&2
		return 1
	fi
	printf 'the older database, migrated, serves the read that took the node down\n'
}

# THE TEETH ON THE TWO CHECKS ABOVE. A check that only ever watches a working
# node cannot tell you it would have noticed a broken one. So break it, in the
# way production broke: take away a relation the binary queries. `artifacts` is
# chosen because /api/artifacts reads it by definition - that will still be true
# whatever schema.sql does next, so this check can never quietly go vacuous the
# way a check pinned to whatever changed most recently would.
upgrade_a_missing_relation_is_an_outage_the_gate_can_see() {
	local health read
	psql -v ON_ERROR_STOP=1 -q -d gate_baseline -c 'DROP TABLE artifacts' || return 1
	read="$(upg_code /api/artifacts "$(upg_token)")"
	health="$(upg_code /healthz)"
	if [ "$read" = "200" ]; then
		printf 'a read served 200 against a database with no artifacts table.\n' >&2
		printf 'That means the check above cannot fail, and a green run here would\n' >&2
		printf 'say nothing about whether the migration worked.\n' >&2
		return 1
	fi
	if [ "$health" != "200" ]; then
		printf 'note: /healthz is %s, not 200 - it used to stay 200 through this\n' "$health"
	else
		printf '/api/artifacts -> %s while /healthz still says 200: the outage, exactly\n' "$read"
	fi
}

upgrade_migrate_repairs_it() {
	"$ROOT/scripts/migrate.sh" "$(upg_dsn gate_baseline)" >"$UPG/migrate-repair.log" 2>&1 || {
		cat "$UPG/migrate-repair.log" >&2
		return 1
	}
	local code
	code="$(upg_code /api/artifacts "$(upg_token)")"
	if [ "$code" != "200" ]; then
		printf 'after re-applying the schema the read is still %s\n' "$code" >&2
		tail -30 "$UPG/serve.log" >&2
		return 1
	fi
	printf 'the same running node serves 200 again once the relation is back\n'
}

# The deploy has to apply the schema BEFORE it restarts the unit, or it is the
# incident with extra steps. Order in a script is a fact about the file, so it
# is read out of the file rather than trusted.
upgrade_deploy_migrates_before_it_restarts() {
	local deploy migrate restart
	deploy="$ROOT/scripts/deploy.sh"
	[ -x "$deploy" ] || {
		printf '%s is not executable\n' "$deploy" >&2
		return 1
	}
	migrate="$(grep -n 'scripts/migrate\.sh' "$deploy" | grep -v '^[0-9]*:#' | head -1 | cut -d: -f1)"
	restart="$(grep -n 'systemctl --user restart' "$deploy" | grep -v '^[0-9]*:#' | head -1 | cut -d: -f1)"
	if [ -z "$migrate" ]; then
		printf 'scripts/deploy.sh never runs scripts/migrate.sh, so a deploy still\n' >&2
		printf 'restarts the node onto whatever schema the database happens to have.\n' >&2
		return 1
	fi
	if [ -z "$restart" ]; then
		printf 'scripts/deploy.sh no longer restarts the unit - this check needs\n' >&2
		printf 'updating to whatever replaced it, not deleting.\n' >&2
		return 1
	fi
	if [ "$migrate" -ge "$restart" ]; then
		printf 'deploy.sh migrates at line %s and restarts at line %s: the new binary\n' \
			"$migrate" "$restart" >&2
		printf 'would start before the schema it needs exists.\n' >&2
		return 1
	fi
	printf 'migrate at line %s, restart at line %s\n' "$migrate" "$restart"
}

# A migration that cannot reach the database must say so and stop, while the old
# binary is still serving. Shrugging and letting the deploy carry on is the
# outage with a log line in front of it.
upgrade_migrate_refuses_a_database_it_cannot_reach() {
	local out status
	out="$("$ROOT/scripts/migrate.sh" "postgres://nobody@127.0.0.1:$PGPORT/no_such_database?sslmode=disable" 2>&1)"
	status=$?
	if [ "$status" -eq 0 ]; then
		printf 'migrate.sh exited 0 against a database that does not exist:\n%s\n' "$out" >&2
		return 1
	fi
	printf '%s\n' "$out" | grep -q 'REFUSED' || {
		printf 'it failed, but did not say it refused:\n%s\n' "$out" >&2
		return 1
	}
	printf 'exit %s, and says REFUSED\n' "$status"
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

# A tombstone that only says "gone" is a delete with extra steps. What makes it
# worth keeping the row is that it can name who took it back and when, so the
# reader who goes looking gets an answer instead of a silence.
#
# B is asked as well as A, and that is the half that matters: B holds the pa
# grant and could have read the bug right up until it went, so B is a principal
# the row would otherwise have been readable by, and B is told.
tombstone_says_who_took_it_back() {
	recall
	want_status 410 GET "$TOKEN_A" "/api/artifact/$BUG" || return 1
	want_eq "the withdrawn id" "$(jqv .withdrawn.id)" "$BUG" || return 1
	want_eq "who withdrew it" "$(jqv .withdrawn.actor)" "$USER_A" || return 1
	want_eq "what it was" "$(jqv .withdrawn.type)" bug || return 1
	if [ -z "$(jqv .withdrawn.at)" ] || [ "$(jqv .withdrawn.at)" = null ]; then
		printf 'the withdrawal names no moment: %s\n' "$API_BODY" >&2
		return 1
	fi
	# And none of the row itself: the artifact stopped being the artifact, so
	# the title and the body do not come back through the door that says so.
	want_eq "the title leaked" "$(printf '%s' "$API_BODY" | grep -c 'cannot parse a handoff' || true)" 0 || return 1
	want_status 410 GET "$TOKEN_B" "/api/artifact/$BUG" || return 1
	want_eq "what B is told" "$(jqv .withdrawn.actor)" "$USER_A" || return 1
	printf 'withdrawn by %s at %s, and B is told the same\n' "$(jqv .withdrawn.actor)" "$(jqv .withdrawn.at)"
}

# THE ORDER OF THE TWO CHECKS, over the wire.
#
# 410 goes only to a principal the row would otherwise have been readable by.
# Everybody else gets 404, and exists-but-not-for-you has to be word for word
# never-existed: an id is guessable, so a door that distinguished them would let
# anyone enumerate what a project holds by asking for ids and reading the code
# that comes back.
#
# So A withdraws a personal row here - the visibility that cost twenty minutes -
# and B, who can read the whole of pa through the grant, gets the same 404 for it
# as for an id nobody ever wrote.
withdrawn_out_of_reach_is_indistinguishable_from_absent() {
	recall
	api POST "$TOKEN_A" /api/artifacts '{
		"type": "note",
		"title": "withdrawn and not for you",
		"body": "a flimbustor of my own",
		"visibility": "personal"
	}' || return 1
	want_eq "create status" "$API_STATUS" 200 || return 1
	local id
	id="$(jqv .id)"

	want_status 404 GET "$TOKEN_B" "/api/artifact/$id" || return 1
	local before="$API_BODY"
	want_status 200 POST "$TOKEN_A" "/api/artifact/$id/delete" || return 1

	want_status 410 GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "who withdrew it" "$(jqv .withdrawn.actor)" "$USER_A" || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$id" || return 1
	want_eq "B's answer after the withdrawal" "$API_BODY" "$before" || return 1
	# And an id nobody ever wrote answers in exactly the same words.
	want_status 404 GET "$TOKEN_B" "/api/artifact/01M0000000000000000000000Z" || return 1
	want_eq "an id nobody wrote" "$API_BODY" "$before" || return 1
	printf 'A is told who and when; B cannot tell the row from one that never existed\n'
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
	# And the ceiling, which is the half that bites silently. Claude Code
	# truncates server instructions at about 2 KB while opencode does not, so a
	# document over the limit reaches one half of a fleet whole and the other cut
	# off mid-sentence, with nothing said on either side. This document was
	# 5,835 bytes for weeks and Claude Code saw the scopes and none of the tools.
	# 1800 leaves margin: the limit is described, not measured.
	if [ "${#instructions}" -gt 1800 ]; then
		printf 'initialize returned %s bytes of instructions, want at most 1800 - the rest is silently truncated by clients that cut at 2 KB, so it belongs in guide.md\n' \
			"${#instructions}" >&2
		return 1
	fi
	# The instructions are the point of the endpoint, not decoration: an agent
	# that reads them has to come away knowing the scopes and the tools - and
	# knowing where the detail went, or the short form is just a shorter lie.
	local word
	for word in personal project shared mem_write mem_search todos guide; do
		case "$instructions" in
		*"$word"*) ;;
		*)
			printf 'the instructions never mention %s\n' "$word" >&2
			return 1
			;;
		esac
	done
	printf 'flowy %s, protocol %s, %s bytes of instructions (ceiling 1800)\n' \
		"$(rv .result.serverInfo.version)" "$(rv .result.protocolVersion)" "${#instructions}"
}

mcp_tools_list() {
	mcp tools/list "$TOKEN_A" || return 1
	local name
	for name in mem_write mem_read mem_search mem_list todos projects; do
		want_eq "$name in tools/list" \
			"$(rv "[.result.tools[] | select(.name == \"$name\")] | length")" 1 || return 1
		if [ "$(rv "[.result.tools[] | select(.name == \"$name\" and (.inputSchema.type == \"object\"))] | length")" != 1 ]; then
			printf '%s has no object input schema\n' "$name" >&2
			return 1
		fi
	done
	printf 'tools: %s\n' "$(rv '[.result.tools[].name] | join(", ")')"
}

# The detail has to be reachable two ways that do not depend on the client
# having read the instructions at all.
#
# The short text at initialize is capped, so the rest lives in the guide - and a
# pointer is only as good as the thing it points at. Both routes are checked
# because they fail differently: the resource is missed by clients that never
# call resources/read, and the tool is dropped by opencode when every tool on a
# server is disabled by permission. Neither failure says anything, so the gate
# has to be what notices.
mcp_instructions_resource() {
	mcp initialize "" '{}' || return 1
	local from_initialize from_resource from_tool
	from_initialize="$(rv .result.instructions)"

	mcp resources/list "$TOKEN_A" || return 1
	want_eq "flowy://instructions is listed" \
		"$(rv '[.result.resources[] | select(.uri == "flowy://instructions")] | length')" 1 || return 1

	mcp resources/read "$TOKEN_A" '{"uri": "flowy://instructions"}' || return 1
	want_eq "resource uri" "$(rv '.result.contents[0].uri')" flowy://instructions || return 1
	from_resource="$(rv '.result.contents[0].text')"
	if [ "${#from_resource}" -le "${#from_initialize}" ]; then
		printf 'the resource is %s bytes against %s at initialize: the detail was trimmed, not relocated\n' \
			"${#from_resource}" "${#from_initialize}" >&2
		return 1
	fi

	# The same document through the tool an agent would actually reach for.
	want_tool guide "$TOKEN_A" '{}' || return 1
	from_tool="$(tv .guide)"
	if [ "$from_tool" != "$from_resource" ]; then
		printf 'the guide tool and the resource serve different documents (%s vs %s bytes)\n' \
			"${#from_tool}" "${#from_resource}" >&2
		return 1
	fi
	local word
	for word in mem_write report_write worklog_append personal; do
		case "$from_tool" in
		*"$word"*) ;;
		*)
			printf 'the guide never mentions %s\n' "$word" >&2
			return 1
			;;
		esac
	done
	printf 'the guide is %s bytes, the same through the resource and the tool, behind %s at initialize\n' \
		"${#from_tool}" "${#from_initialize}"
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

	# It starts AT a state, not at no state. A todo raised with no status used to
	# land with "", which reads as neither outstanding nor done - so it sat on the
	# board unmovable, and the complaint that produced was blamed on agents
	# forgetting to set one. The chat door has always defaulted this; this door
	# did not, and nothing here noticed because every other check states a status.
	want_eq "a todo raised with no status starts at todo" "$(tv .item.status)" todo || return 1

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

# --------------------------------------------------------------- the worklog
#
# The worklog is events rather than a new artifact type: an append-only
# per-project stream, which is what the event DAG already is. So what these
# checks assert is not storage, it is the two invariants the surface exists to
# hold - every entry carries the seat that wrote it, and an entry references
# work by artifact id that its author could read - plus the read shape, which is
# recent-N newest first, because that is the read an agent picking up a seat
# does.

# wl_args - the arguments of one entry, built with jq so ids interpolate
# without hand-quoting JSON inside a shell string.
wl_args() {
	local what=$1 next=${2-} as_of=${3-} ref=${4-} branch=${5-}
	jq -nc --arg w "$what" --arg n "$next" --arg a "$as_of" --arg r "$ref" \
		--arg b "$branch" \
		'{what: $w} + (if $n == "" then {} else {next: $n} end)
		           + (if $a == "" then {} else {as_of: $a} end)
		           + (if $b == "" then {} else {branch: $b} end)
		           + (if $r == "" then {} else {refs: [$r]} end)'
}

# Every entry carries an actor, and the actor is the token's: an agent posts as
# itself and a person as themselves, exactly as a chat message does. There is no
# actor argument, so an entry cannot be put in another seat's mouth.
an_entry_carries_the_seat_that_wrote_it() {
	recall
	local args
	args="$(wl_args "wired the quibblewrench into the lexer" \
		"the continuation case is still open" "0e3b7f6" "$MEM_SHARED")" || return 1
	want_tool worklog_append "$TOKEN_A_AGENT" "$args" || return 1
	want_eq "the actor is the agent, not the person behind it" \
		"$(tv .entry.actor)" "$AGENT_A" || return 1
	want_eq "what changed" "$(tv .entry.what)" "wired the quibblewrench into the lexer" || return 1
	want_eq "what is next" "$(tv .entry.next)" "the continuation case is still open" || return 1
	want_eq "what it is true of" "$(tv .entry.as_of)" 0e3b7f6 || return 1
	want_eq "the work it is about, by id" "$(tv '.entry.refs[0]')" "$MEM_SHARED" || return 1
	want_eq "the entry is in the project" "$(tv .entry.project)" pa || return 1
	if [ "$(tv .entry.seq_hlc)" -le 0 ]; then
		printf 'the entry came back with seq_hlc %s, want a stamped clock\n' "$(tv .entry.seq_hlc)" >&2
		return 1
	fi
	local entry
	entry="$(tv .entry.id)"
	remember WORKLOG_AGENT "$entry"

	args="$(wl_args "read the handoff and picked the parser back up")" || return 1
	want_tool worklog_append "$TOKEN_A" "$args" || return 1
	want_eq "a person's entry is the person's" "$(tv .entry.actor)" "$USER_A" || return 1
	printf 'entry %s by agent %s, and one by user %s\n' \
		"$entry" "$AGENT_A" "$USER_A"
}

# The refs invariant, which is what keeps the worklog an index into the fabric
# rather than a second copy of it: an id is checked through the writer's own
# read filter before it is stored. A's personal memory is the floor, so B cannot
# reference it however many grants B holds - and the refusal is the same words
# an unreadable artifact gets everywhere else.
an_entry_cannot_reference_what_its_author_cannot_read() {
	recall
	local args
	args="$(wl_args "claiming to have worked on something of A's" "" "" "$MEM_PERSONAL")" || return 1
	want_tool_fails worklog_append "$TOKEN_B" "$args" "is not an artifact you can read" || return 1

	# And the other half: the grant pb holds on pa is exactly what makes A's
	# shared item referenceable, so this is a read-filter check and not a
	# same-project one.
	args="$(wl_args "picked up the parser handoff" "finish the continuations" "" "$MEM_SHARED")" || return 1
	want_tool worklog_append "$TOKEN_B" "$args" || return 1
	want_eq "B's entry references A's shared item" "$(tv '.entry.refs[0]')" "$MEM_SHARED" || return 1
	want_eq "and it is B's own entry" "$(tv .entry.actor)" "$USER_B" || return 1
	printf "B cannot reference A's personal item and can reference the shared one\n"
}

# An entry says what changed. Without that it is a timestamp.
an_entry_says_what_changed() {
	recall
	want_tool_fails worklog_append "$TOKEN_A" '{"next": "somebody carry on"}' \
		"what is required" || return 1
}

# The read shape: the most recent entries, newest first, which is the handoff
# read. Not a query language - an agent picking up a seat wants what happened
# lately, and search of the whole corpus is mem_search's job.
the_worklog_reads_recent_first() {
	recall
	local shift args
	for shift in "shift one" "shift two" "shift three"; do
		args="$(wl_args "$shift")" || return 1
		want_tool worklog_append "$TOKEN_A" "$args" || return 1
	done

	want_tool worklog_read "$TOKEN_A" '{"limit": 2}' || return 1
	want_eq "the limit is honoured" "$(tv .count)" 2 || return 1
	want_eq "the newest entry is first" "$(tv '.entries[0].what')" "shift three" || return 1
	want_eq "then the one before it" "$(tv '.entries[1].what')" "shift two" || return 1

	# And the whole stream, which by now holds both seats' entries - the worklog
	# is the project's and not one agent's.
	want_tool worklog_read "$TOKEN_A" '{"limit": 50}' || return 1
	want_eq "the agent's entry is in it" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.entries[] | select(.id == \"$WORKLOG_AGENT\")] | length")" 1 || return 1
	printf 'worklog_read: %s entries, newest first\n' "$(tv .count)"
}

# Entries are events, so they are on the timeline with no new UI - and they are
# read-only there. POST /api/activity would be a second door onto the stream
# that skips the refs check on the first one, so the kind is readable and not
# postable, and the node says so rather than quietly writing an entry with no
# refs checked.
entries_are_on_the_timeline_and_not_postable_onto_it() {
	recall
	api GET "$TOKEN_A" '/api/activity?kind=worklog&q=quibblewrench' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the entry is on the timeline once" \
		"$(printf '%s' "$API_BODY" | jq "[.items[] | select(.id == \"$WORKLOG_AGENT\")] | length")" 1 || return 1
	want_eq "it shows as a worklog entry" \
		"$(jqv '.items[0].kind')" worklog || return 1
	want_eq "and as the agent that wrote it" "$(jqv '.items[0].actor')" "$AGENT_A" || return 1
	want_eq "with the speaker the node stamped beside the refs" \
		"$(jqv '.items[0].actor_kind')" agent || return 1

	want_status 400 POST "$TOKEN_A" /api/activity \
		'{"kind": "worklog", "room": "general", "body": "an entry by the back door"}' || return 1
	printf 'the entry is on the timeline, and /api/activity will not post one: %s\n' \
		"$(jqv .error)"
}

# ------------------------------------------------- the worklog's other doors
#
# The worklog was MCP-ONLY, and the agents doing the work were exactly the ones
# that could not record it: a spawned VM agent is given no MCP server, by design,
# because one that could reach the spawn server would start VMs of its own. So
# the fleet's memory had two entries ever, against 311 chat messages in the same
# window. POST /api/worklog and `flowy worklog` are the doors those agents have.
#
# What these checks assert is not that the endpoint answers 200. It is that the
# doors are doors onto ONE WAY IN: the same argument list, the same refusals, in
# the same words. A second implementation of the write is a second place the
# reference check can be missing, and the reference check is what the surface is
# for.

# The refusal, through all three doors, WORD FOR WORD. This is the assertion
# rather than "each door refuses somehow": two implementations that both refuse
# can still disagree about what they refuse, and the one that is wrong is the one
# nobody read the code of. A's personal memory is the floor, so B cannot
# reference it however many grants B holds.
all_three_doors_refuse_a_ref_in_the_same_words() {
	recall
	local args
	args="$(wl_args "claiming to have worked on something of A's" "" "" "$MEM_PERSONAL")" || return 1
	want_tool_fails worklog_append "$TOKEN_B" "$args" "is not an artifact you can read" || return 1
	local over_mcp="$TOOL_ERR"

	# The HTTP door takes the same body the tool takes, on purpose: an agent that
	# has learned one has learned the other.
	want_status 400 POST "$TOKEN_B" /api/worklog "$args" || return 1
	want_eq "the HTTP door refuses it in the tool's own words" "$(jqv .error)" "$over_mcp" || return 1

	# And the CLI, which is the door a spawned agent actually has. A refusal is a
	# non-zero exit as well as a message: an entry nobody recorded must not look
	# like one that was.
	local out
	if out="$(FLOWY_TOKEN="$TOKEN_B" "$ROOT/flowy" worklog append \
		--url "http://127.0.0.1:$HTTP_PORT" --ref "$MEM_PERSONAL" \
		"claiming to have worked on something of A's" 2>&1)"; then
		printf 'flowy worklog append referenced an artifact it cannot read and exited 0:\n%s\n' \
			"$out" >&2
		return 1
	fi
	case "$out" in
	*"$over_mcp"*) ;;
	*)
		printf 'the CLI refused with %q, want the tool own words %q\n' "$out" "$over_mcp" >&2
		return 1
		;;
	esac
	printf 'three doors, one refusal: %s\n' "$over_mcp"
}

# And the write works through them, with the entry landing the same way. The CLI
# prints the id on stdout so a script can hand it on, which is `flowy say`'s
# rule: what a person reads goes to stderr and stdout stays parseable.
the_http_and_cli_doors_append_an_entry() {
	recall
	local body id
	body="$(wl_args "wrote the log from a VM with no MCP at all" \
		"read it back through the same filter" 7a1c9de "$MEM_SHARED" wl/doors)" || return 1
	api POST "$TOKEN_A" /api/worklog "$body" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the actor is the token's" "$(jqv .entry.actor)" "$USER_A" || return 1
	want_eq "the ref it may read is kept" "$(jqv '.entry.refs[0]')" "$MEM_SHARED" || return 1
	want_eq "the branch is on the entry" "$(jqv .entry.branch)" wl/doors || return 1

	id="$(FLOWY_TOKEN="$TOKEN_A" "$ROOT/flowy" worklog append \
		--url "http://127.0.0.1:$HTTP_PORT" --branch wl/doors --as-of 7a1c9de \
		--ref "$MEM_SHARED" "appended from the command line" 2>/dev/null)" || return 1
	if [ -z "$id" ]; then
		printf 'flowy worklog append printed no id on stdout\n' >&2
		return 1
	fi
	# And the read half, which is the other reason a seat with no MCP was stuck:
	# it could not read the handoff either. It goes through the timeline's filter
	# rather than a read endpoint of its own.
	local out
	out="$(FLOWY_TOKEN="$TOKEN_A" "$ROOT/flowy" worklog read \
		--url "http://127.0.0.1:$HTTP_PORT" --limit 5 2>/dev/null)" || return 1
	case "$out" in
	*"appended from the command line"*) ;;
	*)
		printf 'flowy worklog read did not show the entry just written:\n%s\n' "$out" >&2
		return 1
		;;
	esac
	printf 'appended over HTTP and over the CLI; %s reads back\n' "$id"
}

# VOUCHED IS NOT AUTHORED, and this is the check that matters.
#
# The drainer writes entries on behalf of runs: the harness knows the run id and
# the verify status and cannot lie about whether the gate passed, so it is the
# right author. But an entry written BY it ABOUT an agent must never read as the
# agent's own word - that is the impersonation shape this project has open, and
# the fix is to say which it is. So the row carries both, and the actor stays the
# token's whatever the subject says.
WORKLOG_VOUCHED_WHAT="drained the queue on this run and the gate came back clean"
readonly WORKLOG_VOUCHED_WHAT

an_entry_written_about_another_seat_says_it_is_vouched() {
	recall
	local body
	body="$(jq -nc --arg w "$WORKLOG_VOUCHED_WHAT" --arg s "$AGENT_A" \
		'{what: $w, subject: $s, run: "9f6af5dc9032", verify: "428/0", branch: "wl/vouched"}')" ||
		return 1
	api POST "$TOKEN_A" /api/worklog "$body" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the actor is the seat that WROTE it" "$(jqv .entry.actor)" "$USER_A" || return 1
	want_eq "the subject is the seat whose work it is" "$(jqv .entry.subject)" "$AGENT_A" || return 1
	want_eq "and the entry says it is vouched" "$(jqv .entry.vouched)" true || return 1
	want_eq "the run it is about" "$(jqv .entry.run)" 9f6af5dc9032 || return 1
	want_eq "and what the gate said about it" "$(jqv .entry.verify)" 428/0 || return 1
	local entry
	entry="$(jqv .entry.id)"

	# Read back through the timeline, which is where every human surface reads
	# it. The subject rides meta, inside the row signature, so a relay cannot
	# turn a vouched entry into the subject's own account.
	api GET "$TOKEN_A" '/api/activity?kind=worklog&order=recent&limit=50' || return 1
	local item
	item="$(printf '%s' "$API_BODY" | jq -c ".items[] | select(.id == \"$entry\")")" || return 1
	if [ -z "$item" ]; then
		printf 'the vouched entry %s is not on the timeline\n' "$entry" >&2
		return 1
	fi
	want_eq "the timeline carries the subject" \
		"$(printf '%s' "$item" | jq -r .meta.subject)" "$AGENT_A" || return 1
	want_eq "and still names the writer as the actor" \
		"$(printf '%s' "$item" | jq -r .actor)" "$USER_A" || return 1
	# The body says it too, because the body is what a surface that knows
	# nothing about this kind renders - the TUI's timeline, the activity view.
	case "$(printf '%s' "$item" | jq -r .body)" in
	"vouched for $AGENT_A"*) ;;
	*)
		printf 'the body of a vouched entry is %q - a surface that renders bodies cannot tell it from an authored one\n' \
			"$(printf '%s' "$item" | jq -r .body)" >&2
		return 1
		;;
	esac
	# And the CLI read says so as well, since that is one of the places somebody
	# reads the worklog.
	local out
	out="$(FLOWY_TOKEN="$TOKEN_A" "$ROOT/flowy" worklog read \
		--url "http://127.0.0.1:$HTTP_PORT" --limit 5 2>/dev/null)" || return 1
	case "$out" in
	*"VOUCHING FOR $AGENT_A"*) ;;
	*)
		printf 'flowy worklog read draws the vouched entry as its subject own account:\n%s\n' \
			"$out" >&2
		return 1
		;;
	esac
	remember WORKLOG_VOUCHED "$entry"
	printf 'entry %s: written by %s, about %s work, run 9f6af5dc9032 verify 428/0\n' \
		"$entry" "$USER_A" "$AGENT_A"
}

# Vouching for yourself is authoring. Absent and self are one state, for the same
# reason an absent addressee and an empty one are one row: a vouched badge on
# somebody's own entry teaches a reader to ignore the badge.
vouching_for_yourself_is_authoring() {
	recall
	local body
	body="$(jq -nc --arg w "my own account of my own shift" --arg s "$USER_A" \
		'{what: $w, subject: $s}')" || return 1
	api POST "$TOKEN_A" /api/worklog "$body" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "naming yourself leaves no subject" "$(jqv .entry.subject)" null || return 1
	want_eq "and the entry is not vouched" "$(jqv .entry.vouched)" null || return 1

	# And a subject nobody answers to is refused at the door, the way an
	# addressee is: an entry that reports on a seat that does not exist is
	# written, reads as a report, and no surface anywhere says the name was a typo.
	want_status 400 POST "$TOKEN_A" /api/worklog \
		'{"what": "reporting on a seat that is not here", "subject": "ag-nobody-at-all"}' || return 1
	case "$(jqv .error)" in
	*"no principal called ag-nobody-at-all"*) ;;
	*)
		printf 'a subject nobody answers to was refused with %q\n' "$(jqv .error)" >&2
		return 1
		;;
	esac
	printf 'self is authoring, and a subject nobody answers to is refused: %s\n' "$(jqv .error)"
}

# The other entrance, and the reason it is closed the way it is.
#
# A worklog entry is NOT a minted type - it has to replicate, and a minted one
# does not - so POST /api/events writes an event of that type and must keep
# doing so. What it may not do is STAMP the entry's claims: refs is checked
# against the writer's own read filter and subject against the principals that
# exist here, so a client handing either in through the generic door would be
# making the claim without the check. speakerStripped drops them, exactly as it
# drops the actor keys and a citation.
the_generic_event_door_cannot_stamp_a_worklogs_claims() {
	recall
	local body
	body="$(jq -nc --arg r "$MEM_PERSONAL" --arg s "$AGENT_A" \
		'{type: "worklog", room: "worklog", body: "an entry by the side door",
		  meta: {what: "an entry by the side door", branch: "wl/side",
		         refs: [$r], subject: $s, run: "r-forged", verify: "428/0"}}')" || return 1
	api POST "$TOKEN_B" /api/events "$body" || return 1
	want_eq "the event is still written, because a worklog entry has to replicate" \
		"$API_STATUS" 200 || return 1
	for key in refs subject run verify; do
		want_eq "$key handed in through the generic door is dropped" \
			"$(jqv ".meta.$key")" null || return 1
	done
	# What an entry says about its own shift is still a client's to write: it
	# claims nothing about anybody else, and the body beside it was always theirs.
	want_eq "what it says about its own shift survives" "$(jqv .meta.what)" \
		"an entry by the side door" || return 1
	want_eq "and so does the branch" "$(jqv .meta.branch)" wl/side || return 1
	printf 'the generic door writes the event and stamps none of its claims\n'
}

# ------------------------------------------------------------- proposals
#
# A proposal is an artifact and a vote is an event, so what these checks assert
# is not storage. It is the four claims the surface exists to make, over the
# wire, with two principals:
#
#   - a vote from somebody who cannot read the proposal is refused, in the same
#     words a read of it would be. Voting is not a way to find out that
#     something exists, and consent from somebody who cannot see what they are
#     agreeing to is not consent.
#   - changing your mind APPENDS. The old vote is still in the log afterwards
#     with the reason it was cast for, which is the whole point: an
#     implementation that overwrote would pass every tally check ever written
#     and destroy the record.
#   - the tally is one vote per principal, not one per event, and it says how
#     many entries are behind it so the two can be seen to be different numbers.
#   - a closed proposal takes no more votes, and the refusal says when.

# A proposal is raised in a room, and the room is a filter on it exactly as it
# is on a todo: it narrows the panel and nothing else.
a_proposal_is_raised_in_a_room() {
	recall
	want_tool proposal_write "$TOKEN_A" \
		'{"title": "move the gate to the wired interface",
		  "body": "loopback 8787 is no longer served",
		  "room": "general"}' || return 1
	want_eq "it is a proposal" "$(tv .item.type)" proposal || return 1
	want_eq "born open" "$(tv .item.status)" open || return 1
	want_eq "raised in general" "$(tv .item.fields.room)" general || return 1
	want_eq "in the project" "$(tv .item.project)" pa || return 1

	local raised
	raised="$(tv .item.id)"
	remember PROPOSAL "$raised"

	want_tool proposal_list "$TOKEN_A" '{"room": "general", "status": "open"}' || return 1
	want_eq "the room's open proposals hold it" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.id == \"$raised\")] | length")" 1 || return 1
	want_tool proposal_list "$TOKEN_A" '{"room": "build"}' || return 1
	want_eq "another room's do not" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.id == \"$raised\")] | length")" 0 || return 1
	printf 'proposal %s, open, in general\n' "$raised"
}

# The floor. A proposal written at scope=project is the project's and nobody
# else's - pb holds a read grant on pa and it does not reach this - so B is
# refused, and refused as an id that is not there rather than as one they may
# not have.
a_vote_from_somebody_who_cannot_read_the_proposal_is_refused() {
	recall
	local args
	args="$(jq -nc --arg p "$PROPOSAL" '{proposal: $p, choice: "for", reason: "sounds fine"}')" || return 1
	want_tool_fails vote "$TOKEN_B" "$args" "no such proposal" || return 1
	args="$(jq -nc --arg p "$PROPOSAL" '{id: $p}')" || return 1
	want_tool_fails proposal_read "$TOKEN_B" "$args" "no such proposal" || return 1

	# And nothing landed: the owner sees every vote on their own proposal, so
	# an empty log here is the whole log.
	args="$(jq -nc --arg p "$PROPOSAL" '{id: $p}')" || return 1
	want_tool proposal_read "$TOKEN_A" "$args" || return 1
	want_eq "the refused vote is not in the log" "$(tv '.votes | length')" 0 || return 1
	want_eq "and not in the tally" "$(tv .tally.voters)" 0 || return 1
}

# The discriminating check. Two principals vote, one of them twice, and what is
# asserted afterwards is the LOG: both of A's votes are in it, in the order they
# were cast, with the first one's reason intact. Only then the tally.
changing_a_vote_appends_and_the_tally_follows_the_latest() {
	recall
	local args
	args="$(jq -nc --arg p "$PROPOSAL" \
		'{proposal: $p, choice: "for", reason: "the LAN bind is the point"}')" || return 1
	want_tool vote "$TOKEN_A" "$args" || return 1
	want_eq "the vote is the person's" "$(tv .vote.actor)" "$USER_A" || return 1
	local first
	first="$(tv .vote.id)"

	args="$(jq -nc --arg p "$PROPOSAL" '{proposal: $p, choice: "abstain"}')" || return 1
	want_tool vote "$TOKEN_A_AGENT" "$args" || return 1
	want_eq "an agent votes as itself, not as the person behind it" \
		"$(tv .vote.actor)" "$AGENT_A" || return 1

	# A changes their mind.
	args="$(jq -nc --arg p "$PROPOSAL" \
		'{proposal: $p, choice: "against", reason: "the unit owns the port"}')" || return 1
	want_tool vote "$TOKEN_A" "$args" || return 1
	local second
	second="$(tv .vote.id)"
	if [ "$first" = "$second" ]; then
		printf 'the second vote reused the first row (%s), so the first is gone\n' "$first" >&2
		return 1
	fi

	args="$(jq -nc --arg p "$PROPOSAL" '{id: $p}')" || return 1
	want_tool proposal_read "$TOKEN_A" "$args" || return 1
	want_eq "all three entries are in the log" "$(tv '.votes | length')" 3 || return 1
	want_eq "the changed vote is still there, as it was cast" \
		"$(tv '.votes[0].choice')" for || return 1
	want_eq "with the reason it was cast for" \
		"$(tv '.votes[0].reason')" "the LAN bind is the point" || return 1
	want_eq "and its own id" "$(tv '.votes[0].id')" "$first" || return 1
	want_eq "the latest is last" "$(tv '.votes[2].id')" "$second" || return 1

	# The tally: one vote per principal, and the number of entries behind it.
	want_eq "the latest vote counts" "$(tv .tally.against)" 1 || return 1
	want_eq "and the one it replaced does not" "$(tv .tally.for)" 0 || return 1
	want_eq "the agent's abstention" "$(tv .tally.abstain)" 1 || return 1
	want_eq "two principals answered" "$(tv .tally.voters)" 2 || return 1
	want_eq "behind three entries" "$(tv .tally.votes)" 3 || return 1
	printf 'votes %s then %s by %s, and one by %s: 2 voters, 3 entries\n' \
		"$first" "$second" "$USER_A" "$AGENT_A"
}

# Closing is manual, it records an outcome, and it is a line under the decision:
# what comes after it is refused, and the refusal says when it closed. Nothing
# in here counts the votes and decides - a rule that did would be a governance
# system nobody agreed to.
a_closed_proposal_refuses_further_votes_and_says_when() {
	recall
	local args
	args="$(jq -nc --arg p "$PROPOSAL" \
		'{id: $p, outcome: "agreed: serve takes one listen address, and it is the LAN one"}')" || return 1
	want_tool proposal_write "$TOKEN_A" "$args" || return 1
	want_eq "it is closed" "$(tv .item.status)" closed || return 1
	local at
	at="$(tv .item.fields.closed_at)"
	if [ -z "$at" ] || [ "$at" = null ]; then
		printf 'the closure recorded no moment, so no refusal can name one\n' >&2
		return 1
	fi
	remember PROPOSAL_CLOSED_AT "$at"

	args="$(jq -nc --arg p "$PROPOSAL" '{proposal: $p, choice: "against", reason: "I have thoughts"}')" || return 1
	want_tool_fails vote "$TOKEN_A_AGENT" "$args" "$at" || return 1
	case "$TOOL_ERR" in
	*"serve takes one listen address"*) ;;
	*)
		printf 'the refusal does not say what was decided: %s\n' "$TOOL_ERR" >&2
		return 1
		;;
	esac

	# It closed once. A second outcome over the first would rewrite the record
	# rather than add to it, and so would editing what people voted on.
	args="$(jq -nc --arg p "$PROPOSAL" '{id: $p, outcome: "actually, no"}')" || return 1
	want_tool_fails proposal_write "$TOKEN_A" "$args" "$at" || return 1
	args="$(jq -nc --arg p "$PROPOSAL" '{id: $p, title: "something else entirely"}')" || return 1
	want_tool_fails proposal_write "$TOKEN_A" "$args" "is a record now" || return 1

	# And the votes cast before the close are untouched.
	args="$(jq -nc --arg p "$PROPOSAL" '{id: $p}')" || return 1
	want_tool proposal_read "$TOKEN_A" "$args" || return 1
	want_eq "the log still holds three entries" "$(tv '.votes | length')" 3 || return 1
	want_eq "and the tally two voters" "$(tv .tally.voters)" 2 || return 1
}

# The read path the console will use. No view is drawn in this change - the room
# panel is somebody else's edit - so what is checked is the data: the proposal,
# its votes and its tally over HTTP, under the same filter the tools keep.
the_proposal_reads_back_over_http() {
	recall
	api GET "$TOKEN_A" "/api/proposal/$PROPOSAL" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the proposal" "$(jqv .item.id)" "$PROPOSAL" || return 1
	want_eq "closed" "$(jqv .closed)" true || return 1
	want_eq "when" "$(jqv .closed_at)" "$PROPOSAL_CLOSED_AT" || return 1
	want_eq "every vote, in order" "$(jqv '.votes | length')" 3 || return 1
	want_eq "the first one as it was cast" "$(jqv '.votes[0].choice')" for || return 1
	want_eq "two voters" "$(jqv .tally.voters)" 2 || return 1

	api GET "$TOKEN_A" '/api/proposals?room=general&status=closed' || return 1
	want_eq "the room's closed proposals hold it" \
		"$(printf '%s' "$API_BODY" | jq "[.items[] | select(.id == \"$PROPOSAL\")] | length")" 1 || return 1

	# The filter is the same one, so B gets the answer B gets everywhere else.
	want_status 404 GET "$TOKEN_B" "/api/proposal/$PROPOSAL" || return 1
	printf 'the console reads the proposal, its three votes and its tally; B gets a 404\n'
}

# A vote is minted by the verb that does it. Both refusals that make the record
# worth reading are on that verb, so an event a client could write by hand would
# be a vote cast an hour after the decision was recorded, counted.
a_vote_cannot_be_written_by_hand() {
	recall
	want_status 403 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$PROPOSAL" \
			'{type: "proposal.vote", artifact: $a, room: "general",
			  body: "voted for", meta: {choice: "for"}}')" || return 1
	want_status 403 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$PROPOSAL" '{type: "proposal.close", artifact: $a, body: "closed"}')" || return 1
	printf 'a hand-written vote: %s\n' "$(jqv .error)"
}

# ------------------------------------------------ DEPENDS-ON, and the ready query
#
# The queue is drained by machines - something reads it and starts a VM per item
# that can be started - so what these checks assert is not storage. It is the one
# property that decides whether that is safe:
#
#   A BLOCKER THE READER CANNOT SEE HOLDS ITS TODO, DONE OR NOT.
#
# It is driven with two principals over the wire, because it is only true if it
# is true for a second token: B reads a todo B is carrying, cannot read what
# blocks it, and must not be told it can start. The wrong version - skip the ids
# you cannot resolve - reads as ready, passes every same-project test, and is a
# machine starting work whose dependency is not done.
#
# The rest is what makes the record worth keeping: the edge is an EVENT, so
# taking it back appends and the old edge is still in the log with the seat that
# wrote it; a cycle is refused where the writer can see it and never becomes
# ready where they cannot; and neither verb can be written by hand.

# ready_row ID TOKEN - the readiness row for one todo out of the ready tool's
# answer, empty when this principal's queue does not hold it at all.
ready_row() {
	want_tool ready "$2" '{}' || return 1
	READY_ROW="$(printf '%s' "$TOOL_JSON" | jq -c "first(.items[] | select(.item.id == \"$1\")) // empty")"
	if [ -z "$READY_ROW" ]; then
		printf 'the queue does not hold %s at all\n' "$1" >&2
		return 1
	fi
}

# readyv EXPR - a value out of the last ready_row. Not rv, which is the MCP
# checks' reader of MCP_BODY: two functions of one name is one function.
readyv() { printf '%s' "$READY_ROW" | jq -r "$1"; }

# Two todos in pa: one B can read and is carrying, one B cannot. A can read both,
# which is what lets A say that one blocks the other - a cross-project edge is
# written by somebody who sees both ends, and read later by somebody who does not.
the_queue_gets_a_todo_and_a_blocker_b_cannot_see() {
	recall
	local args
	args="$(jq -nc --arg b "$USER_B" '{
		title: "drain the queue by dependency order",
		body: "one VM per ready todo",
		scope: "shared", kind: "todo", room: "queue", assignee: $b}')" || return 1
	want_tool mem_write "$TOKEN_A" "$args" || return 1
	local blocked
	blocked="$(tv .item.id)"
	remember DEP_BLOCKED "$blocked"

	want_tool mem_write "$TOKEN_A" '{
		"title": "agree the shape of the edge",
		"body": "an edge is an event, not a field",
		"scope": "project", "kind": "todo", "room": "queue", "assignee": "a-orchestrator"
	}' || return 1
	local blocker
	blocker="$(tv .item.id)"
	remember DEP_BLOCKER "$blocker"

	# The fixture is only a fixture if B really cannot see the second one.
	want_tool_fails mem_read "$TOKEN_B" "{\"id\": \"$blocker\"}" "no such memory item" || return 1
	want_tool mem_read "$TOKEN_B" "{\"id\": \"$blocked\"}" || return 1

	# Carried and unblocked, so B can start it. This is the "before" the next
	# check is a comparison against.
	ready_row "$blocked" "$TOKEN_B" || return 1
	want_eq "B can start it before anything blocks it" "$(readyv .ready)" true || return 1
	printf 'blocked=%s (shared, B carries it), blocker=%s (pa only)\n' "$blocked" "$blocker"
}

# An edge is an event naming both todos, with the seat that wrote it and the
# moment it was written. A field would have recorded THAT something changed and
# not WHAT, which is the whole reason this is not a column.
an_edge_is_an_event_naming_both_todos() {
	recall
	local args
	args="$(jq -nc --arg t "$DEP_BLOCKED" --arg b "$DEP_BLOCKER" '{todo: $t, blocker: $b}')" || return 1
	want_tool dep_add "$TOKEN_A" "$args" || return 1
	want_eq "the entry is an add" "$(tv .entry.type)" dep.add || return 1
	want_eq "naming the blocked todo" "$(tv .entry.todo)" "$DEP_BLOCKED" || return 1
	want_eq "and the one it waits on" "$(tv .entry.blocker)" "$DEP_BLOCKER" || return 1
	want_eq "written by the person" "$(tv .entry.actor)" "$USER_A" || return 1
	remember DEP_ADDED "$(tv .entry.id)"

	# A sees both ends, so A is told what it is waiting on and that it is not done.
	want_eq "A resolves the blocker" "$(tv '.deps.blockers[0].known')" true || return 1
	want_eq "and it is not finished" "$(tv '.deps.blockers[0].done')" false || return 1
	want_eq "so A cannot start it either" "$(tv .deps.ready)" false || return 1
	printf 'edge %s: %s depends on %s\n' "$(tv .entry.id)" "$DEP_BLOCKED" "$DEP_BLOCKER"
}

# THE CHECK THIS WHOLE SURFACE EXISTS FOR.
#
# B reads the todo and the edge - the edge hangs off the BLOCKED todo, so it
# reaches exactly that todo's readers - and cannot resolve the other end. Not
# ready. Then the blocker is finished, B still cannot see that it was, and it is
# STILL not ready: a reader who cannot read a blocker cannot confirm it is
# finished, whether or not it is.
#
# The last third is the other half of "per reader": A gets the opposite answer at
# the same moment, off the same rows, and both are right.
a_blocker_b_cannot_see_holds_the_todo_done_or_not() {
	recall
	ready_row "$DEP_BLOCKED" "$TOKEN_B" || return 1
	want_eq "B sees one thing in the way" "$(readyv '.blockers | length')" 1 || return 1
	want_eq "and it is the blocker" "$(readyv '.blockers[0].id')" "$DEP_BLOCKER" || return 1
	want_eq "which B cannot read" "$(readyv '.blockers[0].known')" false || return 1
	want_eq "so B cannot confirm it is done" "$(readyv '.blockers[0].done')" false || return 1
	want_eq "and B must not start it" "$(readyv .ready)" false || return 1

	# Finished, by the only principal who can read it.
	want_tool mem_write "$TOKEN_A" "{\"id\": \"$DEP_BLOCKER\", \"status\": \"done\"}" || return 1
	want_eq "the blocker is done" "$(tv .item.status)" "done" || return 1
	want_tool_fails mem_read "$TOKEN_B" "{\"id\": \"$DEP_BLOCKER\"}" "no such memory item" || return 1

	ready_row "$DEP_BLOCKED" "$TOKEN_B" || return 1
	want_eq "B still cannot read it" "$(readyv '.blockers[0].known')" false || return 1
	want_eq "and is not told it is finished" "$(readyv '.blockers[0].done')" false || return 1
	want_eq "so it is STILL not ready for B" "$(readyv .ready)" false || return 1

	# And for A, who can see it finish, it is.
	ready_row "$DEP_BLOCKED" "$TOKEN_A" || return 1
	want_eq "A resolves the blocker" "$(readyv '.blockers[0].known')" true || return 1
	want_eq "as done" "$(readyv '.blockers[0].done')" true || return 1
	want_eq "so A can start it" "$(readyv .ready)" true || return 1
	printf 'same todo, same moment: ready for A, held for B by %s\n' "$DEP_BLOCKER"
}

# Ready is two conditions and neither is enough alone. Dropping the second looks
# like a queue property rather than a dependency one, and it is how a drainer
# picks up work nobody has claimed - which is the collision this was built after.
deps_done_and_assigned_is_ready_and_either_alone_is_not() {
	recall
	want_tool mem_write "$TOKEN_A" '{
		"title": "unblocked, and nobody is carrying it",
		"scope": "project", "kind": "todo", "room": "queue", "assignee": ""
	}' || return 1
	local unowned
	unowned="$(tv .item.id)"

	ready_row "$unowned" "$TOKEN_A" || return 1
	want_eq "nothing is in the way" "$(readyv '.blockers | length')" 0 || return 1
	want_eq "but nobody is carrying it" "$(readyv .assignee)" "" || return 1
	want_eq "so it is not ready" "$(readyv .ready)" false || return 1

	want_tool mem_write "$TOKEN_A" "$(jq -nc --arg i "$unowned" '{id: $i, assignee: "a-bench"}')" || return 1
	ready_row "$unowned" "$TOKEN_A" || return 1
	want_eq "somebody picked it up, so now it is" "$(readyv .ready)" true || return 1

	# ready=true narrows to what a drainer would start, and the full answer says
	# how many of them there were - a queue that has stopped is not a queue with
	# nothing to do.
	want_tool ready "$TOKEN_A" '{"ready": true}' || return 1
	want_eq "the narrowed answer holds it" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.item.id == \"$unowned\")] | length")" 1 || return 1
	if [ "$(printf '%s' "$TOOL_JSON" | jq '[.items[] | select(.ready == false)] | length')" != 0 ]; then
		printf 'ready=true returned an item that is not ready\n' >&2
		return 1
	fi
	printf 'unowned and unblocked is not ready; carried and unblocked is\n'
}

# The removal appends. The old edge is still in the log afterwards, with the seat
# that wrote it and the seat that took it back - which is the question a field
# destroys: not "what blocks this now" but "who said it did, and when".
removing_a_dep_unblocks_and_the_old_edge_is_still_in_the_log() {
	recall
	local args
	args="$(jq -nc --arg t "$DEP_BLOCKED" --arg b "$DEP_BLOCKER" '{todo: $t, blocker: $b}')" || return 1
	# The agent takes it back, so the two entries are two different seats.
	want_tool dep_remove "$TOKEN_A_AGENT" "$args" || return 1
	want_eq "the entry is a removal" "$(tv .entry.type)" dep.remove || return 1
	want_eq "written by the agent" "$(tv .entry.actor)" "$AGENT_A" || return 1
	want_eq "nothing is in the way now" "$(tv '.deps.blockers | length')" 0 || return 1
	want_eq "so it is ready again" "$(tv .deps.ready)" true || return 1

	want_tool dep_list "$TOKEN_A" "$(jq -nc --arg t "$DEP_BLOCKED" '{todo: $t}')" || return 1
	want_eq "both entries are in the log" "$(tv '.log | length')" 2 || return 1
	want_eq "the edge that was taken back is still there" "$(tv '.log[0].id')" "$DEP_ADDED" || return 1
	want_eq "as it was written" "$(tv '.log[0].type')" dep.add || return 1
	want_eq "with both of its ends" "$(tv '.log[0].blocker')" "$DEP_BLOCKER" || return 1
	want_eq "and the seat that wrote it" "$(tv '.log[0].actor')" "$USER_A" || return 1
	want_eq "the removal is the later one" "$(tv '.log[1].type')" dep.remove || return 1

	# Said once: an add of a live edge and a removal of one that is not are both
	# refused, so every entry in the log is a real transition.
	want_tool_fails dep_remove "$TOKEN_A" "$args" "does not depend on" || return 1
	want_tool dep_add "$TOKEN_A" "$args" || return 1
	want_tool_fails dep_add "$TOKEN_A" "$args" "already depends on" || return 1
	printf 'add, remove, add: 3 entries, and the first is still readable\n'
}

# A cycle is refused where the writer can see it, and the refusal names the way
# round the loop already goes. A queue that deadlocks silently is worse than one
# that says so. A todo depending on itself is refused for the same reason: it is
# an edge that can never be satisfied.
a_cycle_and_a_self_edge_are_refused() {
	recall
	want_tool mem_write "$TOKEN_A" '{"title": "cycle: one", "scope": "project", "kind": "todo"}' || return 1
	local one
	one="$(tv .item.id)"
	want_tool mem_write "$TOKEN_A" '{"title": "cycle: two", "scope": "project", "kind": "todo"}' || return 1
	local two
	two="$(tv .item.id)"
	want_tool mem_write "$TOKEN_A" '{"title": "cycle: three", "scope": "project", "kind": "todo"}' || return 1
	local three
	three="$(tv .item.id)"

	local args
	args="$(jq -nc --arg t "$three" --arg b "$two" '{todo: $t, blocker: $b}')" || return 1
	want_tool dep_add "$TOKEN_A" "$args" || return 1
	args="$(jq -nc --arg t "$two" --arg b "$one" '{todo: $t, blocker: $b}')" || return 1
	want_tool dep_add "$TOKEN_A" "$args" || return 1

	# Two hops away, so a check that only looked at the direct edge lets it through.
	args="$(jq -nc --arg t "$one" --arg b "$three" '{todo: $t, blocker: $b}')" || return 1
	want_tool_fails dep_add "$TOKEN_A" "$args" "would close a cycle" || return 1
	case "$TOOL_ERR" in
	*"$two"*) ;;
	*)
		printf 'the refusal does not say where the loop goes: %s\n' "$TOOL_ERR" >&2
		return 1
		;;
	esac

	# And it was a refusal: nothing landed.
	want_tool dep_list "$TOKEN_A" "$(jq -nc --arg t "$one" '{todo: $t}')" || return 1
	want_eq "the refused edge is not in the log" "$(tv '.log | length')" 0 || return 1

	args="$(jq -nc --arg t "$one" '{todo: $t, blocker: $t}')" || return 1
	want_tool_fails dep_add "$TOKEN_A" "$args" "cannot depend on itself" || return 1

	# An id out of reach is the answer a read of it would give, and nothing more:
	# naming an id in an edge is not a way to find out what else it might be.
	args="$(jq -nc --arg t "$one" --arg b "$DEP_BLOCKED" '{todo: $t, blocker: $b}')" || return 1
	want_tool_fails dep_add "$TOKEN_B" "$args" "no such todo" || return 1
	printf 'the cycle, the self-edge and the id B cannot read are all refused\n'
}

# The read and write paths a drainer uses, over HTTP, under the same filter the
# tools keep. No console view in this change - the room panel is somebody else's
# edit - so what is checked is the data.
the_queue_reads_and_writes_over_http() {
	recall
	api GET "$TOKEN_A" "/api/todo/$DEP_BLOCKED/deps" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the todo" "$(jqv .item.id)" "$DEP_BLOCKED" || return 1
	want_eq "its live edges" "$(jqv '.blockers | length')" 1 || return 1
	want_eq "and the whole log behind them" "$(jqv '.log | length')" 3 || return 1

	# The same rows, and the opposite answer, for the other reader.
	api GET "$TOKEN_B" "/api/todo/$DEP_BLOCKED/deps" || return 1
	want_eq "B reads the edge" "$(jqv '.blockers | length')" 1 || return 1
	want_eq "and cannot resolve it" "$(jqv '.blockers[0].known')" false || return 1
	want_eq "so B is not told to start it" "$(jqv .ready)" false || return 1
	want_status 404 GET "$TOKEN_B" "/api/todo/$DEP_BLOCKER/deps" || return 1

	api GET "$TOKEN_A" '/api/ready?room=queue' || return 1
	want_eq "the room's queue holds it" \
		"$(printf '%s' "$API_BODY" | jq "[.items[] | select(.item.id == \"$DEP_BLOCKED\")] | length")" 1 || return 1
	api GET "$TOKEN_A" '/api/ready?room=nothing-was-raised-here' || return 1
	want_eq "another room's does not" "$(jqv .count)" 0 || return 1

	# The write half: an edge removed over HTTP, and the log longer for it.
	api DELETE "$TOKEN_A" "/api/todo/$DEP_BLOCKED/deps/$DEP_BLOCKER" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "nothing in the way" "$(jqv '.deps.blockers | length')" 0 || return 1
	want_eq "and four entries behind it" "$(jqv '.deps.log | length')" 4 || return 1
	api POST "$TOKEN_A" "/api/todo/$DEP_BLOCKED/deps" \
		"$(jq -nc --arg b "$DEP_BLOCKER" '{blocker: $b}')" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "back in the way" "$(jqv '.deps.blockers | length')" 1 || return 1

	# A refusal about the edge is the caller's mistake and says what it was.
	want_status 400 POST "$TOKEN_A" "/api/todo/$DEP_BLOCKED/deps" \
		"$(jq -nc --arg b "$DEP_BLOCKED" '{blocker: $b}')" || return 1
	printf 'the self-edge over HTTP: %s\n' "$(jqv .error)"
}

# Both verbs are minted. Every refusal that makes the graph safe to drain is on
# them - both ends readable, both ends queue items, no self-edge, no cycle - so
# an edge written by hand is an edge with none of them asked, read by a machine
# deciding whether to start work.
an_edge_cannot_be_written_by_hand() {
	recall
	want_status 403 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$DEP_BLOCKED" --arg b "$DEP_BLOCKER" \
			'{type: "dep.add", artifact: $a, room: "queue", body: "depends on it",
			  meta: {blocker: $b}}')" || return 1
	want_status 403 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$DEP_BLOCKED" '{type: "dep.remove", artifact: $a, body: "no longer"}')" || return 1
	printf 'a hand-written edge: %s\n' "$(jqv .error)"
}

# ----------------------------------------------------------------- attachments
#
# An attachment is an artifact with bytes, and every one of these checks is
# about the bytes: the artifact half rides the same store, the same filter and
# the same write as a memory item, and is already asserted above.
#
# They are driven over the wire like the rest of the MCP checks, and for a
# sharper reason here than elsewhere. What is being claimed is that a payload
# survives a JSON-RPC envelope, a base64 hop, a bytea column and the trip back -
# so a check that called the handler in-process would be asserting the one part
# of that path nobody doubted.

# att_fixture FILE - the payload, written to a file because a bash variable
# cannot hold it: a NUL byte terminates a C string, so a fixture typed as a
# shell string is silently a shorter fixture. That is the same class of bug
# these checks exist to catch, one layer down.
#
# A newline, a NUL, and two bytes that are not valid UTF-8. Anything ASCII would
# round-trip through a text-only path that mangles binary.
att_fixture() {
	printf 'BUILD log\n\000panic: nil\n\377\376\000\r\nend' >"$1"
}

# att_args FILE TITLE [CLAIM] - the arguments of one attachment_write, built as
# a file. The content is megabytes at its ceiling and execve caps a single
# argument at 128 KiB, so nothing here passes a payload as an argument to
# anything: jq reads it with --rawfile and curl posts the request from disk.
att_args() {
	local payload=$1 title=$2 claim=${3-} b64="$WORK/att-b64"
	base64 -w0 "$payload" >"$b64" || return 1
	jq -n --arg t "$title" --arg c "$claim" --rawfile b "$b64" \
		'{title: $t, content_base64: ($b | rtrimstr("\n"))}
		 + (if $c == "" then {} else {content_type: $c} end)' >"$WORK/att-args.json"
}

# tool_file NAME TOKEN ARGS_FILE - one tools/call whose arguments are on disk.
# Same three outputs as `tool`, so the assertions read the same.
tool_file() {
	local name=$1 token=$2 file=$3
	jq -n --arg n "$name" --slurpfile a "$file" \
		'{jsonrpc: "2.0", id: 1, method: "tools/call",
		  params: {name: $n, arguments: $a[0]}}' >"$WORK/att-req.json" || return 1
	MCP_BODY="$(curl --silent --show-error -X POST -H 'Content-Type: application/json' \
		-H "Authorization: Bearer $token" --data-binary "@$WORK/att-req.json" \
		"http://127.0.0.1:$MCP_PORT/mcp")" || return 1
	TOOL_ERR="$(printf '%s' "$MCP_BODY" |
		jq -r '.error.message // (if .result.isError then .result.content[0].text else "" end)')"
	TOOL_JSON="$(printf '%s' "$MCP_BODY" |
		jq -r 'if .error or .result.isError then "null" else .result.content[0].text end')"
}

# The claim the surface rests on: what came out is what went in, compared byte
# for byte with cmp rather than by eye or by length.
an_attachment_round_trips_byte_for_byte() {
	recall
	local in="$WORK/att-in" out="$WORK/att-out"
	att_fixture "$in"
	att_args "$in" "the build log that panicked" || return 1

	tool_file attachment_write "$TOKEN_A" "$WORK/att-args.json" || return 1
	if [ -n "$TOOL_ERR" ]; then
		printf 'attachment_write failed: %s\n' "$TOOL_ERR" >&2
		return 1
	fi
	want_eq "type" "$(tv .item.type)" attachment || return 1
	want_eq "scope defaults to project" "$(tv .item.visibility)" project-only || return 1
	want_eq "project" "$(tv .item.project)" pa || return 1
	want_eq "owner" "$(tv .item.owner_user)" "$USER_A" || return 1
	want_eq "the size it recorded" "$(tv .size)" "$(wc -c <"$in")" || return 1
	want_eq "the digest it recorded" "$(tv .sha256)" "$(sha256sum <"$in" | cut -d' ' -f1)" || return 1

	local id
	id="$(tv .item.id)"
	remember ATTACHMENT "$id"

	# The bytes are not in the artifact row: body is the prose, and a text
	# column could not have held that NUL anyway.
	want_eq "the row carries no payload in its body" "$(tv .item.body)" "" || return 1

	want_tool attachment_read "$TOKEN_A" "{\"id\": \"$id\"}" || return 1
	tv .content_base64 | base64 -d >"$out" || return 1
	if ! cmp -s "$in" "$out"; then
		printf 'the bytes did not survive the round trip:\n' >&2
		cmp -l "$in" "$out" | head -5 >&2
		return 1
	fi
	want_eq "the size it read back" "$(tv .size)" "$(wc -c <"$in")" || return 1
	printf 'attachment %s: %s bytes back, identical (NUL and newline included)\n' \
		"$id" "$(wc -c <"$out")"
}

# The ceiling, and the number in the refusal. Over it is refused whole: an
# upload that came back as a shorter attachment with nothing said would be
# somebody debugging against half a log without knowing it.
an_attachment_over_the_ceiling_is_refused_with_the_number() {
	recall
	local big="$WORK/att-big"
	head -c 4194305 /dev/urandom >"$big" || return 1
	att_args "$big" "one byte over" || return 1

	tool_file attachment_write "$TOKEN_A" "$WORK/att-args.json" || return 1
	if [ -z "$TOOL_ERR" ]; then
		printf 'an attachment of %s bytes was accepted\n' "$(wc -c <"$big")" >&2
		return 1
	fi
	# Kept, because the list below is another call and TOOL_ERR is the last
	# call's.
	local refusal=$TOOL_ERR want
	for want in 4194305 4194304 truncat; do
		case "$refusal" in
		*"$want"*) ;;
		*)
			printf 'the refusal is %q and never says %q\n' "$refusal" "$want" >&2
			return 1
			;;
		esac
	done

	# And nothing landed: the refusal is not a half write with a message.
	want_tool attachment_list "$TOKEN_A" '{"limit": 200}' || return 1
	want_eq "nothing was stored under the refused title" \
		"$(printf '%s' "$TOOL_JSON" | jq '[.items[] | select(.title == "one byte over")] | length')" 0 || return 1
	printf 'over the ceiling: %s\n' "$refusal"
}

# Empty is not legal, and the refusal says which of the two things went wrong.
an_empty_attachment_is_refused() {
	recall
	want_tool_fails attachment_write "$TOKEN_A" \
		'{"title": "nothing at all", "content_base64": ""}' "no bytes" || return 1
	want_tool_fails attachment_write "$TOKEN_A" \
		'{"title": "not encoded", "content_base64": "panic: nil pointer !!!"}' "base64" || return 1
}

# The permission filter, on the new read path. B holds the pb -> pa grant the
# memory checks issued and A's attachment is at scope=project, which is the
# floor a grant does not reach - so this asserts what B GETS, by id and in the
# list, rather than that some code called some filter.
b_cannot_read_or_list_as_attachment() {
	recall
	want_tool_fails attachment_read "$TOKEN_B" "{\"id\": \"$ATTACHMENT\"}" \
		"no such attachment" || return 1
	want_tool_fails attachment_read "$TOKEN_B_AGENT" "{\"id\": \"$ATTACHMENT\"}" \
		"no such attachment" || return 1

	want_tool attachment_list "$TOKEN_B" '{"limit": 200}' || return 1
	want_eq "B's list holds A's attachment" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.id == \"$ATTACHMENT\")] | length")" 0 || return 1

	# The positive control, so the refusal above is about the principal and not
	# about a surface that refuses everybody: A's own agent reads the bytes.
	want_tool attachment_read "$TOKEN_A_AGENT" "{\"id\": \"$ATTACHMENT\"}" || return 1
	want_eq "A's agent gets the same digest" \
		"$(tv .sha256)" "$(sha256sum <"$WORK/att-in" | cut -d' ' -f1)" || return 1

	# One namespace: a memory item's id is not an attachment, and says so in
	# the words an id that is not there gets.
	want_tool_fails attachment_read "$TOKEN_A" "{\"id\": \"$MEM_SHARED\"}" \
		"no such attachment" || return 1
	printf "B is told A's attachment does not exist, and A's agent reads it\n"
}

# What a reader renders from is decided from the bytes. The claim is recorded
# beside it, under a name that says it is a claim: a console that drew whatever
# a client asserted would be an injection surface, and the way that happens is a
# field called content_type holding somebody else's word for it.
the_content_type_is_not_the_clients_to_decide() {
	recall
	local lie="$WORK/att-lie"
	printf '<html><body><script>alert(1)</script></body></html>' >"$lie"
	att_args "$lie" "a screenshot, allegedly" "image/png" || return 1

	tool_file attachment_write "$TOKEN_A" "$WORK/att-args.json" || return 1
	if [ -n "$TOOL_ERR" ]; then
		printf 'attachment_write failed: %s\n' "$TOOL_ERR" >&2
		return 1
	fi
	want_eq "the claim is recorded as a claim" \
		"$(tv '.item.fields.claimed_type')" image/png || return 1
	case "$(tv '.item.fields.content_type')" in
	text/*) ;;
	*)
		printf 'the bytes are markup and the node calls them %q\n' \
			"$(tv '.item.fields.content_type')" >&2
		return 1
		;;
	esac
	want_eq "the kind follows the bytes" "$(tv .item.kind)" text || return 1

	local id
	id="$(tv .item.id)"
	want_tool attachment_read "$TOKEN_A" "{\"id\": \"$id\"}" || return 1
	case "$(tv .content_type)" in
	text/*) ;;
	*)
		printf 'the read says the payload is %q\n' "$(tv .content_type)" >&2
		return 1
		;;
	esac
	want_eq "and still reports what was claimed" \
		"$(tv '.item.fields.claimed_type')" image/png || return 1
	printf 'claimed %s, is %s\n' "$(tv '.item.fields.claimed_type')" "$(tv .content_type)"
}

# An entry carries the branch or worktree the shift worked in, when it worked in
# one. Several seats run at once on separate branches here, so "which branch was
# this" is the second thing the next seat asks after "which seat wrote it" - and
# a reader who cannot tell two branches apart cannot narrow to either.
#
# It is optional and stays optional: an entry written off a branch is still an
# entry and names none rather than a made-up default, which is what lets a
# reader tell "nowhere in particular" from "a branch called something".
an_entry_carries_the_branch_it_was_written_on() {
	recall
	local args
	args="$(wl_args "sharpened the quillon on the escapement" "" "" "" wl/escapement)" || return 1
	want_tool worklog_append "$TOKEN_A" "$args" || return 1
	want_eq "the branch rode the write" "$(tv .entry.branch)" wl/escapement || return 1

	args="$(wl_args "read the handoff off no branch at all")" || return 1
	want_tool worklog_append "$TOKEN_A" "$args" || return 1
	want_eq "an entry with no branch names none" "$(tv .entry.branch)" null || return 1

	# A branch is a ref or a worktree, not a paragraph. The refusal says which,
	# the way the ceiling on what does.
	local long
	long="$(printf 'b%.0s' $(seq 1 201))"
	args="$(wl_args "worked somewhere with a very long name" "" "" "" "$long")" || return 1
	want_tool_fails worklog_append "$TOKEN_A" "$args" "over the 200 ceiling" || return 1
	printf 'the branch rides the entry, and an entry without one says so\n'
}

# The read a worklog view does: the newest entries, newest first, through the
# timeline's own endpoint and therefore through the timeline's own permission
# filter. There is deliberately no worklog endpoint beside it - a second door
# onto the same rows is a second place for that filter to be missing - so what
# the view asks for is an ORDER on the read it already had.
#
# Without it a page that says "newest first" has to take the first page of the
# log and sort it, which on a log longer than one page hands back the OLDEST
# entries under that heading.
the_timeline_answers_the_worklog_newest_first() {
	recall
	api GET "$TOKEN_A" '/api/activity?kind=worklog&order=recent&limit=2' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the newest entry is first" \
		"$(jqv '.items[0].meta.what')" "read the handoff off no branch at all" || return 1
	want_eq "then the one before it" \
		"$(jqv '.items[1].meta.what')" "sharpened the quillon on the escapement" || return 1
	want_eq "and the branch is on it, where the write put it" \
		"$(jqv '.items[1].meta.branch')" wl/escapement || return 1

	# The default is unchanged: the timeline still pages forward from a cursor,
	# which is the read every other client of it does.
	api GET "$TOKEN_A" '/api/activity?kind=worklog&limit=2' || return 1
	want_eq "log order starts at the oldest end" \
		"$(jqv '.items[0].meta.what')" "wired the quibblewrench into the lexer" || return 1

	# An order nobody implements is refused rather than silently ignored: a read
	# that quietly answers in the other order is a page that lies about itself.
	want_status 400 GET "$TOKEN_A" '/api/activity?kind=worklog&order=sideways' || return 1
	printf 'order=recent is the newest end of the same filtered read: %s\n' "$(jqv .error)"
}

# ------------------------------------------------------- the project entity
#
# A project used to be a free string on a token: nothing declared one, nothing
# checked one, and a project came into existence the moment somebody wrote it.
# That is how a day of real shared memory was filed into `pa`, which is the
# smoke seeder's fixture project, with no surface saying so.
#
# So these checks are about three separate claims, and it is worth keeping them
# apart because only one of them refuses anything:
#
#   - the registry is a REFERENT. A write into a project that was never declared
#     is refused, so a typo is not silently a valid target.
#   - the fixture flag REFUSES NOTHING. pa is a legitimate writable project and
#     a write into it lands. What the flag does is make that write say so, at
#     the moment it is made, on every surface that carries it.
#   - none of it is a PERMISSION. What a principal may read is grants plus
#     scope, exactly as before, and the enumeration is a list of names narrowed
#     by the grant edges that already existed.

# The referent. Both ends of a grant name a declared project, so a capability
# into a project nobody declared is refused rather than stored - a typo that
# replicates is worse than a typo that is caught.
a_write_into_an_undeclared_project_is_refused() {
	recall
	want_status 400 POST "$TOKEN_A" /api/grants \
		'{"from_project": "pb-typo", "to_project": "pa"}' || return 1
	case "$(jqv .error)" in
	*"never declared here"*) ;;
	*)
		printf 'the refusal does not say the project was never declared: %s\n' "$(jqv .error)" >&2
		return 1
		;;
	esac
	printf 'a grant out of a project nobody declared: %s\n' "$(jqv .error)"
}

# The indicator. This is the surface whose absence let the pa write stay silent:
# a token is a (user, agent, project) triple, the project half decides where
# every write lands, and whoami answered the first two.
whoami_says_where_this_tokens_writes_land() {
	recall
	api GET "$TOKEN_A" /api/whoami || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the project it writes into" "$(jqv .project)" pa || return 1
	want_eq "which has a registry row" "$(jqv .project_declared)" true || return 1
	want_eq "and is a fixture" "$(jqv .project_fixture)" true || return 1
	want_eq "with an origin to decide a collision against" \
		"$(printf '%s' "$API_BODY" | jq -r 'if .project_origin == "" then "none" else "yes" end')" \
		yes || return 1
	printf 'whoami: %s writes into pa, which is demo seed data\n' "$(jqv .user)"
}

# The same answer from the command line, because the person who needs it is
# usually on the far end of an ssh session rather than in a browser.
the_cli_says_which_project_this_token_writes_to() {
	recall
	local out
	out="$(FLOWY_ADDR="http://127.0.0.1:$HTTP_PORT" FLOWY_TOKEN="$TOKEN_A" \
		./flowy projects 2>&1)" || {
		printf 'flowy projects exited non-zero:\n%s\n' "$out" >&2
		return 1
	}
	case "$out" in
	*"this token writes to pa"*"FIXTURE"*) ;;
	*)
		printf 'flowy projects does not lead with the fixture warning:\n%s\n' "$out" >&2
		return 1
		;;
	esac
	printf '%s\n' "$out" | head -1
}

# The flag refuses nothing, and says so. The write lands - pa is a real
# project - and the answer carries the sentence nobody was shown.
a_write_into_a_fixture_lands_and_says_so() {
	recall
	want_tool mem_write "$TOKEN_A" \
		'{"title": "real work into a fixture", "body": "the write is valid", "scope": "project"}' ||
		return 1
	want_eq "the item was written" "$(tv .item.project)" pa || return 1
	case "$(tv .warning)" in
	*"FIXTURE project"*) ;;
	*)
		printf 'the write into a fixture carried no warning: %s\n' "$(tv .warning)" >&2
		return 1
		;;
	esac

	# And the enumeration tool says the same thing without a write.
	want_tool projects "$TOKEN_A" '{}' || return 1
	want_eq "the current project" "$(tv .current)" pa || return 1
	want_eq "and it is flagged" \
		"$(printf '%s' "$TOOL_JSON" | jq -r '.projects[] | select(.current) | .fixture')" \
		true || return 1
	printf 'the write landed in pa and the answer says pa is a fixture\n'
}

# The enumeration is narrowed by the edges that already existed, and by nothing
# new. B is in pb and holds a grant with pa by now, so it sees both and does not
# see pc, which nobody opened to it.
the_enumeration_is_permission_filtered() {
	recall
	api GET "$TOKEN_B" /api/projects || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "B is writing into pb" "$(jqv .current)" pb || return 1
	want_eq "B sees its own project" \
		"$(printf '%s' "$API_BODY" | jq '[.projects[] | select(.id == "pb")] | length')" 1 || return 1
	want_eq "and the one it holds a grant with" \
		"$(printf '%s' "$API_BODY" | jq '[.projects[] | select(.id == "pa")] | length')" 1 || return 1
	want_eq "and not the one nobody opened to it" \
		"$(printf '%s' "$API_BODY" | jq '[.projects[] | select(.id == "pc")] | length')" 0 || return 1

	# The same token, in another project, is another principal - and sees pc
	# rather than pb. This is the existing scope rule and not a new one.
	api GET "$TOKEN_A_PC" /api/projects || return 1
	want_eq "A in pc writes into pc" "$(jqv .current)" pc || return 1
	printf 'B sees pb and pa and not pc; A-in-pc sees pc\n'
}

# Declaring is open, changing is the operator's. A declaration grants nothing -
# a project nobody holds a token for is a name and no more, because a write
# lands in the principal's own project - so anybody may make one. The fixture
# flag is what the next agent reads to decide whether it is in demo data, so
# that is the operator's.
declaring_is_open_and_flagging_a_fixture_is_the_operators() {
	recall
	local fresh="gate-declared-$$"
	want_status 200 POST "$TOKEN_A" /api/projects "{\"id\": \"$fresh\"}" || return 1
	want_eq "it was declared" "$(jqv .declared)" true || return 1
	want_eq "by the caller" "$(jqv .project.created_by)" "$USER_A" || return 1
	want_eq "with a derived identity, having no repository" \
		"$(printf '%s' "$API_BODY" | jq -r '.project.origin | startswith("derived:")')" true || return 1

	want_status 403 POST "$TOKEN_A" /api/projects "{\"id\": \"$fresh-fixture\", \"fixture\": true}" ||
		return 1
	want_status 403 POST "$TOKEN_A" /api/projects \
		"{\"id\": \"$fresh\", \"pin\": true, \"origin\": \"git@github.com:someone/else.git\"}" ||
		return 1
	printf 'anybody declares, only the operator flags and pins\n'
}

# One repository is one origin however it is spelled, and a project that moves
# to another one SUBSTITUTES rather than rewrites: the old origin goes into the
# chain, and no row that names the project is touched. That is the same rule as
# the pa migration - project is inside the signed payload, so rewriting it
# anywhere forges every row that carried it.
an_origin_is_one_string_and_a_move_is_an_alias() {
	recall
	local name="gate-origin-$$"
	want_status 200 POST "$TOKEN_OP" /api/projects \
		"{\"id\": \"$name\", \"origin\": \"git@github.com:acme/thing.git\"}" || return 1
	want_eq "the remote, canonicalised" "$(jqv .project.origin)" git:github.com/acme/thing || return 1

	# The same repository, spelled as an https URL: one project, no
	# substitution, nothing added to the chain.
	want_status 200 POST "$TOKEN_OP" /api/projects \
		"{\"id\": \"$name\", \"origin\": \"https://github.com/acme/thing\"}" || return 1
	want_eq "still the one origin" "$(jqv .project.origin)" git:github.com/acme/thing || return 1
	want_eq "and nothing was superseded" \
		"$(printf '%s' "$API_BODY" | jq '.project.superseded // [] | length')" 0 || return 1

	# A move: the remote was transferred. The identity substitutes and the old
	# one is kept, which is what lets a peer still holding it be recognised.
	want_status 200 POST "$TOKEN_OP" /api/projects \
		"{\"id\": \"$name\", \"origin\": \"git@gitlab.com:acme/thing.git\"}" || return 1
	want_eq "the new identity" "$(jqv .project.origin)" git:gitlab.com/acme/thing || return 1
	want_eq "and the one it superseded" \
		"$(printf '%s' "$API_BODY" | jq -r '.project.superseded[0]')" \
		git:github.com/acme/thing || return 1
	printf 'one repository is one origin; a transfer is an alias, not a rewrite\n'
}

# The migration claim, read straight out of the database: the rows that existed
# before the registry did still name what they named, and their signatures are
# untouched. Nothing here rewrites a project column to fit a registry.
the_registry_adapted_to_the_data() {
	psql_counts "SELECT count(*) FROM projects WHERE id IN ('pa', 'pb', 'pc')" || return 1
	local unsigned
	unsigned="$(scalar "SELECT count(*) FROM projects WHERE sig IS NULL AND provenance <> 'observed'")"
	want_eq "declared rows with no signature" "$unsigned" 0 || return 1
	local orphans
	orphans="$(scalar "SELECT count(*) FROM artifacts a
	                    WHERE a.project IS NOT NULL
	                      AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = a.project)")"
	want_eq "artifacts naming a project with no registry row" "$orphans" 0 || return 1
	printf 'every project the data names has a row, and every declared row is signed\n'
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

# say_to_in_background ROOM TOKEN TO BODY - the same, addressed at somebody, so
# a waiter that is only listening for its own name has something to wake for.
say_to_in_background() {
	local room=$1 token=$2 to=$3 body=$4
	(
		sleep 1
		curl --silent --show-error -X POST \
			-H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
			--data-binary "$(jq -nc --arg t "$to" --arg b "$body" '{to: $t, body: $b}')" \
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
	want_eq "under the name they had when they said it" \
		"$(jqv .meta.actor_name)" "$HANDLE_A" || return 1
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
	# An agent has no handle of its own - the agents table carries the runtime
	# it is and the person it acts for - so it speaks under that person's
	# handle, and meta.actor_kind above is what says an agent is talking.
	want_eq "and under the name the room knows it by" \
		"$(jqv .meta.actor_name)" "$HANDLE_A" || return 1
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

# And every one of them says who said it, on the read. A client draws a room out
# of GET /api/chat/{room} and can only show what comes back: a read that carried
# actor ids and nothing else is why a room of four agents and a person rendered
# as five lines of the same eight characters, whatever the console did with it.
the_room_read_says_who_said_each_thing() {
	recall
	api GET "$TOKEN_A" /api/chat/general || return 1
	want_eq "messages carrying the speaker's name" \
		"$(chat_len ".meta.actor_name == \"$HANDLE_A\"")" 3 || return 1
	want_eq "messages with no name on them" "$(chat_len '.meta.actor_name == null')" 0 || return 1
	# The name is what the speaker was called when they spoke, and it is a name
	# rather than the id it replaces - a read that answered with the ulid under
	# a new key would pass a check that only asked for the key to be there.
	want_eq "messages naming the actor id instead" \
		"$(chat_len '.meta.actor_name == .actor')" 0 || return 1
	printf 'three messages in the room, each saying %s said it\n' "$HANDLE_A"
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

# ------------------------------------------------------------ chat addressing
#
# A message can be directed at one principal and still be a message in the room.
# The claim worth checking is the second half of that sentence: addressing
# changes what a reader is TOLD and never what they may SEE, so the checks that
# look like they are about a field are really about the read filter not having
# moved. Every one of them runs in a room of its own, so that the counts the
# phase 3 checks above assert are not disturbed by messages said down here.

# say_to TOKEN ROOM TO BODY - a message directed at somebody.
say_to() {
	api POST "$1" "/api/chat/$2/say" \
		"$(jq -nc --arg t "$3" --arg b "$4" '{to: $t, body: $b}')"
}

a_message_can_be_addressed() {
	recall
	local id
	say_to "$TOKEN_A" addressing "$USER_B" "the deploy looks wrong to me" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "who it is for" "$(jqv .addressee)" "$USER_B" || return 1
	want_eq "and it is still a message in a room" "$(jqv .room)" addressing || return 1
	want_eq "in the speaker's project" "$(jqv .project)" pa || return 1
	id="$(jqv .id)"
	remember CHAT_TO_B "$id"

	api GET "$TOKEN_A" /api/chat/addressing || return 1
	want_eq "the read path carries the addressee" \
		"$(chat_len ".id == \"$id\" and .addressee == \"$USER_B\"")" 1 || return 1
	printf 'addressed at %s, said in a room, read back with the addressee on it\n' "$USER_B"
}

# A NAME IS AN ADDRESS, because the name is what every surface shows you.
#
# The transcript, the roster and a todo's owner all draw people by handle, and
# --to took only the id underneath - so the console told you "flowy-claude" and
# the door answered "no principal called flowy-claude here". Reported by
# somebody who could see the name and could not use it.
#
# It resolves through the same table @-mentions use, so @alice and --to alice
# cannot come to disagree about who alice is, and it STORES THE ID: a handle
# can be changed later, and a message addressed to a spelling would retarget
# with it.
a_handle_is_an_address() {
	recall
	local id
	# HANDLE_B is the seeded name the room draws user B by - the same string
	# @-mentions resolve, which is the point of routing both through one table.
	[ -n "$HANDLE_B" ] || {
		printf 'no seeded handle for user B, so nothing about naming was tested\n' >&2
		return 1
	}
	say_to "$TOKEN_A" addressing "$HANDLE_B" "addressed by the name on the screen" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	# The ID, not the handle: what is stored is the principal, not the spelling.
	want_eq "the handle resolved to the principal" "$(jqv .addressee)" "$USER_B" || return 1
	id="$(jqv .id)"
	printf 'addressed as %s, stored as %s (%s)\n' "$HANDLE_B" "$USER_B" "$id"
}

# And a name nothing answers to is still refused at the door, loudly, before
# the row is written. A refusal nobody sees is indistinguishable from success:
# a message addressed to a typo that posts unaddressed is one the sender
# believes was delivered and the recipient never hears about.
a_name_nothing_answers_to_is_refused() {
	recall
	say_to "$TOKEN_A" addressing "nobody-called-this" "into the void"
	want_eq "a typo is refused rather than posted unaddressed" "$API_STATUS" 400 || return 1
	printf 'refused: %s\n' "$(jqv .error)"
}

# An actor is a user or an agent, so an addressee is too: an agent is a thing
# you can say something to.
an_agent_can_be_addressed() {
	recall
	say_to "$TOKEN_A" addressing "$AGENT_A" "over to you" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "who it is for" "$(jqv .addressee)" "$AGENT_A" || return 1
	printf 'an agent is an addressee: %s\n' "$AGENT_A"
}

# A message with no addressee is what a message has always been here, and the
# field is absent rather than empty.
an_unaddressed_message_is_still_a_message() {
	recall
	api POST "$TOKEN_A" /api/chat/addressing/say '{"body": "for the room"}' || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "the addressee of a message to the room" "$(jqv '.addressee // ""')" "" || return 1
	printf 'no addressee, and nothing else about the message changed\n'
}

# A name nothing answers to is the worst available failure - the sender believes
# somebody was told and nobody was - so it is refused at the door, and the
# refusal writes no row.
an_unknown_addressee_is_refused() {
	recall
	local before after
	before="$(scalar "SELECT count(*) FROM events WHERE room = 'addressing'")" || return 1
	say_to "$TOKEN_A" addressing 01NOSUCHPRINCIPAL0000000AA "for nobody at all" || return 1
	want_eq "status" "$API_STATUS" 400 || return 1
	after="$(scalar "SELECT count(*) FROM events WHERE room = 'addressing'")" || return 1
	want_eq "messages the refusal wrote" "$((after - before))" 0 || return 1
	printf 'refused, and nothing written: %s\n' "$(jqv .error)"
}

# ------------------------------------------------------------------ @mentions
#
# The same field, filled in by the words instead of by a flag. `to` works and
# nobody types it mid-sentence, so agents addressed each other constantly and
# the person in the room addressed nobody - see mentions.go. What these check is
# that an @name lands in the addressee column and nowhere else, and that the
# three things that are NOT mentions are not treated as ones.

# say_body TOKEN ROOM BODY - a message with nothing but a body, so whatever
# fills the addressee in can only be the words.
say_body() {
	api POST "$1" "/api/chat/$2/say" "$(jq -nc --arg b "$3" '{body: $b}')"
}

# mentions_meta - the "name:id" pairs the node stamped on the last message.
mentions_meta() {
	printf '%s' "$API_BODY" | jq -r '.meta.mentions // ""'
}

a_mention_addresses_the_message() {
	recall
	say_body "$TOKEN_A" addressing "@$HANDLE_B the deploy looks wrong to me" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "who the words addressed it to" "$(jqv .addressee)" "$USER_B" || return 1
	want_eq "and the body is what was written" \
		"$(jqv .body)" "@$HANDLE_B the deploy looks wrong to me" || return 1
	# Mid-sentence, which is the case the flag could never cover: the same
	# message with --to is a message somebody remembered to flag.
	say_body "$TOKEN_A" addressing "the gearbox again, @$HANDLE_B - can you look?" || return 1
	want_eq "a name inside the sentence addresses too" "$(jqv .addressee)" "$USER_B" || return 1
	# And the same name shouted at the start of a sentence is the same person.
	local shouted
	shouted="$(printf '%s' "$HANDLE_B" | tr '[:lower:]' '[:upper:]')"
	say_body "$TOKEN_A" addressing "@$shouted please" || return 1
	want_eq "the case it was written in does not matter" "$(jqv .addressee)" "$USER_B" || return 1
	printf 'the name in the prose is the addressing: %s\n' "$USER_B"
}

# Several names in one message. The event carries ONE addressee, so the first
# one takes it and the rest ride in meta - and that is a decision worth
# asserting rather than leaving to whichever the map iterated first.
the_first_mention_addresses_and_the_rest_are_recorded() {
	recall
	say_body "$TOKEN_A" addressing "@$HANDLE_B and @$HANDLE_OP, one of you please" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "the first name addressed it" "$(jqv .addressee)" "$USER_B" || return 1
	case "$(mentions_meta)" in
	"$HANDLE_B:$USER_B $HANDLE_OP:$USER_OP") ;;
	*)
		printf 'the mentions on the message are %q, want both pairs in order\n' \
			"$(mentions_meta)" >&2
		return 1
		;;
	esac
	# An explicit `to` is a field somebody filled in deliberately, so it wins.
	say_to "$TOKEN_A" addressing "$USER_OP" "@$HANDLE_B ask the operator about this" || return 1
	want_eq "to beats a mention" "$(jqv .addressee)" "$USER_OP" || return 1
	printf 'first mention addresses, the rest are on the message, --to still wins\n'
}

# The three things that look like mentions and are not. The email address is the
# one that would have broken the naive version - `@(\w+)` turns every address
# anybody pastes into a room into a mention of whoever holds that handle, and it
# is the case nobody writes the feature for.
what_is_not_a_mention_addresses_nobody() {
	recall
	say_body "$TOKEN_A" addressing "write to $HANDLE_B@example.com about the gearbox" || return 1
	want_eq "an email address addresses nobody" "$(jqv '.addressee // ""')" "" || return 1
	want_eq "and stamps no mentions" "$(mentions_meta)" "" || return 1

	# A name nothing answers to is NOT a refusal - people type names that do not
	# exist, and losing what somebody wrote over a word in it is the worse
	# failure by a distance. It stays as text.
	local before after
	before="$(scalar "SELECT count(*) FROM events WHERE room = 'addressing'")" || return 1
	say_body "$TOKEN_A" addressing "@nobody-at-all-here is not here" || return 1
	want_eq "a name nobody answers to is still a message" "$API_STATUS" 200 || return 1
	want_eq "addressed at nobody" "$(jqv '.addressee // ""')" "" || return 1
	want_eq "with the word left in it" "$(jqv .body)" "@nobody-at-all-here is not here" || return 1
	after="$(scalar "SELECT count(*) FROM events WHERE room = 'addressing'")" || return 1
	want_eq "and it was written" "$((after - before))" 1 || return 1

	say_body "$TOKEN_A" addressing "the build is red@" || return 1
	want_eq "a bare @ addresses nobody" "$(jqv '.addressee // ""')" "" || return 1
	printf 'an address, a name nobody answers to and a stray @: none of them addressed anybody\n'
}

# The one that matters, in both directions.
#
# It does not widen: a message said in pc and addressed at B, who holds no grant
# into pc, is not readable by B - not through the room and not through the
# inbox. Being named on a message is not a capability, and this check is what
# fails if it ever becomes one.
#
# It does not narrow: a message in pa addressed at B is read by exactly who read
# pa's rooms before - B through the Phase 1 grant, and the operator, who is not
# named on it at all.
addressing_changes_nothing_about_who_reads() {
	recall
	local named granted
	say_to "$TOKEN_A_PC" addressing "$USER_B" "named in a project you are not in" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "it landed in pc" "$(jqv .project)" pc || return 1
	named="$(jqv .id)"

	api GET "$TOKEN_B" /api/chat/addressing || return 1
	want_eq "B reads a pc message that names B" "$(chat_len ".id == \"$named\"")" 0 || return 1
	api GET "$TOKEN_B" '/api/inbox?since=0' || return 1
	want_eq "and B's inbox holds it" "$(chat_len ".id == \"$named\"")" 0 || return 1

	say_to "$TOKEN_A" addressing "$USER_B" "named across the grant" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	granted="$(jqv .id)"
	api GET "$TOKEN_B" /api/chat/addressing || return 1
	want_eq "B reads a pa message that names B" "$(chat_len ".id == \"$granted\"")" 1 || return 1
	api GET "$TOKEN_OP" /api/chat/addressing || return 1
	want_eq "somebody in pa who is not named still reads it" \
		"$(chat_len ".id == \"$granted\"")" 1 || return 1
	printf 'the addressee opened nothing and closed nothing: the grant decided both\n'
}

# -------------------------------------------------------- direct messages
#
# A DM is a chat event with no project, no room and an addressee, and it is read
# by its author and by the principal it names - one clause in EventFilterSQL's
# projectless branch, which is the branch that already excludes everybody.
#
# THE CHECK THAT MEANS ANYTHING IS THE THIRD PRINCIPAL'S. "Both parties can read
# it" passes under a completely broken implementation - the one where everybody
# can read it - so it is written here as a control and never as the assertion.
# What the assertion is: the operator, who is in pa with A, reads every other
# message A writes and does not read this one, over the wire, as a third token.
#
# Every phrase below is a nonsense word that appears nowhere else in this gate,
# so a count of what came back is a statement about these rows and not about
# what an earlier check left in the log.
DM_PRIVATE_WORD="wombatinairlock"
DM_PUBLIC_WORD="wombatintheroom"
readonly DM_PRIVATE_WORD DM_PUBLIC_WORD

# dm TOKEN TO BODY [THREAD] - send a direct message. The addressee is the path:
# a private message with nobody to send it to is refused by the route itself.
dm() {
	local token=$1 to=$2 body=$3 thread=${4-}
	api POST "$token" "/api/dm/$to" \
		"$(jq -nc --arg b "$body" --arg t "$thread" \
			'{body: $b} + (if $t == "" then {} else {thread: $t} end)')"
}

# items EXPR - how many activity items the last response returned, narrowed by a
# jq select expression. The timeline's ?q= is the only search in this node that
# looks at what was SAID, so it is the one a message can leak through.
items() {
	printf '%s' "$API_BODY" | jq "[.items[] | select($1)] | length"
}

# A direct message is between two people, and both of them have it. This is the
# control for the check below it and not the point: a build where everybody
# could read it would pass this one.
a_direct_message_reaches_both_parties() {
	recall
	dm "$TOKEN_A" "$USER_B" "the $DM_PRIVATE_WORD is loose" || return 1
	want_eq "send status" "$API_STATUS" 200 || return 1
	want_eq "it is a chat event" "$(jqv .type)" chat || return 1
	want_eq "it carries no project" "$(jqv '.project // "null"')" null || return 1
	want_eq "it carries no room" "$(jqv '.room // ""')" "" || return 1
	want_eq "who it is for" "$(jqv .addressee)" "$USER_B" || return 1
	want_eq "and the node says it is private" "$(jqv .private)" true || return 1
	local id thread
	id="$(jqv .id)"
	thread="$(jqv .thread)"
	remember DM_ID "$id"
	remember DM_THREAD "$thread"

	# The sender reads it back through the private log.
	api GET "$TOKEN_A" '/api/dm?since=0' || return 1
	want_eq "the sender's private log has it" "$(chat_len ".id == \"$id\"")" 1 || return 1
	# And the addressee, who is in another project entirely.
	api GET "$TOKEN_B" '/api/dm?since=0' || return 1
	want_eq "the addressee's private log has it" "$(chat_len ".id == \"$id\"")" 1 || return 1
	api GET "$TOKEN_B" '/api/inbox?since=0' || return 1
	want_eq "and it is in their inbox" "$(chat_len ".id == \"$id\"")" 1 || return 1
	printf 'private between %s and %s, thread %s\n' "$USER_A" "$USER_B" "$thread"
}

# THE ONE THAT DECIDES WHETHER THIS FEATURE SHIPS.
#
# The operator is a third principal in pa, which is A's project. They are not
# named on the message, they hold no grant that reaches it, and they are asking
# as themselves - ?scope=all is not used anywhere here, and the check below it
# covers that separately. The control is in the same function on purpose: A says
# something in a room at the same moment, and the operator reads THAT - so a
# failure here is about the direct message and not about the operator having
# been left out of everything.
a_third_principal_in_the_project_cannot_read_a_dm() {
	recall
	# The control: an ordinary room message from A, in pa, said now.
	api POST "$TOKEN_A" /api/chat/dmroom/say \
		"$(jq -nc --arg b "the $DM_PUBLIC_WORD is fine" '{body: $b}')" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	local public
	public="$(jqv .id)"
	remember DM_PUBLIC "$public"

	api GET "$TOKEN_OP" /api/chat/dmroom || return 1
	want_eq "the third principal reads A's room message" \
		"$(chat_len ".id == \"$public\"")" 1 || return 1
	api GET "$TOKEN_OP" '/api/dm?since=0' || return 1
	want_eq "and reads none of the private log" "$(chat_len ".id == \"$DM_ID\"")" 0 || return 1
	printf 'the operator is in pa, reads %s, and does not read %s\n' "$public" "$DM_ID"
}

# And it is on none of their surfaces. Each of these is a different read of the
# same log, and each of them has been the place a row went out before.
a_dm_is_on_none_of_a_third_principals_surfaces() {
	recall
	# The room read it was never in.
	api GET "$TOKEN_OP" /api/chat/dmroom || return 1
	want_eq "the room read" "$(chat_len ".id == \"$DM_ID\"")" 0 || return 1
	# The event list - the widest read there is.
	api GET "$TOKEN_OP" '/api/events?since=0&limit=500' || return 1
	want_eq "the event list" "$(chat_len ".id == \"$DM_ID\"")" 0 || return 1
	# The inbox, which crosses rooms and projects by design.
	api GET "$TOKEN_OP" '/api/inbox?since=0&limit=500' || return 1
	want_eq "the inbox" "$(chat_len ".id == \"$DM_ID\"")" 0 || return 1
	# The activity timeline, and its ?q= - the one search in this node that
	# looks at what was said rather than at an artifact's text.
	api GET "$TOKEN_OP" "/api/activity?q=$DM_PRIVATE_WORD" || return 1
	want_eq "the timeline search" "$(items 'true')" 0 || return 1
	api GET "$TOKEN_OP" "/api/activity?q=$DM_PUBLIC_WORD" || return 1
	want_eq "and it does find the room message beside it" "$(items 'true')" 1 || return 1
	# The artifact search, which is where a body would go out if a message had
	# ever been one.
	api GET "$TOKEN_OP" "/api/search?q=$DM_PRIVATE_WORD" || return 1
	want_eq "the artifact search" "$(hits)" 0 || return 1
	# And the thread, read by id, which is how the tasks clause used to widen.
	api GET "$TOKEN_OP" "/api/events?thread=$DM_THREAD" || return 1
	want_eq "the thread read by id" "$(chat_len 'true')" 0 || return 1

	# The other end of every one of those: the party reads it everywhere.
	api GET "$TOKEN_B" "/api/activity?q=$DM_PRIVATE_WORD" || return 1
	want_eq "the addressee's timeline search finds it" "$(items 'true')" 1 || return 1
	want_eq "and it says private" "$(items '.private == true')" 1 || return 1
	printf 'room read, event list, inbox, timeline search, artifact search and thread: none\n'
}

# The escape hatch is not one either. ?scope=all is the operator's window onto
# their own node and every other read honours it; the private log is the one
# endpoint that is not a place to read somebody else's conversation from.
a_dm_is_not_handed_over_by_scope_all() {
	recall
	# The door refuses the parameter outright rather than reading it and
	# declining to widen. Both leave the private log private, but only the
	# refusal SAYS SO - and this check used to pass because /api/dm ignored
	# `scope` silently, which is indistinguishable from honouring it and
	# finding nothing. A caller could not tell those apart, and neither could
	# this check.
	want_status 400 GET "$TOKEN_OP" '/api/dm?since=0&scope=all' || return 1
	# And the log itself is still not the operator's to read.
	api GET "$TOKEN_OP" '/api/dm?since=0' || return 1
	want_eq "the private log to the operator" "$(chat_len ".id == \"$DM_ID\"")" 0 || return 1
	printf 'scope=all is refused on the private log, and the log is not yours either\n'
}

# A reply stays between the two people the first message was between. The party
# set is fixed by the opening message, and the read filter cannot see this: every
# row it judges names exactly one addressee and looks perfectly private on its
# own, so a thread with three people in it would pass every check above.
a_reply_to_a_dm_does_not_widen_it() {
	recall
	local before after
	# A party replying to the other party: ordinary, and it stays in the thread.
	dm "$TOKEN_B" "$USER_A" "the $DM_PRIVATE_WORD is contained" "$DM_THREAD" || return 1
	want_eq "the reply" "$API_STATUS" 200 || return 1
	want_eq "it stays in the conversation" "$(jqv .thread)" "$DM_THREAD" || return 1
	want_eq "and it is private too" "$(jqv .private)" true || return 1

	# A party trying to bring somebody else in, which is the whole point.
	before="$(scalar "SELECT count(*) FROM events WHERE thread = '$DM_THREAD'")" || return 1
	dm "$TOKEN_B" "$USER_OP" "come and look at this" "$DM_THREAD" || return 1
	want_eq "widening the conversation" "$API_STATUS" 400 || return 1
	after="$(scalar "SELECT count(*) FROM events WHERE thread = '$DM_THREAD'")" || return 1
	want_eq "rows the refusal wrote" "$((after - before))" 0 || return 1

	# And somebody outside it writing in, which is refused one step earlier: they
	# cannot read a single row of the thread, so it is not theirs to write to.
	dm "$TOKEN_OP" "$USER_A" "let me in" "$DM_THREAD" || return 1
	want_eq "an outsider writing into the conversation" "$API_STATUS" 403 || return 1

	# After all of that, the third principal still reads none of it.
	api GET "$TOKEN_OP" "/api/events?thread=$DM_THREAD" || return 1
	want_eq "the outsider's thread read" "$(chat_len 'true')" 0 || return 1
	api GET "$TOKEN_OP" '/api/dm?since=0' || return 1
	want_eq "the outsider's private log" "$(chat_len 'true')" 0 || return 1
	printf 'two people in, two people still in, and nothing written by the two refusals\n'
}

# The other direction, and the trap that is easiest to fall into: a party writing
# into their own private thread through a PUBLIC door. The row would carry their
# home project and be read by everybody in it, from a box that gave no sign of
# the difference - so every public write path refuses a private conversation.
a_public_write_cannot_join_a_private_conversation() {
	recall
	local before after
	before="$(scalar "SELECT count(*) FROM events WHERE thread = '$DM_THREAD'")" || return 1
	api POST "$TOKEN_A" /api/chat/dmroom/say \
		"$(jq -nc --arg t "$DM_THREAD" --arg b "oops" '{body: $b, thread: $t}')" || return 1
	want_eq "a room say into a private thread" "$API_STATUS" 400 || return 1
	api POST "$TOKEN_A" /api/activity \
		"$(jq -nc --arg t "$DM_THREAD" '{kind: "chat", body: "oops", thread: $t}')" || return 1
	want_eq "a timeline post into a private thread" "$API_STATUS" 400 || return 1
	api POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg t "$DM_THREAD" '{type: "note", body: "oops", thread: $t}')" || return 1
	want_eq "an event append into a private thread" "$API_STATUS" 400 || return 1
	after="$(scalar "SELECT count(*) FROM events WHERE thread = '$DM_THREAD'")" || return 1
	want_eq "rows the three refusals wrote" "$((after - before))" 0 || return 1
	printf 'three public doors, three refusals, nothing written\n'
}

# And the reverse of that: a handoff thread is read by the parties to the task,
# because the tasks clause in EventFilterSQL is OR-ed onto the end of the whole
# predicate and ADDS readers. A "private" message dropped into one would be read
# by the assigner, the assignee and the delegated agent, none of whom the sender
# named. This is the check that fails if a DM is ever built on that clause.
a_dm_cannot_join_a_handoff_thread() {
	recall
	local artifact task thread
	artifact="$(new_artifact "$TOKEN_A" bug "a handoff a dm must not join")" || return 1
	assign_as "$TOKEN_A" "$artifact" "$USER_B" "for the dm check" || return 1
	want_eq "assign status" "$API_STATUS" 200 || return 1
	task="$(jqv .id)"
	thread="$(jqv .thread)"
	dm "$TOKEN_A" "$USER_B" "quietly, in the handoff" "$thread" || return 1
	want_eq "a private message into a handoff thread" "$API_STATUS" 400 || return 1
	printf 'task %s keeps its thread: %s\n' "$task" "$(jqv .error)"
}

# A name nothing answers to is worse here than in a room: a room message to a
# typo is still said in the room and somebody reads it, and a private message to
# a typo is read by nobody at all, for ever, while the sender is told it went.
a_dm_to_a_name_nothing_answers_to_is_refused() {
	recall
	local before after
	before="$(scalar "SELECT count(*) FROM events WHERE project IS NULL AND coalesce(room, '') = ''")" || return 1
	dm "$TOKEN_A" 01NOSUCHPRINCIPAL0000000AA "into the void" || return 1
	want_eq "status" "$API_STATUS" 400 || return 1
	after="$(scalar "SELECT count(*) FROM events WHERE project IS NULL AND coalesce(room, '') = ''")" || return 1
	want_eq "messages the refusal wrote" "$((after - before))" 0 || return 1
	printf 'refused, and nothing written: %s\n' "$(jqv .error)"
}

# Nothing that was readable stopped being readable, and nothing that was private
# became visible. The rows named here are the ones the phase 3 and addressing
# checks above created and asserted, re-read now that the filter has a new
# clause in it - so this is the regression half rather than a fresh claim.
the_project_rooms_are_unchanged_by_direct_messages() {
	recall
	# The phase 3 thread: A's own two messages, still there, still in order.
	api GET "$TOKEN_A" /api/chat/general || return 1
	want_eq "A still reads the first message" "$(chat_len ".id == \"$CHAT_M1\"")" 1 || return 1
	want_eq "A still reads the reply" "$(chat_len ".id == \"$CHAT_M2\"")" 1 || return 1
	# The addressed room message in pa: read by B across the grant, and by the
	# operator, who is not named on it at all.
	api GET "$TOKEN_B" /api/chat/addressing || return 1
	want_eq "B still reads the pa message that names B" \
		"$(chat_len ".id == \"$CHAT_TO_B\"")" 1 || return 1
	api GET "$TOKEN_OP" /api/chat/addressing || return 1
	want_eq "somebody in pa who is not named still reads it" \
		"$(chat_len ".id == \"$CHAT_TO_B\"")" 1 || return 1
	# And a room thread still takes a room reply, which the private-thread
	# refusal above must not have caught.
	local thread
	api POST "$TOKEN_A" /api/chat/dmroom/say '{"body": "opens a room thread"}' || return 1
	thread="$(jqv .thread)"
	api POST "$TOKEN_A" /api/chat/dmroom/say \
		"$(jq -nc --arg t "$thread" '{body: "continues it", thread: $t}')" || return 1
	want_eq "a room reply into a room thread" "$API_STATUS" 200 || return 1
	want_eq "and it stayed in the thread" "$(jqv .thread)" "$thread" || return 1
	# The personal floor, which is the branch the new clause lives in: B still
	# cannot reach A's personal note, grant or no grant.
	want_status 404 GET "$TOKEN_B" "/api/artifact/$NOTE" || return 1
	printf 'the rooms, the grant, the addressee and the personal floor: all unchanged\n'
}

# ----------------------------------------------------------- the inbox waiter
#
# `flowy inbox --as NAME` is the thing that replaces a shell loop, so it is
# checked as a process rather than as an endpoint: what it exits with, what goes
# to stdout, what goes to stderr, and where the node thinks it got to
# afterwards. Every clause below comes from a way one of those loops failed.
#
# The room the waiter watches is its own, and the token that speaks into it is
# A's agent - which is a different actor from A, so it is news to A, exactly as
# GET /api/inbox has always had it.

INBOX_ROOM=waiting
readonly INBOX_ROOM

# inbox_run ARGS... - the waiter, with the two streams kept apart because
# "only messages go to stdout" is one of the things being checked, and with its
# exit code kept because the exit code IS the answer.
inbox_run() {
	INBOX_STATUS=0
	"$ROOT/flowy" inbox --url "http://127.0.0.1:$HTTP_PORT" "$@" \
		>"$WORK/inbox.out" 2>"$WORK/inbox.err" || INBOX_STATUS=$?
	INBOX_OUT="$(cat "$WORK/inbox.out")"
	INBOX_ERR="$(cat "$WORK/inbox.err")"
}

# inbox_mark NAME - where the node says that waiter got to.
inbox_mark() {
	scalar "SELECT read_cursor FROM inbox_readers WHERE reader = '$1'"
}

# inbox_acks NAME COLUMN - how many times the mark moved for that reason.
inbox_acks() {
	scalar "SELECT $2 FROM inbox_readers WHERE reader = '$1'"
}

# A label nothing declared is a refusal that names the labels that do exist, not
# a new reader starting from now. The silent version of this is an inbox that is
# permanently empty and never errors, which is indistinguishable from a quiet
# room - and leaves a junk identity behind that anything counting armed waiters
# counts as a session listening.
an_unknown_waiter_is_refused_with_what_does_exist() {
	recall
	inbox_run --token "$TOKEN_A" --as gate-waiter --deadline 5
	want_eq "exit code for a name nothing declared" "$INBOX_STATUS" 2 || return 1
	want_eq "and it wrote no messages" "$INBOX_OUT" "" || return 1
	case "$INBOX_ERR" in
	*"no inbox reader called gate-waiter"*) ;;
	*)
		printf 'the refusal does not name the label:\n%s\n' "$INBOX_ERR" >&2
		return 1
		;;
	esac
	printf 'a typo is exit 2 and a refusal: %s\n' "$INBOX_ERR"
}

# --new declares it, at the head of what this principal can already read rather
# than at the beginning of the log: a waiter is armed to hear what happens next,
# and one that replayed every room it can see would have its first batch thrown
# away by whoever armed it.
a_declared_waiter_starts_at_the_head_and_a_quiet_deadline_is_exit_1() {
	recall
	local head mark start elapsed
	head="$(scalar "SELECT coalesce(max(seq_hlc), 0) FROM events WHERE type = 'chat'")" || return 1
	start=$SECONDS
	inbox_run --token "$TOKEN_A" --as gate-waiter --new --deadline 3
	elapsed=$((SECONDS - start))
	want_eq "exit code for a deadline that passed quietly" "$INBOX_STATUS" 1 || return 1
	want_eq "messages written on a quiet deadline" "$INBOX_OUT" "" || return 1
	mark="$(inbox_mark gate-waiter)" || return 1
	if [ "$mark" -lt "$head" ]; then
		printf 'a new waiter started at %s, below the head at %s\n' "$mark" "$head" >&2
		return 1
	fi
	# And it took about the three seconds it was asked for. The poll window is
	# twenty, so a deadline shorter than one has to shorten the last request or
	# a caller who asked to wait three seconds waits twenty.
	if [ "$elapsed" -gt 12 ]; then
		printf 'a 3s deadline took %ss: the last poll was not shortened to the budget\n' \
			"$elapsed" >&2
		return 1
	fi
	printf 'declared at %s, quiet for %ss, exit 1\n' "$mark" "$elapsed"
}

# The return is the wake-up: the first message ends the wait, and it does not
# wait out the rest of the deadline to batch anything with it.
the_waiter_returns_on_the_first_message() {
	recall
	local poster start elapsed lines
	poster="$(say_in_background "$INBOX_ROOM" "$TOKEN_A_AGENT" "wake up, the build is red")"
	start=$SECONDS
	inbox_run --token "$TOKEN_A" --as gate-waiter --deadline 40
	elapsed=$((SECONDS - start))
	wait "$poster" 2>/dev/null || true

	want_eq "exit code when something was said" "$INBOX_STATUS" 0 || return 1
	if [ "$elapsed" -ge 35 ]; then
		printf 'it returned after %ss, so it waited out the deadline\n' "$elapsed" >&2
		return 1
	fi
	lines="$(printf '%s\n' "$INBOX_OUT" | grep -c . || true)"
	want_eq "lines on stdout" "$lines" 1 || return 1
	want_eq "what it heard" \
		"$(printf '%s' "$INBOX_OUT" | jq -r .body)" "wake up, the build is red" || return 1
	want_eq "which room" "$(printf '%s' "$INBOX_OUT" | jq -r .room)" "$INBOX_ROOM" || return 1
	want_eq "who said it" "$(printf '%s' "$INBOX_OUT" | jq -r .actor)" "$AGENT_A" || return 1
	# The cursor is on every line, not only at the end, so a consumer that dies
	# part way through a batch resumes from what it actually processed.
	if [ "$(printf '%s' "$INBOX_OUT" | jq -r .cursor)" -le 0 ]; then
		printf 'the message carries no cursor:\n%s\n' "$INBOX_OUT" >&2
		return 1
	fi
	# And the two streams stay apart, or the first fire corrupts the JSON stream
	# this is piped into. It used to look for the "re-arm with ..." line as its
	# sample of human-facing text; 82ec53d removed that advice, because the
	# listener now forks its own successor instead of asking the caller to arm
	# another - so the check was left asserting a sentence the design had
	# dropped. What it always meant is asserted directly instead: nothing on
	# stderr may parse as the JSON a consumer reads, whatever the wording is.
	if printf '%s' "$INBOX_ERR" | jq -e . >/dev/null 2>&1; then
		printf 'stderr carries JSON, so the streams are not separated:\n%s\n' "$INBOX_ERR" >&2
		return 1
	fi
	printf 'woke after %ss with one JSON line, and nothing else on stdout\n' "$elapsed"
}

# The cursor is the node's, so the next call does not hand the same message over
# again - which is the whole reason it is not a file beside the client.
the_cursor_is_the_nodes_so_the_next_call_does_not_repeat() {
	recall
	local was
	was="$(inbox_mark gate-waiter)" || return 1
	inbox_run --token "$TOKEN_A" --as gate-waiter --deadline 3
	want_eq "exit code with nothing new said" "$INBOX_STATUS" 1 || return 1
	want_eq "messages repeated" "$INBOX_OUT" "" || return 1
	want_eq "acknowledged after a delivery" "$(inbox_acks gate-waiter acked_delivery)" 1 || return 1
	printf 'the mark stayed at %s and the message was not handed over twice\n' "$was"
}

# The clause claude-host paid for twice. Your own messages must not wake you -
# an inbox is what you did not write - but the mark has to pass them anyway. A
# mark that stops in front of your own message is a waiter that reads it, drops
# it, and stops in the same place on every call afterwards: returning instantly
# in a loop, burning a session, and looking from outside exactly like traffic.
its_own_messages_do_not_wake_it_and_the_mark_still_passes_them() {
	recall
	local mine seq mark quiet_before quiet_after
	quiet_before="$(inbox_acks gate-waiter acked_quiet)" || return 1
	api POST "$TOKEN_A" "/api/chat/$INBOX_ROOM/say" '{"body": "something I said myself"}' || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	mine="$(jqv .id)"
	seq="$(jqv .seq_hlc)"

	inbox_run --token "$TOKEN_A" --as gate-waiter --deadline 3
	want_eq "exit code for a room holding only my own message" "$INBOX_STATUS" 1 || return 1
	want_eq "my own message handed back to me" "$INBOX_OUT" "" || return 1

	mark="$(inbox_mark gate-waiter)" || return 1
	if [ "$mark" -lt "$seq" ]; then
		printf 'the mark is %s and my own message %s is at %s: it will be read forever\n' \
			"$mark" "$mine" "$seq" >&2
		return 1
	fi
	# And the move was recorded as a quiet one rather than as a delivery, so a
	# lost acknowledgement and a quiet night are two different rows.
	quiet_after="$(inbox_acks gate-waiter acked_quiet)" || return 1
	if [ "$quiet_after" -le "$quiet_before" ]; then
		printf 'the quiet ack was not counted: %s then %s\n' "$quiet_before" "$quiet_after" >&2
		return 1
	fi
	printf 'not woken, and the mark moved past %s to %s anyway\n' "$seq" "$mark"
}

# --to-me is the reader's own choice about what to be interrupted for, and it
# narrows delivery and nothing else: what it filters out is counted and said on
# stderr, and the mark passes it, because a room that is busy and a room that is
# dead must not look the same.
to_me_wakes_only_for_what_names_this_principal() {
	recall
	local poster mark_before mark_after
	mark_before="$(inbox_mark gate-waiter)" || return 1
	api POST "$TOKEN_A_AGENT" "/api/chat/$INBOX_ROOM/say" \
		'{"body": "a remark to the room at large"}' || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1

	poster="$(say_to_in_background "$INBOX_ROOM" "$TOKEN_A_AGENT" "$USER_A" "this one is for you")"
	inbox_run --token "$TOKEN_A" --as gate-waiter --to-me --deadline 40
	wait "$poster" 2>/dev/null || true

	want_eq "exit code" "$INBOX_STATUS" 0 || return 1
	want_eq "what it woke for" \
		"$(printf '%s' "$INBOX_OUT" | jq -r .body)" "this one is for you" || return 1
	want_eq "addressed at" "$(printf '%s' "$INBOX_OUT" | jq -r .addressee)" "$USER_A" || return 1
	case "$INBOX_ERR" in
	*"to the room, not for you"*) ;;
	*)
		printf 'what it filtered out was not counted anywhere:\n%s\n' "$INBOX_ERR" >&2
		return 1
		;;
	esac
	mark_after="$(inbox_mark gate-waiter)" || return 1
	if [ "$mark_after" -le "$mark_before" ]; then
		printf 'the mark did not move past what was filtered out\n' >&2
		return 1
	fi
	printf 'woken by the addressed one, told about the rest, mark past both\n'
}

# The whole reason @mentions exist: a name written into the sentence has to wake
# a waiter exactly as `to` does, or the parse is decoration.
#
# Both directions, because either half alone is a feature that looks like it
# works. Woken by a mention of itself, and NOT woken by a mention of somebody
# else - a version that woke on any message with an @ in it would pass the first
# and make --to-me useless, which is the filter this whole thing rides on.
#
# The messages come from A's AGENT rather than from a person, deliberately: a
# person's message wakes a --to-me waiter whatever it says, so only an agent's
# can show that the mention did it.
a_mention_wakes_a_to_me_waiter() {
	recall
	local poster
	api POST "$TOKEN_A_AGENT" "/api/chat/$INBOX_ROOM/say" \
		"{\"body\": \"@$HANDLE_B could you take a look at this one\"}" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "it was addressed at B by the words" "$(jqv .addressee)" "$USER_B" || return 1

	inbox_run --token "$TOKEN_A" --as gate-waiter --to-me --deadline 6
	want_eq "exit code for a room where somebody else was named" "$INBOX_STATUS" 1 || return 1
	want_eq "and it handed nothing over" "$INBOX_OUT" "" || return 1

	poster="$(say_in_background "$INBOX_ROOM" "$TOKEN_A_AGENT" \
		"@$HANDLE_A the build is red, can you look")"
	inbox_run --token "$TOKEN_A" --as gate-waiter --to-me --deadline 40
	wait "$poster" 2>/dev/null || true

	want_eq "exit code when its own name was written" "$INBOX_STATUS" 0 || return 1
	want_eq "what it woke for" \
		"$(printf '%s' "$INBOX_OUT" | jq -r .body)" "@$HANDLE_A the build is red, can you look" ||
		return 1
	want_eq "addressed at" "$(printf '%s' "$INBOX_OUT" | jq -r .addressee)" "$USER_A" || return 1
	printf 'slept through @%s and woke on @%s, with no --to anywhere\n' "$HANDLE_B" "$HANDLE_A"
}

# A waiter that cannot tell a broken configuration from a quiet room cannot be
# restarted in a loop: the loop would spin forever on the broken one and say
# nothing. So everything that is not "somebody spoke" and not "the deadline
# passed" is 2.
a_broken_waiter_is_exit_2_and_not_exit_1() {
	recall
	local out
	inbox_run --token no-such-token --as gate-waiter --deadline 3
	want_eq "exit code with a token the node refuses" "$INBOX_STATUS" 2 || return 1

	mkdir -p "$WORK/no-config"
	INBOX_STATUS=0
	out="$(env -u FLOWY_TOKEN XDG_CONFIG_HOME="$WORK/no-config" \
		"$ROOT/flowy" inbox --url "http://127.0.0.1:$HTTP_PORT" --as gate-waiter 2>&1)" ||
		INBOX_STATUS=$?
	want_eq "exit code with no token anywhere" "$INBOX_STATUS" 2 || return 1
	case "$out" in
	*"no token"*) ;;
	*)
		printf 'it refused, but not for the reason it should have:\n%s\n' "$out" >&2
		return 1
		;;
	esac

	INBOX_STATUS=0
	out="$("$ROOT/flowy" inbox --url http://127.0.0.1:1 --token "$TOKEN_A" \
		--as gate-waiter --deadline 3 2>&1)" || INBOX_STATUS=$?
	want_eq "exit code when the node is not answering" "$INBOX_STATUS" 2 || return 1
	printf 'a bad token, no token and a dead node are all 2, never 1\n'
}

# --------------------------------------------------- what a listener CAN DO
#
# Hearing and waking are two things, and every surface on this node reported the
# first while somebody was asking about the second.
#
# A waiter forks a detached successor before it returns so the room stays heard
# while its agent reads. That successor polls, is attached, and CAN WAKE NOBODY
# - only a harness-tracked waiter exiting produces a notification. One night an
# agent sat deaf for 28 minutes behind a listener that was polling normally, a
# presence row seconds fresh, and a nag hook reporting healthy. The kind was
# written to a pid sidecar and nothing anybody looked at carried it.
#
# So: the waiter says which it is on every poll, the row keeps it, and the
# roster draws it. These check all three, and the browser one is registered with
# the other console checks.

# ROSTER_READERS are declared and polled by the presence check below and read
# again by the browser check, which is two processes and a phase apart - so the
# names are fixed here rather than generated.
ROSTER_TRACKED=roster-tracked
ROSTER_FORKED=roster-forked
ROSTER_QUIET=roster-quiet
# The seat that was armed and stopped. Stalled by the presence check below and
# still stalled when the browser check reads it, because being SAID OUT LOUD on
# the operator's own surface is the half of this that matters.
ROSTER_STOPPED=roster-went-quiet
readonly ROSTER_TRACKED ROSTER_FORKED ROSTER_QUIET ROSTER_STOPPED

# presence_kind NAME - what /api/presence says that listener can do. Not listed
# at all and listed with no kind are two different failures, and a jq default
# that turned the first into the second would send somebody looking in the
# wrong place.
presence_kind() {
	api GET "$TOKEN_A" /api/presence || return 1
	printf '%s' "$API_BODY" | jq -r --arg r "$1" \
		'[.listeners[] | select(.reader == $r)]
		 | if length == 0 then "<not listed>" else (.[0].waiter_kind // "<no kind on the row>") end'
}

# poll_as NAME [KIND] - one short poll of that reader's inbox, from a client
# that says what it is, or - with no KIND - from one that says nothing, which
# is every client written before this existed.
poll_as() {
	local path="/api/inbox/wait?as=$1&window=1"
	if [ -n "${2-}" ]; then
		path="$path&kind=$2"
	fi
	api GET "$TOKEN_A" "$path" || return 1
	want_eq "poll status for $1" "$API_STATUS" 200 || return 1
}

# The waiter itself says which kind it is, over the wire, on every poll. Checked
# through the binary rather than through curl because the marking is read from
# the environment by the process that polls, and a node that can record a kind
# nothing sends it is half a feature.
#
# A quiet deadline in a room nothing says anything in, on purpose: a delivery
# forks a successor, and the successor's own polls would then be the last word
# on the row - so the check would be reading a race instead of the run it made.
the_waiter_says_which_kind_it_is() {
	recall
	local quiet=nothing-is-said-in-this-room
	inbox_run --token "$TOKEN_A" --as kind-waiter --new --deadline 3 --room "$quiet"
	want_eq "a quiet deadline is still exit 1" "$INBOX_STATUS" 1 || return 1
	want_eq "what the node recorded for a harness-tracked waiter" \
		"$(scalar "SELECT waiter_kind FROM inbox_readers WHERE reader = 'kind-waiter'")" \
		tracked || return 1

	# And the successor, which is the one that costs something: same binary,
	# same name, same polling, nothing to wake.
	export FLOWY_WAITER_KIND=forked
	inbox_run --token "$TOKEN_A" --as kind-waiter --deadline 3 --room "$quiet"
	unset FLOWY_WAITER_KIND
	want_eq "the successor's quiet deadline" "$INBOX_STATUS" 1 || return 1
	want_eq "what the node recorded for the forked successor" \
		"$(scalar "SELECT waiter_kind FROM inbox_readers WHERE reader = 'kind-waiter'")" \
		forked || return 1
	printf 'the same waiter reported tracked, then forked, and the row followed it\n'
}

# /api/presence answers what each listener can do, and answers UNKNOWN rather
# than guessing. A row written before this field existed, or by a client that
# does not send one, is evidence of nothing - and tracked is the reading that
# cost 28 minutes.
presence_says_what_each_listener_can_do() {
	recall
	local reader
	for reader in "$ROSTER_TRACKED" "$ROSTER_FORKED" "$ROSTER_QUIET"; do
		api POST "$TOKEN_A" /api/inbox/reader "{\"as\": \"$reader\"}" || return 1
		want_eq "declaring $reader" "$API_STATUS" 200 || return 1
	done

	# Declared and never polled: nobody has claimed anything about it.
	want_eq "a reader that has never polled" "$(presence_kind "$ROSTER_TRACKED")" \
		unknown || return 1

	poll_as "$ROSTER_TRACKED" tracked || return 1
	poll_as "$ROSTER_FORKED" forked || return 1
	poll_as "$ROSTER_QUIET" || return 1
	want_eq "a listener that polled as tracked" "$(presence_kind "$ROSTER_TRACKED")" \
		tracked || return 1
	want_eq "a listener that polled as forked" "$(presence_kind "$ROSTER_FORKED")" \
		forked || return 1
	want_eq "a listener that said nothing" "$(presence_kind "$ROSTER_QUIET")" \
		unknown || return 1

	# It outlives the poll that set it. Both of those polls have already ended,
	# so this has been true once above; the second poll is the other half - the
	# next poll must not reset what the row says, or the roster would flicker
	# through "nobody knows" on every cycle of a perfectly good listener.
	poll_as "$ROSTER_FORKED" forked || return 1
	want_eq "after a second poll cycle" "$(presence_kind "$ROSTER_FORKED")" forked || return 1

	# And a client that invents one claims nothing: the value arrives on a query
	# parameter, so this is what stands between the roster and a state it has no
	# case for.
	poll_as "$ROSTER_QUIET" wide-awake-honest || return 1
	want_eq "a kind nobody can draw" "$(presence_kind "$ROSTER_QUIET")" unknown || return 1
	printf 'presence: tracked, forked, and unknown for everything that has not said\n'
}

# presence_field NAME FIELD - one field of that listener's row, or the marker
# for a reader the roster does not list at all. Not-listed has to be its own
# answer: a jq default would turn "the row is gone" into "the field is empty",
# and those send somebody looking in two different places.
presence_field() {
	api GET "$TOKEN_A" /api/presence || return 1
	printf '%s' "$API_BODY" | jq -r --arg r "$1" --arg f "$2" \
		'[.listeners[] | select(.reader == $r)]
		 | if length == 0 then "<not listed>" else (.[0][$f] | tostring) end'
}

# A SEAT THAT WAS ARMED AND STOPPED IS NAMED, NOT TIDIED AWAY.
#
# polls_in_flight only comes down when a handler returns, so a waiter killed
# mid-poll - or a decrement issued on a request context that had already been
# cancelled by the client going away - leaves the counter up with nobody behind
# it. The roster read used to take any positive counter as attached with no age
# test at all, so such a row said "attached, polling" for as long as the table
# lived. claude-glm sat like that for six hours and ho-test for thirty, and the
# operator asked twice why an agent was not answering.
#
# The fix is not a shorter list. It is that the node can tell the three states
# apart and SAYS which one this is: still polling, never polled yet, or armed
# and stopped. The last one keeps its place on the roster - with the time it was
# last heard from - because that is the fact somebody needs, and a version of
# this that merely dropped the row would have deleted the evidence the complaint
# was about.
presence_retires_a_seat_that_stopped_mid_poll() {
	recall
	api POST "$TOKEN_A" /api/inbox/reader "{\"as\": \"$ROSTER_STOPPED\"}" || return 1
	want_eq "declaring $ROSTER_STOPPED" "$API_STATUS" 200 || return 1

	# Declared and not yet polling: a waiter arming itself, which the roster is
	# where somebody watches.
	want_eq "a reader that has not polled yet" \
		"$(presence_field "$ROSTER_STOPPED" state)" starting || return 1

	poll_as "$ROSTER_STOPPED" tracked || return 1
	want_eq "a reader that just polled" \
		"$(presence_field "$ROSTER_STOPPED" state)" listening || return 1

	# The state a killed waiter leaves behind, written by hand because the pair
	# of calls that would produce it is exactly the pair that never ran.
	psql_do "UPDATE inbox_readers
	            SET last_poll_at = now() - interval '6 hours', polls_in_flight = 1
	          WHERE reader = '$ROSTER_STOPPED'" || return 1
	want_eq "six hours after it stopped, still holding a poll" \
		"$(presence_field "$ROSTER_STOPPED" state)" lost || return 1
	want_eq "and what it says about being attached" \
		"$(presence_field "$ROSTER_STOPPED" attached)" false || return 1
	# The timestamp is what makes "lost" actionable rather than a shrug.
	if [ "$(presence_field "$ROSTER_STOPPED" last_poll_at)" = null ]; then
		printf 'a lost seat carries no last poll, so nothing can say how long it has been deaf\n' >&2
		return 1
	fi

	# A second label, aged past the waiter's own deadline: by then it is a row
	# nobody cleaned up rather than a seat that just went deaf, and it goes.
	local old=roster-stopped-yesterday
	api POST "$TOKEN_A" /api/inbox/reader "{\"as\": \"$old\"}" || return 1
	want_eq "declaring $old" "$API_STATUS" 200 || return 1
	poll_as "$old" tracked || return 1
	psql_do "UPDATE inbox_readers
	            SET last_poll_at = now() - interval '30 hours', polls_in_flight = 1
	          WHERE reader = '$old'" || return 1
	want_eq "a poll abandoned 30 hours ago" \
		"$(presence_field "$old" state)" '<not listed>' || return 1

	# And a bookmark that has never polled does not ride its acks onto the
	# roster. The console keeps one of these per room to hold a human's unread
	# place; they ack every time somebody reads the room, which is what kept
	# three of them on the listening pane permanently - refreshed by the act of
	# looking at the page they were cluttering.
	local bookmark=console:roster-bookmark
	api POST "$TOKEN_A" /api/inbox/reader "{\"as\": \"$bookmark\"}" || return 1
	want_eq "declaring $bookmark" "$API_STATUS" 200 || return 1
	psql_do "UPDATE inbox_readers SET created = now() - interval '3 hours'
	          WHERE reader = '$bookmark'" || return 1
	# Past its own mark, or the ack moves nothing and stamps nothing - which
	# would make this pass without the row ever being touched.
	local mark
	mark="$(scalar "SELECT read_cursor + 1 FROM inbox_readers WHERE reader = '$bookmark'")" || return 1
	api POST "$TOKEN_A" /api/inbox/ack "{\"as\": \"$bookmark\", \"cursor\": $mark}" || return 1
	want_eq "acking the bookmark" "$API_STATUS" 200 || return 1
	want_eq "a bookmark that never polled, acked just now" \
		"$(presence_field "$bookmark" state)" '<not listed>' || return 1

	printf 'presence: polling, starting, and armed-then-stopped are three answers, and the stopped one keeps its place\n'
}

# ------------------------------------------------------------ per-room todos
#
# A todo panel inside the room, where the work is actually being agreed.
#
# The queue has been artifacts of type memory and kind todo for a while, and it
# was readable everywhere except the place it is decided: two agents and a
# person settle in #build what has to happen, and until the room grew a panel
# the settling lived in the messages. The room is a field on the item - in
# fields, beside as_of and supersedes on a report - and it is a filter and
# nothing else: same owner, same visibility, same permission filter in the same
# WHERE clause, and the todos written before the field existed carry no room and
# are on every page they were on yesterday. These checks are written around that
# last sentence, because a change that only handled room-tagged todos would pass
# every check about rooms and quietly empty the list that works today.

# The three titles the checks below file and then look for. Fixed strings rather
# than generated ones because the check that writes one and the check that reads
# it back are two processes, and because the console render check greps a
# painted page for them.
ROOM_TODO_BUILD="bench-test the gearbox before friday"
ROOM_TODO_GENERAL="rewrite the pruning notes"
ROOM_TODO_GLOBAL="a todo nobody raised in any room"
ROOM_TODO_MCP="marrowbone the room filter"
readonly ROOM_TODO_BUILD ROOM_TODO_GENERAL ROOM_TODO_GLOBAL ROOM_TODO_MCP

# The assignee checks get a room of their own, because what the browser one
# asserts is about the exact rows in the panel - a check reading whatever the
# rest of the run had left in #build or #general would be asserting about a
# queue that changes underneath it.
#
# Two todos: one nobody is carrying, and one written the way the whole queue was
# before the field existed, with OWNER as the first line of its body. The second
# is the discriminating one - a panel that can set an assignee and not override
# the OWNER line looks finished and puts the old name back.
ROOM_PLAN="plan"
PLAN_TODO_FREE="grease the mainspring"
PLAN_TODO_OWNED="strip the countershaft"
PLAN_OWNER="a-bench"
PLAN_TAKER="a-writer"
PLAN_SECOND="a-second"
readonly ROOM_PLAN PLAN_TODO_FREE PLAN_TODO_OWNED PLAN_OWNER PLAN_TAKER PLAN_SECOND

# And a room of its own for the hide-done check, for the same reason: it counts
# what the panel is withholding, and a count read off a room the rest of the run
# is still writing into is a number that moves while it is being asserted.
#
# One finished and one not. Both are needed - hiding everything and hiding
# nothing both pass a check that only looks at the finished one.
ROOM_HIDE="hidedone"
HIDE_TODO_DONE="rebush the crank pin"
HIDE_TODO_OPEN="reseat the intake valve"
readonly ROOM_HIDE HIDE_TODO_DONE HIDE_TODO_OPEN

# And a room of its own for the autofill flow, because that check RAISES a todo
# by typing into the panel and then names its carrier: it is the only console
# check that writes through the panel rather than seeding over the API, so it
# would otherwise leave a row in whatever room it was pointed at and move the
# counts the two checks above assert on.
ROOM_AUTOFILL="autofill"
AUTOFILL_TODO="regrind the pinion shoulder"
AUTOFILL_CARRIER="clarke"
readonly ROOM_AUTOFILL AUTOFILL_TODO AUTOFILL_CARRIER

# And a room of its own for the reply check, for a reason of its own: it asserts
# on the LAST two messages in the room and tabs from one to the next, so a room
# the rest of the run is still writing into would move the rows out from under
# it between the read and the gesture.
#
# Two messages, because the keyboard half of the check tabs from the control on
# one to the control on the next - a single row cannot show that the controls
# are in the tab order at all. The second is long: the span half drags a fixed
# distance across it and a body shorter than the drag would be cited whole,
# which is the failure that check exists to name.
ROOM_REPLY="replies"
REPLY_FIRST="the tension arm is rubbing on the sleeve"
REPLY_LAST="the coupling nut backed off overnight and the guard plate is scored right through"
readonly ROOM_REPLY REPLY_FIRST REPLY_LAST

# has_todo TITLE - whether the last /api/artifacts response listed that title.
has_todo() {
	printf '%s' "$API_BODY" | jq --arg t "$1" -e \
		'[.artifacts[] | select(.title == $t)] | length == 1' >/dev/null
}

# want_todos LIST_DESCRIPTION PRESENT... -- ABSENT... - asserts what the last
# artifacts response holds and what it does not. The absent half is the half
# that matters here: "the room's panel" is a claim about what is not in it.
want_todos() {
	local what=$1 title
	shift
	local absent=0
	for title in "$@"; do
		if [ "$title" = "--" ]; then
			absent=1
			continue
		fi
		if [ "$absent" -eq 0 ]; then
			if ! has_todo "$title"; then
				printf '%s does not list %q, and should:\n%s\n' \
					"$what" "$title" "$(printf '%s' "$API_BODY" | jq -r '.artifacts[].title')" >&2
				return 1
			fi
		elif has_todo "$title"; then
			printf '%s lists %q, and should not:\n%s\n' \
				"$what" "$title" "$(printf '%s' "$API_BODY" | jq -r '.artifacts[].title')" >&2
			return 1
		fi
	done
}

# Raising one is the point of the feature: a conversation becomes a plan without
# leaving the conversation. Two rows go in under one clock reading - the item,
# and one ordinary chat message in the room naming it in the artifact column the
# log already has - and the item keeps the id of the message it came out of,
# which is the link filing the same thing in another system loses.
a_todo_is_raised_out_of_a_message() {
	recall
	api POST "$TOKEN_A" /api/chat/build/say \
		'{"body": "the gearbox needs a bench test before we ship it"}' || return 1
	want_eq "the message that raises it" "$API_STATUS" 200 || return 1
	local message thread
	message="$(jqv .id)"
	thread="$(jqv .thread)"

	api POST "$TOKEN_A" /api/chat/build/todo \
		"$(jq -nc --arg t "$ROOM_TODO_BUILD" --arg m "$message" \
			'{title: $t, body: "OWNER: a-bench", message: $m}')" || return 1
	want_eq "raise status" "$API_STATUS" 200 || return 1
	want_eq "it is a todo" "$(jqv .item.kind)" todo || return 1
	want_eq "of the type the queue is" "$(jqv .item.type)" memory || return 1
	want_eq "raised open" "$(jqv .item.status)" todo || return 1
	# Read by the room, so written at the project's scope: filing the room's
	# plan where nobody else in the room can read it is the one outcome this
	# must not produce.
	want_eq "in the project" "$(jqv .item.project)" pa || return 1
	# project-only, not 'project': that value has always meant the project plus
	# whoever its grants reach, and the scope means what it says. See
	# store.VisibilityProjectOnly.
	want_eq "at the project's visibility" "$(jqv .item.visibility)" project-only || return 1
	want_eq "the room it was raised in" "$(jqv .item.fields.room)" build || return 1
	want_eq "and the message it came out of" "$(jqv .item.fields.message)" "$message" || return 1

	# The other half: the room says so, as an ordinary message.
	want_eq "the room heard about it" "$(jqv .event.room)" build || return 1
	want_eq "as a chat message" "$(jqv .event.type)" chat || return 1
	want_eq "naming the todo" "$(jqv .event.artifact)" "$(jqv .item.id)" || return 1
	want_eq "under the message it came out of" \
		"$(jqv '.event.parents | join(",")')" "$message" || return 1
	want_eq "in that conversation's thread" "$(jqv .event.thread)" "$thread" || return 1
	# AND THE MESSAGE NAMES THE ROW IN ITS OWN PROSE. The artifact column above
	# is the machine answer and every rendering is free to drop it; the body is
	# what a reader reads however they are reading. Without the id in here, the
	# only two ULIDs a reader had in hand were the message and its thread, and
	# both of them 404 at every row door.
	want_eq "the announcement names the row it raised" "$(jqv .event.body)" \
		"raised a todo $(jqv .item.id): $ROOM_TODO_BUILD" || return 1
	remember ROOM_TODO_ID "$(jqv .item.id)"
	remember ROOM_TODO_MESSAGE "$message"
	remember ROOM_TODO_EVENT "$(jqv .event.id)"
	remember ROOM_TODO_THREAD "$(jqv .event.thread)"
	printf 'todo %s raised in #build out of message %s\n' "$(jqv .item.id)" "$message"
}

# The other half of the same fix: the refusal a reader gets when they act on the
# WRONG id out of that notification. A message id and a thread id are ULIDs of
# the same shape as a row id, minted moments apart, so they share a long prefix
# and differ in a character or two - and a bare 404 against one reads as "the row
# is gone", which is what sent two agents looking for a deleted artifact on
# 2026-08-18. The refusal now says which id space the id came from and which row
# the message or thread is about, at the read door and at the claim door.
#
# The last assertion is the one that keeps this from being an existence oracle:
# the sentence is assembled out of this reader's own filtered reads, so it stops
# at whatever this reader could already have reached and never a row past it.
a_misread_id_says_which_space_it_came_from() {
	recall
	local diag="the row raised in it is $ROOM_TODO_ID"

	want_status 404 GET "$TOKEN_A" "/api/artifact/$ROOM_TODO_THREAD" || return 1
	want_eq "the read door diagnoses a thread id" "$(jqv .error)" \
		"no such artifact - that id names a chat thread, not a row; $diag" || return 1

	want_status 404 GET "$TOKEN_A" "/api/artifact/$ROOM_TODO_EVENT" || return 1
	want_eq "and tells a message from a thread" "$(jqv .error)" \
		"no such artifact - that id names a chat message, not a row; the row it is about is $ROOM_TODO_ID" ||
		return 1

	# The claim door, which is where the loss actually happened: an agent read
	# the notification, claimed the id it found, and was told there was no such
	# todo by a node that could see perfectly well which one it meant.
	want_status 404 POST "$TOKEN_A" "/api/work/$ROOM_TODO_THREAD/claim" || return 1
	want_eq "the claim door says it too" "$(jqv .error)" \
		"no such todo: $ROOM_TODO_THREAD - that id names a chat thread, not a row; $diag" || return 1

	# An id nothing was ever written under stays a bare 404: there is nothing to
	# diagnose and inventing a sentence would be worse than the silence.
	want_status 404 GET "$TOKEN_A" /api/artifact/01HNOSUCHROW00000000000000 || return 1
	want_eq "an id nothing answers to" "$(jqv .error)" "no such artifact" || return 1

	# AND THE SENTENCE STOPS AT THE PERMISSION BOUNDARY. B is in pb and holds a
	# project grant onto pa, so B genuinely reads the room's messages and is
	# rightly told the id is a thread - and the todo is project-only, which a
	# grant does not reach, so the half of the answer that would name a row B
	# cannot read is not said. The diagnosis is every read it is built from and
	# nothing more: it runs the asking principal's own filter, one clause at a
	# time. Compare TestADiagnosisTellsAStranger... in the store, where a reader
	# with no grant at all is told nothing whatsoever.
	want_status 404 GET "$TOKEN_B" "/api/artifact/$ROOM_TODO_THREAD" || return 1
	want_eq "a grantee is told the space and not the row" "$(jqv .error)" \
		"no such artifact - that id names a chat thread, not a row" || return 1
	printf 'thread %s diagnosed to row %s\n' "$ROOM_TODO_THREAD" "$ROOM_TODO_ID"
}

# The filter, and the case a room-shaped change gets wrong: a todo with no room
# is not in any room's panel and is in every list that did not ask for one.
a_room_is_a_filter_and_not_a_move() {
	recall
	api POST "$TOKEN_A" /api/chat/general/todo \
		"$(jq -nc --arg t "$ROOM_TODO_GENERAL" '{title: $t, body: "OWNER: a-writer"}')" || return 1
	want_eq "the second room's todo" "$API_STATUS" 200 || return 1
	want_eq "its room" "$(jqv .item.fields.room)" general || return 1
	# One filed the way the fourteen on the real node were: no room at all.
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$ROOM_TODO_GLOBAL" \
			'{type: "memory", kind: "todo", status: "todo", visibility: "project",
			  title: $t, body: "OWNER: ?"}')" || return 1
	want_eq "the roomless todo" "$API_STATUS" 200 || return 1

	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=todo&room=build" || return 1
	want_eq "the build panel" "$API_STATUS" 200 || return 1
	want_todos "#build's panel" "$ROOM_TODO_BUILD" -- \
		"$ROOM_TODO_GENERAL" "$ROOM_TODO_GLOBAL" || return 1

	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=todo&room=general" || return 1
	want_todos "#general's panel" "$ROOM_TODO_GENERAL" -- \
		"$ROOM_TODO_BUILD" "$ROOM_TODO_GLOBAL" || return 1

	# The discriminating case. Ask for no room and every todo is there, the ones
	# with a room and the ones without: the page that works today still works.
	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=todo" || return 1
	want_todos "the whole queue" \
		"$ROOM_TODO_BUILD" "$ROOM_TODO_GENERAL" "$ROOM_TODO_GLOBAL" || return 1
	printf 'build has its own, general has its own, and the roomless one is in neither and in the list\n'
}

# The room narrows and never widens: it is not a second visibility, and asking
# for a room somebody else's project talks in answers with what this principal
# could already read, which is nothing.
a_room_is_not_a_permission_axis() {
	recall
	api GET "$TOKEN_B" "/api/artifacts?type=memory&kind=todo&room=build" || return 1
	want_eq "B's read of #build's panel" "$API_STATUS" 200 || return 1
	want_eq "what B gets out of A's room" "$(hits)" 0 || return 1
	api GET "$TOKEN_B" "/api/artifacts?type=memory&kind=todo" || return 1
	want_todos "B's whole queue" -- "$ROOM_TODO_BUILD" "$ROOM_TODO_GENERAL" || return 1
	printf "the room filter hands B nothing it could not already read\n"
}

# A todo is raised out of a conversation in front of you, or out of none. An id
# is a guess anybody can make, and a message that is not there and one that is
# out of reach get the same answer - the answer a read of it would give.
a_todo_cannot_be_raised_out_of_a_message_you_cannot_read() {
	recall
	# Said in pc, which nobody holds a grant into. A's own messages are the
	# wrong test: pb holds a grant on pa, so B can legitimately read those and
	# raising a todo out of one is not a refusal - it is the feature.
	api POST "$TOKEN_A_PC" /api/chat/build/say \
		'{"body": "said in the project nobody has a grant into"}' || return 1
	want_eq "the unreachable message" "$API_STATUS" 200 || return 1
	local hidden
	hidden="$(jqv .id)"
	want_status 404 GET "$TOKEN_B" "/api/artifact/$hidden" || return 1

	want_status 404 POST "$TOKEN_B" /api/chat/build/todo \
		"$(jq -nc --arg m "$hidden" \
			'{title: "raised out of somebody elses room", message: $m}')" || return 1
	case "$(jqv .error)" in
	*"not one you can read"*) ;;
	*)
		printf 'it was refused, but not as an unreadable message: %s\n' "$(jqv .error)" >&2
		return 1
		;;
	esac
	# And nothing was written: a refused raise leaves no half of the pair.
	api GET "$TOKEN_B" "/api/artifacts?type=memory&kind=todo" || return 1
	want_todos "B's queue after the refusal" -- "raised out of somebody elses room" || return 1
	# An id that names nothing gets the same answer a row out of reach does,
	# which is the answer a read of either would give.
	want_status 404 POST "$TOKEN_A" /api/chat/build/todo \
		'{"title": "raised out of nothing at all", "message": "01HNOSUCHMESSAGE00000000"}' || return 1
	want_status 400 POST "$TOKEN_A" /api/chat/build/todo '{"body": "no title"}' || return 1
	printf 'refused, and no row: %s\n' "$(jqv .error)"
}

# The same field over MCP, which is where an agent writes one. mem_write takes
# the room and the message the way report_write takes as_of and supersedes, and
# todos narrows by it - with the unnarrowed call still answering with the whole
# queue, roomless items included.
mem_write_takes_a_room_and_todos_narrows_by_it() {
	recall
	want_tool mem_write "$TOKEN_A" "$(jq -nc --arg t "$ROOM_TODO_MCP" --arg m "$ROOM_TODO_MESSAGE" \
		'{title: $t, body: "OWNER: a-agent", scope: "project", kind: "todo",
		  room: "build", message: $m}')" || return 1
	want_eq "the room rode the write" "$(tv .item.fields.room)" build || return 1
	want_eq "and so did the message" "$(tv .item.fields.message)" "$ROOM_TODO_MESSAGE" || return 1
	local id
	id="$(tv .item.id)"

	# An update that says only "done" keeps the room it was raised in: a todo
	# does not leave its room by being finished.
	want_tool mem_write "$TOKEN_A" "{\"id\": \"$id\", \"status\": \"done\"}" || return 1
	want_eq "the room survived an update that did not restate it" \
		"$(tv .item.fields.room)" build || return 1
	want_eq "and so did the title" "$(tv .item.title)" "$ROOM_TODO_MCP" || return 1

	want_tool todos "$TOKEN_A" '{"room": "build"}' || return 1
	want_eq "the finished one is out of the room's outstanding work" \
		"$(printf '%s' "$TOOL_JSON" | jq "[.items[] | select(.id == \"$id\")] | length")" 0 || return 1
	want_eq "and the raised one is in it" \
		"$(printf '%s' "$TOOL_JSON" | jq --arg t "$ROOM_TODO_BUILD" \
			'[.items[] | select(.title == $t)] | length')" 1 || return 1
	want_eq "and the other room's is not" \
		"$(printf '%s' "$TOOL_JSON" | jq --arg t "$ROOM_TODO_GENERAL" \
			'[.items[] | select(.title == $t)] | length')" 0 || return 1

	# And with no room: the whole of the outstanding queue, which is what every
	# agent that has ever called this tool gets back.
	want_tool todos "$TOKEN_A" '{}' || return 1
	local title
	for title in "$ROOM_TODO_BUILD" "$ROOM_TODO_GENERAL" "$ROOM_TODO_GLOBAL"; do
		want_eq "todos with no room lists $title" \
			"$(printf '%s' "$TOOL_JSON" | jq --arg t "$title" \
				'[.items[] | select(.title == $t)] | length')" 1 || return 1
	done
	printf 'mem_write carries the room, todos narrows by it, and todos {} is still the whole queue\n'
}

# A room is one path segment on the chat routes, so a name that could not go
# back into a URL is not a room this node has, at either surface.
a_room_that_is_not_one_is_refused() {
	recall
	want_status 400 GET "$TOKEN_A" "/api/artifacts?type=memory&kind=todo&room=a%2Fb" || return 1
	want_tool_fails mem_write "$TOKEN_A" \
		'{"title": "wrong room", "kind": "todo", "scope": "project", "room": "a/b"}' \
		"is not one" || return 1
	# The raise takes its room from the path, where a slash cannot reach it -
	# so the bar that catches a name the panel could never ask for again is the
	# length one, and it is the same bar.
	local long
	long="$(printf 'r%.0s' $(seq 1 65))"
	want_status 400 POST "$TOKEN_A" "/api/chat/$long/todo" \
		'{"title": "raised into a room nothing can ask for"}' || return 1
}

# Who is carrying one, set from the room and then moved. The override is the
# half a write-once version gets wrong, and it is the ordinary case: work
# changes hands more often than it is first picked up.
#
# The todo this drives was raised with `OWNER: a-bench` as its body, so the
# first move also asserts the compatibility the whole feature rests on - the
# node reads that line as the assignee until a field says otherwise, and the
# sentence the room is told names it as the previous holder.
a_todo_takes_an_assignee_and_an_override() {
	recall
	local at="/api/chat/build/todo/$ROOM_TODO_ID/assignee"

	api GET "$TOKEN_A" "/api/artifact/$ROOM_TODO_ID" || return 1
	want_eq "no assignee field to begin with" "$(jqv .fields.assignee)" null || return 1

	api POST "$TOKEN_A" "$at" "$(jq -nc --arg a "$PLAN_TAKER" '{assignee: $a}')" || return 1
	want_eq "set status" "$API_STATUS" 200 || return 1
	want_eq "who has it" "$(jqv .item.fields.assignee)" "$PLAN_TAKER" || return 1
	# Saying who is carrying something is not re-filing it: where it was raised
	# and what it came out of are untouched.
	want_eq "still #build's" "$(jqv .item.fields.room)" build || return 1
	want_eq "still out of that message" \
		"$(jqv .item.fields.message)" "$ROOM_TODO_MESSAGE" || return 1
	# And the room heard, as an ordinary message in the thread the todo was
	# raised out of - the conversation that produced the plan is the one that
	# says who picked it up. The sentence names the OWNER line as who had it,
	# which is the compatibility, said out loud.
	want_eq "the room heard about it" "$(jqv .event.room)" build || return 1
	want_eq "as a chat message" "$(jqv .event.type)" chat || return 1
	want_eq "naming the todo" "$(jqv .event.artifact)" "$ROOM_TODO_ID" || return 1
	want_eq "and saying it changed hands" \
		"$(jqv .event.body)" "moved $ROOM_TODO_BUILD from $PLAN_OWNER to $PLAN_TAKER" || return 1

	# The override. A held row moves by naming its holder, so the override is a
	# handover: it says who it takes the work from.
	api POST "$TOKEN_A" "$at" \
		"$(jq -nc --arg a "$PLAN_SECOND" --arg e "$PLAN_TAKER" '{assignee: $a, expect: $e}')" || return 1
	want_eq "override status" "$API_STATUS" 200 || return 1
	want_eq "who has it now" "$(jqv .item.fields.assignee)" "$PLAN_SECOND" || return 1
	want_eq "and the handover is two names" \
		"$(jqv .event.body)" "moved $ROOM_TODO_BUILD from $PLAN_TAKER to $PLAN_SECOND" || return 1

	# Putting it down. An empty name is a value and not a silence: the key stays
	# on the item, saying nobody, which is what outranks the OWNER line still in
	# the body - the next set says "gave", not "moved from a-bench".
	api POST "$TOKEN_A" "$at" "$(jq -nc --arg e "$PLAN_SECOND" '{assignee: "", expect: $e}')" || return 1
	want_eq "unassign status" "$API_STATUS" 200 || return 1
	want_eq "nobody has it" "$(jqv .item.fields.assignee)" "" || return 1
	want_eq "said as a handover back" \
		"$(jqv .event.body)" "took $ROOM_TODO_BUILD off $PLAN_SECOND" || return 1
	# The words the queue has always used for nobody land in the same state, so
	# every surface has one word for it.
	api POST "$TOKEN_A" "$at" '{"assignee": "unassigned"}' || return 1
	want_eq "unassigned is nobody" "$(jqv .item.fields.assignee)" "" || return 1
	want_eq "and nothing changed hands" \
		"$(jqv .event.body)" "left $ROOM_TODO_BUILD unassigned" || return 1

	api POST "$TOKEN_A" "$at" "$(jq -nc --arg a "$PLAN_TAKER" '{assignee: $a}')" || return 1
	want_eq "the empty field outranked the OWNER line" \
		"$(jqv .event.body)" "gave $ROOM_TODO_BUILD to $PLAN_TAKER" || return 1

	# The ROOM hears it, not only whoever said it. An event with no project on
	# it is readable by its own actor and nobody else - see EventFilterSQL - so
	# a message announcing that the plan changed hands would be a message the
	# room never got, which is indistinguishable from the feature working from
	# everywhere except somebody else's screen.
	want_eq "the message is the project's" "$(jqv .event.project)" pa || return 1
	api GET "$TOKEN_OP" /api/chat/build || return 1
	want_eq "somebody else in the project heard it" \
		"$(printf '%s' "$API_BODY" | jq --arg b "gave $ROOM_TODO_BUILD to $PLAN_TAKER" \
			'[.events[] | select(.body == $b)] | length')" 1 || return 1

	# And it is on the list the panel reads, not only on the item.
	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=todo&room=build" || return 1
	want_eq "the panel's read of who has it" \
		"$(printf '%s' "$API_BODY" | jq -r --arg t "$ROOM_TODO_BUILD" \
			'.artifacts[] | select(.title == $t) | .fields.assignee')" "$PLAN_TAKER" || return 1
	printf 'set, moved twice, put down and picked up again, said in #build each time\n'
}

# What it refuses. A panel edits its own room's plan, a todo is what carries an
# assignee, and a name is a handle on one line.
an_assignee_is_refused_where_it_is_not_one() {
	recall
	# An id is a guess anybody can make, so #general's panel writing into
	# #build's queue - and announcing it in #general, where nobody in #build
	# would ever see it said - is not a thing this surface does.
	want_status 404 POST "$TOKEN_A" "/api/chat/general/todo/$ROOM_TODO_ID/assignee" \
		'{"assignee": "a-stranger"}' || return 1
	want_status 404 POST "$TOKEN_A" \
		"/api/chat/build/todo/01HNOSUCHTODO000000000000/assignee" \
		'{"assignee": "a-stranger"}' || return 1
	# Not a todo. What a bug's lifecycle means is a different question with a
	# different verb, and this one does not answer it. A bug filed here rather
	# than phase 1's, which has been deleted by now and would read as absent -
	# a 404 that says nothing about what this refuses on a live row.
	api POST "$TOKEN_A" /api/artifacts \
		'{"type": "bug", "title": "not a todo and not assignable", "status": "open"}' || return 1
	want_eq "the bug this refuses on" "$API_STATUS" 200 || return 1
	want_status 400 POST "$TOKEN_A" "/api/chat/build/todo/$(jqv .id)/assignee" \
		'{"assignee": "a-stranger"}' || return 1
	case "$(jqv .error)" in
	*"carries no assignee"*) ;;
	*)
		printf 'a bug was refused, but not for being one: %s\n' "$(jqv .error)" >&2
		return 1
		;;
	esac
	want_status 400 POST "$TOKEN_A" "/api/chat/build/todo/$ROOM_TODO_ID/assignee" \
		'{"assignee": "two\nlines"}' || return 1
	local long
	long="$(printf 'n%.0s' $(seq 1 65))"
	want_status 400 POST "$TOKEN_A" "/api/chat/build/todo/$ROOM_TODO_ID/assignee" \
		"$(jq -nc --arg a "$long" '{assignee: $a}')" || return 1
	# A todo out of reach reads as one that is not there, which is the answer a
	# read of it gives.
	want_status 404 POST "$TOKEN_B" "/api/chat/build/todo/$ROOM_TODO_ID/assignee" \
		'{"assignee": "a-stranger"}' || return 1
	# And none of that wrote anything.
	api GET "$TOKEN_A" "/api/artifact/$ROOM_TODO_ID" || return 1
	want_eq "whoever had it still has it" "$(jqv .fields.assignee)" "$PLAN_TAKER" || return 1
}

# Being given a piece of work is not being given a copy of it. The assignee is a
# name somebody wrote down - the node resolves it to no principal and the
# permission filter has never looked at that key - so naming somebody hands them
# nothing. The surface that DOES hand over a readable copy is an assignment: a
# share and a task and a thread written together, POST /api/assign, which is a
# different verb on purpose.
an_assignee_hands_the_named_party_nothing() {
	recall
	api POST "$TOKEN_A" "/api/chat/build/todo/$ROOM_TODO_ID/assignee" \
		"$(jq -nc --arg a "$HANDLE_B" --arg e "$PLAN_TAKER" '{assignee: $a, expect: $e}')" || return 1
	want_eq "named the other project's person" "$(jqv .item.fields.assignee)" "$HANDLE_B" || return 1

	want_status 404 GET "$TOKEN_B" "/api/artifact/$ROOM_TODO_ID" || return 1
	api GET "$TOKEN_B" "/api/artifacts?type=memory&kind=todo&room=build" || return 1
	want_eq "what B gets out of the room it was named in" "$(hits)" 0 || return 1
	api GET "$TOKEN_B" "/api/artifacts?type=memory&kind=todo" || return 1
	want_todos "B's queue after being named on A's todo" -- "$ROOM_TODO_BUILD" || return 1

	# Put it back, because the checks after this one read it.
	api POST "$TOKEN_A" "/api/chat/build/todo/$ROOM_TODO_ID/assignee" \
		"$(jq -nc --arg a "$PLAN_TAKER" --arg e "$HANDLE_B" '{assignee: $a, expect: $e}')" || return 1
	want_eq "handed back" "$(jqv .item.fields.assignee)" "$PLAN_TAKER" || return 1
	printf 'B was named on A todo and still reads none of it\n'
}

# The same field over MCP, which is where an agent says it. It rides fields
# beside the room, an update that does not restate it keeps it, and an empty one
# is a value rather than a silence.
mem_write_takes_an_assignee() {
	recall
	want_tool mem_write "$TOKEN_A" \
		"$(jq -nc --arg t "quarry the idler shaft" --arg a "$PLAN_TAKER" \
			'{title: $t, body: "OWNER: a-bench", scope: "project", kind: "todo",
			  room: "build", assignee: $a}')" || return 1
	want_eq "the assignee rode the write" "$(tv .item.fields.assignee)" "$PLAN_TAKER" || return 1
	want_eq "beside the room" "$(tv .item.fields.room)" build || return 1
	local id
	id="$(tv .item.id)"

	want_tool mem_write "$TOKEN_A" "{\"id\": \"$id\", \"status\": \"active\"}" || return 1
	want_eq "kept by an update that did not restate it" \
		"$(tv .item.fields.assignee)" "$PLAN_TAKER" || return 1

	# Nobody, said on purpose - and the OWNER line still in the body does not
	# quietly put the old name back.
	want_tool mem_write "$TOKEN_A" "{\"id\": \"$id\", \"assignee\": \"\"}" || return 1
	want_eq "put down over MCP" "$(tv .item.fields.assignee)" "" || return 1
	want_eq "with the body it came with untouched" "$(tv .item.body)" "OWNER: a-bench" || return 1

	want_tool_fails mem_write "$TOKEN_A" \
		"{\"id\": \"$id\", \"assignee\": \"two\nlines\"}" "is not a name" || return 1
}

# --------------------------------------------------------------- who raised it
#
# A row on this board has two parties and until now it carried one. owner_user
# is the seat whose TOKEN wrote the row - signed, load-bearing, and untouched by
# any of this - and for a queue four agents file into it is the agent that
# typed the line rather than the party the work came from. So an agent filing
# what the operator asked for in #general produced a row that reads as the
# agent's own idea, and the trail back to the ask was four messages up a
# conversation nobody rereads.
#
# The raiser is that second, weaker fact, beside the assignee and shaped like
# it: a handle, granting nothing, and set when the row is raised. The default is
# what makes it worth having - a todo raised out of a message takes the SPEAKER
# of that message, so the ask is recorded without anybody typing it - and the
# stated one is what makes it honest, for the agent filing on somebody's behalf
# out of no message at all.
#
# Its own room, for the reason the assignee checks have one: the browser check
# reads exact rows, and a room the rest of the run is still writing into is a
# queue that moves while it is being asserted.
ROOM_RAISED="raised"
RAISED_TODO="split the todo title from the body"
RAISED_STATED="file the gap in the deploy script"
RAISED_CARRIER="a-drainer"
RAISED_BEHALF="the-operator"
readonly ROOM_RAISED RAISED_TODO RAISED_STATED RAISED_CARRIER RAISED_BEHALF

# The default, at the door a room raises work through: the operator asks for
# something, ALICE files it, and the row says whose request it was without
# either of them saying so.
#
# Both names are asserted, and so is owner_user. A change that recorded the
# raiser by moving owner_user would pass a check that only looked at the new
# field, and it would be rewriting the one fact on this row that is inside the
# signature.
a_todo_says_who_raised_it() {
	recall
	api POST "$TOKEN_OP" "/api/chat/$ROOM_RAISED/say" \
		'{"body": "split todo title and body, record and show who raised it"}' || return 1
	want_eq "the ask" "$API_STATUS" 200 || return 1
	local message id
	message="$(jqv .id)"

	api POST "$TOKEN_A" "/api/chat/$ROOM_RAISED/todo" \
		"$(jq -nc --arg t "$RAISED_TODO" --arg m "$message" '{title: $t, message: $m}')" || return 1
	want_eq "raise status" "$API_STATUS" 200 || return 1
	want_eq "the work came from the seat that asked" \
		"$(jqv .item.fields.raiser)" "$HANDLE_OP" || return 1
	# And on the row itself, beside the assignee, so one read answers both
	# rather than each client digging into fields for one of them.
	want_eq "on the row beside the assignee" "$(jqv .item.raiser)" "$HANDLE_OP" || return 1
	want_eq "written by the seat that filed it" "$(jqv .item.owner_user)" "$USER_A" || return 1
	id="$(jqv .item.id)"
	remember RAISED_ID "$id"

	# The other name: somebody picks the work up. Two parties on one row is the
	# whole point - raised by the operator, carried by whoever drains it.
	api POST "$TOKEN_A" "/api/todo/$id/assignee" \
		"$(jq -nc --arg a "$RAISED_CARRIER" '{assignee: $a}')" || return 1
	want_eq "assign status" "$API_STATUS" 200 || return 1
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "raised by" "$(jqv .raiser)" "$HANDLE_OP" || return 1
	want_eq "carried by" "$(jqv .assignee)" "$RAISED_CARRIER" || return 1
	printf 'todo %s raised by %s, carried by %s\n' "$id" "$HANDLE_OP" "$RAISED_CARRIER"
}

# A stated raiser is the last word, a name that is not one is refused, and a row
# raised out of no conversation says nothing rather than guessing its author.
#
# The last of those is the one a lenient version gets wrong. Every todo on this
# board was written before the field, and answering owner_user for them would
# put a name nobody claimed on the whole queue at once - which reads exactly
# like a fact somebody stated.
a_stated_raiser_wins_and_nothing_is_guessed() {
	recall
	api POST "$TOKEN_OP" "/api/chat/$ROOM_RAISED/say" \
		'{"body": "and the deploy script skips the migration"}' || return 1
	local message
	message="$(jqv .id)"

	api POST "$TOKEN_A" "/api/chat/$ROOM_RAISED/todo" \
		"$(jq -nc --arg t "$RAISED_STATED" --arg m "$message" --arg r "$RAISED_BEHALF" \
			'{title: $t, message: $m, raiser: $r}')" || return 1
	want_eq "the stated raiser wins over the message speaker" \
		"$(jqv .item.fields.raiser)" "$RAISED_BEHALF" || return 1

	# Raised out of nothing, by a seat filing its own work: no raiser at all,
	# and the key is absent rather than empty - an empty one would be somebody
	# saying the work came from nobody, which is a claim and not a silence.
	api POST "$TOKEN_A" "/api/chat/$ROOM_RAISED/todo" \
		'{"title": "a line nobody asked for"}' || return 1
	want_eq "nothing is inferred from the author" "$(jqv .item.fields.raiser)" null || return 1
	want_eq "and the row says nobody" "$(jqv .item.raiser)" null || return 1

	want_status 400 POST "$TOKEN_A" "/api/chat/$ROOM_RAISED/todo" \
		'{"title": "a raiser that is a paragraph", "raiser": "two\nlines"}' || return 1
	printf 'stated wins, absent stays absent, a paragraph is refused\n'
}

# The same field over MCP, which is where an agent files a line it was asked
# for. It defaults from the raising message exactly as the room door does - one
# rule, two doors - and it is settled at the raise: an update that restates it
# is refused rather than quietly rewriting where the work came from.
mem_write_takes_a_raiser_and_settles_it() {
	recall
	api POST "$TOKEN_OP" "/api/chat/$ROOM_RAISED/say" \
		'{"body": "the tui queue should say this too"}' || return 1
	local message id
	message="$(jqv .id)"

	want_tool mem_write "$TOKEN_A" \
		"$(jq -nc --arg t "say it in the terminal client as well" --arg m "$message" \
			'{title: $t, scope: "project", kind: "todo", room: "raised", message: $m}')" || return 1
	want_eq "defaulted from the message speaker" "$(tv .item.fields.raiser)" "$HANDLE_OP" || return 1
	id="$(tv .item.id)"

	# The carrier rides the same write: a row nobody is carrying cannot be
	# active, and mem_write is the door that can say both in one statement. See
	# internal/store/queuecoherence.go.
	want_tool mem_write "$TOKEN_A" \
		"{\"id\": \"$id\", \"status\": \"active\", \"assignee\": \"a-raisedwork\"}" || return 1
	want_eq "kept by an update about something else" \
		"$(tv .item.fields.raiser)" "$HANDLE_OP" || return 1

	want_tool_fails mem_write "$TOKEN_A" \
		"{\"id\": \"$id\", \"raiser\": \"somebody-else\"}" "settled when it is raised" || return 1
	want_tool_fails mem_write "$TOKEN_A" \
		'{"title": "a note about who asked", "kind": "note", "raiser": "the-operator"}' \
		"is not in the queue" || return 1
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "unmoved by the refused update" "$(jqv .raiser)" "$HANDLE_OP" || return 1
}

# And both names on the screen, in a browser, on the row. A feature that is
# complete in the store and shows one of the two parties leaves a reader exactly
# where they were: unable to tell an agent's own idea from somebody's request.
browser_shows_who_raised_a_todo() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/raiser-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		pa "$RAISED_ID" "$RAISED_TODO" "$HANDLE_OP" "$RAISED_CARRIER"
}

# The room says which of its messages raised a row, and shows the row without
# leaving the conversation. Raised by the operator, who tapped a raise and got
# nothing: the id was on the event all along and the transcript never read it,
# so the failure looked like a dead control rather than a missing one.
browser_opens_the_row_a_message_raised() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/rowcard-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$ROOM_RAISED" "$RAISED_ID" "$RAISED_TODO"
}

# ------------------------------------------------------------ assignment, shared
#
# A todo is not the property of whoever typed it. One agent files the queue and
# the room drains it, so who is carrying an item is the one part of it that changes
# hands without asking the author - and READ permission is the whole bar, because
# the assignee is a name in fields that grants nobody anything.
#
# This is the surface a live node was missing, and the shape of the miss is why
# these checks are written the way they are: the only door was the room panel's, a
# queue filed by one agent read as carried by nobody from every other seat, and the
# operator asked three times why. So each check below drives a principal who did
# NOT write the todo.
#
# Its own room and its own titles: the item is handed between three seats here,
# which is not a state to leave in a room another check reads.
ROOM_HANDOUT="handout"
HANDOUT_TODO="pack the countershaft bearings"
HANDOUT_TAKER="a-drainer"
HANDOUT_SECOND="a-second-shift"
readonly ROOM_HANDOUT HANDOUT_TODO HANDOUT_TAKER HANDOUT_SECOND

# THE ONE THAT MATTERS. A principal who did not create a todo can assign it, at
# both doors, and the item stays its author's in every other respect.
#
# The operator goes first because that is the case that was impossible: another
# person in the project, holding no share of the item, handing work out. Then A's
# agent takes it over MCP, so the two doors are shown writing one answer that the
# other one reads back - which is the property that makes them one implementation
# rather than two that agree today.
a_todo_is_assigned_by_somebody_who_did_not_write_it() {
	recall
	api POST "$TOKEN_A" "/api/chat/$ROOM_HANDOUT/todo" \
		"$(jq -nc --arg t "$HANDOUT_TODO" '{title: $t}')" || return 1
	want_eq "raise status" "$API_STATUS" 200 || return 1
	local id
	id="$(jqv .item.id)"
	remember HANDOUT_ID "$id"
	want_eq "nobody is carrying it to begin with" "$(jqv .item.fields.assignee)" null || return 1

	# The operator: a second person in the project, who wrote none of this.
	api POST "$TOKEN_OP" "/api/todo/$id/assignee" \
		"$(jq -nc --arg a "$HANDOUT_TAKER" '{assignee: $a}')" || return 1
	want_eq "assign status" "$API_STATUS" 200 || return 1
	want_eq "who has it" "$(jqv .assignee)" "$HANDOUT_TAKER" || return 1
	# On the row as well as in the answer, because that key is where every panel,
	# the FUSE mount and the ready query all read it.
	want_eq "and the item says so" "$(jqv .item.fields.assignee)" "$HANDOUT_TAKER" || return 1
	# The rest of the item is still the author's. An assignment moves one key; a
	# door that rewrote the row would be the takeover this fabric refuses.
	want_eq "still its author's" "$(jqv .item.owner_user)" "$USER_A" || return 1
	want_eq "with the title its author gave it" "$(jqv .item.title)" "$HANDOUT_TODO" || return 1
	want_eq "still #handout's" "$(jqv .item.fields.room)" "$ROOM_HANDOUT" || return 1

	# What the AUTHOR reads, which is the half that fails when the entry hangs off
	# the wrong row: the value, and the queue answer a drainer acts on.
	api GET "$TOKEN_A" "/api/todo/$id/assignee" || return 1
	want_eq "the author's read of who has it" "$(jqv .assignee)" "$HANDOUT_TAKER" || return 1
	ready_row "$id" "$TOKEN_A" || return 1
	want_eq "and the queue says it too" "$(readyv .assignee)" "$HANDOUT_TAKER" || return 1

	# The second door, and a third seat: A's agent takes the work over MCP,
	# naming who they take it from - a held row moves by naming its holder.
	want_tool todo_assign "$TOKEN_A_AGENT" \
		"$(jq -nc --arg i "$id" --arg a "$HANDOUT_SECOND" --arg e "$HANDOUT_TAKER" \
			'{todo: $i, assignee: $a, expect: $e}')" || return 1
	want_eq "the claim over MCP" "$(tv .assignee)" "$HANDOUT_SECOND" || return 1
	api GET "$TOKEN_OP" "/api/todo/$id/assignee" || return 1
	want_eq "read back through the other door" "$(jqv .assignee)" "$HANDOUT_SECOND" || return 1
	printf 'the operator and an agent both owned somebody else todo, over both doors\n'
}

# Read permission is the bar, and it is a real bar. A principal who cannot read the
# todo is refused at both doors and told exactly what a read of the id would have
# told them - nothing about the row - and nothing moves.
#
# The last two are the same rule about what an id is allowed to reveal: an id that
# is not here and an id that is here and is not a queue item get one answer.
assigning_a_todo_you_cannot_read_is_refused() {
	recall
	local id="$HANDOUT_ID"

	want_status 404 POST "$TOKEN_B" "/api/todo/$id/assignee" \
		"$(jq -nc --arg a "$HANDLE_B" '{assignee: $a}')" || return 1
	want_tool_fails todo_assign "$TOKEN_B" \
		"$(jq -nc --arg i "$id" '{todo: $i, assignee: "b-agent"}')" "no such todo" || return 1

	# And whoever had it still has it. A refusal that wrote the value and then said
	# no would be this round's own failure, from the other end.
	api GET "$TOKEN_A" "/api/todo/$id/assignee" || return 1
	want_eq "whoever had it still has it" "$(jqv .assignee)" "$HANDOUT_SECOND" || return 1
	# jqv reads one expression and takes no jq arguments, so the seat being
	# counted is bound here rather than passed through it.
	want_eq "and nobody added an entry" \
		"$(printf '%s' "$API_BODY" | jq --arg b "$USER_B" \
			'[.log[] | select(.actor == $b)] | length')" 0 || return 1

	want_status 404 POST "$TOKEN_A" "/api/todo/01HNOSUCHTODO000000000000/assignee" \
		'{"assignee": "a-bench"}' || return 1
	# A bug is readable and is not a queue item: same answer, because naming an id
	# here is not a way to find out what else it might be. It is filed here rather
	# than phase 1's, which has been deleted by now and would answer 404 for being
	# gone - which says nothing about what this refuses on a live row.
	api POST "$TOKEN_A" /api/artifacts \
		'{"type": "bug", "title": "readable, and not a queue item", "status": "open"}' || return 1
	want_eq "the bug this refuses on" "$API_STATUS" 200 || return 1
	local bug
	bug="$(jqv .id)"
	want_status 200 GET "$TOKEN_A" "/api/artifact/$bug" || return 1
	want_status 404 POST "$TOKEN_A" "/api/todo/$bug/assignee" '{"assignee": "a-bench"}' || return 1
	printf 'B was refused at both doors and the todo did not move\n'
}

# claim_of ACTOR - the entry one seat left in the last /api/todo/{id}/assignee
# answer, and empty when that seat left none.
#
# The log is read by WHO made each claim rather than by position, because the two
# doors this todo was handed round by are two PROCESSES - `flowy serve` and
# `flowy mcp`, each holding a clock of its own - and which of two claims a
# millisecond apart sorts first is those two clocks' business rather than this
# surface's. That the latest claim wins and the ones before it stay is asked where
# it is one clock's question and has one answer: see the store check beside this
# one, TestTheLatestClaimWinsAndTheLogKeepsTheRest.
claim_of() {
	printf '%s' "$API_BODY" | jq -c --arg a "$1" 'first(.log[] | select(.actor == $a)) // empty'
}

# claimv ENTRY EXPR - a value out of one such entry.
claimv() { printf '%s' "$1" | jq -r "$2"; }

# said_when ENTRY - an entry has to say when it was made. A record of a handover
# that cannot answer "when" is half a record.
said_when() {
	case "$2" in
	"" | null)
		printf 'a claim does not say when it was made: %s\n' "$1" >&2
		return 1
		;;
	esac
}

# An assignment says WHO made it and WHEN, which is the whole reason it is an event
# and not a field write. A column records THAT something changed; the log says the
# operator handed this to a drainer at 09:12, and that an agent took it back after.
#
# The entries are the record and they append: an override does not erase the claim
# before it, so a queue that changed hands three times says so three times. Putting
# the work down is a claim too - the empty name is a value somebody chose.
an_assignment_records_who_made_it() {
	recall
	local id="$HANDOUT_ID" op agent

	api GET "$TOKEN_A" "/api/todo/$id/assignee" || return 1
	want_eq "both claims are in the log" "$(jqv '.log | length')" 2 || return 1
	op="$(claim_of "$USER_OP")"
	agent="$(claim_of "$AGENT_A")"
	if [ -z "$op" ] || [ -z "$agent" ]; then
		printf 'the log does not name both seats: %s\n' \
			"$(printf '%s' "$API_BODY" | jq -c .log)" >&2
		return 1
	fi
	want_eq "the operator handed it to" "$(claimv "$op" .assignee)" "$HANDOUT_TAKER" || return 1
	want_eq "as a person" "$(claimv "$op" .actor_kind)" user || return 1
	want_eq "and the agent took it" "$(claimv "$agent" .assignee)" "$HANDOUT_SECOND" || return 1
	want_eq "as an agent" "$(claimv "$agent" .actor_kind)" agent || return 1
	# The person behind the seat is on the row beside the seat, so "an agent claimed
	# this" and "somebody claimed this" are not the same answer.
	want_eq "acting for its person" "$(claimv "$agent" .actor_user)" "$USER_A" || return 1
	said_when "the operator's" "$(claimv "$op" .created)" || return 1
	said_when "the agent's" "$(claimv "$agent" .created)" || return 1
	# The standing claim is one of the ones that were actually made, and it says
	# when - a fold that named an entry nobody can find would be a claim with no
	# provenance behind it, which is the thing this log exists to prevent.
	want_eq "the standing claim is one of the ones in the log" \
		"$(printf '%s' "$API_BODY" | jq --arg e "$(jqv .assignment.entry)" \
			'[.log[] | select(.id == $e)] | length')" 1 || return 1
	said_when "the standing claim" "$(jqv .assignment.at)" || return 1

	# Putting it down, by a third seat again, naming who it is putting down.
	api POST "$TOKEN_OP" "/api/todo/$id/assignee" \
		"$(jq -nc --arg e "$HANDOUT_SECOND" '{assignee: "unassigned", expect: $e}')" || return 1
	want_eq "nobody has it" "$(jqv .assignee)" "" || return 1
	want_eq "and the claim says nobody, rather than saying nothing" \
		"$(jqv .assignment.assignee)" "" || return 1
	want_eq "made by the operator" "$(jqv .assignment.by)" "$USER_OP" || return 1

	# The author's own write leaves an entry as well. A value that sometimes has a
	# claim behind it and sometimes does not is a log that cannot answer the
	# question it exists for, so mem_write appends one in the same transaction.
	want_tool mem_write "$TOKEN_A" \
		"$(jq -nc --arg i "$id" --arg a "$HANDOUT_TAKER" '{id: $i, assignee: $a}')" || return 1
	api GET "$TOKEN_A" "/api/todo/$id/assignee" || return 1
	want_eq "four claims now" "$(jqv '.log | length')" 4 || return 1
	# An override APPENDS: each of the four names is still in the log with the seat
	# that put it there, and the author's own write is one of them.
	want_eq "the author left one too" "$(claimv "$(claim_of "$USER_A")" .assignee)" \
		"$HANDOUT_TAKER" || return 1
	want_eq "and putting it down is still in the log" \
		"$(printf '%s' "$API_BODY" | jq '[.log[] | select(.assignee == "")] | length')" 1 || return 1
	want_eq "naming who they gave it to" "$(jqv .assignee)" "$HANDOUT_TAKER" || return 1

	# And an entry is minted: the refusals that make it worth reading are on the
	# verb, so one handed in through the generic event door would be a handover
	# nobody made, about work the writer may not even be able to see.
	want_status 403 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg i "$id" '{type: "todo.assign", room: "handout", artifact: $i,
		   body: "assigned to a-forger", meta: {assignee: "a-forger"}}')" || return 1
	api GET "$TOKEN_A" "/api/todo/$id/assignee" || return 1
	want_eq "so the forged claim is not in the log" "$(jqv '.log | length')" 4 || return 1
	printf 'four claims by four seats, each saying who and when; a forged one refused\n'
}

# THE OTHER HALF OF THIS ROUND. An update this principal may not make FAILS, and
# fails where a caller cannot miss it.
#
# mem_write on somebody else's item used to come back as a JSON-RPC RESULT with
# isError set - which at the transport is a 200 with a normal-looking envelope - so
# nine calls in a row "succeeded" and wrote nothing while the operator went looking
# for what was wrong with the ids. A refusal that reports success is
# indistinguishable from success, and this is the check that says so: the answer
# has to be a protocol ERROR with a code, the way POST /api/artifact/{id}/delete
# answers 403, and the row has to be untouched afterwards.
#
# It also has to name the door that DOES work, because half of what went wrong was
# that the thing being attempted is allowed - an item's words are its author's, and
# who is carrying it is not.
mem_write_refuses_an_update_it_will_not_make() {
	recall
	local id="$HANDOUT_ID" was body
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	was="$(jqv .fields.assignee)"
	body="$(jqv .body)"

	mcp tools/call "$TOKEN_OP" \
		"$(jq -nc --arg i "$id" '{name: "mem_write",
		   arguments: {id: $i, assignee: "operator", body: "rewritten by somebody else"}}')" || return 1
	want_eq "no result envelope at all" "$(rv '.result // "none"')" none || return 1
	want_eq "a protocol error with a code" "$(rv .error.code)" -32003 || return 1
	case "$(rv .error.message)" in
	*"belongs to somebody else"*) ;;
	*)
		printf 'the refusal does not say whose item it is: %s\n' "$(rv .error.message)" >&2
		return 1
		;;
	esac
	case "$(rv .error.message)" in
	*todo_assign*) ;;
	*)
		printf 'the refusal does not name the door that works: %s\n' "$(rv .error.message)" >&2
		return 1
		;;
	esac

	# Nothing moved, which is the second half: a refusal that wrote half of it
	# would be worse than the silence.
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "the assignee it had" "$(jqv .fields.assignee)" "$was" || return 1
	want_eq "and the body its author wrote" "$(jqv .body)" "$body" || return 1

	# The whole update path, not only the assignee field: a title-only edit of
	# somebody else's item is refused the same way.
	mcp tools/call "$TOKEN_OP" \
		"$(jq -nc --arg i "$id" '{name: "mem_write",
		   arguments: {id: $i, title: "retitled by a stranger"}}')" || return 1
	want_eq "a title edit is refused the same way" "$(rv .error.code)" -32003 || return 1
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "with the title untouched" "$(jqv .title)" "$HANDOUT_TODO" || return 1

	# And the same write over HTTP, where the code has always been the 403 delete
	# answers with. The two doors onto one write path refuse together.
	want_status 403 POST "$TOKEN_OP" /api/artifacts \
		"$(jq -nc --arg i "$id" '{id: $i, type: "memory", kind: "todo",
		   title: "retitled by a stranger"}')" || return 1
	want_status 403 POST "$TOKEN_OP" "/api/artifact/$id/delete" || return 1
	printf 'refused as an error with a code, twice, and the item is as its author left it\n'
}

# --------------------------------------------------------------- a todo finishes
#
# The other half of the same ruling. Assignment was made collaborative and
# COMPLETION was left private, and the shape of what that cost is why these
# checks are written the way they are: an agent built and deployed a pane, the
# row had been raised by somebody else, and the agent got 403 at one door and
# "belongs to somebody else" at the other - so finished work went on advertising
# itself as open and the queue produced five duplicated builds in a day.
#
# STATUS IS A CLAIM ABOUT THE WORK AND NOT ABOUT THE TEXT, so read permission is
# the whole bar, exactly as it is for who is carrying the item. Every check below
# drives a principal who did NOT raise the todo, and the last one is the pair
# that says what changes hands and what does not: the same seat, the same item,
# refused on the body and allowed on the status.
#
# Its own room and its own title, because the item is closed, reopened and closed
# again here, which is not a state to leave in a room another check reads.
ROOM_CLOSE="closing"
CLOSE_TODO="ship the linear-thread pane"
CLOSE_BODY="OWNER: a-bench"
readonly ROOM_CLOSE CLOSE_TODO CLOSE_BODY

# THE ONE THAT MATTERS. A principal who did not raise a todo can move it through
# its lifecycle, at both doors, and the item stays its author's in every other
# respect.
#
# The status route goes first because that is the door that could not do it at
# all: it answered "a memory has no lifecycle; bug, feature, note and task do"
# for the one artifact whose entire purpose is to be finished. Then the operator
# closes it over MCP, so the two doors are shown writing one answer that the
# other reads back - the property that makes them one implementation rather than
# two that agree today.
a_todo_is_closed_by_somebody_who_did_not_write_it() {
	recall
	api POST "$TOKEN_A" "/api/chat/$ROOM_CLOSE/todo" \
		"$(jq -nc --arg t "$CLOSE_TODO" --arg b "$CLOSE_BODY" '{title: $t, body: $b}')" || return 1
	want_eq "raise status" "$API_STATUS" 200 || return 1
	local id
	id="$(jqv .item.id)"
	remember CLOSE_ID "$id"
	want_eq "raised open" "$(jqv .item.status)" todo || return 1

	# The operator: a second person in the project, who wrote none of this and
	# cannot rewrite a word of it.
	api POST "$TOKEN_OP" "/api/artifact/$id/status" '{"status": "active"}' || return 1
	want_eq "picked up" "$API_STATUS" 200 || return 1
	want_eq "the row moved" "$(jqv .artifact.status)" active || return 1
	want_eq "and it left an entry" "$(jqv .event.type)" todo.status || return 1
	want_eq "naming both ends" "$(jqv .event.body)" "todo->active" || return 1

	# The second door: mem_write with nothing on it but the status. This is the
	# call that answered "belongs to somebody else" for everybody but the author.
	want_tool mem_write "$TOKEN_OP" "{\"id\": \"$id\", \"status\": \"done\"}" || return 1
	want_eq "closed over MCP" "$(tv .item.status)" "done" || return 1

	# What the AUTHOR reads. A closure moves one column and takes nothing over.
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "the author read of where it is" "$(jqv .status)" "done" || return 1
	want_eq "still its author item" "$(jqv .owner_user)" "$USER_A" || return 1
	want_eq "with the title its author gave it" "$(jqv .title)" "$CLOSE_TODO" || return 1
	want_eq "and the body its author wrote" "$(jqv .body)" "$CLOSE_BODY" || return 1
	printf 'the operator finished work they did not file, over both doors\n'
}

# Read permission is the bar, and it is a real bar. A principal who cannot read
# the todo is refused at both doors and told exactly what a read of the id would
# have told them - nothing about the row - and nothing moves.
#
# The last two are the caller mistakes that have to be loud rather than quiet: an
# id that is not here, and a word that is not a status. A queue holding
# "finished" beside "done" is a queue where half the dependencies are satisfied
# and nothing says why.
closing_a_todo_you_cannot_read_is_refused() {
	recall
	local id="$CLOSE_ID"

	want_status 404 POST "$TOKEN_B" "/api/artifact/$id/status" '{"status": "todo"}' || return 1
	want_tool_fails mem_write "$TOKEN_B" "{\"id\": \"$id\", \"status\": \"todo\"}" \
		"no such memory item" || return 1

	want_status 404 POST "$TOKEN_A" "/api/artifact/01HNOSUCHTODO000000000000/status" \
		'{"status": "done"}' || return 1
	want_status 400 POST "$TOKEN_OP" "/api/artifact/$id/status" '{"status": "finished"}' || return 1
	case "$API_BODY" in
	*"active, todo, done"*) ;;
	*)
		printf 'the refusal does not say what a status may be: %s\n' "$API_BODY" >&2
		return 1
		;;
	esac
	want_tool_fails mem_write "$TOKEN_OP" "{\"id\": \"$id\", \"status\": \"finished\"}" \
		"is not a status a queue item has" || return 1

	# And it is still where it was. A refusal that wrote the status and then said
	# no would be this round own failure, from the other end.
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "still where it was" "$(jqv .status)" "done" || return 1
	api GET "$TOKEN_A" "/api/artifact/$id/history" || return 1
	want_eq "and nobody added an entry" \
		"$(printf '%s' "$API_BODY" | jq --arg b "$USER_B" \
			'[.events[] | select(.actor == $b)] | length')" 0 || return 1
	printf 'B was refused at both doors and the queue did not move\n'
}

# move_of FROM TO - the entry in the last history answer for that one move, and
# a failure when the trail does not hold it.
#
# The trail is read by WHICH MOVE each entry is rather than by position, for the
# reason claim_of is read by actor: the doors this todo was moved through are two
# PROCESSES - `flowy serve` and `flowy mcp`, each holding a clock of its own - so
# which of two moves a millisecond apart sorts first is those two clocks'
# business rather than this surface's. That the entries append and the latest
# wins is asked where it is one clock's question and has one answer: see the
# store check beside this one, TestAClosedTodoIsReopenedAndTheLogKeepsBoth.
#
# Every move this todo makes is a distinct pair, which is what makes the pair an
# address: picked up (todo->active), finished (active->done), reopened
# (done->todo), finished again (todo->done).
move_of() {
	local entry
	entry="$(printf '%s' "$API_BODY" | jq -c --arg f "$1" --arg t "$2" \
		'first(.events[] | select(.meta.from == $f and .meta.status == $t)) // empty')"
	if [ -z "$entry" ]; then
		printf 'the trail holds no %s->%s move:\n%s\n' "$1" "$2" \
			"$(printf '%s' "$API_BODY" | jq -c '[.events[].body]')" >&2
		return 1
	fi
	printf '%s' "$entry"
}

# A closure says WHO made it and WHEN, which is the whole reason it is an event
# and not a field write. A column records THAT something changed; the log says
# the operator picked this up and an agent closed it, and that somebody reopened
# it afterwards because it was not done after all.
#
# The trail is the queue own vocabulary, not the issue workflow. Reading the
# other type here would answer "no moves" for a todo that has been closed twice,
# which reads exactly like a todo nobody has touched.
a_closure_records_who_made_it_and_a_reopen_appends() {
	recall
	local id="$CLOSE_ID" pickup closure reopen refinish

	api GET "$TOKEN_A" "/api/artifact/$id/history" || return 1
	want_eq "the trail is the queue own" \
		"$(jqv '[.events[].type] | unique | join(",")')" todo.status || return 1
	want_eq "both moves are in it" "$(jqv '.events | length')" 2 || return 1
	want_eq "where it is now" "$(jqv .status)" "done" || return 1
	# Nothing is terminal here: work that was called done and was not is reopened
	# rather than refiled, and the console draws that list rather than keeping its
	# own copy of the rule.
	want_eq "and where it may go from there" "$(jqv '.next | join(",")')" "active,todo" || return 1

	pickup="$(move_of todo "active")" || return 1
	closure="$(move_of active "done")" || return 1
	want_eq "picked up by the operator" "$(claimv "$pickup" .meta.actor_user)" "$USER_OP" || return 1
	want_eq "as a person" "$(claimv "$pickup" .meta.actor_kind)" user || return 1
	said_when "the pick-up" "$(claimv "$pickup" .created)" || return 1
	want_eq "closed by the operator too" "$(claimv "$closure" .meta.actor_user)" "$USER_OP" || return 1
	said_when "the closure" "$(claimv "$closure" .created)" || return 1

	# Reopened. This is the case a lifecycle with a terminal state cannot express,
	# and the one the room asked for by name.
	want_tool mem_write "$TOKEN_OP" "{\"id\": \"$id\", \"status\": \"todo\"}" || return 1
	want_eq "open again" "$(tv .item.status)" todo || return 1
	# And the author closing their own item leaves an entry as well: a status that
	# sometimes has a claim behind it and sometimes does not is a log that cannot
	# answer the question it exists for.
	want_tool mem_write "$TOKEN_A" "{\"id\": \"$id\", \"status\": \"done\"}" || return 1

	api GET "$TOKEN_A" "/api/artifact/$id/history" || return 1
	want_eq "four moves now" "$(jqv '.events | length')" 4 || return 1
	want_eq "and it is closed again" "$(jqv .status)" "done" || return 1
	reopen="$(move_of "done" todo)" || return 1
	refinish="$(move_of todo "done")" || return 1
	want_eq "reopened by the operator" "$(claimv "$reopen" .meta.actor_user)" "$USER_OP" || return 1
	want_eq "and the author own write is in the trail too" \
		"$(claimv "$refinish" .meta.actor_user)" "$USER_A" || return 1
	# An override APPENDS: the first closure is still there with the seat that
	# made it, so "this was called done on friday" stays answerable.
	want_eq "both closures are in it" \
		"$(printf '%s' "$API_BODY" | jq '[.events[] | select(.meta.status == "done")] | length')" \
		2 || return 1

	# And an entry is minted: the refusal that makes it worth reading is on the
	# verb, so one handed in through the generic event door would be a closure
	# nobody made about work that never moved.
	want_status 403 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg i "$id" --arg r "$ROOM_CLOSE" '{type: "todo.status", room: $r,
		   artifact: $i, body: "done->todo", meta: {status: "todo", from: "done"}}')" || return 1
	api GET "$TOKEN_A" "/api/artifact/$id/history" || return 1
	want_eq "so the forged move is not in the trail" "$(jqv '.events | length')" 4 || return 1
	printf 'four moves by three seats, each saying who and when; a forged one refused\n'
}

# THE PAIR THAT SAYS WHERE THE LINE IS. The same seat, the same item: refused on
# the words, allowed on the queue metadata.
#
# A stranger may say "this is done"; they may not rewrite what you wrote. And a
# write that states both is refused ENTIRELY rather than half-applied - a success
# envelope that changed something other than what it was asked to is the same lie
# as one that changed nothing, and this project has paid for that lie seven times
# in a day.
a_stranger_may_close_a_todo_and_may_not_rewrite_it() {
	recall
	local id="$CLOSE_ID"

	mcp tools/call "$TOKEN_OP" \
		"$(jq -nc --arg i "$id" '{name: "mem_write",
		   arguments: {id: $i, status: "todo", body: "rewritten by somebody else"}}')" || return 1
	want_eq "no result envelope at all" "$(rv '.result // "none"')" none || return 1
	want_eq "a protocol error with a code" "$(rv .error.code)" -32003 || return 1
	case "$(rv .error.message)" in
	*"body is not yours to change"*) ;;
	*)
		printf 'the refusal does not say what it refused: %s\n' "$(rv .error.message)" >&2
		return 1
		;;
	esac

	# Neither half landed, including the half that would have been allowed on its
	# own. Nothing here ever writes part of what it was asked for.
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "the body its author wrote" "$(jqv .body)" "$CLOSE_BODY" || return 1
	want_eq "and the status it had" "$(jqv .status)" "done" || return 1

	want_tool_fails mem_write "$TOKEN_OP" \
		"$(jq -nc --arg i "$id" '{id: $i, title: "retitled by a stranger"}')" \
		"not yours to change" || return 1
	# A write that names nothing it is allowed to move is an error rather than a
	# success that changed nothing.
	want_tool_fails mem_write "$TOKEN_OP" "{\"id\": \"$id\"}" \
		"which piece of the queue metadata" || return 1

	# And the discriminating half: the status alone, by the same seat that was
	# just refused, goes through.
	want_tool mem_write "$TOKEN_OP" "{\"id\": \"$id\", \"status\": \"active\"}" || return 1
	want_eq "the stranger moved the queue metadata" "$(tv .item.status)" active || return 1
	want_eq "and left the words alone" "$(tv .item.title)" "$CLOSE_TODO" || return 1
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "the title its author gave it" "$(jqv .title)" "$CLOSE_TODO" || return 1
	want_eq "the body its author wrote" "$(jqv .body)" "$CLOSE_BODY" || return 1
	printf 'a stranger finished the work and could not rewrite a word of it\n'
}

# ACTIVE AND UNOWNED IS NOT A STATE A ROW CAN HOLD, at any door.
#
# The status moved through POST /api/artifact/{id}/status and the assignee
# through POST /api/todo/{id}/assignee, and neither door knew the other existed:
# setting active left the row carried by nobody, releasing a claim left it
# saying work was in flight. The board drew both, because both were on the row.
#
# The refusal is at the write rather than at the read, so the pair is
# unrepresentable rather than repaired afterwards - see
# internal/store/queuecoherence.go. This check drives every door that can write
# a queue row's status: the status route, the room's raise, POST /api/artifacts
# and mem_write. The discriminating halves are the ones that must still WORK -
# taking a row and then starting it, and putting one down - because a rule that
# refused those would be a queue nobody can drain.
active_and_unowned_is_refused_at_every_door() {
	recall
	local id
	api POST "$TOKEN_A" "/api/chat/$ROOM_CLOSE/todo" \
		'{"title": "regrind the escapement"}' || return 1
	want_eq "raise status" "$API_STATUS" 200 || return 1
	id="$(jqv .item.id)"
	want_eq "raised carried by nobody" "$(jqv '.item.assignee // ""')" "" || return 1

	# THE DOOR THAT MADE THE BAD PAIR. It is the caller's mistake - 400, not a
	# node reporting itself broken - and the sentence says what to do instead.
	want_status 400 POST "$TOKEN_OP" "/api/artifact/$id/status" \
		'{"status": "active"}' || return 1
	case "$(jqv .error)" in
	*"with nobody carrying it"*) ;;
	*)
		printf 'the refusal does not say what it refused: %s\n' "$(jqv .error)" >&2
		return 1
		;;
	esac
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "and the row did not move" "$(jqv .status)" todo || return 1

	# Taken, then started. Two writes, and now the pair is true.
	api POST "$TOKEN_OP" "/api/todo/$id/assignee" '{"assignee": "a-escapement"}' || return 1
	want_eq "taken" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_OP" "/api/artifact/$id/status" '{"status": "active"}' || return 1
	want_eq "and now it may say it is in flight" "$API_STATUS" 200 || return 1
	want_eq "in flight" "$(jqv .artifact.status)" active || return 1

	# PUTTING IT DOWN MOVES BOTH FACTS. It is not refused - an agent that cannot
	# hand work back holds it forever - it returns the row to the queue, and the
	# move is in the trail rather than only on the row.
	api POST "$TOKEN_OP" "/api/todo/$id/assignee" \
		"$(jq -nc '{assignee: "", expect: "a-escapement"}')" || return 1
	want_eq "put down" "$API_STATUS" 200 || return 1
	want_eq "carried by nobody again" "$(jqv .assignee)" "" || return 1
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "and back in the queue" "$(jqv .status)" todo || return 1
	api GET "$TOKEN_A" "/api/artifact/$id/history" || return 1
	want_eq "the release is a move somebody can read" \
		"$(jqv '[.events[] | select(.body == "active->todo")] | length')" 1 || return 1

	# The other two HTTP doors that take a status. The room's raise has never
	# been able to name a carrier at all; POST /api/artifacts writes both columns
	# in one statement and so can say both.
	want_status 400 POST "$TOKEN_A" "/api/chat/$ROOM_CLOSE/todo" \
		'{"title": "raised as work nobody is doing", "status": "active"}' || return 1
	want_status 400 POST "$TOKEN_A" /api/artifacts \
		'{"type": "memory", "kind": "todo", "status": "active", "visibility": "project",
		  "title": "filed as in flight by nobody"}' || return 1
	api POST "$TOKEN_A" /api/artifacts \
		'{"type": "memory", "kind": "todo", "status": "active", "visibility": "project",
		  "title": "filed as in flight, by a-escapement",
		  "fields": {"assignee": "a-escapement"}}' || return 1
	want_eq "with a carrier on it, it lands" "$API_STATUS" 200 || return 1

	# And mem_write, which writes the whole row in one statement: refused when it
	# states the half that cannot stand alone, taken when it states both.
	want_tool_fails mem_write "$TOKEN_OP" "{\"id\": \"$id\", \"status\": \"active\"}" \
		"with nobody carrying it" || return 1
	want_tool mem_write "$TOKEN_OP" \
		"{\"id\": \"$id\", \"status\": \"active\", \"assignee\": \"a-escapement\"}" || return 1
	want_eq "both facts in one write" "$(tv .item.status)" active || return 1
	want_eq "and it says who" "$(tv .item.fields.assignee)" a-escapement || return 1
	# The same put-down over MCP, where the caller said nothing about the status:
	# the queue moves it, rather than refusing a contradiction nobody typed.
	want_tool mem_write "$TOKEN_OP" \
		"{\"id\": \"$id\", \"assignee\": \"\", \"expect\": \"a-escapement\"}" || return 1
	want_eq "put down over MCP" "$(tv .item.fields.assignee)" "" || return 1
	want_eq "and the row came back with it" "$(tv .item.status)" todo || return 1
	printf 'active with nobody on it is refused at four doors, and putting work down returns the row\n'
}

# ------------------------------------------------- what kind of work a todo is
#
# THE THIRD PIECE OF QUEUE METADATA, on the same terms as the other two, and one
# property none of the others have: THE SET IS CLOSED AND IT REFUSES.
#
# The queue has always carried free labels - tags, many per item, anybody's word
# for anything - and they are right for what they are. What they cannot do is be
# counted or routed: "how much of this is bugs" over a tag column answers
# whatever the last agent felt like typing, and bug/bugs/defect/broken are four
# populations that all look like confident answers. So a todo also carries ONE
# word out of four, and a fifth word is an ERROR rather than a row nothing can
# act on.
#
# It is `category` on the wire and "Kind" on screen. A todo already IS kind=todo
# one level up, and the same word meaning two things on one row is precisely the
# defect that cost this room three misreadings in one day.
#
# Every check below drives a principal who did NOT raise the todo, because that
# is the ruling: what kind of work something is, is a claim about the WORK - the
# seat that picked the row up and found a bug underneath is usually not the seat
# that typed the title.
#
# Its own room and its own titles: the item is reclassified three times here,
# which is not a state to leave in a room another check reads.
ROOM_KIND="filing"
KIND_TODO="the pane loses the scroll position"
KIND_PLAIN="a todo nobody has classified"
KIND_TAGGED="quarry the idler shaft again"
KIND_TAG="gearbox"
readonly ROOM_KIND KIND_TODO KIND_PLAIN KIND_TAGGED KIND_TAG

# call_of ACTOR - the entry one seat left in the last /api/todo/{id}/category
# answer, and empty when that seat left none.
#
# Read by WHO made each call rather than by position, for claim_of's reason: the
# doors this todo is filed through are two PROCESSES, each holding a clock of its
# own, so which of two calls a millisecond apart sorts first is those clocks'
# business. That the latest call wins and the ones before it stay is asked where
# it is one clock's question: see TestAnybodyWhoCanReadATodoCanCategoriseIt.
call_of() {
	printf '%s' "$API_BODY" | jq -c --arg a "$1" 'first(.log[] | select(.actor == $a)) // empty'
}

# THE ONE THAT MATTERS. A principal who did not raise a todo can say what kind of
# work it is, at both doors, and the item stays its author's in every other
# respect.
#
# The operator goes first: a second person in the project, holding no share of
# the item. Then A's agent disagrees over MCP, so the two doors are shown writing
# one answer that the other reads back - the property that makes them one
# implementation rather than two that agree today.
a_todo_is_classified_by_somebody_who_did_not_write_it() {
	recall
	api POST "$TOKEN_A" "/api/chat/$ROOM_KIND/todo" \
		"$(jq -nc --arg t "$KIND_TODO" '{title: $t}')" || return 1
	want_eq "raise status" "$API_STATUS" 200 || return 1
	local id
	id="$(jqv .item.id)"
	remember KIND_ID "$id"
	want_eq "raised with no kind at all" "$(jqv .item.fields.category)" null || return 1

	api POST "$TOKEN_OP" "/api/todo/$id/category" '{"category": "bug"}' || return 1
	want_eq "file status" "$API_STATUS" 200 || return 1
	want_eq "what kind of work it is" "$(jqv .category)" bug || return 1
	# On the row as well as in the answer, and at the TOP LEVEL beside the status
	# and the assignee: one read gets all three, which is what this field is put
	# here for rather than left for each client to dig out of fields.
	want_eq "and the item says so" "$(jqv .item.category)" bug || return 1
	want_eq "with the status beside it" "$(jqv .item.status)" todo || return 1
	# Absent rather than empty: nobody carrying it is omitted from the row the way
	# every other empty derived value is, so the read normalises it here.
	want_eq "and who is carrying it beside that" "$(jqv '.item.assignee // ""')" "" || return 1
	# The rest of the item is still the author's.
	want_eq "still its author's" "$(jqv .item.owner_user)" "$USER_A" || return 1
	want_eq "with the title its author gave it" "$(jqv .item.title)" "$KIND_TODO" || return 1
	want_eq "still #filing's" "$(jqv .item.fields.room)" "$ROOM_KIND" || return 1
	# The vocabulary rides the answer, so a client draws the control from the node
	# rather than from its own copy of a list that drifts.
	want_eq "the vocabulary is on the answer" \
		"$(jqv '.vocabulary | join(",")')" "bug,feature,chore,question" || return 1

	# What the AUTHOR reads, which is the half that fails when the entry hangs off
	# the wrong row.
	api GET "$TOKEN_A" "/api/todo/$id/category" || return 1
	want_eq "the author's read of what it is" "$(jqv .category)" bug || return 1

	# The second door, and a third seat: A's agent says it is a chore after all.
	want_tool todo_category "$TOKEN_A_AGENT" \
		"$(jq -nc --arg i "$id" '{todo: $i, category: "chore"}')" || return 1
	want_eq "reclassified over MCP" "$(tv .category)" chore || return 1
	api GET "$TOKEN_OP" "/api/todo/$id/category" || return 1
	want_eq "read back through the other door" "$(jqv .category)" chore || return 1
	printf 'the operator and an agent both filed somebody else todo, over both doors\n'
}

# THE REFUSAL IS THE FEATURE. A word outside the set is an ERROR at every door
# and nothing is written - a vocabulary that quietly took "defect" would hold two
# words for one population, and the count that is the whole reason for having a
# closed set would be wrong with nothing on screen to say so.
#
# The rest are the refusals every queue verb makes: an id that is not here, and
# an id that is here and is not a queue item, get one answer.
a_category_that_is_not_one_is_refused() {
	recall
	local id="$KIND_ID"

	want_status 400 POST "$TOKEN_OP" "/api/todo/$id/category" '{"category": "defect"}' || return 1
	case "$API_BODY" in
	*"bug, feature, chore, question"*) ;;
	*)
		printf 'the refusal does not say what a kind may be: %s\n' "$API_BODY" >&2
		return 1
		;;
	esac
	want_tool_fails todo_category "$TOKEN_OP" \
		"$(jq -nc --arg i "$id" '{todo: $i, category: "epic"}')" \
		"is not a kind of work this queue has" || return 1
	want_tool_fails mem_write "$TOKEN_A" \
		"$(jq -nc --arg i "$id" '{id: $i, category: "urgent"}')" \
		"is not a kind of work this queue has" || return 1
	# The narrowed READ takes the same door: asking for a kind that is not one is
	# a refusal naming the vocabulary rather than an empty list, which would read
	# exactly like "there are no bugs".
	want_status 400 GET "$TOKEN_A" "/api/artifacts?type=memory&kind=todo&category=defect" || return 1
	want_tool_fails todos "$TOKEN_A" '{"category": "defect"}' \
		"is not a kind of work this queue has" || return 1

	# And none of that wrote anything.
	api GET "$TOKEN_A" "/api/todo/$id/category" || return 1
	want_eq "whatever it was, it still is" "$(jqv .category)" chore || return 1
	want_eq "and nobody added an entry for a word that is not one" \
		"$(printf '%s' "$API_BODY" | jq '[.log[] | select(.category == "defect" or .category == "epic")] | length')" \
		0 || return 1

	want_status 404 POST "$TOKEN_A" "/api/todo/01HNOSUCHTODO000000000000/category" \
		'{"category": "bug"}' || return 1
	# A bug is readable and is not a queue item: same answer, because naming an id
	# here is not a way to find out what else it might be.
	api POST "$TOKEN_A" /api/artifacts \
		'{"type": "bug", "title": "readable, and not a queue item either", "status": "open"}' || return 1
	want_eq "the bug this refuses on" "$API_STATUS" 200 || return 1
	local bug
	bug="$(jqv .id)"
	want_status 200 GET "$TOKEN_A" "/api/artifact/$bug" || return 1
	want_status 404 POST "$TOKEN_A" "/api/todo/$bug/category" '{"category": "bug"}' || return 1
	printf 'five words outside the set refused at four doors, and nothing moved\n'
}

# Read permission is the bar, and it is a real bar. A principal who cannot read
# the todo is refused at both doors and told exactly what a read of the id would
# have told them - nothing about the row - and nothing moves.
classifying_a_todo_you_cannot_read_is_refused() {
	recall
	local id="$KIND_ID"

	want_status 404 POST "$TOKEN_B" "/api/todo/$id/category" '{"category": "feature"}' || return 1
	want_status 404 GET "$TOKEN_B" "/api/todo/$id/category" || return 1
	want_tool_fails todo_category "$TOKEN_B" \
		"$(jq -nc --arg i "$id" '{todo: $i, category: "feature"}')" "no such todo" || return 1

	api GET "$TOKEN_A" "/api/todo/$id/category" || return 1
	want_eq "whatever it was, it still is" "$(jqv .category)" chore || return 1
	want_eq "and nobody added an entry" \
		"$(printf '%s' "$API_BODY" | jq --arg b "$USER_B" \
			'[.log[] | select(.actor == $b)] | length')" 0 || return 1
	printf 'B was refused at both doors and the todo is filed as it was\n'
}

# A classification says WHO made it and WHEN, which is the whole reason it is an
# event and not a field write. A column records THAT something changed; the log
# says the operator called this a bug and an agent called it a chore afterwards,
# which is an argument with two sides rather than a value with no history.
a_classification_records_who_made_it() {
	recall
	local id="$KIND_ID" op agent

	api GET "$TOKEN_A" "/api/todo/$id/category" || return 1
	want_eq "both calls are in the log" "$(jqv '.log | length')" 2 || return 1
	op="$(call_of "$USER_OP")"
	agent="$(call_of "$AGENT_A")"
	if [ -z "$op" ] || [ -z "$agent" ]; then
		printf 'the log does not name both seats: %s\n' \
			"$(printf '%s' "$API_BODY" | jq -c .log)" >&2
		return 1
	fi
	want_eq "the operator called it" "$(claimv "$op" .category)" bug || return 1
	want_eq "as a person" "$(claimv "$op" .actor_kind)" user || return 1
	want_eq "out of nothing" "$(claimv "$op" .from)" "" || return 1
	want_eq "and the agent called it" "$(claimv "$agent" .category)" chore || return 1
	want_eq "as an agent" "$(claimv "$agent" .actor_kind)" agent || return 1
	want_eq "acting for its person" "$(claimv "$agent" .actor_user)" "$USER_A" || return 1
	want_eq "naming what it was before" "$(claimv "$agent" .from)" bug || return 1
	said_when "the operator's" "$(claimv "$op" .created)" || return 1
	said_when "the agent's" "$(claimv "$agent" .created)" || return 1
	# The standing call is one of the ones that were actually made, and it says
	# when - a fold naming an entry nobody can find would be a claim with no
	# provenance behind it, which is what this log exists to prevent.
	want_eq "the standing call is one of the ones in the log" \
		"$(printf '%s' "$API_BODY" | jq --arg e "$(jqv .standing.entry)" \
			'[.log[] | select(.id == $e)] | length')" 1 || return 1
	said_when "the standing call" "$(jqv .standing.at)" || return 1
	# WHICH of the two the fold stands on is deliberately not asserted here, and
	# the first version of this check asserting it is why. The two calls were made
	# through two PROCESSES - `flowy serve` and `flowy mcp`, each holding a clock
	# of its own - so which of them sorts last is those clocks' business rather
	# than this surface's, exactly as it is for a claim and for a status move. The
	# ROW is the deterministic answer and is asserted above, through both doors.
	# That the latest call wins and the ones before it stay is asked where it is
	# one clock's question and has one answer: see the store check beside this
	# one, TestAnybodyWhoCanReadATodoCanCategoriseIt.

	# Taking it back is a call too: the empty value is something somebody chose,
	# and the log says so rather than saying nothing.
	api POST "$TOKEN_OP" "/api/todo/$id/category" '{"category": ""}' || return 1
	want_eq "unclassified again" "$(jqv .category)" "" || return 1
	want_eq "and the operator's unfiling is in the log, by them" \
		"$(printf '%s' "$API_BODY" | jq --arg a "$USER_OP" \
			'[.log[] | select(.category == "" and .actor == $a)] | length')" 1 || return 1
	want_eq "naming what it took it back from" \
		"$(printf '%s' "$API_BODY" | jq -r --arg a "$USER_OP" \
			'first(.log[] | select(.category == "" and .actor == $a)).from')" chore || return 1

	# The author's own write leaves an entry as well. A value that sometimes has a
	# call behind it and sometimes does not is a log that cannot answer the
	# question it exists for, so mem_write appends one in the same transaction.
	want_tool mem_write "$TOKEN_A" \
		"$(jq -nc --arg i "$id" '{id: $i, category: "bug"}')" || return 1
	api GET "$TOKEN_A" "/api/todo/$id/category" || return 1
	want_eq "four calls now" "$(jqv '.log | length')" 4 || return 1
	want_eq "the author left one too" "$(claimv "$(call_of "$USER_A")" .category)" bug || return 1
	want_eq "and the unfiling is still in the log" \
		"$(printf '%s' "$API_BODY" | jq '[.log[] | select(.category == "")] | length')" 1 || return 1

	# And an entry is minted: the closed set is held closed by the verb, so one
	# handed in through the generic event door would be a category outside the
	# vocabulary with a record saying somebody chose it.
	want_status 403 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg i "$id" --arg r "$ROOM_KIND" '{type: "todo.category", room: $r,
		   artifact: $i, body: "filed as defect", meta: {category: "defect", from: ""}}')" || return 1
	api GET "$TOKEN_A" "/api/todo/$id/category" || return 1
	want_eq "so the forged call is not in the log" "$(jqv '.log | length')" 4 || return 1
	printf 'four calls by three seats, each saying who and when; a forged one refused\n'
}

# A TODO WITH NO KIND READS AND LISTS EXACTLY AS IT DID YESTERDAY, and the
# narrowed read is the other half of what a closed set is for.
#
# Absent is a value. The whole queue predates this field and none of it is
# backfilled: nothing refuses a row for having no kind, nothing guesses one from
# a title, and an unclassified todo is on every page it was on before. What it is
# NOT on is the page that asked for the bugs - which is the property that makes
# `?category=bug` an answer rather than a suggestion.
an_unclassified_todo_reads_and_lists_fine() {
	recall
	# The bug this narrows to, restated rather than inherited. A restatement is
	# accepted - saying a bug is still a bug is somebody agreeing out loud - and a
	# check that depended on where the check before it happened to leave the row
	# would be asserting about the run rather than about the filter.
	api POST "$TOKEN_A" "/api/todo/$KIND_ID/category" '{"category": "bug"}' || return 1
	want_eq "the bug this narrows to" "$(jqv .category)" bug || return 1

	api POST "$TOKEN_A" "/api/chat/$ROOM_KIND/todo" \
		"$(jq -nc --arg t "$KIND_PLAIN" '{title: $t}')" || return 1
	want_eq "raise status" "$API_STATUS" 200 || return 1
	local plain
	plain="$(jqv .item.id)"

	# It reads, and says it is unclassified rather than failing or inventing one.
	api GET "$TOKEN_A" "/api/artifact/$plain" || return 1
	want_eq "read status" "$API_STATUS" 200 || return 1
	want_eq "no kind on it" "$(jqv '.category // ""')" "" || return 1
	want_eq "and no key in fields either" "$(jqv .fields.category)" null || return 1
	# Its own log is empty rather than missing: nobody has classified it, which is
	# not the same as a reader who cannot see the calls.
	api GET "$TOKEN_A" "/api/todo/$plain/category" || return 1
	want_eq "log status" "$API_STATUS" 200 || return 1
	want_eq "no calls behind it" "$(jqv '.log | length')" 0 || return 1
	want_eq "and no standing call" "$(jqv '.standing // "none"')" none || return 1

	# It lists, beside the classified one.
	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=todo&room=$ROOM_KIND" || return 1
	want_todos "the room's panel" "$KIND_TODO" "$KIND_PLAIN" || return 1

	# And the narrowed read is the bugs: the classified row and not the other.
	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=todo&room=$ROOM_KIND&category=bug" || return 1
	want_todos "the bugs in that room" "$KIND_TODO" -- "$KIND_PLAIN" || return 1
	want_eq "and every row on it is a bug" \
		"$(printf '%s' "$API_BODY" | jq '[.artifacts[] | select(.category != "bug")] | length')" \
		0 || return 1
	# The same question over MCP, which is where a drainer asks it.
	want_tool todos "$TOKEN_A" "$(jq -nc --arg r "$ROOM_KIND" '{room: $r, category: "bug"}')" || return 1
	want_eq "one bug over MCP" "$(tv '[.items[] | select(.title == "'"$KIND_TODO"'")] | length')" 1 || return 1
	want_eq "and the unclassified one is not in it" \
		"$(tv '[.items[] | select(.title == "'"$KIND_PLAIN"'")] | length')" 0 || return 1
	printf 'an unclassified todo reads, lists, and stays out of the list of bugs\n'
}

# The same field over MCP and at the raise, which is where an agent states it.
# It rides fields beside the room, an update that does not restate it keeps it,
# and an empty one is a value rather than a silence. The tags beside it are free
# labels and nothing here refuses them - which is the whole distinction, checked
# on one item.
mem_write_takes_a_category_and_tags_take_anything() {
	recall
	want_tool mem_write "$TOKEN_A" \
		"$(jq -nc --arg t "$KIND_TAGGED" --arg g "$KIND_TAG" \
			'{title: $t, scope: "project", kind: "todo", room: "filing",
			  category: "feature", tags: [$g, "whatever-word-somebody-liked"]}')" || return 1
	want_eq "the kind rode the write" "$(tv .item.fields.category)" feature || return 1
	want_eq "beside the room" "$(tv .item.fields.room)" filing || return 1
	want_eq "and the free labels went in unjudged" \
		"$(tv '.item.tags | join(",")')" "$KIND_TAG,whatever-word-somebody-liked" || return 1
	local id
	id="$(tv .item.id)"

	# With a carrier beside it, because active says somebody is on it and this
	# door can say who in the same statement. See queuecoherence.go.
	want_tool mem_write "$TOKEN_A" \
		"{\"id\": \"$id\", \"status\": \"active\", \"assignee\": \"a-filer\"}" || return 1
	want_eq "kept by an update that did not restate it" \
		"$(tv .item.fields.category)" feature || return 1

	want_tool mem_write "$TOKEN_A" "{\"id\": \"$id\", \"category\": \"\"}" || return 1
	want_eq "unfiled over MCP" "$(tv .item.fields.category)" "" || return 1

	# And the raise door takes one, refusing a word that is not one in the same
	# words - the vocabulary is one vocabulary at every door, or it is not closed.
	api POST "$TOKEN_A" "/api/chat/$ROOM_KIND/todo" \
		'{"title": "raised as a question", "category": "question"}' || return 1
	want_eq "raised with a kind on it" "$(jqv .item.fields.category)" question || return 1
	want_status 400 POST "$TOKEN_A" "/api/chat/$ROOM_KIND/todo" \
		'{"title": "raised as nothing this queue has", "category": "wishlist"}' || return 1
	printf 'the kind rode a write, survived an update, came off, and rode a raise\n'
}

# A TAG NARROWS THE LIST, AND A PARAMETER THIS DOOR DOES NOT HONOUR IS REFUSED
# BY NAME.
#
# The measurement this is for, on 0.8.0+980a537:
#
#   GET /api/artifacts?type=finding             -> 40 artifacts
#   GET /api/artifacts?type=finding&tag=ragflow -> 40 artifacts
#
# with 16 of those findings carrying the tag. The filter was dropped, and a
# dropped filter answers 200 with MORE than was asked for - which no caller can
# detect, because there is no field to check and no count to compare. It is over
# the wire and not in a Go test on purpose: that is where it was measured, and a
# handler test would not see a route or a middleware losing the parameter.
a_tag_narrows_the_list_and_an_unhonoured_parameter_is_refused() {
	recall
	local both="tagfilter both" xonly="tagfilter x" yonly="tagfilter y by its author"
	local plain="tagfilter none"

	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$both" '{type: "memory", kind: "note", title: $t,
		   tags: ["tagfilter-x", "tagfilter-y"]}')" || return 1
	want_eq "the row carrying both" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$xonly" '{type: "memory", kind: "note", title: $t,
		   tags: ["tagfilter-x"]}')" || return 1
	want_eq "the row carrying one" "$API_STATUS" 200 || return 1
	# On the other column of labels, which is the one a reader cannot tell apart:
	# the console draws tags and user_tags as one list, so the chip somebody
	# clicked may have come from either.
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$yonly" '{type: "memory", kind: "note", title: $t,
		   user_tags: ["tagfilter-y"]}')" || return 1
	want_eq "the row labelled by its author" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$plain" '{type: "memory", kind: "note", title: $t}')" || return 1
	want_eq "the row carrying nothing" "$API_STATUS" 200 || return 1

	# One tag: the two rows that carry it and nothing else at all.
	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=note&tag=tagfilter-x" || return 1
	want_eq "narrowed status" "$API_STATUS" 200 || return 1
	want_todos "the rows tagged x" "$both" "$xonly" -- "$yonly" "$plain" || return 1
	want_eq "and nothing came with them" "$(hits)" 2 || return 1

	# Two tags mean AND, because that is what a second click on a stacked filter
	# means to the person clicking it.
	api GET "$TOKEN_A" "/api/artifacts?tag=tagfilter-x&tag=tagfilter-y" || return 1
	want_todos "the rows carrying both" "$both" -- "$xonly" "$yonly" "$plain" || return 1
	want_eq "and only that one" "$(hits)" 1 || return 1

	# Either column of labels answers a tag.
	api GET "$TOKEN_A" "/api/artifacts?tag=tagfilter-y" || return 1
	want_todos "the rows tagged y in either column" "$both" "$yonly" -- "$xonly" "$plain" || return 1

	# Filter first, then cut the page. A filter applied after the limit is the
	# same defect in different clothes: short AND wrong.
	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=note&tag=tagfilter-x&limit=1" || return 1
	want_eq "one row on the page" "$(hits)" 1 || return 1
	want_eq "and it is one of the tagged ones" \
		"$(hits '((.tags // []) + (.user_tags // [])) | index("tagfilter-x")')" 1 || return 1

	# The unnarrowed list still holds all four: a tag is a filter, not a
	# permission axis, and an untagged row is on every page it was on before.
	api GET "$TOKEN_A" "/api/artifacts?type=memory&kind=note" || return 1
	want_todos "the unnarrowed list" "$both" "$xonly" "$yonly" "$plain" || return 1

	# And the plural the defect was filed with is a refusal naming it, rather
	# than an answer that looks right.
	want_status 400 GET "$TOKEN_A" "/api/artifacts?tags=tagfilter-x" || return 1
	printf '%s' "$API_BODY" | grep -q 'tags' || {
		printf 'the refusal does not name the parameter:\n%s\n' "$API_BODY" >&2
		return 1
	}
	want_status 400 GET "$TOKEN_A" "/api/artifacts?type=memory&q=gearbox" || return 1
	printf 'a tag narrowed the list, two meant both, the limit came after it, and the plural was refused\n'
}

# --------------------------------------------- what was learned about a row
#
# A row was fixed at the moment it was filed: the words are its author's, and
# only while nobody had started the work. Everything learned about it afterwards
# went into a room, scrolled away, and was rediscovered by whoever picked the row
# up next. The append door is the fix, and it is deliberately the opposite of the
# edit door beside it - nothing already written changes, read permission is the
# whole bar, and it is NOT refused once the work has started, which is when a
# note is worth the most.
#
# The checks below drive principals who did not raise the row, for the reason the
# classification checks do: what is LEARNED about a row is not authorship of it.
NOTE_TODO="the console loses the room on a reload"
NOTE_BODY="as filed, by whoever filed it"
NOTE_ONE="the reload lands before the token is read, so the first fetch is unauthenticated"
NOTE_TWO="tried moving the fetch into the effect - same race, one tick later"
NOTE_TYPED="written from the console, under the body, by whoever was reading it"
readonly NOTE_TODO NOTE_BODY NOTE_ONE NOTE_TWO NOTE_TYPED

# THE ONE THAT MATTERS. Two seats, neither of which wrote the row, attach what
# they learned to it - and the AUTHOR reads both off the row itself rather than
# out of a log door they have to know exists.
#
# The operator goes first over HTTP, then A's own agent over MCP, so the two
# doors are shown writing one answer that the other reads back. The author's
# words are asserted untouched afterwards, because that is the whole difference
# between this and an edit.
a_note_is_added_by_somebody_who_did_not_raise_the_row() {
	recall
	api POST "$TOKEN_A" "/api/chat/$ROOM_KIND/todo" \
		"$(jq -nc --arg t "$NOTE_TODO" --arg b "$NOTE_BODY" '{title: $t, body: $b}')" || return 1
	want_eq "raise status" "$API_STATUS" 200 || return 1
	local id
	id="$(jqv .item.id)"
	remember NOTE_ID "$id"
	want_eq "nothing learned about it yet" "$(jqv '(.item.notes // []) | length')" 0 || return 1

	api POST "$TOKEN_OP" "/api/todo/$id/note" "$(jq -nc --arg n "$NOTE_ONE" '{note: $n}')" || return 1
	want_eq "note status" "$API_STATUS" 200 || return 1
	want_eq "one note back" "$(jqv '.notes | length')" 1 || return 1
	want_eq "in the words it was written in" "$(jqv .notes[0].note)" "$NOTE_ONE" || return 1
	want_eq "attributed to the seat that wrote it" "$(jqv .notes[0].actor)" "$USER_OP" || return 1
	want_eq "as a person" "$(jqv .notes[0].actor_kind)" user || return 1
	# On the row in the same answer as well as at the top level: a client that
	# reads rows must not have to know this door exists to see what was learned.
	want_eq "and on the row in the same answer" "$(jqv .item.notes[0].note)" "$NOTE_ONE" || return 1
	said_when "the note" "$(jqv .notes[0].created)" || return 1

	# WHAT THE AUTHOR READS, which is the half that fails when the entry hangs off
	# the wrong row or lands where only its writer can see it.
	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "the author reads it on the row itself" "$(jqv .notes[0].note)" "$NOTE_ONE" || return 1
	want_eq "with the body they wrote untouched" "$(jqv .body)" "$NOTE_BODY" || return 1
	want_eq "and the title" "$(jqv .title)" "$NOTE_TODO" || return 1

	# The second door, and a third seat: A's agent adds what it tried.
	want_tool todo_note "$TOKEN_A_AGENT" \
		"$(jq -nc --arg i "$id" --arg n "$NOTE_TWO" '{todo: $i, note: $n}')" || return 1
	want_eq "two notes now" "$(tv '.notes | length')" 2 || return 1
	want_eq "the operator's is still there" \
		"$(tv "[.notes[] | select(.note == \"$NOTE_ONE\")] | length")" 1 || return 1
	want_eq "the agent's is beside it, as an agent" \
		"$(tv "[.notes[] | select(.actor == \"$AGENT_A\" and .actor_kind == \"agent\")] | length")" \
		1 || return 1
	want_eq "acting for its person" \
		"$(tv "first(.notes[] | select(.actor == \"$AGENT_A\")).actor_user")" "$USER_A" || return 1
	# WHICH of the two sorts first is deliberately not asserted, and asserting it
	# is why this check failed the first time it ran. The two notes were written
	# through two PROCESSES - `flowy serve` and `flowy mcp`, each holding a clock
	# of its own - so their order is those clocks' business, exactly as it is for a
	# classification and for a status move. That notes come back oldest first is
	# asked where it is one clock's question and has one answer: see the store
	# check beside this one, TestANoteLandsOnWorkThatIsUnderWayAndOnWorkThatIsFinished.
	printf 'two seats that did not write the row added to it; the author reads both\n'
}

# A NOTE IS NOT WRITTEN AGAINST A STATE, so nothing refuses one because somebody
# picked the row up - and the edit door beside it refuses exactly that, in the
# same check, because the contrast is the design.
#
# Rewording a row under whoever is working from it changes the job. Adding to it
# does not, and active and done are the states a measurement or a landing note is
# worth the most in.
a_note_lands_on_work_that_has_already_started() {
	recall
	local id="$NOTE_ID"

	# Taken first, then started. A row nobody is carrying cannot be active - the
	# two facts move together now, and the status door cannot say who is on it,
	# so it refuses rather than guessing. See internal/store/queuecoherence.go.
	api POST "$TOKEN_OP" "/api/todo/$id/assignee" '{"assignee": "a-noteworker"}' || return 1
	want_eq "the operator took it" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_OP" "/api/artifact/$id/status" '{"status": "active"}' || return 1
	want_eq "somebody picked it up" "$API_STATUS" 200 || return 1
	# The author's own edit is refused now, naming who took it. That is right, and
	# it is what left an agent with nowhere to put what it had just worked out.
	want_status 409 POST "$TOKEN_A" "/api/todo/$id/edit" \
		'{"saw": "todo", "title": "reworded under whoever took it"}' || return 1

	api POST "$TOKEN_OP" "/api/todo/$id/note" \
		'{"note": "measured: the unauthenticated fetch is 40ms before the token lands"}' || return 1
	want_eq "and the note lands anyway" "$API_STATUS" 200 || return 1
	want_eq "three notes now" "$(jqv '.notes | length')" 3 || return 1

	api POST "$TOKEN_OP" "/api/artifact/$id/status" '{"status": "done"}' || return 1
	want_eq "and then it was finished" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" "/api/todo/$id/note" \
		'{"note": "landed - the follow-up is the console rendering these under the body"}' || return 1
	want_eq "a note on finished work" "$API_STATUS" 200 || return 1

	api GET "$TOKEN_A" "/api/todo/$id/notes" || return 1
	want_eq "four of them now" "$(jqv '.notes | length')" 4 || return 1
	# Every earlier one is still there, word for word. An append that quietly
	# replaced what was already learned would be the edit this is not.
	want_eq "the first one still says what it said" \
		"$(printf '%s' "$API_BODY" | jq --arg n "$NOTE_ONE" \
			'[.notes[] | select(.note == $n)] | length')" 1 || return 1
	want_eq "and so does the second" \
		"$(printf '%s' "$API_BODY" | jq --arg n "$NOTE_TWO" \
			'[.notes[] | select(.note == $n)] | length')" 1 || return 1
	want_eq "and nothing rewrote the title" "$(jqv .item.title)" "$NOTE_TODO" || return 1
	printf 'notes on active and on finished work, where an edit is refused\n'
}

# Read permission is a real bar, and a note with nothing to say is not one.
#
# B is refused at both doors and told what a read of the id would have told them
# - nothing about the row - and the row is left exactly as it was.
an_empty_note_and_a_row_you_cannot_read_are_refused() {
	recall
	local id="$NOTE_ID"

	want_status 400 POST "$TOKEN_A" "/api/todo/$id/note" '{"note": "   "}' || return 1
	want_status 404 POST "$TOKEN_B" "/api/todo/$id/note" '{"note": "I can see this row"}' || return 1
	want_status 404 GET "$TOKEN_B" "/api/todo/$id/notes" || return 1
	want_tool_fails todo_note "$TOKEN_B" \
		"$(jq -nc --arg i "$id" '{todo: $i, note: "over the other door"}')" "no such todo" || return 1

	api GET "$TOKEN_A" "/api/todo/$id/notes" || return 1
	want_eq "still four" "$(jqv '.notes | length')" 4 || return 1
	want_eq "and none of them B's" \
		"$(printf '%s' "$API_BODY" | jq --arg b "$USER_B" \
			'[.notes[] | select(.actor == $b)] | length')" 0 || return 1
	printf 'B was refused at both doors and the row says what it said\n'
}

# A note is MINTED, and this is the door that would otherwise be the way round
# the verb. An entry handed in here is worse than a forged status move: for this
# type the entry IS the content, so it would be a paragraph attributed to a seat
# that never wrote it, sitting under the author's own body as what somebody
# learned about the work.
a_note_cannot_be_written_by_hand() {
	recall
	want_status 403 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg i "$NOTE_ID" --arg r "$ROOM_KIND" '{type: "todo.note", room: $r,
		   artifact: $i, body: "measured by nobody"}')" || return 1
	local refused
	refused="$(jqv .error)"
	api GET "$TOKEN_A" "/api/todo/$NOTE_ID/notes" || return 1
	want_eq "so the forged note is not on the row" "$(jqv '.notes | length')" 4 || return 1
	printf 'a hand-written note: %s\n' "$refused"
}

# AND THE HALF A READER ACTUALLY HAS: the row's page draws them, under the body,
# attributed, and a person can add one without knowing a door exists.
#
# Everything above this is true of a store nobody can see. What the doors fixed
# is only fixed once the console shows it - the defect that produced this feature
# was diagnosed four times by four agents because what had been learned lived in
# a room and scrolled away, and a note in a log the console does not draw is in
# the same place.
#
# The row is the one the checks above built, with the four notes they wrote on
# it: two seats, two doors, and one of them an agent, which is the attribution
# the page has to keep apart. The console adds a fifth through its own box.
browser_draws_the_notes_under_the_body() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/notes-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$NOTE_ID" "$NOTE_BODY" "$NOTE_TYPED"
}

# The panel SETS one, OVERRIDES one, and a poll of the room does not wipe it -
# in a real browser, driving the control a person drives.
#
# The last clause is the one this exists for. The panel is refilled from the
# node by every long poll that comes back, so an assignee held in the tab's own
# state looks finished until somebody says something in the room and then
# silently reverts - which is the bug this feature would otherwise have shipped
# with. The check provokes the poll rather than waiting for one, and waits for
# the provoking message to reach the screen, so "the poll came back" is asserted
# and not assumed.
#
# Its own room, with two todos of its own: one nobody is carrying, and one
# written the way the whole queue was before there was a field, with OWNER as
# the first line of its body.
browser_sets_and_overrides_an_assignee() {
	recall
	api POST "$TOKEN_A" "/api/chat/$ROOM_PLAN/todo" \
		"$(jq -nc --arg t "$PLAN_TODO_FREE" '{title: $t}')" || return 1
	want_eq "the unowned one" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" "/api/chat/$ROOM_PLAN/todo" \
		"$(jq -nc --arg t "$PLAN_TODO_OWNED" --arg b "OWNER: $PLAN_OWNER" \
			'{title: $t, body: $b}')" || return 1
	want_eq "the one whose body names an owner" "$API_STATUS" 200 || return 1
	cd "$ROOT/web" || return 1
	node scripts/assignee-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$ROOM_PLAN" "$PLAN_TODO_FREE" "$PLAN_TODO_OWNED" \
		"$PLAN_OWNER" "$PLAN_TAKER" "$PLAN_SECOND"
}

# The console, on the page: the room's todos have to be on the rendered room,
# not merely in an endpoint's answer. A feature that is complete in the data and
# absent from the screen is the failure this whole thing is a fix for.
console_renders_the_rooms_todos() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$ROOM_TODO_GENERAL"
}

# The same claim one layer out, in a browser, asserted on the PANEL rather than
# on the page. The distinction is the whole check: the word "todos" is also in
# the global navigation, so a page-text search for it passes with the panel
# entirely absent - which is what happened the first time this was checked by
# hand. A string that appears in two places is not evidence about either.
browser_renders_the_rooms_todos() {
	recall
	# One written the way several real ones were - "OWNER: unassigned" - because
	# the panel falls back to "unowned" when there is no owner, and the two were
	# on screen together. Raised as a todo through the panel itself: "todo list
	# has unowned and unassigned - looks identical". Two words for one state
	# read as two states. Without this row the check cannot tell the fix from
	# its absence.
	api POST "$TOKEN_A" /api/chat/general/todo \
		'{"title": "quinceberry the idler pulley", "body": "OWNER: unassigned"}' || return 1
	cd "$ROOT/web" || return 1
	node scripts/browser-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$ROOM_TODO_GENERAL" unassigned
}

# The panel puts the finished work away, says how much of it there is, and
# remembers the answer - in a browser, driving the checkbox a person drives.
#
# #general holds 26 todos and 16 are done, which pushes the live work off the
# bottom of a panel with about fifteen visible rows: the surface that exists to
# answer "what is this room doing" was answering "what has this room finished".
#
# The count is the half that keeps it honest, and it is asserted as a NUMBER
# rather than as the presence of some text. A panel showing four rows with no
# sign that sixteen are behind it lies about the size of the queue, and a filter
# that silently removes rows is how somebody concludes a todo does not exist.
browser_hides_the_finished_todos() {
	recall
	api POST "$TOKEN_A" "/api/chat/$ROOM_HIDE/todo" \
		"$(jq -nc --arg t "$HIDE_TODO_DONE" '{title: $t, status: "done"}')" || return 1
	want_eq "the finished one" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" "/api/chat/$ROOM_HIDE/todo" \
		"$(jq -nc --arg t "$HIDE_TODO_OPEN" '{title: $t}')" || return 1
	want_eq "the live one" "$API_STATUS" 200 || return 1
	cd "$ROOT/web" || return 1
	node scripts/hidedone-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$ROOM_HIDE" "$HIDE_TODO_DONE" "$HIDE_TODO_OPEN"
}

# Raising a todo does not make the browser offer a saved password or a stored
# card, driven as the journey rather than read off the source.
#
# The operator reported it in their own words: "raise todo input makes my
# browser show password and credit card suggestion. chat input doesn't". The
# raise box was one unnamed text input with no type and no autocomplete, alone
# in a form with a submit button, which is the shape a browser reads as a
# sign-in. The message box beside it was never affected because what you type
# into it is a textarea.
#
# It is a FLOW - open the room, click the box, type, submit, see the row, then
# name the carrier - because a field that is annotated perfectly and cannot be
# typed into is not fixed. That is the New-button failure two features over,
# where the control was disabled until another box had text, so no click ever
# reached a handler and every test still passed.
#
# The sweep at the end is the other half. The operator hit one field; the next
# one they hit will be in another file, so every text box on the pages a person
# actually opens has to say the same thing. It goes red on the raise box alone
# against the console as it was, which was checked by hand against a stand-in
# serving the previous bundle before this was wired in.
browser_does_not_offer_a_password_over_a_todo() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/autofill-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$ROOM_AUTOFILL" "$AUTOFILL_TODO" "$AUTOFILL_CARRIER" \
		--page=/ --page=/todos --page=/findings --page=/reports \
		--page=/diagrams --page=/activity --page=/direct
}

# The roster, in a browser, on the ELEMENT: each listener's line says what that
# listener can do about what it hears, and the three states read as three
# states. "polling 4s ago" is true of all three and answers none of them, which
# is what the panel said on the night a session went deaf behind it.
#
# It reads the readers the presence check declared and polled, a phase earlier -
# their kinds are on the row, and the row is what the roster draws.
browser_shows_what_a_listener_can_do() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/roster-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$ROSTER_TRACKED=tracked" "$ROSTER_FORKED=forked" "$ROSTER_QUIET=unknown" \
		--went-quiet="$ROSTER_STOPPED"
}

# Speakers are drawn in their own colour, and it is really applied. A palette
# that exists and a component that never uses it are indistinguishable from
# everywhere except a rendered page, so this asks the browser what colour the
# name actually came out. The second half is the discriminating one: giving
# every speaker the SAME colour would pass "is it coloured" and tell nobody
# apart, which is the entire point of the feature.
browser_colours_the_speakers() {
	recall
	# One raised in each remaining state first. Without them the room holds only
	# open todos, the distinctness half of the check has nothing to compare, and
	# it reports "one state present, distinctness untested" - which is honest
	# and useless. The states are what the colours are FOR.
	# The active one is raised and then TAKEN and started, in three calls rather
	# than one: the raise door has never been able to say who is carrying a row,
	# and a row nobody is carrying cannot be active. See queuecoherence.go.
	local flywheel
	api POST "$TOKEN_A" /api/chat/general/todo \
		'{"title": "quicklime the flywheel"}' || return 1
	flywheel="$(jqv .item.id)"
	api POST "$TOKEN_A" "/api/todo/$flywheel/assignee" '{"assignee": "a-flywright"}' || return 1
	api POST "$TOKEN_A" "/api/artifact/$flywheel/status" '{"status": "active"}' || return 1
	api POST "$TOKEN_A" /api/chat/general/todo \
		'{"title": "marrowbone the gasket", "status": "done"}' || return 1
	cd "$ROOT/web" || return 1
	node scripts/colour-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

# A room remembers its decisions: a pin puts a message in the strip, and the
# strip is answerable from the room's own log.
a_pin_puts_a_message_up_in_the_room() {
	local PIN_MESSAGE
	api POST "$TOKEN_A" /api/chat/general/say \
		'{"body": "quicklime: we settled on the cgroup for liveness"}' || return 1
	PIN_MESSAGE=$(jqv .id)
	api POST "$TOKEN_A" "/api/chat/general/pin" "{\"message\": \"$PIN_MESSAGE\"}" || return 1
	api GET "$TOKEN_A" /api/chat/general/pins || return 1
	want_eq "the pinned message" "$(jqv '.pinned[-1]')" "$PIN_MESSAGE" || return 1
	# The log is the record, and it is what answers WHO decided this was the
	# decision - a list of ids cannot.
	want_eq "the log names the pinner" "$(jqv '.log[-1].verb')" "pin.add"
}

# Taking one down leaves the strip empty AND the log complete. A pin that was up
# for a day and then removed is a different history from one that never existed,
# and only the log can tell them apart.
# Self-contained, because `check` runs each of these in a command substitution
# and a variable set in the previous one is gone by the time this starts. The
# first version leaned on PIN_MESSAGE from the check above and died on an
# unbound variable - which is a check that cannot fail for the reason it is
# about.
an_unpin_takes_it_down_and_the_log_remembers_both() {
	local PIN_MESSAGE
	api POST "$TOKEN_A" /api/chat/general/say \
		'{"body": "quicklime: and this one we later changed our minds about"}' || return 1
	PIN_MESSAGE=$(jqv .id)
	api POST "$TOKEN_A" "/api/chat/general/pin" "{\"message\": \"$PIN_MESSAGE\"}" || return 1
	api DELETE "$TOKEN_A" "/api/chat/general/pin/$PIN_MESSAGE" || return 1
	api GET "$TOKEN_A" /api/chat/general/pins || return 1
	local still
	still=$(jqv "[.pinned[] | select(. == \"$PIN_MESSAGE\")] | length")
	want_eq "it is down" "$still" "0" || return 1
	want_eq "both entries are in the log" \
		"$(jqv "[.log[] | select(.message == \"$PIN_MESSAGE\")] | length")" "2"
}

# A pin belongs to the room the message is in. Without this a strip can be made
# to point at a message this room's readers cannot open - the line would be
# visible and the thing it names would not.
a_message_from_another_room_cannot_be_pinned_here() {
	api POST "$TOKEN_A" /api/chat/elsewhere/say '{"body": "said somewhere else"}' || return 1
	local other
	other=$(jqv .id)
	want_status 400 POST "$TOKEN_A" "/api/chat/general/pin" "{\"message\": \"$other\"}" || return 1
	printf '%s\n' "$API_BODY" | grep -q "not in" || {
		printf 'the refusal does not say which room it was said in: %s\n' "$API_BODY" >&2
		return 1
	}
}

# A pin event written by hand is a strip anybody can put a line in, with none of
# the refusals asked. Same rule the dep edges and the votes are under.
a_pin_event_cannot_be_written_by_hand() {
	want_status 403 POST "$TOKEN_A" /api/events \
		'{"type": "pin.add", "room": "general", "body": ""}'
}

# A person can select and copy what was said, which is not a styling question.
#
# Every message row was a <button>, and a browser refuses to select a button's
# text - it reads a drag inside one as a click on a control. So the transcript
# could not be copied while the markup, the CSS and the poll repaint were all
# innocent, and nothing short of a real drag in a real browser would have shown
# it. The fix is on master; this is the guard it landed without, and it has
# already survived one full rewrite of that file by luck rather than by a check.
#
# It drags the way a person does rather than calling selectText(), because that
# API succeeds on a button too - it does not go through the browser's own
# decision about what a drag inside that element means.
browser_lets_a_person_copy_a_message() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/copy-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

# Reading a message is not answering it.
#
# Every row was a div wearing role="button", and a click on it selected the
# message - which is what the next thing you say attaches to AND what it quotes.
# So clicking a line to read it, or to put the caret somewhere, silently armed a
# reply at whatever was under the pointer. Raised by the operator: "dont cite
# automatically when message clicked. add reply to button, as other messages
# have".
#
# The check drives a browser because every part of this is a browser's own
# decision - what a click does, what a drag does, where tab goes - and asserts
# on the COMPOSER'S QUOTED BLOCK rather than on a class: a row that stops
# looking selected while the next message still attaches to it is the same bug
# wearing less. Dragging is checked in the same run because it is the thing this
# change is most likely to break: the drag and the click share an element, and
# the operator objected to clicking, not to quoting exactly what you selected.
browser_replies_only_when_asked() {
	recall
	api POST "$TOKEN_A" "/api/chat/$ROOM_REPLY/say" \
		"$(jq -nc --arg b "$REPLY_FIRST" '{body: $b}')" || return 1
	want_eq "the first message" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" "/api/chat/$ROOM_REPLY/say" \
		"$(jq -nc --arg b "$REPLY_LAST" '{body: $b}')" || return 1
	want_eq "the second" "$API_STATUS" 200 || return 1
	cd "$ROOT/web" || return 1
	node scripts/reply-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$ROOM_REPLY" "$REPLY_LAST"
}

# Reading the history wins over following the room.
#
# The view scrolled to the bottom on every change in message count, whatever
# the reader was doing, so scrolling back through a busy room yanked you to the
# end the moment anybody spoke. The user reported it twice, while trying to read.
#
# The assertion is THE SCROLL POSITION and not the pill: a version that renders
# the pill and still jumps would pass a check that only asked whether the pill
# existed, and jumping is the whole complaint. The pill is checked second,
# because staying put and never saying anything arrived is a different bug.
#
# A's AGENT posts it, not B. Somebody else has to say it - a message the reader
# sent arrives by a different path - but ROOMS ARE PER PROJECT, so a say on B's
# token lands in B's `general` and this waits out the clock on a room that never
# heard it. That cost four gate runs, each accusing a different innocent part.
browser_does_not_scroll_a_reader_away() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/scroll-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT"
}

# The other half of the same complaint, and the half the check above passes.
#
# Reported as "when I reload a page chat automatically scrolls to a random
# place". It was not random and it was not the load: the transcript arrived at
# the end correctly and was displaced SECONDS LATER, and where a reader is
# looking during that window is an arbitrary point in the history.
#
# A room's history does not arrive in one answer - the first GET carries a page
# of 200 and the long poll delivers the rest - so the view scrolled itself once
# per batch, and a smooth scroll latches its destination when it is CALLED.
# Several animations ran at once, each aimed at the bottom of a shorter
# transcript, and whichever the browser finished last is where the reader was
# left. Measured over eight loads of a 718 message room: four landed 201,633px
# short, at the end of the room as it stood at 399 messages.
#
# SO THE CHECK WATCHES RATHER THAN SAMPLES. An assertion taken once when the
# page looks settled passes this outright - the losing animation had already
# fired scrollend, so nothing retried and nothing was visibly wrong for another
# five seconds. It waits for the room to stop arriving, asserts the end, and
# then keeps asking for twelve seconds while nothing at all happens.
#
# A's AGENT seeds it, for the reason the check above learned twice: it has to be
# somebody else, and it has to be somebody in A's project, because rooms are per
# project.
#
# The seed is still MORE THAN ONE PAGE, and it now proves the other half of the
# same complaint: the operator also reported that a reload loads the entire
# history, so a room opens on a bounded window and pages back when somebody
# scrolls up. This asserts the room holds far more than the view fetched, that
# the history is still reachable, and that reaching it leaves the reader on the
# line they were reading - a prepend displaces a reader exactly as a stale
# scroll did.
browser_leaves_a_room_load_at_the_end() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/stay-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT"
}

# The unread badge clears, and the mark it is drawn from moves with it.
#
# Reported as "unread counter is stuck": the sidebar badge never cleared for the
# operator. The count is the inbox - what this principal may read and did not
# write, since the reader mark the node keeps for it - and that mark is moved by
# a WAITER acking. inbox_readers held a row for every agent on the node and NO
# ROW AT ALL for the person in the browser, so nothing ever moved theirs. The
# rule was not wrong; it had no answer for a reader that is not a process.
#
# So the console declares a reader of its own, per room, and acks what it has
# actually reached. The check drives all of that in a browser and asserts BOTH
# HALVES: the badge on the element, and the node's own mark over the API. A
# badge that clears while the mark stays put is the same bug in a new costume -
# the next reload, and the next device, count the same messages again.
#
# A's AGENT says the messages, for the reason the scroll check learned: it has
# to be somebody else, because the inbox excludes what you wrote yourself, and
# it has to be in A's project, because rooms are per project.
browser_clears_the_unread_badge() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/unread-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" "$TOKEN_A_AGENT"
}

# The other half of it, in the table rather than on the screen, and afterwards:
# the row the CONSOLE wrote is a reader row like any waiter's, it is past what
# the console read, and an older acknowledgement does not drag it back.
#
# It reads the row the check above left behind rather than declaring one of its
# own, which is what makes it a statement about the console: under the code this
# replaces there is no such row for anybody who reads in a browser.
the_consoles_mark_is_a_reader_row_that_only_moves_forward() {
	recall
	local reader="console:general" mark read_to back
	mark="$(scalar "SELECT read_cursor FROM inbox_readers
	                 WHERE reader = '$reader'
	                   AND split_part(principal, chr(31), 1) = '$USER_A'")" || return 1
	if [ -z "$mark" ]; then
		printf 'inbox_readers holds no row called %s for user A.\n' "$reader" >&2
		printf 'A person in a browser runs no waiter, so nothing moves their mark and\n' >&2
		printf 'the inbox they are counting never shrinks. That is the stuck counter.\n' >&2
		return 1
	fi
	read_to="$(scalar "SELECT max(seq_hlc) FROM events
	                    WHERE type = 'chat' AND room = 'general'
	                      AND body LIKE 'unread-check arrival%'")" || return 1
	if [ -z "$read_to" ] || [ "$mark" -lt "$read_to" ]; then
		printf 'the console read the room to %s and its mark is at %s\n' "${read_to:-<nothing>}" "$mark" >&2
		return 1
	fi
	# An older position, which is what a second tab or a slow one hands back.
	# Refused quietly and forwards-only, or two tabs take turns reopening the
	# messages the other has already read.
	api POST "$TOKEN_A" /api/inbox/ack \
		"{\"as\": \"$reader\", \"cursor\": $((mark - 5)), \"delivered\": true}" || return 1
	want_eq "the status of an ack of an older position" "$API_STATUS" 200 || return 1
	back="$(scalar "SELECT read_cursor FROM inbox_readers
	                 WHERE reader = '$reader'
	                   AND split_part(principal, chr(31), 1) = '$USER_A'")" || return 1
	want_eq "the mark after an older ack" "$back" "$mark" || return 1
	printf '%s is at %s in inbox_readers, past the %s it read, and an older ack moved nothing\n' \
		"$reader" "$mark" "$read_to"
}

# And the @names inside a body, on the screen, as elements.
#
# Its own room, with its own three messages, because what it asserts is about
# the exact words in them - a check that read whatever the rest of the run had
# left in general would be asserting about a room that changes underneath it.
#
# They are said by A's AGENT and read as A, so the mention of A is a mention of
# whoever is reading, the mention of B is a mention of somebody else, and the
# difference between the two is the thing worth drawing. The agent speaks under
# A's handle - which is how the colour of a mention can be compared with the
# colour that person speaks in on the same page.
browser_draws_the_mentions() {
	recall
	api POST "$TOKEN_A_AGENT" /api/chat/mentions/say \
		"{\"body\": \"@$HANDLE_A the gearbox is stripped again\"}" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A_AGENT" /api/chat/mentions/say \
		"{\"body\": \"@$HANDLE_B please review, cc @nobody-at-all-here\"}" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	cd "$ROOT/web" || return 1
	node scripts/mention-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" mentions \
		"$HANDLE_A" "$HANDLE_B" nobody-at-all-here
}

# A URL somebody typed is a link, and the mention beside it survives. The second
# half is the point: linkifying by sending the body through the markdown
# renderer would work and would silently drop mention chips and span citations,
# which is why it happens on the plain path instead.
a_typed_url_is_a_link() {
	cd "$ROOT/web" || return 1
	node scripts/link-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" links "$HANDLE_A"
}

# Two waiters under one name share one cursor, so the second takes deliveries
# the first should have made and BOTH LOOK HEALTHY while somebody's room goes
# quiet. It silenced the orchestrator's watcher, and the finding that flowy
# inbox lacked the guard firecode chat already had is theirs.
#
# The check starts one, tries to start a second, and requires the refusal to
# name the pid holding it - because the only useful thing to tell somebody who
# just started a second waiter is which one is already theirs. It runs against
# the live node with a reader declared for the occasion.
a_second_waiter_for_one_name_is_refused() {
	recall
	local out first_pid
	# Its output is KEPT rather than sent to /dev/null. When this check first
	# ran, the first waiter died on something and the check could only report
	# that it had died - the one question worth answering was the one thrown
	# away.
	# Narrowed to a room nothing else in this run posts to, because a waiter
	# that WORKS is the failure here: a fresh reader wakes on the first thing
	# said anywhere, and the earlier checks fill several rooms, so the first
	# waiter delivered its messages and exited 0 before the second one started.
	# What this check needs is a waiter that is still holding the name.
	FLOWY_TOKEN="$TOKEN_A" "$ROOT/flowy" inbox --as gate-waiter --new \
		--room nothing-is-said-in-this-room \
		--url "http://127.0.0.1:$HTTP_PORT" --deadline 20 >"$WORK/waiter1.out" 2>&1 &
	first_pid=$!
	# Let it claim the name before racing it. Without this the check would pass
	# for the wrong reason on a slow machine: nothing to conflict with yet.
	sleep 2
	if ! kill -0 "$first_pid" 2>/dev/null; then
		printf 'the first waiter did not stay up, so nothing was tested. It said:\n%s\n' \
			"$(cat "$WORK/waiter1.out" 2>/dev/null)" >&2
		return 1
	fi
	out="$(FLOWY_TOKEN="$TOKEN_A" "$ROOT/flowy" inbox --as gate-waiter \
		--url "http://127.0.0.1:$HTTP_PORT" --deadline 20 2>&1)"
	local status=$?
	kill "$first_pid" 2>/dev/null
	wait "$first_pid" 2>/dev/null
	if [ "$status" -eq 0 ]; then
		printf 'a second waiter for one name started instead of being refused\n' >&2
		return 1
	fi
	case "$out" in
	*"already running (pid "*) ;;
	*)
		printf 'the refusal does not name the pid holding it:\n%s\n' "$out" >&2
		return 1
		;;
	esac
	printf '%s\n' "$out" | head -1
}

# ------------------------------------------------- the queue across projects
#
# The fleet drains this queue by starting a run per ready todo, and the queue was
# per project: a todo is a project-scoped artifact, so "the list" meant "the list
# in whichever project you were pointed at". One collaborative queue is the ask.
#
# THE UNION IS NOT THE RISK. Saying whose union it is, is. Todos are permission
# filtered, so "every project" is "every project THIS READER may read" - the
# operator sees the fleet and an agent sees its own work, and both call it "the
# list". Two readers of one name seeing two lists do not find out by talking;
# they find out hours later by disagreeing about whether a piece of work exists.
#
# So the pair below is the point of these checks, and it is chosen for its reach
# rather than for being two people. The operator's token is in pa, and pc has
# opened itself to pa, so it reads pa AND pc. Alice's second token is in pc and
# nothing has opened pa to pc, so it reads pc alone. Same page, same words, two
# different lists - and the page has to say which one it is showing.
#
# The narrow one is also SHOWN pa, on the same grant read backwards, which is
# what makes this pair discriminating rather than merely unequal: a scope line
# built on the project enumeration would have said "2 projects" to both of them.
CROSS_TODO_PA="the tailrace gate wants regrinding"
CROSS_TODO_PC="the feed pump wants a new impeller"
readonly CROSS_TODO_PA CROSS_TODO_PC

# Shared rather than the API's default, so what crosses the boundary is the
# grant and not the default. project-only is what the room panel's todos are
# written at, and none of those cross - see a_room_is_not_a_permission_axis.
two_projects_hold_a_todo_each() {
	recall
	# The edge this pair stands on, stated here rather than depended on. Phase
	# 6.5 issues the same grant for its own reasons, and pb_holds_a_grant_on_pa
	# restates phase 1's for exactly this reason: a section that reads a grant
	# some other check happened to leave behind fails somewhere else when that
	# check moves.
	api POST "$TOKEN_A_PC" /api/grants '{"from_project": "pa", "to_project": "pc"}' || return 1
	want_eq "grant status" "$API_STATUS" 200 || return 1

	api POST "$TOKEN_OP" /api/artifacts \
		"$(jq -nc --arg t "$CROSS_TODO_PA" \
			'{type: "memory", kind: "todo", status: "todo", visibility: "shared",
			  title: $t, body: "OWNER: a-millwright"}')" || return 1
	want_eq "the pa todo" "$API_STATUS" 200 || return 1
	want_eq "written into pa" "$(jqv .project)" pa || return 1
	api POST "$TOKEN_A_PC" /api/artifacts \
		"$(jq -nc --arg t "$CROSS_TODO_PC" \
			'{type: "memory", kind: "todo", status: "active", visibility: "shared",
			  title: $t, body: "OWNER: c-fitter"}')" || return 1
	want_eq "the pc todo" "$API_STATUS" 200 || return 1
	want_eq "written into pc" "$(jqv .project)" pc || return 1
	printf 'a todo in pa and a todo in pc, with pa reading pc\n'
}

# The cross-project read is the artifacts query with the project narrowing left
# OFF. Same door, same permission filter, no second path: a read that spanned
# projects through an endpoint of its own would be a second place for that filter
# to be missing, which is the shape of the finding this project has open.
the_queue_spans_projects_through_the_one_filter() {
	recall
	api GET "$TOKEN_OP" "/api/artifacts?type=memory&kind=todo&limit=1000" || return 1
	want_eq "the wide queue" "$API_STATUS" 200 || return 1
	want_todos "the wide queue" "$CROSS_TODO_PA" "$CROSS_TODO_PC" || return 1
	# Both projects are in one answer, which is the whole claim.
	want_eq "and it spans both projects" \
		"$(printf '%s' "$API_BODY" | jq '[.artifacts[]
			| select(.title == "'"$CROSS_TODO_PA"'" or .title == "'"$CROSS_TODO_PC"'")
			| .project] | unique | length')" 2 || return 1

	# And the same read, by the principal who reaches one project, hands back one
	# project's rows. The widening is the filter's to decide and not the query's.
	api GET "$TOKEN_A_PC" "/api/artifacts?type=memory&kind=todo&limit=1000" || return 1
	want_todos "the narrow queue" "$CROSS_TODO_PC" -- "$CROSS_TODO_PA" || return 1
	printf 'one read spans pa and pc; the same read from pc stops at pc\n'
}

# What the page is allowed to say it reaches, and the trap under it.
#
# The registry shows a project on a grant edge in EITHER direction, deliberately.
# Reading travels along one of them. The pc token is SHOWN pa - pa reads pc, and
# the enumeration reads that edge backwards too - and can read nothing in it, so
# a scope line built on the enumeration would have told it it reads two projects
# while handing it one project's rows. That is the exact lie the cross-project
# list exists to not tell, so `reads` is a separate, narrower list computed from
# the artifact filter itself.
the_reach_is_narrower_than_the_enumeration() {
	recall
	local shown reads
	api GET "$TOKEN_A_PC" /api/projects || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	shown="$(printf '%s' "$API_BODY" | jq '[.projects[] | select(.id == "pa")] | length')"
	reads="$(printf '%s' "$API_BODY" | jq '[.reads[] | select(. == "pa")] | length')"
	want_eq "the pc token is shown pa" "$shown" 1 || return 1
	want_eq "and reads nothing in it" "$reads" 0 || return 1
	want_eq "it reads its own project" \
		"$(printf '%s' "$API_BODY" | jq '[.reads[] | select(. == "pc")] | length')" 1 || return 1
	want_eq "and one project in all" \
		"$(printf '%s' "$API_BODY" | jq '.reads | length')" 1 || return 1

	api GET "$TOKEN_OP" /api/projects || return 1
	want_eq "the operator's token reads pa" \
		"$(printf '%s' "$API_BODY" | jq '[.reads[] | select(. == "pa")] | length')" 1 || return 1
	want_eq "and pc, along the grant pa holds" \
		"$(printf '%s' "$API_BODY" | jq '[.reads[] | select(. == "pc")] | length')" 1 || return 1
	# The same trap, the other way up: pb opened itself to pa, so pa is shown pb
	# and reads nothing in it either.
	want_eq "and nothing in the project that opened itself to it" \
		"$(printf '%s' "$API_BODY" | jq '[.reads[] | select(. == "pb")] | length')" 0 || return 1
	want_eq "which is shown all the same" \
		"$(printf '%s' "$API_BODY" | jq '[.projects[] | select(.id == "pb")] | length')" 1 || return 1
	printf 'pc is shown pa and reads only pc; pa reads pa and pc and not pb\n'
}

# The two labels, in a browser: the kind badge on a row and the two controls that
# narrow by kind and by tag. It runs against the rows the classification checks
# above left behind - one bug, one nobody classified, one tagged - because a
# filter is only shown to work by a page holding rows it must drop.
console_filters_the_queue_by_kind_and_tag() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/kind-filter-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$KIND_TODO" "$KIND_PLAIN" "$KIND_TAGGED" "$KIND_TAG"
}

# The page itself, in a browser, for both principals - the check that discovers a
# list which filters correctly and claims to be everything.
console_lists_the_queue_across_projects() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/crossproject-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" "$TOKEN_A_PC" \
		pa pc "$CROSS_TODO_PA" "$CROSS_TODO_PC"
}

# The empty answer, which is a statement and not a blank. Alice's token in pc
# reaches one project and no todo has been written into it YET - this runs before
# the seed above, which is the only moment in the run when a real principal's
# queue is legitimately empty. "no todos in the 1 project you can read" is a
# different sentence from "no todos", and both differ from being signed out.
console_says_which_empty_the_queue_is() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A_PC" \
		"no todos in the 1 project you can read" /todos
}

# ---------------------------------------------------- phase 3 console helpers

# The three steps of the frontend build, each its own check so a failure names
# which one. They run in the command substitution `check` puts them in, so the
# cd is theirs alone.
npm_ci() {
	cd "$ROOT/web" || return 1
	npm ci --no-audit --no-fund --prefer-offline --loglevel=error

	# A SUCCESSFUL npm ci CAN STILL LEAVE A BROKEN TREE, and this workspace is
	# where that bites. The layer keeps node_modules AND the npm cache between
	# runs, so a run killed mid-install leaves a truncated tarball in the cache;
	# --prefer-offline then serves that same truncated copy to every later run,
	# npm ci exits 0, and the package is a directory with a README and no entry
	# point. `classcat` was exactly that for hours: rollup could not resolve it,
	# and the failure named the import rather than the cache, so two people read
	# it as their own code being wrong. A third lost four checks to `fuse` the
	# same way, and I lost a morning to `jsdom`.
	#
	# deps-intact.mjs asks the question npm ls cannot: is the FILE there. npm ls
	# compares versions out of each package.json, so a package whose entry point
	# was truncated away still reports as installed - gutting classcat's
	# index.js leaves npm ls saying OK while the import fails, which is the
	# state we actually had. The repair drops --prefer-offline so it fetches
	# rather than re-reading what is already suspect, and it happens once: a
	# second failure is reported, not retried.
	if ! node scripts/deps-intact.mjs; then
		printf 'npm ci exited 0 but the tree does not resolve - treating the cache as poisoned\n'
		npm cache clean --force --loglevel=error 2>/dev/null || true
		rm -rf node_modules
		npm ci --no-audit --no-fund --loglevel=error || return 1
		if ! node scripts/deps-intact.mjs; then
			printf 'still incomplete after a clean fetch - this is not the cache\n'
			return 1
		fi
		printf 'repaired: a clean fetch resolves\n'
	fi

	# maxdepth 3, because a scoped package is node_modules/@scope/name and its
	# package.json is one level deeper than an unscoped one. At maxdepth 2 every
	# @scope/* package was invisible, so this line said "installed 98" in the
	# same breath as npm said "added 168" - two numbers for one install, which
	# read as a partial install to whoever was already suspicious of one.
	printf 'installed %s packages from package-lock.json\n' \
		"$(find node_modules -maxdepth 3 -name package.json | wc -l)"
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

# Signed out, the worklog says so rather than rendering an empty page.
#
# An empty list that means "you are not signed in" and an empty list that means
# "nothing happened" look identical, and the second is a false statement about a
# chronology. So the page has to say which, and this check runs with no node and
# no token at all - which is the state a browser is in when somebody opens the
# link for the first time.
console_says_the_worklog_needs_a_token() {
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "" "" "paste a token to read the worklog" /worklog
}

# The same, for the queue across projects, and it is the third of three empties
# there: signed out, read-and-empty, and reaching no project at all. A queue that
# renders blank when nobody is signed in reads as "there is no work", which is a
# statement about the fleet made by a page that asked nobody.
console_todos_signed_out() {
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "" "" "paste a token to read the queue" /todos
}

# A real browser for the two checks below, downloaded once per machine and
# cached in ~/.cache/ms-playwright after that. About 40 seconds and 115MB on a
# cold VM, nothing on a warm one.
#
# It is a check rather than a silent prerequisite so that "no browser" reads as
# a named failure instead of two checks quietly not running. A skipped check
# reports the same green as a passing one, which is how a browser assertion ends
# up not being an assertion at all.
browser_is_installed() {
	cd "$ROOT/web" || return 1
	# The download and the SHARED LIBRARIES IT NEEDS are two different things,
	# and the first one succeeding says nothing about the second. A plain
	# install put chrome-headless-shell on disk here and every launch died on
	# "libnspr4.so: cannot open shared object file" - a browser that is present,
	# verified present, and cannot start.
	#
	# --with-deps installs them, and it is apt, so it needs root. The test is
	# whether root is available WITHOUT A PROMPT, not whether we are already
	# root: the gate's VM runs as uid 1000 with passwordless sudo, so an
	# `id -u` = 0 test skipped the deps in the one place they were missing and
	# the browser stayed unlaunchable through two gate runs. `sudo -n` asks the
	# question that matters and answers it without hanging - on a workstation it
	# fails, no prompt appears mid-run, and the launch below decides, which is
	# right because a developer's machine usually has these libraries and a
	# fresh VM never does.
	if [ "$(id -u)" -eq 0 ] || sudo -n true 2>/dev/null; then
		npx --no-install playwright install --with-deps chromium
	else
		npx --no-install playwright install chromium
	fi
	# Launching it is the check. Installed and launchable are different claims,
	# and only the second one is the prerequisite the checks below actually have.
	if ! node -e 'import("playwright").then(async ({chromium}) => {
		const b = await chromium.launch()
		console.log("chromium", b.version(), "launches headless")
		await b.close()
	})'; then
		printf 'the browser is installed but will not start.\n' >&2
		printf 'If that is a missing system library, install them with:\n' >&2
		printf '  cd web && npx playwright install --with-deps chromium\n' >&2
		return 1
	fi
}

# A tab left open across a deploy runs code that has been replaced, and nothing
# on the screen says so. That is how the poll flood survived its own fix for
# days: the fix shipped, and the tab holding the bug never reloaded. This checks
# both halves of the answer - that a stale tab reloads, and that it reloads
# EXACTLY ONCE, because a reload that cannot fix the mismatch must not be tried
# again. A console that reloads forever is worse than one that is out of date.
a_stale_tab_reloads_itself_once() {
	cd "$ROOT/web" || return 1
	node scripts/fresh-check.mjs
}

# The regression check for a console that flooded its own node at 567 requests a
# second while every other check passed. It needs a node whose cursor never
# moves, because a correctly long-polling one paces a client that has no pacing
# of its own and hides the bug completely - so the fixture is the node, and it
# lives in scripts/standin-node.mjs beside the check.
poll_does_not_spin() {
	cd "$ROOT/web" || return 1
	node scripts/poll-spin-check.mjs
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

# ---------------------------------------------------------- message citations
#
# A reply can point at a message, and now at one SPAN of a message. What is
# stored is the span and never the quoted text, so every check below is really
# about the same claim: the quote a reader sees is DERIVED from the signed row
# it quotes, by the node, for that reader - so it cannot misquote, and it cannot
# be shown to somebody who could not read the source.
#
# It sits after phase 4 because the first check needs the one thing that makes a
# message readable across a project boundary without a project-wide grant: the
# task thread the assignment above opened. That is the only way to build the
# case that matters - a citing message the reader CAN read, of a cited message
# they CANNOT.

# say_citing TOKEN ROOM BODY CITE - a message that cites another. CITE is the
# JSON object as the API takes it, so a check can hand over a whole message or
# a span with the same helper.
say_citing() {
	api POST "$1" "/api/chat/$2/say" \
		"$(jq -nc --arg b "$3" --argjson c "$4" '{body: $b, cite: $c}')"
}

# The one that matters, and it is first for that reason.
#
# A citation crosses a boundary the message it cites does not: rooms are scoped
# by project and the log is not, so a reader will meet a citation of something
# they may not read - here through the tasks clause, which shows B one thread of
# a project B is not in and none of the rest of it.
#
# What B must get is the citation and NOT ONE WORD of what it quotes. The last
# clause is the assertion this whole feature turns on, and it is deliberately
# not about a field: the invented word is in the cited body and nowhere else in
# the run, so grepping the whole of what B was handed answers "did the text
# leave the node" without trusting the shape of the answer.
# THE WAITER CARRIES THE QUOTE, because the waiter is how every agent here
# reads. The room read has resolved citations all along and the inbox never
# asked, so an agent woken by a reply got its body and no sign of what it
# answered - and three of us, asked directly, described the design and got our
# own deliveries wrong.
#
# Asserted through the API rather than the CLI: the CLI renders the tag, but
# the field it renders from is what this is about, and a check that read the
# rendering would pass on a server that had stopped sending anything to render.
a_waiter_is_handed_the_quote_with_the_reply() {
	recall
	local quoted citing
	# THE READER FIRST, and the order is not cosmetic: a reader's cursor starts
	# where it is created, so messages said before it exists are behind it and
	# are never delivered. The first version of this check invented a name at
	# poll time, got "no such reader" - which has no events field at all - and
	# reported the fix broken when what was broken was the check.
	api POST "$TOKEN_B" /api/inbox/reader '{"as": "cite-waiter"}' || return 1

	api POST "$TOKEN_A" /api/chat/general/say \
		'{"body": "the marmalade gasket goes in dry"}' || return 1
	quoted="$(jqv .id)"
	api POST "$TOKEN_A" /api/chat/general/say \
		"$(jq -nc --arg m "$quoted" --arg to "$USER_B" \
			'{body: "agreed", to: $to, cite: {message: $m}}')" || return 1
	citing="$(jqv .id)"

	# B's waiter, which is the surface under test.
	api GET "$TOKEN_B" "/api/inbox/wait?as=cite-waiter&window=1" || return 1
	want_eq "the reply reached the waiter" \
		"$(jqv "[.events[] | select(.id == \"$citing\")] | length")" 1 || return 1
	want_eq "and it carries the quote" \
		"$(jqv "[.events[] | select(.id == \"$citing\") |
			select(.citation.text == \"the marmalade gasket goes in dry\")] | length")" 1
}

a_citation_of_a_message_you_cannot_read_hands_over_nothing() {
	recall
	local hidden citing
	# Said in pc, in a thread of its own. Nobody outside pc reads it, and the
	# artifact share and the task thread - the two doors out of pc - are not on
	# it.
	api POST "$TOKEN_A_PC" /api/chat/citations/say \
		'{"body": "the quillhammer bearing is scored right through"}' || return 1
	want_eq "the unreachable message" "$API_STATUS" 200 || return 1
	hidden="$(jqv .id)"
	api GET "$TOKEN_B" /api/chat/citations || return 1
	want_eq "what B reads of it" "$(chat_len ".id == \"$hidden\"")" 0 || return 1

	# And the citing message, in the thread the handoff opened, which B is a
	# party to. One message of pc reaches B; the one it quotes does not.
	api POST "$TOKEN_A_PC" /api/chat/citations/say \
		"$(jq -nc --arg t "$THREAD1" --arg m "$hidden" \
			'{body: "this is the half I meant", thread: $t, cite: {message: $m}}')" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	citing="$(jqv .id)"

	# A, who can read both, gets the quote - derived from the row, not stored.
	api GET "$TOKEN_A_PC" /api/chat/citations || return 1
	want_eq "A is quoted the message it cites" \
		"$(chat_len ".id == \"$citing\" and .citation.readable == true and
			.citation.text == \"the quillhammer bearing is scored right through\"")" 1 || return 1

	# B, who cannot, gets the citation and nothing out of the message.
	api GET "$TOKEN_B" /api/chat/citations || return 1
	want_eq "B reads the citing message" "$(chat_len ".id == \"$citing\"")" 1 || return 1
	want_eq "and is told plainly that the source is out of reach" \
		"$(chat_len ".id == \"$citing\" and .citation.readable == false")" 1 || return 1
	want_eq "with no quoted text on it" \
		"$(chat_len ".id == \"$citing\" and (.citation | has(\"text\") | not)")" 1 || return 1
	want_eq "and no speaker for it either" \
		"$(chat_len ".id == \"$citing\" and (.citation | (has(\"actor\") or has(\"name\")) | not)")" \
		1 || return 1
	case "$API_BODY" in
	*quillhammer*)
		printf 'the cited body reached B through the citation:\n%s\n' "$API_BODY" >&2
		return 1
		;;
	esac

	# The same claim through the other chat read, because a second door is where
	# a filter gets forgotten.
	api GET "$TOKEN_B" '/api/inbox?since=0' || return 1
	case "$API_BODY" in
	*quillhammer*)
		printf 'the cited body reached B through the inbox:\n%s\n' "$API_BODY" >&2
		return 1
		;;
	esac
	printf 'B reads the citation of %s and not a word of it\n' "$hidden"
}

# Both grains, there and back. The span is BYTES into the cited body, counted
# here the way the node counts them, so the check is asserting the same
# arithmetic the console does rather than a number somebody wrote down.
a_whole_message_and_a_part_of_one_both_round_trip() {
	recall
	local src whole part body prefix quote start end
	body="the flange is fine but the impeller is cracked"
	prefix="the flange is fine but "
	quote="the impeller is cracked"
	# Said by the operator and cited by A, so the citation is of somebody other
	# than the speaker carrying it - which is what the console has to draw, and
	# what a check on one person's messages could not tell apart.
	api POST "$TOKEN_OP" /api/chat/quotes/say "$(jq -nc --arg b "$body" '{body: $b}')" || return 1
	want_eq "the message being cited" "$API_STATUS" 200 || return 1
	src="$(jqv .id)"
	remember CITE_SOURCE "$src"

	say_citing "$TOKEN_A" quotes "agreed, and it is the second half" \
		"$(jq -nc --arg m "$src" '{message: $m}')" || return 1
	want_eq "citing the whole of it" "$API_STATUS" 200 || return 1
	want_eq "the row records the message and no span" "$(jqv .meta.cite)" "$src" || return 1
	whole="$(jqv .id)"

	start="$(printf '%s' "$prefix" | wc -c)"
	end="$((start + $(printf '%s' "$quote" | wc -c)))"
	say_citing "$TOKEN_A" quotes "no, only that part of it" \
		"$(jq -nc --arg m "$src" --argjson s "$start" --argjson e "$end" \
			'{message: $m, start: $s, end: $e}')" || return 1
	want_eq "citing one span of it" "$API_STATUS" 200 || return 1
	want_eq "the row records the span" "$(jqv .meta.cite)" "$src:$start:$end" || return 1
	part="$(jqv .id)"

	api GET "$TOKEN_A" /api/chat/quotes || return 1
	want_eq "the whole-message citation quotes the whole message" \
		"$(chat_len ".id == \"$whole\" and .citation.whole == true and
			.citation.text == \"$body\"")" 1 || return 1
	want_eq "and says who is being quoted" \
		"$(chat_len ".id == \"$whole\" and .citation.name == \"$HANDLE_OP\"")" 1 || return 1
	want_eq "the part citation quotes exactly the span" \
		"$(chat_len ".id == \"$part\" and .citation.whole == false and
			.citation.start == $start and .citation.end == $end and
			.citation.text == \"$quote\"")" 1 || return 1
	printf 'whole: %s, part: %s..%s of %s\n' "$body" "$start" "$end" "$src"
}

# A citation is a claim about somebody else's message, so it is checked the way
# an edge in the DAG and a raising message are: an id is a guess anybody can
# make, and one that is not here and one that is out of reach get the answer a
# read of it would give.
a_message_you_cannot_read_cannot_be_cited() {
	recall
	local hidden before after
	api POST "$TOKEN_A_PC" /api/chat/citations/say \
		'{"body": "another one nobody outside pc reads"}' || return 1
	want_eq "the unreachable message" "$API_STATUS" 200 || return 1
	hidden="$(jqv .id)"

	before="$(scalar "SELECT count(*) FROM events WHERE room = 'quotes'")" || return 1
	say_citing "$TOKEN_B" quotes "quoting what I cannot see" \
		"$(jq -nc --arg m "$hidden" '{message: $m}')" || return 1
	want_eq "status" "$API_STATUS" 404 || return 1
	case "$(jqv .error)" in
	*"not one you can read"*) ;;
	*)
		printf 'refused, but not as an unreadable message: %s\n' "$(jqv .error)" >&2
		return 1
		;;
	esac
	after="$(scalar "SELECT count(*) FROM events WHERE room = 'quotes'")" || return 1
	want_eq "messages the refusal wrote" "$((after - before))" 0 || return 1
	printf 'refused, and nothing written: %s\n' "$(jqv .error)"
}

# The span is checked against the body it is a span of, at the door. An offset
# that runs past the end, one that names no text, and one that cuts a character
# in half are all the same failure - a citation that could not derive a quote -
# and a node that stored them anyway would be a row that renders as a broken
# quote forever.
a_span_that_is_not_in_the_message_is_refused() {
	recall
	local src accented
	src="$CITE_SOURCE"
	want_status 400 POST "$TOKEN_A" /api/chat/quotes/say \
		"$(jq -nc --arg m "$src" '{body: "past the end", cite: {message: $m, start: 0, end: 9999}}')" ||
		return 1
	case "$(jqv .error)" in
	*"past the end"*) ;;
	*)
		printf 'an offset past the end was refused, but not as one: %s\n' "$(jqv .error)" >&2
		return 1
		;;
	esac
	want_status 400 POST "$TOKEN_A" /api/chat/quotes/say \
		"$(jq -nc --arg m "$src" '{body: "backwards", cite: {message: $m, start: 9, end: 4}}')" ||
		return 1

	# And the one a byte count gets wrong on real prose. The body holds a
	# two-byte character; a span that ends inside it would derive bytes that are
	# not text, which is a quote nobody can read rather than a quote of nobody.
	api POST "$TOKEN_A" /api/chat/quotes/say \
		'{"body": "the café is closed"}' || return 1
	want_eq "the accented message" "$API_STATUS" 200 || return 1
	accented="$(jqv .id)"
	want_status 400 POST "$TOKEN_A" /api/chat/quotes/say \
		"$(jq -nc --arg m "$accented" '{body: "half a letter", cite: {message: $m, start: 4, end: 8}}')" ||
		return 1
	case "$(jqv .error)" in
	*"in half"*) ;;
	*)
		printf 'a span cutting a character in half was refused, but not as one: %s\n' \
			"$(jqv .error)" >&2
		return 1
		;;
	esac
	# The span that stops on the boundary instead is fine, and quotes the word.
	say_citing "$TOKEN_A" quotes "that is the word" \
		"$(jq -nc --arg m "$accented" '{message: $m, start: 4, end: 9}')" || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	api GET "$TOKEN_A" /api/chat/quotes || return 1
	want_eq "the quote is the whole word" \
		"$(chat_len ".citation.text == \"café\"")" 1 || return 1
	printf 'past the end, backwards and mid-character all refused; the boundary span quotes cafe\n'
}

# A citation is the node's to write, exactly as the speaker keys and the
# resolved mentions beside it are. A client that could write its own would be
# putting a quotation of somebody else on a row that is correctly signed and
# correctly actored - which is the forgery this fabric already refuses through
# the front door.
a_client_cannot_write_its_own_citation() {
	recall
	api POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg m "$CITE_SOURCE" \
			'{type: "note", room: "quotes", body: "a citation nobody checked",
			  meta: {cite: ($m + ":0:3"), topic: "kept"}}')" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the client's citation" "$(jqv '.meta.cite // ""')" "" || return 1
	want_eq "and what meta is actually for is kept" "$(jqv .meta.topic)" kept || return 1
	printf 'a hand-written citation is stripped, and the rest of meta rides\n'
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

# The citation on the screen, in a browser, on the ELEMENT.
#
# It reads the room the round-trip check filled: the operator's message, A's
# reply citing the whole of it, and A's reply citing one span. What it asserts
# is the pair of things a half-built version gets wrong - a citation drawn as
# plain text under the citing speaker's name, and a "span" citation that quietly
# renders the whole body - so the span's quote has to be the span AND must not
# carry the half of the sentence that is outside it.
browser_draws_a_citation() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/cite-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" quotes \
		"$HANDLE_OP" "the impeller is cracked" "the flange is fine"
}

# The private page, mounted against the live node as each of the two principals
# who matter.
#
# As the addressee, the message is on the screen and the page says private - a
# console that painted a direct message identically to a room message would be a
# trap for whoever writes the next one.
#
# As the THIRD PRINCIPAL, the page loads, is signed in, is asking the same
# endpoint - and the message is not on it. That is the same claim the API check
# makes, made again at the surface a person actually reads, because "the API
# refused it" and "it is not on the screen" have been different answers before:
# a client that merged pages from two reads, or cached a page from another
# token, would leak it here and nowhere else.
console_renders_direct_messages() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_B" \
		"$DM_PRIVATE_WORD" /direct || return 1
	# The compose button belongs to this page and to no other, so it is the
	# presence half that makes the absence half mean something: without it,
	# "the message is not on the screen" would also be true of a page that
	# never painted.
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" \
		"send privately" /direct "$DM_PRIVATE_WORD"
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
#
# projects goes first and is the one table here that DOES replicate. It is
# copied anyway because it is what the other three point at: an agent's home and
# a token's scope are foreign keys into the registry, so handing node B alice
# without handing it pa would be handing it a principal in a project that does
# not exist there yet. It is the same bootstrap a restore does, and the rows
# arrive signed by node A, so the first sync merges them with itself and nothing
# moves.
copy_principals() {
	local from=$1 to=$2 table
	for table in projects users agents tokens; do
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

# ------------------------------------------------ the project entity, federated
#
# The registry replicates like every other row, and the whole reason it has to
# is that `project` is already inside the signed payload of everything that
# carries one: a node-local registry would leave the referent local while every
# reference to it is federated.
#
# Two things can drift here and only here, and a silent drift in either is
# indistinguishable from working:
#
#   - RECONCILE. Two nodes declare the same project independently, with no
#     contact. They must end up with ONE identity, not two, and not by accident.
#   - COLLISION. Two nodes declare the same NAME for two different projects.
#     They must NOT silently become one. The git remote is what makes that
#     decidable rather than a judgement call, and an operator pin is what
#     settles it.
#
# The deltas here are moved by hand rather than by `flowy sync`, and deliberately
# so: the driver's cursors are per peer, and a check that wedges one on purpose
# would leave every later check syncing from a bookmark this one moved. So each
# check pulls a page from one node and pushes exactly the project rows out of it
# at the other, which is the same merge through the same door.

# declare5 PORT TOKEN NAME ORIGIN - declare a project on one of the two nodes.
declare5() {
	local port=$1 token=$2 name=$3 origin=$4
	want_napi 200 "$port" POST "$token" /api/projects \
		"$(jq -nc --arg id "$name" --arg o "$origin" '{id: $id, origin: $o}')"
}

# token_in_project DSN TOKEN USER PROJECT - a bearer token for one project, on
# one node, written the way an operator writes one: straight into the local
# tokens table, because tokens are local credentials and never replicate.
#
# The user is the one FLOWY_PEERS names, so this principal may push; the project
# is the one being tested, so the merge's reach test sees a principal that is in
# it. Both halves matter, and they are two different rules.
token_in_project() {
	psql -v ON_ERROR_STOP=1 -q -d "$1" \
		-c "INSERT INTO tokens (token, user_id, project) VALUES ('$2', '$3', '$4')
		    ON CONFLICT (token) DO UPDATE SET user_id = excluded.user_id,
		                                      project = excluded.project"
}

# move_projects FROM_PORT TO_PORT TOKEN - pull a page from one node and push
# only its project rows at the other, with the identities that verify them. The
# result of the merge lands in API_BODY, so a caller reads .refused and .reasons
# off it.
move_projects() {
	local from=$1 to=$2 token=$3 delta
	napi "$from" GET "$token" '/api/sync/pull?since=0' || return 1
	want_eq "the pull answered" "$API_STATUS" 200 || return 1
	delta="$(printf '%s' "$API_BODY" | jq -c '{artifacts: [], events: [], tasks: [],
	          grants: [], projects: (.projects // []), identities: (.identities // []),
	          hwm: .hwm}')" || return 1
	napi "$to" POST "$token" /api/sync/push "$delta" || return 1
	want_eq "the push was accepted as a delta" "$API_STATUS" 200 || return 1
}

# project5 DSN NAME COLUMN - one column of one registry row on one node.
project5() { scalar5 "$1" "SELECT coalesce($3::text, '') FROM projects WHERE id = '$2'"; }

# Two nodes, one project, declared on each with no contact between them - and
# spelled differently, because a remote has three spellings and an identity that
# is three strings is not an identity. They converge on one row: same origin,
# same winner, one row each side.
two_nodes_declaring_one_project_converge() {
	recall5
	local name=shared-repo token=tshared-repo
	declare5 "$N5_PORT_A" "$N5_TOKEN_OP" "$name" 'git@github.com:acme/shared.git' || return 1
	declare5 "$N5_PORT_B" "$N5_TOKEN_OP" "$name" 'https://github.com/acme/shared' || return 1
	want_eq "node A canonicalised the remote" \
		"$(project5 "$N5_DSN_A" "$name" origin)" git:github.com/acme/shared || return 1
	want_eq "and node B reached the same string from the other spelling" \
		"$(project5 "$N5_DSN_B" "$name" origin)" git:github.com/acme/shared || return 1

	token_in_project "$N5_DSN_A" "$token" "$N5_USER_B" "$name" || return 1
	token_in_project "$N5_DSN_B" "$token" "$N5_USER_B" "$name" || return 1

	move_projects "$N5_PORT_A" "$N5_PORT_B" "$token" || return 1
	want_eq "nothing was refused" "$(printf '%s' "$API_BODY" | jq '[.refused[]] | add')" 0 || return 1
	move_projects "$N5_PORT_B" "$N5_PORT_A" "$token" || return 1
	want_eq "nothing was refused the other way either" \
		"$(printf '%s' "$API_BODY" | jq '[.refused[]] | add')" 0 || return 1

	# One row, not two, on each node - and the same one, which is what
	# "converge" has to mean when the merge key is the name itself.
	local rows_a rows_b node_a node_b
	rows_a="$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM projects WHERE id = '$name'")" || return 1
	rows_b="$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM projects WHERE id = '$name'")" || return 1
	want_eq "rows on A" "$rows_a" 1 || return 1
	want_eq "rows on B" "$rows_b" 1 || return 1
	node_a="$(project5 "$N5_DSN_A" "$name" node)" || return 1
	node_b="$(project5 "$N5_DSN_B" "$name" node)" || return 1
	want_eq "both nodes hold the same winner" "$node_a" "$node_b" || return 1
	printf 'two independent declarations of %s converged on one row, written by %s\n' \
		"$name" "$node_a"
}

# The other half, and the failure the name alone could never catch: two
# genuinely different projects that are both called `flowy`. They have different
# remotes, so the merge can say so - and it refuses rather than folding two
# teams' work under one name.
two_projects_with_one_name_are_refused_not_merged() {
	recall5
	local name=flowy token=tflowy-collide
	declare5 "$N5_PORT_A" "$N5_TOKEN_OP" "$name" 'git@github.com:acme/flowy.git' || return 1
	declare5 "$N5_PORT_B" "$N5_TOKEN_OP" "$name" 'git@github.com:someone-else/flowy.git' || return 1
	token_in_project "$N5_DSN_A" "$token" "$N5_USER_B" "$name" || return 1
	token_in_project "$N5_DSN_B" "$token" "$N5_USER_B" "$name" || return 1

	move_projects "$N5_PORT_A" "$N5_PORT_B" "$token" || return 1
	if [ "$(printf '%s' "$API_BODY" | jq '.refused.projects')" -lt 1 ]; then
		printf 'node B took a project row from a different repository: %s\n' "$API_BODY" >&2
		return 1
	fi
	case "$(printf '%s' "$API_BODY" | jq -r '.reasons | join(" ")')" in
	*"two projects with one name"*) ;;
	*)
		printf 'the refusal does not say what it refused: %s\n' \
			"$(printf '%s' "$API_BODY" | jq -r '.reasons | join(" ")')" >&2
		return 1
		;;
	esac
	want_eq "and node B still means its own repository" \
		"$(project5 "$N5_DSN_B" "$name" origin)" git:github.com/someone-else/flowy || return 1
	printf 'refused, not merged: %s\n' \
		"$(printf '%s' "$API_BODY" | jq -r '.reasons[0]')"
}

# And the way out of it, which is the precedent this fabric already uses for a
# node's key: the operator says by hand which project this name means here.
# After the pin, the row that was refused is the same project as the pinned one
# and merges like any other.
an_operator_pin_settles_the_collision() {
	recall5
	local name=flowy token=tflowy-collide
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_OP" /api/projects \
		"$(jq -nc '{id: "flowy", origin: "git@github.com:acme/flowy.git", pin: true}')" || return 1
	want_eq "the pin took" "$(jqv .project.provenance)" pinned || return 1
	want_eq "and kept what it superseded" \
		"$(printf '%s' "$API_BODY" | jq -r '.project.superseded[0]')" \
		git:github.com/someone-else/flowy || return 1

	move_projects "$N5_PORT_A" "$N5_PORT_B" "$token" || return 1
	want_eq "nothing is refused once the operator has said which project this is" \
		"$(printf '%s' "$API_BODY" | jq '.refused.projects')" 0 || return 1
	want_eq "and the name means the pinned repository here" \
		"$(project5 "$N5_DSN_B" "$name" origin)" git:github.com/acme/flowy || return 1
	want_eq "and the row is still pinned, a merge later" \
		"$(project5 "$N5_DSN_B" "$name" provenance)" pinned || return 1
	printf 'the pin settled it, and the chain still holds what it superseded\n'
}

# ---------------------------------------------------------------- phase 6.5
#
# Row signing. Every replicated row carries the signature of the node that
# wrote it, and a merge verifies that before it asks anything else. Two things
# follow for the gate:
#
#   - the two nodes have to know each other's public key, which is an operator's
#     job and is done here the way an operator does it: read the key off one
#     machine, pin it on the other. See the_nodes_exchange_keys.
#   - a delta assembled by hand is a delta nothing signed, and a node will not
#     merge one. The checks that hand a node rows it should refuse for some
#     other reason - a forged grant, a re-pointed task - sign them properly
#     first, so that what they are testing is still the check they were written
#     for and not the new one. `flowy sign` is what does it.

# node_key DSN NODE - the public key a node signs its rows with, in hex.
node_key() {
	DATABASE_URL="$1" FLOWY_NODE="$2" "$ROOT/flowy" identity | jq -r .public_key
}

# pin_key DSN NODE PEER KEY - one node's operator recording another node's key.
pin_key() {
	DATABASE_URL="$1" FLOWY_NODE="$2" "$ROOT/flowy" identity pin --node "$3" --key "$4"
}

# sign5 DSN NODE - sign a delta read on stdin with that node's own stored key.
# The node column of each row is left alone: a delta whose rows name a node this
# key does not belong to is a forgery, and building one is how the refusal is
# tested.
sign5() { DATABASE_URL="$1" FLOWY_NODE="$2" "$ROOT/flowy" sign; }

# sign_seed SEED [NODE] - sign a delta read on stdin as the node a seed makes,
# for the checks that need to be a node with no database of its own. With a node
# name, the delta also carries that node's own self-signed identity, which is
# how a page from a node carries the key that verifies it.
sign_seed() {
	if [ -n "${2:-}" ]; then
		"$ROOT/flowy" sign --seed "$1" --node "$2" --identity
		return
	fi
	"$ROOT/flowy" sign --seed "$1"
}

# seed_of NAME - a 32 byte hex seed derived from a name, so a check's stand-in
# node has the same key every run.
seed_of() { printf '%s' "$1" | sha256sum | cut -c1-64; }

# key_of NODE SEED - the public key of the node a seed makes.
key_of() { "$ROOT/flowy" identity keygen --node "$1" --seed "$2" | jq -r .public_key; }

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

# The out-of-band half of federation, done the way an operator does it: the key
# is read off one machine and pinned on the other, over a channel that is not
# the one being secured. Nothing is taken on trust here - both ends are pinned -
# so what the rest of the phase exercises is two nodes that know each other.
the_nodes_exchange_keys() {
	recall5
	local a b
	a="$(node_key "$N5_DSN_A" nodeA)" || return 1
	b="$(node_key "$N5_DSN_B" nodeB)" || return 1
	if [ -z "$a" ] || [ "$a" = null ] || [ "$a" = "$b" ]; then
		printf 'the two nodes report keys %s and %s\n' "${a:-<none>}" "${b:-<none>}" >&2
		return 1
	fi
	pin_key "$N5_DSN_B" nodeB nodeA "$a" >/dev/null || return 1
	pin_key "$N5_DSN_A" nodeA nodeB "$b" >/dev/null || return 1

	want_eq "nodeA's key, pinned on B" \
		"$(scalar5 "$N5_DSN_B" \
			"SELECT count(*) FROM node_identity WHERE node_id = 'nodeA' AND pinned")" 1 || return 1
	want_eq "nodeB's key, pinned on A" \
		"$(scalar5 "$N5_DSN_A" \
			"SELECT count(*) FROM node_identity WHERE node_id = 'nodeB' AND pinned")" 1 || return 1
	# And neither of them handed over a private key doing it.
	want_eq "private keys on B that are not B's own" \
		"$(scalar5 "$N5_DSN_B" \
			"SELECT count(*) FROM node_identity WHERE private_key IS NOT NULL AND node_id <> 'nodeB'")" \
		0 || return 1
	printf 'nodeA signs with %s, nodeB with %s, each pinned on the other\n' \
		"$(printf '%s' "$a" | cut -c1-16)" "$(printf '%s' "$b" | cut -c1-16)"
}

# A row that crossed the fabric carries the signature of the node that wrote it,
# byte for byte: the merge on the far side would not have taken it otherwise,
# and what is stored there is what was verified rather than a re-stamp.
the_replicated_rows_carry_their_authors_signature() {
	recall5
	local id here there
	id="$(scalar5 "$N5_DSN_B" \
		"SELECT id FROM artifacts WHERE node = 'nodeA' AND sig IS NOT NULL ORDER BY hlc LIMIT 1")" ||
		return 1
	if [ -z "$id" ]; then
		printf 'nodeB holds no signed row of nodeA at all\n' >&2
		return 1
	fi
	here="$(scalar5 "$N5_DSN_A" "SELECT encode(sig, 'hex') FROM artifacts WHERE id = '$id'")" || return 1
	there="$(scalar5 "$N5_DSN_B" "SELECT encode(sig, 'hex') FROM artifacts WHERE id = '$id'")" || return 1
	want_eq "the signature on the row that travelled" "$there" "$here" || return 1
	want_eq "rows of nodeA's on B with no signature at all" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE node = 'nodeA' AND sig IS NULL")" \
		0 || return 1
	want_eq "events of nodeA's on B with no signature" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE node = 'nodeA' AND sig IS NULL")" \
		0 || return 1
	# And the other direction, so this is about replication rather than about
	# one node's writes.
	want_eq "rows of nodeB's on A with no signature" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM artifacts WHERE node = 'nodeB' AND sig IS NULL")" \
		0 || return 1
	printf 'artifact %s is on both nodes under the same signature\n' "$id"
}

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
	# A tombstone is a row and stays one, on both nodes alike: what the delete
	# removes is the artifact from every view, and what it leaves behind is the
	# fact in the table, which is how the delete replicates at all. So the API
	# answers 410 on both nodes - a deleted artifact is not there to read, and a
	# reader who could have read it is told so rather than told it never was - and
	# the row is what the second client sees.
	# 410 on both nodes now, not 404: the reader could read the row, so it is told
	# the row was withdrawn rather than told it never existed - and a tombstone
	# that replicated as a fact should read as one on the peer too, which is the
	# whole reason it travels.
	want_napi 410 "$N5_PORT_A" GET "$N5_TOKEN_A" "/api/artifact/$SHARED_ID" || return 1
	want_napi 410 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/artifact/$SHARED_ID" || return 1
	want_eq "the delete on A" \
		"$(scalar5 "$N5_DSN_A" "SELECT tombstone FROM artifacts WHERE id = '$SHARED_ID'")" t || return 1
	want_eq "at the delete's reading on A" \
		"$(scalar5 "$N5_DSN_A" "SELECT hlc FROM artifacts WHERE id = '$SHARED_ID'")" \
		"$TOMB_HLC" || return 1
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

# The row is written as the replication principal itself, because a push
# writes the pusher's own rows - checkArtifact says so for a row that is
# already here and, since the sixth round, for one that is not here yet either.
# Somebody else's rows reach the peer by being pulled, which is the other half
# of the same exchange. What this check is about is the second push: the same
# delta, received again and applied to nothing.
pushing_the_same_delta_twice_applies_it_once() {
	recall5
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_B" /api/artifacts \
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
#
# Those control routes are the operator's, so the token the mock_* helpers are
# handed is the operator's throughout: being the forge is being the machine, not
# being one of the people on it - see the LOW/MED check below.

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
	want_eq "the node reports the phase" "$(jqv .phase)" 9 || return 1
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
	mock_state "$TOKEN_OP" o/r "$ISSUE" closed || return 1
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
	mock_comment "$TOKEN_OP" o/r "$ISSUE" reviewer "does it flimberwock at 3000rpm?" || return 1
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

	mock_issue "$TOKEN_OP" o/r "$ISSUE" || return 1
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
	mock_issue "$TOKEN_OP" o/r "$ISSUE" || return 1
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
	mock_issue "$TOKEN_OP" o/r "$ISSUE" || return 1
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
	# Signed by nodeA, which is a node B holds the key of: the row really was
	# written by the peer it says wrote it, and it is still not a row that peer
	# may write. Authenticity passes and authorisation refuses.
	delta="$(printf '{"artifacts":[],"events":[],"tasks":[],"grants":[{"id":"%s","from_project":"pb","to_project":"pc","cap":"read","granted_by":"%s","hlc":%d,"node":"nodeA","tombstone":false}],"hwm":0}' \
		"$id" "$N5_USER_B" "$((hlc + 65536))" | sign5 "$N5_DSN_A" nodeA)" || return 1

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
	delta="$(printf '{"artifacts":[{"id":"poison-%s","type":"note","project":"pb","owner_user":"%s","title":"poison","body":"","visibility":"project","hlc":9223372036854775807,"node":"nodeA","tombstone":false}],"events":[],"tasks":[],"grants":[],"hwm":0}' \
		"$$" "$N5_USER_B" | sign5 "$N5_DSN_A" nodeA)" || return 1
	want_napi 400 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	# Properly signed and still refused whole: a reading no clock could have
	# made is not a row to check, it is a delta to throw out.
	want_eq "rows it wrote" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = 'poison-$$'")" 0 || return 1

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
	api POST "$TOKEN_OP" /api/forge/mock/fail '{"after": 1}' || return 1
	want_eq "the mock is armed to refuse" "$API_STATUS" 200 || return 1
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the sync reports the refusal" "$API_STATUS" 502 || return 1
	mock_issue "$TOKEN_OP" o/r "$num" || return 1
	want_eq "comments on the issue" \
		"$(jqv '[.comments[] | select(.author == "flowy")] | length')" 1 || return 1

	# The forge is up again: the one that was refused and the one behind it go
	# out, and the one that arrived does not arrive again.
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the second sync" "$API_STATUS" 200 || return 1
	want_eq "replies it sent" "$(jqv .pushed)" 2 || return 1
	mock_issue "$TOKEN_OP" o/r "$num" || return 1
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
#
# The ninth round closed the same hole one step earlier: a parent nobody can
# read is not a parent, so the message is refused rather than quietly opening a
# thread of its own. What this check is about is unchanged - pc's conversation
# does not gain a speaker who cannot read it - and it now asserts the refusal,
# because the reply that used to land was a message with an edge to an event its
# writer had only guessed at.
a_reply_does_not_adopt_an_unreadable_thread() {
	recall
	# CHAT_PC is a message in pc, and the conversation it is in is pc's. B holds
	# a grant into pa and none into pc, so B cannot read either.
	api GET "$TOKEN_A_PC" /api/chat/general || return 1
	local hidden was
	hidden="$(jqv "[.events[] | select(.id == \"$CHAT_PC\")][0].thread")"
	if [ -z "$hidden" ] || [ "$hidden" = null ]; then
		printf 'could not find the thread of %s in pc\n' "$CHAT_PC" >&2
		return 1
	fi
	api GET "$TOKEN_B" "/api/events?thread=$hidden" || return 1
	want_eq "what B can read of that thread" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 0 || return 1
	was="$(scalar "SELECT count(*) FROM events WHERE thread = '$hidden'")" || return 1

	want_status 400 POST "$TOKEN_B" /api/chat/general/say \
		"$(jq -nc --arg p "$CHAT_PC" '{body: "answering something I cannot see", parents: [$p]}')" ||
		return 1

	# Nothing landed anywhere: not in pc's conversation, and not in a thread of
	# its own with an edge into one.
	want_eq "messages in pc's thread afterwards" \
		"$(scalar "SELECT count(*) FROM events WHERE thread = '$hidden'")" "$was" || return 1
	want_eq "rows anywhere naming that message as a parent" \
		"$(scalar "SELECT count(*) FROM events WHERE '$CHAT_PC' = ANY(parents)")" 0 || return 1
	printf 'a reply to a message the speaker cannot read was refused, and thread %s is as it was\n' \
		"$hidden"
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
		   actor: $a, artifact: "", seq_hlc: $h1, node: "nodeA", body: "open->done"},
		  {id: $m, type: "chat", project: "pb", room: "pb/bugs", thread: $m, parents: [],
		   actor: $b, artifact: "", seq_hlc: $h2, node: "nodeA",
		   body: "this one really is mine"}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1

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
		   body: "taken over by a reader", visibility: "project", hlc: $h, node: "nodeA",
		   tombstone: false, reported: false}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1
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
	local id gid hlc delta seed
	# A's artifact, in A's project, owned by A and shared with nobody.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"note","title":"A owns this one too","body":"thrimblewick"}' || return 1
	id="$(jqv .id)"
	want_eq "who owns it" "$(jqv .owner_user)" "$N5_USER_A" || return 1
	gid="forged-grant-$$-$(date +%s)"
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	# Written on a node nobody here pinned, whose key rides in on the page with
	# it, which is what a peer inventing a grantor actually has: signing as a
	# node the operator pinned takes that node's private key, and the whole
	# point of pinning is that somebody decided to believe what that node says
	# about who did what. So the row is authentic - the signature verifies, the
	# key is taken on first use - and the grantor on it is still nobody this
	# node has any reason to believe. The same share authored on a pinned node
	# is a relay of A's own grant and lands at either door: see the fourteenth
	# round.
	seed="$(seed_of share-forger)" || return 1
	delta="$(jq -nc --arg g "$gid" --arg i "$id" --arg s "$N5_USER_B" --arg o "$N5_USER_A" \
		--argjson h "$hlc" '
		{artifacts: [], events: [], tasks: [], hwm: 0, grants: [
		  {id: $g, from_project: "pa", to_project: "pa", subject: $s, artifact: $i,
		   cap: "read", granted_by: $o, hlc: $h, node: "share-forger",
		   tombstone: false}]}' | sign_seed "$seed" share-forger)" || return 1

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
		   assignee_agent: "", thread: $th, hlc: $h,
		   node: "nodeA"}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1

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
		mock_comment "$TOKEN_OP" o/r "$num" reviewer "inbound $i about the sunroof" || return 1
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
	api POST "$TOKEN_OP" /api/forge/mock/login '{"login":"flowy-bot"}' || return 1
	want_eq "the forge is logged in as" "$(jqv .login)" flowy-bot || return 1

	id="$(new_artifact "$TOKEN_A_PC" bug "the horn sticks in the cold")" || return 1
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filed" "$API_STATUS" 200 || return 1
	want_eq "the login the link records" "$(jqv .external.author)" flowy-bot || return 1
	num="$(jqv .external.number)"

	mock_comment "$TOKEN_OP" o/r "$num" flowy-bot "this one came from this node" || return 1
	mock_comment "$TOKEN_OP" o/r "$num" reviewer "and this one did not" || return 1
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "the sync" "$API_STATUS" 200 || return 1
	want_eq "comments it threaded in" "$(jqv .pulled)" 1 || return 1
	want_eq "and who said the one it took" "$(jqv '.events[0].actor')" "forge:reviewer" || return 1

	# Back to the name the rest of the run uses.
	api POST "$TOKEN_OP" /api/forge/mock/login '{"login":"flowy"}' || return 1
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
	api POST "$TOKEN_OP" /api/forge/mock/on-file \
		'{"author":"reviewer","body":"answered while the issue was being opened"}' || return 1
	want_eq "the forge is armed" "$(jqv .armed)" true || return 1

	id="$(new_artifact "$TOKEN_A_PC" bug "the indicator ticks out of time")" || return 1
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filed" "$API_STATUS" 200 || return 1
	num="$(jqv .external.number)"
	mock_issue "$TOKEN_OP" o/r "$num" || return 1
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

# ------------------------------------------------------------ phase 7 fuse
#
# The mount is a real mount. There is a /dev/fuse in this VM and a fusermount3
# on PATH, so these checks attach a filesystem to a directory, write files into
# it with the shell, read them back, delete one, and unmount - and then ask the
# store, over psql and over the API, whether what happened to the files
# happened to the memory. A gate that exercised the tree in process would be
# testing the tree and not the mount.

# fuse_needs_telling: the command mounts nothing unless it is told where, and
# refuses a token it cannot resolve. Both are exit codes rather than mounts.
fuse_needs_telling() {
	local out
	if out="$(./flowy fuse 2>&1)"; then
		printf 'flowy fuse with no arguments succeeded, printing %q\n' "$out" >&2
		return 1
	fi
	case "$out" in
	*--mount*) ;;
	*)
		printf 'flowy fuse said %q, want it to name --mount\n' "$out" >&2
		return 1
		;;
	esac
	local first="$out"

	mkdir -p "$WORK/never-mounted"
	if out="$(DATABASE_URL="$DATABASE_URL" ./flowy fuse --mount "$WORK/never-mounted" \
		--token no-such-token 2>&1)"; then
		printf 'flowy fuse mounted with an unknown token\n' >&2
		return 1
	fi
	if fuse_is_mounted "$WORK/never-mounted"; then
		printf 'a refused mount left a filesystem attached\n' >&2
		return 1
	fi
	printf '%s / %s\n' "$first" "$out"
}

# fuse_is_mounted PATH - is there a fuse filesystem attached there.
#
# By the type field, not by looking for the word: /proc/self/mounts also holds
# fusectl at /sys/fs/fuse/connections, and a grep for "fuse" finds that on every
# machine that has ever loaded the module.
fuse_is_mounted() {
	awk -v want="$1" '$2 == want && $3 ~ /^fuse\./ { found = 1 } END { exit !found }' \
		/proc/self/mounts
}

# fuse_mounts_here - the fuse filesystems attached under $WORK, one per line.
# Phase 7 mounts there and nothing else in this run does.
fuse_mounts_here() {
	awk -v work="$WORK/" '$3 ~ /^fuse\./ && index($2, work) == 1 { print $2 }' /proc/self/mounts
}

# fuse_unmount PATH - detach, whatever state it is in.
fuse_unmount() {
	fusermount3 -u "$1" >/dev/null 2>&1 ||
		fusermount -u "$1" >/dev/null 2>&1 ||
		true
}

# fuse_start MOUNT TOKEN LOG PIDFILE [flags...] - mount in the background and
# wait for the kernel to have finished the handshake.
#
# The pid goes in a file because every check runs inside a command substitution
# of its own, so a variable set in one is gone by the next; the log goes in a
# file because a background process holding the substitution's stdout open is a
# check that never returns.
fuse_start() {
	local mountpoint=$1 token=$2 log=$3 pidfile=$4
	shift 4
	mkdir -p "$mountpoint"
	DATABASE_URL="$DATABASE_URL" FLOWY_NODE=gate \
		./flowy fuse --mount "$mountpoint" --token "$token" "$@" >"$log" 2>&1 &
	printf '%s\n' "$!" >"$pidfile"

	local i
	for i in $(seq 1 100); do
		if [ -d "$mountpoint/_personal" ]; then
			return 0
		fi
		sleep 0.1
	done
	printf 'the mount at %s never came up:\n' "$mountpoint" >&2
	indent <"$log" >&2
	return 1
}

# fuse_stop MOUNT PIDFILE - SIGTERM, wait, and make sure nothing is left
# attached. This is the ordinary unmount: the process is asked to go and does
# its own fusermount.
fuse_stop() {
	local mountpoint=$1 pidfile=$2 pid="" i
	[ -f "$pidfile" ] && pid="$(cat "$pidfile")"
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill -TERM "$pid" 2>/dev/null
		for i in $(seq 1 100); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
	fi
	fuse_unmount "$mountpoint"
	rm -f "$pidfile"
	if fuse_is_mounted "$mountpoint"; then
		printf '%s is still mounted after being asked to stop\n' "$mountpoint" >&2
		return 1
	fi
}

# fuse_kill MOUNT PIDFILE - the crash. SIGKILL leaves the kernel holding a
# connection to a server that is gone, which is exactly the state a node that
# died mid-write comes back from, so the mountpoint is cleared by hand
# afterwards the way a person would have to.
fuse_kill() {
	local mountpoint=$1 pidfile=$2 pid="" i
	[ -f "$pidfile" ] && pid="$(cat "$pidfile")"
	if [ -n "$pid" ]; then
		kill -9 "$pid" 2>/dev/null
		for i in $(seq 1 100); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
	fi
	fuse_unmount "$mountpoint"
	rm -f "$pidfile"
}

# fuse_await SQL - the write is behind, so the assertion waits for it: poll a
# counting query until it says at least one, for ten seconds.
fuse_await() {
	local i n
	for i in $(seq 1 100); do
		n="$(psql -v ON_ERROR_STOP=1 -tAc "$1")" || return 1
		if [ -n "$n" ] && [ "$n" -ge 1 ]; then
			printf '%s\n' "$n"
			return 0
		fi
		sleep 0.1
	done
	printf 'nothing ever satisfied this within ten seconds:\n%s\n' "$1" >&2
	return 1
}

# ------------------------------------------------------- whose word a row is
#
# The accept-side impersonation hole, and the thing that closes it.
#
# A row carries the signature of the node that wrote it, and the merge checked
# that signature and then believed what the row said about who WROTE it. Those
# are two different claims. Pinning a peer's node key is agreeing to carry what
# it relays - which is what federation IS, since a relay's whole job is other
# people's rows - and it was being read as agreeing to whatever that peer said
# about authorship. So a pinned peer could push chat as this node's own alice,
# in alice's room, and every surface here rendered it as alice's own word.
#
# The fix is a second key: rows authored by a principal are signed by that
# PRINCIPAL, and the node signature stays beside it as the relay envelope. Two
# claims, two keys. A principal carries an EPOCH - a clock reading - and from it
# a row naming them without their signature is refused; below it, rows are taken
# as they always were and shown as attributed rather than as that person's own
# word. That epoch is why this is not the home-node floor that had to be
# reverted: nothing already in either store stops replicating, and the rule
# applies to one principal at a time, from the reading their key was made at.
#
# What it buys, exactly: the trust boundary moves from any-pinned-node to the
# one node holding that principal's key. Not "forgery is impossible" - the node
# holding alice's key can still write as alice, because that is what holding a
# key means.
#
# These run last of the federated checks, and deliberately: from here on node A
# and node B both hold alice's epoch, so a row of hers that nothing signed is a
# row they refuse, and every earlier check hands them exactly that.

# principal_seed NAME - the 32 byte seed node A mints a principal's key from.
# Derived from the name so the gate holds the private half as well, which is
# what lets the checks below produce a signature that verifies AND one that does
# not, over otherwise identical rows.
principal_seed() { seed_of "phase5 principal $1"; }

# sign5_as DSN NODE PRINCIPAL SEED - sign a delta read on stdin twice: as the
# node, the way sign5 does, and as the principal whose rows it carries.
sign5_as() {
	DATABASE_URL="$1" FLOWY_NODE="$2" "$ROOT/flowy" sign --as "$3" --principal-seed "$4"
}

# The provisioning, done the way an operator does it and out of band: keygen on
# the node the principal writes from, pin on the node that receives their rows.
# Nothing about a principal key travels on a page - a key a relay could serve
# would be an authorship a relay could grant itself, which is the hole.
a_principal_gets_a_key_on_the_node_it_writes_from() {
	recall5
	local out key epoch
	out="$(DATABASE_URL="$N5_DSN_A" "$ROOT/flowy" principal keygen --node nodeA \
		--as "$N5_USER_A" --seed "$(principal_seed "$N5_USER_A")")" || return 1
	key="$(printf '%s' "$out" | jq -r .public_key)"
	epoch="$(printf '%s' "$out" | jq -r .epoch)"
	if [ -z "$key" ] || [ -z "$epoch" ] || [ "$epoch" = "null" ]; then
		printf 'keygen answered %s\n' "$out" >&2
		return 1
	fi
	want_eq "node A holds the private half" \
		"$(printf '%s' "$out" | jq -r .local)" true || return 1

	# Node B gets the public half and the epoch, and nothing else: it can check
	# alice's rows and it cannot write one.
	DATABASE_URL="$N5_DSN_B" "$ROOT/flowy" principal pin --node nodeB \
		--as "$N5_USER_A" --key "$key" --epoch "$epoch" >/dev/null || return 1
	want_eq "the key node B pinned" \
		"$(scalar5 "$N5_DSN_B" "SELECT encode(public_key, 'hex') FROM principal_identity
		    WHERE principal = '$N5_USER_A'")" "$key" || return 1
	want_eq "the epoch it pinned it from" \
		"$(scalar5 "$N5_DSN_B" "SELECT epoch_hlc FROM principal_identity
		    WHERE principal = '$N5_USER_A'")" "$epoch" || return 1
	want_eq "the private half on node B" \
		"$(scalar5 "$N5_DSN_B" "SELECT coalesce(length(private_key), 0) FROM principal_identity
		    WHERE principal = '$N5_USER_A'")" 0 || return 1

	remember5 N5_ALICE_KEY "$key"
	remember5 N5_ALICE_EPOCH "$epoch"
	printf 'nodeA signs for %s from reading %s; nodeB holds the public half\n' \
		"$N5_USER_A" "$epoch"
}

# The write side, and the other half of the claim: the node holding the key
# signs what that principal writes, the row crosses the boundary carrying the
# signature, and the node that did NOT write it can say it is hers.
#
# Without this the fix would be indistinguishable from a rule that refuses
# everything about a principal with a key, which is a break and not a fix.
a_message_from_the_node_holding_the_key_is_authored() {
	recall5
	local id thread mine
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/events \
		'{"type":"chat","room":"general","body":"signed with my own key"}' || return 1
	id="$(jqv .id)"
	thread="$(jqv .thread)"
	want_eq "what node A says about it" "$(jqv .authorship)" authored || return 1

	sync_round || return 1
	want_eq "what node B stored" \
		"$(scalar5 "$N5_DSN_B" "SELECT authorship FROM events WHERE id = '$id'")" authored || return 1
	# And a reader on node B is told, over the wire, on the node that did not
	# write it and does not hold the key it was signed with.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/events?thread=$thread" || return 1
	want_eq "what node B tells a reader" "$(jqv '.events[0].authorship')" authored || return 1

	# A principal with no key anywhere is attributed, which is what every row in
	# a fabric that has provisioned nothing is. It is honest rather than
	# alarming: it says this node is holding somebody's word for it.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/events \
		'{"type":"chat","room":"general","body":"and nobody signed for this one"}' || return 1
	mine="$(jqv .authorship)"
	want_eq "a principal with no key here" "$mine" attributed || return 1
	printf 'message %s is %s on both nodes\n' "$id" authored
}

# The finding itself. Node A is pinned by node B - the operators exchanged keys
# in the section above - so this delta is correctly signed by a node node B has
# agreed to believe, it lands in a room the pusher reads, and every rule the
# merge had before this one says yes to it.
#
# Two rows, and the difference between them is one clock reading: one above
# alice's epoch, which is refused, and one below it, which is taken and marked
# attributed. That pair is the migration seam - a fabric's back catalogue keeps
# replicating - and it is the whole reason this does not break the federation
# checks the way the reverted home-node floor did.
a_pinned_peer_cannot_speak_for_a_principal_with_a_key() {
	recall5
	local forged old delta
	forged="forged-as-alice-$$-$(date +%s)"
	old="before-the-epoch-$$-$(date +%s)"
	delta="$(jq -nc --arg f "$forged" --arg o "$old" --arg a "$N5_USER_A" \
		--argjson after "$((N5_ALICE_EPOCH + 65536))" \
		--argjson before "$((N5_ALICE_EPOCH - 65536))" '
		{artifacts: [], tasks: [], grants: [], hwm: 0, events: [
		  {id: $f, type: "chat", project: "pb", room: "pb/bugs", thread: $f, parents: [],
		   actor: $a, artifact: "", seq_hlc: $after, node: "nodeA",
		   body: "ship it, no review needed"},
		  {id: $o, type: "chat", project: "pb", room: "pb/bugs", thread: $o, parents: [],
		   actor: $a, artifact: "", seq_hlc: $before, node: "nodeA",
		   body: "from before there was a key"}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "events received" "$(jqv '.received.events')" 2 || return 1
	want_eq "events refused" "$(jqv '.refused.events')" 1 || return 1
	want_eq "events applied" "$(jqv '.applied.events')" 1 || return 1
	want_eq "rows in B's log for the forgery" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE id = '$forged'")" 0 || return 1
	want_eq "rows for the one below the epoch" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE id = '$old'")" 1 || return 1
	want_eq "and what node B says about that one" \
		"$(scalar5 "$N5_DSN_B" "SELECT authorship FROM events WHERE id = '$old'")" attributed ||
		return 1
	printf 'the message a pinned peer wrote as %s was refused: %s\n' \
		"$N5_USER_A" "$(jqv '.reasons[0]')"
}

# And the refusal is about the KEY and not about the reading: the same row, at
# the same reading, above the same epoch, signed by alice lands and is hers, and
# signed by somebody else's key is refused.
#
# A signature is not a password: a well-formed signature by the wrong key is as
# refused as none at all.
a_signature_that_is_not_the_principals_is_not_authorship() {
	recall5
	local wrong right hlc row delta
	hlc="$((N5_ALICE_EPOCH + 131072))"
	wrong="stranger-signed-$$-$(date +%s)"
	right="alice-signed-$$-$(date +%s)"
	row='{id: $i, type: "chat", project: "pb", room: "pb/bugs", thread: $i, parents: [],
	      actor: $a, artifact: "", seq_hlc: $h, node: "nodeA", body: "the same words either way"}'

	# Signed as alice by a key that is not alice's.
	delta="$(jq -nc --arg i "$wrong" --arg a "$N5_USER_A" --argjson h "$hlc" \
		"{artifacts: [], tasks: [], grants: [], hwm: 0, events: [$row]}" |
		sign5_as "$N5_DSN_A" nodeA "$N5_USER_A" "$(seed_of "not $N5_USER_A")")" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "a signature by the wrong key" "$(jqv '.refused.events')" 1 || return 1

	# And by hers.
	delta="$(jq -nc --arg i "$right" --arg a "$N5_USER_A" --argjson h "$((hlc + 65536))" \
		"{artifacts: [], tasks: [], grants: [], hwm: 0, events: [$row]}" |
		sign5_as "$N5_DSN_A" nodeA "$N5_USER_A" "$(principal_seed "$N5_USER_A")")" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "a signature by hers" "$(jqv '.applied.events')" 1 || return 1
	want_eq "and node B says whose it is" \
		"$(scalar5 "$N5_DSN_B" "SELECT authorship FROM events WHERE id = '$right'")" authored ||
		return 1
	want_eq "the one the stranger signed" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE id = '$wrong'")" 0 || return 1
	printf 'one delta, two keys: %s landed as hers and %s was refused\n' "$right" "$wrong"
}

# The other half of the finding: not chat as somebody, but a rewrite of what
# somebody wrote. An artifact is mutable and a party other than its owner
# legitimately writes parts of it - a status move, a todo's assignee - so what
# the owner signs is what only the owner writes: what the thing is and what it
# says. A party's status move carries that signature forward; a rewrite of the
# body under her name cannot produce one.
a_rewrite_of_what_somebody_wrote_is_refused() {
	recall5
	local id delta rewrite at
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"bug","title":"the drainer stops","body":"and here is how"}' || return 1
	id="$(jqv .id)"
	want_eq "what node A says about it" "$(jqv .authorship)" authored || return 1
	sync_round || return 1
	want_eq "what node B stored" \
		"$(scalar5 "$N5_DSN_B" "SELECT authorship FROM artifacts WHERE id = '$id'")" authored ||
		return 1

	# The same row, her name, her project, one changed sentence, at a later
	# reading - which is all last-writer-wins needs to make it the truth here
	# and on every node downstream.
	rewrite='{id: $i, type: "bug", project: "pa", owner_user: $a, visibility: "project",
	          title: "the drainer stops", body: "actually it was operator error",
	          hlc: $h, node: "nodeA", tombstone: false}'
	# Above node B's own clock, and so above the reading the row already has
	# there: an artifact is last-writer-wins, and a rewrite that loses its merge
	# would be settled without being judged, which is not what is being asked.
	at="$(forged_hlc "$N5_PORT_B")" || return 1
	delta="$(jq -nc --arg i "$id" --arg a "$N5_USER_A" --argjson h "$at" \
		"{events: [], tasks: [], grants: [], hwm: 0, artifacts: [$rewrite]}" |
		sign5 "$N5_DSN_A" nodeA)" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "artifacts refused" "$(jqv '.refused.artifacts')" 1 || return 1
	printf 'the rewrite was refused: %s\n' "$(jqv '.reasons[0]')"
	want_eq "what node B still holds" \
		"$(scalar5 "$N5_DSN_B" "SELECT body FROM artifacts WHERE id = '$id'")" \
		"and here is how" || return 1

	# And an edit she signs is an edit: the rule is her key, not her row being
	# frozen.
	delta="$(jq -nc --arg i "$id" --arg a "$N5_USER_A" --argjson h "$((at + 65536))" \
		"{events: [], tasks: [], grants: [], hwm: 0, artifacts: [$rewrite]}" |
		sign5_as "$N5_DSN_A" nodeA "$N5_USER_A" "$(principal_seed "$N5_USER_A")")" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "artifacts applied" "$(jqv '.applied.artifacts')" 1 || return 1
	want_eq "the body she signed for" \
		"$(scalar5 "$N5_DSN_B" "SELECT body FROM artifacts WHERE id = '$id'")" \
		"actually it was operator error" || return 1
	printf 'the unsigned rewrite of %s was refused and the signed one landed\n' "$id"
}

# ------------------------------------------------- and the refusal STANDS
#
# Everything above decides one row once, against the rule as it stands at that
# moment. That is not enough, and the gap is not subtle: a refused row was
# simply dropped. Nothing on this side remembered that it had been refused, so
# the peer went on offering it - a peer holds its rows and re-serves the same
# bytes on every pull, which is what replication IS - and on any later pull,
# after an operator moved a principal's epoch or removed a key by hand, the same
# bytes were judged again against the wider rule and applied.
#
# The window does not have to overlap the attack. It only has to exist. So a
# refusal was a delay rather than a decision, and the forgery sat in the peer's
# delta waiting for somebody to do something perfectly ordinary.
#
# What closes it is a ledger of refused CLAIMS, and the keying is the whole fix:
# a claim is the principal named as the author, the bytes their signature would
# have covered, and the signature actually offered. A claim in the ledger is
# refused on sight, without being judged against what the rule says now. The same
# CONTENT carrying that principal's real signature is a different claim and it
# lands - which it must, or one forged row in somebody's name would be a
# permanent embargo on their real one, mintable by whoever forged it first.
#
# The two halves are checked separately below, and the second one is not
# decoration. A fix that made this terminal by row id would pass the first check
# and be a denial of service on every author it protects.

# hlc_date PACKED - the wall clock instant a packed reading names, as the date a
# row carrying that reading would honestly have been created at.
#
# A row states its date twice, in the created column and in the clock reading
# beside it, and the merge refuses one where the two disagree by more than a day
# - see incoherentDate in sync.go, and hlc.MaxSkew for why a day. So the date the
# row below carries cannot be a literal somebody typed: it has to be derived from
# the reading the row is offered at, or the gate would be asking this node to take
# a row it is right to refuse, and reading the refusal as the fix failing.
#
# Truncated to the second, which is inside the skew by five orders of magnitude
# and keeps the value a pure function of its argument - which is the property the
# checks below actually need.
hlc_date() { date -u -d "@$((($1 >> 16) / 1000))" +%Y-%m-%dT%H:%M:%SZ; }

# terminal_row ID BODY HLC - the delta both checks offer, as JSON on stdout,
# unsigned. The caller decides which signatures go on it.
#
# created is derived here rather than left to `flowy sign` to stamp, and that is
# what makes this a re-offer rather than a new row: the date is inside both
# signatures and inside the claim, so a second signing run would stamp a later
# date, produce different bytes, and the ledger would rightly treat them as a
# different claim. Derived from the reading, so the same reading is the same date
# is the same bytes, on every offer and in both checks. A real peer serves the row
# it holds, date and all.
terminal_row() {
	local created
	created="$(hlc_date "$3")" || return 1
	jq -nc --arg i "$1" --arg b "$2" --arg a "$N5_USER_A" --arg c "$created" \
		--argjson h "$3" '
		{artifacts: [], tasks: [], grants: [], hwm: 0, events: [
		  {id: $i, type: "chat", project: "pb", room: "pb/bugs", thread: $i, parents: [],
		   actor: $a, artifact: "", seq_hlc: $h, node: "nodeA", body: $b, created: $c}]}'
}

# THE FINDING, driven end to end over the wire.
#
# The same delta is offered three times: once against the rule that refuses it,
# once again to show the refusal is remembered, and once after the operator has
# moved alice's epoch past the row - which is the one thing about a pinned key
# that legitimately changes, and which under the rule alone makes the row predate
# the key and land.
#
# The control in the middle is what makes the last assertion mean anything. A row
# of alice's that was NEVER offered before, at the same reading, does land under
# the widened epoch. So the rule really did widen, and the refusal that follows is
# the ledger holding rather than the pin failing to take.
a_refusal_is_terminal_for_the_claim_it_refused() {
	recall5
	local id control hlc widened forged fresh
	id="terminal-as-alice-$$-$(date +%s)"
	control="never-offered-$$-$(date +%s)"
	hlc="$((N5_ALICE_EPOCH + 262144))"
	forged="$(terminal_row "$id" "approved, merge it" "$hlc" | sign5 "$N5_DSN_A" nodeA)" || return 1

	# Refused on the rule, and written down.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$forged" || return 1
	want_eq "the first offer" "$(jqv '.refused.events')" 1 || return 1
	want_eq "the claim node B wrote down" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM refused_authorship
		    WHERE row_kind = 'event' AND row_id = '$id'")" 1 || return 1

	# The same bytes again: refused, and the peer is told it is a decision.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$forged" || return 1
	want_eq "the same bytes offered again" "$(jqv '.refused.events')" 1 || return 1
	case "$(jqv '.reasons[0]')" in
	*"already refused"*) ;;
	*)
		printf 'the second refusal does not say the refusal stands: %s\n' "$(jqv '.reasons[0]')" >&2
		return 1
		;;
	esac
	want_eq "one claim, not one per offer" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM refused_authorship
		    WHERE row_kind = 'event' AND row_id = '$id'")" 1 || return 1

	# The operator moves the epoch past the row. Ordinary, local, and exactly
	# what used to let the forgery in.
	widened="$((hlc + 65536))"
	DATABASE_URL="$N5_DSN_B" "$ROOT/flowy" principal pin --node nodeB \
		--as "$N5_USER_A" --key "$N5_ALICE_KEY" --epoch "$widened" >/dev/null || return 1

	# The control: never offered before, same reading, below the new epoch.
	fresh="$(terminal_row "$control" "approved, merge it" "$hlc" | sign5 "$N5_DSN_A" nodeA)" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$fresh" || return 1
	want_eq "an unrefused row under the widened rule" "$(jqv '.applied.events')" 1 || return 1
	want_eq "and what node B says about it" \
		"$(scalar5 "$N5_DSN_B" "SELECT authorship FROM events WHERE id = '$control'")" \
		attributed || return 1

	# And the one that was refused, under that same widened rule.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$forged" || return 1
	want_eq "the refused row after the pin that would have allowed it" \
		"$(jqv '.refused.events')" 1 || return 1
	want_eq "and applied" "$(jqv '.applied.events')" 0 || return 1
	want_eq "rows in B's log for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE id = '$id'")" 0 || return 1

	# The epoch goes back where the operator had it, so this check's widening
	# does not leak into anything after it.
	DATABASE_URL="$N5_DSN_B" "$ROOT/flowy" principal pin --node nodeB \
		--as "$N5_USER_A" --key "$N5_ALICE_KEY" --epoch "$N5_ALICE_EPOCH" >/dev/null || return 1
	want_eq "the epoch node B is back on" \
		"$(scalar5 "$N5_DSN_B" "SELECT epoch_hlc FROM principal_identity
		    WHERE principal = '$N5_USER_A'")" "$N5_ALICE_EPOCH" || return 1

	remember5 N5_TERMINAL_ID "$id"
	remember5 N5_TERMINAL_HLC "$hlc"
	printf 'the pin that would have let %s in did not: %s\n' "$id" "$(jqv '.reasons[0]')"
}

# The other half, and the half that keeps this from being a permanent blacklist.
#
# The SAME row - same id, same words, same reading, same date, so the same bytes
# the ledger holds a refusal about - carrying alice's own signature this time. It
# is a different CLAIM, it is judged on its own, and it lands as hers.
#
# Without this the fix would be an attack of its own: forge one row in somebody's
# name and their real one can never arrive.
the_same_content_with_the_authors_signature_still_lands() {
	recall5
	local signed
	signed="$(terminal_row "$N5_TERMINAL_ID" "approved, merge it" "$N5_TERMINAL_HLC" |
		sign5_as "$N5_DSN_A" nodeA "$N5_USER_A" "$(principal_seed "$N5_USER_A")")" || return 1

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$signed" || return 1
	want_eq "the same content, signed by its author" "$(jqv '.applied.events')" 1 || return 1
	want_eq "rows in B's log for it now" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE id = '$N5_TERMINAL_ID'")" \
		1 || return 1
	want_eq "and whose word node B says it is" \
		"$(scalar5 "$N5_DSN_B" "SELECT authorship FROM events WHERE id = '$N5_TERMINAL_ID'")" \
		authored || return 1

	# The refusal of the OTHER claim is still on the ledger. It has not been
	# withdrawn by the row arriving properly - a different claim landing says
	# nothing about the one that was refused.
	want_eq "the refused claim, after the signed one landed" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM refused_authorship
		    WHERE row_kind = 'event' AND row_id = '$N5_TERMINAL_ID'")" 1 || return 1
	printf '%s was refused unsigned and landed as hers: not a blacklist\n' "$N5_TERMINAL_ID"
}

# And it is VISIBLE, which is the rule this codebase follows everywhere: a
# refusal nobody can see is indistinguishable from success.
#
# The count rides on the same answer the withheld count does, through the same
# read filter, and it is a SEPARATE number because it is a separate statement. A
# withheld row may turn up on the next pull; a refused claim will not turn up at
# all until somebody signs for it. The check above left node B in exactly the
# state that tells them apart - the row is here, so nothing about it is withheld,
# and the claim is refused, so something about it is.
the_refused_claims_are_counted_where_a_reader_would_have_seen_them() {
	recall5
	local claims
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" /api/artifacts || return 1
	claims="$(jqv '.refused.claims')"
	if [ -z "$claims" ] || [ "$claims" = "null" ] || [ "$claims" -lt 1 ]; then
		printf 'node B refused claims and its artifacts read says %s\n' "${claims:-<absent>}" >&2
		return 1
	fi
	want_eq "the reason it carries" "$(jqv '.refused.reason')" \
		"refused authorship, and the refusal stands" || return 1
	# The same set spelled out independently of the query that produced the
	# number, which is the only way this says anything: what this reader reaches
	# is their own project AND pa, because the grant that made pa replicable at
	# all - up in "what replicates" - is a project-wide read from pb into pa, and
	# the rewrite refused in pa above is a claim about a row they read. A refusal
	# personal to somebody else is the one they are not told about, and there is
	# none here to be told about.
	want_eq "and it matches what node B actually holds" "$claims" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM refused_authorship
		    WHERE project IN ('pb', 'pa') AND visibility <> 'personal'")" || return 1

	# Who is told is the artifact read rule over the ledger's own columns, so a
	# refusal in a project a reader cannot reach is not a second way to learn
	# what is in it. That is asked row by row in the store, where a personal row
	# and the one reader who owns it can be set up exactly - see
	# TestWhatWasRefusedIsCountedWhereTheRowWouldHaveBeenRead.
	printf 'node B reports %s refused claim(s) to the reader who would have had the rows\n' \
		"$claims"
}

# -------------------------------------------------------------- phase 10 tui
#
# `flowy tui` is another client of this same API - the console's endpoints, the
# console's token - rendered for a terminal. So the checks are the ones a
# terminal client can get wrong that a browser one cannot: that it is driven by
# the keyboard alone, that it does not take a key the multiplexer needs, that it
# reflows on a resize instead of falling over, and that when it exits the
# terminal still works.
#
# It is driven two ways. The model is driven headless with teatest, which is
# what asserts that a room renders, that a message typed into the box comes back
# through the watcher, that the inbox has the seeded task in it and that memory
# search finds the seeded item. And the built binary is driven on a real
# pseudo-terminal, which is the only way to ask the question teatest cannot:
# after q, is ECHO back on.

# The seeds the headless drive looks for. They are fixed strings rather than
# generated ones because the check that seeds them and the check that looks for
# them are two processes.
TUI_MESSAGE="tui-gate-seeded-message"
TUI_MEMORY="quinceberry"
TUI_TASK="the tui gate handoff"
TUI_REPORT="quinceberry harvest report"
TUI_REPORT_AS_OF="tui-gate-abc123"
# The todo is worded away from the memory seed on purpose: memory search is
# narrowed by type and not by kind, so a todo sharing quinceberry with it would
# make the memory hit count say something about this seed instead.
TUI_TODO="marrowbone the todos view"
TUI_TODO_OWNER="tui-gate-owner"
# And one raised in the room the drive opens, which the room view has to draw
# beside the messages. It is worded away from TUI_TODO because the drive asserts
# both - one on the todos tab, one in the room - and a shared word would let a
# pane that drew the wrong list pass either wait.
TUI_ROOM_TODO="quicklime the room panel"
readonly TUI_MESSAGE TUI_MEMORY TUI_TASK TUI_REPORT TUI_REPORT_AS_OF
readonly TUI_TODO TUI_TODO_OWNER TUI_ROOM_TODO

# The tui links no database driver and no store package: it reaches the node the
# way any other client does or it does not reach it at all. This is a structural
# claim and go list settles it - a client that grew a direct connection would
# have a second permission filter, which is the one thing this node does not
# have room for.
tui_talks_only_to_the_api() {
	local reached
	reached="$(go list -deps ./internal/tui | grep -E 'lib/pq|flowy/internal/store' || true)"
	if [ -n "$reached" ]; then
		printf 'the tui package reaches past the HTTP API:\n%s\n' "$reached" >&2
		return 1
	fi
	printf 'no database driver and no store: it is an API client\n'
}

# Every read it makes is a read the node has to attribute to somebody, so with
# no token anywhere it refuses rather than starting up and rendering empty panes
# that look like "you have nothing".
tui_needs_a_token() {
	local out
	mkdir -p "$WORK/no-config"
	if out="$(env -u FLOWY_TOKEN XDG_CONFIG_HOME="$WORK/no-config" \
		"$ROOT/flowy" tui --url "http://127.0.0.1:$HTTP_PORT" 2>&1)"; then
		printf 'flowy tui started with no token at all:\n%s\n' "$out" >&2
		return 1
	fi
	case "$out" in
	*"no token"*) printf 'refused: %s\n' "$out" ;;
	*)
		printf 'it refused, but not for the reason it should have:\n%s\n' "$out" >&2
		return 1
		;;
	esac
}

# What the headless drive looks for: a message in general, a memory to search
# for, a task in A's own inbox, a report, and a todo.
#
# The todo is written active, with the OWNER line the queue's items carry, and
# it is the only active one here - the earlier todos check left a done one in
# this project, so the two of them together are what the ordering claim rests
# on: the drive asserts the active one is above the done one in the list the
# client actually rendered.
#
# The report is written over POST /api/artifacts and deliberately not through
# report_write, because that is the case the reports view exists for: a report
# filed over the API emits no report.write activity event, so the timeline - the
# only way the tui reached artifacts before this view - cannot show it. A seed
# that went through the mcp verb would pass whether or not the view worked.
tui_seed() {
	recall
	api POST "$TOKEN_A" /api/chat/general/say \
		"$(jq -nc --arg b "$TUI_MESSAGE" '{body: $b}')" || return 1
	want_eq "the seeded message" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$TUI_MEMORY pruning notes" \
			'{type: "memory", title: $t, body: "how to prune a quinceberry"}')" || return 1
	want_eq "the seeded memory" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$TUI_REPORT" --arg a "$TUI_REPORT_AS_OF" \
			'{type: "report", title: $t, body: "how the harvest went",
			  fields: {as_of: $a}}')" || return 1
	want_eq "the seeded report" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$TUI_TODO" --arg o "$TUI_TODO_OWNER" \
			'{type: "memory", kind: "todo", status: "active", visibility: "project",
			  title: $t, body: ("OWNER: " + $o + "\nDEPENDS ON: nothing")}')" || return 1
	want_eq "the seeded todo" "$API_STATUS" 200 || return 1
	# Raised in the room rather than filed at it: the room field and the message
	# that raised it are what the room panel reads, so the seed goes in through
	# the door a person in the room uses.
	api POST "$TOKEN_A" /api/chat/general/todo \
		"$(jq -nc --arg t "$TUI_ROOM_TODO" --arg o "$TUI_TODO_OWNER" \
			'{title: $t, body: ("OWNER: " + $o)}')" || return 1
	want_eq "the seeded room todo" "$API_STATUS" 200 || return 1
	want_eq "raised in general" "$(jqv .item.fields.room)" general || return 1
	local artifact
	artifact="$(new_artifact "$TOKEN_A" bug "$TUI_TASK")" || return 1
	assign_as "$TOKEN_A" "$artifact" "$USER_A" "for the tui gate" || return 1
	want_eq "the seeded assignment" "$API_STATUS" 200 || return 1
	printf 'a message in general, a %s memory, a report as of %s, a todo owned by %s, "%s" raised in general, and a task about %s\n' \
		"$TUI_MEMORY" "$TUI_REPORT_AS_OF" "$TUI_TODO_OWNER" "$TUI_ROOM_TODO" "$artifact"
}

# ran_the_live_tests NAME ENV... - runs one of the teatest drives and insists it
# really ran.
#
# The insisting is the point. These tests skip themselves when there is no node
# to talk to, so that `go test ./...` above is runnable on its own - and a skip
# prints "ok" and exits zero, which would make a gate that only looked at the
# exit code green on a run where nothing was driven at all. So the verdict is
# read out of -v output and a SKIP is a failure here.
ran_the_live_tests() {
	local pattern=$1
	shift
	local out status
	out="$(env "$@" go test -count=1 -run "$pattern" -v ./internal/tui 2>&1)"
	status=$?
	if [ "$status" -ne 0 ]; then
		printf '%s\n' "$out" >&2
		return 1
	fi
	if printf '%s' "$out" | grep -q -- '--- SKIP'; then
		printf 'the drive skipped rather than running:\n%s\n' "$out" >&2
		return 1
	fi
	if ! printf '%s' "$out" | grep -q -- '--- PASS'; then
		printf 'no test in %s actually passed:\n%s\n' "$pattern" "$out" >&2
		return 1
	fi
	printf '%s' "$out" | grep -E '^(--- |ok )'
}

# The client itself, driven by keystrokes against the live node: the room
# renders, a message typed into the box comes back through the watcher, the
# inbox has the seeded task, memory search finds the seeded item, the reports
# view lists the seeded report with what it is true of, the todos view lists the
# queue in flight-first order with the owner on the row, the timeline and the
# metrics render, it is resized twice and then it quits.
tui_headless() {
	recall
	ran_the_live_tests TestLiveTUIDrivenByTheKeyboard \
		"FLOWY_TUI_URL=http://127.0.0.1:$HTTP_PORT" \
		"FLOWY_TUI_TOKEN=$TOKEN_A" \
		"FLOWY_TUI_ROOM=general" \
		"FLOWY_TUI_MESSAGE=$TUI_MESSAGE" \
		"FLOWY_TUI_MEMORY=$TUI_MEMORY" \
		"FLOWY_TUI_TASK=$TUI_TASK" \
		"FLOWY_TUI_REPORT=$TUI_REPORT" \
		"FLOWY_TUI_REPORT_AS_OF=$TUI_REPORT_AS_OF" \
		"FLOWY_TUI_TODO=$TUI_TODO" \
		"FLOWY_TUI_TODO_OWNER=$TUI_TODO_OWNER" \
		"FLOWY_TUI_ROOM_TODO=$TUI_ROOM_TODO"
}

# And the failure the gate has to see: a token the node refuses is a line on the
# status bar, not a stack trace over the terminal.
tui_headless_refuses_a_bad_token() {
	ran_the_live_tests TestLiveABadTokenIsRefusedClearly \
		"FLOWY_TUI_URL=http://127.0.0.1:$HTTP_PORT" \
		"FLOWY_TUI_TOKEN=whatever"
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
	printf 'node %s is too old; the console needs node >= 20 to build\n' "$(node -v)" >&2
	exit 1
fi
# initdb refuses to run as root, and says so in terms of postgres rather than in
# terms of who is running this. A container or an agent harness lands you as
# root by default, so the gate says which of the two it is before it gets there.
if [ "$(id -u)" -eq 0 ]; then
	printf 'run the gate as an ordinary user: initdb refuses to run as root.\n' >&2
	printf 'from a root shell: sudo -u <user> %s\n' "$0" >&2
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
check "signed out, the worklog says so instead of rendering an empty page" \
	console_says_the_worklog_needs_a_token
check "a browser to run the browser checks in" browser_is_installed
check "the room poll does not flood a node whose cursor never moves" poll_does_not_spin
check "a tab open across a deploy reloads itself once, and only once" \
	a_stale_tab_reloads_itself_once

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

PGPORT="$(free_port 15432)"
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
check "go build" go_build
check "go build ./cmd/smoke" go build -o "$WORK/smoke" ./cmd/smoke
# The trusted-host binary is built by the gate but never run by it: it needs a
# Docker daemon and a source checkout, which is exactly why it is a second
# deployable. Building it is what catches the case that matters here - a change
# to internal/store or internal/repro that leaves the node compiling and the
# runner not.
check "go build ./cmd/handoff-runner" go build -o "$WORK/handoff-runner" ./cmd/handoff-runner
check "gofmt" gofmt_clean
check "go vet" go vet ./...

say "unit tests"
# -count=1 so the live store tests really talk to the database this run rather
# than replaying a cached result from an earlier one.
check "go test ./..." go test -count=1 ./...

# ------------------------------------------ an older database meets this binary
#
# Here, early, because a schema that does not migrate is a broken deploy and
# there is no point learning that after five hundred other checks. Everything
# this needs already exists at this point: the cluster, `./flowy`, and
# `$WORK/smoke`. Nothing it does touches the gate's own database - it works in
# databases of its own on the same cluster, and its node runs on a port of its
# own and is stopped before the one every other check uses starts.
#
# The long version of why this section exists, and how the baseline is chosen,
# is at the helpers above.

say "an older database meets this binary"
mkdir -p "$UPG"
check "the baseline is a real, older revision of schema.sql" \
	upgrade_baseline_is_a_real_older_schema
check "a database built from the baseline schema loads" \
	upgrade_baseline_database_loads
check "the fingerprint agrees with the database the rest of this run uses" \
	upgrade_fingerprint_agrees_with_the_gates_own_database
check "the fingerprint sees a database with nothing in it" \
	upgrade_fingerprint_sees_an_empty_database
check "the baseline database is behind the current schema, and says by what" \
	upgrade_baseline_is_behind_the_current_schema
check "principals seed into the older database" upgrade_seed_the_baseline

UPG_PORT="$(free_port 9300)"
printf '%s\n' "$UPG_PORT" >"$UPG/port"
# Started here rather than inside a check: a check runs in a command
# substitution, and a background process that inherits its stdout holds that
# substitution open forever.
upg_op="$(sed -n 's/^USER_OP=//p' "$UPG/ids" 2>/dev/null | head -1 || true)"
upg_url="$(upg_dsn gate_baseline)"
DATABASE_URL="$upg_url" FLOWY_NODE=upgrade FLOWY_OPERATOR="$upg_op" FLOWY_FORGE=mock \
	FLOWY_FORGE_REPOS=o/r \
	./flowy serve -addr "127.0.0.1:$UPG_PORT" >"$UPG/serve.log" 2>&1 &
UPG_PID=$!
printf '%s\n' "$UPG_PID" >"$WORK/upgrade-node.pid"
printf 'flowy serve pid %s on 127.0.0.1:%s, against the OLDER database\n' "$UPG_PID" "$UPG_PORT"

check "the node comes up against a database that is behind it" \
	"$WORK/smoke" healthz "http://127.0.0.1:$UPG_PORT/healthz"
check "healthz says 200 either way, which is why the outage ran for four minutes" \
	upgrade_node_is_up_on_the_older_database
check "scripts/migrate.sh brings the baseline database up to the current schema" \
	upgrade_migrate_brings_the_baseline_up
check "A MIGRATED DATABASE IS STRUCTURALLY IDENTICAL TO A FRESH ONE" \
	upgrade_migrated_matches_a_fresh_database
check "scripts/migrate.sh brings an empty database up to the current schema too" \
	upgrade_migrate_brings_an_empty_database_up
check "the older database, migrated, serves the read that took the node down" \
	upgrade_read_serves_after_the_migration
check "a relation the binary queries, missing, is an outage this gate can see" \
	upgrade_a_missing_relation_is_an_outage_the_gate_can_see
check "and scripts/migrate.sh repairs it, on the same running node" \
	upgrade_migrate_repairs_it
check "scripts/deploy.sh applies the schema before it restarts the unit" \
	upgrade_deploy_migrates_before_it_restarts
check "a migration that cannot reach its database refuses instead of shrugging" \
	upgrade_migrate_refuses_a_database_it_cannot_reach
check "the node survived the schema-drift checks" kill -0 "$UPG_PID"

# Stopped before the gate's own node starts, so nothing below this can be
# reading the wrong port or the wrong database.
kill "$UPG_PID" 2>/dev/null || true
wait "$UPG_PID" 2>/dev/null || true
rm -f "$WORK/upgrade-node.pid"
UPG_PID=""

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
check "healthz answers when counts are asked for" \
	"$WORK/smoke" healthz "http://127.0.0.1:$HTTP_PORT/healthz?counts=1"
check "spine tables exist" "$WORK/smoke" schema

say "subcommands"
# There are no stubs left: mcp left this list in Phase 2, sync in Phase 5 and
# fuse in Phase 7. What is checked instead is that the last one refuses to do
# anything by accident - a mount is opt-in, and a `flowy fuse` with nothing said
# mounts nothing rather than picking a directory.
check "flowy fuse mounts nothing unless it is told where" fuse_needs_telling

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
check "the withdrawal names who took it back, to everyone who could have read it" tombstone_says_who_took_it_back
check "a withdrawn row out of reach is a 404 like any id that was never written" \
	withdrawn_out_of_reach_is_indistinguishable_from_absent

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
check "tools/list offers the memory tools and the project indicator" mcp_tools_list
check "the guide is reachable by resource and by tool, behind capped instructions" mcp_instructions_resource
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

say "the worklog"
check "an entry carries the seat that wrote it" an_entry_carries_the_seat_that_wrote_it
check "an entry cannot reference an artifact its author cannot read" \
	an_entry_cannot_reference_what_its_author_cannot_read
check "an entry says what changed, or it is not one" an_entry_says_what_changed
check "the read is the recent entries, newest first" the_worklog_reads_recent_first
check "entries are on the timeline, and cannot be posted onto it" \
	entries_are_on_the_timeline_and_not_postable_onto_it
check "an entry carries the branch it was written on" \
	an_entry_carries_the_branch_it_was_written_on
check "the timeline answers the worklog newest first" \
	the_timeline_answers_the_worklog_newest_first

say "the worklog's other doors: HTTP, the CLI, and vouching"
check "all three doors refuse an unreadable ref in the same words" \
	all_three_doors_refuse_a_ref_in_the_same_words
check "the HTTP and CLI doors append an entry, and the CLI reads it back" \
	the_http_and_cli_doors_append_an_entry
check "an entry written by one seat about another's work says it is vouched" \
	an_entry_written_about_another_seat_says_it_is_vouched
check "vouching for yourself is authoring, and a subject nobody answers to is refused" \
	vouching_for_yourself_is_authoring
check "the generic event door writes the event and stamps none of its claims" \
	the_generic_event_door_cannot_stamp_a_worklogs_claims

say "proposals, and voting on them"
check "a proposal is raised in a room, and the room narrows the list" \
	a_proposal_is_raised_in_a_room
check "a vote from a principal who cannot read the proposal is refused" \
	a_vote_from_somebody_who_cannot_read_the_proposal_is_refused
check "changing a vote appends, the old vote stays, and the tally follows the latest" \
	changing_a_vote_appends_and_the_tally_follows_the_latest
check "the tally, the log and the refusals, in the store" \
	go test -count=1 -run 'TestAVoteFromSomebodyWhoCannotReadTheProposalIsRefused|TestChangingAVoteAppendsAndTheOldVoteIsStillThere|TestTheTallyCountsOneVotePerPrincipalNotOnePerEvent|TestAClosedProposalRefusesVotesAndSaysWhenItClosed' ./internal/store
check "a closed proposal refuses further votes, and says when it closed" \
	a_closed_proposal_refuses_further_votes_and_says_when
check "the proposal, its votes and its tally read back over HTTP" \
	the_proposal_reads_back_over_http
check "a vote is written by the verb that casts it, not by hand" \
	a_vote_cannot_be_written_by_hand

say "DEPENDS-ON, and the ready query"
check "a todo B carries, and a blocker B cannot see" \
	the_queue_gets_a_todo_and_a_blocker_b_cannot_see
check "an edge is an event naming both todos, with the seat that wrote it" \
	an_edge_is_an_event_naming_both_todos
check "a blocker B cannot see holds the todo for B, finished or not" \
	a_blocker_b_cannot_see_holds_the_todo_done_or_not
check "the invisible blocker, the fold and the cycle, in the store" \
	go test -count=1 -run 'TestABlockerTheReaderCannotSeeHoldsTheTodoDoneOrNot|TestReadyIsDepsDoneAndAssignedAndNeitherAlone|TestRemovingADepUnblocksAndBothEntriesStayInTheLog|TestACycleIsRefusedAndNothingInOneIsEverReady|TestATodoCannotDependOnItselfAndAnEdgeIsSaidOnce|TestAnEdgeNamesTwoReadableTodosAndMayCrossAProject|TestLiveDepsFoldsTheLatestEntryPerBlocker' ./internal/store
check "deps done and assigned is ready, and either one alone is not" \
	deps_done_and_assigned_is_ready_and_either_alone_is_not
check "removing an edge unblocks, and the old edge is still in the log" \
	removing_a_dep_unblocks_and_the_old_edge_is_still_in_the_log
check "a cycle and a self-edge are refused, and the cycle says where it goes" \
	a_cycle_and_a_self_edge_are_refused
check "the queue reads and writes over HTTP, and B gets the other answer" \
	the_queue_reads_and_writes_over_http
check "an edge is written by the verb that makes it, not by hand" \
	an_edge_cannot_be_written_by_hand

say "attachments"
check "the bytes come back byte for byte, NUL and newline included" \
	an_attachment_round_trips_byte_for_byte
check "over the ceiling is refused, and the refusal names it" \
	an_attachment_over_the_ceiling_is_refused_with_the_number
check "an attachment with no bytes is not an attachment" an_empty_attachment_is_refused
check "an attachment B may not read is not readable and not listable" \
	b_cannot_read_or_list_as_attachment
check "the content type is decided from the bytes, and the claim is kept as a claim" \
	the_content_type_is_not_the_clients_to_decide

say "the project entity"
check "a write into a project nobody declared is refused" \
	a_write_into_an_undeclared_project_is_refused
check "whoami says where this token's writes land" whoami_says_where_this_tokens_writes_land
check "and so does the command line" the_cli_says_which_project_this_token_writes_to
check "a write into a fixture lands, and says it landed in a fixture" \
	a_write_into_a_fixture_lands_and_says_so
check "the enumeration is filtered by the edges that already existed" \
	the_enumeration_is_permission_filtered
check "anybody declares, only the operator flags and pins" \
	declaring_is_open_and_flagging_a_fixture_is_the_operators
check "one repository is one origin, and a move is an alias" \
	an_origin_is_one_string_and_a_move_is_an_alias
check "the registry adapted to the data, and no row was rewritten" \
	the_registry_adapted_to_the_data

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
check "the room read says who said each thing" the_room_read_says_who_said_each_thing

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

# A message can be directed at somebody without leaving the room. The field is
# the small half; the invariant is the whole point, and it is the last two
# checks here: an addressee opens nothing and closes nothing.
say "chat addressing"
check "a message carries who it is for, there and back" a_message_can_be_addressed
check "an agent is an addressee too" an_agent_can_be_addressed
check "a handle is an address, and it stores the principal" a_handle_is_an_address
check "a name nothing answers to is refused at the door" \
	a_name_nothing_answers_to_is_refused
check "a message to the room carries none" an_unaddressed_message_is_still_a_message
check "a name nothing answers to is refused, and writes no row" an_unknown_addressee_is_refused
check "being named on a message is not a capability" addressing_changes_nothing_about_who_reads
check "an @name in the body fills the same field in" a_mention_addresses_the_message
check "the first mention addresses, the rest are on the message" \
	the_first_mention_addresses_and_the_rest_are_recorded
check "an email address, an unknown name and a stray @ address nobody" \
	what_is_not_a_mention_addresses_nobody
check "what counts as a mention, and what only looks like one" \
	go test -count=1 -run 'TestWhatCountsAsAMention|TestAnUnresolvedMentionIsPlainTextAndNotARefusal|TestTheFirstMentionAddressesAndTheRestAreOnTheMessage' .
check "a name resolves to the one principal it names" \
	go test -count=1 -run 'TestANameResolvesToTheOnePrincipalItNames|TestAnAmbiguousNameResolvesToNobody|TestAnAgentWhosePersonHasAHandleIsNamedByTheHandle' ./internal/store
check "the addressee is inside what the node signs" \
	go test -count=1 -run 'TestAnUnaddressedEventEncodesAsItAlwaysDid|TestAnAddresseeCannotBeAddedRemovedOrSwapped' ./internal/sign

# Direct messages. The first check is the control and proves nothing on its own -
# a build where everybody could read the message would pass it. The second is
# the one that decides whether this ships: a third principal in the SAME PROJECT
# as the sender, who reads every other message the sender writes, does not read
# this one. Asked over the wire, as that third token, and not by reading the SQL.
say "direct messages"
check "a direct message reaches both parties, and carries no project or room" \
	a_direct_message_reaches_both_parties
check "A THIRD PRINCIPAL IN THE SAME PROJECT CANNOT READ IT" \
	a_third_principal_in_the_project_cannot_read_a_dm
check "and it is on none of their surfaces: room, events, inbox, timeline, search, thread" \
	a_dm_is_on_none_of_a_third_principals_surfaces
check "scope=all does not hand over somebody else's private log" \
	a_dm_is_not_handed_over_by_scope_all
check "a reply does not widen the conversation, and an outsider cannot write into it" \
	a_reply_to_a_dm_does_not_widen_it
check "no public door writes into a private conversation" \
	a_public_write_cannot_join_a_private_conversation
check "a direct message cannot join a handoff thread, which the task parties read" \
	a_dm_cannot_join_a_handoff_thread
check "a private message to a name nothing answers to is refused, and writes no row" \
	a_dm_to_a_name_nothing_answers_to_is_refused
check "the project-scoped rooms are exactly as readable as they were" \
	the_project_rooms_are_unchanged_by_direct_messages
check "the same rule in the store, over the whole matrix of readers" \
	go test -count=1 -run 'TestADirectMessageIsInvisibleToAThirdPrincipal|TestADirectMessageThreadKnowsItsParties' ./internal/store
check "the merge refuses a public message into a private conversation, and takes the private one" \
	go test -count=1 -run 'TestTheMergeRefusesAPublicEventInAPrivateThread' ./internal/store
check "the terminal client draws a private message as one, and a room message as one" \
	go test -count=1 -run 'TestAPrivateMessageIsDrawnAsOne|TestThePrivateEntryIsInTheListAndIsNotWhereItOpens' ./internal/tui

# `flowy inbox --as NAME` - the thing that replaces the shell loop everybody
# reimplemented. Checked as a process, because what it exits with and which
# stream it writes on are the contract.
say "the inbox waiter"
check "a label nothing declared is refused, with the ones that exist" \
	an_unknown_waiter_is_refused_with_what_does_exist
check "--new starts at the head, and a quiet deadline is exit 1" \
	a_declared_waiter_starts_at_the_head_and_a_quiet_deadline_is_exit_1
check "it returns on the first message, as one JSON line on stdout" \
	the_waiter_returns_on_the_first_message
check "the cursor is the node's, so the next call does not repeat it" \
	the_cursor_is_the_nodes_so_the_next_call_does_not_repeat
check "its own messages do not wake it, and the mark passes them anyway" \
	its_own_messages_do_not_wake_it_and_the_mark_still_passes_them
check "--to-me wakes for what names it and counts what it skipped" \
	to_me_wakes_only_for_what_names_this_principal
check "--to-me wakes on an @name in the body, and not on somebody else's" \
	a_mention_wakes_a_to_me_waiter
check "the wake-up rule for a mention, in the unit" \
	go test -count=1 -run TestAWaiterNarrowedToItsOwnMailWakesOnAMentionOfIt .
check "a bad token, no token and a dead node are exit 2, never exit 1" \
	a_broken_waiter_is_exit_2_and_not_exit_1

# Hearing is not waking. A forked successor polls exactly like a tracked waiter
# and can wake nobody, so every check here is about telling them apart - and
# about what the node says when nothing has told it either way.
say "what a listener can do about what it hears"
check "the waiter says which kind it is, and the row follows it" \
	the_waiter_says_which_kind_it_is
check "presence answers tracked, forked, and unknown for anything unsaid" \
	presence_says_what_each_listener_can_do
check "the kind is per reader and survives the poll that set it" \
	go test -count=1 -run TestPresenceCarriesTheWaiterKind ./internal/store

# Hearing stops, and the node has to be able to say so. A poll counter that only
# comes down when a handler returns kept two seats on this roster reading
# "attached" - one for six hours, one for thirty - while the operator asked twice
# why an agent was not answering. The answer is not a shorter list: it is a row
# that says the seat was armed, stopped, and when.
check "a seat that stopped mid-poll is named as gone quiet, not left reading attached" \
	presence_retires_a_seat_that_stopped_mid_poll
check "the roster retires a stalled reader and keeps the evidence" \
	go test -count=1 -run 'TestPresenceRetiresAReaderThatStoppedMidPoll|TestPresenceStartingIsJudgedByTheRowsAge' ./internal/store
check "the two windows follow the waiter's own numbers" \
	go test -count=1 -run 'TestPresenceWindowIsManyServerWindowsWide|TestPresenceLostWindowFollowsTheWaitersDeadline' .

# A todo panel inside the room, and the field it needs. The room rides fields
# the way as_of rides a report, and it is a filter and not a permission axis -
# so half of what is asserted below is what a room's panel does NOT hold, and
# what a list that asked for no room still does.
say "per-room todos"
check "a todo is raised out of a message, and the room hears about it" \
	a_todo_is_raised_out_of_a_message
check "a message or thread id out of that notification says which it is" \
	a_misread_id_says_which_space_it_came_from
check "the id spaces, in the store" \
	go test -count=1 -run 'TestAThreadIdOutOfARaise|TestADiagnosisTellsAStranger' ./internal/store
check "each room's panel holds its own, and a roomless todo is in every list" \
	a_room_is_a_filter_and_not_a_move
check "the room filter, in the store" \
	go test -count=1 -run TestARoomIsAFilterAndNotAPermissionAxis ./internal/store
check "the filter hands another project nothing it could not already read" \
	a_room_is_not_a_permission_axis
check "a todo cannot be raised out of a message you cannot read" \
	a_todo_cannot_be_raised_out_of_a_message_you_cannot_read
check "mem_write carries the room, and todos narrows by it" \
	mem_write_takes_a_room_and_todos_narrows_by_it
check "a room name that is not one is refused at both surfaces" \
	a_room_that_is_not_one_is_refused
check "the room says who is carrying a todo, and who it was before" \
	a_todo_takes_an_assignee_and_an_override
check "an assignee is refused on another room's todo, on a bug, and as a paragraph" \
	an_assignee_is_refused_where_it_is_not_one
check "naming somebody on a todo hands them nothing" \
	an_assignee_hands_the_named_party_nothing
check "the assignee is not a permission axis, in the store" \
	go test -count=1 -run TestAnAssigneeHandsTheNamedPartyNothing ./internal/store
check "mem_write carries the assignee, and an empty one means nobody" \
	mem_write_takes_an_assignee

# And WHO RAISED IT, which is the other party on the row and was not on it at
# all. owner_user is the signing author and stays that; this is a second, weaker
# fact beside it, and the queue was ambiguous without it - a row an agent filed
# because somebody asked read exactly like a row the agent invented.
say "a todo says who raised it"
check "a todo raised out of a message carries the speaker of it" \
	a_todo_says_who_raised_it
check "a stated raiser wins, and a row that says nothing is not guessed at" \
	a_stated_raiser_wins_and_nothing_is_guessed
check "mem_write defaults one from the message and refuses to restate it" \
	mem_write_takes_a_raiser_and_settles_it
check "the raiser is a handle, is never inferred, and is settled at the raise, in Go" \
	go test -count=1 \
	-run 'TestATodoRaisedOutOfAMessageSaysWhoseRequestItWas|TestAnExplicitRaiserWinsOverTheMessagesSpeaker|TestARowWithNoRaiserSaysNobodyAndNothingIsInferred|TestARaiserIsAHandleAndTheWordsForNobodyCollapse' \
	.
check "the queue row and the artifact page say both names, in a browser" \
	browser_shows_who_raised_a_todo
check "tapping a raise opens the row, links to it, dismisses, and names a failure" \
	browser_opens_the_row_a_message_raised

# A todo belongs to the queue and not to whoever typed it. The checks above are
# the AUTHOR's own surface; these are the ones that were missing, and each drives
# a principal who did NOT write the item - which is the case a live node could
# not do at all, and the case a refusal that reports success made invisible.
say "a todo changes hands"
check "somebody who did not write a todo can assign it, at both doors" \
	a_todo_is_assigned_by_somebody_who_did_not_write_it
check "a todo you cannot read is a todo you cannot assign" \
	assigning_a_todo_you_cannot_read_is_refused
check "an assignment says who made it and when, and an override appends" \
	an_assignment_records_who_made_it
check "mem_write on an item that is not yours is an error, not an empty success" \
	mem_write_refuses_an_update_it_will_not_make
check "read permission is the whole bar for assigning, in the store" \
	go test -count=1 \
	-run 'TestAnybodyWhoCanReadATodoCanAssignIt|TestAPrincipalWhoCannotReadATodoCannotAssignIt' \
	./internal/store
check "the latest claim wins and the log keeps the ones before it, in the store" \
	go test -count=1 -run TestTheLatestClaimWinsAndTheLogKeepsTheRest ./internal/store
check "a claim says who it took the work from, so a handover is not silent" \
	go test -count=1 -run TestAClaimSaysWhoItTookTheWorkFrom ./internal/store
check "one read says both what state a todo is in and who is carrying it" \
	go test -count=1 -run TestAReadSaysWhoIsCarryingItWithoutDiggingIntoFields ./internal/store

# And a todo gets FINISHED. The same ruling one field along: the queue metadata
# on an item changes hands, its words do not, and read permission is the whole
# bar for both. Until this round the status route answered a todo with "a memory
# has no lifecycle" and mem_write refused everybody but the author, so the one
# artifact whose purpose is to be finished was the one that could not be.
say "a todo is finished"
check "somebody who did not write a todo can close it, at both doors" \
	a_todo_is_closed_by_somebody_who_did_not_write_it
check "a todo you cannot read is a todo you cannot close" \
	closing_a_todo_you_cannot_read_is_refused
check "a closure says who made it and when, and a reopen appends" \
	a_closure_records_who_made_it_and_a_reopen_appends
check "a stranger may close a todo and may not rewrite it" \
	a_stranger_may_close_a_todo_and_may_not_rewrite_it
check "read permission is the whole bar for closing, in the store" \
	go test -count=1 \
	-run 'TestAnybodyWhoCanReadATodoCanCloseIt|TestAPrincipalWhoCannotReadATodoCannotCloseIt' \
	./internal/store
check "a closed todo reopens and the log keeps every move, in the store" \
	go test -count=1 -run TestAClosedTodoIsReopenedAndTheLogKeepsBoth ./internal/store
check "the queue lifecycle is the queue own, and its vocabulary is one, in the store" \
	go test -count=1 \
	-run 'TestOnlyAQueueItemHasTheQueuesLifecycle|TestTheVerbRefusesAStatusThatIsNotOne' \
	./internal/store
check "closing a todo unblocks what waits on it, in the store" \
	go test -count=1 -run TestClosingATodoUnblocksWhatWaitsOnIt ./internal/store

# And the two facts move TOGETHER. Where the work is and who is carrying it were
# written through two doors that had never heard of each other, so a row could
# say work was in flight and that nobody was doing it - and the board drew both,
# because both were on the row. The pair is refused at the write now rather than
# tidied at the read, which is the difference between a state that cannot happen
# and one that keeps happening and gets cleaned up.
say "active means somebody is on it"
check "active with nobody carrying it is refused at every door, and putting work down returns the row" \
	active_and_unowned_is_refused_at_every_door
check "the pair is refused at every statement that writes a row, in the store" \
	go test -count=1 \
	-run 'TestActiveWithNobodyCarryingItIsRefused|TestARowCannotBeRaisedActiveWithNobodyCarryingIt' \
	./internal/store
check "putting work down returns the row to the queue and says so in the log, in the store" \
	go test -count=1 \
	-run 'TestPuttingWorkDownReturnsTheRowToTheQueue|TestAClaimOfNobodyIsAReleaseAndMovesTheRowWithIt' \
	./internal/store

# And a todo says WHAT KIND OF WORK IT IS, out of a closed set of four, beside
# the free-form tags that have always been on it. The two are not variants of
# each other: tags are unlimited and refuse nothing, and this one refuses
# everything outside the set - which is the only reason a queue can be counted or
# routed by it. The same ruling as the other two pieces of queue metadata, so
# every check here drives a principal who did not raise the row.
say "a todo is what kind of work"
check "somebody who did not write a todo can say what kind of work it is, at both doors" \
	a_todo_is_classified_by_somebody_who_did_not_write_it
check "A KIND THAT IS NOT ONE IS REFUSED, at every door, and nothing is written" \
	a_category_that_is_not_one_is_refused
check "a todo you cannot read is a todo you cannot classify" \
	classifying_a_todo_you_cannot_read_is_refused
check "a classification says who made it and when, and an override appends" \
	a_classification_records_who_made_it
check "a todo with no kind reads and lists fine, and is not one of the bugs" \
	an_unclassified_todo_reads_and_lists_fine
check "mem_write and the raise both carry the kind, and tags take any word at all" \
	mem_write_takes_a_category_and_tags_take_anything
check "read permission is the whole bar for classifying, in the store" \
	go test -count=1 \
	-run 'TestAnybodyWhoCanReadATodoCanCategoriseIt|TestAPrincipalWhoCannotReadATodoCannotCategoriseIt' \
	./internal/store
check "the set is closed and the verb refuses anything outside it, in the store" \
	go test -count=1 -run TestTheVerbRefusesACategoryThatIsNotOne ./internal/store
check "an unclassified todo reads, lists, and drops out of a narrowed list, in the store" \
	go test -count=1 -run TestATodoWithNoCategoryReadsAndListsFine ./internal/store
check "the console draws the kind and filters by it and by a tag, in a browser" \
	console_filters_the_queue_by_kind_and_tag
# And the tags beside the kind are a filter the NODE applies, which is the half
# that was missing: the console could narrow a page it had already been handed,
# and the door handed it every row whatever it asked for.
check "A TAG NARROWS THE LIST AT THE DOOR, and a parameter it does not honour is refused" \
	a_tag_narrows_the_list_and_an_unhonoured_parameter_is_refused
check "the list door's tag filter, composition and refusal, in the handler" \
	go test -count=1 \
	-run 'TestATagNarrowsTheListAndTwoTagsMeanBoth|TestATagMatchesEitherColumnOfLabels|TestATagComposesWithTheOtherNarrowingsAndIsAppliedBeforeTheLimit|TestTheListRefusesAParameterItDoesNotHonour|TestTheListStillTakesTheParametersItDocuments' \
	.

say "what was learned about a row"
check "somebody who did not raise a row attaches what they learned, at both doors" \
	a_note_is_added_by_somebody_who_did_not_raise_the_row
check "a note lands on work that has started, where an edit is refused" \
	a_note_lands_on_work_that_has_already_started
check "an empty note, and a row you cannot read, are refused at both doors" \
	an_empty_note_and_a_row_you_cannot_read_are_refused
check "a note is written by the verb that appends it, not by hand" \
	a_note_cannot_be_written_by_hand
check "read permission is the bar and the author reads what somebody else learned, in the store" \
	go test -count=1 \
	-run 'TestSomebodyElseCanAttachWhatTheyLearnedAndTheAuthorReadsItOnTheRow|TestANoteLandsOnWorkThatIsUnderWayAndOnWorkThatIsFinished' \
	./internal/store
check "a note on a row only its writer could read is refused, in the store" \
	go test -count=1 \
	-run 'TestANoteOnAProjectlessRowIsRefusedRatherThanWrittenWhereNobodyReadsIt|TestTheAppendRefusesNothingToSayAndARowTheWriterCannotRead|TestANoteCannotBeHandedOver' \
	./internal/store
check "the row's page draws its notes under the body and takes a new one, in a browser" \
	browser_draws_the_notes_under_the_body

check "the console paints the room's todos on the room page" \
	console_renders_the_rooms_todos
check "the room's todo panel is on the screen in a browser, as an element" \
	browser_renders_the_rooms_todos
check "the panel sets and overrides one, in a browser, and a poll does not wipe it" \
	browser_sets_and_overrides_an_assignee
check "the panel hides the finished ones, counts them, and remembers it, in a browser" \
	browser_hides_the_finished_todos
check "raising a todo offers nobody a saved password or a stored card, in a browser" \
	browser_does_not_offer_a_password_over_a_todo
check "each speaker is drawn in their own colour, in a browser" \
	browser_colours_the_speakers
check "the roster draws what each listener can do, distinctly, in a browser" \
	browser_shows_what_a_listener_can_do
check "a typed URL is a link, and the mention beside it survives" \
	a_typed_url_is_a_link
check "an @name is drawn as a mention, in their colour, in a browser" \
	browser_draws_the_mentions
check "a pin puts a message up in the room's strip" a_pin_puts_a_message_up_in_the_room
check "an unpin takes it down, and the log remembers both" \
	an_unpin_takes_it_down_and_the_log_remembers_both
check "a message from another room cannot be pinned here" \
	a_message_from_another_room_cannot_be_pinned_here
check "a pin event cannot be written by hand" a_pin_event_cannot_be_written_by_hand
check "a person can select and copy a message, in a browser, by dragging" \
	browser_lets_a_person_copy_a_message
check "clicking a message answers nothing; its reply control does, by pointer and by keyboard" \
	browser_replies_only_when_asked
check "a message arriving does not scroll a reader out of the history" \
	browser_does_not_scroll_a_reader_away
check "a room opens on a window, lands at the end, stays, and pages back without moving the reader" \
	browser_leaves_a_room_load_at_the_end
check "the unread badge counts, clears when the room is read, and ignores your own" \
	browser_clears_the_unread_badge
check "the console's own reader row is past what it read, and only moves forward" \
	the_consoles_mark_is_a_reader_row_that_only_moves_forward

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

# What a reply is about, at a finer grain than the edge beside it. The span is
# stored and the quoted text never is, so the first check is the whole of it:
# a citation reaching a reader who cannot read what it quotes hands them
# nothing.
say "message citations"
check "a waiter is handed the quote with the reply" a_waiter_is_handed_the_quote_with_the_reply
check "a citation of a message you cannot read hands over nothing" \
	a_citation_of_a_message_you_cannot_read_hands_over_nothing
check "a whole message and a span of one both round-trip" \
	a_whole_message_and_a_part_of_one_both_round_trip
check "a message you cannot read cannot be cited" a_message_you_cannot_read_cannot_be_cited
check "a span that is not in the message it cites is refused" \
	a_span_that_is_not_in_the_message_is_refused
check "a citation is the node's to write, not a client's" a_client_cannot_write_its_own_citation
check "what a citation encodes, and what it derives" \
	go test -count=1 -run 'TestACitationRecordsTheSpanAndNeverTheText|TestASpanThatIsNotInTheBodyIsNotACitation|TestAQuoteIsDerivedFromTheBodyItCites' ./internal/store
check "a client cannot hand the node a citation, in the unit" \
	go test -count=1 -run TestClientMetaCannotCarryACitation .
check "the citation is drawn on the message, in the cited speaker's colour, in a browser" \
	browser_draws_a_citation

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
check "the console shows a direct message to its addressee and not to a third principal" \
	console_renders_direct_messages

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
PG5A_PORT="$(free_port 15440)"
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
check "each node's operator pins the other node's signing key" the_nodes_exchange_keys

say "what replicates"
check "A opens pa up to pb, which is what makes pa replicable" a_opens_pa_up_to_pb_on_node_a
check "A writes a shared artifact" a_writes_a_shared_artifact
check "A writes a personal one, and one in a project with no grant" a_writes_what_the_peer_may_not_see
check "A appends a thread of two events" a_appends_a_thread
check "B writes one of its own" b_writes_one_of_its_own
check "sync: A pulls B's delta and pushes its own" the_first_sync
check "A's artifact is on B, same id, same hlc, same author" the_shared_artifact_is_on_b
check "and under the signature nodeA wrote it with" \
	the_replicated_rows_carry_their_authors_signature
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

say "projects across the fabric"
check "two nodes declaring one project converge on one identity" \
	two_nodes_declaring_one_project_converge
check "two different projects with one name are refused, not merged" \
	two_projects_with_one_name_are_refused_not_merged
check "an operator pin settles which project the name means here" \
	an_operator_pin_settles_the_collision

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

# ------------------------------------------------- the third round of fixes
#
# The re-review of the second round: the push side was checked and the pull side
# went straight round it, and seven more behind that. One check each, and every
# one of them fails on the code as it was.

# psql5_do DSN SQL - a statement against one of the two federated databases.
# It is how the gate plays a peer that answers a pull with rows nothing on that
# node would ever have accepted over its own API.
psql5_do() { psql -v ON_ERROR_STOP=1 -q -d "$1" -c "$2"; }

# sync5_flags DSN NODE PORT TOKEN FLAGS... - sync5 with the driver's own flags,
# so a check can drive one half of the exchange on its own.
sync5_flags() {
	local dsn=$1 node=$2 port=$3 token=$4
	shift 4
	SYNC_REPORT="$(DATABASE_URL="$dsn" FLOWY_NODE="$node" \
		"$ROOT/flowy" sync --peer "http://127.0.0.1:$port" --token "$token" "$@")" || return 1
}

# HIGH 1. The pull side merged whatever came back with no check of any kind:
# every check short-circuited on a nil principal and the driver handed it one.
# Being willing to read from a peer was therefore being willing to let it write
# anything - and the first thing a peer writes is a grant, which is a project
# the next pull carries back out.
a_pulled_forgery_is_refused() {
	recall5
	local hlc gid aid eid tid bookmarked
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	# Above nodeA's bookmark for B as well as above B's own clock. A cursor is
	# a promise that everything below it has been dealt with, so a row written
	# under one is a row nodeA has already stepped past - and this check is
	# about what the merge does with the four rows, not about whether they are
	# offered.
	bookmarked="$(scalar5 "$N5_DSN_A" "SELECT coalesce(max(pull_cursor), 0) FROM peers")" || return 1
	if [ "$bookmarked" -ge "$hlc" ]; then
		hlc=$((bookmarked + 65536))
	fi
	gid="pull-grant-$$"
	aid="pull-art-$$"
	eid="pull-ev-$$"
	tid="pull-task-$$"

	# Node B answers the way a hostile peer would: four rows written straight
	# into its database, none of which its own API would have taken. The grant
	# is the one that matters - it opens pz to pb, which is what makes B serve
	# the other three to the principal replication runs as. pz is a project
	# neither node has ever heard of, so nothing but this grant reaches it.
	psql5_do "$N5_DSN_B" "INSERT INTO grants
	    (id, from_project, to_project, cap, granted_by, hlc, node, tombstone)
	    VALUES ('$gid', 'pb', 'pz', 'read', '$N5_USER_B', $((hlc + 1)), 'forger', false)" || return 1
	psql5_do "$N5_DSN_B" "INSERT INTO artifacts
	    (id, type, project, owner_user, title, body, visibility, hlc, node, tombstone)
	    VALUES ('$aid', 'note', 'pz', '$N5_USER_A', 'forged into pz', 'wibblesnatch',
	            'project', $((hlc + 2)), 'forger', false)" || return 1
	psql5_do "$N5_DSN_B" "INSERT INTO events
	    (id, type, project, room, thread, parents, actor, seq_hlc, node, body)
	    VALUES ('$eid', 'chat', 'pz', 'pz/quiet', '$eid', '{}', '$N5_USER_A',
	            $((hlc + 3)), 'forger', 'said by nobody')" || return 1
	psql5_do "$N5_DSN_B" "INSERT INTO tasks
	    (id, artifact, from_user, to_user, project, state, thread, hlc, node)
	    VALUES ('$tid', '$aid', '$N5_USER_A', '$N5_USER_B', 'pz', 'open', '$eid',
	            $((hlc + 4)), 'forger')" || return 1

	# The peer really does hand all four over, so what follows is a check of the
	# merge rather than of the peer being unable to say it.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/sync/pull?since=$hlc" || return 1
	want_eq "rows the peer served" \
		"$(printf '%s' "$API_BODY" |
			jq '[.artifacts[].id, .events[].id, .tasks[].id, .grants[].id] |
			     map(select(. == "'"$gid"'" or . == "'"$aid"'" or . == "'"$eid"'" or . == "'"$tid"'")) |
			     length')" 4 || return 1

	sync5_flags "$N5_DSN_A" nodeA "$N5_PORT_B" "$N5_TOKEN_B" --push=false || return 1
	local refused
	refused="$(printf '%s' "$SYNC_REPORT" | jq '[.refused[]] | add')"
	if [ "$refused" -lt 4 ]; then
		printf 'nodeA refused %s rows, want at least the four forged ones: %s\n' \
			"$refused" "$SYNC_REPORT" >&2
		return 1
	fi

	want_eq "the forged grant on nodeA" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM grants WHERE id = '$gid'")" 0 || return 1
	want_eq "the forged artifact on nodeA" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM artifacts WHERE id = '$aid'")" 0 || return 1
	want_eq "the forged event on nodeA" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM events WHERE id = '$eid'")" 0 || return 1
	want_eq "the forged task on nodeA" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM tasks WHERE id = '$tid'")" 0 || return 1

	# And pz on nodeA is still nothing to do with pb, which is what the grant
	# was for.
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" "/api/artifacts" || return 1
	want_eq "pz artifacts the peer principal can see on nodeA" \
		"$(hits '.project == "pz"')" 0 || return 1

	# The forgery goes back out of the peer, so nothing after this is running
	# against a poisoned node B.
	psql5_do "$N5_DSN_B" "DELETE FROM tasks WHERE id = '$tid';
	                      DELETE FROM events WHERE id = '$eid';
	                      DELETE FROM artifacts WHERE id = '$aid';
	                      DELETE FROM grants WHERE id = '$gid'" || return 1
	printf 'four forged rows served, four refused, none of them on nodeA\n'
}

# HIGH 2. A new artifact was taken whatever project it named and whoever it said
# owned it - the "land where the API would put it" rule was only ever applied to
# rows that were already here - and an owned row could be re-projected, which is
# a row walked out of the project that was reading it.
a_pushed_artifact_lands_where_it_may() {
	recall5
	local new hlc delta id
	new="pushed-into-pz-$$"
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1

	# pz is a project nothing on either node opens to anybody.
	delta="$(jq -nc --arg i "$new" --arg b "$N5_USER_B" --argjson h "$hlc" '
		{events: [], tasks: [], grants: [], hwm: 0, artifacts: [
		  {id: $i, type: "note", project: "pz", owner_user: $b, title: "filed in pz",
		   body: "by somebody with no business in pz", visibility: "project",
		   hlc: $h, node: "nodeA", tombstone: false,
		   reported: false}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "artifacts refused" "$(jqv '.refused.artifacts')" 1 || return 1
	want_eq "artifacts applied" "$(jqv '.applied.artifacts')" 0 || return 1
	want_eq "rows in B's table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$new'")" 0 || return 1

	# And a row of the pusher's own does not move project either. pa is a
	# project the pusher really can read, so what refuses this is the move
	# rather than the reach.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"note","title":"the peer own row","body":"grimsbyfeather"}' || return 1
	id="$(jqv .id)"
	want_eq "the project it landed in" "$(jqv .project)" pb || return 1
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	delta="$(jq -nc --arg i "$id" --arg b "$N5_USER_B" --argjson h "$hlc" '
		{events: [], tasks: [], grants: [], hwm: 0, artifacts: [
		  {id: $i, type: "note", project: "pa", owner_user: $b, title: "the peer own row",
		   body: "grimsbyfeather", visibility: "project", hlc: $h, node: "nodeA",
		   tombstone: false, reported: false}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the re-projection refused" "$(jqv '.refused.artifacts')" 1 || return 1
	want_eq "the re-projection applied" "$(jqv '.applied.artifacts')" 0 || return 1
	want_eq "the project it is still in" \
		"$(scalar5 "$N5_DSN_B" "SELECT project FROM artifacts WHERE id = '$id'")" pb || return 1
	printf 'a row into pc was refused, and %s did not walk from pb into pa\n' "$id"
}

# HIGH 3. A task that was already here was let through whole if the pusher was a
# party to it, and applying a task replaces every column - including the thread,
# which the tasks clause in the event filter turns into a read. So a party could
# re-point their own handoff at any conversation on the node and read it.
a_party_cannot_re_point_its_task() {
	recall5
	local thread art task hlc delta
	# A conversation in pz, which is a project no token here names and no grant
	# reaches. It goes in as a row because there is no principal to write it as,
	# which is the point: it is somebody else's conversation.
	thread="victim-thread-$$"
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	psql5_do "$N5_DSN_B" "INSERT INTO events
	    (id, type, project, room, thread, parents, actor, seq_hlc, node, body)
	    VALUES ('$thread', 'chat', 'pz', 'pz/quiet', '$thread', '{}', '$N5_USER_A',
	            $hlc, 'nodeB', 'the pz thing the assignee may not read')" || return 1

	# A real handoff to the peer, made the way the API makes one.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"bug","title":"the handoff that gets re-pointed","body":"clatterpike"}' || return 1
	art="$(jqv .id)"
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/assign \
		"$(jq -nc --arg a "$art" --arg u "$N5_USER_B" '{artifact: $a, to_user: $u, note: "yours"}')" ||
		return 1
	task="$(jqv .id)"
	want_eq "who it is for" "$(jqv .to_user)" "$N5_USER_B" || return 1
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/events?thread=$thread" || return 1
	want_eq "what the assignee can read of pz to begin with" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 0 || return 1
	local was
	was="$(scalar5 "$N5_DSN_B" "SELECT thread FROM tasks WHERE id = '$task'")" || return 1

	# The assignee pushes its own task back with the thread swapped for the one
	# it wants to read.
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	delta="$(jq -nc --arg t "$task" --arg a "$art" --arg f "$N5_USER_A" --arg u "$N5_USER_B" \
		--arg th "$thread" --argjson h "$hlc" '
		{artifacts: [], events: [], grants: [], hwm: 0, tasks: [
		  {id: $t, artifact: $a, from_user: $f, to_user: $u, project: "pa", state: "open",
		   assignee_agent: "", thread: $th, hlc: $h,
		   node: "nodeA"}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "tasks refused" "$(jqv '.refused.tasks')" 1 || return 1
	want_eq "tasks applied" "$(jqv '.applied.tasks')" 0 || return 1
	want_eq "the thread the task still names" \
		"$(scalar5 "$N5_DSN_B" "SELECT thread FROM tasks WHERE id = '$task'")" "$was" || return 1

	# Which is the whole point: the conversation is still somebody else's.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/events?thread=$thread" || return 1
	want_eq "what the assignee can read of it now" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 0 || return 1
	printf 'task %s could not be re-pointed at thread %s, which still reads back empty\n' \
		"$task" "$thread"
}

# MEDIUM 4. Writing into a thread needed no read on it. A thread id is a guess
# anybody can make and the tasks clause shows a thread to the parties, so a
# message dropped into somebody else's conversation was read by exactly the
# people whose conversation it is not.
a_message_does_not_enter_a_thread_it_cannot_read() {
	recall
	local thread
	api POST "$TOKEN_A_PC" /api/events \
		'{"type":"note","room":"pc/quiet","body":"the pc conversation"}' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	thread="$(jqv .thread)"

	want_status 403 POST "$TOKEN_B" /api/chat/general/say \
		"$(jq -nc --arg t "$thread" '{body: "let me in", thread: $t}')" || return 1
	want_status 403 POST "$TOKEN_B" /api/events \
		"$(jq -nc --arg t "$thread" '{type: "note", room: "pb/bugs", thread: $t, body: "or here"}')" ||
		return 1

	# Nothing landed, and the thread is what it was.
	api GET "$TOKEN_A_PC" "/api/events?thread=$thread" || return 1
	want_eq "events in the thread" "$(printf '%s' "$API_BODY" | jq '.events | length')" 1 || return 1
	want_eq "the one that is there" "$(jqv '.events[0].body')" "the pc conversation" || return 1

	# Saying something without naming a thread still works, and starts one.
	api POST "$TOKEN_B" /api/chat/general/say '{"body":"then I will start my own"}' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	if [ "$(jqv .thread)" = "$thread" ]; then
		printf 'a fresh message joined %s anyway\n' "$thread" >&2
		return 1
	fi
	printf 'thread %s is closed to the speaker who cannot read it, and a fresh one is not\n' "$thread"
}

# MEDIUM 5. Two nodes writing in the same millisecond with the same logical
# counter produce two equal readings and two different rows, and a merge that
# compares on the reading alone has each node refusing the other's row forever.
# The node name is the tiebreak, and it has to be the same tiebreak on both.
a_tied_reading_still_has_a_winner() {
	recall5
	local id hlc delta zz_seed aa_seed
	# Two nodes that exist only for this check, each a keypair and a name. Node
	# B's operator pins both, because a row from a node whose key is not here is
	# refused before the tiebreak is ever reached - and what is being tested is
	# the tiebreak.
	zz_seed="$(seed_of zz-node)" || return 1
	aa_seed="$(seed_of aa-node)" || return 1
	local node
	for node in "$N5_DSN_A:nodeA" "$N5_DSN_B:nodeB"; do
		# Both machines, because the winning row replicates onward: a node that
		# never speaks for itself has no way to hand its key to the far side, so
		# the operator does it. An identity that arrived on a page relays; a pin
		# is local to the machine it was made on.
		pin_key "${node%:*}" "${node#*:}" zz-node "$(key_of zz-node "$zz_seed")" >/dev/null ||
			return 1
		pin_key "${node%:*}" "${node#*:}" aa-node "$(key_of aa-node "$aa_seed")" >/dev/null ||
			return 1
	done

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"note","title":"written on nodeB","body":"snaffleburr"}' || return 1
	id="$(jqv .id)"
	hlc="$(jqv .hlc)"
	want_eq "the node that wrote it" "$(jqv .node)" nodeB || return 1

	# The same reading, from a node whose name sorts after nodeB: it wins.
	delta="$(jq -nc --arg i "$id" --arg b "$N5_USER_B" --argjson h "$hlc" '
		{events: [], tasks: [], grants: [], hwm: 0, artifacts: [
		  {id: $i, type: "note", project: "pb", owner_user: $b, title: "written on zz",
		   body: "snaffleburr", visibility: "project", hlc: $h, node: "zz-node",
		   tombstone: false, reported: false}]}' | sign_seed "$zz_seed")" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the tie applied" "$(jqv '.applied.artifacts')" 1 || return 1
	want_eq "the tie refused" "$(jqv '.refused.artifacts')" 0 || return 1
	want_eq "the title now" \
		"$(scalar5 "$N5_DSN_B" "SELECT title FROM artifacts WHERE id = '$id'")" "written on zz" ||
		return 1
	want_eq "and the reading it is at" \
		"$(scalar5 "$N5_DSN_B" "SELECT hlc FROM artifacts WHERE id = '$id'")" "$hlc" || return 1

	# The same reading from a node whose name sorts before it loses, and losing
	# is not being refused: it is a delta being replayed.
	delta="$(jq -nc --arg i "$id" --arg b "$N5_USER_B" --argjson h "$hlc" '
		{events: [], tasks: [], grants: [], hwm: 0, artifacts: [
		  {id: $i, type: "note", project: "pb", owner_user: $b, title: "written on aa",
		   body: "snaffleburr", visibility: "project", hlc: $h, node: "aa-node",
		   tombstone: false, reported: false}]}' | sign_seed "$aa_seed")" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the losing side applied" "$(jqv '.applied.artifacts')" 0 || return 1
	want_eq "the losing side refused" "$(jqv '.refused.artifacts')" 0 || return 1
	want_eq "the title after it" \
		"$(scalar5 "$N5_DSN_B" "SELECT title FROM artifacts WHERE id = '$id'")" "written on zz" ||
		return 1
	printf 'at %s, zz-node beats nodeB and aa-node loses to it - the same way round on any node\n' \
		"$hlc"
}

# MEDIUM 6. A status refresh spends this node's forge credential and writes: it
# moves the artifact to done and signs a status event. It was gated on being
# able to read the artifact, so a read-share was enough to make the node act
# outside itself - which filing and syncing have never allowed.
forge_status_is_the_owners() {
	recall
	local id was
	id="$(new_artifact "$TOKEN_A_PC" bug "the wipers judder on the return stroke")" || return 1
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filed" "$API_STATUS" 200 || return 1
	api POST "$TOKEN_A_PC" /api/grants \
		"$(jq -nc --arg a "$id" --arg s "$USER_B" '{artifact: $a, subject: $s}')" || return 1
	want_eq "shared with B" "$API_STATUS" 200 || return 1
	want_status 200 GET "$TOKEN_B" "/api/artifact/$id" || return 1
	was="$(jqv .status)"

	want_status 403 GET "$TOKEN_B" "/api/forge/status?artifact=$id" || return 1
	case "$API_BODY" in
	*"only the owner"*) ;;
	*)
		printf 'the refusal does not say it is the owner: %s\n' "$API_BODY" >&2
		return 1
		;;
	esac
	# The owner's own refresh still goes through, so what is being tested is who
	# may ask rather than the endpoint being broken.
	forge_status "$TOKEN_A_PC" "$id" || return 1
	want_eq "the owner's refresh" "$API_STATUS" 200 || return 1
	api GET "$TOKEN_B" "/api/artifact/$id" || return 1
	want_eq "the status the reader still sees" "$(jqv .status)" "$was" || return 1
	printf 'a reader may read %s and may not spend the node credential on it\n' "$id"
}

# MEDIUM/LOW 7. The push cursor moved to the high water mark whatever the peer
# said, so a row the peer refused was never offered again: the two nodes differ
# from then on and nothing says so.
a_refused_push_does_not_move_the_cursor() {
	recall5
	local peer before after art bad hlc
	peer="http://127.0.0.1:$N5_PORT_B"
	before="$(scalar5 "$N5_DSN_A" "SELECT pushed_cursor FROM peers WHERE peer = '$peer'")" || return 1

	# The share the rest of this check follows to the peer: A's own artifact in
	# pc, handed to B by the person who owns it.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_PC" /api/artifacts \
		'{"type":"note","title":"the pc one that gets shared","body":"cloddlewhisk"}' || return 1
	art="$(jqv .id)"
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_PC" /api/grants \
		"$(jq -nc --arg a "$art" --arg s "$N5_USER_B" '{artifact: $a, subject: $s}')" || return 1

	# And the row the peer refuses, which is what the cursor is being held
	# against. It is written straight into node A's store with nothing signed
	# on it, in a project the replication principal reads - so A really does
	# offer it and B really does refuse it, on the merge rule that has nothing
	# to do with which door it arrived at. This used to be the share above: node
	# B refused it from this pusher and took the same row when it pulled it,
	# which is the asymmetry the fourteenth round removed. A refusal a check
	# like this leans on has to be about the row rather than about the door.
	bad="unsigned-push-$$-$(date +%s)"
	hlc="$(forged_hlc "$N5_PORT_A")" || return 1
	psql5_do "$N5_DSN_A" "INSERT INTO artifacts
	    (id, type, project, owner_user, title, body, visibility, hlc, node, tombstone)
	    VALUES ('$bad', 'note', 'pb', '$N5_USER_B', 'the one nothing signed', 'wrenchfell',
	            'project', $hlc, 'nodeA', false)" || return 1

	sync5_flags "$N5_DSN_A" nodeA "$N5_PORT_B" "$N5_TOKEN_B" --pull=false || return 1
	local refused sent
	refused="$(printf '%s' "$SYNC_REPORT" | jq '[.peer_refused[]] | add')"
	if [ "$refused" -lt 1 ]; then
		printf 'the peer refused nothing, so there is no cursor to hold: %s\n' "$SYNC_REPORT" >&2
		return 1
	fi
	after="$(scalar5 "$N5_DSN_A" "SELECT pushed_cursor FROM peers WHERE peer = '$peer'")" || return 1
	want_eq "the cursor after a refused page" "$after" "$before" || return 1

	# And the next push offers the same rows again rather than skipping them.
	sync5_flags "$N5_DSN_A" nodeA "$N5_PORT_B" "$N5_TOKEN_B" --pull=false || return 1
	sent="$(printf '%s' "$SYNC_REPORT" | jq '[.pushed[]] | add')"
	if [ "$sent" -lt 1 ]; then
		printf 'the second push offered nothing, so the refused rows were dropped: %s\n' \
			"$SYNC_REPORT" >&2
		return 1
	fi
	after="$(scalar5 "$N5_DSN_A" "SELECT pushed_cursor FROM peers WHERE peer = '$peer'")" || return 1
	want_eq "the cursor after the second refused page" "$after" "$before" || return 1

	# Take the unsigned row out of A's store, and the same page has nothing on
	# it for the peer to refuse: the cursor comes unstuck and the rows behind it
	# - the share and the artifact it opens - reach the peer.
	psql5_do "$N5_DSN_A" "DELETE FROM artifacts WHERE id = '$bad'" || return 1
	sync_round || return 1
	sync_round || return 1
	after="$(scalar5 "$N5_DSN_A" "SELECT pushed_cursor FROM peers WHERE peer = '$peer'")" || return 1
	if [ "$after" -le "$before" ]; then
		printf 'the cursor never came unstuck: %s -> %s\n' "$before" "$after" >&2
		return 1
	fi
	want_eq "the share reached the peer in the end" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$art'")" 1 || return 1
	printf 'held at %s while the peer refused, and moved to %s once it did not\n' "$before" "$after"
}

say "the pull side is not a way in"
check "a peer that answers a pull with rows it invented is refused (HIGH 1)" \
	a_pulled_forgery_is_refused

say "a replicated artifact lands where it may"
check "a pushed artifact cannot be filed into a project the pusher cannot reach (HIGH 2)" \
	a_pushed_artifact_lands_where_it_may

say "a task is a read, and a party does not re-point it"
check "a party cannot swap its task's thread for one it may not read (HIGH 3)" \
	a_party_cannot_re_point_its_task

say "you cannot write into a conversation you cannot see"
check "a message into a thread the speaker cannot read is refused (MED 4)" \
	a_message_does_not_enter_a_thread_it_cannot_read

say "the merge order is total"
check "two rows at the same reading have a winner, the same one on every node (MED 5)" \
	a_tied_reading_still_has_a_winner

say "the forge, a third time"
check "a status refresh is the owner's, not every reader's (MED 6)" \
	forge_status_is_the_owners

say "a refused row is not a dropped row"
check "a push the peer refused does not move the cursor past it (MED/LOW 7)" \
	a_refused_push_does_not_move_the_cursor

say "the delete says whose it is"
check "a reader cannot tombstone an artifact it does not own (LOW 8)" \
	go test -count=1 -run TestTombstoneNamesTheOwner ./internal/store

# ------------------------------------------------ the fourth round of fixes
#
# The re-review of the third round: the big items held, and eight smaller ones
# behind them - a cursor that is one integer over a two-column order, a create
# that read "not readable" as "not there", two columns of a task nobody
# checked, a thread inherited rather than tested, a delete that only removed
# the row from the lists, a scope that was documented narrower than it was, the
# node's own row counts answered to anybody, and four multi-row writes that
# were not one write. One check each, and every one of them fails on the source
# it fixes.

# node_hlc - a reading comfortably above anything the single node has minted,
# so a row written straight into its tables is newer than everything there.
node_hlc() {
	api GET "" /healthz || return 1
	printf '%s' "$(($(jqv .hlc) + 65536))"
}

# HIGH 1. Every page is ordered by (reading, id) and was paged by the reading
# alone. Two rows at one reading - two nodes writing in the same millisecond, a
# handoff stamping its three rows together - straddling a page boundary meant
# the second was handed over never: the cursor moved to the first one's reading
# and "strictly greater" did the rest. Silent and permanent, in replication and
# in the chat log alike.
a_page_does_not_cut_a_reading_in_half() {
	recall
	recall5
	# napi and want_napi point the helpers at one of the federated nodes and
	# leave them there, so the single node's port is put by for the second half.
	local gate=$HTTP_PORT
	local hlc first second hwm carried
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	first="tie-a-$$"
	second="tie-b-$$"

	# Two artifacts of the peer's own, at exactly one reading, ids in sort
	# order. Straight into the table, because nothing mints two readings the
	# same on one node - it takes two of them, which is the case this is about.
	psql5_do "$N5_DSN_B" "INSERT INTO artifacts
	    (id, type, project, owner_user, title, body, visibility, hlc, node, tombstone)
	    VALUES ('$first', 'note', 'pb', '$N5_USER_B', 'first at the reading', 'twinnerdash',
	            'project', $hlc, 'nodeB', false),
	           ('$second', 'note', 'pb', '$N5_USER_B', 'second at the reading', 'twinnerdash',
	            'project', $hlc, 'nodeB', false)" || return 1

	# One row per table per page, so the boundary falls between the two.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" \
		"/api/sync/pull?since=$((hlc - 1))&limit=1" || return 1
	carried="$(printf '%s' "$API_BODY" |
		jq '[.artifacts[].id | select(. == "'"$first"'" or . == "'"$second"'")] | length')"
	want_eq "the rows one page carried" "$carried" 2 || return 1

	# And the cursor it reports does not step over either of them: what is above
	# it is nothing of theirs.
	hwm="$(jqv .hwm)"
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/sync/pull?since=$hwm&limit=1" || return 1
	carried="$(printf '%s' "$API_BODY" |
		jq '[.artifacts[].id | select(. == "'"$first"'" or . == "'"$second"'")] | length')"
	want_eq "the rows left above the cursor" "$carried" 0 || return 1
	psql5_do "$N5_DSN_B" "DELETE FROM artifacts WHERE id IN ('$first', '$second')" || return 1

	# The same hole in the log a chat client pages, which is the same code.
	HTTP_PORT=$gate
	local chlc e1 e2
	chlc="$(node_hlc)" || return 1
	e1="tie-ev-a-$$"
	e2="tie-ev-b-$$"
	psql_do "INSERT INTO events
	    (id, type, project, room, thread, parents, actor, seq_hlc, node, body)
	    VALUES ('$e1', 'chat', 'pa', 'tieroom', '$e1', '{}', '$USER_A', $chlc, 'gate',
	            'the first at the reading'),
	           ('$e2', 'chat', 'pa', 'tieroom', '$e1', '{}', '$USER_A', $chlc, 'gate',
	            'the second at the reading')" || return 1

	api GET "$TOKEN_A" "/api/chat/tieroom?since=$((chlc - 1))&limit=1" || return 1
	want_eq "messages one page carried" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 2 || return 1
	want_eq "the cursor it reports" "$(jqv .cursor)" "$chlc" || return 1
	api GET "$TOKEN_A" "/api/chat/tieroom?since=$(jqv .cursor)&limit=1" || return 1
	want_eq "messages left above that cursor" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 0 || return 1
	printf 'both rows at %s replicated, and both messages at %s read back\n' "$hlc" "$chlc"
}

# HIGH 2. handleCreateArtifact read the id through the permission filter and
# read "nothing you may see" as "nothing there". The owner of an artifact in
# one project, holding a token for another, could POST its id and take the
# update branch on a row they could not read: it moved project, lost every
# field the request left out, and a deleted one came back.
a_create_does_not_move_the_row_it_cannot_see() {
	recall
	local id
	api POST "$TOKEN_A" /api/artifacts '{
		"type": "note", "title": "the pa one", "body": "grumblewick",
		"discovery": "written in pa and staying there", "tags": ["pa"]
	}' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	id="$(jqv .id)"
	want_eq "the project it landed in" "$(jqv .project)" pa || return 1

	# The same person, in another project, naming that id. pc holds no grant
	# into pa, so the row is out of this principal's reach - and out of reach is
	# the answer it gets.
	want_status 404 POST "$TOKEN_A_PC" /api/artifacts \
		"$(jq -nc --arg i "$id" '{id: $i, type: "note", title: "moved to pc"}')" || return 1

	api GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "the project it is still in" "$(jqv .project)" pa || return 1
	want_eq "the title" "$(jqv .title)" "the pa one" || return 1
	want_eq "the body" "$(jqv .body)" grumblewick || return 1
	want_eq "the discovery" "$(jqv .discovery)" "written in pa and staying there" || return 1
	want_eq "rows for it in pc" "$(scalar "SELECT count(*) FROM artifacts
	    WHERE id = '$id' AND project = 'pc'")" 0 || return 1
	printf '%s stayed in pa with every field it had\n' "$id"
}

# HIGH 3. checkTask let a party through on the two columns a party may move,
# and checked neither of them. assignee_agent is the third read capability on
# the row - the tasks clause shows the thread to the agent named there - so a
# party could hand its own handoff's conversation to anybody's agent, and park
# the task in a state the lifecycle has no way out of.
a_party_cannot_delegate_its_task_to_a_stranger() {
	recall5
	local art task thread project hlc delta was
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"bug","title":"the handoff that gets re-delegated","body":"grindlespoke"}' || return 1
	art="$(jqv .id)"
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/assign \
		"$(jq -nc --arg a "$art" --arg u "$N5_USER_B" '{artifact: $a, to_user: $u, note: "yours"}')" ||
		return 1
	task="$(jqv .id)"
	thread="$(jqv .thread)"
	project="$(jqv .project)"
	was="$(scalar5 "$N5_DSN_B" "SELECT coalesce(assignee_agent, '') FROM tasks WHERE id = '$task'")" ||
		return 1

	# push_task STATE AGENT - the assignee pushes its own task back, moved.
	push_task() {
		hlc="$(forged_hlc "$N5_PORT_B")" || return 1
		delta="$(jq -nc --arg t "$task" --arg a "$art" --arg f "$N5_USER_A" --arg u "$N5_USER_B" \
			--arg p "$project" --arg th "$thread" --arg st "$1" --arg ag "$2" --argjson h "$hlc" '
			{artifacts: [], events: [], grants: [], hwm: 0, tasks: [
			  {id: $t, artifact: $a, from_user: $f, to_user: $u, project: $p, state: $st,
			   assignee_agent: $ag, thread: $th, hlc: $h,
			   node: "nodeA"}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1
		want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta"
	}

	# A's agent does not act for the assignee, so it is not the assignee's to
	# delegate to - the rule POST /api/task/{id}/delegate keeps.
	push_task delegated "$N5_AGENT_A" || return 1
	want_eq "the stranger's agent refused" "$(jqv '.refused.tasks')" 1 || return 1
	want_eq "the stranger's agent applied" "$(jqv '.applied.tasks')" 0 || return 1
	want_eq "the agent it still names" \
		"$(scalar5 "$N5_DSN_B" "SELECT coalesce(assignee_agent, '') FROM tasks WHERE id = '$task'")" \
		"$was" || return 1

	# Nor is a state the lifecycle has never heard of a move.
	push_task mine-now "$was" || return 1
	want_eq "the invented state refused" "$(jqv '.refused.tasks')" 1 || return 1
	want_eq "the state it is still in" \
		"$(scalar5 "$N5_DSN_B" "SELECT state FROM tasks WHERE id = '$task'")" delegated || return 1

	# And the move a party really can make is still a move.
	push_task "done" "$N5_AGENT_B" || return 1
	want_eq "the real move applied" "$(jqv '.applied.tasks')" 1 || return 1
	want_eq "the state it is in now" \
		"$(scalar5 "$N5_DSN_B" "SELECT state FROM tasks WHERE id = '$task'")" "done" || return 1
	printf 'task %s would not go to %s, nor to mine-now, and still closed\n' "$task" "$N5_AGENT_A"
}

# MEDIUM 4. A reply with no thread of its own inherits its parent's, and the
# parent was read through the filter while the thread it names was not. A
# readable message in an unreadable conversation is exactly what the tasks
# clause produces, so answering one put the speaker inside a conversation they
# cannot see - and put what they said next in front of the people whose it is.
a_reply_does_not_join_a_thread_it_cannot_read() {
	recall
	local thread bridge hlc landed
	api POST "$TOKEN_A_PC" /api/chat/pcquiet/say \
		'{"body":"the pc conversation nobody else reads"}' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	thread="$(jqv .thread)"

	# One message of that thread that B may read, in B's own project. No
	# principal here could have written it - both endpoints refuse exactly this
	# - so it goes in as a row, which is what a merge from a peer would do.
	hlc="$(node_hlc)" || return 1
	bridge="bridge-ev-$$"
	psql_do "INSERT INTO events
	    (id, type, project, room, thread, parents, actor, seq_hlc, node, body)
	    VALUES ('$bridge', 'chat', 'pb', 'general', '$thread', '{}', '$USER_B', $hlc, 'gate',
	            'the one message of it B can read')" || return 1
	api GET "$TOKEN_B" "/api/events?thread=$thread" || return 1
	want_eq "what B can read of the thread" \
		"$(printf '%s' "$API_BODY" | jq '.events | length')" 1 || return 1

	api POST "$TOKEN_B" /api/chat/general/say \
		"$(jq -nc --arg p "$bridge" '{body: "answering the one I can see", parents: [$p]}')" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	landed="$(jqv .thread)"
	if [ "$landed" = "$thread" ]; then
		printf 'the reply joined %s, which the speaker cannot read\n' "$thread" >&2
		return 1
	fi
	want_eq "messages in the pc thread" \
		"$(scalar "SELECT count(*) FROM events WHERE thread = '$thread'")" 2 || return 1
	printf 'the reply started thread %s instead of joining %s\n' "$landed" "$thread"
}

# HIGH 5. ReadArtifact ignored the tombstone, so a deleted artifact was still
# readable by id, still had a status to move, still had a history and could
# still be filed as an issue - and an edit of one brought it back with a fresh
# reading that beat the delete on every peer.
a_deleted_artifact_is_gone_and_stays_gone() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A" note "the one that gets deleted")" || return 1
	# WHAT B COULD SEE BEFORE THE DELETE. The property is not "B gets 404", it is
	# THE DISCLOSURE IS SCOPED TO REACH: whoever could read the row is told it was
	# withdrawn, and whoever could not still cannot tell it from one that never
	# existed. A fixed expectation here would be a guess about B's reach at this
	# point in the run - and reach changes as earlier phases add grants, so the
	# guess would be a true reading of the wrong population.
	local b_before
	api GET "$TOKEN_B" "/api/artifact/$id" || true
	b_before=$API_STATUS
	api POST "$TOKEN_A" "/api/artifact/$id/delete" || return 1
	want_eq "delete status" "$API_STATUS" 200 || return 1

	# 410, not 404, TO SOMEBODY WHO COULD HAVE READ IT. 404 says "never existed",
	# which is the one claim a tombstone exists to deny - and tonight twenty
	# minutes went into ids answering 404 that were neither absent nor deleted.
	want_status 410 GET "$TOKEN_A" "/api/artifact/$id" || return 1
	want_eq "and it says the row was withdrawn" \
		"$(printf '%s' "$API_BODY" | jq -r '.withdrawn.id')" "$id" || return 1
	# AND THE LEAK CHECK, stated as the invariant rather than as a number: B is
	# told exactly what B was entitled to know a moment earlier. A reader who
	# could not reach the row must not be able to tell a withdrawal from an
	# absence, or a guessable id becomes an existence oracle.
	if [ "$b_before" = 200 ]; then
		want_status 410 GET "$TOKEN_B" "/api/artifact/$id" || return 1
	else
		want_status 404 GET "$TOKEN_B" "/api/artifact/$id" || return 1
	fi
	printf 'B read it as %s before the delete, and is answered accordingly after\n' "$b_before"
	# Everything else stays absent: withdrawn is a sentence about a read, not a
	# door back into the artifact.
	want_status 404 GET "$TOKEN_A" "/api/artifact/$id/history" || return 1
	want_status 404 POST "$TOKEN_A" "/api/artifact/$id/status" '{"status":"triaged"}' || return 1

	# And an edit by its owner is not a resurrection.
	want_status 404 POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg i "$id" '{id: $i, type: "note", title: "back from the dead"}')" || return 1
	want_eq "still a tombstone" \
		"$(scalar "SELECT tombstone FROM artifacts WHERE id = '$id'")" t || return 1
	want_eq "with the title it was deleted under" \
		"$(scalar "SELECT title FROM artifacts WHERE id = '$id'")" "the one that gets deleted" ||
		return 1
	printf '%s reads as absent everywhere, and an edit did not bring it back\n' "$id"
}

# MEDIUM 6. The memory tools offer three scopes and the store had two: 'project'
# and 'shared' were the same row test, so an item written at the narrower of
# them was readable by everyone the wider one reaches. An agent choosing the
# scope it was told meant "my project" got "my project and whoever holds a
# grant on it".
a_project_scoped_memory_stays_in_its_project() {
	recall
	local id
	want_tool mem_write "$TOKEN_A" '{
		"title": "how the gate starts postgres",
		"body": "crumplebosk is the word",
		"scope": "project"
	}' || return 1
	id="$(tv .item.id)"
	want_eq "the project it landed in" "$(tv .item.project)" pa || return 1

	# B holds the grant Phase 1 issued on pa and reads shared memory in pa with
	# it, which is the control: the grant is live and the surface works.
	want_tool mem_search "$TOKEN_B_AGENT" '{"q":"wobblethorn"}' || return 1
	want_eq "the shared item across the grant" "$(tv .count)" 1 || return 1

	want_tool mem_search "$TOKEN_B_AGENT" '{"q":"crumplebosk"}' || return 1
	want_eq "the project item across the same grant" "$(tv .count)" 0 || return 1
	want_tool_fails mem_read "$TOKEN_B_AGENT" "{\"id\": \"$id\"}" "no such memory item" || return 1

	# Its own project reads it, or the scope would be personal by another name.
	want_tool mem_read "$TOKEN_A" "{\"id\": \"$id\"}" || return 1
	want_eq "what its own project reads" "$(tv .item.id)" "$id" || return 1
	printf 'memory %s is pa and only pa, grant or no grant\n' "$id"
}

# MEDIUM 7. /healthz?counts=1 answered the row count of every spine table -
# users, tokens, grants, artifacts, events, tasks - to anybody who asked, with
# no credential at all. That is the shape and the size of what the node holds.
healthz_counts_are_the_operators() {
	recall
	api GET "" '/healthz?counts=1' || return 1
	want_eq "status with no token" "$API_STATUS" 200 || return 1
	want_eq "counts to a stranger" "$(jqv '.counts')" null || return 1
	api GET "$TOKEN_A" '/healthz?counts=1' || return 1
	want_eq "counts to an ordinary token" "$(jqv '.counts')" null || return 1

	api GET "$TOKEN_OP" '/healthz?counts=1' || return 1
	want_eq "status for the operator" "$API_STATUS" 200 || return 1
	if [ "$(jqv '.counts.tokens')" = null ]; then
		printf 'the operator asked for counts and got none: %s\n' "$API_BODY" >&2
		return 1
	fi

	# The health check itself is still open, because one that needs a credential
	# is one that stops working at the worst moment.
	api GET "" /healthz || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "ok" "$(jqv .ok)" true || return 1
	printf 'counts are the operator view, %s tables of it; /healthz is open\n' \
		"$(printf '%s' "$API_BODY" | jq '.counts | length')"
}

say "a cursor is a page boundary, and a page has two columns"
check "two rows at one reading are both delivered (HIGH 1)" \
	a_page_does_not_cut_a_reading_in_half

say "a create is not an update of what you cannot see"
check "posting an id you cannot read is 404 and moves nothing (HIGH 2)" \
	a_create_does_not_move_the_row_it_cannot_see

say "the two columns a party may move"
check "a party cannot delegate its task to somebody else's agent (HIGH 3)" \
	a_party_cannot_delegate_its_task_to_a_stranger

say "a parent you can read is not a thread you can read"
check "an inherited thread the speaker cannot read starts a fresh one (MED 4)" \
	a_reply_does_not_join_a_thread_it_cannot_read

say "a delete is a delete"
check "a tombstoned artifact is 404 and an edit does not raise it (HIGH 5)" \
	a_deleted_artifact_is_gone_and_stays_gone

say "the scopes mean what they say"
check "a project-scoped memory item is not readable across a grant (MED 6)" \
	a_project_scoped_memory_stays_in_its_project

say "the node's own numbers"
check "healthz counts are the operator's, and /healthz stays open (MED 7)" \
	healthz_counts_are_the_operators

say "one operation, one write"
check "a failure mid-assignment leaves no half of it behind (MED 8)" \
	go test -count=1 \
	-run 'TestWriteAssignmentIsAllOrNothing|TestMoveArtifactStatusIsAllOrNothing' ./internal/store

# ------------------------------------------------- the fifth round of fixes
#
# The re-review of the fourth: ten more, and the theme is that a rule kept in
# one place was not kept in the other. The pull side of the merge took a grant
# that opened the puller's own project up and a minted event the push side has
# always refused; the pull side of the driver stepped past a row it had just
# refused, which the push side stopped doing a round ago. The mock forge's
# control routes - being the other side of the conversation - answered any
# token at all. A memory update rewrote the project of an item it did not
# write. A task moved in two writes. /healthz spent a clock reading to report
# one. A share believed the projects its body named. And meta was a second way
# to sign an event after the first was closed.

# LOW/MED. Being the forge is being the machine: the mock's control routes say
# who the forge is logged in as, what a reviewer said, and when the next call
# fails, and any token that authenticated at all could drive every one of them.
# A reader of one artifact could close somebody else's issue, put words in a
# reviewer's mouth, and rename the login the node posts under.
the_mock_forge_is_the_operators() {
	recall
	want_status 403 POST "$TOKEN_A_PC" /api/forge/mock/fail '{"after":1}' || return 1
	want_status 403 POST "$TOKEN_A_PC" /api/forge/mock/state \
		'{"repo":"o/r","number":1,"state":"closed"}' || return 1
	want_status 403 POST "$TOKEN_A_PC" /api/forge/mock/comment \
		'{"repo":"o/r","number":1,"author":"reviewer","body":"said by a stranger"}' || return 1
	want_status 403 POST "$TOKEN_A_PC" /api/forge/mock/login '{"login":"impostor"}' || return 1
	want_status 403 POST "$TOKEN_A_PC" /api/forge/mock/on-file \
		'{"author":"reviewer","body":"armed by a stranger"}' || return 1
	want_status 403 GET "$TOKEN_A_PC" '/api/forge/mock/issue?repo=o/r&number=1' || return 1

	# The owner of the filed artifact is no different: this is not about the
	# artifact, it is about the machine.
	want_status 403 POST "$TOKEN_B" /api/forge/mock/fail '{"after":1}' || return 1
	# With no token at all it is the mount that answers, one step earlier.
	want_status 401 POST "" /api/forge/mock/fail '{"after":1}' || return 1

	# And the operator still drives them, or the gate would be a break rather
	# than a rule. login back to what phase 6 left it as.
	want_status 200 POST "$TOKEN_OP" /api/forge/mock/login '{"login":"flowy"}' || return 1
	printf 'all six mock control routes are the operators, and %s still drives them\n' "$USER_OP"
}

# HIGH. A refused row held the push cursor a round ago and did not hold the pull
# one: the driver moved its bookmark to the page's high water mark whatever the
# merge had said about the rows in it. So a row this node would not take was
# offered once, refused once, and never offered again - the two nodes differ
# from then on, with the reason buried in one run's report.
a_refused_pull_does_not_move_the_cursor() {
	recall5
	local peer before after forged art hlc refused
	peer="http://127.0.0.1:$N5_PORT_A"

	# Settle first, so what follows is about the rows this check writes.
	sync_round || return 1
	sync_round || return 1
	before="$(scalar5 "$N5_DSN_B" "SELECT pull_cursor FROM peers WHERE peer = '$peer'")" || return 1

	# One ordinary row for the page to carry besides the forgery, written
	# through the API so it is stamped by node A's own clock.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"note","title":"the one behind the refusal","body":"spindlewrack"}' || return 1
	art="$(jqv .id)"

	# And a grant on node A that opens node B's own project up, signed by
	# somebody node B has never heard of. It reaches the replication principal -
	# to_project is that principal's project - and the pull check refuses it.
	hlc="$(forged_hlc "$N5_PORT_A")" || return 1
	forged="forged-grant-$$-$(date +%s)"
	psql5_do "$N5_DSN_A" "INSERT INTO grants
	    (id, from_project, to_project, subject, artifact, cap, granted_by, hlc, node, tombstone)
	    VALUES ('$forged', 'pz-nowhere', 'pb', '', '', 'read', 'u-stranger', $hlc, 'nodeA', false)" ||
		return 1

	sync5_flags "$N5_DSN_B" nodeB "$N5_PORT_A" "$N5_TOKEN_B" --push=false || return 1
	refused="$(printf '%s' "$SYNC_REPORT" | jq '[.refused[]] | add')"
	if [ "$refused" -lt 1 ]; then
		printf 'the pull refused nothing, so there is no cursor to hold: %s\n' "$SYNC_REPORT" >&2
		return 1
	fi
	want_eq "the forged grant on B" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM grants WHERE id = '$forged'")" 0 || return 1
	after="$(scalar5 "$N5_DSN_B" "SELECT pull_cursor FROM peers WHERE peer = '$peer'")" || return 1
	want_eq "the cursor after a refused page" "$after" "$before" || return 1

	# The next pull is offered the same row again rather than stepping past it.
	sync5_flags "$N5_DSN_B" nodeB "$N5_PORT_A" "$N5_TOKEN_B" --push=false || return 1
	refused="$(printf '%s' "$SYNC_REPORT" | jq '[.refused[]] | add')"
	if [ "$refused" -lt 1 ]; then
		printf 'the second pull was not offered the refused row: %s\n' "$SYNC_REPORT" >&2
		return 1
	fi
	after="$(scalar5 "$N5_DSN_B" "SELECT pull_cursor FROM peers WHERE peer = '$peer'")" || return 1
	want_eq "the cursor after the second refused page" "$after" "$before" || return 1

	# Take the forgery off A and the cursor comes unstuck, which is what makes
	# this a hold rather than a wedge.
	psql5_do "$N5_DSN_A" "DELETE FROM grants WHERE id = '$forged'" || return 1
	sync5_flags "$N5_DSN_B" nodeB "$N5_PORT_A" "$N5_TOKEN_B" --push=false || return 1
	after="$(scalar5 "$N5_DSN_B" "SELECT pull_cursor FROM peers WHERE peer = '$peer'")" || return 1
	if [ "$after" -le "$before" ]; then
		printf 'the cursor never came unstuck: %s -> %s\n' "$before" "$after" >&2
		return 1
	fi
	want_eq "the ordinary row behind it, on B" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$art'")" 1 || return 1
	sync_round || return 1
	printf 'held at %s while %s was refused, and moved to %s once it was gone\n' \
		"$before" "$forged" "$after"
}

# MED/HIGH. mem_write's update path rewrote the artifact's project to the
# token's own, every time. An owner holding tokens in two projects moved their
# own item out of one and into the other by editing its title - silently, and
# past the rule POST /api/artifacts and the merge both keep, which is that a
# principal writes in its own project or not at all.
a_memory_item_does_not_change_project_when_it_is_edited() {
	recall
	local id
	want_tool mem_write "$TOKEN_A_PC" '{
		"title": "the one that stays in pc",
		"body": "wrinklethorpe is the word",
		"scope": "shared"
	}' || return 1
	id="$(tv .item.id)"
	want_eq "the project it landed in" "$(tv .item.project)" pc || return 1

	# pc opens up to pa, so the owner's other token can read the item at all:
	# what is being tested is the write, not whether the row is visible.
	want_status 200 POST "$TOKEN_A_PC" /api/grants \
		'{"from_project":"pa","to_project":"pc"}' || return 1
	want_tool mem_read "$TOKEN_A" "{\"id\": \"$id\"}" || return 1
	want_eq "what pa reads" "$(tv .item.project)" pc || return 1

	# The same owner, from pa, edits it.
	want_tool_fails mem_write "$TOKEN_A" \
		"$(jq -nc --arg i "$id" '{id: $i, title: "moved to pa behind your back"}')" \
		"lives in project pc" || return 1

	want_tool mem_read "$TOKEN_A_PC" "{\"id\": \"$id\"}" || return 1
	want_eq "the project it is still in" "$(tv .item.project)" pc || return 1
	want_eq "and its title" "$(tv .item.title)" "the one that stays in pc" || return 1
	want_eq "rows of it that ever moved to pa" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE id = '$id' AND project = 'pa'")" 0 || return 1

	# The token that does live there still edits it, so the refusal is about
	# where the item is and not about updates.
	want_tool mem_write "$TOKEN_A_PC" \
		"$(jq -nc --arg i "$id" '{id: $i, title: "edited from pc, where it lives"}')" || return 1
	want_eq "the project after an edit from home" "$(tv .item.project)" pc || return 1
	printf 'memory %s stays in pc when its owner edits it from pa\n' "$id"
}

# LOW/MED. /healthz reported the clock through Pack, which goes through Now,
# which advances it. So looking at the clock was a use of it: an open endpoint
# with no credential at all walked the logical counter up, one probe at a time,
# spending readings nothing was ever written under.
healthz_does_not_spend_the_clock() {
	recall
	local first i
	api GET "" /healthz || return 1
	first="$(jqv .hlc)"
	api GET "" /healthz || return 1
	want_eq "the reading a second probe reports" "$(jqv .hlc)" "$first" || return 1
	for i in $(seq 1 10); do
		api GET "" /healthz || return 1
	done
	want_eq "the reading after ten more probes" "$(jqv .hlc)" "$first" || return 1

	# And a write still moves it, or the endpoint would be reporting a clock
	# that has stopped rather than one that is not spent by being read.
	want_status 200 POST "$TOKEN_A" /api/events \
		'{"type":"chat","room":"general","body":"quillsprocket"}' || return 1
	api GET "" /healthz || return 1
	if [ "$(jqv .hlc)" -le "$first" ]; then
		printf 'the clock did not move for a write: %s -> %s\n' "$first" "$(jqv .hlc)" >&2
		return 1
	fi
	printf 'twelve probes left the clock at %s; one write moved it to %s\n' "$first" "$(jqv .hlc)"
}

# LOW. The share branch of POST /api/grants defaulted to_project only when the
# body left it empty and never looked at from_project at all, so a share was
# recorded along whatever edge its body claimed. Both ends of a share follow
# from the artifact and the owner handing it over, and neither is the caller's
# to say.
a_share_names_its_own_projects() {
	recall
	local id gid
	id="$(new_artifact "$TOKEN_A_PC" note "the one whose share is corrected")" || return 1
	api POST "$TOKEN_A_PC" /api/grants \
		"$(jq -nc --arg a "$id" --arg s "$USER_B" \
			'{artifact: $a, subject: $s, from_project: "pz-nowhere", to_project: "pz-elsewhere"}')" ||
		return 1
	want_eq "share status" "$API_STATUS" 200 || return 1
	gid="$(jqv .id)"
	want_eq "where the share lands" "$(jqv .to_project)" pc || return 1
	want_eq "and where it comes from" "$(jqv .from_project)" pc || return 1
	want_eq "the artifact it is about" "$(jqv .artifact)" "$id" || return 1

	# The row, not just the response: what a peer replicates is the table.
	want_eq "the stored edge" \
		"$(scalar "SELECT from_project || '->' || to_project FROM grants WHERE id = '$gid'")" \
		'pc->pc' || return 1
	want_eq "rows of it along the edge the body claimed" \
		"$(scalar "SELECT count(*) FROM grants WHERE id = '$gid' AND to_project = 'pz-elsewhere'")" \
		0 || return 1

	# And it still does what a share is for.
	want_status 200 GET "$TOKEN_B" "/api/artifact/$id" || return 1
	printf 'share %s was written pc->pc whatever its body said, and still shares %s\n' "$gid" "$id"
}

# LOW. The actor column has been the token's since the forgery in it was fixed,
# but meta rode in verbatim - and every reader that cares who is speaking reads
# meta. `{"actor_kind":"agent","actor_user":"somebody-else"}` on a hand-appended
# event is the same forgery through the second door: the row is correctly signed
# and reads, everywhere it is rendered, as somebody it is not.
an_event_meta_is_not_a_signature() {
	recall
	local id served
	want_status 200 POST "$TOKEN_B" /api/events \
		"$(jq -nc --arg u "$USER_A" '{type: "chat", room: "general", body: "not who you think",
		    meta: {actor_kind: "agent", actor_user: $u, topic: "kept"}}')" || return 1
	id="$(jqv .id)"
	want_eq "who the row is signed by" "$(jqv .actor)" "$USER_B" || return 1
	want_eq "the actor_kind it claimed" "$(jqv .meta.actor_kind)" null || return 1
	want_eq "the actor_user it claimed" "$(jqv .meta.actor_user)" null || return 1
	want_eq "what meta is still for" "$(jqv .meta.topic)" kept || return 1

	# The name a client picked for itself goes the same way. It is the key a
	# reader now draws first, so a hand-written one would be a forgery straight
	# onto the screen rather than one a reader has to resolve an id to see.
	want_status 200 POST "$TOKEN_B" /api/events \
		'{"type": "chat", "room": "general", "body": "and not who this says either",
		  "meta": {"actor_name": "the operator"}}' || return 1
	want_eq "the name it gave itself" "$(jqv .meta.actor_name)" null || return 1

	# And served back the same way, because what the console renders is the
	# stored row rather than the answer to the write.
	api GET "$TOKEN_B" '/api/events?room=general' || return 1
	served="$(printf '%s' "$API_BODY" |
		jq -r "[.events[] | select(.id == \"$id\")] | .[0]")"
	want_eq "the forged kind, as served" \
		"$(printf '%s' "$served" | jq -r '.meta.actor_kind')" null || return 1
	want_eq "the forged speaker, as served" \
		"$(printf '%s' "$served" | jq -r '.meta.actor_user')" null || return 1
	want_eq "what meta still carries, as served" \
		"$(printf '%s' "$served" | jq -r '.meta.topic')" kept || return 1
	want_eq "rows of it holding a speaker key" \
		"$(scalar "SELECT count(*) FROM events
		            WHERE id = '$id' AND (meta -> 'actor_user') IS NOT NULL")" \
		0 || return 1

	# The endpoints that do say who is speaking still do: an agent's message is
	# still marked as one, which is what makes the meta worth reading.
	api POST "$TOKEN_A_AGENT" /api/chat/general/say '{"body":"said by the agent itself"}' || return 1
	want_eq "say status" "$API_STATUS" 200 || return 1
	want_eq "and what it is marked as" "$(jqv .meta.actor_kind)" agent || return 1
	printf 'event %s is signed %s and carries no actor_* meta at all\n' "$id" "$USER_B"
}

say "a pulled grant does not open the puller's own project"
check "a project-wide grant into this project needs a local opener (HIGH 1)" \
	go test -count=1 -run TestPulledProjectGrantNeedsALocalOpener ./internal/store

say "being the forge is being the machine"
check "the mock forge's control routes are the operator's (HIGH 2)" \
	the_mock_forge_is_the_operators

say "a refused row is not a dropped row, the other way round"
check "a pull the node refused does not move the cursor past it (HIGH 3)" \
	a_refused_pull_does_not_move_the_cursor

say "an edit is not a move"
check "editing a memory item does not drag it into the token's project (MED/HIGH 4)" \
	a_memory_item_does_not_change_project_when_it_is_edited

say "a minted type is minted at both doors"
check "a pulled status, task or forge event is refused (MED 5)" \
	go test -count=1 -run TestPulledMintedEventIsRefused ./internal/store

say "one operation, one write, again"
check "a failure mid-delegate leaves no task move without its entry (MED 6)" \
	go test -count=1 -run TestUpdateTaskEventIsAllOrNothing ./internal/store

say "reading the clock is not using it"
check "probing /healthz does not advance the logical counter (LOW/MED 7)" \
	healthz_does_not_spend_the_clock

say "a share is about an artifact, not about an edge"
check "a share is stored with the projects it actually joins (LOW 8)" \
	a_share_names_its_own_projects

say "the clock learns what was committed"
check "a page that rolls back does not leave the clock ahead of it (LOW 9)" \
	go test -count=1 -run TestSyncApplyObservesTheClockAfterTheCommit ./internal/store

say "meta is not a second signature"
check "a hand-appended event carries no speaker it made up (LOW 10)" \
	an_event_meta_is_not_a_signature

# ------------------------------------------------- the sixth round of fixes
#
# The re-review of the fifth: the core held, and five behind it - an assignment
# that gated on a read and then minted a share, a push check that never fired
# for a row that is not here yet, a visibility no grant reaches through being
# handed over anyway, the split the first of those left across two nodes, and
# an UPDATE that matched nothing and said it had.

# HIGH 1 (and 4). handleAssign asked only that the caller could read the
# artifact, and then wrote the share itself with the caller in granted_by. The
# share clause matches on artifact and subject alone, so any reader of an
# artifact could hand a read on it to anybody - re-delegation across a tenant
# boundary, from a capability that was only ever a read.
#
# It was not even replicable: checkGrant refuses a share whose grantor is not
# the artifact's owner, so the task and its opening message pushed while the
# share behind them did not, and the far side held a task whose artifact it
# gets a 404 on - with the refused grant holding the push cursor where it was,
# so nothing moved after it either.
#
# A share is the owner's to give, which is the bar POST /api/grants keeps.
only_the_owner_hands_the_work_on() {
	recall
	local id
	id="$(new_artifact "$TOKEN_A_PC" bug "the flywheel that gets passed on")" || return 1

	# A share puts it in B's reach, which is all the handler used to ask for.
	want_status 200 POST "$TOKEN_A_PC" /api/grants \
		"$(jq -nc --arg a "$id" --arg s "$USER_B" '{artifact: $a, subject: $s}')" || return 1
	want_status 200 GET "$TOKEN_B" "/api/artifact/$id" || return 1

	# B reads it and does not own it, so B does not hand it on.
	want_status 403 POST "$TOKEN_B" /api/assign \
		"$(jq -nc --arg a "$id" --arg u "$USER_OP" '{artifact: $a, to_user: $u}')" || return 1
	want_eq "shares of it to the third party" \
		"$(scalar "SELECT count(*) FROM grants
		            WHERE artifact = '$id' AND subject = '$USER_OP'")" 0 || return 1
	want_eq "tasks about it" \
		"$(scalar "SELECT count(*) FROM tasks WHERE artifact = '$id'")" 0 || return 1

	# The same through a project-wide grant rather than a share: B reads
	# anything in pa because A opened pa up to pb, and B still owns none of it.
	local inpa
	inpa="$(new_artifact "$TOKEN_A" bug "the pa one a reader tries to pass on")" || return 1
	want_status 200 GET "$TOKEN_B" "/api/artifact/$inpa" || return 1
	want_status 403 POST "$TOKEN_B" /api/assign \
		"$(jq -nc --arg a "$inpa" --arg u "$USER_OP" '{artifact: $a, to_user: $u}')" || return 1
	want_eq "tasks about the pa one" \
		"$(scalar "SELECT count(*) FROM tasks WHERE artifact = '$inpa'")" 0 || return 1

	# And the owner still hands their own work over, with the share signed by
	# the owner - which is the only kind a peer will take.
	assign_as "$TOKEN_A_PC" "$id" "$USER_OP" "yours" || return 1
	want_eq "the owner's assignment" "$API_STATUS" 200 || return 1
	want_eq "who signed the share" "$(jqv .grant.granted_by)" "$USER_A" || return 1
	want_eq "who it is for" "$(jqv .to_user)" "$USER_OP" || return 1

	# And the stored share is signed by the artifact's owner, which is the one
	# shape checkGrant will take on a push - a reader-minted one never was.
	want_eq "the share the handoff wrote" \
		"$(scalar "SELECT count(*) FROM grants g JOIN artifacts a ON a.id = g.artifact
		            WHERE g.artifact = '$id' AND g.subject = '$USER_OP'
		              AND g.granted_by = a.owner_user")" 1 || return 1
	printf 'a reader of %s could not pass it on; its owner could\n' "$id"
}

# MED 3. A 'project-only' artifact is the project it is in and nothing else:
# the read filter takes that branch and the grant and share tests below it are
# never reached. So the share an assignment writes for one can never take
# effect, and the task beside it points at an artifact the assignee gets a 404
# on - the riddle the handler exists to refuse, arrived at the other way round.
a_project_only_artifact_is_not_assignable() {
	recall
	local id
	api POST "$TOKEN_A_PC" /api/artifacts '{
		"type": "note", "title": "the narrow one", "body": "snickerbolt",
		"visibility": "project-only"
	}' || return 1
	want_eq "create status" "$API_STATUS" 200 || return 1
	id="$(jqv .id)"
	want_eq "the visibility it kept" "$(jqv .visibility)" project-only || return 1

	want_status 400 POST "$TOKEN_A_PC" /api/assign \
		"$(jq -nc --arg a "$id" --arg u "$USER_B" '{artifact: $a, to_user: $u}')" || return 1
	want_eq "shares of it" \
		"$(scalar "SELECT count(*) FROM grants WHERE artifact = '$id'")" 0 || return 1
	want_eq "tasks about it" \
		"$(scalar "SELECT count(*) FROM tasks WHERE artifact = '$id'")" 0 || return 1

	# Which is not a guess about the read filter: a share of it, written the
	# ordinary way, reaches nothing at all.
	want_status 200 POST "$TOKEN_A_PC" /api/grants \
		"$(jq -nc --arg a "$id" --arg s "$USER_B" '{artifact: $a, subject: $s}')" || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$id" || return 1

	# Widen it and the same handoff goes through, so the refusal is about the
	# visibility and not about the artifact.
	api POST "$TOKEN_A_PC" /api/artifacts \
		"$(jq -nc --arg i "$id" '{id: $i, type: "note", visibility: "shared"}')" || return 1
	want_eq "the update status" "$API_STATUS" 200 || return 1
	want_eq "the widened visibility" "$(jqv .visibility)" shared || return 1
	assign_as "$TOKEN_A_PC" "$id" "$USER_B" "now you can open it" || return 1
	want_eq "the handoff once it can be shared" "$API_STATUS" 200 || return 1
	want_status 200 GET "$TOKEN_B" "/api/artifact/$id" || return 1
	printf '%s could not be handed over while no grant reached it\n' "$id"
}

# HIGH 4. The split the reader-made assignment left across two nodes, and what
# an owner-made one does instead: all three rows travel, nothing is refused,
# and the cursor moves on.
an_owner_assignment_replicates_whole() {
	recall5
	local peer art task thread before after refused
	peer="http://127.0.0.1:$N5_PORT_A"

	# The reader first, on node B: A's artifact, shared with B, and B trying to
	# pass it on. That is the assignment whose three rows used to split.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"bug","title":"the one a reader tried to pass on","body":"thrummelcask"}' || return 1
	art="$(jqv .id)"
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_A" /api/grants \
		"$(jq -nc --arg a "$art" --arg s "$N5_USER_B" '{artifact: $a, subject: $s}')" || return 1
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/artifact/$art" || return 1
	want_napi 403 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/assign \
		"$(jq -nc --arg a "$art" --arg u "$N5_USER_OP" '{artifact: $a, to_user: $u}')" || return 1
	want_eq "tasks the reader wrote" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM tasks WHERE artifact = '$art'")" 0 || return 1

	# Settle, so what the push below carries is this check's own three rows.
	sync_round || return 1
	sync_round || return 1
	before="$(scalar5 "$N5_DSN_B" "SELECT pushed_cursor FROM peers WHERE peer = '$peer'")" || return 1

	# And now the owner's own handoff, made as the principal replication
	# authenticates as - which is what makes the three rows one thing on the
	# far side as well as on this one.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"bug","title":"the owner-made handoff","body":"quirkleflange"}' || return 1
	art="$(jqv .id)"
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/assign \
		"$(jq -nc --arg a "$art" --arg u "$N5_USER_A" '{artifact: $a, to_user: $u, note: "yours"}')" ||
		return 1
	task="$(jqv .id)"
	thread="$(jqv .thread)"

	# One push, nothing pulled: what node A makes of the three rows on their own.
	sync5_flags "$N5_DSN_B" nodeB "$N5_PORT_A" "$N5_TOKEN_B" --pull=false || return 1
	refused="$(printf '%s' "$SYNC_REPORT" | jq '[.refused[]] | add')"
	want_eq "rows the push refused" "$refused" 0 || return 1
	want_eq "the share on A" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM grants
		            WHERE artifact = '$art' AND subject = '$N5_USER_A'")" 1 || return 1
	want_eq "the task on A" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM tasks WHERE id = '$task'")" 1 || return 1
	want_eq "the opening message on A" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM events WHERE thread = '$thread'")" 1 || return 1

	# Which is the point of the share: the side that was handed the work can
	# open it.
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_A" "/api/artifact/$art" || return 1

	after="$(scalar5 "$N5_DSN_B" "SELECT pushed_cursor FROM peers WHERE peer = '$peer'")" || return 1
	if [ "$after" -le "$before" ]; then
		printf 'the push cursor did not move: %s -> %s\n' "$before" "$after" >&2
		return 1
	fi
	sync_round || return 1
	printf 'task %s, its share and its thread all pushed; the cursor moved %s -> %s\n' \
		"$task" "$before" "$after"
}

say "a share is the owner's to give, assignment included"
check "a reader of an artifact cannot assign it onward (HIGH 1)" \
	only_the_owner_hands_the_work_on

say "a push writes the pusher's own rows"
check "a new artifact pushed in somebody else's name is refused (HIGH 2)" \
	go test -count=1 -run TestPushedNewArtifactIsThePushersOwn ./internal/store

say "a handoff nobody can open is not a handoff"
check "a project-only artifact cannot be assigned (MED 3)" \
	a_project_only_artifact_is_not_assignable
check "nor replicated in as a task (MED 3)" \
	go test -count=1 -run TestPushedTaskAboutAProjectOnlyArtifactIsRefused ./internal/store

say "three rows, one handoff, both nodes"
check "an owner's assignment pushes whole and moves the cursor (HIGH 4)" \
	an_owner_assignment_replicates_whole

say "an update that matched nothing is not a write"
check "moving a task that is not here appends no entry for it (LOW 5)" \
	go test -count=1 -run TestUpdateTaskEventNeedsTheTaskToBeThere ./internal/store

# --------------------------------------------------------- security fixes, 7
#
# Six more, each with the check that fails without it.

# HIGH 1. forgePushReplies forwarded every chat event in a filed artifact's
# thread to the issue, gated only on the sync's caller being the owner. Who may
# write in that thread is a much wider set - any project mate, any party to the
# task - so a mate's message went out over the node's forge credential, under
# the node's name, on a public issue. Reading an artifact is not permission to
# publish it, and neither is answering in its thread.
only_the_owners_replies_reach_the_forge() {
	recall
	local id thread filed bseq pushed
	id="$(new_artifact "$TOKEN_A_PC" bug "the alternator whines at idle")" || return 1

	# Hand it to B first, so the issue's conversation is the handoff thread and
	# B is a party to it: somebody who may write here and may not publish.
	assign_as "$TOKEN_A_PC" "$id" "$USER_B" "have a look at this one" || return 1
	want_eq "the handoff" "$API_STATUS" 200 || return 1
	thread="$(jqv .thread)"
	forge_file "$TOKEN_A_PC" "$id" o/r || return 1
	want_eq "filing status" "$API_STATUS" 200 || return 1
	filed="$(jqv .external.number)"
	want_eq "the issue's thread is the handoff's" "$(jqv .external.thread)" "$thread" || return 1

	# B answers, which is an ordinary message in a conversation B is part of.
	say_in_thread "$TOKEN_B" "quibberflax - I will take it tomorrow" "$thread" || return 1
	want_eq "B could say it" "$API_STATUS" 200 || return 1
	bseq="$(jqv .seq_hlc)"

	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "sync status" "$API_STATUS" 200 || return 1
	want_eq "what the sync sent out" "$(jqv .pushed)" 0 || return 1
	mock_issue "$TOKEN_OP" o/r "$filed" || return 1
	want_eq "B's words on the public issue" \
		"$(jqv '[.comments[] | select(.body | test("quibberflax"))] | length')" 0 || return 1

	# It is still a message here: held back is not deleted.
	api GET "$TOKEN_A_PC" "/api/chat/forge?thread=$thread" || return 1
	want_eq "B's message in the thread" \
		"$(jqv '[.events[] | select(.body | test("quibberflax"))] | length')" 1 || return 1

	# The owner's own reply goes out, so the refusal is about who said it.
	say_in_thread "$TOKEN_A_PC" "thanks - it also snickerbolts on the overrun" "$thread" || return 1
	want_eq "the owner could say it" "$API_STATUS" 200 || return 1
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "what the second sync sent out" "$(jqv .pushed)" 1 || return 1
	pushed="$(jqv .external.pushed)"
	if [ "$pushed" -le "$bseq" ]; then
		printf 'the push cursor stopped behind the message it skipped: %s <= %s\n' \
			"$pushed" "$bseq" >&2
		return 1
	fi
	mock_issue "$TOKEN_OP" o/r "$filed" || return 1
	want_eq "the owner's reply reached the issue" \
		"$(jqv '[.comments[] | select(.body | test("snickerbolts on the overrun"))] | length')" \
		1 || return 1
	want_eq "and B's still did not" \
		"$(jqv '[.comments[] | select(.body | test("quibberflax"))] | length')" 0 || return 1

	# And the skipped one is not queued for the next sync either.
	forge_sync "$TOKEN_A_PC" "$id" || return 1
	want_eq "a third sync sends nothing" "$(jqv .pushed)" 0 || return 1
	printf 'o/r#%s carries the owner alone; the cursor went past %s to %s\n' "$filed" "$bseq" "$pushed"
}

# MED 2. An artifact with no project is its owner's and nobody else's, whatever
# the visibility column says. An update that left the project field out was
# read as "the principal's home project", so a bare {id, type} from a token that
# has one handed the row to that project - on a request that said nothing about
# scope at all.
an_update_does_not_adopt_the_callers_project() {
	recall
	local id legacy
	api POST "$TOKEN_A" /api/artifacts '{
		"type": "note", "title": "mine alone", "body": "thrundlewick",
		"visibility": "shared", "project": null
	}' || return 1
	want_eq "create status" "$API_STATUS" 200 || return 1
	id="$(jqv .id)"
	want_eq "the project it landed in" "$(jqv .project)" null || return 1
	# The floor is a property of the row now, not only of the read filter.
	want_eq "the visibility a projectless row keeps" "$(jqv .visibility)" personal || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$id" || return 1

	# The bare update. It says nothing about scope, so it changes nothing.
	api POST "$TOKEN_A" /api/artifacts "$(jq -nc --arg i "$id" '{id: $i, type: "note"}')" || return 1
	want_eq "the update status" "$API_STATUS" 200 || return 1
	want_eq "the project after a bare update" "$(jqv .project)" null || return 1
	want_eq "the visibility after it" "$(jqv .visibility)" personal || return 1
	want_eq "the title it kept" "$(jqv .title)" "mine alone" || return 1
	want_eq "rows of it that ever landed in a project" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE id = '$id' AND project IS NOT NULL")" \
		0 || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$id" || return 1

	# A row written the way replication can still deliver one: NULL project with
	# a non-personal visibility on it. Asking for a project is refused rather
	# than taken, and a bare update leaves it where it is.
	legacy="$(scalar "SELECT 'legacy-' || md5(random()::text)")" || return 1
	psql_do "INSERT INTO artifacts (id, type, project, owner_user, title, body, visibility,
	                                hlc, node)
	         VALUES ('$legacy', 'note', NULL, '$USER_A', 'the one that arrived', 'blimberwatt',
	                 'shared', 1, 'peer')" || return 1
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg i "$legacy" '{id: $i, type: "note", project: "pa"}')" || return 1
	want_eq "asking for a project outright" "$API_STATUS" 400 || return 1
	want_eq "the project it is still in" \
		"$(scalar "SELECT coalesce(project, 'none') FROM artifacts WHERE id = '$legacy'")" \
		none || return 1

	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg i "$legacy" '{id: $i, type: "note", title: "edited, not moved"}')" || return 1
	want_eq "the bare update status" "$API_STATUS" 200 || return 1
	want_eq "the project it kept" "$(jqv .project)" null || return 1
	want_eq "and the visibility it was corrected to" "$(jqv .visibility)" personal || return 1
	want_eq "the edit that was asked for" "$(jqv .title)" "edited, not moved" || return 1
	printf '%s and %s stayed out of pa through an update\n' "$id" "$legacy"
}

# LOW 3. A cross-project share let the subject read the artifact and its status
# trail - /api/artifact/{id}/history is gated on the artifact read - and not one
# event about it anywhere else, because the event filter had no per-artifact
# share clause. Two read surfaces, two answers about the same rows.
a_share_reaches_the_events_about_it() {
	recall
	local id thread other othread
	id="$(new_artifact "$TOKEN_A_PC" bug "the clutch judders in the wet")" || return 1
	move_status "$TOKEN_A_PC" "$id" triaged || return 1
	want_eq "the status move" "$API_STATUS" 200 || return 1
	thread="$(jqv .event.thread)"
	api POST "$TOKEN_A_PC" /api/events \
		"$(jq -nc --arg a "$id" --arg t "$thread" \
			'{type: "chat", room: "forge", artifact: $a, thread: $t,
			  body: "wobblethwack - it is worse above 60"}')" || return 1
	want_eq "the message about it" "$API_STATUS" 200 || return 1

	# B is in pb, which holds no grant into pc: the share of this one artifact
	# is the only thing that can be doing any work here.
	want_status 200 POST "$TOKEN_A_PC" /api/grants \
		"$(jq -nc --arg a "$id" --arg s "$USER_B" '{artifact: $a, subject: $s}')" || return 1
	want_status 200 GET "$TOKEN_B" "/api/artifact/$id" || return 1
	api GET "$TOKEN_B" "/api/artifact/$id/history" || return 1
	want_eq "the trail the artifact surface shows" "$(jqv '.events | length')" 1 || return 1

	# The same events, read as events.
	api GET "$TOKEN_B" "/api/events?thread=$thread" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the status move B can see" \
		"$(jqv '[.events[] | select(.type == "status")] | length')" 1 || return 1
	want_eq "and what was said about it" \
		"$(jqv '[.events[] | select(.body | test("wobblethwack"))] | length')" 1 || return 1

	# An artifact in the same project that was never shared is still nothing to
	# B, which is what says the share is doing this and not the widening.
	other="$(new_artifact "$TOKEN_A_PC" bug "the one nobody shared")" || return 1
	move_status "$TOKEN_A_PC" "$other" triaged || return 1
	othread="$(jqv .event.thread)"
	want_status 404 GET "$TOKEN_B" "/api/artifact/$other" || return 1
	api GET "$TOKEN_B" "/api/events?thread=$othread" || return 1
	want_eq "events about the unshared one" "$(jqv '.events | length')" 0 || return 1
	printf 'the share of %s reaches its events; %s stays out of reach\n' "$id" "$other"
}

# LOW 6. The console parsed every error body as JSON before it looked at the
# status, so a proxy's HTML 502 became "Unexpected token '<'" - a SyntaxError
# with no status on it, thrown past every caller that handles ApiError.
console_reports_the_status_not_the_parse_error() {
	cd "$ROOT/web" || return 1
	node scripts/api-error-check.mjs
}

say "answering in a thread is not publishing"
check "a project mate's reply is not sent to the forge (HIGH 1)" \
	only_the_owners_replies_reach_the_forge

say "an update that says nothing about scope changes nothing about scope"
check "a projectless artifact is not adopted into the caller's project (MED 2)" \
	an_update_does_not_adopt_the_callers_project

say "one share, one answer"
check "a share reaches the events about the artifact (LOW 3)" \
	a_share_reaches_the_events_about_it

say "a thread is one read"
check "ThreadEvents is a single query, in (seq_hlc, id) order (LOW 4)" \
	go test -count=1 -run TestThreadEventsIsOneQuery ./internal/store

say "a peer that answers with too much is told so"
check "an oversized pull answer is a named error, not a parse error (LOW 5)" \
	go test -count=1 -run 'TestPeerAnswerRefusesAnOversizedPage|TestPullFromAPeerThatAnswersTooMuchSaysSo' .

say "a body that is not json still has a status"
check "a non-json error body becomes an ApiError with the status (LOW 6)" \
	console_reports_the_status_not_the_parse_error

# -------------------------------------------------- phase 6.5: signed rows
#
# The one HIGH the seventh review left: a hostile PULL peer could rewrite the
# content of any artifact, grant, task or event the pulling principal may read.
# Every check in the rounds above asks what the peer handing a row over is
# allowed to write, and a peer serving a page is allowed to hand over other
# people's rows - that is what federation is. Nothing asked whether the node
# named on the row wrote it, because nothing on an unsigned row answers that.
#
# The two below are the end-to-end halves of the fix, driven through the real
# nodes: a peer rewriting somebody else's row, and a third node's rows reaching
# a node that has never spoken to it. The property-by-property checks are Go
# tests against the merge itself, registered underneath them.

# psql5_do - a statement against one of the two federated databases. It is how
# a check is something a node cannot do to itself: a hostile peer's database is
# a database somebody else writes.

# forged_sig DSN NODE ROW - the signature the node behind DSN would put on a row
# that names somebody else as its author, in base64. The row is one artifact
# object; what comes back is the best a peer holding its own key and somebody
# else's row can do, which is exactly the forgery.
forged_sig() {
	local dsn=$1 node=$2 row=$3
	jq -nc --argjson a "$row" '{artifacts: [$a], events: [], tasks: [], grants: [], hwm: 0}' |
		sign5 "$dsn" "$node" | jq -r '.artifacts[0].sig'
}

# HIGH 1 (phase 6.5). A pulled row used to be believed because of who handed it
# over. Node A owns an artifact; node B holds a copy of it because it was
# replicated there; node B rewrites it - title, body, status, a reading that
# wins - and puts node A's name back on it. On the code as it was, A pulled its
# own artifact back with somebody else's words in it and kept them.
a_peer_cannot_rewrite_the_row_it_was_given() {
	recall5
	local id hlc was_title row forged
	# A's own row, written on A and replicated to B by an ordinary sync.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"bug","title":"the alternator whines","body":"only when cold","status":"open"}' ||
		return 1
	id="$(jqv .id)"
	sync_round || return 1
	want_eq "the row reached nodeB" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$id' AND node = 'nodeA'")" \
		1 || return 1

	was_title="$(scalar5 "$N5_DSN_A" "SELECT title FROM artifacts WHERE id = '$id'")" || return 1
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1

	# What a hostile peer writes into its own database: A's id, A's owner, A's
	# project, everything else its own, and its own signature over the result.
	# It cannot produce A's signature over these bytes, and this is the nearest
	# thing it can produce.
	row="$(jq -nc --arg i "$id" --arg o "$N5_USER_B" --argjson h "$hlc" \
		'{id: $i, type: "bug", project: "pb", owner_user: $o, title: "the alternator is fine",
		  body: "closed as invalid", status: "done", visibility: "project",
		  hlc: $h, node: "nodeA", tombstone: false, reported: false}')"
	forged="$(forged_sig "$N5_DSN_B" nodeB "$row")" || return 1
	psql5_do "$N5_DSN_B" "UPDATE artifacts
	    SET title = 'the alternator is fine', body = 'closed as invalid', status = 'done',
	        owner_user = '$N5_USER_B', project = 'pb', hlc = $hlc, node = 'nodeA',
	        sig = decode('$forged', 'base64')
	  WHERE id = '$id'" || return 1

	# The peer really does serve it, so what follows is the merge rather than
	# the peer being unable to say it.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/sync/pull?since=$((hlc - 1))" || return 1
	want_eq "the rewritten row the peer served" \
		"$(printf '%s' "$API_BODY" | jq '[.artifacts[] | select(.id == "'"$id"'")] | length')" 1 ||
		return 1

	sync5_flags "$N5_DSN_A" nodeA "$N5_PORT_B" "$N5_TOKEN_B" --push=false || return 1
	local refused
	refused="$(printf '%s' "$SYNC_REPORT" | jq '[.refused[]] | add')"
	if [ "$refused" -lt 1 ]; then
		printf 'nodeA refused %s rows and took the rewrite: %s\n' "$refused" "$SYNC_REPORT" >&2
		return 1
	fi
	case "$SYNC_REPORT" in
	*"does not verify"*) ;;
	*)
		printf 'the refusal is not about the signature: %s\n' "$SYNC_REPORT" >&2
		return 1
		;;
	esac

	# And A's own copy is exactly as A wrote it.
	want_eq "the title on nodeA" \
		"$(scalar5 "$N5_DSN_A" "SELECT title FROM artifacts WHERE id = '$id'")" "$was_title" ||
		return 1
	want_eq "the body on nodeA" \
		"$(scalar5 "$N5_DSN_A" "SELECT body FROM artifacts WHERE id = '$id'")" "only when cold" ||
		return 1
	want_eq "the status on nodeA" \
		"$(scalar5 "$N5_DSN_A" "SELECT coalesce(status, '') FROM artifacts WHERE id = '$id'")" \
		open || return 1

	# The rewrite goes out of the peer, so nothing after this runs against a
	# node holding a row no other node will ever merge again.
	psql5_do "$N5_DSN_B" "DELETE FROM artifacts WHERE id = '$id'" || return 1
	printf 'the rewrite of %s was served, refused and never landed: %s\n' "$id" \
		"$(printf '%s' "$SYNC_REPORT" | jq -r '.reasons[0]')"
}

# HIGH 1, the other way round: the relay that works. Node C is a node with a
# key and no server - it exists as a keypair and a name, which is all a node is
# to a row - and its rows reach nodeA through nodeB, which holds neither key.
#
# The identity travels with them, self-signed, so nodeB cannot alter it in
# transit; nodeA has never heard of nodeC and takes it on first use. And when
# nodeB does alter one of C's rows, C's signature breaks and nodeA refuses it -
# which is the whole point of verifying the author rather than the sender.
a_third_nodes_rows_relay_through_the_peer() {
	recall5
	local seed key id hlc delta
	seed="$(seed_of nodeC)" || return 1
	key="$(key_of nodeC "$seed")" || return 1
	id="relayed-$$-$(date +%s)"

	# Neither node has ever heard of nodeC. Its identity travels with its rows,
	# self-signed, which is the only thing that makes a relay possible at all.
	want_eq "what nodeB knows of nodeC to begin with" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM node_identity WHERE node_id = 'nodeC'")" 0 ||
		return 1
	want_eq "what nodeA knows of nodeC to begin with" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM node_identity WHERE node_id = 'nodeC'")" 0 ||
		return 1

	# C writes a row and signs it, and B takes it: the signature is C's, the
	# owner is the principal pushing it, and B holds C's key.
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	delta="$(jq -nc --arg i "$id" --arg o "$N5_USER_B" --argjson h "$hlc" '
		{events: [], tasks: [], grants: [], hwm: 0, artifacts: [
		  {id: $i, type: "note", project: "pb", owner_user: $o, title: "written on nodeC",
		   body: "flimberwhack", visibility: "project", hlc: $h, node: "nodeC",
		   tombstone: false, reported: false}]}' | sign_seed "$seed" nodeC)" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "nodeB took C's row" "$(jqv '.applied.artifacts')" 1 || return 1
	want_eq "and C's key, on first use" \
		"$(scalar5 "$N5_DSN_B" \
			"SELECT count(*) FROM node_identity WHERE node_id = 'nodeC' AND NOT pinned")" 1 ||
		return 1

	# nodeA pulls from nodeB and gets a row from a node it has never met.
	sync5_flags "$N5_DSN_A" nodeA "$N5_PORT_B" "$N5_TOKEN_B" --push=false || return 1
	want_eq "rows nodeA refused" \
		"$(printf '%s' "$SYNC_REPORT" | jq '[.refused[]] | add')" 0 || return 1
	want_eq "C's row on nodeA" \
		"$(scalar5 "$N5_DSN_A" "SELECT count(*) FROM artifacts WHERE id = '$id' AND node = 'nodeC'")" \
		1 || return 1
	want_eq "and the key it verified it with, taken on first use" \
		"$(scalar5 "$N5_DSN_A" \
			"SELECT count(*) FROM node_identity WHERE node_id = 'nodeC' AND NOT pinned")" 1 ||
		return 1
	want_eq "which is the key nodeC really signs with" \
		"$(scalar5 "$N5_DSN_A" \
			"SELECT encode(public_key, 'hex') FROM node_identity WHERE node_id = 'nodeC'")" \
		"$key" || return 1

	# Now the relay tampers: same row, nodeB's own words, a reading that wins,
	# and nodeC's signature still on it because nodeB cannot make another.
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	psql5_do "$N5_DSN_B" "UPDATE artifacts
	    SET title = 'edited by the relay', body = 'wobblethwack', hlc = $hlc
	  WHERE id = '$id'" || return 1
	sync5_flags "$N5_DSN_A" nodeA "$N5_PORT_B" "$N5_TOKEN_B" --push=false || return 1
	if [ "$(printf '%s' "$SYNC_REPORT" | jq '[.refused[]] | add')" -lt 1 ]; then
		printf 'nodeA took a row the relay had edited: %s\n' "$SYNC_REPORT" >&2
		return 1
	fi
	want_eq "the title nodeA still has" \
		"$(scalar5 "$N5_DSN_A" "SELECT title FROM artifacts WHERE id = '$id'")" \
		"written on nodeC" || return 1

	# And out of the peer again, so the cursor is not held against the checks
	# that follow.
	psql5_do "$N5_DSN_B" "DELETE FROM artifacts WHERE id = '$id'" || return 1
	printf 'nodeC relayed through nodeB to nodeA; the relay could not edit %s\n' "$id"
}

say "a peer that hands a row over is not the node that wrote it"
check "a hostile peer's rewrite of somebody else's row is refused end to end (HIGH 1)" \
	a_peer_cannot_rewrite_the_row_it_was_given
check "a third node's rows relay through a peer, and the relay cannot edit them (HIGH 1)" \
	a_third_nodes_rows_relay_through_the_peer

say "the canonical encoding is what is signed"
check "one row is one byte string, and every field is in it" \
	go test -count=1 ./internal/sign

say "authorship, checked at the merge"
check "a rewritten, unsigned or replayed row is refused" \
	go test -count=1 -run TestAHostilePeerCannotRewriteAnothersRow ./internal/store
check "one flipped byte of any signed field is refused" \
	go test -count=1 -run TestOneFlippedByteIsRefused ./internal/store
check "a validly signed row is still refused when it is not the peer's to write" \
	go test -count=1 -run TestAuthenticityAndAuthorisationAreTwoLayers ./internal/store
check "every local write of every replicated table is signed" \
	go test -count=1 -run TestALocalWriteIsSignedForEveryTable ./internal/store
check "a signature survives the database that stores the row" \
	go test -count=1 -run TestASignatureSurvivesTheDatabase ./internal/store

say "keys: pinned, taken on first use, never rotated over the wire"
check "this node mints one key and signs with it" \
	go test -count=1 -run TestThisNodeSignsAndKeepsItsKey ./internal/store
check "an identity arrives with the rows it verifies" \
	go test -count=1 -run TestAnIdentityArrivesWithTheRowsItVerifies ./internal/store
check "a second key for a node already known is refused, at the pin" \
	go test -count=1 -run TestPinnedKeyDoesNotRotate ./internal/store
check "and over the wire, with the rows that came with it" \
	go test -count=1 -run TestAKeyDoesNotRotateOverTheWire ./internal/store
check "FLOWY_REQUIRE_PINNED_PEERS refuses a node the operator did not pin" \
	go test -count=1 -run TestRequirePinnedPeersRefusesATrustedOnFirstUseNode ./internal/store
check "a pull hands over public keys and no private ones" \
	go test -count=1 -run TestSyncPullHandsOverTheKeysAndNoPrivateOnes ./internal/store

say "signing says who wrote a row, not what they may write"
check "a pulled share of somebody else's artifact is refused (HIGH 1)" \
	go test -count=1 -run TestPulledArtifactShareIsStillTheOwnersToGive ./internal/store
check "a pulled new task is the owner's, into a thread nobody has spoken in (MED 2)" \
	go test -count=1 -run TestPulledNewTaskIsTheOwnersHandoffIntoAFreshThread ./internal/store

say "a read is not a write, and a deleted row is not there"
check "a status move and a forge link both refuse a deleted artifact (MED 3)" \
	go test -count=1 -run TestADeletedArtifactIsNotMovedOrFiled ./internal/store

say "a clock with nothing left says so"
check "a saturated clock refuses a reading rather than repeating one (LOW 4)" \
	go test -count=1 -run TestSaturatedClockRefusesRatherThanRepeat ./internal/hlc

# ------------------------------------------- the ninth round of security fixes
#
# The re-review found nothing left in the core - the permission filter, the
# clock, the signatures and both sync doors were certified as they stand - and
# four hardening defects around it, each of them a place where something was
# accepted, stored or started without being held to the rule the rest of the
# node keeps. Plus one packaging defect, which is not a security question at all
# but breaks the build for whoever receives this tree.

# ROBUSTNESS. web/dist holds a tracked placeholder so that `//go:embed
# all:web/dist` in console.go matches something on a tree where the console has
# never been built - a pattern that matches nothing is a compile error, not an
# empty directory.
#
# The check is one sentence: the commit carries one file under web/dist, it is
# not empty, and the committed tree builds on its own - no node_modules, no vite
# output, no network. Non-empty is asserted because the placeholder's own bytes
# have to match what the postbuild step writes back, and because a file that
# says what it is for is worth more than one that says nothing; it is not, as
# the last round of this claimed, what makes the file survive being copied out
# of the sandbox. That was an overclaim. The copy was dropping the file for
# reasons of its own and the fix for it lives in the harness that does the
# copying, not here.
the_committed_tree_builds_with_no_console_build() {
	local tree="$WORK/committed" keep
	rm -rf "$tree"
	mkdir -p "$tree"
	git -C "$ROOT" archive HEAD | tar -x -C "$tree" || return 1

	keep="$tree/web/dist/.gitkeep"
	if [ ! -f "$keep" ]; then
		printf 'nothing is committed under web/dist; go:embed all:web/dist matches nothing\n' >&2
		return 1
	fi
	if [ ! -s "$keep" ]; then
		printf 'web/dist/.gitkeep is committed empty; it is meant to carry the line\n' >&2
		printf 'that says what it is for, and the postbuild step writes that back\n' >&2
		return 1
	fi
	want_eq "files committed under web/dist" "$(git -C "$ROOT" ls-files web/dist | wc -l)" 1 ||
		return 1

	# And what the build actually does with it: vite empties dist, so the
	# placeholder is written back by the postbuild step, and it has to be written
	# back as the bytes that are committed or every build leaves the tree dirty.
	if ! cmp -s "$keep" "$ROOT/web/dist/.gitkeep"; then
		printf 'the placeholder after a build is not the one that is committed:\n' >&2
		diff "$keep" "$ROOT/web/dist/.gitkeep" >&2
		return 1
	fi

	(cd "$tree" && go build ./...) || return 1
	printf 'the committed tree builds with a %s byte placeholder and no console build\n' \
		"$(wc -c <"$keep")"
}

# MED. A grant's cap was taken verbatim from the body: stored, signed and
# replicated, and read by nothing. Every read rule here treats a live grant as a
# read and never looks at the column, so `cap: "write"` was accepted and
# travelled to every peer describing a reach nobody granted - waiting for the
# first reader that does look at it. The set is what is implemented, and both
# doors ask.
a_grant_carries_a_cap_this_node_implements() {
	recall
	local gid

	want_status 400 POST "$TOKEN_A" /api/grants \
		'{"from_project": "pb", "to_project": "pa", "cap": "write"}' || return 1
	want_status 400 POST "$TOKEN_A" /api/grants \
		"$(jq -nc '{from_project: "pb", to_project: "pa", cap: ("x" * 4096)}')" || return 1
	want_eq "grants stored with a cap nothing implements" \
		"$(scalar "SELECT count(*) FROM grants WHERE cap <> 'read'")" 0 || return 1

	# read, and left out, are the same grant and both go through.
	want_status 200 POST "$TOKEN_A" /api/grants \
		'{"from_project": "pb", "to_project": "pa", "cap": "read"}' || return 1
	want_eq "the cap it stored" "$(jqv .cap)" read || return 1
	want_status 200 POST "$TOKEN_A" /api/grants \
		'{"from_project": "pb", "to_project": "pa"}' || return 1
	gid="$(jqv .id)"
	want_eq "the cap a body that left it out gets" "$(jqv .cap)" read || return 1
	want_eq "the cap in the row" \
		"$(scalar "SELECT cap FROM grants WHERE id = '$gid'")" read || return 1
	printf 'cap is read or nothing at the API door; the row says read\n'
}

# MED, the other door. A peer signs its own rows, so a cap it invents is
# authentic - authenticity says who wrote a row, never what it may say. The two
# grants below differ in one field, which is what makes the refusal about the
# cap and not about anything else on the row.
a_pushed_grant_with_a_cap_nobody_implements_is_refused() {
	recall5
	local good bad hlc delta
	good="cap-ok-$$-$(date +%s)"
	bad="cap-bad-$$-$(date +%s)"
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1

	# Both open pb up to a project that does not exist, so nothing here is
	# opened up by the one that lands.
	delta="$(jq -nc --arg g "$good" --arg b "$bad" --arg o "$N5_USER_B" \
		--argjson h1 "$hlc" --argjson h2 "$((hlc + 65536))" '
		{artifacts: [], events: [], tasks: [], hwm: 0, grants: [
		  {id: $g, from_project: "pz-nowhere", to_project: "pb", subject: "", artifact: "",
		   cap: "read", granted_by: $o, hlc: $h1, node: "nodeA", tombstone: false},
		  {id: $b, from_project: "pz-nowhere", to_project: "pb", subject: "", artifact: "",
		   cap: "write", granted_by: $o, hlc: $h2, node: "nodeA",
		   tombstone: false}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "grants refused" "$(jqv '.refused.grants')" 1 || return 1
	want_eq "grants applied" "$(jqv '.applied.grants')" 1 || return 1
	want_eq "rows for the one that claims write" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM grants WHERE id = '$bad'")" 0 || return 1
	want_eq "rows for the one that claims read" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM grants WHERE id = '$good'")" 1 || return 1
	case "$(jqv '.reasons[0]')" in
	*cap*) ;;
	*)
		printf 'the refusal is not about the cap: %s\n' "$(jqv '.reasons[0]')" >&2
		return 1
		;;
	esac

	# Out of the peer's table again, so a grant this check invented is not
	# something the nodes go on replicating to each other.
	psql5_do "$N5_DSN_B" "DELETE FROM grants WHERE id = '$good'" || return 1
	printf 'the write cap was refused on the merge and the read one applied: %s\n' \
		"$(jqv '.reasons[0]')"
}

# MED/LOW. serveStdio read stdin with `for scanner.Scan()`, which is not
# interruptible: the signal handler cancelled the context and the process stayed
# in the read until the client closed the pipe. So `flowy mcp` outlived SIGTERM,
# and a client that kills its server and waits for it waited for its own
# timeout instead.
#
# The check is the real thing: a `flowy mcp` on stdio whose stdin is held open
# and silent, which is what an idle client looks like, and a SIGTERM.
flowy_mcp_exits_on_sigterm() {
	local fifo="$WORK/mcp-stdin" log="$WORK/mcp-sigterm.log" pid i
	rm -f "$fifo" "$log"
	mkfifo "$fifo" || return 1

	DATABASE_URL="$DATABASE_URL" FLOWY_NODE=gate "$ROOT/flowy" mcp <"$fifo" >/dev/null 2>"$log" &
	pid=$!
	# The write end stays open here, so the server's stdin never sees EOF and
	# only the signal can end it.
	exec 9>"$fifo"

	# Up first: the transport says so on stderr before it reads anything.
	for i in $(seq 1 100); do
		if grep -q 'stdio transport' "$log" 2>/dev/null; then
			break
		fi
		sleep 0.1
	done
	if ! grep -q 'stdio transport' "$log" 2>/dev/null; then
		kill -9 "$pid" 2>/dev/null
		exec 9>&-
		printf 'flowy mcp never reached its stdio loop:\n%s\n' "$(cat "$log")" >&2
		return 1
	fi

	kill -TERM "$pid" 2>/dev/null || true
	for i in $(seq 1 50); do
		if ! kill -0 "$pid" 2>/dev/null; then
			break
		fi
		sleep 0.1
	done
	if kill -0 "$pid" 2>/dev/null; then
		kill -9 "$pid" 2>/dev/null
		wait "$pid" 2>/dev/null || true
		exec 9>&-
		rm -f "$fifo"
		printf 'flowy mcp was still running five seconds after SIGTERM, with its\n' >&2
		printf 'stdin held open: the read loop never looked at the signal\n' >&2
		return 1
	fi
	wait "$pid" 2>/dev/null || true
	exec 9>&-
	rm -f "$fifo"
	printf 'flowy mcp on stdio exited on SIGTERM with its stdin still open\n'
}

# LOW. Event parents were stored verbatim on both write paths. POST /api/events
# took the whole list on trust; the chat path read parents[0] through the filter
# only to decide which thread to inherit, and only when the body named no thread.
# So an event could claim descent from ids that are not here, or from a
# conversation the writer cannot see, and the console's thread pane and every
# future reader of the DAG take those edges for structure.
an_event_cannot_name_a_parent_it_cannot_read() {
	recall
	local missing before after mine

	missing="no-such-event-$$-$(date +%s)"
	before="$(scalar "SELECT count(*) FROM events")" || return 1

	# An id that is not an event at all.
	want_status 400 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg p "$missing" \
			'{type: "chat", room: "general", body: "descended from nothing", parents: [$p]}')" ||
		return 1
	want_status 400 POST "$TOKEN_A" /api/chat/general/say \
		"$(jq -nc --arg p "$missing" '{body: "answering nothing at all", parents: [$p]}')" || return 1

	# And one that is here, and out of this speaker's reach: CHAT_PC is a
	# message in pc, and B holds a grant into pa and none into pc.
	want_status 400 POST "$TOKEN_B" /api/events \
		"$(jq -nc --arg p "$CHAT_PC" \
			'{type: "chat", room: "general", body: "descended from pc", parents: [$p]}')" || return 1
	want_status 400 POST "$TOKEN_B" /api/chat/general/say \
		"$(jq -nc --arg p "$CHAT_PC" '{body: "answering what I cannot see", parents: [$p]}')" ||
		return 1

	# A parent hiding in a list of readable ones is still a parent.
	want_status 200 POST "$TOKEN_A" /api/chat/general/say '{"body": "a message of my own"}' || return 1
	mine="$(jqv .id)"
	want_status 400 POST "$TOKEN_A" /api/chat/general/say \
		"$(jq -nc --arg ok "$mine" --arg p "$missing" \
			'{body: "one real parent and one invented", parents: [$ok, $p]}')" || return 1

	# Nothing was written by any of them: five refusals, and the one message
	# above is the only row the log gained.
	after="$(scalar "SELECT count(*) FROM events")" || return 1
	want_eq "events the refusals wrote" "$((after - before))" 1 || return 1
	want_eq "rows naming the invented parent" \
		"$(scalar "SELECT count(*) FROM events WHERE '$missing' = ANY(parents)")" 0 || return 1

	# And the edge a speaker may name goes through, on both paths.
	want_status 200 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg p "$mine" \
			'{type: "chat", room: "general", body: "a real edge", parents: [$p]}')" || return 1
	want_eq "the parent it recorded" "$(jqv '.parents | join(",")')" "$mine" || return 1
	want_status 200 POST "$TOKEN_A" /api/chat/general/say \
		"$(jq -nc --arg p "$mine" '{body: "and a real reply", parents: [$p]}')" || return 1
	want_eq "the reply parent" "$(jqv '.parents | join(",")')" "$mine" || return 1
	printf 'a parent that is not there and a parent out of reach are both refused\n'
}

say "a grant says what it can do, and it is a capability this node has"
check "cap outside the implemented set is refused at the API door (MED 2)" \
	a_grant_carries_a_cap_this_node_implements
check "and on the merge, where a peer signs its own invention (MED 2)" \
	a_pushed_grant_with_a_cap_nobody_implements_is_refused

say "a memory and the entry that records it are one write"
check "a memory write that cannot log itself writes neither row (MED 3)" \
	go test -count=1 -run TestWriteMemoryIsAllOrNothing ./internal/store

say "the mcp server stops when it is told to"
check "flowy mcp on stdio exits on SIGTERM instead of waiting for its client (MED 4)" \
	flowy_mcp_exits_on_sigterm
check "the stdio loop returns on a cancelled context rather than blocking in Scan (MED 4)" \
	go test -count=1 -run TestStdioStopsOnCancellation .

say "an event descends from what its writer can see"
check "a parent that is missing or out of reach is refused on both write paths (LOW 5)" \
	an_event_cannot_name_a_parent_it_cannot_read

say "the tree that is handed over is a tree that builds"
check "the committed tree builds with no console build in it (ROBUSTNESS)" \
	the_committed_tree_builds_with_no_console_build

# ------------------------------------------- the tenth round of security fixes
#
# The re-review certified the core again - the filter is still a WHERE clause
# with nothing filtered after the fact, the SQL is still parameterised, the
# clock and the ids still fail loud, the tombstone and TOCTOU holes are still
# shut - and found one real leak and four pieces of hardening around it.
#
# The leak is the event filter's project-wide grant branch: it asked for a live
# edge into the event's project and nothing else, so an artifact behind the
# personal or project-only floor was refused row by row and handed over event by
# event. The rest are places where the node says more than it should, answers
# less than it was asked for, or lets a boundary move without saying so.
#
# They run last, against the node the earlier phases left standing.

# HIGH 1. The event filter's project-wide branch had no floor. The share branch
# beside it joins artifacts and refuses a personal or project-only one; this one
# joined nothing, so a principal of pb holding the pb -> pa grant read every
# event in pa - the chat about a project-only design, the status trail it mints,
# the bodies and the meta - over /api/events, the inbox, a room read and a
# replication pull, which is how a federated peer came to hold event bodies it
# could never have pulled row by row.
an_event_about_a_floored_artifact_stays_behind_the_floor() {
	recall
	local art word chatter open_id trail ids mine id
	word="grithersnap"
	chatter="hollowmarch"

	# A project-only note in pa. pb holds the project-wide grant Phase 1 issued,
	# and the floor is exactly what that grant does not reach.
	want_status 200 POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg w "$word" '{type: "note", title: "the design nobody outside pa sees",
			body: ("the word is " + $w), visibility: "project-only"}')" || return 1
	art="$(jqv .id)"
	want_eq "the visibility it landed at" "$(jqv .visibility)" project-only || return 1

	# Row by row, B is refused. That is the control, and it has always held.
	want_status 404 GET "$TOKEN_B" "/api/artifact/$art" || return 1

	# Three events name it: a message about it, the status trail the node mints
	# itself, and a message naming A's personal item, which is the other floor.
	want_status 200 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$art" --arg w "$word" '{type: "chat", room: "padesign", artifact: $a,
			body: ("about the " + $w + " design")}')" || return 1
	want_status 200 POST "$TOKEN_A" "/api/artifact/$art/status" '{"status": "triaged"}' || return 1
	want_status 200 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$MEM_PERSONAL" --arg w "$word" '{type: "chat", room: "padesign",
			artifact: $a, body: ("and the " + $w + " note I keep to myself")}')" || return 1

	# And one that names no artifact at all: project chatter, which is what the
	# grant is for and which must still cross it.
	want_status 200 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg w "$chatter" '{type: "chat", room: "padesign",
			body: ("standup: " + $w)}')" || return 1
	open_id="$(jqv .id)"

	trail="$(scalar "SELECT count(*) FROM events WHERE artifact = '$art'")" || return 1
	want_eq "events in the log naming the project-only note" "$trail" 2 || return 1
	# Every event in the log that names either floored artifact - the two above
	# and, for the personal item, the memory.write entries earlier phases left -
	# and, separately, the ones this check wrote into its own room, which is what
	# pa itself has to still be able to read.
	ids="$(scalar "SELECT string_agg(id, ' ') FROM events
	                WHERE artifact IN ('$art', '$MEM_PERSONAL')")" || return 1
	mine="$(scalar "SELECT string_agg(id, ' ') FROM events
	                 WHERE artifact IN ('$art', '$MEM_PERSONAL') AND room = 'padesign'")" || return 1

	# What B gets: the log, the inbox, the room and a replication pull. None of
	# them carries an event about either floored artifact, by id or by word.
	local path
	for path in "/api/events?limit=1000" "/api/inbox?limit=1000" \
		"/api/chat/padesign?limit=1000" "/api/sync/pull?since=0&limit=5000"; do
		api GET "$TOKEN_B" "$path" || return 1
		want_eq "status of $path for B" "$API_STATUS" 200 || return 1
		for id in $ids; do
			if printf '%s' "$API_BODY" | grep -qF "$id"; then
				printf 'B read %s over %s: an event about an artifact B is refused\n' \
					"$id" "$path" >&2
				return 1
			fi
		done
		if printf '%s' "$API_BODY" | grep -qF "$word"; then
			printf 'B read the body of a floored event over %s\n' "$path" >&2
			return 1
		fi
	done

	# The widening the grant is for is untouched: the event that names no
	# artifact still crosses, over the same two surfaces.
	api GET "$TOKEN_B" "/api/events?limit=1000" || return 1
	want_eq "the project event B may read" \
		"$(printf '%s' "$API_BODY" | jq "[.events[] | select(.id == \"$open_id\")] | length")" 1 ||
		return 1
	api GET "$TOKEN_B" "/api/sync/pull?since=0&limit=5000" || return 1
	want_eq "and the same event on a pull" \
		"$(printf '%s' "$API_BODY" | jq "[.events[] | select(.id == \"$open_id\")] | length")" 1 ||
		return 1

	# And pa reads its own log as it always did - the floor narrows the grant,
	# not the project the events are in.
	api GET "$TOKEN_A" "/api/events?room=padesign&limit=1000" || return 1
	for id in $mine $open_id; do
		want_eq "pa's own read of $id" \
			"$(printf '%s' "$API_BODY" | jq "[.events[] | select(.id == \"$id\")] | length")" 1 ||
			return 1
	done
	printf 'the %s events about %s stay in pa; the one that names no artifact crosses\n' \
		"$trail" "$art"
}

# MED 2. Every 500 carried err.Error() into the body: the store's wrapped chain
# ending in a lib/pq diagnostic - table names, column names, constraint names,
# a fragment of the statement - handed to any principal holding any token, a
# minimal federated peer included. The operator needs all of it and gets it in
# the log; the caller gets a stable string and a reference to quote.
#
# The failure is forced with a CHECK constraint added and dropped around the one
# request, which is the least this can disturb: no trigger, no procedure, no
# schema of ours changed, and the statement that fails is a perfectly ordinary
# INSERT whose error is exactly the kind that used to go out.
a_failed_write_answers_internal_error() {
	recall
	local title status body ref forbidden
	title="boom-$$-$(date +%s)"

	psql_do "ALTER TABLE artifacts ADD CONSTRAINT flowy_gate_boom CHECK (title <> '$title')" ||
		return 1
	api POST "$TOKEN_A" /api/artifacts \
		"$(jq -nc --arg t "$title" '{type: "note", title: $t, body: "this write cannot land"}')"
	status="$API_STATUS"
	body="$API_BODY"
	psql_do "ALTER TABLE artifacts DROP CONSTRAINT flowy_gate_boom" || return 1

	want_eq "status of a write the store could not do" "$status" 500 || return 1
	want_eq "what the body says" "$(printf '%s' "$body" | jq -r .error)" "internal error" || return 1

	# Nothing about the database, and nothing about the statement.
	for forbidden in pq constraint flowy_gate_boom "store:" relation column INSERT; do
		if printf '%s' "$body" | grep -qF "$forbidden"; then
			printf 'the 500 body said %q:\n%s\n' "$forbidden" "$body" >&2
			return 1
		fi
	done

	# The operator's half: a reference in the body, and the whole chain under it
	# in the log.
	ref="$(printf '%s' "$body" | jq -r .ref)"
	if [ -z "$ref" ] || [ "$ref" = null ]; then
		printf 'the 500 carried no reference to grep for:\n%s\n' "$body" >&2
		return 1
	fi
	if ! grep -q "ref=$ref" "$SERVE_LOG"; then
		printf 'nothing in the serve log carries ref=%s\n' "$ref" >&2
		return 1
	fi
	if ! grep "ref=$ref" "$SERVE_LOG" | grep -q flowy_gate_boom; then
		printf 'the log line for ref=%s does not carry the error it stands for:\n%s\n' \
			"$ref" "$(grep "ref=$ref" "$SERVE_LOG")" >&2
		return 1
	fi

	# And the refused write wrote nothing.
	want_eq "rows the failed write left behind" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE title = '$title'")" 0 || return 1
	printf 'a failed write answers "internal error" with ref=%s, and the log has the rest\n' "$ref"
}

# MED 3. limit() returned the default for an absent limit and for one over the
# cap, so ?limit=5000 got 200 rows with nothing said about it - and a short page
# means "that is all of them" everywhere else here, so a caller reading one
# stopped at 200 believing it had the lot. Over the cap is now the cap.
#
# The rows are inserted and deleted here rather than written over the API: what
# is being tested is the paging arithmetic, and 1100 of anything is not
# something to leave in a database the checks after this one read.
a_limit_over_the_cap_is_the_cap() {
	recall
	local artifacts events defaulted asked
	psql_do "INSERT INTO artifacts (id, type, owner_user, title, body, visibility, hlc, node, tombstone)
	         SELECT 'limitcap-ar-' || lpad(g::text, 5, '0'), 'note', '$USER_A',
	                'limit cap row ' || g, 'paging', 'personal', g, 'gate', false
	           FROM generate_series(1, 1100) g" || return 1
	psql_do "INSERT INTO events (id, type, project, room, thread, parents, actor, seq_hlc, node, body)
	         SELECT 'limitcap-ev-' || lpad(g::text, 5, '0'), 'chat', 'pa', 'limitcap',
	                'limitcap-ev-' || lpad(g::text, 5, '0'), '{}', '$USER_A', g, 'gate', 'paging'
	           FROM generate_series(1, 1100) g" || return 1

	# Asked for five thousand: the cap, not the default.
	api GET "$TOKEN_A" "/api/artifacts?limit=5000" || return 1
	artifacts="$(hits)"
	api GET "$TOKEN_A" "/api/events?room=limitcap&limit=5000" || return 1
	events="$(printf '%s' "$API_BODY" | jq '.events | length')"

	# Asked for nothing, and asked for something in between: unchanged.
	api GET "$TOKEN_A" /api/artifacts || return 1
	defaulted="$(hits)"
	api GET "$TOKEN_A" "/api/artifacts?limit=250" || return 1
	asked="$(hits)"

	psql_do "DELETE FROM artifacts WHERE id LIKE 'limitcap-ar-%'" || return 1
	psql_do "DELETE FROM events WHERE id LIKE 'limitcap-ev-%'" || return 1

	want_eq "artifacts for limit=5000" "$artifacts" 1000 || return 1
	want_eq "events for limit=5000" "$events" 1000 || return 1
	want_eq "artifacts for no limit at all" "$defaulted" 200 || return 1
	want_eq "artifacts for limit=250" "$asked" 250 || return 1
	printf 'over the cap is the cap (1000), absent is the default (200), in between is asked for\n'
}

# LOW 4. decodeJSON decoded one value and never asked whether the reader was
# exhausted, so everything after the first JSON value was dropped without a
# word. DisallowUnknownFields - the whole strict-input guarantee - only ever
# looked inside the value it decoded, and silently dropped input is how a row
# gets written at a visibility nobody asked for.
a_body_with_a_second_json_value_is_refused() {
	recall
	local before after

	before="$(scalar "SELECT count(*) FROM artifacts")" || return 1

	want_status 400 POST "$TOKEN_A" /api/artifacts \
		'{"type":"note","title":"the first value"}{"type":"note","title":"the second","visibility":"personal"}' ||
		return 1
	want_eq "what it says" "$(jqv .error | cut -d: -f1)" "bad request body" || return 1
	# Not only objects: anything at all after the first value.
	want_status 400 POST "$TOKEN_A" /api/artifacts '{"type":"note","title":"and a stray number"} 7' ||
		return 1
	want_status 400 POST "$TOKEN_A" /api/artifacts '{"type":"note","title":"and a stray word"} nope' ||
		return 1
	# The same door on the other write paths.
	want_status 400 POST "$TOKEN_A" /api/chat/general/say '{"body":"one"}{"body":"two"}' || return 1
	want_status 400 POST "$TOKEN_A" /api/events \
		'{"type":"chat","room":"general","body":"one"}{"type":"chat","room":"general","body":"two"}' ||
		return 1

	after="$(scalar "SELECT count(*) FROM artifacts")" || return 1
	want_eq "artifacts the refusals wrote" "$((after - before))" 0 || return 1
	want_eq "events the refusals wrote" \
		"$(scalar "SELECT count(*) FROM events WHERE body IN ('one', 'two')")" 0 || return 1

	# One value, with the whitespace a client is entitled to send around it, is
	# still a body.
	want_status 200 POST "$TOKEN_A" /api/artifacts '
		{"type":"note","title":"just the one value"}
	' || return 1
	want_eq "the title that went through" "$(jqv .title)" "just the one value" || return 1
	printf 'a second JSON value is a 400 on every write path, and one value still goes through\n'
}

# MED 5. mem_write's update path filled in a project whenever the update named a
# non-personal scope, so mem_write {id, scope: "shared"} on a personal item
# moved it into the caller's project as shared - owner-initiated, so not an
# escalation, but a floor crossed with nothing said about it, and the refusal
# POST /api/artifacts gives for the same move was right there to copy.
mem_write_will_not_promote_a_personal_item() {
	recall
	local id scope
	want_tool mem_write "$TOKEN_A" '{
		"title": "the one that stays mine",
		"body": "snickerdoodlethrum is the word",
		"scope": "personal"
	}' || return 1
	id="$(tv .item.id)"
	want_eq "the visibility it landed at" "$(tv .item.visibility)" personal || return 1
	want_eq "and its project" "$(tv .item.project)" null || return 1

	for scope in shared project; do
		want_tool_fails mem_write "$TOKEN_A" \
			"$(jq -nc --arg i "$id" --arg s "$scope" '{id: $i, scope: $s}')" \
			"create it there instead" || return 1
	done

	# It is where it was, and it is still nobody else's: B holds the grant into
	# pa and reads neither the item nor the word.
	want_tool mem_read "$TOKEN_A" "{\"id\": \"$id\"}" || return 1
	want_eq "the visibility afterwards" "$(tv .item.visibility)" personal || return 1
	want_eq "the project afterwards" "$(tv .item.project)" null || return 1
	want_eq "rows of it that ever got a project" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE id = '$id' AND project IS NOT NULL")" 0 ||
		return 1
	want_tool_fails mem_read "$TOKEN_B_AGENT" "{\"id\": \"$id\"}" "no such memory item" || return 1
	want_tool mem_search "$TOKEN_B_AGENT" '{"q": "snickerdoodlethrum"}' || return 1
	want_eq "B's hits for it" "$(tv .count)" 0 || return 1

	# An update that leaves the scope alone still works, and so does writing a
	# new item at a scope - the refusal is about moving a floor, not about
	# updates or about scopes.
	want_tool mem_write "$TOKEN_A" \
		"$(jq -nc --arg i "$id" '{id: $i, title: "edited, and still mine"}')" || return 1
	want_eq "the title after an ordinary edit" "$(tv .item.title)" "edited, and still mine" || return 1
	want_eq "the project after an ordinary edit" "$(tv .item.project)" null || return 1
	want_tool mem_write "$TOKEN_A" '{"title": "a new one, written shared", "scope": "shared"}' || return 1
	want_eq "where a new shared item lands" "$(tv .item.project)" pa || return 1
	printf 'memory %s cannot be promoted out of the personal floor by an edit\n' "$id"
}

say "an event is no more readable than the artifact it names"
check "a project-wide grant does not reach events about a floored artifact (HIGH 1)" \
	an_event_about_a_floored_artifact_stays_behind_the_floor
check "the event filter and the artifact filter agree on the same rows (HIGH 1)" \
	go test -count=1 -run TestEventFilterInheritsTheArtifactFloor ./internal/store

say "a 500 says that it failed and nothing else"
check "a write the store could not do answers an opaque body (MED 2)" \
	a_failed_write_answers_internal_error

say "a page that was asked for is the page that comes back"
check "a limit over the cap is the cap, not the default (MED 3)" \
	a_limit_over_the_cap_is_the_cap

say "a request body is one JSON value"
check "anything after the first value is a 400 on every write path (LOW 4)" \
	a_body_with_a_second_json_value_is_refused

say "a memory item does not leave the personal floor by being edited"
check "mem_write cannot promote a personal item into a project (MED 5)" \
	mem_write_will_not_promote_a_personal_item

# ---------------------------------------------------------------- iteration 11
#
# An event says four things about work that may not be its writer's: who wrote
# it, what thread it is in, what it descends from, and what artifact it is
# about. Three of those are checked on the doors they arrive at. The fourth was
# not, and on the pull door the first was not either - so both are here, with
# the rescan a fresh project-wide grant sets off beside them.

# MED/HIGH 2. The artifact column was taken on trust on every door. It is not
# decoration: the per-artifact share clause in the event filter carries the
# events about an artifact to everybody it is shared with, so a writer holding
# nothing but a guessed id could put entries into what that artifact's readers
# see - and they replicated from there. parents were closed for this exact
# reason ("an edge in the log is a claim"); the artifact column was left.
an_event_cannot_name_an_artifact_it_cannot_read() {
	recall
	local mine before
	# A's personal memory item. B holds the pb -> pa grant and still reads
	# nothing of it, which is what makes it the right thing to try to name.
	want_status 404 GET "$TOKEN_B" "/api/artifact/$MEM_PERSONAL" || return 1
	before="$(scalar "SELECT count(*) FROM events WHERE artifact = '$MEM_PERSONAL'")" || return 1

	want_status 404 POST "$TOKEN_B" /api/events \
		"$(jq -nc --arg a "$MEM_PERSONAL" '{type: "chat", room: "pb/bugs", artifact: $a,
			body: "an entry in a trail that is not mine"}')" || return 1
	want_eq "what it says" "$(jqv .error | cut -d';' -f1)" \
		"artifact $MEM_PERSONAL is not one you can read" || return 1

	# An id that is not here at all gets the same answer, the same way a parent
	# that is not here and a parent out of reach do.
	want_status 404 POST "$TOKEN_B" /api/events \
		'{"type":"chat","room":"pb/bugs","artifact":"01HZZZZZZZZZZZZZZZZZZZZZZZ",
		  "body":"or about one nobody has"}' || return 1

	# Nothing landed in the trail either way.
	want_eq "events naming A's personal item" \
		"$(scalar "SELECT count(*) FROM events WHERE artifact = '$MEM_PERSONAL'")" "$before" ||
		return 1
	want_eq "events B wrote about it" \
		"$(scalar "SELECT count(*) FROM events
		            WHERE artifact = '$MEM_PERSONAL' AND actor = '$USER_B'")" 0 || return 1

	# B naming its own artifact is untouched: what is refused is naming somebody
	# else's, not naming one at all.
	mine="$(new_artifact "$TOKEN_B" note "B's own to be about")" || return 1
	want_status 200 POST "$TOKEN_B" /api/events \
		"$(jq -nc --arg a "$mine" '{type: "chat", room: "pb/bugs", artifact: $a,
			body: "and this trail is mine"}')" || return 1
	want_eq "the artifact it went in under" "$(jqv .artifact)" "$mine" || return 1
	printf 'B cannot put an entry in %s trail and can put one in %s\n' "$MEM_PERSONAL" "$mine"
}

say "an event's attribution is not the signer's to invent"
check "a pulled event under a third party's name from an unpinned node is refused (HIGH 1)" \
	go test -count=1 -run TestPulledEventCannotClaimSomebodyElsesName ./internal/store

say "an event is about something its writer can read"
check "naming an artifact out of reach is a 404 on POST /api/events (MED/HIGH 2)" \
	an_event_cannot_name_an_artifact_it_cannot_read
check "and it is refused at both merge doors (MED/HIGH 2)" \
	go test -count=1 -run TestEventCannotNameAnArtifactItCannotRead ./internal/store

say "one grant does not buy a project's worth of statements"
check "the newly-visible rescan reads and writes in batches (MED 3)" \
	go test -count=1 -run TestNewlyVisibleRescanIsBoundedAndBatched ./internal/store

# ---------------------------------------------------------------- iteration 12
#
# The floor iteration 10 put on the event filter's grant branches was not on the
# branch beside them - the home-project one, which every reader in the event's
# own project takes and which grants nothing conditionally. So it is not fixed
# branch by branch this time: the artifact's read rule has one definition,
# artifactReachSQL, and both the artifact filter and the event filter evaluate
# it. The other two are a filing that could be raced into orphaning an issue,
# and an error the chat path threw away.

# HIGH 1. A per-artifact share was a way to publish somebody else's artifact to
# a whole project. The share reaches the subject and nobody else - the row is
# refused to every one of their project mates - but their events land in their
# home project, and the home branch hands every event in a project to everybody
# in it with no test on the artifact named. So the body and the meta went to the
# project, over /api/events, the inbox, a room read and a replication pull,
# while /api/artifact/{id}/history answered those same readers 404.
#
# pa is the project with two people in it: A, and the operator, who is a second
# person there and not a second token for the first.
an_event_in_your_project_is_no_wider_than_the_artifact() {
	recall
	local word theirs ours ev ours_ev open_ev path id
	word="quillshadow"

	# B's artifact, in pb, shared to A by name. Nothing joins pa to pb, so it is
	# A's to read through the share and nobody else's in pa.
	theirs="$(new_artifact "$TOKEN_B" bug "the fault only A was shown")" || return 1
	want_status 200 POST "$TOKEN_B" /api/grants \
		"$(jq -nc --arg a "$theirs" --arg s "$USER_A" '{artifact: $a, subject: $s}')" || return 1
	want_status 200 GET "$TOKEN_A" "/api/artifact/$theirs" || return 1
	want_status 404 GET "$TOKEN_OP" "/api/artifact/$theirs" || return 1

	# A names it on an event, which lands in pa: that is where A's writes land,
	# and it is the whole of how a pb artifact's events got into pa.
	want_status 200 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$theirs" --arg w "$word" '{type: "chat", room: "quillroom",
			artifact: $a, body: ("what pb showed me: " + $w)}')" || return 1
	ev="$(jqv .id)"
	want_eq "the project the event landed in" "$(jqv .project)" pa || return 1

	# Two controls in the same room: an event about pa's own artifact, and one
	# about nothing at all. The project is not narrowed by any of this.
	ours="$(new_artifact "$TOKEN_A" note "pa's own, and pa's to read")" || return 1
	want_status 200 POST "$TOKEN_A" /api/events \
		"$(jq -nc --arg a "$ours" '{type: "chat", room: "quillroom", artifact: $a,
			body: "about one of our own"}')" || return 1
	ours_ev="$(jqv .id)"
	want_status 200 POST "$TOKEN_A" /api/events \
		'{"type": "chat", "room": "quillroom", "body": "and the standup"}' || return 1
	open_ev="$(jqv .id)"

	# Every surface the log leaves by, read as the other person in pa.
	for path in "/api/events?limit=1000" "/api/inbox?limit=1000" \
		"/api/chat/quillroom?limit=1000" "/api/sync/pull?since=0&limit=5000"; do
		api GET "$TOKEN_OP" "$path" || return 1
		want_eq "status of $path for the operator" "$API_STATUS" 200 || return 1
		if printf '%s' "$API_BODY" | grep -qF "$ev"; then
			printf 'a pa mate read %s over %s: an event about an artifact they are refused\n' \
				"$ev" "$path" >&2
			return 1
		fi
		if printf '%s' "$API_BODY" | grep -qF "$word"; then
			printf 'a pa mate read the body of %s over %s\n' "$ev" "$path" >&2
			return 1
		fi
	done

	# And the project still works: both controls cross to the same reader.
	for path in "/api/events?limit=1000" "/api/chat/quillroom?limit=1000"; do
		api GET "$TOKEN_OP" "$path" || return 1
		for id in "$ours_ev" "$open_ev"; do
			want_eq "the operator's read of $id over $path" \
				"$(printf '%s' "$API_BODY" | jq "[.events[] | select(.id == \"$id\")] | length")" 1 ||
				return 1
		done
	done

	# The person the share was made to reads their own event, as they always did.
	api GET "$TOKEN_A" "/api/chat/quillroom?limit=1000" || return 1
	for id in "$ev" "$ours_ev" "$open_ev"; do
		want_eq "A's own read of $id" \
			"$(printf '%s' "$API_BODY" | jq "[.events[] | select(.id == \"$id\")] | length")" 1 ||
			return 1
	done
	printf 'event %s about the shared %s stays with A; the two that name pa rows cross\n' \
		"$ev" "$theirs"
}

# MED 2. Filing is a read, a round trip to the forge and a write, and only the
# first two ever asked whether the artifact had been filed. Two filings could be
# inside all three at once: both minted a real issue and both wrote the link, so
# the second overwrote the first and issue #1 was left open on the tracker with
# no row pointing at it - no state sync, no reply push, and nothing to find it
# by. The link is now written under `external IS NULL`, so one of the two
# matches no row and is answered the same 409 the up-front read gives.
two_filings_at_once_leave_one_link() {
	recall
	local id body one two s1 s2 b1 b2 won lost winner trail
	id="$(new_artifact "$TOKEN_A_PC" bug "the one two requests file at once")" || return 1
	body="$(jq -nc --arg a "$id" '{artifact: $a, repo: "o/r"}')"
	one="$WORK/file-one"
	two="$WORK/file-two"

	# Both in flight before either can have written.
	curl --silent --show-error -X POST -H "Authorization: Bearer $TOKEN_A_PC" \
		-H 'Content-Type: application/json' --data-binary "$body" -w '\n%{http_code}' \
		"http://127.0.0.1:$HTTP_PORT/api/forge/file" >"$one" 2>&1 &
	local first=$!
	curl --silent --show-error -X POST -H "Authorization: Bearer $TOKEN_A_PC" \
		-H 'Content-Type: application/json' --data-binary "$body" -w '\n%{http_code}' \
		"http://127.0.0.1:$HTTP_PORT/api/forge/file" >"$two" 2>&1 &
	local second=$!
	wait "$first" || return 1
	wait "$second" || return 1

	s1="$(tail -n1 "$one")"
	s2="$(tail -n1 "$two")"
	b1="$(sed '$d' "$one")"
	b2="$(sed '$d' "$two")"

	# One of them filed it. The other is refused, whichever door refused it.
	case "$s1$s2" in
	200409) won="$b1" lost="$b2" ;;
	409200) won="$b2" lost="$b1" ;;
	200200)
		printf 'both filings reported success: one of the two issues is unreferenced\n%s\n%s\n' \
			"$b1" "$b2" >&2
		return 1
		;;
	*)
		printf 'two filings answered %s and %s:\n%s\n%s\n' "$s1" "$s2" "$b1" "$b2" >&2
		return 1
		;;
	esac

	winner="$(printf '%s' "$won" | jq -r .external.number)"
	if [ -z "$winner" ] || [ "$winner" = null ]; then
		printf 'the filing that succeeded named no issue:\n%s\n' "$won" >&2
		return 1
	fi

	# The row names the issue that won, and it is the only trail entry: the
	# loser's filing entry went back out with its transaction.
	want_status 200 GET "$TOKEN_A_PC" "/api/artifact/$id" || return 1
	want_eq "the issue the artifact names" "$(jqv .external.number)" "$winner" || return 1
	trail="$(scalar "SELECT count(*) FROM events WHERE artifact = '$id' AND type = 'forge'")" ||
		return 1
	want_eq "filing entries in the trail" "$trail" 1 || return 1

	# The refusal names the link that won, so the caller is told which issue it
	# has. If this one lost late - after its own issue was open - it says so and
	# names that issue too, which is the only record anybody gets of it.
	want_eq "the issue the refusal points at" \
		"$(printf '%s' "$lost" | jq -r .external.number)" "$winner" || return 1
	case "$(printf '%s' "$lost" | jq -r .error)" in
	*"which nothing points at"*)
		local orphan
		orphan="$(printf '%s' "$lost" | jq -r '.error | capture("#(?<n>[0-9]+) [(]").n')"
		if [ "$orphan" = "$winner" ]; then
			printf 'the refusal named the winning issue as the orphan:\n%s\n' "$lost" >&2
			return 1
		fi
		api GET "$TOKEN_OP" "/api/forge/mock/issue?repo=o/r&number=$orphan" || return 1
		want_eq "the orphaned issue is on the forge" "$API_STATUS" 200 || return 1
		printf 'one link (#%s), and the issue #%s the loser opened is named in its 409\n' \
			"$winner" "$orphan"
		;;
	*)
		printf 'one link (#%s); the second filing was refused before it opened anything\n' "$winner"
		;;
	esac
}

# LOW 3. A reply that names a parent and no thread inherits the parent's thread.
# The read of that parent treated every error as "cannot read it": a parent that
# is not readable is a deliberate fresh thread, but a store that could not answer
# - a dropped connection, a statement timeout - silently forked the conversation
# instead, minting a thread while the DAG edge still pointed at the parent, and
# nothing said the store had been unreachable. ThreadHidden six lines below has
# always told the two apart.
#
# The failure is forced on one row and put back immediately: created is NULL for
# the length of one request, which the scan cannot turn into an event and which
# is not "no such row" either. The id-only read the parents check does is
# unaffected, so the request gets all the way to the line under test.
a_parent_the_store_cannot_read_is_a_500() {
	recall
	local parent created before after status body thread
	want_status 200 POST "$TOKEN_A" /api/chat/brokenparent/say \
		'{"body": "the message that gets answered"}' || return 1
	parent="$(jqv .id)"
	thread="$(jqv .thread)"

	created="$(scalar "SELECT created FROM events WHERE id = '$parent'")" || return 1
	before="$(scalar "SELECT count(*) FROM events WHERE room = 'brokenparent'")" || return 1
	psql_do "UPDATE events SET created = NULL WHERE id = '$parent'" || return 1
	api POST "$TOKEN_A" /api/chat/brokenparent/say \
		"$(jq -nc --arg p "$parent" '{body: "an answer that must not fork the thread",
			parents: [$p]}')"
	status="$API_STATUS"
	body="$API_BODY"
	after="$(scalar "SELECT count(*) FROM events WHERE room = 'brokenparent'")" || return 1
	psql_do "UPDATE events SET created = '$created' WHERE id = '$parent'" || return 1

	want_eq "status when the store could not read the parent" "$status" 500 || return 1
	want_eq "what it says" "$(printf '%s' "$body" | jq -r .error)" "internal error" || return 1
	want_eq "messages the refusal wrote" "$((after - before))" 0 || return 1
	want_eq "threads it minted" \
		"$(scalar "SELECT count(DISTINCT thread) FROM events WHERE room = 'brokenparent'")" 1 ||
		return 1

	# And with the row readable again the same request goes through, and lands in
	# the thread it answers rather than in one of its own.
	want_status 200 POST "$TOKEN_A" /api/chat/brokenparent/say \
		"$(jq -nc --arg p "$parent" '{body: "and now it answers", parents: [$p]}')" || return 1
	want_eq "the thread the reply landed in" "$(jqv .thread)" "$thread" || return 1
	printf 'a parent the store could not read is a 500, and no thread was forked\n'
}

say "the event floor and the artifact floor are one rule"
check "a share to one person is not a broadcast to their project (HIGH 1)" \
	an_event_in_your_project_is_no_wider_than_the_artifact
check "the home-project branch inherits the artifact floor (HIGH 1)" \
	go test -count=1 -run TestEventFilterHomeProjectInheritsTheArtifactFloor ./internal/store
check "an event never reaches further than the artifact it names (HIGH 1)" \
	go test -count=1 -run TestEventFloorMatchesTheArtifactFloor ./internal/store

say "one artifact, one issue, however many people ask at once"
check "two filings at once leave one link and name the loser's issue (MED 2)" \
	two_filings_at_once_leave_one_link
check "the link is written under external IS NULL (MED 2)" \
	go test -count=1 -run 'TestOnlyOneFilingWinsTheLink|TestTwoFilingsAtOnceLeaveOneLink' \
	./internal/store

say "a store that cannot answer is not an answer"
check "a parent the store could not read is a 500, not a new thread (LOW 3)" \
	a_parent_the_store_cannot_read_is_a_500

# ----------------------------------------------------- thirteenth round

# HIGH 2. created was outside the signature, on artifacts and on events, so a
# relay could hand a row on with its date moved and everything around it would
# still say authentic - a signed row carrying somebody else's timestamp, which
# is worse than an unsigned one. This is the wire version of the fix: a row is
# taken out of the node that wrote it, its created moved three months back and
# nothing else touched, and handed to a node that does not hold it yet by the
# very user who owns it. No authorisation rule has anything to say about that
# row, so the only thing that can refuse it is the signature.
a_moved_date_is_refused_over_the_wire() {
	recall5
	local id hlc delta moved dated
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_B" /api/artifacts \
		'{"type":"note","title":"dated by its author","body":"marrowglint"}' || return 1
	id="$(jqv .id)"
	hlc="$(jqv .hlc)"

	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" "/api/sync/pull?since=$((hlc - 1))" || return 1
	delta="$(printf '%s' "$API_BODY" | jq -c --arg i "$id" \
		'{artifacts: [.artifacts[] | select(.id == $i)], events: [], tasks: [], grants: []}')" ||
		return 1
	want_eq "the delta holds the row" \
		"$(printf '%s' "$delta" | jq '.artifacts | length')" 1 || return 1
	dated="$(printf '%s' "$delta" | jq -r '.artifacts[0].created | .[0:10]')" || return 1

	moved="$(printf '%s' "$delta" |
		jq -c '.artifacts[0].created = "2026-06-01T00:00:00Z"')" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$moved" || return 1
	want_eq "the back-dated row refused" "$(jqv '.refused.artifacts')" 1 || return 1
	want_eq "the back-dated row applied" "$(jqv '.applied.artifacts')" 0 || return 1
	want_eq "rows in B's table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$id'")" 0 || return 1

	# And the same delta with its date left alone is taken, dated as its author
	# dated it: what was refused is the rewrite, not the row.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the untouched row applied" "$(jqv '.applied.artifacts')" 1 || return 1
	want_eq "the date it crossed with" \
		"$(scalar5 "$N5_DSN_B" "SELECT to_char(created AT TIME ZONE 'UTC', 'YYYY-MM-DD')
		                          FROM artifacts WHERE id = '$id'")" "$dated" || return 1
	printf 'the row was refused with its date moved and taken with it left alone: %s\n' "$id"
}

# The addressee is the newest field inside an event's signature, and it is the
# only one in any of these encoders that is written conditionally - present only
# when there is one, so that an unaddressed event still encodes to the bytes
# every older node signed. A conditional field is exactly where a verify could
# be lenient without anybody noticing, so it is checked on the wire in the three
# ways a relay could lie with it: take it off, point it at somebody else, and
# put one on a message that had none.
a_rewritten_addressee_is_refused_over_the_wire() {
	recall5
	local id seq delta stripped redirected
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_B" /api/chat/addressing/say \
		"$(jq -nc --arg t "$N5_USER_A" '{to: $t, body: "for you, and signed as such"}')" || return 1
	id="$(jqv .id)"
	seq="$(jqv .seq_hlc)"
	want_eq "the addressee it was written with" "$(jqv .addressee)" "$N5_USER_A" || return 1

	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" "/api/sync/pull?since=$((seq - 1))" || return 1
	delta="$(printf '%s' "$API_BODY" | jq -c --arg i "$id" \
		'{artifacts: [], events: [.events[] | select(.id == $i)], tasks: [], grants: []}')" ||
		return 1
	want_eq "the delta holds the message" \
		"$(printf '%s' "$delta" | jq '.events | length')" 1 || return 1

	stripped="$(printf '%s' "$delta" | jq -c 'del(.events[0].addressee)')" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$stripped" || return 1
	want_eq "the message with its addressee taken off, refused" "$(jqv '.refused.events')" 1 || return 1
	want_eq "and applied" "$(jqv '.applied.events')" 0 || return 1

	redirected="$(printf '%s' "$delta" |
		jq -c --arg u "$N5_USER_B" '.events[0].addressee = $u')" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$redirected" || return 1
	want_eq "the message pointed at somebody else, refused" "$(jqv '.refused.events')" 1 || return 1
	want_eq "rows in B's table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM events WHERE id = '$id'")" 0 || return 1

	# And an addressee put onto a message that never had one - the other end of
	# the same conditional.
	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_B" /api/chat/addressing/say \
		'{"body": "said to the room and to nobody"}' || return 1
	local plain plain_seq named
	plain="$(jqv .id)"
	plain_seq="$(jqv .seq_hlc)"
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" "/api/sync/pull?since=$((plain_seq - 1))" || return 1
	named="$(printf '%s' "$API_BODY" | jq -c --arg i "$plain" --arg u "$N5_USER_A" \
		'{artifacts: [], events: [.events[] | select(.id == $i) | .addressee = $u],
		  tasks: [], grants: []}')" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$named" || return 1
	want_eq "an addressee put onto a room message, refused" "$(jqv '.refused.events')" 1 || return 1

	# The control: the same delta untouched is taken, with the addressee its
	# author put on it. What was refused is the rewrite and not the message.
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the untouched message applied" "$(jqv '.applied.events')" 1 || return 1
	want_eq "the addressee it crossed with" \
		"$(scalar5 "$N5_DSN_B" "SELECT addressee FROM events WHERE id = '$id'")" \
		"$N5_USER_A" || return 1
	printf 'an addressee cannot be removed, redirected or added in flight: %s\n' "$id"
}

say "a pulled row is the authoring party's to assert"
check "an artifact, a grant and an event owned by a third party from an unpinned node (HIGH 1)" \
	go test -count=1 -run TestPulledRowsAreTheAuthoringPartysToAssert ./internal/store
check "a rewrite of somebody else's artifact needs a pinned node too (HIGH 1)" \
	go test -count=1 -run TestAPulledRewriteOfAnothersArtifactNeedsAPinnedNode ./internal/store

say "the date a row carries is the date its author signed"
check "created is inside the signature, on artifacts and on events (HIGH 2)" \
	go test -count=1 -run TestTheCreatedDateIsInsideTheSignature ./internal/store
check "a local write signs the date the column will hold (HIGH 2)" \
	go test -count=1 -run TestALocalWritesDateIsSignedWithIt ./internal/store
check "a row whose date was moved is refused over the wire (HIGH 2)" \
	a_moved_date_is_refused_over_the_wire

say "the addressee a message crossed with is the one its author signed"
check "removed, redirected or added in flight, all refused" \
	a_rewritten_addressee_is_refused_over_the_wire

say "an identity is self-signed, is never rotated, and is pinned where that is required"
check "a key served for a node that did not sign it, and a second key for a known one (MED 3)" \
	go test -count=1 -run TestAServedIdentityIsSelfSignedAndNeverRotates ./internal/store

# --------------------------------------------------- security fixes, round 14
#
# The two merge doors used to run two different partial rules, and the sets of
# forgeries they refused were nearly disjoint rather than nested: a forged owner
# was refused on push and taken on pull, a share the artifact's real owner wrote
# elsewhere was taken on pull and refused on push, a grant out of the carrier's
# project was refused on push and taken on pull. A peer picks its door. So
# provenance is one predicate now - see mayAssert - and these checks are the
# same delta going in both ways.

# a_relay_is_the_same_at_both_doors - a row owned by A, authored on nodeA, at
# B's push door.
#
# B pinned nodeA's key, which is B's operator saying they believe what nodeA
# says about who did what - including about B's own users. So a row nodeA
# authored and A owns lands at the push door exactly as it lands at the pull
# door, and the same row authored on a node nobody pinned is refused at both.
# That is the whole of the fourteenth round, over the wire.
a_relay_is_the_same_at_both_doors() {
	recall5
	local relayed forged hlc delta seed
	relayed="relayed-owner-$$-$(date +%s)"
	forged="unpinned-owner-$$-$(date +%s)"
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1

	# nodeA is pinned on B, and the row is A's: a relay of somebody B decided to
	# believe.
	delta="$(jq -nc --arg i "$relayed" --arg o "$N5_USER_A" --argjson h "$hlc" '
		{events: [], tasks: [], grants: [], hwm: 0, artifacts: [
		  {id: $i, type: "note", project: "pb", owner_user: $o, title: "written on nodeA",
		   body: "cindergrass", visibility: "project", hlc: $h, node: "nodeA",
		   tombstone: false, reported: false}]}' | sign5 "$N5_DSN_A" nodeA)" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the relayed row applied" "$(jqv '.applied.artifacts')" 1 || return 1
	want_eq "the relayed row refused" "$(jqv '.refused.artifacts')" 0 || return 1
	want_eq "who B has it down as belonging to" \
		"$(scalar5 "$N5_DSN_B" "SELECT owner_user FROM artifacts WHERE id = '$relayed'")" \
		"$N5_USER_A" || return 1

	# The same row, the same owner, on a node whose key merely arrives with it.
	hlc="$(forged_hlc "$N5_PORT_B")" || return 1
	seed="$(seed_of owner-forger)" || return 1
	delta="$(jq -nc --arg i "$forged" --arg o "$N5_USER_A" --argjson h "$hlc" '
		{events: [], tasks: [], grants: [], hwm: 0, artifacts: [
		  {id: $i, type: "note", project: "pb", owner_user: $o, title: "not written on nodeA",
		   body: "cindergrass too", visibility: "project", hlc: $h, node: "owner-forger",
		   tombstone: false, reported: false}]}' | sign_seed "$seed" owner-forger)" || return 1
	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "the unpinned row applied" "$(jqv '.applied.artifacts')" 0 || return 1
	want_eq "the unpinned row refused" "$(jqv '.refused.artifacts')" 1 || return 1
	want_eq "rows in B's table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$forged'")" 0 || return 1

	# Out of B again, so the checks that follow are not reading this one's rows.
	psql5_do "$N5_DSN_B" "DELETE FROM artifacts WHERE id = '$relayed'" || return 1
	printf 'a relay from pinned nodeA landed and the same row from an unpinned node did not: %s\n' \
		"$(jqv '.reasons[0]')"
}

# the_version_names_the_build - two builds of two different commits report two
# versions, and the version on the wire is the one the binary carries.
#
# It was one constant for half a dozen distinct builds, so "which build refused
# that row" and "what is this peer running" had no answer over the wire - which
# is the question security work asks first.
the_version_names_the_build() {
	recall5
	local one two served
	go build -ldflags "-X main.buildStamp=aaaaaaa" -o "$WORK/flowy-stamp-a" . || return 1
	go build -ldflags "-X main.buildStamp=bbbbbbb" -o "$WORK/flowy-stamp-b" . || return 1
	one="$("$WORK/flowy-stamp-a" version)" || return 1
	two="$("$WORK/flowy-stamp-b" version)" || return 1
	if [ "$one" = "$two" ]; then
		printf 'two builds of two commits both report %s\n' "$one" >&2
		return 1
	fi
	case "$one" in
	*+aaaaaaa) ;;
	*)
		printf 'the version %s does not carry the stamp it was built with\n' "$one" >&2
		return 1
		;;
	esac

	napi "$N5_PORT_B" GET "" /healthz || return 1
	served="$(jqv .version)"
	want_eq "the version /healthz serves" "$served" "$("$ROOT/flowy" version)" || return 1
	case "$served" in
	*+*) ;;
	*)
		printf 'the version on the wire is %s, which names no build\n' "$served" >&2
		return 1
		;;
	esac
	printf 'the wire says %s, and another build would say something else\n' "$served"
}

say "the two merge doors refuse the same forgeries"
check "one signed delta, refused row for row and reason for reason at both doors (HIGH 1)" \
	go test -count=1 -run TestBothDoorsRefuseAndApplyTheSameRows ./internal/store
check "and a relay from a pinned node lands at whichever door it arrives at (HIGH 1)" \
	a_relay_is_the_same_at_both_doors

say "the version says which build is answering"
check "two builds report two versions, and the wire carries the build stamp (MED 2)" \
	the_version_names_the_build
check "the scheme is release+stamp and nothing is frozen into a literal (MED 2)" \
	go test -count=1 -run TestTheVersionCarriesABuildStamp .

# ---------------------------------------------------------------- phase 7: fuse
#
# The agent filesystem. Everything above this line ran with nothing mounted,
# which is the first thing this section asserts: FUSE is additive, the node does
# not know it exists until somebody runs `flowy fuse`, and the 300-odd checks
# that have already passed are the evidence for that rather than a claim about
# it.
#
# What follows mounts for real - one process, one directory, /dev/fuse - and
# then does file operations from the shell and asks the store what happened.

FUSE_MNT="$WORK/fuse-a"
FUSE_MNT_B="$WORK/fuse-b"
FUSE_LOG="$WORK/fuse-a.log"
FUSE_LOG_B="$WORK/fuse-b.log"
FUSE_PID_FILE="$WORK/fuse-a.pid"
FUSE_PID_FILE_B="$WORK/fuse-b.pid"
readonly FUSE_MNT FUSE_MNT_B FUSE_LOG FUSE_LOG_B FUSE_PID_FILE FUSE_PID_FILE_B

# The words below appear nowhere else in this run, so a search that finds one
# found the file it was written into.
FUSE_WORD=flumdiddle
FUSE_SHARED_WORD=gribbleflax
FUSE_CRASH_WORD=snorkbeetle
readonly FUSE_WORD FUSE_SHARED_WORD FUSE_CRASH_WORD

# This VM has FUSE. If it did not, every check below would fail for one reason,
# and it is worth saying which.
fuse_is_available() {
	if [ ! -c /dev/fuse ]; then
		printf 'there is no /dev/fuse here, so nothing can be mounted\n' >&2
		return 1
	fi
	if ! command -v fusermount3 >/dev/null 2>&1 && ! command -v fusermount >/dev/null 2>&1; then
		printf 'there is no fusermount on PATH\n' >&2
		return 1
	fi
	printf '/dev/fuse and %s\n' "$(command -v fusermount3 || command -v fusermount)"
}

# Nothing has been mounted yet, and memory has been working for three thousand
# lines. That is what "opt-in" means, said as an assertion.
fuse_off_is_the_default() {
	recall
	if [ -n "$(fuse_mounts_here)" ]; then
		printf 'a fuse filesystem is attached and this run did not attach one:\n%s\n' \
			"$(fuse_mounts_here)" >&2
		return 1
	fi
	# And memory works, with nothing mounted, exactly as it has all run.
	want_tool mem_write "$TOKEN_A" \
		'{"title": "written with no mount anywhere", "body": "the fuse-off path"}' || return 1
	local id
	id="$(tv .item.id)"
	want_tool mem_read "$TOKEN_A" "{\"id\": \"$id\"}" || return 1
	want_eq "the item reads back" "$(tv .item.id)" "$id" || return 1
	printf 'no fuse filesystem attached, and memory item %s written and read anyway\n' "$id"
}

fuse_mounts() {
	recall
	fuse_start "$FUSE_MNT" "$TOKEN_A" "$FUSE_LOG" "$FUSE_PID_FILE" || return 1
	if ! fuse_is_mounted "$FUSE_MNT"; then
		printf '%s is not in /proc/self/mounts after the mount\n' "$FUSE_MNT" >&2
		return 1
	fi
	# The path is the scope: the personal floor and the project this token is
	# for, and no directory for a project it has no business in.
	local listing dir
	listing="$(ls "$FUSE_MNT")"
	case "$listing" in
	*_personal*) ;;
	*)
		printf 'the root holds %q, want the personal floor in it\n' "$listing" >&2
		return 1
		;;
	esac
	case "$listing" in
	*pa*) ;;
	*)
		printf 'the root holds %q, want project pa in it\n' "$listing" >&2
		return 1
		;;
	esac
	for dir in "_personal/$USER_A/memory" "_personal/$USER_A/note" "pa/$USER_A/memory"; do
		if [ ! -d "$FUSE_MNT/$dir" ]; then
			printf '%s is not a directory in the mount\n' "$dir" >&2
			return 1
		fi
	done
	# The negotiated protocol level, read back off the server rather than
	# assumed from what was asked for.
	if ! grep -q 'FUSE 7\.' "$FUSE_LOG"; then
		printf 'the mount never reported what it negotiated:\n' >&2
		indent <"$FUSE_LOG" >&2
		return 1
	fi
	grep 'mounted' "$FUSE_LOG" | head -1
}

# A file written into the mount is a memory item in the store: one artifact,
# signed, with the scope its directory named, and one event that records it.
fuse_write_lands_in_the_store() {
	recall
	cat >"$FUSE_MNT/_personal/$USER_A/memory/decisions.md" <<EOF
---
title: the write-behind queue is the durability
tags: phase7, fuse
---
A $FUSE_WORD is what we called it while we were building it.
EOF
	fuse_await "SELECT count(*) FROM artifacts
	             WHERE type = 'memory' AND file_path = 'decisions.md'
	               AND owner_user = '$USER_A'" >/dev/null || return 1

	local id
	id="$(scalar "SELECT id FROM artifacts WHERE file_path = 'decisions.md' AND owner_user = '$USER_A'")"
	remember FUSE_ITEM "$id"
	want_eq "the item is memory" \
		"$(scalar "SELECT type FROM artifacts WHERE id = '$id'")" memory || return 1
	want_eq "the path decided the scope" \
		"$(scalar "SELECT visibility FROM artifacts WHERE id = '$id'")" personal || return 1
	want_eq "and the personal floor is a property of the row" \
		"$(scalar "SELECT coalesce(project, 'NULL') FROM artifacts WHERE id = '$id'")" NULL || return 1
	want_eq "the front matter became the title" \
		"$(scalar "SELECT title FROM artifacts WHERE id = '$id'")" \
		"the write-behind queue is the durability" || return 1
	want_eq "the tags came off the header" \
		"$(scalar "SELECT array_to_string(tags, ',') FROM artifacts WHERE id = '$id'")" \
		"phase7,fuse" || return 1
	want_eq "the row is signed, like every other write" \
		"$(scalar "SELECT sig IS NOT NULL FROM artifacts WHERE id = '$id'")" t || return 1
	want_eq "one event records the write" \
		"$(scalar "SELECT count(*) FROM events WHERE artifact = '$id'")" 1 || return 1
	want_eq "and it says what it was" \
		"$(scalar "SELECT type FROM events WHERE artifact = '$id'")" memory.write || return 1
	want_eq "the intent was applied" \
		"$(scalar "SELECT count(*) FROM fs_intents WHERE artifact = '$id' AND applied IS NOT NULL")" \
		1 || return 1
	printf 'decisions.md is memory item %s\n' "$id"
}

# Indexed, which is the whole claim: the tsvector is written by the same
# statement, so the agents that never mount anything find it.
fuse_write_is_indexed() {
	recall
	want_tool mem_search "$TOKEN_A" "{\"q\": \"$FUSE_WORD\"}" || return 1
	want_eq "mem_search finds the file" "$(tv .count)" 1 || return 1
	want_eq "and it is the right item" "$(tv '.items[0].id')" "$FUSE_ITEM" || return 1

	api GET "$TOKEN_A" "/api/search?q=$FUSE_WORD" || return 1
	want_eq "the API finds it too" "$(hits)" 1 || return 1
	want_eq "the same item" "$(jqv '.artifacts[0].id')" "$FUSE_ITEM" || return 1

	want_tool mem_read "$TOKEN_A" "{\"id\": \"$FUSE_ITEM\"}" || return 1
	want_eq "and mem_read has it" "$(tv .item.id)" "$FUSE_ITEM" || return 1
	printf 'a file in the mount is searchable memory: %s\n' "$FUSE_ITEM"
}

fuse_reads_back_what_was_written() {
	recall
	local body
	body="$(cat "$FUSE_MNT/_personal/$USER_A/memory/decisions.md")"
	case "$body" in
	*"$FUSE_WORD"*) ;;
	*)
		printf 'the file read back as:\n%s\n' "$body" >&2
		return 1
		;;
	esac
	case "$body" in
	*"scope: personal"*) ;;
	*)
		printf 'the header does not say what scope it is:\n%s\n' "$body" >&2
		return 1
		;;
	esac
	case "$body" in
	*"id: $FUSE_ITEM"*) ;;
	*)
		printf 'the header does not name the row:\n%s\n' "$body" >&2
		return 1
		;;
	esac
	# The name it was written under is the name it has, rather than the ULID of
	# the row - which is what the items written by the memory tool, in the same
	# directory, are called.
	local listed
	listed="$(ls "$FUSE_MNT/_personal/$USER_A/memory")"
	case "$listed" in
	*decisions.md*) ;;
	*)
		printf 'the directory holds %q, want decisions.md in it\n' "$listed" >&2
		return 1
		;;
	esac
	printf '%s bytes of header and body, listed as decisions.md\n' "${#body}"
}

# The mount is a view of the store and not a second copy of it: an item written
# by the memory tool, with no file involved, is a file.
fuse_shows_what_mem_write_wrote() {
	recall
	want_tool mem_write "$TOKEN_A" \
		'{"title": "written by the tool, not by a file", "body": "and yet here it is"}' || return 1
	local id path
	id="$(tv .item.id)"
	path="$FUSE_MNT/_personal/$USER_A/memory/$id.md"
	if [ ! -f "$path" ]; then
		printf 'mem_write item %s is not in the mount:\n%s\n' \
			"$id" "$(ls "$FUSE_MNT/_personal/$USER_A/memory")" >&2
		return 1
	fi
	case "$(cat "$path")" in
	*"and yet here it is"*) ;;
	*)
		printf 'the file for %s does not hold the item body\n' "$id" >&2
		return 1
		;;
	esac
	remember FUSE_TOOL_ITEM "$id"
	printf 'mem_write item %s reads as %s.md\n' "$id" "$id"
}

# A file in a project directory is that project's, and the header chooses
# between the two scopes that are that project. This one says shared, so the
# grant pb holds on pa reaches it - and the agent on the other side of that
# grant sees it without mounting anything at all.
fuse_project_scope_reaches_the_grant() {
	recall
	cat >"$FUSE_MNT/pa/$USER_A/memory/handoff.md" <<EOF
---
title: how the parser work is handed over
scope: shared
kind: handoff
---
Whoever picks it up should know about the $FUSE_SHARED_WORD.
EOF
	fuse_await "SELECT count(*) FROM artifacts
	             WHERE file_path = 'handoff.md' AND owner_user = '$USER_A'" >/dev/null || return 1

	local id
	id="$(scalar "SELECT id FROM artifacts WHERE file_path = 'handoff.md' AND owner_user = '$USER_A'")"
	remember FUSE_SHARED_ITEM "$id"
	want_eq "it lives in pa" \
		"$(scalar "SELECT project FROM artifacts WHERE id = '$id'")" pa || return 1
	want_eq "at the scope the header asked for" \
		"$(scalar "SELECT visibility FROM artifacts WHERE id = '$id'")" shared || return 1
	want_eq "with the kind the header asked for" \
		"$(scalar "SELECT kind FROM artifacts WHERE id = '$id'")" handoff || return 1

	want_tool mem_search "$TOKEN_B" "{\"q\": \"$FUSE_SHARED_WORD\"}" || return 1
	want_eq "B finds it across the grant" "$(tv .count)" 1 || return 1
	want_eq "and it is the file A wrote" "$(tv '.items[0].id')" "$id" || return 1
	printf 'handoff.md is shared item %s, and B reads it over the grant\n' "$id"
}

# A default in a project directory is the narrower of the two scopes, and the
# narrower one is the one a grant does not reach.
fuse_project_default_is_the_narrow_scope() {
	recall
	cat >"$FUSE_MNT/pa/$USER_A/memory/internal.md" <<'EOF'
---
title: for the project and nobody else
---
A quibblewrap stays in pa.
EOF
	fuse_await "SELECT count(*) FROM artifacts
	             WHERE file_path = 'internal.md' AND owner_user = '$USER_A'" >/dev/null || return 1
	local id
	id="$(scalar "SELECT id FROM artifacts WHERE file_path = 'internal.md' AND owner_user = '$USER_A'")"
	want_eq "the default scope in a project directory" \
		"$(scalar "SELECT visibility FROM artifacts WHERE id = '$id'")" project-only || return 1
	want_tool mem_search "$TOKEN_B" '{"q": "quibblewrap"}' || return 1
	want_eq "and the grant does not reach it" "$(tv .count)" 0 || return 1
	remember FUSE_NARROW_ITEM "$id"
	printf 'internal.md is project-only: %s\n' "$id"
}

# A second mount, of a second principal, is a second view of the same store -
# and it is the permission filter, not a copy of the rules.
fuse_second_principal_sees_only_what_it_may() {
	recall
	fuse_start "$FUSE_MNT_B" "$TOKEN_B" "$FUSE_LOG_B" "$FUSE_PID_FILE_B" || return 1

	local wrong=""
	if [ ! -f "$FUSE_MNT_B/pa/$USER_A/memory/handoff.md" ]; then
		wrong="the shared file is not in B's mount"
	fi
	if [ -f "$FUSE_MNT_B/pa/$USER_A/memory/internal.md" ]; then
		wrong="the project-only file is in B's mount"
	fi
	if [ -e "$FUSE_MNT_B/_personal/$USER_A" ]; then
		wrong="A's personal directory is in B's mount"
	fi
	if [ -f "$FUSE_MNT_B/pa/$USER_A/memory/decisions.md" ]; then
		wrong="A's personal file is in B's mount"
	fi
	# And B cannot write into A's directory: being able to read a file is not
	# being able to change it.
	if printf 'x\n' >"$FUSE_MNT_B/pa/$USER_A/memory/evil.md" 2>/dev/null; then
		wrong="B wrote a file into A's directory"
	fi
	fuse_stop "$FUSE_MNT_B" "$FUSE_PID_FILE_B" || return 1
	if [ -n "$wrong" ]; then
		printf '%s\n' "$wrong" >&2
		return 1
	fi
	printf "B's mount holds the shared file and nothing else of A's\n"
}

# The refusals, in one place. Every one of them is an errno on a real syscall.
fuse_refuses_what_it_should() {
	recall
	local failures="" out
	# A directory here is a scope, not something to create.
	if mkdir "$FUSE_MNT/invented" 2>/dev/null; then
		failures="$failures; mkdir at the root was allowed"
	fi
	# A project this token has no business in has no directory at all.
	if [ -d "$FUSE_MNT/pb" ]; then
		failures="$failures; project pb is in A's mount"
	fi
	# A dotfile is an editor's leavings, not a memory item.
	if printf 'x\n' >"$FUSE_MNT/_personal/$USER_A/memory/.swap.md" 2>/dev/null; then
		failures="$failures; a dotfile was accepted"
	fi
	# The floor cannot be left by saying so in the file - and the refusal is on
	# the close, which is where a write-behind filesystem has to put it. cp
	# rather than a redirection, because cp looks at what close said and the
	# shell does not.
	# shellcheck disable=SC2216  # cp /dev/stdin does read stdin, and it is what
	# looks at close(2) - see above
	if out="$(printf -- '---\ntitle: promote me\nscope: shared\n---\nbody\n' |
		cp /dev/stdin "$FUSE_MNT/_personal/$USER_A/memory/promote.md" 2>&1)"; then
		failures="$failures; a personal file asking for scope shared was accepted"
	fi
	case "$out" in
	*"failed to close"*) ;;
	*)
		failures="$failures; the promotion was refused somewhere other than the close: $out"
		;;
	esac
	if [ -n "$(psql -v ON_ERROR_STOP=1 -tAc \
		"SELECT id FROM artifacts WHERE file_path = 'promote.md'")" ]; then
		failures="$failures; the refused promotion was written anyway"
	fi

	if [ -n "$failures" ]; then
		printf '%s\n' "${failures#; }" >&2
		return 1
	fi
	printf 'mkdir, a project that is not this one, a dotfile and a promotion: all refused\n'
}

# The same bytes twice are one write. The queue is at-least-once and the store
# is deduped by hash, and this is the pair of them meeting.
fuse_the_same_bytes_are_one_write() {
	recall
	local before after
	before="$(scalar "SELECT count(*) FROM events WHERE artifact = '$FUSE_ITEM'")"
	cat >"$FUSE_MNT/_personal/$USER_A/memory/decisions.md" <<EOF
---
title: the write-behind queue is the durability
tags: phase7, fuse
---
A $FUSE_WORD is what we called it while we were building it.
EOF
	# Two intents now, and the second one is the duplicate: wait for it to come
	# off the queue rather than for a row that is not going to change.
	fuse_await "SELECT count(*) FROM fs_intents
	             WHERE artifact = '$FUSE_ITEM' AND applied IS NOT NULL
	            HAVING count(*) >= 2" >/dev/null || return 1
	after="$(scalar "SELECT count(*) FROM events WHERE artifact = '$FUSE_ITEM'")"
	want_eq "events after saving the same bytes again" "$after" "$before" || return 1
	want_eq "and still one artifact" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE id = '$FUSE_ITEM'")" 1 || return 1
	printf 'two intents, one write, %s event(s)\n' "$after"
}

# unlink is a delete. It is the one write the mount does inline, because a
# caller is entitled to be told whether the thing is gone.
fuse_unlink_tombstones_the_item() {
	recall
	rm "$FUSE_MNT/_personal/$USER_A/memory/decisions.md" || return 1
	want_eq "the row is tombstoned" \
		"$(scalar "SELECT tombstone FROM artifacts WHERE id = '$FUSE_ITEM'")" t || return 1
	if [ -e "$FUSE_MNT/_personal/$USER_A/memory/decisions.md" ]; then
		printf 'the file is still in the mount after the unlink\n' >&2
		return 1
	fi
	# Gone through every other door - and its owner, who could have read it, is
	# told it was WITHDRAWN rather than told it never was. Both doors say that,
	# and saying it in both is the point: one reader getting "410, withdrawn"
	# over HTTP and "no such memory item" over MCP is the ambiguity this is
	# supposed to end rather than a second place to have it.
	want_tool_fails mem_read "$TOKEN_A" "{\"id\": \"$FUSE_ITEM\"}" \
		"was withdrawn by $USER_A" || return 1
	want_status 410 GET "$TOKEN_A" "/api/artifact/$FUSE_ITEM" || return 1
	# And B, who never could read a personal row of A's, gets the words an id
	# nobody ever wrote gets - at both doors, for the same reason.
	want_tool_fails mem_read "$TOKEN_B" "{\"id\": \"$FUSE_ITEM\"}" "no such memory item" || return 1
	want_status 404 GET "$TOKEN_B" "/api/artifact/$FUSE_ITEM" || return 1
	want_tool mem_search "$TOKEN_A" "{\"q\": \"$FUSE_WORD\"}" || return 1
	want_eq "and it is out of the index" "$(tv .count)" 0 || return 1
	printf '%s is gone from the mount, the store and the search\n' "$FUSE_ITEM"
}

# The mount is stateless: unmount, mount again, and the files are the files,
# because they were never anywhere but the store.
fuse_remount_shows_the_same_items() {
	recall
	fuse_stop "$FUSE_MNT" "$FUSE_PID_FILE" || return 1
	if [ -n "$(ls -A "$FUSE_MNT")" ]; then
		printf 'the mountpoint is not empty after the unmount: %s\n' "$(ls -A "$FUSE_MNT")" >&2
		return 1
	fi
	fuse_start "$FUSE_MNT" "$TOKEN_A" "$FUSE_LOG" "$FUSE_PID_FILE" || return 1

	if [ ! -f "$FUSE_MNT/pa/$USER_A/memory/handoff.md" ]; then
		printf 'the shared file is not in the remount:\n%s\n' "$(find "$FUSE_MNT" -type f)" >&2
		return 1
	fi
	case "$(cat "$FUSE_MNT/pa/$USER_A/memory/handoff.md")" in
	*"$FUSE_SHARED_WORD"*) ;;
	*)
		printf 'the remounted file does not hold what was written to it\n' >&2
		return 1
		;;
	esac
	if [ -e "$FUSE_MNT/_personal/$USER_A/memory/decisions.md" ]; then
		printf 'the deleted file came back with the remount\n' >&2
		return 1
	fi
	# The item written by mem_write is still a file, and the one written as a
	# file is still an item: one store, two doors.
	if [ ! -f "$FUSE_MNT/_personal/$USER_A/memory/$FUSE_TOOL_ITEM.md" ]; then
		printf 'the tool-written item is not in the remount\n' >&2
		return 1
	fi
	printf 'the same items are there, and the deleted one is not\n'
}

# Deleting a file whose write is still on the queue takes the write off the
# queue. Nothing else would: the intent names a row that does not exist yet, so
# there is no tombstone for the apply to refuse against, and the item the caller
# just deleted would appear a second later.
fuse_deleting_a_queued_write_takes_it_off_the_queue() {
	recall
	fuse_stop "$FUSE_MNT" "$FUSE_PID_FILE" || return 1
	fuse_start "$FUSE_MNT" "$TOKEN_A" "$FUSE_LOG" "$FUSE_PID_FILE" --no-drain || return 1

	cat >"$FUSE_MNT/_personal/$USER_A/memory/doomed.md" <<'EOF'
---
title: written and then deleted before it was ever stored
---
A blatherfen nobody should ever find.
EOF
	want_eq "it is on the queue" "$(scalar "SELECT count(*) FROM fs_intents WHERE name = 'doomed.md' AND applied IS NULL")" 1 || return 1
	rm "$FUSE_MNT/_personal/$USER_A/memory/doomed.md" || return 1
	want_eq "and the delete took it off" "$(scalar "SELECT count(*) FROM fs_intents WHERE name = 'doomed.md' AND applied IS NULL")" 0 || return 1

	fuse_stop "$FUSE_MNT" "$FUSE_PID_FILE" || return 1
	local out
	out="$(DATABASE_URL="$DATABASE_URL" FLOWY_NODE=gate ./flowy fuse --reconcile 2>&1)" || {
		printf '%s\n' "$out" >&2
		return 1
	}
	want_eq "and a reconcile does not bring it back" "$(scalar "SELECT count(*) FROM artifacts WHERE file_path = 'doomed.md'")" 0 || return 1
	want_tool mem_search "$TOKEN_A" '{"q": "blatherfen"}' || return 1
	want_eq "there is nothing to find" "$(tv .count)" 0 || return 1
	printf 'the queued write was cancelled by the unlink: %s\n' "$out"
}

# The crash. A mount with no drainer queues the write and never applies it,
# which is the state a node that died between the close and the store write
# comes back from - and here it really is killed, with SIGKILL, so nothing it
# might have done on the way out can be doing the work.
fuse_crash_between_the_close_and_the_write() {
	recall
	fuse_start "$FUSE_MNT" "$TOKEN_A" "$FUSE_LOG" "$FUSE_PID_FILE" --no-drain || return 1

	cat >"$FUSE_MNT/_personal/$USER_A/memory/crash.md" <<EOF
---
title: closed before the crash
---
The $FUSE_CRASH_WORD was written and the node died.
EOF
	# The file reads back through the mount before the store has it: the write
	# is behind, the file is not.
	case "$(cat "$FUSE_MNT/_personal/$USER_A/memory/crash.md")" in
	*"$FUSE_CRASH_WORD"*) ;;
	*)
		printf 'the queued file does not read back through the mount\n' >&2
		return 1
		;;
	esac
	want_eq "the intent is on the queue" \
		"$(scalar "SELECT count(*) FROM fs_intents WHERE name = 'crash.md' AND applied IS NULL")" \
		1 || return 1
	want_eq "and the store has nothing" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE file_path = 'crash.md'")" 0 || return 1

	local id
	id="$(scalar "SELECT artifact FROM fs_intents WHERE name = 'crash.md' AND applied IS NULL")"
	remember FUSE_CRASH_ITEM "$id"

	fuse_kill "$FUSE_MNT" "$FUSE_PID_FILE"
	want_eq "the store still has nothing after the kill" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE id = '$id'")" 0 || return 1
	want_eq "and the intent is still pending" \
		"$(scalar "SELECT count(*) FROM fs_intents WHERE artifact = '$id' AND applied IS NULL")" \
		1 || return 1
	printf 'killed with crash.md queued and nothing written: %s\n' "$id"
}

# The replay. Reconcile applies what the crash left, exactly once - and doing it
# again does nothing at all, which is the difference between at-least-once
# delivery and at-least-once writing.
fuse_reconcile_replays_exactly_once() {
	recall
	local out again clock
	out="$(DATABASE_URL="$DATABASE_URL" FLOWY_NODE=gate ./flowy fuse --reconcile 2>&1)" || {
		printf '%s\n' "$out" >&2
		return 1
	}
	want_eq "one artifact" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE id = '$FUSE_CRASH_ITEM'")" 1 || return 1
	want_eq "not a partial one" \
		"$(scalar "SELECT title FROM artifacts WHERE id = '$FUSE_CRASH_ITEM'")" \
		"closed before the crash" || return 1
	want_eq "one event" \
		"$(scalar "SELECT count(*) FROM events WHERE artifact = '$FUSE_CRASH_ITEM'")" 1 || return 1
	want_eq "signed like any other write" \
		"$(scalar "SELECT sig IS NOT NULL FROM artifacts WHERE id = '$FUSE_CRASH_ITEM'")" t || return 1

	clock="$(scalar "SELECT hlc FROM artifacts WHERE id = '$FUSE_CRASH_ITEM'")"
	again="$(DATABASE_URL="$DATABASE_URL" FLOWY_NODE=gate ./flowy fuse --reconcile 2>&1)" || {
		printf '%s\n' "$again" >&2
		return 1
	}
	want_eq "the second reconcile applies nothing" \
		"$(scalar "SELECT count(*) FROM events WHERE artifact = '$FUSE_CRASH_ITEM'")" 1 || return 1
	want_eq "and does not rewrite the row" \
		"$(scalar "SELECT hlc FROM artifacts WHERE id = '$FUSE_CRASH_ITEM'")" "$clock" || return 1
	want_eq "nothing is left on the queue" \
		"$(scalar "SELECT count(*) FROM fs_intents WHERE applied IS NULL")" 0 || return 1

	# And it is indexed, from a replay as much as from a live write.
	want_tool mem_search "$TOKEN_A" "{\"q\": \"$FUSE_CRASH_WORD\"}" || return 1
	want_eq "the replayed item is searchable" "$(tv .count)" 1 || return 1
	printf '%s / %s\n' "$out" "$again"
}

# And with everything unmounted, the node is the node it was before any of this:
# the memory tools and the API read and write the same store, including the
# items that arrived as files.
fuse_off_again_and_everything_still_works() {
	recall
	fuse_stop "$FUSE_MNT" "$FUSE_PID_FILE" || return 1
	if [ -n "$(fuse_mounts_here)" ]; then
		printf 'a fuse filesystem is still attached at the end of the section:\n%s\n' \
			"$(fuse_mounts_here)" >&2
		return 1
	fi

	want_tool mem_write "$TOKEN_A" \
		'{"title": "written after the mount went away", "body": "a wobblesprocket"}' || return 1
	local id
	id="$(tv .item.id)"
	want_tool mem_search "$TOKEN_A" '{"q": "wobblesprocket"}' || return 1
	want_eq "memory still writes and searches with nothing mounted" "$(tv .count)" 1 || return 1

	# The items that came in as files are still there, still readable by the
	# tools, still searchable - they were always rows.
	want_tool mem_read "$TOKEN_A" "{\"id\": \"$FUSE_CRASH_ITEM\"}" || return 1
	want_eq "the replayed file is still an item" "$(tv .item.id)" "$FUSE_CRASH_ITEM" || return 1
	api GET "$TOKEN_A" "/api/artifact/$FUSE_SHARED_ITEM" || return 1
	want_eq "and the shared one answers the API" "$API_STATUS" 200 || return 1
	want_eq "with the scope its path gave it" "$(jqv .visibility)" shared || return 1
	printf 'no mount, and memory item %s written, %s and %s read\n' \
		"$id" "$FUSE_CRASH_ITEM" "$FUSE_SHARED_ITEM"
}

say "fuse: memory as files, and only if you ask"
check "there is a /dev/fuse and a fusermount here" fuse_is_available
check "nothing is mounted, and memory works anyway" fuse_off_is_the_default
check "flowy fuse mounts, and says what it negotiated" fuse_mounts
check "a file written into the mount is an item in the store" fuse_write_lands_in_the_store
check "and mem_search and the API find it" fuse_write_is_indexed
check "and it reads back through the mount" fuse_reads_back_what_was_written
check "an item written by mem_write is a file" fuse_shows_what_mem_write_wrote
check "a file in a project directory carries the scope its header asked for" \
	fuse_project_scope_reaches_the_grant
check "and the default in a project is the scope a grant does not reach" \
	fuse_project_default_is_the_narrow_scope
check "a second principal's mount holds what that principal may read" \
	fuse_second_principal_sees_only_what_it_may
check "a scope, a directory and a name that are not allowed are refused" \
	fuse_refuses_what_it_should
check "saving the same bytes again is not a second write" fuse_the_same_bytes_are_one_write
check "unlink tombstones the item, everywhere" fuse_unlink_tombstones_the_item
check "unmount and mount again: the same items, from the store" \
	fuse_remount_shows_the_same_items
check "deleting a file whose write is still queued takes the write off the queue" \
	fuse_deleting_a_queued_write_takes_it_off_the_queue
check "a kill between the close and the store write leaves the intent pending" \
	fuse_crash_between_the_close_and_the_write
check "reconcile replays it exactly once, and again is nothing" \
	fuse_reconcile_replays_exactly_once
check "with nothing mounted, memory is memory" fuse_off_again_and_everything_still_works

# ---------------------------------------------------------------- phase 8
#
# Observability: what the fabric measured about itself, what it saw itself do,
# and what it will not claim. The three things being tested are the three the
# architecture asks for - a metric is filtered like a read, a verdict is refused
# where there is not enough history to draw one, and one handoff across two
# nodes is one trace.

# ----------------------------------------------------------- phase 8 helpers
#
# Observability. The claims being tested are not "the endpoint answers" - they
# are that what it answers is filtered, that what it cannot measure it refuses
# to guess at, and that one handoff across two nodes is one trace. So most of
# these read a number as one principal and then read the same number as another,
# and a few of them go behind the API to psql: a span is this node's own account
# of what it did, and the only way to know that two nodes agree about a trace id
# is to look in both databases.

# apih METHOD TOKEN PATH [BODY] - api(), keeping the response headers too. The
# trace the node ran the request in lands in API_TRACE, which is what makes a
# trace assertable from outside: the node hands it back on every response.
apih() {
	local method=$1 token=$2 path=$3 body=${4-}
	local -a curl_args=(--silent --show-error -D "$WORK/headers" -X "$method" -w '\n%{http_code}')
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
	API_TRACE="$(tr -d '\r' <"$WORK/headers" | sed -n 's/^[Tt]race-[Ii]d: //p' | tail -n 1)"
}

# napih PORT METHOD TOKEN PATH [BODY] - apih against one of the federated nodes.
napih() {
	local port=$1
	shift
	HTTP_PORT="$port"
	apih "$@"
}

# metrics TOKEN [QUERY] - GET /api/metrics as one principal.
metrics() {
	api GET "$1" "/api/metrics${2-}" || return 1
	if [ "$API_STATUS" != "200" ]; then
		printf 'GET /api/metrics answered %s: %s\n' "$API_STATUS" "$API_BODY" >&2
		return 1
	fi
}

# want_at_least WHAT GOT FLOOR
want_at_least() {
	if [ "$(printf '%s' "$2" | tr -dc '0-9-')" = "" ] || [ "$2" -lt "$3" ]; then
		printf '%s is %q, want at least %s\n' "$1" "$2" "$3" >&2
		return 1
	fi
}

# await_count DSN SQL MIN - a span is written when the operation it describes
# has finished, which is after the response the client is reading. So the
# assertions poll rather than assume: ten seconds, a tenth of a second apart.
await_count() {
	local dsn=$1 query=$2 want=$3 i n
	for i in $(seq 1 100); do
		n="$(psql -v ON_ERROR_STOP=1 -tA -d "$dsn" -c "$query")" || return 1
		if [ -n "$n" ] && [ "$n" -ge "$want" ]; then
			printf '%s\n' "$n"
			return 0
		fi
		sleep 0.1
	done
	printf 'never reached %s within ten seconds:\n%s\n' "$want" "$query" >&2
	return 1
}

# await_spans TOKEN TRACE MIN - the same wait, over the API: poll GET
# /api/trace/{id} until the trace holds at least MIN spans.
await_spans() {
	local token=$1 trace=$2 want=$3 i n
	for i in $(seq 1 100); do
		api GET "$token" "/api/trace/$trace" || return 1
		n="$(jqv '.trace.spans | length')"
		if [ -n "$n" ] && [ "$n" -ge "$want" ]; then
			return 0
		fi
		sleep 0.1
	done
	printf 'trace %s never held %s spans:\n%s\n' "$trace" "$want" "$API_BODY" >&2
	return 1
}

# post_activity TOKEN KIND BODY [WHERE_JSON] - one write into the timeline.
post_activity() {
	local token=$1 kind=$2 body=$3 where=${4-'{"room":"runs"}'}
	local payload
	payload="$(jq -nc --arg k "$kind" --arg b "$body" --argjson w "$where" \
		'$w + {kind: $k, body: $b}')" || return 1
	want_status 200 POST "$token" /api/activity "$payload"
}

# ------------------------------------------------------------- phase 8 checks

# Every group is in the answer, every time - including the ones this principal
# may not read. A group that is simply absent is indistinguishable from a group
# that measured nothing.
metrics_answers_every_group() {
	recall
	metrics "$TOKEN_A" || return 1
	local group
	for group in node corpus sync collaboration permissions anomalies; do
		if [ "$(jqv ".groups | has(\"$group\")")" != "true" ]; then
			printf 'the %s group is missing from the answer\n' "$group" >&2
			return 1
		fi
		if [ "$(jqv ".groups.$group.available")" = "null" ]; then
			printf 'the %s group does not say whether it was measured\n' "$group" >&2
			return 1
		fi
	done
	want_eq "whose numbers these are" "$(jqv .scope.user)" "$USER_A" || return 1
	want_eq "and which project" "$(jqv .scope.project)" pa || return 1
	printf 'six groups, scope %s\n' "$(jqv .scope.key)"
}

# The security property: a metric is an aggregate, and an aggregate over rows
# somebody may not read tells them how many there are. So the number has to be
# exactly what that principal may list, and a row that is out of their reach has
# to be out of their total.
#
# "another project's numbers" is not the same as "any row with another project
# on it": B holds a read grant on pa, so some of pa is legitimately B's to count.
# What is not is a row the grant does not reach - which is what project-only
# means, and what the assertion below is written on.
metrics_are_scope_filtered() {
	recall
	local a_total b_total a_list b_list
	metrics "$TOKEN_A" || return 1
	a_total="$(jqv .groups.corpus.artifacts)"
	api GET "$TOKEN_A" '/api/artifacts?limit=1000' || return 1
	a_list="$(printf '%s' "$API_BODY" | jq '.artifacts | length')"
	want_eq "A's corpus is what A may list" "$a_total" "$a_list" || return 1

	metrics "$TOKEN_B" || return 1
	want_eq "B's scope" "$(jqv .scope.project)" pb || return 1
	b_total="$(jqv .groups.corpus.artifacts)"
	api GET "$TOKEN_B" '/api/artifacts?limit=1000' || return 1
	b_list="$(printf '%s' "$API_BODY" | jq '.artifacts | length')"
	want_eq "B's corpus is what B may list" "$b_total" "$b_list" || return 1

	# A row in pa that no grant reaches: A's project's, and nobody else's.
	want_status 200 POST "$TOKEN_A" /api/artifacts \
		'{"type":"note","title":"a project-only counting note","visibility":"project-only"}' || return 1
	metrics "$TOKEN_A" || return 1
	want_eq "A's total went up by one" "$(jqv .groups.corpus.artifacts)" "$((a_total + 1))" || return 1
	metrics "$TOKEN_B" || return 1
	want_eq "and B's did not move" "$(jqv .groups.corpus.artifacts)" "$b_total" || return 1
	printf 'A counts %s (what A may list), B counts %s; the project-only row is A alone\n' \
		"$((a_total + 1))" "$b_total"
}

# A principal of one project cannot read another project's numbers even by
# asking for them: ?scope=all is the operator's, and for anybody else it is
# their own view under their own scope key.
a_stranger_cannot_read_another_projects_metrics() {
	recall
	metrics "$TOKEN_B" || return 1
	local own
	own="$(jqv .groups.corpus.artifacts)"
	metrics "$TOKEN_B" '?scope=all' || return 1
	want_eq "scope=all is not for B" "$(jqv .scope.all)" false || return 1
	want_eq "B's key is still B's" "$(jqv .scope.key)" "user:$USER_B|project:pb" || return 1
	want_eq "and the count did not widen" "$(jqv .groups.corpus.artifacts)" "$own" || return 1
	# The node's own health is not B's to read, and it says so rather than
	# answering zero.
	want_eq "node health for B" "$(jqv .groups.node.available)" false || return 1
	if [ -z "$(jqv '.groups.node.reason // ""')" ]; then
		printf 'the node group was withheld with no reason given\n' >&2
		return 1
	fi
	want_eq "and no uptime came with it" "$(jqv '.groups.node | has("uptime_s")')" false || return 1
	want_eq "replication cursors for B" "$(jqv .groups.sync.available)" false || return 1
	printf 'B asked for the node and was told: %s\n' "$(jqv .groups.node.reason)"
}

# A personal artifact is its owner's, and the count is too. It is the one that
# would leak most quietly: nobody expects a total to be a disclosure.
personal_artifacts_count_only_for_their_owner() {
	recall
	metrics "$TOKEN_B" || return 1
	local before after mine
	before="$(jqv .groups.corpus.artifacts)"
	metrics "$TOKEN_A" || return 1
	mine="$(jqv .groups.corpus.artifacts)"

	want_status 200 POST "$TOKEN_A" /api/artifacts \
		'{"type":"note","title":"a private counting note","visibility":"personal"}' || return 1

	metrics "$TOKEN_A" || return 1
	want_eq "A's count went up by one" "$(jqv .groups.corpus.artifacts)" "$((mine + 1))" || return 1
	want_at_least "A's personal bucket" "$(jqv '.groups.corpus.by_scope.personal // 0')" 1 || return 1
	metrics "$TOKEN_B" || return 1
	want_eq "B's count did not move" "$(jqv .groups.corpus.artifacts)" "$before" || return 1
	after="$(jqv '.groups.corpus.by_scope.personal // 0')"
	printf "A: %s -> %s artifacts; B: %s, personal bucket %s\n" \
		"$mine" "$((mine + 1))" "$before" "$after"
}

# The operator's ?scope=all is the node: the health of the machine, the peers,
# the bytes on disk, and a corpus that includes what no single principal can
# read.
the_operator_scope_all_sees_the_node() {
	recall
	metrics "$TOKEN_A" || return 1
	local a_artifacts
	a_artifacts="$(jqv .groups.corpus.artifacts)"

	metrics "$TOKEN_OP" '?scope=all' || return 1
	want_eq "scope=all for the operator" "$(jqv .scope.all)" true || return 1
	want_eq "the key says so" "$(jqv .scope.key)" "node:all" || return 1
	want_eq "node health is measured" "$(jqv .groups.node.available)" true || return 1
	want_eq "the store answers" "$(jqv .groups.node.db.up)" true || return 1
	want_at_least "the node holds at least what A can read" \
		"$(jqv .groups.corpus.artifacts)" "$a_artifacts" || return 1

	# The denominator is named, which is the whole of the CPU number meaning
	# anything on a machine with more than one core.
	case "$(jqv .groups.node.cpu.of)" in
	*"one core"*) ;;
	*)
		printf 'the cpu share does not name its denominator: %q\n' "$(jqv .groups.node.cpu.of)" >&2
		return 1
		;;
	esac
	want_at_least "cores reported" "$(jqv .groups.node.cpu.cores)" 1 || return 1
	want_at_least "resident bytes" "$(jqv .groups.node.memory.rss_bytes)" 1 || return 1
	want_at_least "the pool's ceiling" "$(jqv .groups.node.pool.max_open)" 1 || return 1
	want_eq "bytes on disk are the operator's" "$(jqv .groups.corpus.storage.available)" true || return 1
	printf 'node: %ss up, %s of one core over %ss, %s bytes resident, %s artifacts\n' \
		"$(jqv '.groups.node.uptime_s | floor')" "$(jqv .groups.node.cpu.core_share)" \
		"$(jqv '.groups.node.cpu.window_s | floor')" "$(jqv .groups.node.memory.rss_bytes)" \
		"$(jqv .groups.corpus.artifacts)"
}

# What is not measured says so. The pull side of replication is the honest case:
# what a peer holds above our cursor is that peer's high water mark, and this
# node has not asked it.
what_was_not_measured_is_not_a_zero() {
	recall
	metrics "$TOKEN_OP" '?scope=all' || return 1
	want_eq "pending pull is not measured" "$(jqv .groups.sync.pending_pull.available)" false || return 1
	if [ -z "$(jqv '.groups.sync.pending_pull.reason // ""')" ]; then
		printf 'pending pull is unavailable and gives no reason\n' >&2
		return 1
	fi
	# And the coverage number that does not exist yet: no vector index here, so
	# embedded is zero and is reported as zero of a named denominator rather
	# than as a share of an index nothing built.
	want_eq "embeddings" "$(jqv .groups.corpus.embedding.embedded)" 0 || return 1
	want_at_least "text-indexed rows" "$(jqv .groups.corpus.embedding.bm25_only)" 1 || return 1
	printf 'pending pull: %s\n' "$(jqv .groups.sync.pending_pull.reason)"
}

# The serenedash rule, and the one this whole group exists for: below the
# minimum sample count there is no verdict. It says how many readings it has and
# how many it needs, and it does not print a baseline somebody would read as the
# finding.
anomalies_refuse_a_verdict_below_the_minimum() {
	recall
	metrics "$TOKEN_A" || return 1
	want_eq "the anomaly pass ran" "$(jqv .groups.anomalies.available)" true || return 1
	want_at_least "series watched" "$(jqv '.groups.anomalies.series | length')" 1 || return 1
	want_at_least "the minimum it needs" "$(jqv .groups.anomalies.min_samples)" 2 || return 1

	local refused
	refused="$(jqv '[.groups.anomalies.series[] | select(.verdict == "insufficient samples")] | length')"
	want_at_least "series that refused a verdict" "$refused" 1 || return 1
	# Every refusal says what it has and what it needs, and carries no baseline.
	if [ "$(jqv '[.groups.anomalies.series[]
	              | select(.verdict == "insufficient samples")
	              | select((.reason // "") == "" or (.baseline // 0) != 0)] | length')" != "0" ]; then
		printf 'a refusal came with a baseline or without a reason:\n%s\n' \
			"$(jqv .groups.anomalies.series)" >&2
		return 1
	fi
	# And it is a refusal, not a number: nothing in the series claims normality
	# on no evidence.
	printf '%s of %s series refused: %s\n' "$refused" \
		"$(jqv '.groups.anomalies.series | length')" \
		"$(jqv '.groups.anomalies.series[0].reason')"
}

# With a history recorded, the verdict is a distance from what this node has
# actually seen - not a threshold anybody chose. The readings are inserted
# straight into the local table, which is where the node's own readings go.
anomalies_judge_against_recorded_history() {
	recall
	local key series
	key="user:$USER_A|project:pa"
	series="corpus.artifacts"
	psql -v ON_ERROR_STOP=1 -q -c \
		"INSERT INTO metric_samples (id, scope, series, value, at)
		 SELECT 'hist-' || g, '$key', '$series', 1, now() - (g || ' minutes')::interval
		   FROM generate_series(1, 12) g" || return 1

	metrics "$TOKEN_A" || return 1
	local verdict reason samples
	verdict="$(jqv ".groups.anomalies.series[] | select(.series == \"$series\") | .verdict")"
	reason="$(jqv ".groups.anomalies.series[] | select(.series == \"$series\") | .reason")"
	samples="$(jqv ".groups.anomalies.series[] | select(.series == \"$series\") | .samples")"
	want_at_least "readings behind the verdict" "$samples" 12 || return 1
	want_eq "the verdict" "$verdict" unusual || return 1
	if [ -z "$reason" ] || [ "$reason" = "null" ]; then
		printf 'a verdict was drawn with nothing said about what it rests on\n' >&2
		return 1
	fi
	# The history is per scope: B's numbers are not judged against A's.
	metrics "$TOKEN_B" || return 1
	want_eq "B's verdict for the same series" \
		"$(jqv ".groups.anomalies.series[] | select(.series == \"$series\") | .verdict")" \
		"insufficient samples" || return 1
	printf 'A: %s (%s readings) - %s; B: still insufficient\n' "$verdict" "$samples" "$reason"
}

# A refusal is counted, for the principal it was given to, and it names no row.
refusals_are_counted_for_whoever_was_refused() {
	recall
	metrics "$TOKEN_B" || return 1
	local before
	before="$(jqv .groups.permissions.denied_24h)"

	# The peers list is the operator's view, so B is refused it.
	want_status 403 GET "$TOKEN_B" /api/peers || return 1
	# And a token that is not a token at all.
	want_status 401 GET "" /api/artifacts || return 1

	metrics "$TOKEN_B" || return 1
	local after
	after="$(jqv .groups.permissions.denied_24h)"
	want_at_least "B's refusals" "$after" "$((before + 1))" || return 1
	want_at_least "counted under 403" "$(jqv '.groups.permissions.denied_by_status["403"] // 0')" 1 || return 1
	# The unauthenticated one has no principal, so it is the operator's to see
	# and nobody else's.
	metrics "$TOKEN_OP" '?scope=all' || return 1
	want_at_least "the node's refusals include the 401" \
		"$(jqv '.groups.permissions.denied_by_status["401"] // 0')" 1 || return 1
	printf "B was refused %s -> %s time(s) in 24h; the node counted a 401 as well\n" \
		"$before" "$after"
}

# The Prometheus endpoint is the same numbers behind the same token, and it
# writes down which groups it could not read rather than leaving them out.
prometheus_text_is_the_same_measurements() {
	recall
	local out
	out="$(curl --silent --show-error -H "Authorization: Bearer $TOKEN_OP" \
		-w '\n%{http_code} %{content_type}' \
		"http://127.0.0.1:$HTTP_PORT/metrics?scope=all")" || return 1
	local tail="${out##*$'\n'}" body="${out%$'\n'*}"
	want_eq "status" "${tail%% *}" 200 || return 1
	case "${tail#* }" in
	text/plain*) ;;
	*)
		printf 'the scrape came back as %q\n' "${tail#* }" >&2
		return 1
		;;
	esac
	local name
	for name in flowy_artifacts flowy_group_available flowy_cpu_core_share flowy_denied_24h; do
		if ! printf '%s' "$body" | grep -q "^$name{"; then
			printf 'the scrape has no %s series:\n%s\n' "$name" "$body" >&2
			return 1
		fi
	done
	# One HELP per family, whatever the labels: a scrape with two is a scrape a
	# scraper rejects.
	local dupes
	dupes="$(printf '%s' "$body" | grep '^# HELP ' | awk '{print $3}' | sort | uniq -d)"
	if [ -n "$dupes" ]; then
		printf 'these families are declared twice: %s\n' "$dupes" >&2
		return 1
	fi
	# The scope is a label, so two tokens scraping this node are two series.
	if ! printf '%s' "$body" | grep -q 'scope="node:all"'; then
		printf 'the scrape does not say whose numbers it is:\n%s\n' "$body" >&2
		return 1
	fi
	printf '%s series over %s families\n' \
		"$(printf '%s' "$body" | grep -c '^flowy_')" \
		"$(printf '%s' "$body" | grep -c '^# HELP ')"
}

# A browser following the link to /metrics gets the console, because a
# navigation carries no Authorization header and a scrape does.
the_metrics_path_is_a_page_as_well() {
	www /metrics || return 1
	want_eq "status" "$WWW_STATUS" 200 || return 1
	case "$WWW_TYPE" in
	text/html*) ;;
	*)
		printf 'an untokened GET /metrics came back as %q\n' "$WWW_TYPE" >&2
		return 1
		;;
	esac
	printf 'no token: %s bytes of console\n' "${#WWW_BODY}"
}

# The timeline indexes all four kinds, in log order, and says which is which.
the_timeline_indexes_every_kind() {
	recall
	post_activity "$TOKEN_A" turn "a turn: read the gearbox bug" || return 1
	local turn
	turn="$(jqv .id)"
	post_activity "$TOKEN_A" log "fc run-log: exit status 0, 12s" || return 1
	post_activity "$TOKEN_A" chat "and a message about it" || return 1
	post_activity "$TOKEN_A" steer "vm_say: try the other approach" || return 1

	api GET "$TOKEN_A" '/api/activity?room=runs' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	local kind
	for kind in turn log chat steer; do
		want_at_least "$kind items" \
			"$(printf '%s' "$API_BODY" | jq "[.items[] | select(.kind == \"$kind\")] | length")" 1 ||
			return 1
	done
	# In order, by the same cursor everything else here pages by.
	if [ "$(printf '%s' "$API_BODY" | jq '[.items[].seq_hlc] | . == sort')" != "true" ]; then
		printf 'the timeline is not in log order:\n%s\n' \
			"$(printf '%s' "$API_BODY" | jq -c '[.items[] | {kind, seq_hlc}]')" >&2
		return 1
	fi
	want_at_least "the cursor moved" "$(jqv .cursor)" 1 || return 1
	remember N8_TURN "$turn"
	remember N8_THREAD "$(printf '%s' "$API_BODY" | jq -r '.items[0].thread')"
	printf '%s items, kinds [%s]\n' "$(printf '%s' "$API_BODY" | jq '.items | length')" \
		"$(printf '%s' "$API_BODY" | jq -r '[.items[].kind] | unique | join(", ")')"
}

# It is searchable, and searching it is a narrowing of the same filtered read
# rather than a second index with rules of its own.
the_timeline_is_searchable() {
	recall
	api GET "$TOKEN_A" '/api/activity?q=gearbox' || return 1
	want_at_least "hits for a word in one item" \
		"$(printf '%s' "$API_BODY" | jq '.items | length')" 1 || return 1
	if [ "$(printf '%s' "$API_BODY" | jq '[.items[] | select(.body | test("gearbox"; "i") | not)] | length')" != "0" ]; then
		printf 'the search returned something that does not contain the word\n' >&2
		return 1
	fi
	api GET "$TOKEN_A" '/api/activity?q=nothing-ever-said-this' || return 1
	want_eq "a word nobody wrote" "$(printf '%s' "$API_BODY" | jq '.items | length')" 0 || return 1
	api GET "$TOKEN_A" '/api/activity?kind=steer' || return 1
	if [ "$(printf '%s' "$API_BODY" | jq '[.items[] | select(.kind != "steer")] | length')" != "0" ]; then
		printf 'the kind filter let something else through\n' >&2
		return 1
	fi
	want_status 400 GET "$TOKEN_A" '/api/activity?kind=status' || return 1
	printf 'searched, narrowed, and a kind nobody may post was refused\n'
}

# And it is filtered. The grant runs pb -> pa, so some of A's project is
# legitimately B's to read; nothing of B's is A's. So the assertion is written
# the way round the permission model actually promises: B posts a run of its
# own, and A - who holds no grant into pb - sees none of it.
the_timeline_is_scope_filtered() {
	recall
	post_activity "$TOKEN_B" log "fc run-log: b-side only, pb" '{"room":"runs"}' || return 1
	local said
	said="$(jqv .id)"

	api GET "$TOKEN_B" '/api/activity?q=b-side' || return 1
	want_at_least "B's own run is on B's timeline" \
		"$(printf '%s' "$API_BODY" | jq "[.items[] | select(.id == \"$said\")] | length")" 1 || return 1

	api GET "$TOKEN_A" '/api/activity?q=b-side' || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "what A sees of B's runs" "$(printf '%s' "$API_BODY" | jq '.items | length')" 0 || return 1
	api GET "$TOKEN_A" '/api/activity?room=runs' || return 1
	if [ "$(printf '%s' "$API_BODY" | jq "[.items[] | select(.id == \"$said\")] | length")" != "0" ]; then
		printf "B's run line is on A's timeline\n" >&2
		return 1
	fi
	remember N8_B_ITEM "$said"
	printf "A holds nothing of B's runs; B holds %s\n" "$said"
}

# The message box is everywhere: you post into a run, a branch of one, or a
# room, from the same place you were reading.
the_timeline_is_postable_into() {
	recall
	post_activity "$TOKEN_A" steer "into the run itself" \
		"{\"thread\": \"$N8_THREAD\", \"room\": \"runs\"}" || return 1
	local said
	said="$(jqv .id)"
	want_eq "it landed in the run" "$(jqv .thread)" "$N8_THREAD" || return 1
	want_eq "as a steer" "$(jqv .kind)" steer || return 1
	want_eq "said by A" "$(jqv .actor_user)" "$USER_A" || return 1

	api GET "$TOKEN_A" "/api/activity?thread=$N8_THREAD" || return 1
	want_at_least "the run now holds it" \
		"$(printf '%s' "$API_BODY" | jq "[.items[] | select(.id == \"$said\")] | length")" 1 || return 1
	# Saying where is required: a message with no destination is not a message.
	want_status 400 POST "$TOKEN_A" /api/activity '{"kind":"steer","body":"nowhere"}' || return 1
	printf 'posted %s into run %s\n' "$said" "$N8_THREAD"
}

# A request is a trace: the permission check that decided who was asking, and
# the queries that answered under it.
a_request_is_a_trace() {
	recall
	apih GET "$TOKEN_A" /api/artifacts || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	if [ -z "$API_TRACE" ]; then
		printf 'the node answered without saying which trace it ran in\n' >&2
		return 1
	fi
	local trace="$API_TRACE"
	# The root span ends after the response is written, so the trace is asked
	# for until it is there rather than once, immediately.
	await_spans "$TOKEN_A" "$trace" 3 || return 1
	want_eq "the trace" "$(jqv .trace.trace_id)" "$trace" || return 1
	local names
	names="$(jqv '[.trace.spans[].name] | join(",")')"
	case "$names" in
	*principal.resolve*) ;;
	*)
		printf 'the trace has no permission check in it: %s\n' "$names" >&2
		return 1
		;;
	esac
	case "$names" in
	*artifacts.list*) ;;
	*)
		printf 'the trace has no query in it: %s\n' "$names" >&2
		return 1
		;;
	esac
	want_eq "one node held it" "$(jqv '.trace.nodes | length')" 1 || return 1
	remember N8_TRACE_A "$trace"
	printf 'trace %s: %s\n' "$trace" "$names"
}

# And a trace is filtered like everything else: B may ask for A's trace by id
# and is handed none of it.
traces_are_scope_filtered() {
	recall
	api GET "$TOKEN_B" "/api/trace/$N8_TRACE_A" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "what B sees of A's trace" "$(jqv '.trace.spans | length')" 0 || return 1
	api GET "$TOKEN_B" /api/traces || return 1
	if [ "$(printf '%s' "$API_BODY" | jq "[.traces[] | select(.trace_id == \"$N8_TRACE_A\")] | length")" != "0" ]; then
		printf "A's trace is in B's list\n" >&2
		return 1
	fi
	# The operator's view of the node has it.
	api GET "$TOKEN_OP" "/api/trace/$N8_TRACE_A?scope=all" || return 1
	want_at_least "the operator sees the trace" "$(jqv '.trace.spans | length')" 3 || return 1
	printf "B: 0 spans of %s; the operator: %s\n" "$N8_TRACE_A" "$(jqv '.trace.spans | length')"
}

# The four tools an agent already knows from serenedash, answering what that
# agent's token may read.
the_mcp_observability_tools_are_offered() {
	recall
	mcp tools/list "$TOKEN_A" || return 1
	local name
	for name in status activity storage anomalies; do
		want_eq "$name in tools/list" \
			"$(rv "[.result.tools[] | select(.name == \"$name\")] | length")" 1 || return 1
	done
	printf 'tools: %s\n' "$(rv '[.result.tools[].name] | join(", ")')"
}

the_mcp_tools_are_scope_filtered() {
	recall
	want_tool status "$TOKEN_A" || return 1
	want_eq "status is A's view" "$(tv .scope.user)" "$USER_A" || return 1
	want_eq "and the node is not A's to see" "$(tv .groups.node.available)" false || return 1
	want_at_least "A's messages are counted" "$(tv '.groups.collaboration.messages_24h')" 0 || return 1

	# The corpus a tool reports is the corpus that token may list, over the
	# same filter and to the row.
	local token
	for token in "$TOKEN_A" "$TOKEN_B"; do
		want_tool storage "$token" || return 1
		local counted listed
		counted="$(tv .groups.corpus.artifacts)"
		api GET "$token" '/api/artifacts?limit=1000' || return 1
		listed="$(printf '%s' "$API_BODY" | jq '.artifacts | length')"
		want_eq "storage counts what this token may list" "$counted" "$listed" || return 1
	done

	want_tool anomalies "$TOKEN_B" || return 1
	want_at_least "series B has too little history for" \
		"$(tv '[.groups.anomalies.series[] | select(.verdict == "insufficient samples")] | length')" 1 ||
		return 1

	# And the timeline the tool reads is the timeline the API reads: B's own run
	# line is B's, and A holds no grant into pb.
	want_tool activity "$TOKEN_B" '{"q": "b-side"}' || return 1
	want_at_least "B finds its own run" "$(tv .count)" 1 || return 1
	want_tool activity "$TOKEN_A" '{"q": "b-side"}' || return 1
	want_eq "A finds none of it" "$(tv .count)" 0 || return 1
	printf 'status, storage, anomalies and activity all answered per token\n'
}

# The exporter really exports: a collector that is not this node receives an
# OTLP payload carrying the trace of a request that really was made.
otlp_export_reaches_a_collector() {
	recall
	local cport nport trace
	cport="$(free_port 8990)"
	nport="$(free_port "$((cport + 1))")"
	: >"$WORK/otlp.jsonl"

	"$WORK/smoke" otlp-collector "127.0.0.1:$cport" "$WORK/otlp.jsonl" \
		>"$WORK/otlp-collector.log" 2>&1 &
	printf '%s' "$!" >"$WORK/otlp-collector.pid"
	DATABASE_URL="$DATABASE_URL" FLOWY_NODE=otlp-node FLOWY_OPERATOR="$USER_OP" \
		FLOWY_OTLP_ENDPOINT="http://127.0.0.1:$cport" \
		./flowy serve -addr "127.0.0.1:$nport" >"$WORK/otlp-node.log" 2>&1 &
	printf '%s' "$!" >"$WORK/otlp-node.pid"

	"$WORK/smoke" healthz "http://127.0.0.1:$nport/healthz" >/dev/null || return 1
	HTTP_PORT="$nport" apih GET "$TOKEN_A" /api/artifacts || return 1
	trace="$API_TRACE"
	if [ -z "$trace" ]; then
		printf 'the exporting node answered without a trace id\n' >&2
		return 1
	fi

	local waited=0
	while [ "$waited" -lt 100 ]; do
		if grep -q "$trace" "$WORK/otlp.jsonl" 2>/dev/null; then
			break
		fi
		sleep 0.1
		waited=$((waited + 1))
	done
	kill "$(cat "$WORK/otlp-node.pid")" 2>/dev/null
	kill "$(cat "$WORK/otlp-collector.pid")" 2>/dev/null
	rm -f "$WORK/otlp-node.pid" "$WORK/otlp-collector.pid"

	if ! grep -q "$trace" "$WORK/otlp.jsonl" 2>/dev/null; then
		printf 'nothing carrying %s reached the collector:\n' "$trace" >&2
		cat "$WORK/otlp-node.log" >&2
		return 1
	fi
	# And what arrived is OTLP, not this node's own shape.
	local spans
	spans="$(jq -s --arg t "$trace" \
		'[.[] | .resourceSpans[]? | .scopeSpans[]? | .spans[]? | select(.traceId == $t)] | length' \
		"$WORK/otlp.jsonl")" || return 1
	want_at_least "spans of that trace in the payload" "$spans" 1 || return 1
	local names
	names="$(jq -s --arg t "$trace" -r \
		'[.[] | .resourceSpans[]? | .scopeSpans[]? | .spans[]? | select(.traceId == $t) | .name]
		 | unique | join(", ")' "$WORK/otlp.jsonl")" || return 1
	printf 'the collector received %s span(s) of %s: %s\n' "$spans" "$trace" "$names"
}

# --------------------------------------- phase 8 across the two federated nodes

# The claim the architecture makes: a handoff assigned on one node and taken
# delivery of on another is ONE trace, and the id is the same in both databases.
#
# Nothing requests anything of node B when the assignment replicates - what
# crosses is a delta - so the id travels in the meta of the event that opens the
# handoff's thread, inside its signature. B reads it back off the thread and
# records its own spans under it.
a_handoff_is_one_trace_across_two_nodes() {
	recall5
	napih "$N5_PORT_A" POST "$N5_TOKEN_A" /api/artifacts \
		'{"type":"bug","title":"the traced bug","body":"followed across the fabric","status":"open"}' ||
		return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	local art task trace
	art="$(jqv .id)"

	napih "$N5_PORT_A" POST "$N5_TOKEN_A" /api/assign \
		"{\"artifact\":\"$art\",\"to_user\":\"$N5_USER_B\",\"note\":\"traced handoff\"}" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	task="$(jqv .id)"
	trace="$API_TRACE"
	if [ -z "$trace" ]; then
		printf 'the assignment answered without a trace id\n' >&2
		return 1
	fi

	# Assigned on A: A's own account of it.
	local on_a
	on_a="$(await_count "$N5_DSN_A" \
		"SELECT count(*) FROM spans WHERE trace_id = '$trace' AND node = 'nodeA'" 1)" || return 1
	# And the id rode out on the row, not on a header nobody sent.
	await_count "$N5_DSN_A" \
		"SELECT count(*) FROM events WHERE meta ->> 'trace' = '$trace'" 1 >/dev/null || return 1

	sync_round || return 1

	# Delivered to B: B's own account, under the same trace id.
	local delivered
	delivered="$(await_count "$N5_DSN_B" \
		"SELECT count(*) FROM spans
		  WHERE trace_id = '$trace' AND node = 'nodeB' AND name = 'handoff.deliver'" 1)" || return 1

	# Worked by B: the request that opens the task joins the trace it was
	# assigned in rather than starting one of its own.
	napih "$N5_PORT_B" GET "$N5_TOKEN_B" "/api/task/$task" || return 1
	want_eq "status" "$API_STATUS" 200 || return 1
	want_eq "the task on B" "$(jqv .id)" "$task" || return 1
	# Said out loud, on the response: B is working in the trace the handoff was
	# assigned in on A, not in one of its own.
	want_eq "the trace B answered in" "$API_TRACE" "$trace" || return 1
	local worked
	worked="$(await_count "$N5_DSN_B" \
		"SELECT count(*) FROM spans
		  WHERE trace_id = '$trace' AND node = 'nodeB' AND name LIKE 'GET /api/task%'" 1)" || return 1

	# Applying the same delta again does not record the delivery twice.
	sync_round || return 1
	local again
	again="$(scalar5 "$N5_DSN_B" \
		"SELECT count(*) FROM spans
		  WHERE trace_id = '$trace' AND node = 'nodeB' AND name = 'handoff.deliver'")" || return 1
	want_eq "a second sync recorded no second delivery" "$again" "$delivered" || return 1

	remember5 N8_HANDOFF_TRACE "$trace"
	remember5 N8_HANDOFF_TASK "$task"
	printf 'trace %s: %s span(s) on nodeA, %s delivery + %s worked on nodeB\n' \
		"$trace" "$on_a" "$delivered" "$worked"
}

# And the collector puts the two halves back together into one waterfall, naming
# every node it reached.
the_collector_reassembles_the_two_halves() {
	recall5
	local out nodes sources spans
	out="$(DATABASE_URL="$N5_DSN_B" FLOWY_NODE=nodeB FLOWY_OPERATOR="$N5_USER_OP" \
		"$ROOT/flowy" traces --trace "$N8_HANDOFF_TRACE" \
		--peer "http://127.0.0.1:$N5_PORT_A" --token "$N5_TOKEN_OP")" || return 1

	nodes="$(printf '%s' "$out" | jq -r '.nodes | sort | join(",")')"
	case ",$nodes," in
	*,nodeA,*) ;;
	*)
		printf 'the collected trace does not include nodeA: %s\n' "$out" >&2
		return 1
		;;
	esac
	case ",$nodes," in
	*,nodeB,*) ;;
	*)
		printf 'the collected trace does not include nodeB: %s\n' "$out" >&2
		return 1
		;;
	esac
	spans="$(printf '%s' "$out" | jq '.spans | length')"
	want_at_least "spans in the collected trace" "$spans" 2 || return 1
	# In one order, on one clock - compared as instants rather than as the
	# strings they arrive as.
	#
	# A start time is RFC3339 with a fraction whose length varies, because Go
	# trims trailing zeros on the way out: a span at .641000000 is written
	# "…31.641Z" and one at .641500000 is written "…31.6415Z". As strings the
	# first sorts after the second ('Z' is above '5'); as instants it is before
	# it. So the string form said "not in start order" about a collector that
	# had ordered them correctly, on whichever runs a span happened to land on a
	# trailing zero - which is roughly one in ten. The fraction is padded to
	# nine digits first, and then the comparison is the one it was always
	# meant to be.
	if [ "$(printf '%s' "$out" | jq '[.spans[].started
		| capture("^(?<whole>[^.Z]+)(\\.(?<frac>[0-9]+))?Z?$")
		| .whole + "." + (((.frac // "") + "000000000")[0:9])] | . == sort')" != "true" ]; then
		printf 'the collected spans are not in start order: %s\n' \
			"$(printf '%s' "$out" | jq -c '[.spans[].started]')" >&2
		return 1
	fi
	# Every source is named, so a half that could not be reached would be
	# visible rather than silently missing.
	sources="$(printf '%s' "$out" | jq -r '[.sources[] | "\(.from):\(.spans)"] | join(" ")')"
	want_at_least "sources collected from" \
		"$(printf '%s' "$out" | jq '.sources | length')" 2 || return 1
	if [ "$(printf '%s' "$out" | jq '[.sources[] | select((.error // "") != "")] | length')" != "0" ]; then
		printf 'a source could not be collected from: %s\n' "$out" >&2
		return 1
	fi
	printf 'one trace, %s spans, nodes [%s], from %s\n' "$spans" "$nodes" "$sources"
}

# A peer that cannot be reached is named with the reason, rather than leaving a
# half-trace that reads as the whole of what happened.
the_collector_says_which_half_it_could_not_reach() {
	recall5
	local out
	out="$(DATABASE_URL="$N5_DSN_B" FLOWY_NODE=nodeB FLOWY_OPERATOR="$N5_USER_OP" \
		"$ROOT/flowy" traces --trace "$N8_HANDOFF_TRACE" \
		--peer "http://127.0.0.1:1" --token "$N5_TOKEN_OP")" || return 1
	if [ "$(printf '%s' "$out" | jq '[.sources[] | select((.error // "") != "")] | length')" != "1" ]; then
		printf 'an unreachable peer was not reported: %s\n' "$out" >&2
		return 1
	fi
	# What it did reach is still there.
	want_at_least "the local half" "$(printf '%s' "$out" | jq '.spans | length')" 1 || return 1
	printf 'unreachable peer reported: %s\n' \
		"$(printf '%s' "$out" | jq -r '[.sources[] | select((.error // "") != "") | .from] | join(",")')"
}

# The console: the two new tabs and the timeline mount at their own paths, with
# a token, against the live node - the same check the room and the inbox get.
console_renders_the_metrics_tab() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"insufficient samples" /metrics
}

console_renders_the_traces_tab() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_OP" "waterfall" /traces
}

console_renders_the_timeline() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"vm_say: try the other approach" /activity
}

# The worklog, on a page a person can open.
#
# It is the fleet's memory across sessions - a fresh seat is meant to read it
# rather than somebody's session transcript - and it had no human surface at
# all: written and read over MCP, so the person the fleet works for could not
# see it without asking an agent to read it out.
#
# The three entries the two checks below assert on, on two branches, newest
# last. Seeded from the gate on purpose: a check with nothing to find reports
# "nothing present, nothing tested", which is honest and useless.
WORKLOG_PAGE_NEWEST="rehung the escapement and it keeps time"
WORKLOG_PAGE_OLDER="stripped the old escapement out"
WORKLOG_PAGE_OTHER="quenched the mainspring on its own branch"
WORKLOG_PAGE_BRANCH="wl/escapement"
WORKLOG_PAGE_OTHER_BRANCH="wl/mainspring"
# The vouched one: written by the PERSON about the AGENT's shift, which is the
# drainer's shape - a harness recording a run it drove. The page must not draw it
# as the agent's own entry.
WORKLOG_PAGE_VOUCHED="regulated the balance wheel on the run that just ended"
readonly WORKLOG_PAGE_NEWEST WORKLOG_PAGE_OLDER WORKLOG_PAGE_OTHER
readonly WORKLOG_PAGE_BRANCH WORKLOG_PAGE_OTHER_BRANCH WORKLOG_PAGE_VOUCHED

seeds_the_worklog_the_page_has_to_show() {
	recall
	local args
	args="$(wl_args "$WORKLOG_PAGE_OTHER" "" "" "" "$WORKLOG_PAGE_OTHER_BRANCH")" || return 1
	want_tool worklog_append "$TOKEN_A" "$args" || return 1
	# Seeded before the newest on purpose: the checks below assert which entry is
	# FIRST, so a vouched entry appended last would be asserting about the wrong
	# row in two places.
	args="$(jq -nc --arg w "$WORKLOG_PAGE_VOUCHED" --arg s "$AGENT_A" --arg b "$WORKLOG_PAGE_BRANCH" \
		'{what: $w, subject: $s, branch: $b, run: "b41c0de", verify: "428/0"}')" || return 1
	api POST "$TOKEN_A" /api/worklog "$args" || return 1
	want_eq "the vouched entry is vouched" "$(jqv .entry.vouched)" true || return 1
	args="$(wl_args "$WORKLOG_PAGE_OLDER" "" "" "" "$WORKLOG_PAGE_BRANCH")" || return 1
	want_tool worklog_append "$TOKEN_A" "$args" || return 1
	# The newest is the agent's, so the page has a seat to name that is not the
	# person reading it.
	args="$(wl_args "$WORKLOG_PAGE_NEWEST" "hand the mainspring back" b41c0de \
		"$MEM_SHARED" "$WORKLOG_PAGE_BRANCH")" || return 1
	want_tool worklog_append "$TOKEN_A_AGENT" "$args" || return 1
	want_eq "the newest entry is on its branch" "$(tv .entry.branch)" "$WORKLOG_PAGE_BRANCH" || return 1
	printf 'three entries on two branches, newest by %s\n' "$AGENT_A"
}

console_renders_the_worklog() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$WORKLOG_PAGE_NEWEST" /worklog
}

# The same claim one layer out, in a browser, asserted on the LIST and its ROWS
# rather than on the page's text - "worklog" is in the global navigation, so a
# page-text search for it passes with the list entirely absent, which is the
# mistake the room's todo panel check was written around.
browser_renders_the_worklog() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/worklog-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$WORKLOG_PAGE_NEWEST" "$WORKLOG_PAGE_BRANCH" "$WORKLOG_PAGE_OTHER" "$AGENT_A"
}

# And an entry one seat wrote about another's work is drawn as VOUCHED rather
# than as that seat's own entry. This is the half of the worklog change that
# matters: the marker exists so a reader is not handed the harness's report of a
# run as the run's own account of itself, and a marker no reader is shown has
# bought nothing.
browser_draws_a_vouched_entry_as_vouched() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/vouch-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$WORKLOG_PAGE_VOUCHED" "$AGENT_A" "$USER_A" "$WORKLOG_PAGE_NEWEST"
}

# ------------------------------------------------- reports: what replaced what
#
# A report names the report it replaces. supersedes rides fields on the NEWER
# document and points backwards, because backwards is the only direction the
# writer can name: the thing being replaced already exists and the replacement
# is the row being written.
#
# The reader has the other question, and it is the one that costs something to
# get wrong. They have found an old document - through a link, through a
# search, through the list - and nothing on it says a newer one exists, so they
# act on a measurement that has been superseded. Answering it means looking for
# the report whose supersedes names this one, which is a read, and the whole of
# what these checks are about is that it is the SAME read: the answer is
# another artifact's id, and a reader who may not see the replacement must not
# learn that it exists from a row they are allowed to see.
#
# So there are two pairs. Both old halves are shared and B reaches them. One
# replacement is shared too and B is told about it; the other is personal to A,
# which is the floor, and B has to come away from the old one knowing nothing.

REPORT_OLD_TITLE="the mill race bearing survey"
REPORT_NEW_TITLE="the mill race bearing survey, remeasured"
# In the body of the old report and nowhere else - not in a title, not in a
# tag, not on any card the list draws. A search that finds it searched the
# document, which is the difference between the node's search and a filter over
# whatever is already on the page.
REPORT_BODY_WORD="brambleshaft"
REPORT_KEPT_TITLE="the tailrace silt survey"
REPORT_PRIVATE_TITLE="the tailrace silt survey, remeasured"
readonly REPORT_OLD_TITLE REPORT_NEW_TITLE REPORT_BODY_WORD
readonly REPORT_KEPT_TITLE REPORT_PRIVATE_TITLE

# rep_replaced ID - what the last tool output says replaced the report with this
# id, and "" when it says nothing. Empty rather than null, because "you were
# told nothing" is the assertion half these checks make.
rep_replaced() {
	printf '%s' "$TOOL_JSON" |
		jq -r --arg id "$1" '[.items[] | select(.id == $id)][0].replaced_by // ""'
}

# rep_listed ID - how many times the last tool output holds that report.
#
# It is always asserted beside rep_replaced when the expected mark is "". A
# report that is absent from the answer entirely also reports no mark, so
# without this the leak assertions would pass on a list that had dropped the row
# for some other reason - which is a green run and no evidence.
rep_listed() {
	printf '%s' "$TOOL_JSON" | jq --arg id "$1" '[.items[] | select(.id == $id)] | length'
}

seeds_the_reports_and_what_replaced_them() {
	recall
	want_tool report_write "$TOKEN_A" \
		"$(jq -nc --arg t "$REPORT_OLD_TITLE" --arg w "$REPORT_BODY_WORD" \
			'{title: $t, scope: "shared", as_of: "mill-2026-05",
			  body: ("the " + $w + " runs 0.4mm out of true at the downstream end")}')" || return 1
	local old new silted withheld
	old="$(tv .item.id)"
	want_eq "the old one is shared" "$(tv .item.visibility)" shared || return 1

	want_tool report_write "$TOKEN_A" \
		"$(jq -nc --arg t "$REPORT_NEW_TITLE" --arg s "$old" \
			'{title: $t, scope: "shared", as_of: "mill-2026-08", supersedes: $s,
			  body: "shimmed and remeasured: 0.05mm, inside tolerance"}')" || return 1
	new="$(tv .item.id)"
	want_eq "the replacement points back at what it replaces" \
		"$(tv .item.fields.supersedes)" "$old" || return 1

	# The second pair, whose replacement never leaves A.
	want_tool report_write "$TOKEN_A" \
		"$(jq -nc --arg t "$REPORT_KEPT_TITLE" \
			'{title: $t, scope: "shared", as_of: "mill-2026-05",
			  body: "silt at the tailrace mouth, measured off the sill"}')" || return 1
	silted="$(tv .item.id)"

	want_tool report_write "$TOKEN_A" \
		"$(jq -nc --arg t "$REPORT_PRIVATE_TITLE" --arg s "$silted" \
			'{title: $t, scope: "personal", supersedes: $s,
			  body: "remeasured, and not published yet"}')" || return 1
	withheld="$(tv .item.id)"
	want_eq "the withheld replacement is personal" "$(tv .item.visibility)" personal || return 1

	remember REPORT_OLD "$old"
	remember REPORT_NEW "$new"
	remember REPORT_KEPT "$silted"
	remember REPORT_WITHHELD "$withheld"
	printf '%s replaced by %s, both shared; %s replaced by %s, which is A alone\n' \
		"$old" "$new" "$silted" "$withheld"
}

# The search reaches the body, and what it hands back says whether it still
# stands. A search is where a superseded report is most likely to be found:
# the words somebody remembers are in the old document as readily as in the one
# that replaced it, and the old one usually ranks first because it is the one
# that used the phrase.
the_search_finds_a_report_by_a_word_in_its_body() {
	recall
	want_tool report_search "$TOKEN_A" "$(jq -nc --arg q "$REPORT_BODY_WORD" '{q: $q}')" || return 1
	want_eq "hits for a word only in a body" "$(tv .count)" 1 || return 1
	want_eq "the hit" "$(tv '.items[0].id')" "$REPORT_OLD" || return 1
	want_eq "the hit says it has been replaced" \
		"$(tv '.items[0].replaced_by // ""')" "$REPORT_NEW" || return 1

	# The same read over the endpoint the console's box calls, because that is
	# the one the page has to be using: narrowed to reports, ranked by the node,
	# and carrying the same mark.
	api GET "$TOKEN_A" "/api/search?type=report&q=$REPORT_BODY_WORD" || return 1
	want_eq "the http search hits" "$(hits)" 1 || return 1
	want_eq "and the mark is on the hit over http" \
		"$(printf '%s' "$API_BODY" | jq -r '.artifacts[0].replaced_by // ""')" "$REPORT_NEW" || return 1
	printf '%s is in one body, and the hit says %s replaced it\n' \
		"$REPORT_BODY_WORD" "$REPORT_NEW"
}

# The mark is a read like any other read, and it stops where the reader stops.
#
# B holds the pb -> pa grant, so both shared documents are B's to read and the
# mark on the first pair is B's to follow. The second pair is the floor: B may
# read the old report and may not read what replaced it, so B must be told
# nothing - not the id, and not that there is one. A, who owns the replacement,
# is told.
the_mark_stops_where_the_reader_does() {
	recall
	want_tool report_read "$TOKEN_B" "{\"id\": \"$REPORT_OLD\"}" || return 1
	want_eq "B is told what replaced it" \
		"$(tv '.item.replaced_by // ""')" "$REPORT_NEW" || return 1

	want_tool report_read "$TOKEN_B" "{\"id\": \"$REPORT_KEPT\"}" || return 1
	want_eq "B is told nothing about a replacement B cannot read" \
		"$(tv '.item.replaced_by // ""')" "" || return 1
	want_tool_fails report_read "$TOKEN_B" "{\"id\": \"$REPORT_WITHHELD\"}" "no such" || return 1

	want_tool report_read "$TOKEN_A" "{\"id\": \"$REPORT_KEPT\"}" || return 1
	want_eq "A, who owns the replacement, is told" \
		"$(tv '.item.replaced_by // ""')" "$REPORT_WITHHELD" || return 1

	# Every door, not one: a mark that is filtered on the read by id and handed
	# out by the list is a leak with a check standing next to it.
	want_tool report_list "$TOKEN_B" '{}' || return 1
	want_eq "B's list marks the shared pair" "$(rep_replaced "$REPORT_OLD")" "$REPORT_NEW" || return 1
	want_eq "B's list holds the other old report" "$(rep_listed "$REPORT_KEPT")" 1 || return 1
	want_eq "and says nothing about what replaced it" "$(rep_replaced "$REPORT_KEPT")" "" || return 1
	want_eq "and does not list the personal replacement at all" \
		"$(rep_listed "$REPORT_WITHHELD")" 0 || return 1

	want_tool report_search "$TOKEN_B" '{"q": "tailrace silt"}' || return 1
	want_eq "B's search finds the old report" "$(rep_listed "$REPORT_KEPT")" 1 || return 1
	want_eq "and says nothing there either" "$(rep_replaced "$REPORT_KEPT")" "" || return 1
	want_eq "and the personal replacement is not a hit" "$(rep_listed "$REPORT_WITHHELD")" 0 || return 1
	printf "B follows the mark it may follow and never learns the id of A's personal replacement\n"
}

# The list, which is what somebody scanning the reports actually reads: which of
# these have been replaced, and by what.
the_list_says_which_reports_have_been_replaced() {
	recall
	want_tool report_list "$TOKEN_A" '{}' || return 1
	want_eq "what replaced the old one" "$(rep_replaced "$REPORT_OLD")" "$REPORT_NEW" || return 1
	want_eq "and the second pair" "$(rep_replaced "$REPORT_KEPT")" "$REPORT_WITHHELD" || return 1
	# Both halves of a pair are listed. Marking the old one while dropping the
	# new one leaves the mark pointing somewhere the reader cannot get to from
	# the page they are on.
	want_eq "the replacement is listed too" "$(rep_listed "$REPORT_NEW")" 1 || return 1
	want_eq "and so is the personal one, for its owner" "$(rep_listed "$REPORT_WITHHELD")" 1 || return 1
	# The replacements are not themselves marked. Without this a list that
	# stamped every row with the same id passes everything above.
	want_eq "the replacement is not itself replaced" "$(rep_replaced "$REPORT_NEW")" "" || return 1
	want_eq "nor is the withheld one" "$(rep_replaced "$REPORT_WITHHELD")" "" || return 1

	api GET "$TOKEN_A" '/api/artifacts?type=report' || return 1
	want_eq "and the same over the list the console reads" \
		"$(printf '%s' "$API_BODY" |
			jq -r "[.artifacts[] | select(.id == \"$REPORT_OLD\")][0].replaced_by // \"\"")" \
		"$REPORT_NEW" || return 1
	printf 'the list marks %s replaced by %s, and lists both\n' "$REPORT_OLD" "$REPORT_NEW"
}

# The same claims one layer out, in a browser, on the ROWS - and the search box
# on the page driven against the live node. See scripts/reports-check.mjs for
# why each half is asserted the way it is.
browser_marks_a_superseded_report() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/reports-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$REPORT_OLD" "$REPORT_NEW" "$REPORT_OLD_TITLE" "$REPORT_NEW_TITLE" "$REPORT_BODY_WORD"
}

# Signed out, the page says what to do about it. It said "no token", which is
# true and which reads, under a heading that says reports, as "there are none" -
# and a reader who has been told nothing must not come away thinking they have
# been told there is nothing. No node and no token here on purpose: this is the
# state a browser is in when somebody opens the link for the first time.
the_reports_page_says_it_is_signed_out() {
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "" "" "paste a token to see the reports" /reports
}

say "reports: the search, and what replaced what"
# The floor in the query that implements it, ahead of the live checks. Those go
# through the tools and prove the answer; this one goes at the SQL, which is
# where a second read path gets added and where the filter gets forgotten.
check "the reverse of supersedes is filtered like the row it is on, in the store" \
	go test -count=1 -run 'TestWhatReplacedAReportIsFilteredLikeTheReportItself|TestAReportReplacedTwiceNamesTheNewestReplacement' ./internal/store
check "two pairs of reports, one replacement shared and one personal" \
	seeds_the_reports_and_what_replaced_them
check "the search finds a report by a word in its body, and narrows to it" \
	the_search_finds_a_report_by_a_word_in_its_body
check "and it is the same permission filter, mark included: B stops at the personal floor" \
	the_mark_stops_where_the_reader_does
check "the list says which of the reports have been replaced, and by what" \
	the_list_says_which_reports_have_been_replaced
check "a superseded report is marked where somebody reads it, and the console's search is the node's" \
	browser_marks_a_superseded_report
check "signed out, the reports page says so rather than looking empty" \
	the_reports_page_says_it_is_signed_out

# The findings console: three axes, and the two documents that are not the body.
#
# A finding carries OUR lifecycle (open/triaged/done), a filing state on
# SOMEBODY ELSE'S tracker (unfiled/filed as #123/accepted), and evidence (source/
# reproduced/verified against a commit). None of the three answers either of the
# others - done-and-unfiled is written up and sent to nobody, which is the state
# most of the corpus sits in - and every attempt to draw one in place of another
# has produced a page that says something false. The console reads all three off
# the row through web/src/lib/findings.ts, and these checks are what stops that
# collapsing again.
#
# TWO FINDINGS, DELIBERATELY OPPOSITE. One is done for us, filed upstream with a
# number, ships a repro tree and carries an upstream draft. The other is open,
# unfiled, has no tree and no draft. Everything either page claims about the
# first has to be denied about the second, or a page that stamped every row
# alike would pass.
#
# The filing keys are written with SQL rather than through a verb, because the
# verb that writes them (store.SetFindingUpstream) is landing on another branch.
# What the console must do is render a row that carries them, and that row is a
# row whether a tool or an operator's import put the keys there - the names are
# the ones internal/store/findingupstream.go and the corpus importers were both
# given, and if they ever disagree this check is what goes red.
FINDING_ISSUE="4471"
# In the upstream draft and nowhere else - not in the body, not in the
# discovery, not in a title. A pane that showed the body over again would
# otherwise pass every assertion about the draft.
FINDING_DRAFT_WORD="quernstone"
FINDING_REPRO_PATH="repro-01-tables.sh"
readonly FINDING_ISSUE FINDING_DRAFT_WORD FINDING_REPRO_PATH

seeds_two_findings_on_three_axes() {
	recall
	local script filed unfiled referenced project
	script="$(printf '#!/usr/bin/env bash\nexit 0\n' | base64 -w0)"

	want_tool finding_write "$TOKEN_A" \
		"$(jq -nc --arg c "$script" --arg w "$FINDING_DRAFT_WORD" --arg p "$FINDING_REPRO_PATH" \
			'{title: "the sluice gate counter double-counts a reversed flow",
			  body: "the counter adds on both edges, so a reversal reads as throughput",
			  discovery: "found while reconciling the weir log against the day sheet",
			  report: ("Reversed flow is counted as throughput. The " + $w +
			           " test rig reproduces it in one pass."),
			  scope: "project", severity: "high", kind: "correctness",
			  repro: [{path: $p, content_base64: $c}],
			  repro_entrypoint: $p, repro_interp: "bash", isolation: "plain"}')" || return 1
	filed="$(tv .item.id)"
	project="$(tv .item.project)"
	want_eq "the repro tree is on the row" "$(tv '.item.fields.repro_files | length')" 1 || return 1
	want_eq "and the upstream draft is a field, not the body" \
		"$(tv '.item.fields.report | contains("'"$FINDING_DRAFT_WORD"'")')" true || return 1
	want_eq "which the body does not carry" \
		"$(tv '.item.body | contains("'"$FINDING_DRAFT_WORD"'")')" false || return 1

	# Our lifecycle, walked through the door that leaves the trail event. It is
	# walked rather than jumped because the issue workflow is a LINE - open,
	# triaged, in-progress, in-review, done - and a jump straight to the end is
	# refused (lifecycle.go's canTransition). That refusal is the right one and
	# it is also why this is four calls: a finding reaches done by somebody
	# having worked it, and the trail says so.
	local step
	for step in triaged in-progress in-review "done"; do
		api POST "$TOKEN_A" "/api/artifact/$filed/status" "{\"status\": \"$step\"}" || return 1
		want_eq "moved to $step" "$(printf '%s' "$API_BODY" | jq -r .artifact.status)" "$step" || return 1
	done

	# Their tracker, which our status says nothing about. See the head of this
	# block on why this is SQL.
	psql_do "UPDATE artifacts SET fields = coalesce(fields, '{}'::jsonb) || jsonb_build_object(
		 'upstream_tracker', 'serenedb',
		 'upstream_id', '$FINDING_ISSUE',
		 'upstream_state', 'filed',
		 'upstream_url', 'https://tracker.invalid/issues/$FINDING_ISSUE')
	   WHERE id = '$filed'" || return 1

	# The evidence, THROUGH THE VERB. It used to be another key in the SQL
	# above, with a comment saying the verb was landing on another branch. It
	# has landed, so the seed goes through the door a person would use - which
	# means this check now also proves the door writes the key the console
	# reads, rather than proving that psql can write a string.
	api POST "$TOKEN_A" "/api/finding/$filed/evidence" '{"state": "reproduced"}' || return 1
	want_eq "the evidence door wrote the key the console reads" \
		"$(printf '%s' "$API_BODY" | jq -r .evidence.evidence_state)" reproduced || return 1

	# The opposite row: nothing written up for anybody else, nothing to run.
	want_tool finding_write "$TOKEN_A" \
		'{"title": "the weir board warps in the wet and the reading drifts",
		  "body": "suspected from the shape of the drift; nobody has run anything",
		  "scope": "project", "severity": "medium", "kind": "correctness"}' || return 1
	unfiled="$(tv .item.id)"
	want_eq "and it is open" "$(tv .item.status)" open || return 1

	# THE THIRD ROW IS THE ONE THAT GETS MISCOUNTED: it names things over there
	# and nobody claims to have sent it. Seven of the sixteen RAGFlow findings
	# are that, and reading them as filings is what reported one filing as
	# eight - so the console has to draw REFERENCED as its own word and leave it
	# out of the filed count. The row carries citations and no state, which is
	# exactly what an import writes, and the state it reads as is the store's
	# own fallback (FindingUpstreamOf) rather than anything this check states.
	want_tool finding_write "$TOKEN_A" \
		'{"title": "the tailrace gauge disagrees with two of their open issues",
		  "body": "their issue text describes the same drift; nobody has written to them",
		  "scope": "project", "severity": "low", "kind": "correctness"}' || return 1
	referenced="$(tv .item.id)"
	psql_do "UPDATE artifacts SET fields = coalesce(fields, '{}'::jsonb) || jsonb_build_object(
		 'upstream_refs', jsonb_build_array(
		   jsonb_build_object('tracker', 'serenedb', 'kind', 'issue', 'id', '901'),
		   jsonb_build_object('tracker', 'serenedb', 'kind', 'pr', 'id', '902')))
	   WHERE id = '$referenced'" || return 1

	remember FINDING_FILED "$filed"
	remember FINDING_UNFILED "$unfiled"
	remember FINDING_REFERENCED "$referenced"
	remember FINDING_PROJECT "$project"
	printf '%s is done here and filed there as #%s; %s is open and unfiled; %s cites two and was sent to nobody\n' \
		"$filed" "$FINDING_ISSUE" "$unfiled" "$referenced"
}

# The list read the way the console reads it: both axes have to survive the trip
# out, or the page has nothing to draw them from. This asserts on the API before
# the browser check asserts on the elements, so a failure says which half broke.
the_list_carries_both_axes() {
	recall
	api GET "$TOKEN_A" '/api/artifacts?type=finding' || return 1
	local row
	row="$(printf '%s' "$API_BODY" | jq -c --arg id "$FINDING_FILED" '[.artifacts[] | select(.id == $id)][0]')"
	want_eq "our lifecycle is on the row" "$(printf '%s' "$row" | jq -r .status)" "done" || return 1
	want_eq "their filing state is on it too" \
		"$(printf '%s' "$row" | jq -r .fields.upstream_state)" filed || return 1
	want_eq "with the number a reader can act on" \
		"$(printf '%s' "$row" | jq -r .fields.upstream_id)" "$FINDING_ISSUE" || return 1
	want_eq "and the evidence, which is neither of them" \
		"$(printf '%s' "$row" | jq -r .fields.evidence_state)" reproduced || return 1
	want_eq "the upstream draft rides out with it" \
		"$(printf '%s' "$row" | jq -r '.fields.report | contains("'"$FINDING_DRAFT_WORD"'")')" true || return 1
	printf 'the list read carries status, upstream_state #%s and evidence_state separately\n' \
		"$FINDING_ISSUE"
}

# And the same claims one layer out, in a browser, on the ELEMENTS - plus the
# filter, which is the thing the list exists for: "everything written up and not
# yet filed" is the question somebody asks before filing anything.
browser_shows_both_axes_and_the_two_documents() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/findings-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$FINDING_PROJECT" "$FINDING_FILED" "$FINDING_UNFILED" "$FINDING_REFERENCED" \
		"$FINDING_ISSUE" "$FINDING_DRAFT_WORD" "$FINDING_REPRO_PATH"
}

# The repro runner is a second binary on a second host, so nothing in this
# build makes the console and its doors agree. This drives the real client
# against the answers cmd/handoff-runner/http.go actually writes - no node and
# no runner needed, which is why it does not sit behind the live phase.
the_console_speaks_the_runners_answers() {
	cd "$ROOT/web" || return 1
	node scripts/repro-contract-check.mjs
}

# THE REFUSAL THE EVIDENCE AXIS EXISTS FOR, over the wire.
#
# `verified` is a word PLUS A COMMIT: REPORTABLE-FINDINGS' filing rule is that
# nothing goes upstream until its reproduction has been run against a build of
# current origin/main HEAD with that sha on the item, because a report whose
# repro ran against the released image is closed as already-fixed. Without the
# sha the word is `reproduced` spelled more confidently, and the list somebody
# works from before filing - reproduced but not against current main - loses rows
# to it silently.
#
# So the refusal is driven here rather than trusted from the store test: a rule
# that is only true in a unit test is a rule that has never met the door. It also
# asserts THE REFUSED WRITE CHANGED NOTHING, which is the half that would
# otherwise go unnoticed - a 400 with the row moved anyway is worse than no
# check.
the_evidence_door_requires_a_commit() {
	recall
	want_status 400 POST "$TOKEN_A" "/api/finding/$FINDING_FILED/evidence" \
		'{"state": "verified"}' || return 1
	want_eq "the refusal names the commit it wanted" \
		"$(printf '%s' "$API_BODY" | jq -r '.error | contains("verified_on")')" true || return 1
	api GET "$TOKEN_A" "/api/finding/$FINDING_FILED/evidence" || return 1
	want_eq "and the refused write left the claim where it stood" \
		"$(printf '%s' "$API_BODY" | jq -r .evidence.evidence_state)" reproduced || return 1

	# A refutation names its commit too, and this is the one that matters more:
	# "it does not reproduce" with nothing saying WHERE is how a real defect
	# gets closed.
	want_status 400 POST "$TOKEN_A" "/api/finding/$FINDING_FILED/evidence" \
		'{"state": "refuted"}' || return 1

	# A commit under `source` is the mirror refusal: source is nobody having run
	# it, so there is no run for a commit to be the commit OF.
	want_status 400 POST "$TOKEN_A" "/api/finding/$FINDING_UNFILED/evidence" \
		'{"state": "source", "verified_on": "67adbe04"}' || return 1

	# REFUTED IS NOT VERIFIED, recorded and read back as its own word. Two of the
	# twenty-four SereneDB reproductions are this - tried on a named commit, the
	# defect was not there - and a build that folded them into verified would
	# send somebody upstream with a defect that does not exist.
	api POST "$TOKEN_A" "/api/finding/$FINDING_UNFILED/evidence" \
		'{"state": "refuted", "verified_on": "bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032"}' || return 1
	want_eq "a refutation is its own word" \
		"$(printf '%s' "$API_BODY" | jq -r .evidence.evidence_state)" refuted || return 1
	want_eq "and it names what it was run against" \
		"$(printf '%s' "$API_BODY" | jq -r .evidence.verified_on)" \
		bc07c51d4b8d9f0c6f4e3ad6a3a8952decd6d032 || return 1

	# And the accepted shape, on the row that carries no claim yet: the word and
	# the commit land together and read back off the row.
	api POST "$TOKEN_A" "/api/finding/$FINDING_REFERENCED/evidence" \
		'{"state": "verified", "verified_on": "67adbe04", "verified_at": "2026-08-07"}' || return 1
	want_eq "verified is recorded" \
		"$(printf '%s' "$API_BODY" | jq -r .evidence.evidence_state)" verified || return 1
	want_eq "with the commit it rests on" \
		"$(printf '%s' "$API_BODY" | jq -r .evidence.verified_on)" 67adbe04 || return 1
	# The log's first entry comes out of an unstated claim, which is what "nobody
	# has said" reads as - and is not the word `source`.
	want_eq "and the log says it came from nobody having said" \
		"$(printf '%s' "$API_BODY" | jq -r '.log[0].from')" "" || return 1
	printf 'verified and refuted both refused without a commit; refuted recorded as its own word\n'
}

# CAN A PERSON TELL TWO STATES APART WITHOUT READING A WORD. The operator's
# report was that everything except chat looks bland, and the diagnosis off the
# python console's stylesheet was not that we lack colours - it is that ours
# carry no facts. Every axis on a finding was drawn in one grey, so filed,
# unfiled and referenced, and reproduced and nobody-has-said, were all the same
# chip with a different word in it.
#
# So this asserts DISCRIMINATION and never presence. Tinting everything would
# satisfy "is it coloured" and would be worse than the grey it replaced, because
# a page whose colours mean nothing teaches people to stop reading them. See
# scripts/statecolour-check.mjs for the three questions it asks of every pair,
# and web/src/lib/statecolour.ts for which fact gets which tone.
the_console_colours_carry_facts() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/statecolour-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$FINDING_PROJECT" "$FINDING_FILED" "$FINDING_UNFILED" "$FINDING_REFERENCED" \
		"$REPORT_OLD" "$REPORT_NEW"
}

say "findings: our lifecycle, their filing, and the evidence - three axes"
check "two findings, one done and filed as #4471, one open and unfiled" \
	seeds_two_findings_on_three_axes
check "the list read carries all three axes, and the upstream draft" \
	the_list_carries_both_axes
check "the console draws both axes, filters on the mark, and keeps the draft and the tree apart" \
	browser_shows_both_axes_and_the_two_documents
check "verified is a word plus a commit, and the door refuses the word alone" \
	the_evidence_door_requires_a_commit
check "the console reads the repro runner's own answers - /runs, /run and /version" \
	the_console_speaks_the_runners_answers
check "two states are told apart at a glance - filed from unfiled, reproduced from source-only" \
	the_console_colours_carry_facts

# The new button on /diagrams, clicked in a real browser with the name box
# EMPTY - the state an operator reported as "cant create a diagram". Every unit
# test passed and the node's write door was fine; the button was simply
# disabled until the box beside it was filled, and a disabled button here has
# no cursor, no hover, no message and no navigation. Nothing exercised the
# click, so nothing saw it. See scripts/diagram-new-check.mjs for why the four
# ways this fails are told apart in its output rather than after the fact.
a_diagram_is_created_by_clicking_new() {
	cd "$ROOT/web" || return 1
	node scripts/diagram-new-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A"
}

# Signed out, the page says what to do about it rather than reading as "there
# are no diagrams". No node and no token on purpose: this is the state a
# browser is in when somebody opens the link for the first time.
the_diagrams_page_says_it_is_signed_out() {
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "" "" "paste a token to see the diagrams" /diagrams
}

say "diagrams: the new button, clicked"
check "new with an empty name makes a diagram, opens the editor and can be renamed" \
	a_diagram_is_created_by_clicking_new
check "signed out, the diagrams page says so rather than looking empty" \
	the_diagrams_page_says_it_is_signed_out

say "metrics: what was measured, and for whom"
check "every group is in the answer, and says whether it was measured" \
	metrics_answers_every_group
check "the corpus is the caller's corpus, project by project" metrics_are_scope_filtered
check "a personal artifact counts for its owner and for nobody else" \
	personal_artifacts_count_only_for_their_owner
check "a stranger cannot read another project's metrics, scope=all or not" \
	a_stranger_cannot_read_another_projects_metrics
check "the operator's scope=all is the node: cpu as a share of one core, rss, pool, disk" \
	the_operator_scope_all_sees_the_node
check "what was not measured says so rather than answering zero" \
	what_was_not_measured_is_not_a_zero

say "anomalies: against recorded history, or not at all"
check "below the minimum sample count there is no verdict" \
	anomalies_refuse_a_verdict_below_the_minimum
check "with a history, the verdict is a distance from it - and it is per scope" \
	anomalies_judge_against_recorded_history
check "a refusal is counted for whoever was refused, and names no row" \
	refusals_are_counted_for_whoever_was_refused

say "the scrape"
check "GET /metrics is the same measurements in the prometheus format" \
	prometheus_text_is_the_same_measurements
check "and without a token it is the console, because a browser sends none" \
	the_metrics_path_is_a_page_as_well

say "the activity timeline"
check "turns, run logs, chat and steers are one indexed timeline, in order" \
	the_timeline_indexes_every_kind
check "it is searchable, and a kind the node mints cannot be posted" the_timeline_is_searchable
check "and it is filtered: B holds none of A's runs" the_timeline_is_scope_filtered
check "the message box is everywhere: post into a run from the timeline" \
	the_timeline_is_postable_into

say "traces"
check "a request is a trace: the permission check and the queries under it" \
	a_request_is_a_trace
check "a trace is filtered like everything else" traces_are_scope_filtered
check "the OTLP exporter reaches a collector that is not this node" \
	otlp_export_reaches_a_collector

say "the observability tools an agent already knows"
check "status, activity, storage and anomalies are offered" \
	the_mcp_observability_tools_are_offered
check "and each answers what that token may read" the_mcp_tools_are_scope_filtered

say "one handoff, two nodes, one trace"
check "assigned on A, delivered to B, worked on B - the same trace id in both databases" \
	a_handoff_is_one_trace_across_two_nodes
check "the collector reassembles both halves into one waterfall" \
	the_collector_reassembles_the_two_halves
check "and it names the half it could not reach" \
	the_collector_says_which_half_it_could_not_reach

say "the console's new tabs"
check "the metrics tab mounts and renders what the node measured" \
	console_renders_the_metrics_tab
check "the traces tab mounts" console_renders_the_traces_tab
check "the activity timeline mounts and renders what was said" console_renders_the_timeline
check "the worklog the page has to show is there to find" \
	seeds_the_worklog_the_page_has_to_show
check "the worklog tab mounts and renders a seeded entry" console_renders_the_worklog
check "and a browser shows it newest first, narrowable by branch" \
	browser_renders_the_worklog
check "an entry written about another seat's work is drawn as vouched, and an authored one is not" \
	browser_draws_a_vouched_entry_as_vouched

# ---------------------------------------------------------------- phase 9
#
# Announcements, system agents and the quiesce protocol.
#
# An announcement is an artifact of type 'announcement', so everything the
# fabric already promises about an artifact is what this phase leans on: the
# signature, the permission filter, the merge. What is new is three things, and
# each is checked as a property rather than as a call that returns 200.
#
#   scope     - a node announcement does not leave the node, a federation one
#               travels. Both doors are checked, and the federation one is
#               checked over the two nodes Phase 5 already stood up rather than
#               over a third pair: two nodes on two clusters is expensive, and a
#               second pair fighting the first for ports is how the last attempt
#               at this made the whole federation phase fail from clean.
#   kind      - a worker agent cannot post to the fabric and a system agent can,
#               and the generic artifact endpoint will not write one either. A
#               capability that has a second door has no capability.
#   quiesce   - a maintenance announcement that names a resource holds it, and
#               resolving is refused while it is held. The refusal is the whole
#               protocol: without it the acks are a report.
#
# It runs last on purpose. It writes announcements into pa on the single node
# and into pa on nodeA, and the metrics checks above count that corpus.

# ----------------------------------------------------------- phase 9 helpers

# render_room EXPECTED [ABSENT] - the built console, signed in as A, against the
# live single node: the room has to paint EXPECTED and must not be showing
# ABSENT. The absence is asserted after the wait for EXPECTED, so it is a
# statement about a page that loaded.
render_room() {
	recall
	cd "$ROOT/web" || return 1
	node scripts/render-check.mjs "http://127.0.0.1:$HTTP_PORT" "$TOKEN_A" \
		"$1" /chat/general "${2:-}"
}

# quiesce_of ID - read an announcement's quiesce as A, leaving it in API_BODY.
quiesce_of() { api GET "$TOKEN_A" "/api/announcement/$1/quiesce"; }

# ------------------------------------------------------------- phase 9 checks

# The column exists, it defaults, and the default is what every agent that was
# seeded without asking for a kind came back as.
the_agent_kind_defaults_to_worker() {
	recall
	want_eq "agents with no kind at all" \
		"$(scalar "SELECT count(*) FROM agents WHERE agent_kind IS NULL")" 0 || return 1
	want_eq "the agent the seed did not give a kind" \
		"$(scalar "SELECT agent_kind FROM agents WHERE id = '$AGENT_A'")" worker || return 1
	want_eq "the one it did" \
		"$(scalar "SELECT agent_kind FROM agents WHERE id = '$AGENT_A_SYSTEM'")" system || return 1
	# And the runtime column is untouched: they are different questions.
	want_eq "the runtime of the system agent" \
		"$(scalar "SELECT kind FROM agents WHERE id = '$AGENT_A_SYSTEM'")" claude || return 1

	# The kind reaches the principal, which is where the capability is read.
	api GET "$TOKEN_A_SYSTEM" /api/whoami || return 1
	want_eq "whoami agent" "$(jqv .agent)" "$AGENT_A_SYSTEM" || return 1
	want_eq "whoami agent_kind" "$(jqv .agent_kind)" system || return 1
	want_eq "whoami user" "$(jqv .user)" "$USER_A" || return 1
	api GET "$TOKEN_A_AGENT" /api/whoami || return 1
	want_eq "the ordinary agent's kind" "$(jqv .agent_kind)" worker || return 1
	# A person is not an agent of the least privileged kind - they are not an
	# agent, and the field is absent rather than defaulted.
	api GET "$TOKEN_A" /api/whoami || return 1
	want_eq "a person's kind" "$(jqv .agent_kind)" null || return 1
	printf 'worker by default, system where the seed asked for it, and it reaches the principal\n'
}

# The capability, and the second door it must not have.
only_a_system_agent_announces_to_the_fabric() {
	recall
	want_status 403 POST "$TOKEN_A_AGENT" /api/announcements \
		'{"scope":"federation","severity":"warning","title":"a worker speaks for everybody"}' ||
		return 1
	want_status 403 POST "$TOKEN_A" /api/announcements \
		'{"scope":"federation","severity":"warning","title":"and so does the person"}' || return 1
	# The operator is a person too. Being the operator of this node is not being
	# a machine that speaks for the fabric.
	want_status 403 POST "$TOKEN_OP" /api/announcements \
		'{"scope":"federation","severity":"warning","title":"and so does the operator"}' || return 1

	want_status 200 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"federation","severity":"info","title":"a system agent may say quibblenock"}' ||
		return 1
	want_eq "the scope it landed with" "$(jqv .announcement.fields.scope)" federation || return 1
	want_eq "the status it opened in" "$(jqv .announcement.status)" active || return 1
	remember FED_NOTICE "$(jqv .announcement.id)"

	# And there is no way round it through the endpoint that writes artifacts,
	# where the scope would be a blob nobody checks.
	want_status 403 POST "$TOKEN_A_AGENT" /api/artifacts \
		'{"type":"announcement","title":"round the side","fields":{"scope":"federation"}}' || return 1
	want_eq "rows it wrote" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE title = 'round the side'")" 0 || return 1

	# A worker may still say something about its own node, which is what makes
	# the refusal above about the scope rather than about the agent.
	want_status 200 POST "$TOKEN_A_AGENT" /api/announcements \
		'{"scope":"node","severity":"info","title":"a worker may still say this much"}' || return 1
	printf 'federation is the system agent alone, and only through its own endpoint\n'
}

# What a well-formed announcement is. Each of these is a 400 rather than a row
# that describes a reach the node does not implement.
an_announcement_says_what_it_is() {
	recall
	want_status 400 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"everywhere","severity":"info","title":"a scope nobody implements"}' || return 1
	want_status 400 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"catastrophic","title":"a severity nobody implements"}' || return 1
	want_status 400 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"maintenance","title":"no title","resource":"x","mode":"soon"}' ||
		return 1
	want_status 400 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"maintenance","title":""}' || return 1
	# A notice does not hold a resource: quiescing is a property of a change, and
	# an info announcement that could hold things would be a way to stop other
	# people's work by describing it.
	want_status 400 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"info","title":"a notice that stops the world","resource":"the-world"}' ||
		return 1
	# And a mode with nothing to quiesce says nothing.
	want_status 400 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"maintenance","title":"a mode and no resource","mode":"drain"}' ||
		return 1
	printf 'scope, severity, mode and the pairing of a resource with a change\n'
}

# The quiesce log is minted by the endpoints that do the thing. An ack anybody
# can type is a gate anybody can open, and a hold anybody can type is a way to
# stop somebody else's release by claiming to depend on it.
the_quiesce_log_is_not_something_a_client_types() {
	recall
	local kind
	for kind in quiesce.ack quiesce.hold quiesce.release announcement; do
		want_status 403 POST "$TOKEN_A_AGENT" /api/events \
			"{\"type\":\"$kind\",\"body\":\"consider it done\"}" || return 1
	done
	printf 'the four minted types are refused to a client writing events by hand\n'
}

# The banner: what the node wants everybody to know, above the conversation.
the_console_banner_shows_an_active_announcement() {
	recall
	want_status 200 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"maintenance","title":"the store goes down at grumblewick",
		  "body":"back in ten minutes"}' || return 1
	remember BANNER_ANN "$(jqv .announcement.id)"
	render_room "the store goes down at grumblewick"
}

# And it clears when the announcement is resolved, because what clears it is the
# announcement's own state and not this browser's.
the_banner_clears_when_the_announcement_is_resolved() {
	recall
	want_status 200 POST "$TOKEN_A" "/api/announcement/$BANNER_ANN/resolve" '{}' || return 1
	want_eq "the status it left in" "$(jqv .announcement.status)" resolved || return 1
	if [ -z "$(jqv .announcement.fields.resolved_at)" ] ||
		[ "$(jqv .announcement.fields.resolved_at)" = null ]; then
		printf 'the resolved announcement carries no resolved_at, so it has no window\n' >&2
		return 1
	fi
	# It is out of the active list, and still a row - the window closed, the
	# announcement did not vanish.
	api GET "$TOKEN_A" /api/announcements || return 1
	want_eq "it is still offered as active" \
		"$(printf '%s' "$API_BODY" | jq '[.announcements[] | select(.id == "'"$BANNER_ANN"'")] | length')" \
		0 || return 1
	want_eq "and the row is still there" \
		"$(scalar "SELECT count(*) FROM artifacts WHERE id = '$BANNER_ANN' AND status = 'resolved'")" \
		1 || return 1
	# The federation notice from earlier is still active, so the page below has
	# certainly loaded its banner: the absence is a statement about a banner
	# that rendered, not about a page that did not.
	render_room "a system agent may say quibblenock" "the store goes down at grumblewick"
}

# The quiesce protocol, under ack-required: only an answer clears the resource.
an_ack_required_announcement_holds_its_resource() {
	recall
	# A worker agent depends on the thing that is about to be taken away.
	want_status 200 POST "$TOKEN_A_AGENT" /api/quiesce/hold \
		'{"resource":"flimberwock-index"}' || return 1

	want_status 200 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"maintenance","title":"reindexing flimberwock",
		  "resource":"flimberwock-index","mode":"ack-required"}' || return 1
	remember QUIESCE_ANN "$(jqv .announcement.id)"
	local ann
	ann="$(jqv .announcement.id)"

	quiesce_of "$ann" || return 1
	want_eq "the state before anybody answers" "$(jqv .state)" held || return 1
	want_eq "how many it is waiting on" "$(jqv '.pending | length')" 1 || return 1
	want_eq "and which" "$(jqv '.pending[0]')" "$AGENT_A" || return 1

	# The change does not proceed while the resource is held. This refusal is
	# the protocol - everything else about a quiesce is a report.
	want_status 409 POST "$TOKEN_A" "/api/announcement/$ann/resolve" '{}' || return 1
	want_eq "and the announcement is still open" \
		"$(scalar "SELECT status FROM artifacts WHERE id = '$ann'")" active || return 1

	# Letting go quietly is not an answer under ack-required: the mode asked to
	# be acknowledged, and a process that went away has acknowledged nothing.
	want_status 200 POST "$TOKEN_A_AGENT" /api/quiesce/release \
		'{"resource":"flimberwock-index"}' || return 1
	quiesce_of "$ann" || return 1
	want_eq "still held after a bare release" "$(jqv .state)" held || return 1
	printf 'flimberwock-index is held against %s, and the resolve is refused\n' "$AGENT_A"
}

# ... and releases it when the dependent answers.
and_releases_it_when_the_dependent_acks() {
	recall
	want_status 200 POST "$TOKEN_A_AGENT" "/api/announcement/$QUIESCE_ANN/ack" '{}' || return 1
	want_eq "the state the ack left it in" "$(jqv .quiesce.state)" released || return 1
	want_eq "nothing pending" "$(jqv '.quiesce.pending | length')" 0 || return 1

	want_status 200 POST "$TOKEN_A" "/api/announcement/$QUIESCE_ANN/resolve" '{}' || return 1
	want_eq "the announcement closed" "$(jqv .announcement.status)" resolved || return 1
	want_eq "the ack is in the log" \
		"$(scalar "SELECT count(*) FROM events
		            WHERE type = 'quiesce.ack' AND artifact = '$QUIESCE_ANN'
		              AND actor = '$AGENT_A'")" 1 || return 1
	printf 'acked, released, resolved - in that order and no other\n'
}

# Under drain, letting go IS the answer. Same machinery, different mode, and the
# difference is the point of having modes at all.
a_drain_announcement_releases_when_the_holder_lets_go() {
	recall
	want_status 200 POST "$TOKEN_A_AGENT" /api/quiesce/hold \
		'{"resource":"zibbleflax-queue"}' || return 1
	want_status 200 POST "$TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"breaking","title":"the queue format changes",
		  "resource":"zibbleflax-queue","mode":"drain"}' || return 1
	local ann
	ann="$(jqv .announcement.id)"

	quiesce_of "$ann" || return 1
	want_eq "held while the queue is in use" "$(jqv .state)" held || return 1
	want_status 409 POST "$TOKEN_A" "/api/announcement/$ann/resolve" '{}' || return 1

	want_status 200 POST "$TOKEN_A_AGENT" /api/quiesce/release \
		'{"resource":"zibbleflax-queue"}' || return 1
	quiesce_of "$ann" || return 1
	want_eq "draining is the answer drain asked for" "$(jqv .state)" released || return 1
	want_status 200 POST "$TOKEN_A" "/api/announcement/$ann/resolve" '{}' || return 1
	printf 'drain releases on a release; ack-required does not\n'
}

# ------------------------------------------ phase 9 across the two Phase 5 nodes
#
# The same two nodes Phase 5 stood up, still running, still holding each other's
# keys. Nothing new is started here.

a_system_agent_announces_across_the_fabric() {
	recall5
	# The worker agent's token was copied to both nodes with everything else,
	# and it is still not a token that speaks for the fabric.
	want_napi 403 "$N5_PORT_A" POST "$N5_TOKEN_A_AGENT" /api/announcements \
		'{"scope":"federation","severity":"warning","title":"a worker speaks for the fabric"}' ||
		return 1

	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"federation","severity":"breaking","title":"the wire format changes on thrumbleaxe day",
		  "body":"every node in the fabric"}' || return 1
	want_eq "the node that wrote it" "$(jqv .announcement.node)" nodeA || return 1
	remember5 FED_ANN "$(jqv .announcement.id)"
	remember5 FED_ANN_HLC "$(jqv .announcement.hlc)"

	want_napi 200 "$N5_PORT_A" POST "$N5_TOKEN_A_SYSTEM" /api/announcements \
		'{"scope":"node","severity":"warning","title":"nodeA reboots at midnight",
		  "body":"this node and no other"}' || return 1
	remember5 LOCAL_ANN "$(jqv .announcement.id)"

	# Both are on A, in the same project, with the same visibility. From here on
	# the only thing that differs between them is the scope.
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_A" /api/announcements || return 1
	want_eq "A sees both of them" \
		"$(printf '%s' "$API_BODY" | jq '[.announcements[] | select(.type == "announcement")] | length >= 2')" \
		true || return 1
	printf 'a federation announcement and a node one, both in pa on nodeA\n'
}

the_scope_decides_what_crosses_the_boundary() {
	recall5
	sync_round || return 1

	# The federation one is on B, under nodeA's name, with the scope it was
	# written with - which is inside the signature, so a relay could not have
	# widened a node announcement into this on the way past.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/artifact/$FED_ANN" || return 1
	want_eq "the title" "$(jqv .title)" "the wire format changes on thrumbleaxe day" || return 1
	want_eq "the node that wrote it" "$(jqv .node)" nodeA || return 1
	want_eq "the scope it carries" "$(jqv .fields.scope)" federation || return 1
	want_eq "the severity" "$(jqv .severity)" breaking || return 1
	# And under nodeA's signature, not B's copy of it.
	want_eq "the signature came with it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts
		                         WHERE id = '$FED_ANN' AND node = 'nodeA' AND sig IS NOT NULL")" \
		1 || return 1

	# The node one is not on B, and not as a row B is merely hiding either.
	want_napi 404 "$N5_PORT_B" GET "$N5_TOKEN_A" "/api/artifact/$LOCAL_ANN" || return 1
	want_eq "rows for it in B's table" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$LOCAL_ANN'")" 0 || return 1

	# The first door: A's pull does not offer it, however much of the delta the
	# peer asks for. The federation one is in the same page, so what is missing
	# is the scope rule rather than the cursor.
	want_napi 200 "$N5_PORT_A" GET "$N5_TOKEN_B" \
		"/api/sync/pull?since=$((FED_ANN_HLC - 1))" || return 1
	want_eq "the node announcement in the delta" \
		"$(printf '%s' "$API_BODY" | jq '[.artifacts[] | select(.id == "'"$LOCAL_ANN"'")] | length')" \
		0 || return 1
	want_eq "the federation one in the same delta" \
		"$(printf '%s' "$API_BODY" | jq '[.artifacts[] | select(.id == "'"$FED_ANN"'")] | length')" \
		1 || return 1
	printf 'the federation announcement replicated; the node one did not leave nodeA\n'
}

# The second door. This row is correctly signed by nodeA, whose key B has
# pinned, and pushed by the principal B takes deltas from - so authenticity and
# authorisation both pass, and it is refused anyway.
a_pushed_node_announcement_is_refused_at_the_door() {
	recall5
	napi "$N5_PORT_B" GET "" /healthz || return 1
	local hlc id delta
	hlc="$(jqv .hlc)"
	id="pushed-node-ann-$$"
	delta="$(printf '{"artifacts":[{"id":"%s","type":"announcement","kind":"node","project":"pb","owner_user":"%s","title":"nodeB is going down, says nodeA","body":"","status":"active","severity":"breaking","visibility":"shared","fields":{"scope":"node"},"hlc":%d,"node":"nodeA","tombstone":false}],"events":[],"tasks":[],"grants":[],"hwm":0}' \
		"$id" "$N5_USER_B" "$((hlc + 65536))" | sign5 "$N5_DSN_A" nodeA)" || return 1

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "artifacts refused" "$(jqv '.refused.artifacts')" 1 || return 1
	want_eq "artifacts applied" "$(jqv '.applied.artifacts')" 0 || return 1
	want_eq "rows in B's table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$id'")" 0 || return 1
	case "$(jqv '.reasons[0]')" in
	*node-scope*) ;;
	*)
		printf 'it was refused, but not as a node-scope announcement: %s\n' "$(jqv '.reasons[0]')" >&2
		return 1
		;;
	esac
	printf 'a properly signed node announcement is still refused at the push door\n'
}

# A federation announcement is the one message on this fabric that one process
# says and every node then shows to everybody, so it is the one worth forging.
# It is signed by a node nobody here has ever heard of, wearing nodeA's name.
a_forged_federation_announcement_is_refused() {
	recall5
	napi "$N5_PORT_B" GET "" /healthz || return 1
	local hlc id delta
	hlc="$(jqv .hlc)"
	id="forged-ann-$$"
	delta="$(printf '{"artifacts":[{"id":"%s","type":"announcement","kind":"federation","project":"pb","owner_user":"%s","title":"every node is being decommissioned","body":"","status":"active","severity":"breaking","visibility":"shared","fields":{"scope":"federation"},"hlc":%d,"node":"nodeA","tombstone":false}],"events":[],"tasks":[],"grants":[],"hwm":0}' \
		"$id" "$N5_USER_B" "$((hlc + 65536))" | sign_seed "$(seed_of announcement-forger)")" || return 1

	want_napi 200 "$N5_PORT_B" POST "$N5_TOKEN_B" /api/sync/push "$delta" || return 1
	want_eq "artifacts refused" "$(jqv '.refused.artifacts')" 1 || return 1
	want_eq "artifacts applied" "$(jqv '.applied.artifacts')" 0 || return 1
	want_eq "rows in B's table for it" \
		"$(scalar5 "$N5_DSN_B" "SELECT count(*) FROM artifacts WHERE id = '$id'")" 0 || return 1
	# And nobody is being shown it: the banner reads the active list, and the
	# forgery is not in it.
	want_napi 200 "$N5_PORT_B" GET "$N5_TOKEN_B" /api/announcements || return 1
	want_eq "the forgery in B's banner" \
		"$(printf '%s' "$API_BODY" | jq '[.announcements[] | select(.id == "'"$id"'")] | length')" \
		0 || return 1
	printf 'the forged federation announcement was refused: %s\n' "$(jqv '.reasons[0]')"
}

say "system agents: what an agent is for, as opposed to what it runs"
check "an agent with no kind is a worker, and the kind reaches the principal" \
	the_agent_kind_defaults_to_worker
check "a kind nothing implements is refused, and a person is not an agent" \
	go test -count=1 -run TestAnAgentWithNoKindIsAWorker ./internal/store
check "only a system agent posts to the fabric, and only through its own door" \
	only_a_system_agent_announces_to_the_fabric

say "announcements: scope, severity and a window"
check "an announcement says what it is, or it is refused" an_announcement_says_what_it_is
check "the quiesce log is minted, not typed" the_quiesce_log_is_not_something_a_client_types
check "the console banner shows an active announcement above the room" \
	the_console_banner_shows_an_active_announcement
check "and clears when it is resolved, with the window on the row" \
	the_banner_clears_when_the_announcement_is_resolved

say "quiesce: the change waits for the people it is about to affect"
check "an ack-required maintenance announcement holds its resource" \
	an_ack_required_announcement_holds_its_resource
check "and releases it when the dependent acks" and_releases_it_when_the_dependent_acks
check "a drain announcement releases when the holder lets go" \
	a_drain_announcement_releases_when_the_holder_lets_go

say "announcements across the two nodes phase 5 stood up"
check "a system agent posts one to the fabric and one to its own node" \
	a_system_agent_announces_across_the_fabric
check "the federation one replicates and the node one does not" \
	the_scope_decides_what_crosses_the_boundary
check "a node announcement pushed by a peer is refused at the door" \
	a_pushed_node_announcement_is_refused_at_the_door
check "neither door offers nor takes a node-scope announcement" \
	go test -count=1 -run TestANodeAnnouncementCrossesNeitherDoor ./internal/store
check "an unreadable scope does not travel" \
	go test -count=1 -run TestAnUnreadableScopeDoesNotTravel ./internal/store
check "a forged federation announcement is refused on merge" \
	a_forged_federation_announcement_is_refused
check "node A survived the announcements" kill -0 "$NODE5A_PID"
check "node B survived the announcements" kill -0 "$NODE5B_PID"

say "whose word a row is"
check "a principal gets a signing key on the node it writes from, and the peer pins it" \
	a_principal_gets_a_key_on_the_node_it_writes_from
check "a message written where the key is lands on the other node as that person's own" \
	a_message_from_the_node_holding_the_key_is_authored
check "a pinned peer cannot speak for a principal with a key, and rows below the epoch still land" \
	a_pinned_peer_cannot_speak_for_a_principal_with_a_key
check "a well-formed signature by the wrong key is refused, and hers is not" \
	a_signature_that_is_not_the_principals_is_not_authorship
check "a rewrite of what somebody wrote is refused, and an edit they signed is not" \
	a_rewrite_of_what_somebody_wrote_is_refused

say "and the refusal stands"
check "a refused row stays refused when it is re-offered, and after a pin that would have allowed it" \
	a_refusal_is_terminal_for_the_claim_it_refused
check "the same content carrying the author's own signature is a different claim, and lands" \
	the_same_content_with_the_authors_signature_still_lands
check "the refused claims are reported, count and reason, where the rows would have been read" \
	the_refused_claims_are_counted_where_a_reader_would_have_seen_them
check "a refusal survives a moved epoch, a removed key and a re-offer, in the store" \
	go test -count=1 -run 'TestARefusedClaimIsRefusedAgain|TestAMovedEpochDoesNotResurrectARefusedRow|TestARemovedKeyDoesNotResurrectARefusedRow|TestTheSameContentSignedByItsAuthorIsADifferentClaim|TestARewrittenArtifactStaysRefusedAfterTheEpochMoves|TestWhatWasRefusedIsCountedWhereTheRowWouldHaveBeenRead|TestAClaimIsThePrincipalTheBytesAndTheSignature' \
	./internal/store
check "the same rules in the store, row by row" \
	go test -count=1 -run 'TestAPinnedNodeCannotSpeakForAPrincipalWithAKey|TestARowBelowTheEpochIsStillTaken|TestAPrincipalSignedEventIsAuthored|TestTheEpochHoldsWhoeverIsCarryingTheRow|TestALocalWriteCarriesThePrincipalsSignature|TestAPartysWriteKeepsTheOwnersSignature|TestARewrittenArtifactIsRefusedAfterTheEpoch|TestAPrincipalKeyIsNotReplacedInPlace' \
	./internal/store
check "an authorship signature is not a node signature, and covers what its owner writes" \
	go test -count=1 -run 'TestAnAuthorshipMessageIsNotTheRowsOwnMessage|TestAnArtifactsAuthorshipCoversTheOwnersFieldsAndNoOthers' \
	./internal/sign
check "the terminal draws a speaker nobody signed for as attributed" \
	go test -count=1 -run TestASpeakerNobodySignedForIsDrawnAsAttributed ./internal/tui
check "node A survived the authorship checks" kill -0 "$NODE5A_PID"
check "node B survived the authorship checks" kill -0 "$NODE5B_PID"

# -------------------------------------------------------------- phase 10 tui

say "the terminal client"
check "the tui reaches the node only through the HTTP API" tui_talks_only_to_the_api
check "flowy tui refuses to start with no token anywhere" tui_needs_a_token
check "a second waiter for one name is refused, and says which pid holds it" \
	a_second_waiter_for_one_name_is_refused
check "a message, a memory, a report, two todos and a task are seeded for the tui" tui_seed
check "the tui, driven headless by the keyboard against the live node" tui_headless
check "a token the node refuses is a status line, not a panic" \
	tui_headless_refuses_a_bad_token
check "flowy tui on a real pty: two resizes, q quits, the terminal is restored" \
	"$WORK/smoke" tui-pty "$ROOT/flowy" "http://127.0.0.1:$HTTP_PORT" "${TOKEN_A:-}"
check "the node survived the terminal client" kill -0 "$SERVE_PID"

# --------------------------------------------- the queue across projects
#
# Last, because it writes todos that every earlier count would have to know
# about. See the section of the same name above for what the pair of principals
# is for.
# First, while the pc token's queue is still legitimately empty: the seed below
# is what fills it.
check "an empty queue says which empty it is, and how far it looked" \
	console_says_which_empty_the_queue_is
check "two projects hold a todo each, one of them shared across the grant" \
	two_projects_hold_a_todo_each
check "the queue spans projects through the one query and the one filter" \
	the_queue_spans_projects_through_the_one_filter
check "what a token READS is narrower than what the registry SHOWS it" \
	the_reach_is_narrower_than_the_enumeration
check "the page lists both projects for the reader who reaches both, one for the other, and says which" \
	console_lists_the_queue_across_projects
check "signed out, the queue says so instead of reading as no work" \
	console_todos_signed_out

# ------------------------------------------------------- the deploy scripts
#
# THE GUARDS WERE NEVER TESTED. scripts/deploy.sh and scripts/migrate.sh exist
# because the same deploy mistakes kept landing - a console 35 minutes stale
# because go:embed took whatever was in web/dist, and a node serving 500s for
# four minutes because schema.sql was applied after the binary rather than
# before. Both scripts are guards written after an outage, and neither was run
# by anything: a guard nobody exercises is a guard that rots quietly and fails
# on the night it is needed, which is precisely the shape it was written for.
#
# What is checked here is what CAN be checked without a live node: that the
# scripts parse, that they meet the bar every persistent script here meets, and
# that their REFUSALS still refuse. The refusals are the load-bearing half -
# "not on master", "tree is dirty", "no database named" are what stop a deploy
# from shipping somebody else's work in progress or migrating a database
# nobody asked about.

repo_shell_scripts() {
	# The repo's own scripts. vendor/ is other people's code and its style is
	# not ours to enforce; run-tests.sh itself is included, because it is the
	# most load-bearing script here.
	printf '%s\n' ./run-tests.sh ./scripts/deploy.sh ./scripts/migrate.sh \
		./scripts/waiter-spin-test.sh ./scripts/build-sut.sh \
		./deploy/bootstrap.sh ./deploy/handoff-runner.sh
}

shell_scripts_parse() {
	local script
	while read -r script; do
		bash -n "$script" || return 1
	done < <(repo_shell_scripts)
}

shell_scripts_lint() {
	# MISSING TOOLING IS SAID OUT LOUD AND NOT PASSED OVER. A check that exits
	# 0 because the thing it checks with is absent reads as a pass, which is
	# how "the suite is green" and "the suite ran" came apart here before.
	if ! command -v shellcheck >/dev/null 2>&1; then
		# A NAMED FAILURE, NOT A QUIET PASS. The comment above said absent
		# tooling must not read as a pass and the code returned 0 anyway,
		# which is the same defect it was warning about - and the suite's own
		# convention elsewhere is that a skip IS a failure. Both tools are in
		# the gate image today, so this changes no verdict; it removes the
		# path where losing them turns two checks into silence.
		printf 'shellcheck is not installed, so the scripts were NOT CHECKED - install it (mise) or this suite is lying about them\n'
		return 1
	fi
	# --severity=warning, not the default. The default reports info-level
	# style notes too, and this suite is not the place to hold anybody's
	# hand about single quotes in a jq program - what it must refuse is the
	# class that actually breaks a script at 3am: unquoted expansions, an
	# unchecked cd, a masked pipeline status. Every script in the list is clean
	# at this level today, so a new warning means somebody added one.
	local script
	while read -r script; do
		shellcheck --severity=warning "$script" || return 1
	done < <(repo_shell_scripts)
}

shell_scripts_formatted() {
	if ! command -v shfmt >/dev/null 2>&1; then
		printf 'shfmt is not installed, so the scripts were NOT CHECKED - install it (mise) or this suite is lying about them\n'
		return 1
	fi
	# -d prints the diff and exits non-zero, so a script somebody hand-edited
	# out of shape fails with the change it needs attached rather than with a
	# bare "reformat it".
	local script
	while read -r script; do
		shfmt -d "$script" || return 1
	done < <(repo_shell_scripts)
}

# ---------------------------------------------------------------- land guard
#
# Every rule this fleet built for landing lives inside the land verb, and on
# 18 Aug master moved by a plain `git merge` while another agent held the lock
# for a thirteen-commit batch. Measured after: five merge.gate events on the
# row, ZERO merge.land, no merge_lands row for the new tip, and the row closed
# with an empty landed_tip. Nothing was bypassed - nothing had to be used.
#
# So the guard sits in a reference-transaction hook, where the ref actually
# changes, and these checks drive it rather than describe it. The node is a
# stub: what is under test is the hook's decision, and a stub is the only way
# to put "the lock is held by somebody else" on the table on demand.

# guard_stub starts a fake node answering one lock and one identity, and prints
# its pid. Killed by its caller.
guard_stub() {
	local port="$1" lock="$2"
	STUB_LOCK="$lock" python3 -c '
import json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
lock = json.loads(os.environ["STUB_LOCK"])
class H(BaseHTTPRequestHandler):
    def do_GET(self):
        body = {"lock": lock} if self.path.startswith("/api/merge-queue") else {"user": "u1", "agent": "a1"}
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)
    def log_message(self, *a):
        pass
HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
' "$port" >/dev/null 2>&1 &
	# THE CHILD'S STDOUT IS CLOSED ON PURPOSE. This is called as
	# pid="$(guard_stub ...)", and a background child that inherits the
	# substitution's pipe holds it open, so the substitution never returns and
	# the suite hangs with no output and no failure - which is how it hung the
	# first time it was run.
	printf '%s' "$!"
}

# guard_says runs the hook on one ref update and answers with its exit status.
guard_says() {
	local addr="$1" ref="$2" token="${3-}" off="${4:-on}"
	printf 'aaa bbb %s\n' "$ref" |
		env FLOWY_ADDR="$addr" FLOWY_TOKEN="$token" FLOWY_LAND_GUARD="$off" \
			bash "$ROOT/scripts/land-guard.sh" prepared >/dev/null 2>&1
	printf '%s' "$?"
}

the_land_guard_refuses_what_it_should() {
	recall
	local dead=http://127.0.0.1:9 pid rc

	# A branch nobody shares moves freely. A guard that made every commit ask a
	# server would be removed within the hour, and then it guards nothing.
	want_eq "a feature branch is not the guard's business" \
		"$(guard_says "$dead" refs/heads/feature tok)" 0 || return 1

	# Each of these is a REFUSAL, and each is a way the guard could otherwise
	# turn itself off exactly when nobody is watching.
	want_eq "no token is a refusal, not a pass" \
		"$(guard_says "$dead" refs/heads/master "")" 1 || return 1
	want_eq "a node that does not answer is a refusal" \
		"$(guard_says "$dead" refs/heads/master tok)" 1 || return 1

	pid="$(guard_stub 9101 '{"held": false}')"
	sleep 1
	rc="$(guard_says http://127.0.0.1:9101 refs/heads/master tok)"
	kill "$pid" 2>/dev/null || true
	want_eq "a free lock is a refusal - a landing goes through a declaration" "$rc" 1 || return 1

	pid="$(guard_stub 9102 '{"held": true, "holder": "somebody", "holder_name": "flowy-glm", "item": "01ROW"}')"
	sleep 1
	rc="$(guard_says http://127.0.0.1:9102 refs/heads/master tok)"
	kill "$pid" 2>/dev/null || true
	want_eq "somebody else's lock is a refusal" "$rc" 1 || return 1

	# The positive control. Without it every line above passes on a guard that
	# refuses everything, which is not a guard, it is a broken repository.
	pid="$(guard_stub 9103 '{"held": true, "holder": "a1", "holder_name": "me", "item": "01ROW"}')"
	sleep 1
	rc="$(guard_says http://127.0.0.1:9103 refs/heads/master tok)"
	kill "$pid" 2>/dev/null || true
	want_eq "the holder may move it" "$rc" 0 || return 1

	# And the way out, which is deliberate: a guard with no override gets
	# uninstalled the first time it is wrong at three in the morning.
	want_eq "the override lets it through" \
		"$(guard_says "$dead" refs/heads/master tok off)" 0 || return 1
}

# The half that matters: GIT ITSELF refuses. The checks above measure a script;
# this measures the thing the script was written to stop, in the form it took -
# a fast-forward of master by somebody holding no lock.
git_refuses_to_move_master_without_the_lock() {
	recall
	local repo pid out before
	repo="$(mktemp -d)" || return 1
	(
		cd "$repo" || exit 1
		git init -q -b master .
		git config user.email a@b
		git config user.name a
		git config commit.gpgsign false
		echo one >f
		git add f
		git commit -qm one
		git checkout -qb feature
		echo two >f
		git commit -qam two
		git checkout -q master
	) || {
		rm -rf "$repo"
		return 1
	}
	bash "$ROOT/scripts/install-land-guard.sh" "$repo" >/dev/null || {
		rm -rf "$repo"
		return 1
	}
	before="$(git -C "$repo" rev-parse master)"

	pid="$(guard_stub 9104 '{"held": false}')"
	sleep 1
	out="$(cd "$repo" && FLOWY_ADDR=http://127.0.0.1:9104 FLOWY_TOKEN=tok git merge --ff-only feature 2>&1 || true)"
	kill "$pid" 2>/dev/null || true
	printf '%s' "$out" | grep -q REFUSED || {
		printf 'the merge was not refused: %s\n' "$out"
		rm -rf "$repo"
		return 1
	}
	# THE REF DID NOT MOVE, which is the claim. A hook that prints a refusal and
	# lets the transaction commit anyway is the shape of every silent success
	# this suite has had to learn about.
	want_eq "master stayed where it was" "$(git -C "$repo" rev-parse master)" "$before" || {
		rm -rf "$repo"
		return 1
	}

	# Positive control, same repository: with the lock, the same merge lands.
	pid="$(guard_stub 9105 '{"held": true, "holder": "a1", "holder_name": "me", "item": "01ROW"}')"
	sleep 1
	(cd "$repo" && FLOWY_ADDR=http://127.0.0.1:9105 FLOWY_TOKEN=tok git merge --ff-only feature >/dev/null 2>&1) || true
	kill "$pid" 2>/dev/null || true
	if [ "$(git -C "$repo" rev-parse master)" != "$(git -C "$repo" rev-parse feature)" ]; then
		printf 'the holder could not land - the guard refuses everything\n'
		rm -rf "$repo"
		return 1
	fi
	rm -rf "$repo"
}

deploy_refuses_misuse() {
	local out status
	out=$(./scripts/deploy.sh --wat 2>&1)
	status=$?
	[ "$status" -eq 2 ] || {
		printf 'an unknown argument exited %d, want 2:\n%s\n' "$status" "$out"
		return 1
	}
	printf '%s\n' "$out" | grep -q 'usage' || {
		printf 'the refusal does not say how to call it:\n%s\n' "$out"
		return 1
	}
}

deploy_refuses_off_master() {
	# THE ONE THAT MATTERS HERE. The gate runs on a branch, never on master, so
	# this is the refusal every gate run is in a position to exercise: a deploy
	# from a branch would ship whatever that branch happens to be, and four
	# agents share the checkout it reads. If this ever passes silently, a
	# spawned agent can deploy its own unlanded work to the node everyone uses.
	# On master there is nothing to refuse, and a dry run there would go on to
	# npm ci and a full build - minutes, for a check about a guard that does
	# not apply. Said out loud rather than passed over, so a run that skipped
	# it does not read the same as a run that exercised it.
	if [ "$(git rev-parse --abbrev-ref HEAD)" = "master" ]; then
		printf 'HEAD is master, so there is no off-master refusal to make - NOT CHECKED\n'
		return 0
	fi
	local out status
	out=$(FLOWY_REPO="$PWD" ./scripts/deploy.sh --dry-run 2>&1)
	status=$?
	[ "$status" -eq 1 ] || {
		printf 'a dry run on branch %s exited %d, want a refusal (1):\n%s\n' \
			"$(git rev-parse --abbrev-ref HEAD)" "$status" "$out"
		return 1
	}
	printf '%s\n' "$out" | grep -qE 'master is the only deploy source|uncommitted changes' || {
		printf 'it refused for some other reason than the branch or the tree:\n%s\n' "$out"
		return 1
	}
}

migrate_refuses_without_a_database() {
	# It has to name a database or refuse. Guessing one is how a migration
	# lands somewhere the node never looks and the deploy reports success.
	#
	# DATABASE_URL IS CLEARED, and that is the whole point of the invocation
	# rather than a detail of it. migrate.sh takes its DSN from $DATABASE_URL,
	# and this suite runs with $DATABASE_URL pointing at its own live test
	# database - so an earlier cut of this check, which cleared
	# FLOWY_DATABASE_URL (deploy.sh's variable, not this one), handed
	# migrate.sh the suite's own database and it applied the schema to it
	# mid-run. A check that mutates what the suite is measuring is worse than
	# no check.
	local out status
	out=$(env -u DATABASE_URL FLOWY_LIVE_DIR="$(mktemp -d)" ./scripts/migrate.sh 2>&1)
	status=$?
	[ "$status" -ne 0 ] || {
		printf 'it migrated something with no DSN in sight:\n%s\n' "$out"
		return 1
	}
}

build_sut_refuses_misuse() {
	# WHAT CAN BE CHECKED WITHOUT DOCKER, AND WHY IT IS WORTH CHECKING. An
	# actual build needs a toolchain image, a source checkout and hours; none
	# of that exists in here. What does exist is the front half of the script,
	# and that half is the one the runner meets when something is wrong: it
	# takes a commit sha and a project, and every one of those values is
	# substituted into a path or a git command. A build that quietly runs
	# against the wrong commit, or writes outside the scratch area, is worse
	# than one that never starts, so each of these has to refuse with 2 rather
	# than carry on.
	local case out status
	while read -r case; do
		# shellcheck disable=SC2086  # each case is a deliberately split argv
		out=$(./scripts/build-sut.sh $case 2>&1)
		status=$?
		[ "$status" -eq 2 ] || {
			printf 'build-sut.sh %s exited %d, want a refusal (2):\n%s\n' \
				"$case" "$status" "$out"
			return 1
		}
	done <<-'EOF'

		--wat abc1234def
		--config /nonexistent/nope.env abc1234def
		--project no-such-project abc1234def
		--project serenedb not-a-sha
		--project ../escape abc1234def
	EOF
	out=$(./scripts/build-sut.sh 2>&1) || true
	printf '%s\n' "$out" | grep -q 'usage' || {
		printf 'called with nothing, it does not say how to call it:\n%s\n' "$out"
		return 1
	}
}

build_sut_config_is_complete() {
	# A build-sut config that is missing a value fails at the point a repro
	# run wanted a binary - which is minutes into a queued run, in a log
	# nobody is watching, and reported as a build failure rather than as the
	# typo it is. The four required keys are cheap to assert here instead.
	local f v
	for f in ./scripts/build-sut.d/*.env; do
		[ -e "$f" ] || {
			printf 'no build-sut configs at all - the script has no project to build\n'
			return 1
		}
		for v in SUT_SRC_REPO SUT_BUILD_IMAGE SUT_BUILD_CMD SUT_ARTIFACT; do
			(
				# shellcheck source=/dev/null  # a config file, named by the glob above
				. "$f"
				[ -n "${!v:-}" ]
			) || {
				printf '%s does not set %s\n' "$f" "$v"
				return 1
			}
		done
	done
}

bootstrap_refuses_misuse() {
	# THE SAME REASONING AS deploy.sh, for the script that stands a node up from
	# nothing. It parses its arguments before it touches Docker, so this refusal
	# is checkable in an image with no daemon in it - which is the only part of
	# either deployment script a gate can exercise.
	local out status
	out=$(./deploy/bootstrap.sh --wat 2>&1)
	status=$?
	[ "$status" -eq 2 ] || {
		printf 'an unknown argument exited %d, want 2:\n%s\n' "$status" "$out"
		return 1
	}
	printf '%s\n' "$out" | grep -q 'usage' || {
		printf 'the refusal does not say how to call it:\n%s\n' "$out"
		return 1
	}
}

handoff_runner_refuses_without_config() {
	# deploy/.env holds the database password and is gitignored, so it is never
	# present in a checkout - which makes "no configuration" the state this
	# script meets on any fresh clone, and refusing clearly the behaviour worth
	# pinning. It must not fall through to docker compose with an empty
	# environment, where the failure would name an interpolation variable.
	local out status
	out=$(./deploy/handoff-runner.sh up -d 2>&1)
	status=$?
	[ "$status" -eq 2 ] || {
		printf 'a missing deploy/.env exited %d, want 2:\n%s\n' "$status" "$out"
		return 1
	}
	printf '%s\n' "$out" | grep -q 'bootstrap.sh' || {
		printf 'the refusal does not say how to produce the file:\n%s\n' "$out"
		return 1
	}
}

check "the repo's shell scripts parse" shell_scripts_parse
check "the repo's shell scripts are shellcheck clean" shell_scripts_lint
check "the repo's shell scripts are shfmt clean" shell_scripts_formatted
check "the land guard refuses a move by anybody who does not hold the lock" \
	the_land_guard_refuses_what_it_should
check "git itself will not move master without the lock" \
	git_refuses_to_move_master_without_the_lock
check "deploy.sh refuses an argument it does not know, and says how to call it" \
	deploy_refuses_misuse
check "deploy.sh refuses to deploy anything that is not master" \
	deploy_refuses_off_master
check "migrate.sh refuses rather than guessing which database to migrate" \
	migrate_refuses_without_a_database
check "build-sut.sh refuses a call it cannot build from, and says how to call it" \
	build_sut_refuses_misuse
check "the build-sut config it ships with sets everything a build needs" \
	build_sut_config_is_complete
check "bootstrap.sh refuses an argument it does not know, and says how to call it" \
	bootstrap_refuses_misuse
check "handoff-runner.sh refuses to run with no deploy/.env, and says where it comes from" \
	handoff_runner_refuses_without_config

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
