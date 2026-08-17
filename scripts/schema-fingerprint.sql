-- What a database's schema IS, as text a diff can read.
--
-- This exists because "the schema is up to date" was, until now, a thing
-- somebody remembered rather than a thing anything checked. The gate built its
-- database from schema.sql on every run, so a fresh database always had every
-- new table and the gate could not tell a migrated database from a new one.
-- The live node is the only place an EXISTING database meets NEW code, and the
-- day that produced this file it met it without the table it was about to be
-- asked for.
--
-- One definition, used by two callers, deliberately: scripts/migrate.sh prints
-- the delta it caused, and run-tests.sh diffs an upgraded database against a
-- fresh one. If those two disagreed about what a schema is, the gate would be
-- checking something other than what the deploy does.
--
-- Run it as:  psql -tA -F'|' -f scripts/schema-fingerprint.sql
--
-- Every row is kind|relation|object|definition, sorted, so two databases are
-- the same schema exactly when the two outputs are byte-identical. It reads
-- structure only - no row counts, no data - because a migrated database and a
-- fresh one are never going to hold the same rows, and that difference is not
-- drift.
--
-- COLUMNS are what caught the case worth catching. schema.sql is CREATE TABLE
-- IF NOT EXISTS throughout, so a column added inside a CREATE TABLE body and
-- nowhere else lands on a new database and is silently skipped on an existing
-- one - the table is already there, so the whole statement is a no-op. That is
-- invisible in the SQL, invisible on a fresh database, and a 500 on the node.
-- Comparing columns is what makes it visible before the deploy rather than
-- after.

SELECT 'relation' AS kind,
       c.relname   AS rel,
       ''          AS obj,
       CASE c.relkind
           WHEN 'r' THEN 'table'
           WHEN 'v' THEN 'view'
           WHEN 'm' THEN 'materialized view'
           WHEN 'S' THEN 'sequence'
           WHEN 'p' THEN 'partitioned table'
           ELSE c.relkind::text
       END AS def
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = 'public'
   AND c.relkind IN ('r', 'v', 'm', 'S', 'p')

UNION ALL

SELECT 'column',
       table_name,
       column_name,
       data_type
           || ' null=' || is_nullable
           || ' default=' || coalesce(column_default, '-')
  FROM information_schema.columns
 WHERE table_schema = 'public'

UNION ALL

SELECT 'index', tablename, indexname, indexdef
  FROM pg_indexes
 WHERE schemaname = 'public'

-- Constraints carry the primary keys, the foreign keys and the checks. A
-- CREATE TABLE IF NOT EXISTS that grew a REFERENCES clause is the same trap as
-- a column: no-op on an existing table, present on a new one.
UNION ALL

SELECT 'constraint',
       conrelid::regclass::text,
       conname,
       pg_get_constraintdef(oid)
  FROM pg_constraint
 WHERE connamespace = 'public'::regnamespace

 ORDER BY 1, 2, 3, 4;
