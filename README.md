# flowy - Handoff Fabric node (Phase 2)

The host-side node: one Go binary, one Postgres-wire database, and the schema
spine every later phase rides on. Phase 0 was the skeleton - a server that
answers `/healthz` against the store, monotonic ULIDs, a hybrid logical clock,
and stubs where the MCP, FUSE and sync surfaces will go.

Phase 1 puts a typed API on top of it and a permission middleware in front of
that: a bearer token resolves to a principal, and every read - by id, by list,
by search - is narrowed to what that principal may see, in SQL, before a row
leaves the database.

Phase 2 is the point of the whole thing: **shared memory every agent reads and
writes, reachable over MCP**. `flowy mcp` is a real Model Context Protocol
server with two transports and one set of handlers, so Claude Code, GLM,
opencode and Claude on the web all hit the same rows in the same store, under
the same permission filter.

## Run the gate

```sh
./run-tests.sh
```

It needs `go`, a Postgres installation (`initdb`, `pg_ctl`, `psql`), and `curl`
and `jq` for the HTTP checks - no network, no running database, no systemd. It
creates a throwaway cluster in a temp directory on a free port, loads
`schema.sql`, builds
the binary, runs the unit tests, starts `flowy serve`, runs the live checks
against it, then tears the whole thing down in a trap. It prints PASS or FAIL
per check and ends with `passed: N failed: M`, exiting non-zero if anything
failed. Phase 2 adds an MCP section to it: the gate starts `flowy mcp --http` on
a free port, does a real handshake against it, and separately pipes JSON-RPC
into `flowy mcp` on stdin - both transports, one store.

On Ubuntu the dependencies are:

```sh
apt-get install -y golang-go postgresql postgresql-client curl jq git
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
| `flowy mcp` | MCP server: shared memory over stdio, or `--http :PORT` |
| `flowy fuse` | prints `fuse: not yet` (artifacts as a filesystem) |
| `flowy sync` | prints `sync: not yet` (peer replication over `seq_hlc`) |
| `flowy version` | build version |
| `flowy help` | usage |

`mcp` and `serve` read their configuration from the environment, and flags
override it:

| env | flag | default |
| --- | --- | --- |
| `DATABASE_URL` | `-dsn` | none; required |
| `FLOWY_ADDR` | `-addr` | `127.0.0.1:8787` |
| `FLOWY_NODE` | `-node` | the hostname |
| `FLOWY_OPERATOR` | `-operator` | empty; nobody may use `?scope=all` |
| `FLOWY_TOKEN` | - | the bearer token `flowy mcp` uses over stdio |

`GET /healthz` (add `?counts=1` for per-table row counts), `GET /version` and
`GET /` are open - a health check that needs a credential stops working at the
worst possible moment, and none of the three reads a row of fabric data.
Everything under `/api/` needs a token.

## Shared memory over MCP

```sh
./flowy mcp                                # stdio: what a local client launches
./flowy mcp --http 127.0.0.1:8788          # streamable HTTP: what a remote client reaches
```

Both transports run the same handlers. Over stdio the protocol is
newline-delimited JSON-RPC 2.0 on stdin and stdout - one message per line, and
nothing but the protocol ever goes to stdout, logs included. Over HTTP it is the
same JSON-RPC as the body of a `POST /mcp`, which is how **Claude on the web
connects**: point a connector at the node's `/mcp` URL and give it a token.
`GET /healthz` on the same port says whether the node can reach its store.

The protocol version is `2024-11-05`. Implemented: `initialize`, `ping`,
`tools/list`, `tools/call`, `resources/list`, `resources/read`, and the
`notifications/*` a client sends and nothing answers.

### Identity is the same principal

The token arrives in `Authorization: Bearer <token>` over HTTP and in
`FLOWY_TOKEN` over stdio, and either way it goes through the same
`store.PrincipalForToken` the HTTP API uses, resolving to the same
(user, agent, project) triple. `initialize` works without one - a client has to
be able to learn what this server is before it holds a credential - and
`tools/call` does not: with no principal it is JSON-RPC error `-32001`, because
there is nobody to filter the store for.

### The instructions

`initialize` returns an `instructions` string, and the same text is served as
the resource `flowy://instructions` for clients that ignore it. It is
`instructions.md` in this repository, embedded into the binary, and it is the
document an agent reads instead of guessing: the three scopes, the kinds, tags,
when to store and when to recall.

### The tools

A memory item is an artifact of `type='memory'` with a `kind`. There is no
second table and no second visibility rule - the personal floor that holds for a
bug holds here, and the grant that opens a project opens the memories in it.

| tool | arguments | what it does |
| --- | --- | --- |
| `mem_write` | `title, body, scope?, kind?, tags?, status?, id?` | create an item, or update one by `id` |
| `mem_read` | `id` | one item, or the same answer a missing id gets |
| `mem_search` | `q, scope?, kind?, limit?` | ranked full text over title, body and tags |
| `mem_list` | `scope?, kind?, limit?` | newest first |
| `todos` | `scope?` | `todo`, `feature` and `handoff` items that are not done |

`scope` is `personal` (default), `project` or `shared`, and it is the item's
visibility:

- **personal** - the owner and the agents acting for them, nobody else. The row
  is written with no project at all, so the floor is a property of the data: no
  grant written afterwards can reach it.
- **project** - the principal's home project, which is the only project a token
  can write into.
- **shared** - the project, plus anyone holding a project-wide grant on it or a
  per-artifact share of the item. This is how a memory crosses a boundary.

`kind` is `note` (default), `todo`, `feature` or `handoff`. `mem_write` with an
`id` updates: fields left out keep their values, so closing a todo is
`{"id": "...", "status": "done"}` and nothing else.

Every write is stamped with a ULID, a fresh HLC reading and this node's name,
and appends a `memory.write` event to the log - so a peer paging `seq_hlc` sees
that memory moved without diffing the table.

An id naming something the caller cannot read is refused rather than treated as
a create with a caller-chosen id: an agent that guessed an id would otherwise
overwrite a memory it was never allowed to see. Ids for new items are minted by
the node.

### Connecting a client

Claude Code, opencode and anything else that launches a server as a subprocess:

```json
{"mcpServers": {"flowy": {"command": "/path/to/flowy", "args": ["mcp"],
  "env": {"DATABASE_URL": "postgres://...", "FLOWY_TOKEN": "tA-01J..."}}}}
```

A remote client - Claude on the web - takes the URL of a node running
`flowy mcp --http` and a bearer token. Same store, same tokens, same filter;
the only difference is which end of the pipe the JSON-RPC arrives on.

## Identity

A **principal** is a `(user, agent, project)` triple, resolved from
`Authorization: Bearer <token>` against the `tokens` table. No header, an
unparseable one, or a token that is not in the table is `401` - there is no
anonymous read.

- The token's `project` is the principal's **home project**: the one it reads
  without needing a grant, and the only one it may write into.
- A token that names only an agent inherits that agent's user and project. That
  is what lets an agent read the personal artifacts of the person it works for,
  and it is the whole of "or an agent whose user is the owner".
- The same person working in two projects is two principals, with two tokens.

`GET /api/whoami` echoes the triple back, which is the quickest way to find out
why a read came back empty.

Tokens are local credentials, not fabric state: the table carries no `hlc`/`node`
and Phase 3 will not replicate it.

## The permission model

One rule, written twice: `store.CanRead` in Go, `store.ArtifactFilterSQL` as a
SQL `WHERE` fragment. The filter is what actually runs - it goes into the same
`WHERE` clause as every list and every search, so the database never hands the
node a row the principal may not see. `TestCanReadMatchesSQL` walks the whole
matrix of principals against artifacts and fails if the two ever disagree.

In the order they are applied:

1. **Personal is a floor.** `visibility='personal'`, or no project at all, is
   readable by its owner and by nobody else. No grant reaches through it - the
   rule is a `CASE`, not an `OR`, so when this branch is taken the grant tests
   are not merely false, they are unreachable. Writing an artifact `personal`
   clears its project in the store, so the floor is a property of the data
   rather than a promise of the API.
2. **Your own project is yours.** Same project as the principal, allowed.
3. **Anything else needs a live grant**, either a project-wide one along the
   edge (`from_project` = the reader's project, `to_project` = the artifact's)
   or a share of that one artifact to that one user (`artifact` = the id,
   `subject` = the reader). Tombstoned grants count for nothing.

`?scope=all` bypasses the filter, and only for the node's operator - the user id
in `-operator`/`FLOWY_OPERATOR`. Operator-ness is local configuration, not a
column: it is a fact about who runs this machine, and it must never be a row
that could replicate to another node and hand somebody a view of everything
there. For anybody else the parameter is simply not there.

**An unreadable artifact is `404`, never `403`.** A `403` would confirm that the
id exists, which is exactly the thing the boundary is there not to leak. The
same goes for writing to an id you cannot see.

Writes are narrower than reads, deliberately:

- `owner_user` is the calling principal. The field is accepted so a client can
  be explicit, not so one user can mint another user's personal artifacts.
- An artifact goes into the principal's home project or into no project at all;
  writing into another one is `403`. It would otherwise produce an artifact its
  own author cannot read back, because reads go by project and not by
  authorship.
- Updating an existing artifact needs both: readable (else `404`) and owned
  (else `403`).
- A project-wide grant can only be issued by a principal of the project being
  opened up; a share, only by the artifact's owner. Sharing a personal artifact
  is refused rather than silently ineffective.

Capabilities beyond `read` (`cap` is already on the row) and project membership
as data both belong to the handoff phase; right now `cap` is recorded and not
yet enforced.

## API

All of it is JSON, all of it needs a bearer token, all writes are HLC-stamped
and deletes are tombstones.

| route | what it does |
| --- | --- |
| `POST /api/artifacts` | create, or replace one you own. Body: `type` (required), `kind`, `title`, `body`, `discovery`, `status`, `severity`, `tags`, `user_tags`, `related`, `visibility`, `project`, `file_path`, `fields`, `id?`. A new `id` is a ULID; `hlc` and `node` are stamped |
| `GET /api/artifacts?type=&kind=&project=&status=` | `{"artifacts":[...]}`, permission-filtered, newest first, tombstones omitted |
| `GET /api/artifact/{id}` | the artifact, or `404` if it is missing **or** out of reach |
| `POST /api/artifact/{id}/delete` | tombstone it and bump the clock past the write it removes |
| `GET /api/search?q=&type=&kind=&project=` | `{"query":..., "artifacts":[{..., "rank":...}]}`, ranked and permission-filtered |
| `POST /api/events` | append. Body: `type` (required), `room`, `thread`, `parents`, `actor`, `artifact`, `body`, `meta`. `id` is a ULID, `seq_hlc` comes from the clock, the project is the principal's |
| `GET /api/events?thread=&since=&room=&type=` | `{"events":[...]}` with `seq_hlc > since`, in log order, permission-filtered |
| `POST /api/grants` | issue a capability: `{from_project,to_project}` for a project-wide one, `{artifact,subject}` for a share |
| `GET /api/whoami` | the principal this token resolves to |

On `project` in a create, absent, `null` and a string are three different
things: absent means the home project, `null` means none, which is what personal
is. An update keeps whatever it does not restate.

A thread with no `thread` given is named after its first event. `parents` is the
DAG: none opens a thread, one continues it, several merge. `since` is the same
cursor peer replication will page by, and it is strictly greater, so a caller
hands back the last value it saw.

A tombstoned artifact still answers a `GET` by id, marked `"tombstone": true` -
that is how the delete replicates - but it is gone from every list and every
search.

## Search, and why it is temporary

`artifacts.search` is a `tsvector` over title, body, discovery and tags, with a
GIN index, matched with `plainto_tsquery` and ranked by `ts_rank_cd`. Discovery
is in there on purpose: what an agent found out is often the only place a word
appears, and searching for it has to find the artifact.

This is the one part of the schema that is Postgres the storage engine rather
than Postgres the wire, and it is quarantined in a `SEARCH` section at the
bottom of `schema.sql` - one column and one index, with nothing above depending
on anything below. **Vector search comes with SereneDB**, and when it does that
section is deleted rather than ported.

The column is filled by the node in the same statement that writes the row,
which is neither a generated column nor a trigger. A generated column is not
possible - `array_to_string` is `STABLE`, so Postgres refuses the expression -
and a trigger would drag PL/pgSQL into a schema that promises none. The cost is
that a row written by something other than the node has no search vector; every
write path in the node goes through `store.UpsertArtifact`.

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
| `tokens` | bearer token to `(user, agent, project)`; local, never replicated |
| `grants` | cross-project capabilities, tombstoned rather than deleted |
| `artifacts` | transcripts, memories, chats, bugs, features, notes |
| `events` | the append-only log and its DAG |
| `tasks` | handoffs between users, optionally assigned to an agent |
| `peers` | replication bookmarks, one row per peer node |

An artifact with `project` NULL is personal to `owner_user`; `visibility` is
`personal`, `project` or `shared`. `kind` narrows `type` without multiplying it -
a memory item is `type='memory'` with a kind of `note`, `todo`, `feature` or
`handoff` - so one table, one permission filter and one search index serve all of
them. Indexes cover the reads the later phases do: artifacts by `(project, type)`
and `(type, kind)`, events by `thread` and by `seq_hlc`, plus owner, grant
direction and task inbox.

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
  `perm.go` holds the principal, the read predicate and the SQL filter;
  `artifacts.go` and `events.go` hold the queries that carry it.
- `cmd/smoke` - the live checks the gate runs against a running node, plus
  `smoke seed`, which mints the principals the permission checks act as.
- `auth.go`, `api.go` - the token middleware and the handlers. The whole of
  `/api/` is mounted behind `authenticate` in one place, so a route added later
  cannot arrive without a token check.
- `mcp.go`, `mcp_tools.go`, `instructions.md` - the MCP surface. `mcp.go` is
  JSON-RPC 2.0, the two transports and the method dispatch; `mcp_tools.go` is
  the five memory tools and their schemas; `instructions.md` is embedded into
  the binary and served both as `initialize.instructions` and as the
  `flowy://instructions` resource. A transport hands a request to `handle()`
  and writes back what it returns, so a tool cannot behave one way over stdio
  and another over HTTP.

## What the gate asserts

`schema.sql` loads and reloads, `go build`, `gofmt`, `go vet`, `go test`, then
against a live `flowy serve`:

- `/healthz` comes up and reports `ok:true` with the database up
- the seven spine tables exist
- `fuse` and `sync` print their placeholder and exit zero
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

Then Phase 1, driven over HTTP with `curl` and `jq` - because the thing being
tested is what a second agent holding a second token can and cannot see, and
that is only true if it is true over the wire. The gate seeds user A with an
agent in project `pa`, user B with an agent in `pb`, an operator, and a second
token for A in `pc`, then asserts:

- no token, and an unknown token, are `401`, for reads and for writes
- a token resolves to its user and home project, and an agent's token inherits
  both from the agent
- A creates a bug in `pa` whose distinctive word appears **only** in the
  discovery; A reads it, finds it by that word with a positive rank, and sees it
  in the list
- B gets `404` - not `403` - on it, and it is absent from B's list and B's search
- A opens `pa` up to `pb`; B now reads it and B's search finds it
- A creates a personal artifact with no project: B gets `404` **even with the
  grant**, B's search cannot reach it either, and A's own agent token reads it
- A creates two artifacts in `pc`, which nobody has a grant into, and shares
  exactly one with B: B reads that one and gets `404` on the other, and B's
  search finds one and not the other
- a principal cannot write into a project it is not acting in
- two events, the second with `parents: [first]`, read back from `?thread=` in
  order with their parents intact and `seq_hlc` advancing; `?since=` pages past
  the first
- a delete tombstones the artifact, moves the clock past the write, and takes it
  out of both the list and search
- `?scope=all` does nothing for a non-operator and opens the whole node to the
  operator, and only when asked
- `psql` sees the tokens, the grants, the tombstone still in the table, and a
  populated search vector

Then Phase 2, against `flowy mcp --http` on a free port and against
`flowy mcp` on a pipe:

- `initialize` answers **without a token** with `serverInfo` naming flowy and its
  version, protocol `2024-11-05`, and a non-empty `instructions` document that
  names the three scopes and the tools
- `tools/list` offers `mem_write`, `mem_read`, `mem_search`, `mem_list` and
  `todos`, each with an object input schema
- `resources/list` carries `flowy://instructions` and `resources/read` returns
  byte-for-byte what `initialize` returned
- `tools/call` with no token, and with an unknown token, is JSON-RPC `-32001`;
  an unknown tool is `-32602`
- A writes a memory item: personal by default, no project, kind `note`, a
  stamped clock - then finds it by a word that appears **only in the body**,
  reads it back by id, and lists it
- B, holding a grant on A's project, still cannot `mem_read` A's personal item -
  by user token or by agent token, and with the same message a missing id gets -
  and B's `mem_search` cannot reach it either
- A writes an item at `scope=shared`; **a second agent identity, B's agent
  token in another project, reads it, finds it by search, and sees the handoff
  in its own `todos`**. That is the shared-memory claim, over the wire, as two
  different principals
- a todo is outstanding until `mem_write {"id", "status": "done"}` closes it -
  which leaves the title and kind alone - and then it is out of `todos`
- `flowy mcp` on a pipe answers the handshake and a `tools/call` one line each,
  and says nothing at all to a notification
- the item stdio wrote is found by search over HTTP: **one store, two
  transports**
- `psql` sees the memory rows, the shared item in `pa`, and the `memory.write`
  events

The last thing the gate does is ask git whether the tree it just tested is the
tree on disk: uncommitted changes, or nothing ever committed, is a failure.

## Deployment

The store speaks the Postgres wire and nothing else. The spine of `schema.sql`
depends on nothing that is Postgres the storage engine - no extensions, no
partitioning, no identity columns, no triggers, no stored procedures - so
deploying against a SereneDB node is a change of DSN:

```sh
export DATABASE_URL='postgres://user@serenedb-host:5432/flowy?sslmode=disable'
```

The gate itself runs against stock Postgres, which is the point: the SQL has to
be portable enough to pass on both.

The exception is the `SEARCH` section at the bottom of `schema.sql` - a
`tsvector` column and a GIN index, which are Postgres full text and nothing
else. It is quarantined there because it is meant to be deleted: when SereneDB
brings vectors, that section goes and `store.SearchArtifacts` becomes a vector
query. Nothing above it depends on anything below it.

## Phase 2 status

Green. `./run-tests.sh` reports `passed: 79 failed: 0` on Ubuntu 24.04 with Go
1.22 and Postgres 16 - the 21 Phase 0 checks, the 37 Phase 1 checks, and 21
more for the MCP endpoint. Phase 1 stayed green throughout: the memory tools are
the Phase 1 store, principal and permission filter with an MCP surface on top,
not a second path to the same rows.

Not here yet: `flowy fuse` and `flowy sync` still print their placeholder, and
the HTTP transport answers `POST /mcp` only - there is no server-initiated SSE
stream, because nothing in this phase pushes.
