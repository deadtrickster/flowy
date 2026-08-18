#!/usr/bin/env bash
#
# A throwaway postgres for running the store tests without the gate.
#
# WHY THIS EXISTS. The DB-backed tests skip unless DATABASE_URL is set, so the
# only way to run one was ./run-tests.sh - the whole gate, about 35 minutes, to
# find out that a new test calls a verb with the wrong arguments. Two of mine
# failed that way today on a refusal the store prints in its first sentence.
#
#   eval "$(scripts/dev-db.sh start)"     # exports DATABASE_URL
#   go test ./internal/store/ -run Clocks # about 30 seconds
#   scripts/dev-db.sh stop
#
# IT IS NOT THE GATE AND MUST NOT BE MISTAKEN FOR IT. This starts one database
# and loads schema.sql. The gate builds the console, runs the browser checks,
# the upgrade path, the shell lint and the full suite against a database it
# builds the same way. A green run here means your test compiles and its verbs
# line up; it says nothing about the other 600 checks. Gate before you record a
# verdict - the pre-gate stamp exists so the verdict names the tree that was
# measured.
#
# The socket goes in /tmp rather than beside the data directory: a unix socket
# path is capped at 107 bytes and a scratch directory under a session id is
# already longer than that, which fails as "could not create any Unix-domain
# sockets" with the port line right above it looking healthy.
set -euo pipefail

PG_BIN=${PG_BIN:-$HOME/.local/pg17-bin}
export LD_LIBRARY_PATH=${LD_LIBRARY_PATH:-$HOME/.local/pg17-libs}

work=${DEV_DB_DIR:-${TMPDIR:-/tmp}/flowy-dev-db}
port=${DEV_DB_PORT:-55437}
data=$work/data
dsn="postgres://postgres@127.0.0.1:$port/flowy?sslmode=disable"

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

usage() {
	printf 'usage: %s start|stop|dsn\n' "$0" >&2
	exit 2
}

start() {
	# ALREADY RUNNING IS NOT ALREADY READY. The first cut returned here as soon
	# as pg_ctl said the server was up, and a data directory left behind by a
	# run that died before loading the schema then printed a DSN for an empty
	# database - "relation users does not exist" from a script whose whole job
	# is to hand you a working one. schema.sql is CREATE TABLE IF NOT EXISTS
	# throughout, so loading it again is cheap and makes ready mean ready.
	if [ -d "$data" ] && "$PG_BIN/pg_ctl" -D "$data" status >/dev/null 2>&1; then
		load_schema
		printf 'export DATABASE_URL=%q\n' "$dsn"
		return 0
	fi
	rm -rf "$work"
	mkdir -p "$work"
	"$PG_BIN/initdb" -D "$data" -U postgres -A trust -E UTF8 --locale=C --no-sync \
		>"$work/initdb.log" 2>&1 || {
		cat "$work/initdb.log" >&2
		exit 1
	}
	"$PG_BIN/pg_ctl" -D "$data" -l "$work/pg.log" -w -t 60 \
		-o "-p $port -k /tmp -c listen_addresses=127.0.0.1" start >/dev/null || {
		cat "$work/pg.log" >&2
		exit 1
	}
	"$PG_BIN/createdb" -h 127.0.0.1 -p "$port" -U postgres flowy
	load_schema
	printf 'export DATABASE_URL=%q\n' "$dsn"
}

# ON_ERROR_STOP, because a schema that half-loaded is a database that fails
# tests for a reason that has nothing to do with the tests.
load_schema() {
	"$PG_BIN/psql" "$dsn" -q -v ON_ERROR_STOP=1 -f "$here/schema.sql" >"$work/schema.log" 2>&1 || {
		tail -20 "$work/schema.log" >&2
		exit 1
	}
}

stop() {
	if [ -d "$data" ]; then
		"$PG_BIN/pg_ctl" -D "$data" -m immediate -w stop >/dev/null 2>&1 || true
	fi
	rm -rf "$work"
}

case "${1:-}" in
start) start ;;
stop) stop ;;
dsn) printf '%s\n' "$dsn" ;;
*) usage ;;
esac
