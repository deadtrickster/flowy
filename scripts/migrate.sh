#!/usr/bin/env bash
# Bring one database up to this checkout's schema.sql, and SAY what that changed.
#
# THE OUTAGE THIS COMES FROM. The refusal-ledger work added a table. The live
# node was redeployed with the binary that queries it, onto a database that
# never got the table, and every /api/artifacts read returned 500 for about four
# minutes:
#
#     store: count what was refused: pq: relation "refused_authorship" does not exist
#
# Nothing applied the schema. Applying it was a step in a document, done from
# memory by whoever was deploying, and that day it was not done. /healthz stayed
# 200 through all of it, because healthz does not read anything.
#
# So the step is a script now, and scripts/deploy.sh runs it before it restarts
# the unit. It is also what run-tests.sh runs against a deliberately older
# database, so the thing the gate checks is the thing the deploy does rather
# than a second implementation that agrees with it today.
#
# WHY APPLYING schema.sql WHOLESALE IS THE MIGRATION, and there is no
# migrations table. schema.sql is CREATE TABLE IF NOT EXISTS / CREATE INDEX IF
# NOT EXISTS / ALTER TABLE ADD COLUMN IF NOT EXISTS throughout, and the whole
# file is one BEGIN/COMMIT, so applying it to a database at any earlier state is
# idempotent and atomic: it adds what is missing, it skips what is there, and it
# either does all of that or none of it. An ordered-migrations table would buy
# two things this schema does not need yet - destructive steps (a DROP, a
# RENAME, a backfill, a NOT NULL added to a populated column), which cannot be
# expressed idempotently and which schema.sql contains none of, and a record of
# which steps ran, which the catalogue itself answers here. It would also buy a
# new way to be wrong: a migration numbered and applied on one node and not
# another. When the first destructive change lands, that is the day to build it,
# and the fingerprint check in the gate is what will refuse the change until it
# is built - a DROP in schema.sql does not converge an existing database, and
# the gate diffs an existing database against a fresh one.
#
#   scripts/migrate.sh [DSN]
#
# DSN defaults to $DATABASE_URL. Exit 0 applied, 1 refused or failed, 2 misused.

set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$HERE")"
SCHEMA="$ROOT/schema.sql"
FINGERPRINT="$HERE/schema-fingerprint.sql"

say() { printf '%s\n' "$*"; }
die() {
	printf 'REFUSED: %s\n' "$*" >&2
	exit 1
}

# shellcheck disable=SC2016 # the usage line names the variable, it does not read it
usage='usage: migrate.sh [DSN]   (DSN defaults to $DATABASE_URL)'
case "${1:-}" in
-h | --help)
	printf '%s\n' "$usage"
	exit 0
	;;
-*)
	printf '%s\n' "$usage" >&2
	exit 2
	;;
esac

dsn="${1:-${DATABASE_URL:-}}"
[ -n "$dsn" ] || die "no database: pass a DSN or set DATABASE_URL"
[ -f "$SCHEMA" ] || die "no schema.sql at $SCHEMA"
[ -f "$FINGERPRINT" ] || die "no fingerprint query at $FINGERPRINT"
# PSQL, OR A CONTAINER THAT HAS ONE. The dogfood database runs as a docker
# container (postgres:18 on 127.0.0.1:5433) and this host has no postgres client
# installed at all, so a deploy from here refused at this line and could not
# migrate anything. That refusal was CORRECT - it did not install a binary it
# could not migrate for - but it made every deploy from this machine impossible,
# which is a worse outcome than the one it was guarding against.
#
# So: use psql if it is here, otherwise run it inside the container that is
# already serving this DSN's port. FLOWY_PSQL overrides both when somebody knows
# better than this guess. The container form pipes files in on stdin because the
# schema lives on the host and not in the container.
psql_run() { psql "$@"; }
if [ -n "${FLOWY_PSQL:-}" ]; then
	# shellcheck disable=SC2086 # deliberately word-split: FLOWY_PSQL may name a
	# command with arguments, e.g. "docker exec -i somepg psql".
	psql_run() { $FLOWY_PSQL "$@"; }
elif ! command -v psql >/dev/null 2>&1; then
	port=$(printf '%s' "$dsn" | sed -nE 's#.*:([0-9]+)/.*#\1#p')
	container=""
	if command -v docker >/dev/null 2>&1 && [ -n "$port" ]; then
		container=$(docker ps --format '{{.Names}} {{.Ports}}' 2>/dev/null |
			awk -v p=":$port->" '$0 ~ p {print $1; exit}')
	fi
	[ -n "$container" ] || die "psql is not on PATH and no running container publishes port ${port:-?} - install a postgres client, or set FLOWY_PSQL to a command that reaches this database (for example: FLOWY_PSQL=\"docker exec -i somepg psql\")"
	# INSIDE the container the host's published port does not exist - postgres
	# listens on its own 5432 and the DSN we were handed says 5433, which is the
	# mapping seen from out here. So the host/port are dropped and the user and
	# database are taken from the DSN, reaching postgres over the container's own
	# local socket. Passing the host DSN through unchanged connects to nothing.
	dbuser=$(printf '%s' "$dsn" | sed -nE 's#^[a-z]+://([^:@/]+).*#\1#p')
	dbname=$(printf '%s' "$dsn" | sed -nE 's#.*/([^/?]+)(\?.*)?$#\1#p')
	[ -n "$dbname" ] || die "cannot read a database name out of the DSN, so the container fallback has nothing to connect to"
	say "psql is not on PATH - using the container serving port $port: $container (db=$dbname user=${dbuser:-default})"
	# Every call site says -d "${PSQL_DB:-$dsn}", so setting this here is what
	# swaps the host DSN for the plain database name on the container path, and
	# leaves the DSN untouched when a real psql is doing the work.
	PSQL_DB=$dbname
	psql_run() {
		if [ -n "$dbuser" ]; then
			docker exec -i "$container" psql -U "$dbuser" "$@"
		else
			docker exec -i "$container" psql "$@"
		fi
	}
fi

# CREATE ... IF NOT EXISTS is chatty on a re-apply - one NOTICE per object that
# was already there, which is all of them on a routine deploy. The delta below
# is the part worth reading, so the notices are turned down rather than left to
# bury it. Warnings and errors still come through.
export PGOPTIONS="${PGOPTIONS:-} -c client_min_messages=warning"

# REACHABLE FIRST, and refuse rather than carry on. A deploy that cannot reach
# the database must stop here, while the old binary is still serving: the
# alternative is to shrug, restart the unit anyway, and find out from a 500 that
# the schema was never applied. That is the outage, exactly.
errlog="$(mktemp -t flowy-migrate-XXXXXX)" || die "cannot make a temp file"
trap 'rm -f "$errlog"' EXIT
if ! psql_run -v ON_ERROR_STOP=1 -tAq -d "${PSQL_DB:-$dsn}" -c 'SELECT 1' >/dev/null 2>"$errlog"; then
	[ -s "$errlog" ] && sed 's/^/     /' "$errlog" >&2
	die "cannot reach the database"
fi

fingerprint() { psql_run -v ON_ERROR_STOP=1 -tAq -F'|' -d "${PSQL_DB:-$dsn}" <"$FINGERPRINT"; }

before="$(fingerprint)" || die "cannot read the schema catalogue"

say "==> applying schema.sql"
if ! psql_run -v ON_ERROR_STOP=1 -q -d "${PSQL_DB:-$dsn}" <"$SCHEMA"; then
	die "schema.sql did not apply - the database is unchanged (the file is one transaction)"
fi

after="$(fingerprint)" || die "cannot read the schema catalogue back"

# WHAT IT ACTUALLY CHANGED, from the catalogue rather than from the exit status.
# "psql exited 0" is true of applying the schema to a database that already had
# it, and true of applying it to one that was missing a table - the delta is the
# only thing that tells the two apart, and it is what a deploy log should carry.
added="$(comm -13 <(printf '%s\n' "$before" | sort) <(printf '%s\n' "$after" | sort))"
removed="$(comm -23 <(printf '%s\n' "$before" | sort) <(printf '%s\n' "$after" | sort))"

if [ -z "$added" ] && [ -z "$removed" ]; then
	say "    schema already up to date - nothing changed"
	exit 0
fi

if [ -n "$added" ]; then
	say "    added:"
	printf '%s\n' "$added" | sed 's/^/      + /'
fi
# Applying schema.sql cannot drop anything - there is no DROP in it. If this
# ever prints, something else is writing to the database during a deploy, and
# that is worth seeing rather than swallowing.
if [ -n "$removed" ]; then
	say "    REMOVED (schema.sql contains no DROP - somebody else is writing):"
	printf '%s\n' "$removed" | sed 's/^/      - /'
fi
