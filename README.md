# flowy - Handoff Fabric node (Phase 0)

The host-side node: one Go binary, one Postgres-wire database, and the schema
spine every later phase rides on. Phase 0 is the skeleton - a server that
answers `/healthz` against the store, monotonic ULIDs, a hybrid logical clock,
and stubs where the MCP, FUSE and sync surfaces will go.

## Run the gate

```sh
./run-tests.sh
```

It needs `go` and a Postgres installation (`initdb`, `pg_ctl`, `psql`) and
nothing else - no network, no running database, no systemd. It creates a
throwaway cluster in a temp directory on a free port, loads `schema.sql`, builds
the binary, runs the unit tests, starts `flowy serve`, runs the live checks
against it, then tears the whole thing down in a trap. It prints PASS or FAIL
per check and ends with `passed: N failed: M`, exiting non-zero if anything
failed.

On Ubuntu the dependencies are:

```sh
apt-get install -y golang-go postgresql postgresql-client git
```

## Run the node

```sh
export DATABASE_URL='postgres://user@127.0.0.1:5432/flowy?sslmode=disable'
psql "$DATABASE_URL" -f schema.sql
go build -o flowy .
./flowy serve                      # or: ./flowy serve -addr 127.0.0.1:8787
curl -s 127.0.0.1:8787/healthz     # {"ok":true,...}
```

## Subcommands

| command | what it does |
| --- | --- |
| `flowy serve` | HTTP server, wired to the store |
| `flowy mcp` | prints `mcp: not yet` (Phase 2 surface for agents) |
| `flowy fuse` | prints `fuse: not yet` (artifacts as a filesystem) |
| `flowy sync` | prints `sync: not yet` (peer replication over `seq_hlc`) |
| `flowy version` | build version |
| `flowy help` | usage |

`serve` reads its configuration from the environment, and flags override it:

| env | flag | default |
| --- | --- | --- |
| `DATABASE_URL` | `-dsn` | none; required |
| `FLOWY_ADDR` | `-addr` | `127.0.0.1:8787` |
| `FLOWY_NODE` | `-node` | the hostname |

Routes: `GET /healthz` (add `?counts=1` for per-table row counts), `GET
/version`, `GET /` for the route list.

## Schema spine

`schema.sql` is the whole of Phase 0's persistence. Three decisions run through
every table:

- **Text ULID primary keys.** Ids are minted by whichever node is holding the
  pen, sort by the time they were minted, and never collide, so two nodes can
  write without asking each other first.
- **`hlc bigint` + `node text` on every mutable row.** A hybrid logical clock
  reading packed into one sortable integer, plus the node that stamped it. That
  is enough for a later phase to merge concurrent edits and to break ties the
  same way on both sides.
- **`events` is append-only and carries the thread DAG** in `parents text[]`.
  No parents opens a thread, one continues it, several merge branches. Peers
  page through the log by `seq_hlc`.

| table | holds |
| --- | --- |
| `users` | people; `auto_delegate` decides whether work can go straight to an agent |
| `agents` | agents acting for a user; `kind` is `claude`\|`glm`\|`opencode` |
| `grants` | cross-project capabilities, tombstoned rather than deleted |
| `artifacts` | transcripts, memories, chats, bugs, features, notes |
| `events` | the append-only log and its DAG |
| `tasks` | handoffs between users, optionally assigned to an agent |
| `peers` | replication bookmarks, one row per peer node |

An artifact with `project` NULL is personal to `owner_user`; `visibility` is
`personal`, `project` or `shared`. Indexes cover the reads the later phases do:
artifacts by `(project, type)`, events by `thread` and by `seq_hlc`, plus owner,
grant direction and task inbox.

## Packages

- `internal/ulid` - 48-bit millisecond timestamp, 80 bits of randomness,
  Crockford base32, 26 characters. Monotonic inside a process: ids minted in the
  same millisecond increment the random component instead of redrawing it, so
  generation order and sort order agree. A backwards wall clock cannot produce a
  smaller id.
- `internal/hlc` - hybrid logical clock. `Now()` takes the larger of the local
  wall clock and the last reading, incrementing a logical counter on ties;
  `Update(remote)` merges a peer's timestamp and lands above both. `Pack()`
  folds `{wall_ms, logical}` into `wall_ms<<16 | logical`, which is what the
  `hlc` and `seq_hlc` columns store. Mutex-guarded, so it stays monotonic under
  concurrent goroutines.
- `internal/store` - the Postgres-wire persistence layer. Stamps id, clock and
  node on the way in; reads rows back with their arrays and jsonb intact.
- `cmd/smoke` - the live checks the gate runs against a running node.

## What the gate asserts

`schema.sql` loads and reloads, `go build`, `gofmt`, `go vet`, `go test`, then
against a live `flowy serve`:

- `/healthz` comes up and reports `ok:true` with the database up
- the seven spine tables exist
- `mcp`, `fuse` and `sync` print their placeholder and exit zero
- 10000 ULIDs are unique and strictly increasing - sorted order equals
  generation order
- 8 goroutines minting 5000 HLC readings each produce 40000 distinct values,
  none going backwards, and a merge from a peer that is ahead still lands above
  everything already handed out
- a user, an agent, a `type=bug` artifact in `project='flowy'` and two events
  insert and read back, with `parents` surviving as a `text[]` - including a
  two-parent merge event
- a personal artifact (`project` NULL, `visibility='personal'`) inserts, reads
  back and is selectable by `project IS NULL`
- `psql`, as a second client over the wire, sees all of the above

## Deployment

The store speaks the Postgres wire and nothing else. Nothing in `schema.sql`
depends on Postgres the storage engine - no extensions, no partitioning, no
engine-specific index methods, no identity columns, no triggers - so deploying
against a SereneDB node is a change of DSN:

```sh
export DATABASE_URL='postgres://user@serenedb-host:5432/flowy?sslmode=disable'
```

The gate itself runs against stock Postgres, which is the point: the SQL has to
be portable enough to pass on both.

## Phase 0 status

Green. `./run-tests.sh` reports `passed: 21 failed: 0` on Ubuntu 24.04 with Go
1.22 and Postgres 16.
