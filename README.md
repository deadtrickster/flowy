# flowy - Handoff Fabric node (Phase 8)

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

Phase 3 puts people back in the loop: **chat over the same event DAG, and a
React console served by the binary**. A message is an event of type `chat` in a
room - same log, same `seq_hlc` cursor, same `parents` DAG, same permission
filter - so a human in the message box and an agent over MCP are writing to one
place. The console is path-routed, embedded with `go:embed`, and served by
`flowy serve` itself.

Phase 4 is the agentic Jira layer: **assignment, delegation and an issue
lifecycle**. Assigning an artifact is one operation that writes three rows under
one clock reading - a share, so the assignee can read the work at all; a task,
which is the state of the handoff; and a thread, opened with a message, so the
conversation about it is the same log as everything else. If the assignee's
`auto_delegate` is on, the task arrives already handed to their agent. And a
bug moves `open -> triaged -> in-progress -> in-review -> done`, with `wont-fix`
and `duplicate` as terminal exits, every move appending an event that names the
one before it.

Phase 5 is federation: **two nodes, each with its own database, holding each
other's work**. A node hands a peer a delta of everything the peer's principal
may read since a cursor, applies what it gets back, and merges it
last-writer-wins by HLC - so the same edit made on a laptop and on a server
converges to one row on both, a delete travels as a tombstone, and an artifact
nobody granted the peer access to never leaves the node it was written on.
`flowy sync --peer <url> --token <t>` is the whole of the driver, and being
offline just means syncing later.

Phase 6 is the forge bridge: **a bug here becomes an issue on GitHub or GitLab,
and the two conversations stay one conversation**. Filing shells out to that
forge's own CLI - `gh`, or `glab` - so the credential stays where it already
is, and writes the issue back onto the artifact as an external ref. A closed or
merged issue moves the artifact to `done`. And the reviewer loop runs both ways:
every new comment on the issue is threaded into the artifact's chat thread as an
event by a synthetic external principal, and every reply written in that thread
since the last push goes back out as a comment. It is idempotent - both cursors
live on the ref and only move forward - and the gate drives all of it against an
in-process `MockForge`, because a gate that needed a GitHub token would be a
gate that leaves issues in somebody's repository.

Phase 6.5 signs what replicates: **every row carries the ed25519 signature of
the node that wrote it, and a merge checks that before it checks anything
else**. Federation is peers handing each other other people's rows - that is
what it is for - so a peer serving a page could rewrite every column of any row
the puller may read, leave the original node's name on it, raise the clock
reading, and watch last-writer-wins make the rewrite the truth on every node
downstream. Nothing in the merge could see it, because nothing on an unsigned
row says who wrote it. Now a node mints a keypair on first use, signs each
artifact, grant, task and event as it writes it, and refuses any row whose
signature does not verify under the key of the node named on it. Keys travel by
operator pin or trust-on-first-use, a node's key never changes over the wire,
and `FLOWY_REQUIRE_PINNED_PEERS` narrows a deployment to nodes its operator
named by hand.

Phase 7 is the file layer: **`flowy fuse` mounts a principal's memory as files,
so an agent writes memory where it already writes files**. The path is the
scope - `/<project>/<user>/<type>/<id>.md`, with `_personal` for the floor -
and a file closed in one of those directories is a memory item in the store,
signed, indexed and searchable by every agent that never mounts anything. The
close does not wait for that: it writes an intent down - the path, the bytes and
their hash - and answers, and a drainer applies intents to the store one
transaction each, deduped by hash, replaying whatever a crash left behind the
next time the mount starts. It is **opt-in and personal by default**: nothing
mounts until somebody runs the command, a file with no project in its path is
its owner's alone and no save promotes it, and with nothing mounted the node is
exactly the node the six phases above describe.

Phase 8 is the fabric watching itself: **metrics, traces and a timeline, all
filtered by the same rules the reads they aggregate are**. The store is
Postgres-wire, so the engine's numbers come from the database and the fabric
layers its own on top - corpus, sync, collaboration, permissions - and every one
of them is the caller's: a metric is an aggregate, and an aggregate over rows
somebody may not read tells them how many there are. `GET /api/metrics` answers
six groups, a group that could not be measured says why rather than answering
zero, every rate and share names its denominator, and the anomaly pass refuses a
verdict below eight recorded readings instead of calling three points a
baseline. Every operation is an OpenTelemetry span - the MCP call, the
permission check, the query, the ingest, the leg of replication - and **the
trace id rides the sync deltas across the node boundary**, in the meta of the
event that opens a handoff's thread and inside its signature, so a handoff
assigned on one node and worked on another is one trace in two databases and
`flowy traces` puts the halves back together. The console gets a metrics tab, a
trace waterfall, and an activity timeline that indexes every turn, run log line,
message and steer - searchable, and postable-into, because the moment you find
the run where something went wrong is the moment you want to say something into
it.

And the surface after those: **`flowy tui`, the same fabric from a terminal**.
The console needs a browser, and the person running these agents is usually on
the far end of an ssh session inside tmux, where there is not one. So the rooms,
the inbox, the artifacts, memory, the timeline, the metrics and the
announcements are a keyboard-driven client of the same `/api` - no new endpoint,
no database handle, one permission filter still. It never blocks on the long
poll, it reflows on a resize, it degrades to 16 colours and to none, it binds
neither `ctrl-a` nor `ctrl-b` because those belong to screen and tmux, and when
it exits the terminal still echoes.

## Run the gate

```sh
./run-tests.sh
```

It needs `go`, `node` >= 20 with `npm`, a Postgres installation (`initdb`,
`pg_ctl`, `psql`), and `curl` and `jq` for the HTTP checks - no running
database, no systemd.

Two prerequisites are worth stating on their own, because getting either wrong
fails somewhere that does not name the real cause:

- **Run it as an ordinary user, not as root.** `initdb` refuses to run as root,
  and a container or an agent harness that puts you there gets an error about
  the database that reads like a Postgres problem rather than a wrong user.
  From a root shell: `sudo -u someone ./run-tests.sh`. The gate checks `id -u`
  first and says which of the two it is.
- **node >= 20.** The console is built with vite 6, and node 18 fails `npm run
  build` partway through with a message about the bundler. Checked in the same
  place, before anything is built. It builds the console first (`npm ci`, Biome, `vite
build`), because `flowy serve` embeds `web/dist`; then it creates a throwaway
cluster in a temp directory on a free port, loads `schema.sql`, builds the
binary, runs the unit tests, starts `flowy serve`, runs the live checks against
it, and tears the whole thing down in a trap. It prints PASS or FAIL per check
and ends with `passed: N failed: M`, exiting non-zero if anything failed. Phase
2 added an MCP section - `flowy mcp --http` on a free port and JSON-RPC piped
into `flowy mcp` on stdin, both transports, one store - and Phase 3 adds the
chat, watcher, inbox and console-routing checks.

Phase 5 stands up **two more clusters and two more nodes** inside the same run -
`nodeA` and `nodeB`, separate `PGDATA`, separate ports, separate
`DATABASE_URL`s, seeded with the same principals so a peer can authenticate -
and drives the real `flowy sync` between them. Everything it creates is torn
down by the same trap, including both extra clusters.

Phase 6 runs the node with `FLOWY_FORGE=mock`, so every forge check goes through
the in-process fake: **no network, no credential, no CLI, and nothing left
behind in a real repository**. The gate puts a `gh` of its own on `PATH` first -
a script that records having been run and then refuses - so that selecting the
mock is a choice rather than the only option, and so `gh was never invoked` is a
fact the run checks rather than assumes. The real `gh`/`glab` argv and the
parsing of what they print are covered by unit tests with the runner injected;
the real path itself runs on a host that has the CLI and a login.

`npm ci` is the one step that wants the network, and only when the package cache
is cold; the Go build never does, because the module's one dependency is
vendored.

On Ubuntu the dependencies are:

```sh
apt-get install -y golang-go postgresql postgresql-client curl jq git
curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && apt-get install -y nodejs
```

## Run the node

```sh
export DATABASE_URL='postgres://user@127.0.0.1:5432/flowy?sslmode=disable'
psql "$DATABASE_URL" -f schema.sql
(cd web && npm ci && npm run build)   # the console, into web/dist
go build -o flowy .                   # which go:embed compiles in
./flowy serve                         # or: ./flowy serve -addr 127.0.0.1:8787
curl -s 127.0.0.1:8787/healthz        # {"ok":true,...}
open http://127.0.0.1:8787/           # the console
```

`go build` works without the console build - `web/dist` holds a tracked
`.gitkeep` so the embed pattern always matches - and a binary built that way
serves the API and answers `503` with a hint at every console path. Nothing
guesses: the log line at startup says which of the two you have.

Two more things the node will not do until it is told to: take a replication
push, and file an issue. `FLOWY_PEERS` names the user ids whose token may push a
delta here, and `FLOWY_FORGE_REPOS` names the repositories it may file into -
both empty by default. See [The security fixes](#the-security-fixes).

## Subcommands

| command | what it does |
| --- | --- |
| `flowy serve` | HTTP server, wired to the store, serving the embedded console |
| `flowy mcp` | MCP server: shared memory over stdio, or `--http :PORT` |
| `flowy tui` | the terminal client: rooms, inbox, artifacts, memory, timeline, metrics and announcements, over the HTTP API |
| `flowy fuse` | mount this principal's memory as files: `--mount <dir>`, or `--reconcile` to apply what an earlier mount queued |
| `flowy sync` | replicate with a peer: `--peer <url> --token <t>`, pull then push |
| `flowy traces` | collect one trace from this node and its peers: `--trace <id> [--peer <url>,...]` |
| `flowy identity` | this node's signing key, the keys it holds, and how a key gets in |
| `flowy sign` | sign a replication delta read on stdin |
| `flowy version` | build version |
| `flowy help` | usage |

`flowy tui` needs a node and a token, and has a default for both:

| flag | what it is |
| --- | --- |
| `--url` | node to talk to, or `$FLOWY_ADDR`, default `http://127.0.0.1:8787`; a bare `host:port` or `:8787` is read as one |
| `--token` | bearer token, or `$FLOWY_TOKEN`, or `~/.config/flowy/token` |

With no token anywhere it refuses to start rather than opening on empty panes
that read as "you have nothing".

`flowy sync` takes the peer and the token it authenticates as; everything else
has a default:

| flag | what it is |
| --- | --- |
| `--peer` | base URL of the peer node, required |
| `--token` | bearer token, or `$FLOWY_TOKEN`; must resolve on **both** nodes |
| `-dsn` | this node's database, default `$DATABASE_URL` |
| `-node` | this node's name, default `$FLOWY_NODE` or the hostname |
| `--limit` | rows per table per page, default 500 |
| `--pull` / `--push` | either half on its own, both default true |

It prints one JSON object per run - what it pulled, what that applied, what it
pushed, what the peer applied, and where both cursors ended up - so a cron entry
or a shell can read the result back.

`flowy traces` is the collector. It reads this node's spans for a trace out of
the database, asks each peer for theirs over the same API the console uses, and
prints one waterfall:

| flag | what it is |
| --- | --- |
| `--trace` | the trace id, 32 hex digits, required |
| `--peer` | comma-separated peer base URLs to collect from as well |
| `--token` | bearer token, or `$FLOWY_TOKEN`; used here and at each peer |
| `-dsn` / `-node` | this node's database and name |
| `--operator` | the user id `scope=all` obeys here, or `$FLOWY_OPERATOR` |

There is nothing to correlate: the trace id is already the same on both sides,
because it crossed on the rows. What the collector adds is the gathering, and
the honesty about it - every source is named with how many spans it gave, and a
peer that could not be reached is named with the reason rather than quietly
leaving its half out of a trace that then reads as the whole of what happened.

`flowy identity` is the operator's side of row signing:

| command | what it does |
| --- | --- |
| `flowy identity` | print this node's node id and public key, minting the keypair if it has none yet |
| `flowy identity list` | every identity this node holds, and whether each was pinned or taken on trust |
| `flowy identity pin --node N --key K` | record a peer's public key, hex or base64, as the operator's own decision |
| `flowy identity keygen --node N [--seed HEX]` | mint a keypair without touching a database |

Standing two nodes up is one exchange, over whatever channel the two operators
already trust:

```sh
# on the laptop
flowy identity
{"node":"laptop","pinned":true,"public_key":"e96a8cce...b016f89b5"}

# on the server, and the other way round
flowy identity pin --node laptop --key e96a8cce...b016f89b5
```

`flowy sign` reads a delta on stdin and writes it back with every row signed -
by this node's stored key, or by the key a `--seed` makes. It leaves the `node`
column of each row exactly as it found it, because a row says which node wrote
it and signing is not the place to decide that. `--identity` puts the signer's
own self-signed identity on the delta, the way a page from that node carries it.

`flowy fuse` mounts one principal's memory, and takes the token that says
whose:

| flag | what it is |
| --- | --- |
| `--mount` | directory to mount on; it has to exist |
| `--token` | bearer token, or `$FLOWY_TOKEN`; the mount is that principal's view |
| `--reconcile` | apply the writes an earlier mount queued and exit, mounting nothing |
| `--no-drain` | mount without a drainer: writes are queued and left there |
| `-dsn` | this node's database, default `$DATABASE_URL` |
| `-node` | this node's name, default `$FLOWY_NODE` or the hostname |
| `--debug` | log the FUSE protocol traffic |

`mcp` and `serve` read their configuration from the environment, and flags
override it:

| env | flag | default |
| --- | --- | --- |
| `DATABASE_URL` | `-dsn` | none; required |
| `FLOWY_ADDR` | `-addr` | `127.0.0.1:8787` |
| `FLOWY_NODE` | `-node` | the hostname |
| `FLOWY_OPERATOR` | `-operator` | empty; nobody may use `?scope=all` |
| `FLOWY_TOKEN` | - | the bearer token `flowy mcp` uses over stdio |
| `FLOWY_FORGE` | `-forge` | empty; `gh` if it is on `PATH`, else `glab`, else no forge |
| `FLOWY_PEER_KEYS` | `-peer-keys` | empty; `node=publickey` pairs to pin at startup, comma-separated |
| `FLOWY_REQUIRE_PINNED_PEERS` | - | `false`; when true, only rows from nodes the operator pinned are merged |

`GET /healthz`, `GET /version` and `GET /` are open - a health check that needs
a credential stops working at the worst possible moment, and none of the three
reads a row of fabric data. `?counts=1` adds the per-table row counts, and
those are the operator's: the shape and the size of what the node holds is not
something a stranger on the port needs, so it is answered only to a request
carrying the operator's token, and to everyone else the health check answers as
if the parameter were not there. Everything under `/api/` needs a token.

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
  can write into, and nobody outside it. A grant another project holds does not
  reach these and neither does a share: it is a second floor, one step above
  `personal`. The row is written `visibility='project-only'`, which is what the
  filter tests - `visibility='project'`, the default an artifact written over
  the API gets, has always meant "the project and whoever its grants reach" and
  still does.
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

## The agent filesystem

```sh
./flowy fuse --mount ~/memory --token tA-01J...
```

Phase 2 gave every agent one shared memory over MCP. This puts a file layer on
top of it, for the agents that write files whether or not anybody gave them a
tool: a directory tree whose paths are the scopes, whose files are the memory
items, and whose writes land in the same store, through the same permission
filter, indexed the same way.

It is on top and not underneath. Memory works whole with nothing mounted -
`mem_write`, `mem_search`, the API, the console, the merge - and the node does
not know this exists until somebody runs the command.

### The path is the scope

```
~/memory/
  _personal/<user>/memory/<name>.md     the floor: no project, owner only
  _personal/<user>/note/<name>.md
  <project>/<user>/memory/<name>.md     that project's memory
  <project>/<user>/note/<name>.md
```

A directory here is not a container, it is a question the permission filter has
already answered. `/<project>` is listed only if the principal may read
something in it, `/<project>/<user>` holds that person's items and only the ones
this principal may read, and `_personal` is the reserved name of the floor -
rows with no project at all, which are their owner's and nobody's else however
many grants exist. A project that is genuinely called `_personal` is skipped in
the listing and said so in the log, because one name cannot mean two things.

Writing follows from the same idea, and it is the mem_write rule with a path
instead of an argument: you write under your own user directory, in the project
your token is for, or on your own floor. There is no path that means "promote
this". A personal item does not become a project item by being saved somewhere
else, and a project item is not taken out of its project by being saved on the
floor - both are refused at the door and again inside the transaction that
would do the write.

A file is the item, with a short header for what a body cannot carry:

```markdown
---
title: the write-behind queue is the durability
id: 01M02Z8BYQ7C4FXRYMRF3JG5A5
scope: personal
kind: note
tags: phase7, fuse
updated: 2026-08-15T15:04:44Z
---

The store write does not happen in the callback.
```

`id` and `updated` are printed for whoever is reading and ignored on the way
back in: the row a file writes is the row its path names, and a header that
could redirect it would be a way into a directory the writer was refused. A file
with no header at all is a note whose title is its first line - an agent that
has never heard of any of this writes something this can read. Inside a project
directory `scope:` chooses between `project` (the project and nobody else, which
is the default because it is the narrower) and `shared` (the project plus
whoever its grants reach); on the floor it may only say `personal`. A `kind:` or
a `scope:` that is not one of the words is refused rather than defaulted, and
the refusal arrives on the `close(2)`.

A new file may be called anything: the name is kept in `artifacts.file_path`, so
`decisions.md` reads back as `decisions.md` rather than as a ULID nobody typed.
An item written by `mem_write`, which has no name of its own, is `<id>.md`.
Deleting a file tombstones the item - that one write is inline, because a caller
is entitled to be told whether the thing is gone.

### Write-behind, and what a crash leaves

A `close(2)` cannot wait for a signed, indexed, transactional write and then
report a database error to an agent that has already moved on. So the mount does
not do the write in the callback:

1. **close** writes an intent to `fs_intents` - the path, the bytes, their
   sha256 - and answers. That row is committed before the callback returns, and
   it is the durability point: from here the write will happen, this run or the
   next one.
2. **the drainer** takes intents oldest first and applies each one in a single
   transaction: the artifact, the `memory.write` event that records it, the
   search vector, and the intent's own `applied` stamp. One transaction, so
   there is no state in between for a crash to leave behind.
3. **the next mount reconciles** before it serves a single callback, which is
   what makes step 1 worth anything: whatever the last run did not finish is
   finished first.

That is at-least-once delivery, and at-least-once delivery of a write is not the
same as writing once. The apply compares the intent's hash against the last one
applied for the same row and skips a write the store already has, so a replay
after a crash is one artifact and one event, and saving a file again with
nothing changed is nothing at all. Two more things it will not do: a queued
write does not resurrect an item deleted since the file was closed, and it does
not apply to a row that has changed owner or home underneath it.

Deleting a file is the one case where the queue is written rather than read.
`rm` on a file whose write has not been drained cancels that write: the intent
names a row that does not exist yet, so there is no tombstone for the apply to
refuse against, and without the cancel the item somebody just deleted would
appear a second later.

`flowy fuse --reconcile` is step 3 on its own, for a queue whose mount is not
coming back. It takes no token, deliberately: every intent carries the owner,
the actor, the project and the scope decided when the file was closed by a
principal that had already been checked, so replaying one needs no credential
and must not be able to acquire one. What it can still do is refuse.

Reads go the other way and are not cached at all - every entry and attribute
timeout is zero, and files are opened `FOPEN_DIRECT_IO`. The store changes under
this process constantly (another agent's `mem_write`, the API, a merge from a
peer), so a cached lookup is this mount telling the kernel something that was
true a moment ago. The mount is a view: unmount it and mount it again and the
same items are there, because they were never anywhere else.

### The four things a FUSE filesystem gets wrong

They are worth naming, because each one is a specific decision in
`internal/agentfs` rather than a general intention:

- **Exactly one reply per callback.** Every operation returns a `syscall.Errno`
  and the go-fuse `fs` layer turns exactly one of those into exactly one reply.
  The path that would otherwise not reply is the panicking one, so every
  callback that touches the store goes through `op()`, which recovers, logs and
  answers `EIO`. A panic in a callback takes the mount down and leaves the
  kernel holding a request nobody will ever answer; no store error is unwrapped
  into a callback, and every one of them is mapped to an errno.
- **Ask what was negotiated, do not trust what was asked for.** go-fuse does not
  put `FUSE_ATOMIC_O_TRUNC` in the flags it agrees to at INIT, so the kernel
  does not pass `O_TRUNC` through to `Open`: it opens the file and then sends a
  separate `SETATTR` with the size and, on the kernels measured here, without a
  file handle. Believing the documented shape of `open(O_TRUNC)` instead of
  reading the trace is how a rewrite ends up with the tail of the previous
  content still on the end of it. A size is taken on the handle it names, on the
  handles the node already has open, and - for a kernel that sends it the other
  way round - as a mark for the open about to happen. The protocol level itself
  is read back off the server after the mount and the mount refuses to serve
  below 7.12, which is a sentence rather than an `EIO`.
- **Names are bytes.** A filename off the kernel is not a Go string with a
  guarantee attached. It is checked as bytes - no slash, no NUL, no control
  bytes, not `.` or `..`, at most 255 - and then, explicitly, for being valid
  UTF-8, because the name ends up in a text column and a text column in a UTF-8
  database refuses invalid UTF-8 with a driver error halfway through a
  transaction. A refusal at the door is a name the agent can fix. A leading dot
  is refused too: editors drop `.swp` and `.#` files beside the one they are
  writing, and a memory item that exists because vim was open is worse than no
  file at all.
- **Know the dispatch model.** go-fuse runs a goroutine per request and bounds
  only the background ones; synchronous reads and writes are not bounded at all.
  So the bound is in this package, sized to the connection pool the store opens,
  rather than assumed from the library. Every callback also carries a deadline:
  a filesystem call that never returns is a process in uninterruptible sleep,
  and a database that has stopped answering should be an errno.

### Mounting it for an agent

The mount is private: no `allow_other`, so the kernel refuses every other uid,
root included, before a callback here is reached. Point an agent at a directory
under it and it has memory:

```sh
mkdir -p ~/memory
flowy fuse --mount ~/memory --token "$FLOWY_TOKEN" &
ls ~/memory/_personal/$USER_ID/memory
```

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
3. **`project-only` stops there.** An artifact written `visibility='project-only'`
   is readable by its project and by nothing else - no project-wide grant, no
   per-artifact share. It is the second floor, and it is what the `project`
   memory scope means; `visibility='project'`, which is what an artifact written
   over the API gets by default, is the wider one that grants reach.
4. **Anything else needs a live grant**, either a project-wide one along the
   edge (`from_project` = the reader's project, `to_project` = the artifact's)
   or a share of that one artifact to that one user (`artifact` = the id,
   `subject` = the reader). Tombstoned grants count for nothing.

Events are narrowed by `EventFilterSQL`, which asks two questions and takes both
answers. First, does the reader reach the event's project at all: an event with
no project belongs to whoever wrote it, an event in your project is yours, a
project-wide grant along the edge reaches it, and a **share of the artifact
reaches the events about that artifact**. Second, if the event names an
artifact, does the reader reach that artifact - and that test is
`artifactReachSQL`, which is `ArtifactFilterSQL`'s own branches evaluated on the
named row, so an event never reaches further than the artifact it is about,
floors included. An event that names no artifact is chatter and stops at the
first question. Beside the two, the parties to a task read the thread that task
names, whichever project each of them writes from.

The floor is one definition on purpose. It was written branch by branch twice
and missed a branch both times - see the tenth and twelfth rounds of security
fixes below.

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
| `POST /api/artifacts` | create, or replace one you own **and can read**. Body: `type` (required), `kind`, `title`, `body`, `discovery`, `status`, `severity`, `tags`, `user_tags`, `related`, `visibility`, `project`, `file_path`, `fields`, `id?`. A new `id` is a ULID; `hlc` and `node` are stamped. An `id` that names a row you cannot read - another project's, a deleted one - is `404` and writes nothing |
| `GET /api/artifacts?type=&kind=&project=&status=` | `{"artifacts":[...]}`, permission-filtered, newest first, tombstones omitted |
| `GET /api/artifact/{id}` | the artifact, or `404` if it is missing **or** out of reach |
| `POST /api/artifact/{id}/delete` | tombstone it and bump the clock past the write it removes |
| `POST /api/artifact/{id}/status` | move it through the lifecycle. Body: `status`. Returns `{artifact, event}`. `409` on a move the workflow does not allow, `404` on one you cannot read |
| `GET /api/artifact/{id}/history` | `{"artifact","status","next":[...],"events":[...]}` - the status trail in order, and where it may go from here |
| `GET /api/search?q=&type=&kind=&project=` | `{"query":..., "artifacts":[{..., "rank":...}]}`, ranked and permission-filtered |
| `POST /api/events` | append. Body: `type` (required), `room`, `thread`, `parents`, `actor`, `artifact`, `body`, `meta`. `id` is a ULID, `seq_hlc` comes from the clock, the project is the principal's |
| `GET /api/events?thread=&since=&room=&type=` | `{"events":[...]}` with `seq_hlc > since`, in log order, permission-filtered |
| `POST /api/chat/{room}/say` | say something. Body: `body` (required), `thread?`, `parents?`. Returns the event |
| `GET /api/chat/{room}?since=&thread=` | `{"room","events":[...],"since","cursor"}` with `seq_hlc > since`, in log order |
| `GET /api/chat/{room}/wait?cursor=&window=` | long poll: blocks up to 25s for events after `cursor`, returns them or an empty list |
| `GET /api/inbox?since=&room=` | chat you may see and did not write, across rooms |
| `POST /api/assign` | hand work over. Body: `artifact`, `to_user`, `note?`. Returns the task, plus the `grant` and the `opening` message it wrote |
| `GET /api/inbox/tasks?state=` | `{"tasks":[...]}` assigned to you or your agent, newest first, with the artifact's title and type joined in |
| `GET /api/task/{id}` | the task, to a party to it. `404` to anybody else, including the operator |
| `POST /api/task/{id}/delegate` | hand it to the assignee's agent. Body: `agent?`. Only the assignee may |
| `POST /api/task/{id}/state` | move it: `open`\|`delegated`\|`done`. Either party may. Returns `{task, event}` |
| `PUT /api/me/auto_delegate` | `{on: bool}` - your standing answer to inbound work |
| `POST /api/grants` | issue a capability: `{from_project,to_project}` for a project-wide one, `{artifact,subject}` for a share |
| `GET /api/sync/pull?since=&limit=` | the delta a peer may read: `{artifacts, events, tasks, grants, hwm}`, ordered by the clock, tombstones included |
| `POST /api/sync/push` | merge a peer's delta: upsert by id, append-only events, last-writer-wins by `hlc` and `node`. Rows the pushing principal could not have written are refused and counted |
| `GET /api/peers` | replication bookmarks and their cursors; the operator only |
| `GET /api/metrics?scope=all` | the six metric groups, filtered to this principal; `scope=all` is the node and is the operator's alone. Every group says whether it was measured, and why not when it was not |
| `GET /api/activity?q=&kind=&room=&thread=&since=` | the timeline: turns, run logs, chat and steers this token may read, in log order, with a cursor |
| `POST /api/activity` | post into it. Body: `kind` (`chat`\|`turn`\|`log`\|`steer`), `body`, and `room` and/or `thread`. Same three gates as `say`: the thread, the parents and the artifact all have to be yours to name |
| `GET /api/traces?since=&limit=` | recent traces this token may read, one summary each |
| `GET /api/trace/{id}` | one trace, its spans in start order, and the nodes that recorded them |
| `GET /api/forge` | which forge this node speaks to, why, and which CLIs it can see |
| `POST /api/forge/file` | file an artifact as an issue. Body: `artifact`, `repo`. Returns `{artifact, external, event}`. `409` if it is already filed, `404` if you cannot read it, `502` if the forge refused |
| `GET /api/forge/status?artifact=` | refresh the issue's state; a closed or merged issue moves the artifact to `done`. Returns `{artifact, external, state, status, moved}` |
| `POST /api/forge/sync` | one turn of the reviewer loop: thread new comments in, push new replies out. Body: `artifact`. Returns `{external, pulled, pushed, events}` |
| `POST /api/announcements` | post an announcement. Body: `scope` (`node`\|`project`\|`federation`, default `project`), `severity` (`info`\|`warning`\|`maintenance`\|`breaking`, default `info`), `title` (required), `body`, `resource?`, `mode?` (`drain`\|`pause`\|`ack-required`). `403` for `federation` unless the token is a `system` or `monitor` agent |
| `GET /api/announcements?status=` | the active announcements this token may read, worst severity first. `status=resolved` or `status=all` for the rest. This is what the console banner reads |
| `GET /api/announcement/{id}/quiesce` | `{resource, mode, holders, acked, pending, state}` - who the change is still waiting on. `400` if it names no resource |
| `POST /api/announcement/{id}/ack` | this principal has seen it and is out of the way. Returns `{quiesce, event}` |
| `POST /api/announcement/{id}/resolve` | close the window. `409` while the quiesce is `held`, with the pending list in the body; the owner only |
| `POST /api/quiesce/hold` | `{resource}` - this principal depends on that resource, so a maintenance announcement naming it knows who to wait for |
| `POST /api/quiesce/release` | `{resource}` - and has let it go. Under `drain` and `pause` that is the answer; under `ack-required` it is not |
| `GET /api/whoami` | the principal this token resolves to, including `agent_kind` when the token names an agent |
| `GET /api/node` | this node, its version and its routes |

On `project` in a create, absent, `null` and a string are three different
things: absent means the home project, `null` means none, which is what personal
is. An update keeps whatever it does not restate.

A thread with no `thread` given is named after its first event. `parents` is the
DAG: none opens a thread, one continues it, several merge. `since` is the same
cursor peer replication pages by - `/api/sync/pull` takes the same parameter and
means the same thing by it - and it is strictly greater, so a caller hands back
the last value it saw.

A tombstoned artifact is gone from every read: the list, the search and a `GET`
by id alike, and its status and its history with them. The row stays in the
table, marked, because that is how the delete replicates - a delete travels as a
fact and not as an absence - and `psql` is where you see it. What that closes is
resurrection by accident: every read went through one filter, so an edit of a
deleted artifact used to write it back with a reading that beat the delete on
every peer.

## Chat, over the event DAG

There is no chat table. A message is a row in `events` with `type = 'chat'`, a
`room`, a `thread` and the same `parents text[]` every other event carries, so
it inherits the log's cursor, its DAG and its permission filter instead of
getting a second set of its own.

- **Who is speaking is the token's business.** A human posts as their user, an
  agent as its agent - `POST /api/chat/{room}/say` takes no actor field, so a
  message cannot be put in somebody else's mouth. The node stamps
  `meta.actor_kind` (`user` or `agent`) and, for an agent, `meta.actor_user`,
  which is what lets a console tell a person from the agent working for them
  without a join per message.
- **Rooms are scoped by project.** The room is the `room` column and the project
  is the principal's, so `pa` and `pc` may both have a `general` and neither
  reads the other's - unless a grant says otherwise, exactly as for artifacts.
- **Branches are `parents`.** No parents opens a thread, one continues it,
  several merge. A reply that names a parent and no thread inherits that
  parent's thread, so answering a message cannot fork a second thread by
  accident. Two replies to one message are two lanes, which is what the console
  draws.
- **The watcher contract is a finite window.** `GET /api/chat/{room}/wait`
  blocks for up to 25 seconds (`?window=<seconds>` shortens it) and then returns
  an empty list rather than hanging: a poll always returns, and a client loops.
  It polls the store rather than using `LISTEN`/`NOTIFY`, which is Postgres the
  engine and would not survive the move to SereneDB.
- **The inbox is what you did not write.** `GET /api/inbox` is every chat event
  the principal may read whose actor is neither their user nor their agent,
  filtered in SQL so `since` and the limit still count the rows the caller gets.

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"body":"deploy looks wrong"}' 127.0.0.1:8787/api/chat/general/say
curl -s -H "Authorization: Bearer $TOKEN" '127.0.0.1:8787/api/chat/general?since=0'
curl -s -H "Authorization: Bearer $TOKEN" '127.0.0.1:8787/api/chat/general/wait?cursor=123'
```

## Assignment, delegation and the lifecycle

Handing work to somebody who is not in your project needs three things to be
true at once, so `POST /api/assign` writes all three under **one clock
reading**:

1. a **share** - a `grants` row for this one artifact, subject to the assignee.
   A task pointing at something the other side gets a `404` on is not a handoff,
   it is a riddle;
2. a **task** - the `tasks` row, which is the state of the handoff and the only
   thing either side has to poll;
3. a **thread** - opened with a chat message, in the `handoffs` room, so the
   conversation about the work is in the same log as everything else rather
   than in a comment field.

They are written in that order for the same reason they share a reading: a node
that dies halfway leaves a share nobody is using, which is harmless and visible,
rather than a task whose artifact the assignee cannot open.

- **You assign what you own.** The first of the three writes is a share, and a
  share is the owner's to give - the same bar `POST /api/grants` keeps, and a
  `403` here for the same reason. A personal artifact cannot be assigned at
  all: it has no project to share it into, and the personal floor in the
  permission filter would refuse it anyway. Nor can a `project-only` one: the
  filter stops at the project for those and never reaches the share clause, so
  the share an assignment writes for one can never take effect and the task
  beside it is the riddle again. That is a `400`; share it first.

- **Delegation is the receiver's call.** `auto_delegate` on the user row is a
  standing answer to inbound work - on, and the task arrives already
  `delegated` to that person's agent. Off, and it waits at `open` until they
  say so with `POST /api/task/{id}/delegate`. The sender cannot delegate on
  their behalf: that is a `403`, and it is the one refusal here that is not a
  `404`, because the sender may legitimately see the task.
- **A handoff is between two people.** `tasks` reads are filtered on the row -
  `from_user`, `to_user`, or the agent it was delegated to - and not on the
  project. Nobody else can read one, move one or find one in an inbox, including
  this node's operator, for whom `?scope=all` does not reach tasks at all.
- **The thread crosses the boundary the task crosses.** The event permission
  filter has one extra clause since Phase 4: an event whose `thread` is named by
  a `tasks` row is readable by the parties to that task, whichever project each
  of them writes from. Without it an assignment would open a conversation only
  one side could read, which is not a conversation.
- **The thread is the audit trail too.** A task's own moves are appended to the
  same thread as `type='task'` events, chained by `parents`, so "delegated it,
  then asked a question, then closed it" reads top to bottom without joining
  anything.

### Delegation: passing on work that was shared with you

Not supported yet, deliberately, and this is a follow-up rather than an
oversight. Handing on a bug somebody handed to you is a real thing to want, and
it is not what "the caller can read it" bought: the share clause matches on
artifact and subject alone, so a reader who could mint a share could hand a read
on somebody else's artifact to anybody at all, across a tenant boundary, from a
capability that was only ever a read. The row was not even replicable -
`checkGrant` refuses a share whose `granted_by` is not the artifact's owner, so
the task travelled and the share behind it did not, and the far side held a task
whose artifact it gets a `404` on.

Doing it properly needs a **may-reshare** capability bit that the owner sets on
a grant, designed together with `checkGrant`'s push rule so that a re-share is a
row a peer can tell apart from a forgery. Until that exists, the owner is the
one who assigns.

The lifecycle is the other half. `POST /api/artifact/{id}/status` moves an issue
along one line and no further:

```
open -> triaged -> in-progress -> in-review -> done
   \________________|______________|_____________/
                    v
            wont-fix | duplicate      (terminal exits, from anywhere in the line)
```

- Nothing skips a step. A status that can jump is a status nobody trusts -
  `in-review` has to mean the work happened - so `open -> in-review` is a `409`
  that names what the workflow would allow instead.
- Nothing moves out of `done`, `wont-fix` or `duplicate`. An issue that comes
  back is worked on again, and the trail should say so rather than rewind.
- An artifact with no status at all reads as `open`, so a fresh bug is at the
  start of the line without anybody having had to say so.
- Only `bug`, `feature`, `note` and `task` have a lifecycle. A transcript has no
  status to move, and a memory item's `status` is `mem_write`'s own - neither is
  dragged into this.
- **Every move is an event.** `type='status'`, naming the artifact and the
  actor, with a body of `open->triaged` and the previous status event as its
  parent. The chain is what `GET /api/artifact/{id}/history` returns, in order.
  It lands in the **artifact's** project rather than the actor's, so an assignee
  moving a shared bug from another project does not fork its history into a
  second log, and the history read is gated on reading the artifact rather than
  on each event.
- Whoever can read it can move it. That is the point of the assignment: the
  share is what makes the assignee a participant, and a participant who cannot
  say "I am working on this" has to ask somebody else to say it for them.

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"artifact":"01H...","to_user":"01H...","note":"can you take this"}' \
  127.0.0.1:8787/api/assign
curl -s -H "Authorization: Bearer $TOKEN" 127.0.0.1:8787/api/inbox/tasks
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"status":"triaged"}' 127.0.0.1:8787/api/artifact/01H.../status
curl -s -H "Authorization: Bearer $TOKEN" 127.0.0.1:8787/api/artifact/01H.../history
```

## Federation

A node is one `flowy serve`, one database and one name - `FLOWY_NODE`, the
hostname by default - and that name is stamped on every row it writes, next to
the clock reading that ordered it. Two nodes hold each other's work by
exchanging deltas. The mechanism is symmetric: there is no primary, and a laptop
syncing with a server is the same command run from either end, even though that
deployment is a hub with one spoke.

```sh
# on the laptop, whenever it has the server in front of it
flowy sync --peer https://box.local:8787 --token "$TOKEN"
{"peer":"https://box.local:8787","node":"laptop","peer_node":"box","as":"01H...",
 "pulled":{"artifacts":3,"events":11,"tasks":1,"grants":0},
 "applied":{"artifacts":3,"events":11,"tasks":1,"grants":0},
 "refused":{"artifacts":0,"events":0,"tasks":0,"grants":0},
 "pushed":{"artifacts":1,"events":4,"tasks":0,"grants":1},
 "peer_applied":{"artifacts":1,"events":4,"tasks":0,"grants":1},
 "peer_refused":{"artifacts":0,"events":0,"tasks":0,"grants":0},
 "pull_cursor":117094...,"pushed_cursor":117094...}
```

`refused` is what this node would not take from the peer and `peer_refused` is
what the peer would not take from this one, both with `reasons` beside them when
either is non-zero. Neither should ever be anything but zero between two nodes
that trust each other, which is why they are in the report rather than in a log:
a peer quietly having half its delta dropped is exactly what a cursor hides.
`pushed_cursor` does not move past a page the peer refused, so those rows are
offered again next time rather than lost.

**Replication is permission-filtered, and that is the whole design.** A peer
authenticates as a principal exactly like an agent does - it holds a bearer
token, the token resolves to a `(user, agent, project)` triple - and it gets
what that principal may read, through the same `ArtifactFilterSQL`,
`EventFilterSQL` and task party test every other read goes through. So the
cross-project grant that lets somebody in `pb` read an artifact in `pa` is what
lets a node holding `pb`'s work replicate it, a personal artifact replicates to
nobody, and a project nobody opened up stays on the machine it was written on.
There is no replication user, no bypass and no second set of rules to keep in
step with the first.

The two endpoints:

| route | what it does |
| --- | --- |
| `GET /api/sync/pull?since=<hlc>&limit=` | `{artifacts, events, tasks, grants, hwm, node}` - every row the requesting principal may read whose `hlc`/`seq_hlc` is **strictly greater** than `since`, ordered by the clock. Tombstones included: a delete has to travel, and it travels as a row |
| `POST /api/sync/push` | body `{artifacts, events, tasks, grants}`; upserts each by id and answers with what it received, what that actually changed, and what it refused - with the reasons |

`hwm` is the cursor the caller may store once it has applied the page. It is the
greatest reading in the set, except when a table filled its page - then it is
the smallest of the truncated tables' greatest readings, so nothing above the
cursor is left behind. Rows below it arrive again on the next pull and applying
them again does nothing.

**The merge**, which is the same code on both sides (`internal/store/sync.go`),
whether a row arrived by pull or by push:

- **events are append-only.** Insert when the id is new, ignore it otherwise.
  Nothing about an event is ever updated, including by replication, so a thread
  arrives with its `parents` DAG exactly as it was written.
- **artifacts, tasks and grants are last-writer-wins by `hlc`, and by `node`
  when two readings tie.** An incoming row replaces the local one when
  `incoming.hlc > local.hlc`, or when the readings are equal and
  `incoming.node > local.node` - the `WHERE` on the upsert is the only place
  that is decided. The tiebreak is what makes the order total: a packed reading
  carries a wall clock and a logical counter and nothing about who made it, so
  two nodes writing in the same millisecond produce equal readings and different
  rows, and comparing on the reading alone leaves each node refusing the other's
  forever. Both nodes therefore pick the same winner whichever order the rows
  arrive in and however many times they arrive, which is what makes a push
  idempotent rather than merely repeatable.
- **an hlc is never lowered**, and a tombstone is a column rather than an
  absence, so a delete beats an older write for exactly the same reason - and
  loses to a newer one, which is what makes an edit made after a delete on
  another node come back rather than vanish.
- **applying a remote row advances the local clock past it.** The next local
  write is then strictly newer than everything replication has brought in, so an
  edit made here after pulling a peer's edit wins the next merge. `flowy serve`
  also lifts its clock above the highest reading in its store at startup, since
  the clock lives in memory and the rows do not.

**Cursors live in `peers`**, one row per peer: `pull_cursor` is the greatest
reading pulled and applied, `pushed_cursor` the greatest handed over, and
neither ever moves backwards. That is what makes a sync resumable - a run that
dies half way through resumes from where it got to, because each page is applied
in one transaction and the cursor moves after it - and idempotent: a second run
with nothing new to say transfers nothing and prints zeros. Being offline is
being behind, and being behind is a cursor that has not moved yet.

What replication does **not** carry is `tokens`, `users` and `agents`. Tokens
are local credentials and the schema says so - they carry no `hlc`, no `node`
and no tombstone. Two nodes that are meant to authenticate the same people are
handed the same rows out of band, the way two machines are handed the same key.
`GET /api/peers` shows the bookmarks, and only to this node's operator: a peer's
cursor is not something one principal's token should reveal to another.

## Row signing: who wrote this

Replication is peers handing each other rows they did not write. That is not a
weakness in the design, it is the design - the share a handoff wrote on one node
is how the other side reads the artifact on this one - and it is exactly what
made the merge's checks insufficient. Every check asks what the principal
*handing a row over* may write. None of them could ask whether the node named on
the row is the node that wrote it, because nothing on an unsigned row answers
that.

What that bought a hostile peer: take any artifact, grant, task or event the
pulling principal may read, rewrite every column of it, leave the original
node's name in `node`, stamp a reading that beats what the puller holds, and
serve it. The row lands where it always landed, owned by whoever always owned
it, and only its contents are somebody else's. Last-writer-wins then makes it
the truth on the pulling node and on every node downstream of that one.

So every replicated row now carries `sig`: the ed25519 signature of the node
named in `node`, over a canonical encoding of the row's authenticated fields.

**The encoding** (`internal/sign`) is canonical in both directions that matter:
one row has exactly one byte string, and no two different rows share one. Fields
are length-prefixed with an 8-byte big-endian count, so no run of fields can be
re-cut into a different run - `"ab"+"c"` and `"a"+"bc"` are different messages
here. Each message opens with its own domain string, so an artifact's signature
is not a task's. A NULL column and an empty one are different bytes, because the
read filter reads them differently. Large or structured values - a transcript
body, a `fields` object - are folded in as their sha256 rather than copied, so
signing a megabyte artifact costs a hash of it. `parents` is sorted first,
because the DAG is a set of edges and two nodes may list them in either order.

A `jsonb` column is parsed and re-encoded before it is hashed. It has to be:
Postgres does not store `meta` as the string you handed it - it drops the
whitespace, orders the keys its own way and normalises the numbers - so a
signature over the request body would verify on the node that made it and
nowhere else. What is authenticated is the value, which is what the column
holds.

`created` is in the encoding too, as microseconds since the epoch - the
resolution `timestamptz` keeps, so a row still verifies after the round trip
through the column. A date outside the signature is a date an honest-looking
relay may move, and everything downstream ages and orders by it. The date is
therefore minted by the node that writes the row rather than by the column's
`DEFAULT now()`, which used to fill it in after the signing.

**Signing is on the write.** Every path that mints or moves a replicated row -
create, update, status move, tombstone, the forge link, assignment's three rows,
a task delegation, a message - stamps the reading and this node's name and then
signs the result, in the same statement that writes it. So `node` is always this
node on a signature this node makes, and the row is self-consistent by
construction. Local reads do not verify: the store is this node's own, and a
database whose rows an attacker can edit directly is one whose `node_identity`
they can edit directly too.

**Verifying is at the merge, ahead of everything else.** `syncApply` asks three
questions of every row before it looks at the merge order or the permission
checks: is there a key here for the node named on the row, does the row carry a
signature, and is that signature that node's over these bytes. A row that fails
any of them is refused with the reason - `artifact <id>: signature from node
<n> does not verify` - counted, and not written, exactly like a row that fails
an authorisation check. The cursor holds, so the peer is offered the same page
again rather than the two nodes quietly differing.

**Authenticity and authorisation are two layers, and both ship.** A peer that
really did write a row - its own key, its own node, a signature that verifies -
is still refused when the row is not one it may write: a project-wide grant
naming a grantor who is nobody here, a task re-pointed at somebody else's
thread, an artifact landing in a project it cannot reach. Authenticity says who
wrote it; authorisation says whether writing it was theirs to do. Neither
subsumes the other, and the gate has a check that proves it - a validly signed
forgery, refused by the *authorisation* message rather than the signature one.

### Keys, and how a node comes to hold one

`node_identity` is one row per node this one has ever had to believe: the node
name, its ed25519 public key, whether the operator pinned it, and the node's own
signature over its name and key. The local node's row is the only one that also
holds a private key - the 32-byte seed - and nothing that replicates ever
selects that column. It is minted on first use, so a node that has never signed
anything gets a key on its first write rather than on a command somebody has to
remember to run.

Two routes in, and the first is authoritative:

- **the operator pins it.** `flowy identity pin --node N --key K`, or
  `FLOWY_PEER_KEYS` at startup. The key came off the other machine and travelled
  by some channel that is not the one being secured. Nothing over the wire
  changes a pinned key.
- **trust on first use.** Identities replicate: a page carries the public keys
  the serving node holds, and an identity for a node this one has never heard of
  is taken and marked unpinned. That is what makes a relay work - A pulls from B
  a page holding C's rows, and C's key rides along in it. B cannot alter that
  key, because the identity is signed by C over C's own name and key.

A second, different key for a node already here is **refused**, whether it
arrives on a page or at `flowy identity pin`, and whether the key here was
pinned or taken on trust. A key rotation a peer can serve is an impersonation a
peer can serve; a peer genuinely rebuilt with a new key is a row somebody
deletes by hand on the machine, which is the deliberate act it should be. The
window trust-on-first-use leaves open is first contact and nothing after it.

An operator's pin carries no self-signature - the operator holds the peer's
public key, not its private one - so a pinned identity does not relay onward
from the node that pinned it. When that peer's own self-signed identity turns up
on a page, the signature is filled in on the key already agreed, and from then
on it does. A node that never speaks for itself is pinned on every machine that
has to verify it.

`FLOWY_REQUIRE_PINNED_PEERS=true` refuses rows from any node whose key was only
taken on trust, and refuses the key itself on the way in: first contact is not
recorded there, so every key such a node holds is one its operator named. It
costs transitive relay, which is the trade a high-security deployment is making
on purpose.

Pinning also decides what a peer may say about other people. A row whose owner,
actor, grantor or assigner is somebody other than the principal carrying the
page is taken only when the node that authored it is pinned - see `mayAssert`,
the thirteenth round below and the fourteenth, which is where that became one
rule on both merge doors instead of two different partial ones. Federation
between two pinned nodes is unaffected; a relay whose key merely arrived on a
page can hand over only rows the carrier could have written itself.

**Pinning a node means trusting everything it says about who did what, including
about your own users.** A pinned peer can serve rows naming your own users as
their author - an artifact owned by one of them, a message under their name, a
share they are said to have given - and those rows are applied, at either merge
door, because that is exactly what pinning it said. So pin only nodes whose
operator you trust with your users' identities.

## Announcements, system agents and the quiesce protocol

An announcement is how a node tells the people and the agents working on it that
something is changing: a release is going out, a store is coming down, the wire
format moves next week.

It is an artifact of type `announcement` and not a table of its own, for the same
reason a memory item is not: one table means one permission filter, one canonical
encoding, one signature and one merge, and every property the fabric already
promises about an artifact is then a property of an announcement without a line
of code saying so. In particular **a federation announcement is unforgeable
because every artifact is** - the merge verifies the signature of the node named
on the row before it looks at anything else, and that signature covers the type,
the severity, the status and the `fields` blob the scope lives in.

What makes an artifact an announcement:

| column | holds |
| --- | --- |
| `type` | `announcement` |
| `severity` | `info`\|`warning`\|`maintenance`\|`breaking` |
| `status` | `active` until it is `resolved` - that pair is the window |
| `fields` | `{"scope":..., "resource":..., "mode":..., "resolved_at":...}` |
| `kind` | a copy of the scope, so "every federation announcement" is a column read. Nothing decides by it |

### Scope, and the two doors

`scope` is how far the announcement is meant to travel, and the one place
travelling happens is replication - so that is where it is enforced, on both
doors rather than on one:

- **node** - stays here. `SyncPull` does not offer it (nor does the
  newly-visible rescan, nor the `sync_pending` drain), and a peer that pushes
  one is refused with the reason. The predicate is written once as SQL for the
  pull side and once as Go for the push side, next to each other in
  `internal/store/announce.go`, because this project's history is a list of
  rules that were on one door and not the other.
- **project**, **federation** - replicate under the permission rules every other
  artifact replicates under. Scope says how far an announcement is *meant* to go;
  the permission filter still says who may read it, and those are different
  questions. There is no widening: an announcement does not get a way round the
  filter, so a federation one reaches the peers that can read the project it was
  posted in.

A `fields` blob that is absent, malformed, or carries a scope nothing implements
reads as node scope. That is the end of the decision that does not hand somebody
else's readers an announcement.

### Who may post one

`agents` now answers two questions instead of one. `kind` is the runtime -
`claude`\|`glm`\|`opencode` - and says nothing about what the agent may do.
`agent_kind` is what the agent is *for*:

| agent_kind | |
| --- | --- |
| `worker` | the default, and what every agent written before the column existed reads back as |
| `reviewer` | no more privileged than a worker; the kind is a capability, not a hierarchy |
| `system` | may post a federation-scope announcement |
| `monitor` | the same |

The column carries `DEFAULT 'worker'` in the `CREATE TABLE` and in the `ALTER`,
and `InsertAgent` applies it too, so every existing seed and every existing row
is a worker rather than a NULL. A kind nothing implements is refused on the way
in instead of being coalesced: a typo that silently becomes the default is a
system agent somebody thinks they created and did not.

Federation scope is refused to a worker agent, to a reviewer, to a person
holding their own token, and to the operator - being the operator of this node
is not being a machine that speaks for the fabric. And `POST /api/artifacts`
refuses `type: announcement` outright: the scope lives in a blob that endpoint
takes as it is given, so without that refusal the capability check would have had
a second door and no capability.

### Quiesce

A `maintenance` or `breaking` announcement may name a `resource` and a `mode`,
and then the change does not proceed until the dependents holding that resource
have answered:

| mode | what clears a holder |
| --- | --- |
| `drain` | finish and let go. A `POST /api/quiesce/release` is the answer |
| `pause` | stop now and let go. The same |
| `ack-required` | only `POST /api/announcement/{id}/ack`. Letting the resource go quietly is not an acknowledgement, and a process that went away has not answered |

A dependent registers with `POST /api/quiesce/hold {"resource": ...}`, which
writes a `quiesce.hold` event. `GET /api/announcement/{id}/quiesce` reports
`holders`, `acked`, `pending` and a `state` of `held` or `released`.

**The enforcement is that `POST /api/announcement/{id}/resolve` answers `409`
while the state is `held`**, with the pending list in the body. That refusal is
the protocol; without it the modes and the acks would be a report rather than a
gate. An announcement that names no resource has no quiesce and resolves
straight away.

Only `maintenance` and `breaking` may hold a resource. A notice that could
quiesce something would be a way to stop other people's work by describing it.

The four quiesce event types - `announcement`, `quiesce.hold`,
`quiesce.release`, `quiesce.ack` - are minted by the endpoints that do the
thing, on both wire paths: `POST /api/events` refuses them and so does the
merge. An ack anybody can type is a gate anybody can open, and a hold anybody
can type is a way to stop somebody else's release by claiming to depend on it.
That has a consequence worth stating plainly: **the announcement travels the
fabric and the answers to it do not**. A hold is a claim that a process on *this*
node depends on a resource, and a node waits for its own dependents.

### The banner

The console reads `GET /api/announcements` and paints the active ones above the
room - above the chat transport, not in it. An announcement that the node is
going down is not something somebody said in a room: it must not scroll away
with the log, and it is the same on every route that shows it. There is no
dismiss button, because what clears the banner is the announcement's own state
and not this browser's: resolve it and it goes.

## The forge bridge

A node holds bugs; the world holds an issue tracker. Phase 6 is the join, and it
is one interface with three implementations:

```go
type ForgeClient interface {
	Kind() string                                                        // gh|glab|mock
	FileIssue(ctx, repo, title, body string) (number int, url string, err error)
	GetState(ctx, repo string, number int) (string, error)               // open|closed|merged
	Comment(ctx, repo string, number int, body string) error
	ListComments(ctx, repo string, number int, since time.Time) ([]Comment, error)
}
```

**`GhClient` shells out to the forge's own CLI.** There is no HTTP client and no
token in the process: `gh` and `glab` are already logged in on the machine, and
being logged in twice is how a node ends up with a credential it cannot rotate.
One type serves both forges, because they differ in argv and in the JSON they
print and in nothing else that matters here:

| | GitHub (`gh`) | GitLab (`glab`) |
| --- | --- | --- |
| file | `gh issue create --repo R --title T --body B` | `glab issue create --repo R --title T --description B --yes` |
| state | `gh issue view N --repo R --json state,url` | `glab issue view N --repo R -F json` |
| comment | `gh issue comment N --repo R --body B` | `glab issue note N --repo R --message B` |
| read comments | `gh api repos/R/issues/N/comments?since=...` | `glab api projects/<urlencoded R>/issues/N/notes` |

Neither create command has a `--json`, so the issue number is read off the end
of the URL the CLI prints. `state` is normalised on the way in - `OPEN`,
`opened`, `CLOSED`, `merged` all become one of three words - and GitLab's system
notes ("changed the description") are dropped, because they are the forge
talking to itself rather than somebody reviewing the issue.

**`MockForge` is a map in this process**, with the same interface and a small
control surface - close an issue, comment on it as somebody else, read back what
the node pushed - exposed over HTTP as `/api/forge/mock/*` **only when the mock
is the selected forge**. On a node talking to GitHub those routes do not exist.
It is what the gate drives: the questions worth asking here are the node's, not
GitHub's.

**Which one a node uses is decided once, at startup.** `FLOWY_FORGE=gh|glab|mock`
names it; naming one that is not installed is an error at startup rather than a
surprise at the first filing; naming one that does not exist refuses to start at
all. With `FLOWY_FORGE` unset it is capability detection - `gh` if it is on
`PATH`, else `glab`, else nothing - and detection only *looks the binaries up*,
so a node that comes up with `gh` installed and no credential has still not
touched GitHub. A node with no forge starts anyway and answers `503` with the
reason on `/api/forge/*`: not being able to file a bug is not a reason to refuse
to hold one. `GET /api/forge` says which of these happened.

**The link is a column on the artifact**, `artifacts.external jsonb`, next to
`reported boolean`:

```json
{"forge":"mock","repo":"o/r","number":7,"url":"https://.../o/r/issues/7","state":"open",
 "thread":"01M0...","author":"flowy","since":"2026-08-14T22:21:06Z","seen":["c1"],
 "pushed":117096190244421632,"filed":"2026-08-14T22:21:06Z"}
```

It is a column rather than a table because it is a property of the artifact and
travels with it: federation replicates artifacts last-writer-wins by `hlc`, so a
bug filed on one node and pulled by another arrives already carrying the issue it
was filed as, cursors and all - no second table to merge, and neither node
double-posts the same reply. Both columns are written by `SetArtifactExternal`
alone and are not in the ordinary upsert's column list, so editing the title of
a filed bug cannot unfile it.

**Permission is the ordinary one**: all three endpoints are gated on reading the
artifact, and reading is what makes somebody a participant - the same rule that
lets an assignee in another project move a shared bug through the workflow lets
them answer its reviewer. Anything else is `404`, not `403`, so a probe cannot
learn that an id exists by trying to file it. Filing is separate and explicit
because it is the one operation visible outside this machine: nothing files an
artifact because it looked like a bug, and filing one twice is a `409` carrying
the issue there already is rather than a second issue nobody closes. What a sync
*sends* is the same decision one step later - the replies that go out are the
owner's own, an agent of theirs, or the operator's - because reading an artifact
is not permission to publish it and neither is answering in its thread.

**A closed issue moves the artifact to `done`, and that move is the one
transition the workflow itself would refuse.** The lifecycle has no shortcuts on
purpose, but the forge is the authority on its own issue, and an artifact still
claiming `in-progress` about an issue somebody closed a week ago is worse than a
jump. It is recorded as a `status` event like every other move, with `via:
forge` and the issue in its meta, so the trail says where it came from. A
refresh that finds nothing new writes nothing at all - not even a clock bump,
which would make every peer merge a row that says the same thing.

**The reviewer loop**, one `POST /api/forge/sync`, in both directions:

- **in** - every comment newer than the ref's cursor becomes a `chat` event in
  the artifact's thread, in room `forge`, written by `forge:<login>`: a
  synthetic external principal, deliberately not a `users` row - they hold no
  token here, they can read nothing, and the only thing the node knows about
  them is the login the forge printed. The comment id goes into the event's meta
  and the cursor advances over it.
- **out** - every reply *the owner wrote* in that thread above the push cursor
  goes to the issue as a comment, attributed (`**alice** via flowy:`) because
  the credential posting it is the node's and the person who wrote it is not.
  Everyone else who can reach that thread - a project mate, the assignee, the
  agent working on it - can say what they like in it and none of it leaves the
  building: publishing is the owner's, or the operator's, exactly as filing is.
  Status moves and task handoffs stay here too: the reviewer did not ask for
  this node's bookkeeping.
- **the loop does not echo.** Comments written under the node's own login are
  skipped on the way in, and events written by a `forge:` actor are skipped on
  the way out.
- **idempotent.** `since` plus a capped list of seen comment ids handles a forge
  whose timestamps have one-second resolution; the push cursor moves over
  everything it *looked at*, including what it decided not to send. A sync with
  nothing new returns `pulled: 0, pushed: 0` and leaves the artifact's `hlc`
  exactly where it was.

The thread is the one the artifact already has: if it has been assigned, the
issue's conversation lands in the handoff thread the two people are already
talking in, so the reviewer's question arrives where the work is being discussed
rather than in a second thread beside it. Otherwise filing opens one, and the
filing itself is the first event in it.

## The console

`web/` is a React 19 + TypeScript app built by Vite, styled with Tailwind CSS v4
(`@tailwindcss/vite`, `tailwindcss-animate`) and shadcn/ui components, animated
with framer-motion, drawing thread DAGs with react-flow (`@xyflow/react`), and
linted and formatted by Biome. `npm run build` produces `web/dist`, which
`console.go` compiles into the binary with `go:embed`.

**Routing is by path, through the History API.** Every view is a URL somebody
can bookmark or send:

| path | view |
| --- | --- |
| `/` | overview: who this token is, the inbox, a way into any room |
| `/chat/:room` | the room: messages, the human message box, the thread DAG |
| `/inbox` | the work assigned to this token: state, delegate and done, and the auto-delegate switch |
| `/task/:id` | one handoff: the task, and its thread rendered as chat with its DAG |
| `/p/:project/:type/:id` | one artifact, with the lifecycle control and its history |
| `/activity` | the timeline: every turn, run log line, message and steer this token may read, searchable, with the message box on it |
| `/metrics` | the six metric groups, as this token may see them; a group that could not be measured renders its reason, never a zero |
| `/traces` | the recent traces, and one of them as a waterfall |

Which means the server has to answer with the app for all of them: `flowy serve`
serves `web/dist` and falls back to `index.html` for **any** non-`/api` GET, so
a reload of `/chat/general` lands back on the room. Unknown `/api/*` paths still
`404` in JSON - a client that asked for JSON and got a 200 of HTML would have to
parse the app to find out it had a typo.

Above the room, and above the transport rather than in it, is the announcement
banner: the active announcements this token may read, worst severity first, with
an ack button on the ones that are quiescing something. It polls its own list on
a slow timer - an announcement is not chat - and it has no dismiss, because what
clears it is the announcement being resolved and not this browser being told to
forget it. A read that fails renders nothing at all: a banner that appears over
the top of your work to say it could not read itself is worse than one that
stays quiet, and the room below it reports the same failure already.

The chat view posts as the person holding the token and keeps up by looping the
long poll: `GET /api/chat/{room}` once, then `GET .../wait?cursor=` until the
view goes away, which aborts the request in flight. A failed poll backs off two
seconds rather than spinning. Selecting a message makes the next thing you say
a reply to it - the new message names it in `parents`, and the DAG on the right
grows a lane.

The inbox is tasks rather than messages - `/api/inbox` is the chat you have not
read, `/api/inbox/tasks` is the work you have not done - and each row carries
the two things an assignee ever does with a handoff: pass it to their agent, or
say it is finished. The task view long-polls the same watcher the room does,
narrowed to the thread, so a reply from the other side arrives without a reload.

The lifecycle control draws its options from `next` in `GET /history` rather
than from a copy of the workflow kept in the browser. A console that knows the
rules itself is a console that disagrees with the server the first time they
change, and the disagreement shows up as a button that does nothing.

Auth is a bearer token pasted into the sidebar and kept in `localStorage`; it
goes out as `Authorization` on every `/api` call. There is no login, because the
node has no session - a token is minted by the operator and this console only
carries it.

```sh
cd web
npm ci
npm run dev      # vite on :5173, proxying /api to 127.0.0.1:8787
npm run check    # biome
npm run build    # -> web/dist, which go:embed picks up
```

## The terminal client

`flowy tui` is the console's job done in a terminal: the same endpoints, the
same bearer token, the same permission filter deciding what comes back. It adds
nothing to the server and it holds no database handle - `go list -deps` says so,
and the gate asserts it - because a second client that needed its own reach
would be a second permission filter, and the whole claim of this node is that
there is one.

It exists because of where the work happens. An agent's operator is usually on
the far end of an ssh session inside tmux, and a browser is not there. What is
there is a terminal, often 80x24, often on a machine whose `$TERM` is whatever
the session inherited.

```sh
flowy tui                                   # $FLOWY_ADDR, $FLOWY_TOKEN
flowy tui --url http://box:8787 --token tA-…
```

Seven views, on a tab bar, each one a digit:

| view | what it is |
| --- | --- |
| 1 rooms | the room list, the live stream, a thread/DAG pane, and the message box |
| 2 inbox | `GET /api/inbox/tasks`: state, delegate to your agent, mark done, jump to the artifact |
| 3 artifact | body, badges, history, and the lifecycle moves the node says are legal |
| 4 memory | browse and full-text search, read, write back |
| 5 timeline | the Phase 8 activity log - turns, run logs, chat, steers - searchable and postable-into |
| 6 metrics | the six metric groups as text panels, with bars and a sparkline drawn in cells |
| 7 announce | the active announcements, acknowledge, and post one |

The banner is above all seven: the active announcements this token may read,
coloured by severity, on every view. Like the console's, it has no dismiss -
what clears it is the announcement being resolved.

**The stream is live and the UI never waits on it.** Every call to the node is a
`tea.Cmd`, which bubbletea runs on a goroutine of its own and delivers back as a
message, so the long poll blocking on the server for its window is not the
update loop blocking. Switching rooms bumps a generation counter and the poll
still out for the old room is dropped when it lands, rather than merged into the
room now on screen. A lost connection is a line on the status bar and a poll
that keeps trying: a node that went away comes back, and a client that stopped
polling the first time a laptop slept would need restarting.

**The keys are the point, and two of them are not bound.** `ctrl-a` and `ctrl-b`
are screen's prefix and tmux's, and the text box binds neither - upstream's
default puts "start of line" on the first and "back one character" on the
second, which inside tmux is dead weight and outside it is a surprise. `home`,
`end` and the arrows do the same work and collide with nothing. Nothing else in
the client is on a control key either, and no mouse is captured, so the
terminal's own selection and tmux's copy-mode still work.

```
j/k or arrows  move          tab / shift-tab  next / previous view
1 … 7          a view        enter            open
/              search        i                insert: post, or compose
esc            leave a box, close the help, go back
r              refresh       ?  help          q / ctrl-c  quit
```

Rooms adds `o` to open any room by name, `n`/`p` for the next and previous one
and `t` for the thread pane; inbox adds `d` delegate and `x` done; artifact adds
`s`, then a digit off the list the node returned in `next`; memory adds `i` and
`e`; timeline adds `f` for the kind filter; announcements add `a` to
acknowledge and `v`/`c` to pick a severity and a scope before posting one.

**Personal stays personal.** A memory written here is written `personal`, which
is what the memory surface's own default is, and an edit sends no visibility at
all so the node keeps whatever the row already had. Promoting something to a
project is a thing you do on purpose; a terminal client that did it as a side
effect of fixing a typo would be a leak with a keyboard shortcut.

**It reflows, and it degrades.** Every pane computes its geometry from the
current width and height at render time, so `SIGWINCH` is a redraw and there is
no cached layout to go stale - the gate resizes it twice under a real pty and
renders every view at 20x6, 40x12, 80x24, 132x43 and 200x60, asserting no line
is wider than the terminal and no pane taller. Colour comes from `$TERM` and
`$COLORTERM` through lipgloss, which degrades truecolor to 256, to 16, and to
none; `NO_COLOR` and `TERM=dumb` get none, and a locale that does not say UTF-8
gets `#` and `-` instead of block and box characters.

**And it gives the terminal back.** `q` and `ctrl-c` quit, the alternate screen
is left so the pane's scrollback comes back, and raw mode is off. That last one
is the failure a model test cannot see - a model has no terminal - so the gate
runs the built binary on a real pty and reads the pty's termios afterwards:
`ECHO` and `ICANON` on, or the check fails.

## Observability

The fabric watches itself. Because the store is Postgres-wire, the engine's
numbers come out of the database; the fabric's own - corpus, sync,
collaboration, permissions - are layered on top of the reads the API already
does, which is the point: they are the *same* reads, behind the same filter.

Three habits run through all of it, and they are worth naming because each one
is a way a dashboard usually lies:

- **Report only what was measured.** Nothing here is estimated, extrapolated or
  defaulted. A group that could not be read carries `available: false` and the
  reason.
- **Name the denominator.** `cpu 0.42` is not a number. `0.42 of one core,
  over the last 37 seconds, on a machine with 4` is. Every rate and every share
  carries what it is a share of, in the payload, beside the value.
- **Empty is could-not-read, not all-clear.** The failure mode this is written
  against is a console showing `0` for "we were not allowed to look". So a
  group's reason is rendered where its numbers would have been, and even the
  Prometheus scrape carries `flowy_group_available` per group.

### GET /api/metrics

Six groups, every time, each one able to say it was not measured:

| group | what is in it |
| --- | --- |
| `node` | uptime, the DB pool in-use/max, CPU as a share of one core, RSS, whether the store answers and how fast. The operator's: everybody else gets `available:false` and the reason |
| `corpus` | artifacts by type, scope, project and owner; events; growth over 24h and 7d; index coverage; bytes on disk (operator) |
| `sync` | per-peer cursors, last-seen age, pending push, the offline queue, conflicts. The operator's, like `GET /api/peers` |
| `collaboration` | messages in 24h and per day for a week, tasks by state, open todos, active rooms, active people and agents, handoffs in flight |
| `permissions` | grants you are party to, single-artifact shares, cross-project grants, and what this node refused you in 24h |
| `anomalies` | one verdict per watched series, against this scope's own recorded history |

**It is scope-filtered, and that is a security property rather than a nicety.**
A principal of `pa` gets `pa`'s corpus and collaboration numbers and not `pb`'s;
a personal artifact is counted for its owner and for nobody else; a refusal is
counted for whoever was refused. `?scope=all` is the node, and it is the
operator's alone - for anybody else the parameter is simply not there, and the
answer's `scope` block says whose numbers these are.

`GET /metrics` is the same measurements in the Prometheus exposition format,
behind the same token and the same filter - a scrape is a read, and an
unauthenticated one would hand the shape of the whole corpus to anybody who
asked. The path is shared with the console page: a request with a bearer token
is a scrape, a request without one is a browser following a link and gets the
app, which then reads `/api/metrics` over `fetch` with the token it holds.

### Anomalies, and the refusal

The comparison is against **recorded history** - readings this node took of
itself, at most one a minute, kept per scope so two principals' different
corpora are never averaged into one baseline - and never against a threshold
somebody picked. Nothing here says "more than 100 messages is a lot", because
that is a number about a deployment nobody has seen.

Below `store.MetricMinSamples` readings there is no verdict:

```json
{ "series": "corpus.artifacts", "verdict": "insufficient samples",
  "latest": 41, "samples": 3, "required": 8,
  "reason": "3 of 8 readings recorded; no verdict is drawn below 8" }
```

Not `"normal"`, and no baseline beside it - a number printed next to the word
"insufficient" is a number somebody will act on. Above it, the verdict carries
what it rests on: the mean, the spread, the z, and how many readings made them.

### Traces

Every operation is an OpenTelemetry span: the HTTP request or MCP call, the
permission check that decided who was asking, the queries that ran under it, the
ingest, and each leg of replication. `internal/otel` implements the parts
anything else has to agree with - 16-byte trace ids and 8-byte span ids in hex,
W3C `traceparent`, and OTLP/HTTP with a JSON body POSTed to
`<endpoint>/v1/traces` - rather than vendoring the SDK for one span type and one
exporter. Set `FLOWY_OTLP_ENDPOINT` and the spans go to a collector as well as
into the store; leave it unset and they are kept here and exported nowhere.

Spans are recorded locally and **not replicated**: a span is this node's account
of what this node did, and merging somebody else's telemetry into the fabric
under the fabric's own rules is not what telemetry is. They are read back
through a filter like everything else - a span is its principal's first, then
its project's, and then only as far as the artifact it names.

**The trace id rides the sync deltas.** This is the part that makes a handoff
followable end to end across nodes, and it cannot be done with a header: nothing
requests anything of the node the work is going to, because what crosses is a
delta. So the id travels on the row - in the `meta` of the event that opens the
assignment's thread, which is *inside that event's signature*, so a relay
holding neither node's key cannot rewrite it. When the delta lands, the
receiving node records a `handoff.deliver` span under that same trace id, with a
span id derived from the event's so that applying the same page twice is one
delivery. When somebody there opens or works the task, the request adopts the
thread's trace instead of starting a second one - and because a request's child
spans are held until its root ends, the permission check and the queries come
with it rather than being stranded in a trace of one.

The result is one trace id in two databases, and `flowy traces --trace <id>
--peer <url>` is the collector that reassembles them into one waterfall.

### The activity timeline

`GET /api/activity` is every turn, run log line, chat message and steer, in one
order, searchable. They are four kinds of the same event log - `turn`,
`run.log`, `chat`, `steer` - so the timeline is one read with the event filter
in its `WHERE` clause: a run in another project is not on it, a personal item is
on nobody's but its owner's, and the thread of a handoff is on the timeline of
the two people it is between. Anything else in the log - a status move, a task
move, something the forge bridge did - shows as `activity` rather than being
hidden, because a timeline that quietly omits rows lies about what happened.

It is **postable-into**, which is why the message box is on it and not only in a
room: `POST /api/activity {kind, body, room?, thread?}` writes into a room, into
a run's thread, or into a subagent's branch of one, through the same three gates
`POST /api/chat/{room}/say` keeps. The kinds a client may post are the four; a
`status` or a `task` event is a claim this node makes by doing the thing, and a
timeline that let a client write one would be a timeline whose entries mean
nothing.

### The tools an agent gets

Four MCP tools, named the way serenedash names them, so an agent that already
knows that vocabulary does not need a second one:

| tool | what it answers |
| --- | --- |
| `status` | node health (the operator's), the conversation and work you are party to, replication |
| `activity` | the timeline: `q`, `kind`, `room`, `thread`, `since` |
| `storage` | the corpus you may read, its index coverage, and the grants around it |
| `anomalies` | the verdicts, including the refusals |

Same code as the HTTP endpoints, same filter, same refusal wording.

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
  is what Phase 5 merges concurrent edits by: last-writer-wins on `hlc`, and the
  same winner picked on both sides because the readings are total and travel
  with the row.
- **`events` is append-only and carries the thread DAG** in `parents text[]`.
  No parents opens a thread, one continues it, several merge branches. Peers
  page through the log by `seq_hlc`.

| table | holds |
| --- | --- |
| `users` | people; `auto_delegate` decides whether work can go straight to an agent |
| `agents` | agents acting for a user; `kind` is the runtime (`claude`\|`glm`\|`opencode`), `agent_kind` is what it is for (`worker`\|`reviewer`\|`system`\|`monitor`, default `worker`) |
| `tokens` | bearer token to `(user, agent, project)`; local, never replicated |
| `grants` | cross-project capabilities, tombstoned rather than deleted |
| `artifacts` | transcripts, memories, chats, bugs, features, notes |
| `events` | the append-only log and its DAG |
| `tasks` | handoffs: one artifact, from one user to another, with a thread and a state of `open`\|`delegated`\|`done` |
| `peers` | replication bookmarks: `pull_cursor`, `pushed_cursor` and `last_seen`, one row per peer, neither cursor ever moving backwards |

Phase 6 adds two columns to `artifacts` rather than a table: `reported boolean`
and `external jsonb`, the link to an issue on a forge and the two cursors the
reviewer loop keeps. They are written only by the forge endpoints, replicate
with the row they are on, and are indexed by `reported` for "what have I filed".

Phase 6.5 adds `sig bytea` to `artifacts`, `events`, `tasks` and `grants` - the
writing node's signature over the row - and one table:

| table | holds |
| --- | --- |
| `node_identity` | one row per node this one has to believe: `node_id`, `public_key`, the node's own signature over the two, `pinned`, and - for this node alone - `private_key`, the ed25519 seed that never leaves the machine |

The `sig` columns are nullable, because the column is not where the rule lives:
the merge requires a signature that verifies under the key of the node named on
the row and refuses the row when there is none, which is a place that can say
*why*. `node_identity` replicates its public half and only its public half.

The second security slice adds one more local table beside `peers`:

| table | holds |
| --- | --- |
| `sync_pending` | artifacts a grant made readable below a reader's cursor that did not fit in the page the grant arrived on: `(principal, artifact, sent_hwm)`, drained by later pulls and struck off when the reader's cursor passes the mark they went out under |

Like `peers` it is bookkeeping about replication rather than fabric state, so it
carries no `hlc`, replicates nowhere, and means nothing on another node.

Phase 7 adds one more of the same kind:

| table | holds |
| --- | --- |
| `fs_intents` | one file write, written down before it happens: the mount path, the artifact id, the owner, the actor, the scope, the name, the bytes and their sha256, and `applied` - NULL until the drainer has turned it into a row |

It is the queue behind the FUSE mount and it is local for the same reasons: no
`hlc`, no signature, never replicated. A peer has no business knowing what a
file on this machine was called or when it was closed. The artifact the drainer
writes out of it is the fabric row, and that one is stamped and signed like
every other.

An artifact with `project` NULL is personal to `owner_user`; `visibility` is
`personal`, `project` or `shared`. `kind` narrows `type` without multiplying it -
a memory item is `type='memory'` with a kind of `note`, `todo`, `feature` or
`handoff` - so one table, one permission filter and one search index serve all of
them. Indexes cover the reads the later phases do: artifacts by `(project, type)`
and `(type, kind)`, events by `thread` and by `seq_hlc`, plus owner, grant
direction and task inbox. Phase 4 adds the ones its reads need: `tasks` by
thread, sender and assignee agent - the event filter asks "is this thread a task
of mine" on every read of the log - and `events` by `(artifact, type)`, which is
what a status trail is.

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
- `internal/sign` - the canonical encoding and the two ed25519 operations. One
  `Canonical<T>` per replicated row type, length-prefixed and domain-tagged, and
  `Sign`/`Verify` over the result. It knows nothing about the store: the row
  views it takes are plain structs, so the encoding can be tested field by field
  without a database, which is what its unit tests do.
- `internal/store/identity.go`, `internal/store/rowsig.go` - the store's half of
  signing. `identity.go` is the keypair, the pin, trust-on-first-use, the
  no-rotation rule and what a page hands over; `rowsig.go` is the adapters from
  the store's rows to `internal/sign`, one struct literal per table, so adding a
  replicated column and forgetting to authenticate it is a diff somebody can
  see.
- `identity.go` - `flowy identity` and `flowy sign`: the operator's commands and
  the one that signs a delta by hand.
- `internal/store` - the Postgres-wire persistence layer. Stamps id, clock and
  node on the way in; reads rows back with their arrays and jsonb intact.
  `perm.go` holds the principal, the read predicate and the SQL filter;
  `artifacts.go` and `events.go` hold the queries that carry it.
- `internal/store/sync.go` - replication, both halves. `SyncPull` is a
  permission-filtered read of all four replicated tables plus the high water
  mark; `SyncApply` is the merge - append-only for events, last-writer-wins by
  `hlc` for everything else - in one transaction, reporting what it actually
  changed rather than what it was handed. The peers bookmarks and `SeedClock`
  are here too.
- `sync.go` - the node half: `GET /api/sync/pull`, `POST /api/sync/push`,
  `GET /api/peers`, and the `flowy sync` driver that pages between two nodes and
  moves the cursors. Both endpoints and the driver go through the one merge, so
  a row cannot be applied one way over a pull and another over a push.
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
- `tasks.go`, `lifecycle.go` - Phase 4's handlers. `tasks.go` is assignment,
  the task inbox, delegation and state; `lifecycle.go` is the workflow table,
  the transition check and the status trail. Both reuse what was already there:
  assignment writes a `grants` row through the Phase 1 store and opens the
  thread through Phase 3's chat event, and a transition is one column and one
  event.
- `internal/store/tasks.go`, `internal/store/lifecycle.go` - the task row, the
  party filter that decides who may read one, the inbox query that joins the
  artifact in through the ordinary permission filter, and the two status
  queries.
- `announce.go`, `internal/store/announce.go` - announcements and quiesce.
  `announce.go` is the handlers: the scope check against the principal's agent
  kind, what a well-formed announcement is, and the `409` that holds a resolve
  while a resource is held. `internal/store/announce.go` is the domain - the
  scopes, severities and modes as closed sets, the fields blob, the write that
  puts the announcement and its log entry in one transaction, the quiesce
  accounting over the event log, and the node-scope predicate in both the SQL
  the pull door reads and the Go the push door reads, written next to each other.
- `web/src/components/AnnouncementBanner.tsx` - the banner. It reads the active
  list and paints it; it has no filter and no dismiss of its own, because both
  of those belong to the node.
- `chat.go` - the room view of the event log: `say`, the room read, the long
  poll and the inbox. It adds one field to `store.EventQuery` (`NotActors`) and
  otherwise reuses the log's cursor, DAG and permission filter as they are.
- `internal/forge` - the forge bridge's client half: the `ForgeClient`
  interface, `GhClient` (which serves both `gh` and `glab`, with the runner as a
  field so the argv and the parsing are unit-testable without a binary or a
  credential), `MockForge`, and `Select`, which is the whole of the startup
  decision.
- `forge.go`, `internal/store/forge.go` - the node half. `forge.go` is the three
  endpoints, the capability answer and the mock's control routes; the store side
  is the `ExternalRef`, its two cursors, the one write that sets them, and the
  lookup that finds an artifact's existing handoff thread. Both reuse what was
  there: a threaded comment is a Phase 3 chat event, the status move is Phase
  4's status event with one extra meta field, and the link replicates because it
  is a column on a row Phase 5 already merges.
- `internal/agentfs` - the FUSE filesystem. `agentfs.go` is the tree (four
  directory levels, each one more of the scope fixed) and the enqueue a close
  performs; `file.go` is one artifact seen as a file, its handles and the
  truncate the kernel splits off the open; `format.go` is the front matter and
  the rules for a filename, which are byte rules; `drain.go` is the drainer;
  `mount.go` is the mount, the negotiated protocol check and the unmount. It
  holds no memory of its own: the tree is rebuilt from the store on every
  lookup, and the only state is what has been closed and not yet drained.
- `internal/store/agentfs.go` - the store's half. The scope reads the mount's
  directories are made of, all of them narrowed by `ArtifactFilterSQL` rather
  than by a second reach rule, and the intent queue: enqueue, the pending read,
  and the one transaction that writes the artifact, its event and the applied
  stamp together - with the hash dedup, the ownership check and the personal
  floor evaluated inside it.
- `fuse.go` - `flowy fuse`: the flags, the principal the mount acts as, the
  reconcile that needs no token, and the signal handling that unmounts.
- `internal/tui` - the terminal client. `client.go` is the HTTP API as this
  client sees it, one method per endpoint and the response types field for
  field; `model.go` is the root bubbletea model - the tab bar, the banner, the
  status line, the one text box and every key; `cmds.go` is every call to the
  node, each one a `tea.Cmd` so nothing blocks the update loop; `theme.go` is
  the colour profile, its degradation and the bars and sparklines; and one file
  per view. It imports no store and no database driver.
- `tui.go` - `flowy tui`: the flags, where the URL and the token come from, and
  the program that runs the model on the alternate screen.
- `console.go`, `web/` - the console and its serving. `console.go` embeds
  `web/dist`, serves hashed assets immutably and falls back to `index.html` for
  every other non-API path; `web/` is the React app itself.

## What the gate asserts

`schema.sql` loads and reloads, `go build`, `gofmt`, `go vet`, `go test`, then
against a live `flowy serve`:

- `/healthz` comes up and reports `ok:true` with the database up
- the eight spine tables exist
- `flowy fuse` with nothing said mounts nothing and says it wants `--mount`,
  and a mount asked for with a token that does not resolve attaches nothing
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

Then Phase 3 - the frontend build, the chat API, and the console as it is
actually served:

- `npm ci`, `biome check .` and `vite build` all succeed, and `web/dist/index.html`
  holds an app root and references a hashed JS asset that is really in the build
- the built bundle **mounts**: `web/scripts/render-check.mjs` loads it in jsdom at
  `/chat/general` with no token and asserts the app painted the shell and the
  room view. A bundle that is served and throws on mount looks identical from
  the outside otherwise
- A says a message and then a reply naming it in `parents`: the room reads back
  in order, the edge survives, `seq_hlc` advances, and the reply inherits the
  thread
- A's **agent** says one in the same room: the actor is the agent, `meta` marks
  it as one and still carries the person it works for, and the room's three
  messages split two human and one agent
- `wait?cursor=` returns only what is newer; a caught-up poll **blocks** and
  wakes with a message posted a second later; a poll of a quiet room returns an
  empty list when its window runs out rather than hanging
- A's inbox excludes A's own messages and contains the agent's; the agent
  token's inbox excludes both its own and its user's
- A **in `pc`**, with no grant into `pa`, sees none of `pa`'s `general` - and
  its own `general` is a different room holding only its own message. B, holding
  the grant Phase 1 issued, does see `pa`'s room and not `pc`'s
- `GET /` is `200 text/html` with the app root and the bundle; the bundle is
  served next to it; `/chat/general`, `/p/pa/bug/01H` and `/metrics` all return
  the same index; `GET /api/does-not-exist` is `404` in JSON with a token and
  `401` without one
- the console, signed in with a real token against the live node, **fetches the
  room and renders it**: the same jsdom mount, pointed at the running server,
  waits for A's first message to appear on screen
- A creates a bug in `pc`, which nobody holds a grant into, and B gets `404` on
  it. A assigns it to B, and in one response: the task names the artifact, the
  sender and the receiver; the grant names the artifact and B; the opening
  message is a `chat` event; and all three carry the same `hlc` reading
- **the share landed** - B now reads that artifact across the project boundary,
  the task is in `GET /api/inbox/tasks` with the artifact's title joined in, and
  the thread holds the opening message. B answers in it and A, in a third
  project, reads both halves back in order
- a personal artifact cannot be assigned, and neither can one the caller cannot
  read
- B's `auto_delegate` defaults on, so that first task arrived already
  `delegated` to B's agent. `PUT /api/me/auto_delegate {"on":false}` flips it,
  the next assignment stops at `open`, `POST /api/task/{id}/delegate` hands it
  to the agent and appends `open->delegated` as a child of what was in the
  thread, and B's **agent token** then closes it - which the sender sees
- the sender gets `403` delegating somebody else's work; a third party - this
  node's operator, the most privileged principal there is - gets `404` on the
  task, `404` moving its state, and an inbox with neither task in it, and the
  task does not move
- an issue walks `open -> triaged -> in-progress -> in-review -> done`, each
  move answering with the artifact and a `status` event whose body is the move;
  `/history` returns the four in order, each naming the one before it as a
  parent, with the first opening the trail
- `done` refuses to move and the artifact stays put; `open -> in-review` is a
  `409` and a status nobody has heard of is a `400`, and neither writes an
  event; `triaged -> wont-fix` works and leaves nowhere to go
- the **assignee** moves the status of a bug in a project they are not in, and
  both sides read back the same trail; an artifact you cannot read has no status
  you can move or history you can see; a transcript has no lifecycle at all
- `GET /inbox` and a `/task/{id}` deep link are the app, and the console -
  mounted in jsdom against the live node as **B** - renders B's inbox with the
  assigned bug and its state on screen
- `psql` sees the tasks, one delegated to an agent and one closed, the
  per-artifact share joined to the task it was written for, the assignment
  threads in the chat log, the task moves in the same threads, the status trail
  with a move carrying its predecessor, and `auto_delegate` switched off for one
  user
- `psql` sees the chat rows in the same `events` table, a reply carrying its
  parent, and two projects with a room called `general`

Then Phase 5, against **two** nodes - two clusters, two `flowy serve`
processes, `nodeA` and `nodeB` - seeded with the same principals and driven by
the real `flowy sync`:

- both nodes come up on their own databases with their own names, and neither
  holds a single artifact the other wrote
- the replication token resolves to the same principal in the same project on
  both, which is what lets a peer authenticate at all
- each node's operator reads the other's public key off it and pins it, the two
  keys differ, and neither node hands over a private one
- a row that crossed carries the **same signature bytes** on both nodes, and no
  row of either node's is on the other without one
- A opens `pa` up to `pb`, writes a shared artifact, a **personal** one and one
  in `pc` that nobody granted the peer, and a thread of two events; B writes one
  of its own in `pb`
- one sync, and A's artifact is on B with **the same id, the same `hlc` and the
  same author** - and searchable there, because the search vector is rebuilt on
  the way in rather than shipped. B's artifact is on A the same way, still
  stamped `nodeB`
- the thread is on B with its `parents` edge intact and the node that appended
  it unchanged
- the personal artifact is **not on B at all**, and neither is the one in `pc`;
  `GET /api/sync/pull` offers the peer neither of them and offers the granted
  one, which is the permission filter deciding replication rather than a second
  rule about it
- **conflict**: the same artifact is edited on A at `h1` and then on B at
  `h2 > h1` with no sync between. After one sync both nodes hold the `h2`
  version, at `h2`, authored `nodeB`, and **exactly one row each** - no lost
  update, no duplicate
- A deletes it: the tombstone reads after the edit it removes, and after a sync
  both nodes answer `404` for it while both tables still hold the row, at the
  delete's reading, gone from B's list and B's search
- an assignment on A arrives on B as all three of its parts - the task, the
  share that makes the artifact readable, and the thread it opened
- a sync with nothing new **moves nothing**: pulled and pushed are zero on both
  sides. The same delta pushed twice is received twice and applied once, and B
  ends with one row at the reading A wrote
- each node holds a `peers` row for the other with both cursors moved and
  `last_seen` set; `GET /api/peers` answers the operator and `403`s everyone
  else; `flowy sync` refuses a run with no peer, and refuses a token that names
  no principal on this node before anything is sent anywhere
- the two databases **agree row for row**: every artifact id they both hold
  carries the same reading, the same author and the same tombstone state
- `psql` sees A's rows on B and B's on A stamped with the node that wrote them,
  the replicated DAG, the tombstone as a row rather than a hole, the replicated
  task joined to its share, the personal artifact on A and only on A, and both
  cursors on both nodes

Then the forge bridge, against `MockForge`:

- `FLOWY_FORGE=mock` selects the mock **while `gh` is on `PATH`** - the node
  reports `forge: mock`, `why: FLOWY_FORGE=mock`, `available.gh: true` - and a
  forge nobody has heard of (`-forge bitbucket`) is refused at startup
- filing a bug writes an external ref - `{forge: mock, repo: o/r, number, url,
  state: open}` - marks the artifact `reported`, and logs a `forge` event saying
  `filed o/r#N`. An ordinary `GET /api/artifact/{id}` carries the ref, so the
  link is on the row rather than in one response
- filing the same artifact twice is `409` **carrying the issue there already
  is**; a repo that is not `owner/name` files nothing and leaves no link; an
  artifact nobody filed has nothing to sync
- a principal who cannot read the artifact gets `404` on all three endpoints -
  file, status and sync - the same as on the artifact itself
- the reviewer closes the issue on the forge: `GET /api/forge/status` moves the
  artifact to `done` and writes `open->done` into the status trail with `via:
  forge` and the issue number in its meta. A second refresh moves nothing and
  **leaves the artifact's `hlc` untouched**
- the reviewer comments: one sync threads it in as a `chat` event in the
  artifact's thread, actor `forge:reviewer`, carrying the comment id in its
  meta - and it reads back through the ordinary `GET /api/chat/forge?thread=`
- a reply said in that thread through the ordinary chat endpoint is pushed out
  on the next sync: the mock forge has it, posted as `flowy`, attributed to the
  person who wrote it here. It does not come back in as a comment on the sync
  after that
- a sync with nothing new is a no-op, **twice over**: `pulled: 0`, `pushed: 0`,
  the same number of events in the thread, the same number of comments on the
  issue, and the artifact at exactly the same clock reading
- **`gh` was never invoked**: the script the gate put on `PATH` recorded nothing
- `psql` sees the artifact `reported` with its `external` link, the filing in
  the event log, the comment that came in from the forge, the reply that went
  out in the same thread, and the status move the forge caused

And the security slice, one check per defect - each written against the fix and
run against the source it fixes to see it fail first:

- `POST /api/sync/push` answers the peer named in `FLOWY_PEERS` and refuses
  every other token, agent identities included, with `403`
- a pushed grant into a project the pusher has no say over is **received,
  refused and not written**: the response says `refused.grants: 1` with the
  reason, and the row is not in the peer's `grants` table
- a pushed reading of `MaxInt64` is `400`, nothing is merged, and afterwards the
  node still writes strictly increasing positive readings that page by cursor
- writing to an artifact this principal cannot read is `404`, and the row is
  untouched: owner, project, visibility, title and body all as they were
- an event is signed by the token that appended it, whatever the body says, for
  a user token and an agent token both - and `status`, `task` and `forge` are
  refused as hand-written types
- a reader who is not the owner cannot file (`403`, and nothing is filed), the
  owner cannot file into a repository the operator never named (`403`), the
  owner can file into the one that is on the list, and a reader who was handed
  the artifact still cannot push its thread out to the forge
- with the mock forge refusing the second of three replies: the sync answers
  `502` with one comment on the issue, and the next one sends the two that were
  left - each of the three is on the issue exactly once
- an artifact shared after the peer's cursor had passed it arrives on the peer
  anyway, and reads back there
- `mem_write` refuses an id that is not a memory item and leaves it a bug
- a reply naming a parent in a project it cannot read does not join that
  parent's thread, and does not appear in it
- `affectedRows` reports a driver that will not count the rows it changed
- a comment made at exactly the cursor survives a hundred more at the same
  instant, and both shapes of the seen list still parse

And the second slice, the same way - one check per defect the re-review found:

- an event pushed under another user's name, of a type this node mints, is
  received, refused and not in the log, while the one the pusher signed in the
  same delta is applied
- a peer holding a read-share on somebody else's artifact pushes a newer version
  of it naming itself as owner: `refused.artifacts: 1`, and the row still has
  its owner, its project and its title
- a pushed share of an artifact the pusher does not own is refused however it is
  signed, and no grant row is written
- a pushed task naming a thread in a project the pusher holds no grant into is
  refused, and that thread still reads back empty for them
- `GET /api/forge/status` on an artifact whose link names a repository that is
  not on the operator's list is `403`, and nothing about the artifact moves
- with the log refusing the third of five inbound comments: the sync answers
  `502` with two threaded, and the next one threads three - each comment is in
  the thread exactly once
- the mock forge renamed to `flowy-bot`: filing records that login on the link,
  and a comment by it is treated as this node's own and not threaded back in
- a `gh` invocation that fails names `issue create --repo o/r` and carries
  neither the title nor the body of what it was filing
- a comment recorded while the issue was being opened is threaded in by the
  first sync rather than lost behind the cursor
- a project-wide grant with a page of one row still carries both of the old
  artifacts it opened, over successive pulls
- `checkEvent` row by row in the store, and the endpoint's minted types and the
  store's are asserted to be one list
- a peer that answers a pull with four rows written straight into its own
  database - a grant into a project nothing opens, and the artifact, event and
  task that lean on it - has all four refused, and none of them land
- a pushed artifact cannot be filed into a project the pusher cannot reach, and
  one of the pusher's own cannot be walked from one project into another
- a party to a task cannot push it back with its thread swapped for a
  conversation it may not read, and still reads that thread as empty afterwards
- a message into a thread the speaker cannot read is `403` on both endpoints
  that write one, and saying something without naming a thread still opens one
- two rows at the same reading from differently-named nodes have a winner, and
  the loser is ignored rather than refused
- a status refresh by somebody who can read the artifact but does not own it is
  `403`, and the owner's own refresh still goes through
- a push the peer refused leaves the pushed cursor where it was, the next run
  offers the same rows again, and the cursor moves once the peer takes them
- a reader cannot tombstone an artifact it does not own, in the store rather
  than in the handler
- two rows at one reading straddling a `LIMIT 1` page boundary are both
  delivered, in a replication pull and in a room's chat log alike, and the
  cursor each reports steps over neither
- posting the id of an artifact you own but cannot read from the project you are
  acting in is `404`, and the row keeps its project and every field
- a party cannot push its own task delegated to an agent that does not act for
  the assignee, nor into a state the lifecycle has never heard of, and the move
  a party really can make still lands
- a reply to a readable message in an unreadable thread starts a thread of its
  own rather than joining that conversation
- a deleted artifact is `404` by id, for its history and for a status move, and
  an edit by its owner does not raise it
- a memory item written at `scope=project` is not readable through the
  cross-project grant that reaches the `shared` one beside it
- `/healthz?counts=1` reports the spine tables to the operator's token and to
  nobody else, and `/healthz` itself stays open
- an assignment whose task cannot be written leaves neither the share nor the
  opening message behind, and a status move whose entry cannot be written does
  not move the status

Then Phase 7, which mounts a real filesystem in the machine the gate is running
on - `/dev/fuse`, `fusermount3`, one `flowy fuse` process per mount - and does
its file operations from the shell:

- before anything is mounted, nothing of type `fuse.*` is attached under the
  gate's work directory, and `mem_write` and `mem_read` work anyway: everything
  above this point in the gate ran with no mount at all, which is what opt-in
  means said as an assertion
- `flowy fuse --mount` attaches, and logs the protocol it negotiated rather than
  the one it asked for; `_personal`, the token's project, and `<user>/memory`
  and `<user>/note` under each are directories
- a file written into `_personal/<A>/memory/decisions.md` becomes one artifact:
  `type=memory`, `visibility=personal`, `project` NULL, the title and tags off
  the front matter, `file_path` the name it was written under, a signature, and
  exactly one `memory.write` event - with the intent marked applied
- `mem_search`, `GET /api/search` and `mem_read` all find it, so a file is
  indexed memory and not a file
- it reads back through the mount with its header, its scope and its id
- an item written by `mem_write`, with no file involved, is a file in the mount:
  the mount is a view of the store rather than a second copy of it
- a file in a project directory saying `scope: shared` lands in that project as
  `shared`, with the kind its header asked for, and the agent on the other side
  of the `pa -> pb` grant finds it by search without mounting anything
- a file in a project directory saying nothing lands `project-only`, and that
  same grant does not reach it
- a second mount, of a second principal, holds the shared file and none of the
  first principal's personal or project-only ones, and cannot write into the
  first principal's directory
- `mkdir` at the root, a project this token has no business in, a dotfile, and a
  personal file asking for `scope: shared` are all refused - the last one on the
  `close(2)`, where a write-behind filesystem has to put it, with nothing
  written
- saving the same bytes again queues a second intent and writes nothing: one
  artifact, one event
- `rm` tombstones the item, and it is `404` through `mem_read`, `404` through
  the API, and out of the search index
- unmounting empties the mountpoint, and mounting again shows the same items -
  including the one written by the tool - and not the deleted one
- a file written into a `--no-drain` mount and then deleted before anything
  could drain it takes its own write off the queue, and a reconcile afterwards
  does not bring it back
- a mount started with `--no-drain`, written to, and then killed with `SIGKILL`
  leaves the intent pending and the store with nothing: the file still reads
  back through the mount before the crash, because the write is behind and the
  file is not
- `flowy fuse --reconcile` replays it into exactly one artifact with the right
  title, exactly one event and a signature; running it again writes nothing and
  does not move the row's clock; the queue ends empty and the replayed item is
  searchable
- with everything unmounted again, `mem_write`, `mem_search`, `mem_read` and the
  API still work, and the items that arrived as files are still items

And Phase 8, on the same live node and across the two federated ones:

- `GET /api/metrics` answers all six groups, every one of them saying whether it
  was measured, and the answer names whose numbers they are
- the corpus a principal is given is exactly the corpus that principal may
  list - to the row, for two different tokens - and an artifact in `pa` that no
  grant reaches is counted for `pa` and not for the token holding a grant into it
- a personal artifact moves its owner's total by one and nobody else's at all
- a principal that is not the operator gets `available:false` and a reason for
  node health and for the replication cursors, with no `uptime_s` beside it, and
  `?scope=all` does not widen a single number for them - the scope key it is
  answered under is still their own
- the operator's `?scope=all` is the node: the store answering, the pool's
  ceiling, resident bytes, and CPU as a share of **one core** with the
  denominator and the window in the payload
- what was not measured says so: the pull side of replication names the peer's
  high water mark as the reason, and embeddings are `0` of a named denominator
  rather than a share of an index nothing built
- **anomalies refuse a verdict below the minimum sample count** - the answer is
  `insufficient samples` with the count it has and the count it needs, and no
  baseline beside it - and with twelve readings inserted into this node's own
  history the same series comes back `unusual` with what it rests on, while the
  other principal's view of that series is still a refusal, because a history is
  per scope
- a 403 is counted for the principal it was given to, a 401 with no principal is
  the operator's to see, and neither records the row that was refused
- `GET /metrics` is the same measurements in the Prometheus format, one `HELP`
  per family, labelled with the scope - and without a token it is the console,
  because a browser following a link sends no `Authorization` header
- the timeline holds turns, run log lines, chat and steers in log order, is
  searchable by what was said, refuses a `kind` the node mints by doing the
  thing, and can be posted into - a steer into a run's thread lands in that
  thread, as that kind, said by the token that sent it
- and it is filtered: a run written in `pb` is on `pb`'s timeline and on nobody
  else's, over the API and through the `activity` tool alike
- a request is a trace: the permission check that decided who was asking and the
  queries that ran under it, in one trace, readable back by id - and a second
  principal asking for that trace by id is handed none of it, while the operator
  sees all of it
- the OTLP exporter reaches a collector that is not this node: a real POST to
  `/v1/traces`, carrying spans of a request that really was made, in the
  protocol's own shape
- the four tools an agent already knows - `status`, `activity`, `storage`,
  `anomalies` - are offered and each answers what that token may read
- **one handoff is one trace across two nodes**: assigned on A, the trace id on
  the opening event's `meta` in A's database, delivered to B as a
  `handoff.deliver` span under the same id after a real sync, and B's request to
  open the task answering with that same trace on the wire and recording its
  spans under it. Syncing again records no second delivery
- `flowy traces --peer` reassembles both halves into one waterfall, in start
  order, naming both nodes and how many spans each gave - and a peer it cannot
  reach is reported with the reason rather than silently left out
- the metrics tab, the traces tab and the timeline mount in a real DOM against
  the live node and render what it measured, what it did, and what was said

Announcements, system agents and quiesce:

- an agent seeded without a kind reads back as `worker`, no `agents` row is left
  with a NULL kind, the runtime column is untouched, and the kind reaches the
  principal: `/api/whoami` says `system` for the system agent's token, `worker`
  for the ordinary one, and nothing at all for a person's - a person is not an
  agent of the least privileged sort. A kind nothing implements is refused
- a worker agent, a person and the operator are each refused federation scope,
  and the system agent is not - while the same worker agent may still post about
  its own node, so what was refused is the scope and not the agent.
  `POST /api/artifacts` refuses
  `type: announcement`, so the capability has one door and not two
- a scope, a severity or a mode nothing implements is a `400`; so is a notice
  that tries to quiesce a resource, and a mode with no resource to apply to
- the four minted quiesce types are refused to a client writing events by hand,
  and the endpoint's list and the store's are held together by a test
- the console banner paints an active announcement above the room in a real DOM
  against the live node, and **is gone from the same page once it is resolved** -
  asserted as an absence on a page that had certainly loaded its banner, because
  a string that is missing from a page that never rendered proves nothing. The
  resolved row is still in the table, carrying the `resolved_at` that closes its
  window
- an `ack-required` maintenance announcement **holds** its resource: the quiesce
  names the holder, `resolve` answers `409`, the announcement stays `active`, and
  a bare release does not clear it. The ack does, and then the resolve goes
  through - in that order and no other
- a `drain` announcement releases on a release, which is the answer that mode
  asked for. Same machinery, different mode
- across the two nodes Phase 5 already stood up, with nothing new started: a
  system agent on nodeA posts one federation announcement and one node
  announcement, in the same project with the same visibility, so that scope is
  the only thing that differs between them. After a sync the federation one is on
  nodeB under nodeA's name, nodeA's signature and the scope it was written with;
  the node one is a `404` there and is not a row nodeB is merely hiding. **Both
  doors are checked**: nodeA's pull does not offer it in a delta that carries the
  federation one, and a node announcement pushed at nodeB - correctly signed by
  nodeA, whose key nodeB has pinned, by the principal nodeB takes deltas from -
  is refused as node-scope rather than applied
- a forged federation announcement, wearing nodeA's name and signed by a node
  nobody has heard of, is refused on merge, writes no row, and is in nobody's
  banner

- `flowy tui` reaches the node only through the HTTP API - `go list -deps` on
  the package links neither `lib/pq` nor the store - and with no token in the
  flag, the environment or `~/.config/flowy/token` it refuses to start
- the terminal client, driven headless by teatest against the live node with a
  seeded token: the room renders, a message typed into the box is posted and
  comes back **through the watcher** rather than being echoed locally (the wait
  ignores any occurrence sitting behind the box's own prompt, so a client that
  posted nothing could not pass it), the inbox holds the seeded task, memory
  search re-renders under the query the node answered with, the timeline and the
  metrics load, it is resized to 80x24, 40x10 and back, and `q` finishes it -
  after which its own model is asked whether the message, the task, the search
  hits, the metrics and the timeline are really in it
- a token the node refuses puts `token refused` on the status bar and leaves the
  client running: not a panic, and not an empty pane
- and the built binary on a real pseudo-terminal: it draws, it survives two
  window-size changes from underneath, `q` exits zero, the alternate screen is
  left, and the pty's `ECHO` and `ICANON` are on again afterwards - raw mode
  was not leaked

The last thing the gate does is ask git whether the tree it just tested is the
tree on disk: uncommitted changes, or nothing ever committed, is a failure.
`web/node_modules` and everything vite writes into `web/dist` are ignored, so
what the gate builds does not count as a change to the tree.

## The security fixes

A review found ten defects, and this slice fixes all ten. Each one has a check
in `run-tests.sh` that fails on the code as it was and passes on the code as it
is - the run below verifies that by reverting the source and leaving the checks
in place, which is the only way to know a regression test is testing anything.
A re-review of this slice found ten more, mostly in the push gate it added, a
third round found eight behind those, a fourth found eight more, and a fifth
found ten - the four sections after this one.

Two of them change how a node is configured, so they are worth reading before
upgrading one:

| variable | what it does |
| --- | --- |
| `FLOWY_PEERS` | comma-separated user ids whose token may `POST /api/sync/push` here. Empty means only the operator may push. |
| `FLOWY_FORGE_REPOS` | comma-separated repositories this node files into and comments on. Empty means it files nowhere. |

Both default to closed. A node that replicates has to name the principal
replication runs as, and a node that files has to name the repositories it
files into; until it does, the two endpoints refuse rather than accept
everything, which is the way round that fails safely.

**CRITICAL - `POST /api/sync/push` took a delta from any token.** A peer's
delta is merged last-writer-wins, so a row with a high enough reading wins and
stays won, and anybody holding any token - a share subject with one grant on one
artifact - could hand the node whatever rows they liked. Three fixes: the
endpoint is gated on `isPeer` (operator, or `FLOWY_PEERS`); every row is checked
against the pusher in `store.SyncApplyAs` before it is merged - a project-wide
grant has to open the pusher's own project, a share has to belong to the
artifact's owner, a personal artifact has to be the pusher's, and a row that is
already here is only overwritten by somebody who can read the row that is here;
and a delta carrying a reading no clock could have made is refused whole, before
a single reading reaches `Clock.Update`. That last one was the worst of the
three: `hlc=MaxInt64` packed into a negative int64 and left the node unable to
write anything that ordered after what it already held, permanently, on every
node the reading reached. `hlc.Pack` now clamps and `Clock.bump` saturates, so
the arithmetic cannot go negative even if a reading gets past the check.

The pull side is deliberately not filtered: it is this node's own operator
fetching from a peer they named and hold a token for, which is a different
relationship from an unsolicited push. The clamp and the reading check are on
both paths.

**CRITICAL - `POST /api/artifacts` took over rows it could not read.** An id is
a guess anybody can make, and the store's `ON CONFLICT (id) DO UPDATE` replaced
every column of whatever it landed on, `owner_user` and `visibility` included.
The handler's ownership test only ran when it could read the row, which is
exactly when the attack does not need it. The guard is now in the store - `WHERE
artifacts.owner_user = excluded.owner_user` - so every caller has it, and a
write that matches nothing is `ErrNotFound`, which the handler answers as `404`:
the same answer a read gives, so a write cannot be used to find out that an id
exists.

**HIGH - `POST /api/events` let the caller pick the actor.** The log is what the
lifecycle, the inbox and the forge bridge all read back, and it was signable
with anybody's name. The actor is now the token's, always; the field is still
accepted and ignored so an older client is not broken by a `400`. `status`,
`task` and `forge` events are refused outright - they are claims this node makes
about things it did. `chat` is still allowed, because it carries no authority
beyond what `POST /api/chat/{room}/say` already gives the same principal.

**HIGH - the forge bridge published on a read.** Filing sends an artifact's body
out of the machine over a credential the caller does not hold, and reading the
artifact was enough to do it, into any repository the request named. Filing and
pushing replies now require the owner (or the operator), and the repository has
to be on the operator's list. Everything that does not leave the building -
reading, commenting in the thread, moving the status - is unchanged.

**MEDIUM - the forge push cursor moved over comments that never went out.** It
was raised to the highest event the loop had looked at whether or not the forge
accepted it, and `handleForgeSync` answered `502` before writing anything, so a
refusal halfway through both lost the replies that had not been sent and sent
the ones that had a second time. The cursor now advances one event at a time,
only behind a comment the forge accepted, and is written before the refusal is
reported.

**MEDIUM - a share newer than the cursor never carried its artifact.** A cursor
is a clock reading and an artifact's reading does not move when it is shared, so
a peer that had already paged past it held the grant and nothing to use it on.
`SyncPull` now looks for the artifacts a fresh grant has just opened up and adds
them below the high water mark, which does not move the cursor - so once the
grant is under the cursor the extra scan stops on its own.

**MEDIUM - `mem_write` rewrote artifacts that were not memory.** The update path
never checked what it had read, so an owned bug could be turned into a note and
leave the lifecycle it was in. It now answers `no such memory item`, which is
what `mem_read` says about the same row.

**LOW - a reply inherited its thread from an unfiltered parent.** `handleChatSay`
read the parent with `GetEvent`, which asks nobody's permission, so naming an id
was enough to join a conversation you cannot read - and to put what you said
next in front of the people who can. The parent is read through the permission
filter now, and an unreadable one is ignored: the message starts its own thread.

**LOW - `SetAutoDelegate` swallowed a driver error.** `RowsAffected` returning
an error meant "the driver would not say whether that update found the row", and
it was read as "it did". It is reported now, through `affectedRows`.

**LOW - the forge's seen-comment list forgot what it needed.** It was capped at
a hundred ids by count, and a forge whose timestamps have one-second resolution
hands back several comments at the cursor's exact time - the hundred-and-first
pushed the first one out, and the cursor could not rule it out either, because a
comment made at exactly the cursor is not before it. So it was threaded in
again. Entries now carry their comment's time, and the trim only ever drops one
the cursor already covers. Refs written by older nodes still parse: a bare id is
an entry whose age is unknown, and is the first to be forgotten.

## The second round of security fixes

The first round was reviewed again, and the review found that the push gate had
been put in the right place with the wrong question in it. `SyncApplyAs` checked
three tables and not the fourth, and it checked those three against what the
pusher may **read** where the API checks what the pusher **owns**. Ten more
defects, four of them in that one paragraph. Same rule as before: one check in
`run-tests.sh` per defect, each verified by reverting the source and leaving the
checks in place.

Nothing here changes how a node is configured. `FLOWY_PEERS` and
`FLOWY_FORGE_REPOS` mean what they meant.

**HIGH - a pushed event was not checked at all.** `syncApply` called
`applyEvent` straight from the loop, so the one append-only table - the one the
lifecycle, the inbox and the forge bridge all read back - was the one table a
peer could write anything into. A pushed `status` event was a lifecycle move
nobody made, on every node it reached, and a pushed `chat` was a message from
somebody who never said it. `checkEvent` now asks what `POST /api/events` asks:
the actor is the pusher, the project is the pusher's own (or the event has none
and is theirs), and the minted types - `status`, `task`, `forge` - are refused
outright. The two lists of minted types live in two packages, so a test in the
server package holds them together.

**HIGH - a read-share was a write.** `checkArtifact` let a row that is already
here through if the pusher could **read** it, and `applyArtifact` then replaced
every column of it - `owner_user`, `project` and `visibility` included. Being
shown an artifact was therefore enough to take it over and share it onwards.
The rule is now the one `handleCreateArtifact` keeps: a row that is already here
is rewritten only by its owner. Readability is still the rule for a row that is
not here yet, which is ordinary replication.

**HIGH - a pushed share was checked against its own claim.** `checkGrant`
verified that the artifact's owner is whoever the grant says granted it, and
never that the pusher is that owner - so writing the owner's id into
`granted_by` was enough to share anybody's artifact with yourself. It now
requires both.

**HIGH - a pushed task was a read capability nobody checked.** The tasks clause
in `EventFilterSQL` lets the parties to a task read the thread it names,
whichever project they write from. A *new* task was accepted from any principal,
so `{thread: T, from_user: me, to_user: me}` was a way to read conversation `T`.
A new task is now validated the way `POST /api/assign` is: the pusher is the
side handing the work over, the artifact exists, is readable by them and is not
personal, and the thread is one they can already read - a thread nobody has said
anything in yet has nothing to leak.

**HIGH - `GET /api/forge/status` skipped the repository list.** Filing and
syncing check it; the refresh did not, and the repository it uses comes off the
artifact's `external` link - which is a replicated column. So a peer that pushed
an artifact carrying a link chose which repository this node's credential
talked to. It checks the list now, like the other two.

**MEDIUM - a grant that opened a project carried one page of it.** The rescan
that catches artifacts a fresh grant made readable below the cursor is a page
like any other, and what did not fit in it was dropped: those rows are below the
cursor and the grant is about to be, so nothing pages towards them ever again.
One project-wide grant is more than one page by definition. The overflow is
written to `sync_pending` now, keyed by the reader rather than by the peer
machine - a pull knows which principal is asking and not which host - and later
pulls drain it. A row is struck off when the reader comes back with a cursor at
or above the high water mark it went out under, which is the only acknowledgement
the protocol has; the mark moves by one on a page that carried nothing else, so
the drain always makes progress.

**MEDIUM - the forge pull threaded comments twice on a failure.** The push half
was fixed last round to write its cursor before reporting a refusal. The pull
half still answered `502` first, and by then it had threaded the comments before
the failure into the log - so the next sync threaded them in again. It now
returns what it threaded along with the error, the ref is written either way,
and the refusal is reported after. Pushing is not attempted after a pull that
broke.

**MEDIUM - the node assumed the forge's name for it.** `Author` on a new link
was `forge.SelfAuthor`, which is the *mock's* login. Against a real `gh` the
node posts as whoever the machine is logged in as, so its own replies came back
as a stranger's comments, were threaded in, and were pushed out again - the echo
the field exists to stop. It is resolved once, when the artifact is filed, by
`gh api user --jq .login` / `glab api user`, through a `SelfLoginer` a client
implements when it can answer. A forge that cannot say leaves it at `flowy`,
which is what the mock is called.

**MEDIUM - a CLI failure quoted the argv.** `runCommand` folded the whole
command line into its error, and the argv of a filing carries the artifact's
whole body - so an issue that could not be opened published the thing it could
not publish, to whoever made the request and to the node's log. The error names
the call now: program, subcommand, issue number and repository, and nothing a
flag introduced. `gh`'s own stderr is still folded in, because that is the
diagnosis.

**MEDIUM - the comment cursor was read after the filing.** `Since` was stamped
when `FileIssue` returned, so anything a reviewer said between the request going
out and the answer coming back was already behind the cursor when it was
written, and `ListComments` never offered it again. It is stamped before the
round trip. The mock can now be told to record a comment as part of opening an
issue, which is that window made deterministic rather than raced for.

## The third round of security fixes

The second round was reviewed again, and the first thing it found was the shape
of the first two: both had put their checks on the **push** endpoint, and the
driver's **pull** half went straight round them. Eight more defects. Same rule
as before: one check in `run-tests.sh` per defect, each verified by reverting
the source and leaving the checks in place.

Nothing here changes how a node is configured. `FLOWY_PEERS` and
`FLOWY_FORGE_REPOS` mean what they meant.

One thing did change in the model, and it is worth stating before the list.
**A push and a pull are not checked by the same rule, and they cannot be.** A
push is a principal's claim about its own work, so every row has to be one that
principal could have written over the API. A pull is a peer this node's operator
named answering with the rows that principal may read *there*, and most of those
are other people's - a project mate's artifact, the log of a thread, the share
that opened it. Refusing them is refusing federation. The proof that one rule
cannot serve both is in the gate: the share a handoff writes on one node and the
share a hostile peer invents are the same row, carried by the same principal,
and nothing in an unsigned row tells them apart. What decides it is who asked.
So the pull check is a different question - does this row land inside the world
this principal already has here, and does it leave what is already here alone -
and the residual is that a peer you pull from can still hand you a capability
signed with somebody else's name. Closing that needs signed rows, which is a
bigger change than a check. *(Phase 6.5 made it: rows are signed now, and the
merge verifies authorship before it asks any of these questions. The two rules
above are unchanged - authorisation is still two questions, and it is now the
second layer of two rather than the only one.)*

**HIGH - the pull side applied a peer's rows with no check at all.**
`pullFromPeer` called `SyncApply`, which takes no principal, and every check in
`syncApply` short-circuits on a nil one. So a peer this node was willing to read
from was a peer that could write anything into it: a forged grant lands, the
project it names is readable by whoever the peer says, and the next pull carries
that project out of the door. The driver already resolves the replication
principal for the push half - what it may hand over is what that principal may
read here - and it now passes it to the pull half too, through `SyncApplyFrom`.
The gate plays the hostile peer by writing four rows straight into node B's
database, none of which B's own API would have taken, and pulling them: all four
are refused and none of them land.

**HIGH - a replicated artifact did not have to land anywhere in particular.**
`checkArtifact` applied the "land where the API would put it" rule to rows that
were already here and to nothing else, so a *new* row could name any project at
all and any owner at all - including a project the peer has never been let into,
which is then real for every node downstream. And an owned row could be
re-projected, which walks an artifact out of the project that was reading it and
into one that was not. A row now has to be readable by the principal carrying it
as it would land - its own project, a project a live grant opens to it, or an
artifact shared to it - and a row that is already here does not change hands and
does not change project, either way round.

**HIGH - a party could re-point its own task.** `checkTask` returned early for a
pusher who is already a party to the task, and `applyTask` replaces every
column - thread included. A task row is a read capability: the tasks clause in
`EventFilterSQL` shows a thread's events to the parties. So the person a handoff
was made *to* could push their own task back with `thread` swapped for any
conversation on the node and read it from then on. A party may still move the
two things `POST /api/task/{id}/state` and `/delegate` move - the state and the
agent - and the thread, the artifact and the two people are now refused: they
are the shape of the handoff and not a party's to change.

**MEDIUM - writing into a thread needed no read on it.** `handleChatSay`,
`POST /api/events` and `checkEvent` all took a caller-named thread as given. A
thread id is a guess anybody can make and the tasks clause shows a thread to the
parties, so a message dropped into somebody else's conversation was read by
exactly the people whose conversation it is not, over a thread the speaker
cannot see. All three ask `ThreadHidden` now - a thread holding an event the
caller may not read is closed to them, `403` - and a thread with nothing in it
is still nobody's, which is what every conversation starts as. Leaving `thread`
out still opens one of your own.

**MEDIUM - last-writer-wins had no last writer when the readings tied.** The
upsert compared `coalesce(hlc, 0) < excluded.hlc`, and `hlc.Pack` folds a wall
reading and a logical counter into an int64 with nothing about the node in it.
Two nodes writing in the same millisecond with the same counter therefore
produce two equal readings and two different rows, and each node refuses the
other's: they differ from then on, permanently and silently. The comparison is
total now - `hlc <` or `hlc =` and `node <` - in `applyArtifact`, `applyTask`
and `applyGrant` alike. Any order both sides agree on would do; the node name is
the one thing already on every row.

**MEDIUM - `GET /api/forge/status` was gated on readability.** Filing and
syncing are the owner's because they spend this node's forge credential outside
the building. A refresh does the same, and it writes: a terminal issue moves the
artifact to `done` and signs a status event. It was open to anybody who could
read the artifact, so a read-share was enough to make the node act. It calls
`forgeOwner` now, like the other two. Everyone who can read the artifact can
still read it, and still sees the state the last refresh found.

**MEDIUM/LOW - the push cursor moved past rows the peer refused.**
`pushToPeer` advanced `pushed_cursor` to the page's high water mark whatever
came back, so a refused row was never offered again: the two nodes differ and
nothing says so. The bookmark now stays where it is when the peer refused
anything, and the run stops pushing - the next one hands the same page over,
which clears a refusal that was only about the order things arrived in and
leaves a real one in the report where somebody can read it. What makes that
terminate rather than wedge is the other half of the change: a row that *loses*
its merge is ignored rather than refused, because replaying a delta is not a
write. A peer's own rows coming back at it are not a refusal, so they do not
hold the cursor.

**LOW - the delete trusted a read.** `TombstoneArtifact` read the artifact,
checked the owner and then updated by id alone - two statements with a gap
between them, and a merge landing in that gap changes the owner. The delete
would then go ahead on the strength of a read of somebody else's row. The
`UPDATE` names the caller as well as the id now, so the row it finds is the row
it was allowed to find, and no rows affected is `ErrNotFound` - which also makes
the rule the store's rather than a promise the handler happens to keep.

### The merge, after all three rounds

`syncApply` is one loop over the rows of a delta, in the order grants,
artifacts, tasks, events - a grant is what makes the rows that follow readable,
and a task is what opens a thread. It goes round again over whatever it refused
and stops when a pass changes nothing, because a delta is a set rather than a
sequence: one page can carry an artifact that needs the share that opens it and
a share that needs the artifact it shares, and a single pass in a fixed order
would refuse one of them for being in the wrong place. Each row is asked three
things in turn: would applying it change anything at all (if not, it is neither
applied nor refused), may this principal write it (which is the question above,
and the answer differs by direction), and then it is applied.

## The fourth round of security fixes

The third round was reviewed again. The big items held - the permission SQL, the
clock's packing and clamping, the node-name tiebreak in the merge - and eight
smaller things were behind them. Same rule as before: one check in
`run-tests.sh` per defect, each verified by reverting the source and leaving the
checks in place, and each of the eight fails on the source it fixes.

Nothing here changes how a node is configured.

**HIGH - a cursor is one integer and every page is ordered by two columns.**
Artifacts, events, tasks and grants are all read `ORDER BY hlc, id` and paged by
`hlc > since`. Two rows can carry the same reading - two nodes writing in the
same millisecond, or one handoff stamping its three rows together - and a
`LIMIT` that falls between them hands over the first and reports its reading as
the high water mark. The next pull asks for what is strictly greater, and the
second row is never offered again. Not delayed: dropped, permanently, with
nothing anywhere saying so. `ListEvents` had the same hole, so the same thing
happened to a chat message a client paged past. A page that fills now goes on to
read the rest of the reading it stopped in, so the cursor it reports names a
reading every row of which has been handed over - which is what an integer
cursor has to mean for paging by it to be safe. A tie is bounded by how many
nodes wrote in one instant, so finishing one costs a handful of rows.

**HIGH - a create read "not readable" as "not there".** `handleCreateArtifact`
read the id through the permission filter and treated `ErrNotFound` as a free
id, which sent it down the update branch of the upsert - and the upsert's own
guard is ownership, which the caller had. So the owner of a personal artifact in
one project, holding a token for another, could POST its id and watch the row
move project, lose every field the request left out, and come back from the
dead. The read and the write now agree about what they are doing: a caller that
read the row updates it, and a caller that did not creates - and a create that
lands on a taken id writes nothing and comes back `ErrTaken`, which the handler
answers as the `404` a read would have given. The id is the only thing anyone
can be told about a row they cannot read, and even that is told as a 404.

**HIGH - `checkTask` waved through the two columns a party may move.** The merge
refuses to let a party re-point its task's thread, artifact or people, and then
took `state` and `assignee_agent` as given. `assignee_agent` is the third read
capability on the row - the tasks clause in `EventFilterSQL` shows the thread to
the agent named there - so a party could hand its own handoff's conversation to
anybody's agent by pushing its own task back. Over the API only the assignee
delegates, and only to an agent that acts for them, so that is the rule here
now: an incoming agent that differs from the one on the row has to be an agent
of the assignee. The state is checked against the three the lifecycle has, so a
task cannot be parked somewhere nothing moves out of.

**MEDIUM - an inherited thread was not a checked thread.** A message with no
thread of its own inherits its parent's, and the parent was read through the
filter while the thread it names was not. A readable message inside an
unreadable conversation is exactly what the tasks clause produces, so answering
one put the speaker inside a conversation they cannot see and put what they said
next in front of the people whose it is. The inherited thread goes through
`ThreadHidden` now, and a closed one is not a `403`: the caller never named a
thread, and refusing would say that the parent's is one worth guessing. It
starts a fresh thread, which is what leaving `thread` out has always done.

**HIGH - a delete only removed the artifact from the lists.** `ReadArtifact` did
not look at the tombstone, and every read of one artifact goes through it, so a
deleted artifact still answered a `GET` by id, still had a status to move, still
had a history, and could still be filed as an issue on a forge. Worse, an edit
of one was a resurrection: `UpsertArtifact` stamps a fresh reading, and a fresh
reading beats the delete on every peer. Both are shut - the read filters the
tombstone and the upsert will not update one - so coming back is something to do
on purpose rather than a side effect of a write that never mentioned it. The row
itself is untouched, because that is how the delete replicates.

**MEDIUM - the memory scopes promised three levels and the store had two.**
`visibility='project'` and `visibility='shared'` were the same row test: both
reach the project, both are reached by the project's grants and shares. The
`project` scope is documented, in the tool description and in the instructions
an agent reads, as narrower than `shared` - so an agent choosing the narrower of
the two got the wider one, on the one write here that cannot be taken back. The
store has a value that means what the scope says now, `project-only`, and the
filter and `CanRead` both stop at it; `mem_write` at `scope=project` writes it.
`visibility='project'` keeps the reach it has always had, because it is what
every artifact written over the API gets by default and what every cross-project
grant in the fabric has always reached.

**MEDIUM - `/healthz?counts=1` answered the node's row counts to anybody.** How
many users, tokens, grants, artifacts, events and tasks this node holds is its
shape and its size, and it needed no credential at all. The health check itself
stays open - one that needs a credential stops working at the worst moment, and
`ok`, `db` and a version tell a load balancer what it needs and a stranger
nothing the port answering had not already told them. The counts are the
operator's view of the operator's machine, like `?scope=all` and like
`/api/peers`, so they are answered to the operator's token and to nobody else.
Everyone else gets the health check, without the parameter having any effect.

**MEDIUM - four multi-row writes were not one write.** An assignment is three
rows - the share, the task, and the message that opens the thread - and a status
move is two, and a filing is two. Written one statement at a time, a failure in
the middle left the half behind: a share nothing points at, a task about an
artifact the assignee gets a `404` on, a status with no entry in the trail, a
log entry saying a bug was filed on an artifact that says it was not, which
files it again the next time somebody asks. Nothing in the node ever came back
to finish any of them, and the half replicated on its own, because every row
carries its own reading and a peer merges what is there. Each sequence is one
`BeginTx` in the store now - `WriteAssignment`, `MoveArtifactStatus`,
`LinkArtifactExternal` - taking one clock reading before it starts, which is the
same-hlc ordering those rows always had.

What is still not transactional is the forge itself. Opening an issue is a call
to another machine and no rollback closes it, so the order is file, then record
the event and the link together, and a failure recording them answers with the
issue number and URL it could not write down - the person reading that error is
the only one who can go and look.

## The fifth round of security fixes

The fourth round was reviewed again, and ten more came out of it. The theme is
that a rule kept in one place was not kept in the other: the pull half of the
merge took two things the push half has always refused, the pull half of the
driver stepped past a row it had just refused while the push half holds, and a
set of routes that all needed the same gate had it on some of them. Same rule as
before: one check in `run-tests.sh` per defect, each of them verified to fail on
the source it fixes.

Nothing here changes how a node is configured.

**HIGH - a pulled grant could open the puller's own project.** `checkGrant` in
`modePull` asked whether a grant reaches this principal at all, and a
project-wide grant naming this principal's project as `to_project` reaches it by
definition - so a peer serving a page could write one with a `from_project` of
its own choosing and any `granted_by` it liked, and from the next pull onwards
that project reads this one. Merging is last-writer-wins, so a high enough
reading makes it permanent. Federation never needs that: a grant that opens this
project up is written here, by somebody who is here, and travels outwards. So
one is taken on the pull side only when `granted_by` resolves to a local user
who holds a principal in the project being opened. Grants between other
projects, riding a page this principal may read, are untouched - refusing those
would be refusing federation.

**HIGH - the mock forge's control surface answered any token.** The routes that
play the other side of the conversation - the reviewer who closes an issue, the
reviewer who comments, who the forge says it is logged in as, and when the next
call fails - existed for anybody who authenticated at all. A reader of one
artifact could close somebody else's issue, put words in a reviewer's mouth, and
rename the login the node posts under, which is the whole of the evidence the
bridge works from. All six are behind the operator now, and the gate is a
wrapper applied where they are registered rather than a line inside each
handler: a set of routes that all need the same check is a set where one of them
eventually does not have it, which is what happened here.

**HIGH - a refused pull moved the cursor past the row it refused.** The push
side stopped doing this a round ago; the pull side went on advancing its
bookmark to the page's high water mark whatever the merge said about the rows in
it. A cursor is a promise that everything below it has been offered and dealt
with, and a row this node would not take was not: moving past it is how that row
is never offered again and the two nodes quietly differ, with the reason buried
in one run's report. The pull loop now stops before persisting the cursor when a
page carried a refusal. A row that merely loses its merge is not a refusal, so
our own rows coming back at us do not wedge it.

**MEDIUM/HIGH - a memory update moved the item into the token's project.**
`mem_write`'s update path rewrote `project` to the principal's home every time.
An owner holding tokens in two projects moved their own item out of one and into
the other by editing its title - silently, and past the rule that
`POST /api/artifacts` and `checkArtifact` both keep, which is that a principal
writes in its own project or not at all. An update keeps the home the item
already has now, and one that would land outside the token's project is refused
rather than performed. A create still writes where the token is, and a personal
item being given a scope for the first time lands there too.

**MEDIUM - a minted type was refused at one door and not the other.**
`checkEvent` refuses a pushed `status`, `task` or `forge` event because it is a
claim the pusher is not entitled to make. A pulled one was taken without
question - which is a peer writing this node's own history for it: a lifecycle
move nobody made, a handoff nobody handed over, and every peer downstream then
holds it too. The trail is only worth reading if the only way into it is to have
done the thing, so the pull side refuses them as well. Chat is not minted and
still replicates in both directions, because it carries no authority the peer did
not already have.

**MEDIUM - a task moved in two writes.** `handleDelegateTask` and
`handleTaskState` each wrote the row and then appended the entry that accounts
for the move, with nothing holding the two together. A failure between them left
a task in a state its own thread does not explain - and because each row carries
its own reading and replicates on its own, the half that landed reached every
peer while the half that did not never existed anywhere. `UpdateTaskEvent` is
the same shape as `MoveArtifactStatus`: one reading taken before the first
write, stamped on the row and on the entry, both inside one `BeginTx`.

**LOW/MEDIUM - `/healthz` spent a clock reading to report one.** The response
carried `Clock().Pack()`, which goes through `Now()`, which advances the
counter. So looking at the clock was a use of it, on an open endpoint that needs
no credential: a stranger could walk the logical counter up one probe at a time,
spending readings nothing was ever written under. `Clock().Reading()` returns
where the clock stands without moving it, and that is what the health check
reports. `Now()` is still the only way to get a reading to stamp a row with,
because two writes must never share one.

**LOW - a share believed the projects its body named.** The share branch of
`POST /api/grants` defaulted `to_project` only when the body left it empty and
never looked at `from_project` at all, so a share was recorded along whatever
edge the caller claimed. Both ends of a share follow from the artifact and from
the owner handing it over, and neither is the caller's to say: `to_project` is
forced to the artifact's project and `from_project` to the caller's, whatever
the body holds.

**LOW - the merge moved the clock before it knew the page had landed.**
`syncApply` observed each row's reading as it applied it, inside the transaction
and before the commit that decides whether any of those rows exist. A page that
failed halfway rolled its writes back and left the clock standing past readings
this node does not hold, and nothing puts that back: every write afterwards is
stamped above rows that are not here, so the peer that does have them loses
every merge against a node that never applied them. The highest applied reading
is observed once, after the commit succeeds.

**LOW - `meta` was a second way to sign an event.** The `actor` column has been
the token's since the forgery in it was fixed, but `meta` rode in verbatim - and
every reader that cares who is speaking reads `meta`, because that is where
`actor_kind` and `actor_user` live. `{"actor_kind":"agent","actor_user":"…"}` on
a hand-appended event is the same forgery through the second door: the row is
correctly signed and reads, everywhere it is rendered, as somebody it is not.
`POST /api/events` strips every `actor_*` key out of client-supplied meta now.
The rest of meta is still carried, because it is where a client puts what an
event is about; it is simply not a channel for saying who is talking.

## The sixth round of security fixes

The fifth round was reviewed again. The core held - the composite `(hlc, id)`
cursor, a minted type refused at both doors, the forge owner gates, the cursor
held on a refusal, `meta` stripped of speaker keys, the clock observed only
after the commit, `mem_write`'s scope rules - and five more came out from behind
it. The theme this time is a rule that fires against the wrong row: a check on
what the caller can **read** standing in for a check on what they **own**, a
check written against the **stored** row that a **new** row walks past, and a
statement that matched **nothing** reporting that it wrote. Same rule as before:
one check in `run-tests.sh` per defect, each verified to fail on the source it
fixes.

Nothing here changes how a node is configured.

**HIGH - any reader of an artifact could share it with anybody.**
`handleAssign` gated on `ReadArtifact` and then wrote the share itself with the
caller in `granted_by`. The share clause in `ArtifactFilterSQL` matches on
artifact and subject alone, so a user in another project - reaching in through a
cross-project grant, or through a share of that one artifact - could hand a read
on somebody else's artifact to any user on the node. It was a re-delegation hole
out of a capability that was only ever a read, and everywhere else a share is
the owner's: `POST /api/grants` refuses a non-owner, and `checkGrant` refuses a
pushed share whose `granted_by` is not the artifact's owner. `handleAssign`
keeps the same bar now - `403`, naming the artifact. Passing on work that was
shared with you is a real feature and it is not this one; see the delegation
note above for what it needs.

**HIGH - a pushed artifact could be created in somebody else's name.**
`checkArtifact` runs the reach test, then reads the row already here to decide
the third rule - a merge does not change hands, does not move project, and on a
push has to be the pusher's own. On `sql.ErrNoRows` it returned early, so for a
row that is **not here yet** the push rule never fired at all: a peer in
`FLOWY_PEERS` could push a brand new artifact into any project it can reach with
`owner_user` set to anybody, and the forgery then replicated onward from here
while the name it invented held the real update and tombstone rights that column
carries. The new-row case is decided rather than skipped now: on a push the row
has to be the pusher's own, which is what the doc comment always claimed, what
`POST /api/artifacts` does, and what `checkEvent`, `checkTask` and `checkGrant`
have each required of their own table all along. Artifacts were the one table
where it was not true. So a push carries the pusher's rows and nothing else, and
somebody else's cross by being **pulled** - which is the half of the exchange
that is allowed to carry them, because a pull is filtered to what the principal
may already read there and lands the row in the same world it came from.

**MEDIUM - a `project-only` artifact could be assigned, and the handoff could
not be opened.** The read filter's `project-only` branch is a floor below the
grant and share tests: it reaches the project and stops. `handleAssign` refused
only the personal floor, so a `project-only` artifact passed and the handler
minted a share that can never take effect - leaving the assignee with a task
whose artifact is a `404`, which is the exact riddle the three-writes rule
exists to prevent. `checkTask` had the same gap on the replication side. Both
refuse it now: `400` from the endpoint, and a pushed or pulled task about one is
refused with the rest of the page applied around it.

**HIGH - the reader-made assignment split its three rows across two nodes.**
The consequence of the first defect, and worth its own check. `WriteAssignment`
lands the share, the task and the opening message in one transaction locally,
but a non-owner's share is exactly what `checkGrant` refuses on a push - so the
task and the message crossed and the share did not, the far side held a task it
could not open, and the refused grant held the push cursor where it was, so
nothing behind it moved either. Making assignment owner-only removes the
divergence at the source: there is no code change here beyond that fix, and the
check asserts the whole of it - a reader's assignment is refused outright, and
an owner's pushes grant, task and event with zero refusals and a cursor that
advances.

**LOW - a task move that matched no row reported success.** `updateTask` threw
its `sql.Result` away, so an `UPDATE` whose `WHERE` matched nothing - an id that
is not here, or one a peer's tombstone has already taken away - returned `nil`.
`UpdateTaskEvent` then appended the entry that accounts for the move and
committed it: a trail entry for a move that did not happen, in a thread whose
task does not exist, replicating outwards from here. That is the half-write
`UpdateTaskEvent` exists to rule out, arrived at from the other side. It checks
`RowsAffected` now and returns `ErrNotFound`, which rolls the transaction back
with the entry in it.

## The seventh round of security fixes

The sixth round was reviewed again, deeply. The core came back clean - no SQL
injection anywhere, `CanRead`, the SQL filters and the sync mirror agreeing row
for row, every route authenticated, the HLC and the ULIDs correct, every page
tie-safe on `(hlc, id)`, every multi-row write in one transaction with
`RowsAffected` checked - and six more came out from behind it. The theme is a
rule that is right about one surface and silent about the next: who may **write**
standing in for who may **publish**, an update that says **nothing** about scope
being read as a request to change it, and one artifact's share reaching the row
but not the log. Same rule as before: one check in `run-tests.sh` per defect,
each verified to fail on the source it fixes.

Nothing here changes how a node is configured.

**HIGH - a project mate's message was published to the public issue.**
`forgePushReplies` forwarded every `chat` event in a filed artifact's thread to
the forge, gated only on the *caller* of `POST /api/forge/sync` being the owner.
But who may write in that thread is a far wider set - `mayWriteThread` plus the
project and task clauses of `EventFilterSQL` admit any project mate and any
party to the task - so somebody else could `POST /api/chat/say` into the forge
thread with any body they liked, and the owner's next sync posted it to the
issue over the node's `gh` credential, under the node's name, where it cannot be
taken back. That is the exact thing `forgeOwner` says it exists to stop:
*reading an artifact is not permission to publish it... none of that leaves the
building*. The push side keeps the same predicate now - the artifact's owner, an
agent acting for them, the operator, or an agent acting for the operator - and
what anybody else says stays here, with the cursor stepping over it exactly as
it steps over a status move. Held back is not queued: a second sync does not
send it either.

**MEDIUM - updating a projectless artifact adopted the caller's project.** A row
with no project is its owner's and nobody else's - the read filter's first
branch, and a floor no grant reaches through. `fillFrom` carried the old project
forward only when it was **not** nil, and an absent project field means "the
principal's home project", so a bare `{"id": ..., "type": "note"}` from a token
that has one moved the row into that project: what was owner-only became
project-readable, on a request that said nothing about scope at all. Three
changes, one rule. `fillFrom` carries `null` forward like any other value; an
update that would give a projectless row a project while it keeps a non-personal
visibility is refused outright, the same shape `handleAssign` refuses; and the
store's floor now fires for **any** row with no project rather than only for the
two project-scoped visibilities, so `shared` over a `NULL` project is written as
`personal` instead of describing a reach it does not have.

**LOW - the artifact and the event surfaces disagreed about a share.**
`ArtifactFilterSQL` has a per-artifact share clause and `EventFilterSQL` did
not, so a cross-project share let the subject read the artifact and its status
trail - `GET /api/artifact/{id}/history` is gated on the artifact read - and not
one event about it through `GET /api/events` or the chat. Two reads of the same
rows, two answers. The event filter carries the clause now, joined to the
artifact so a share reaches only what a share can reach: an artifact behind the
personal or the `project-only` floor is no more readable event by event than it
is row by row. The tasks clause stays as the wider rule it always was.

**LOW - reading a thread was a query per message.** `ThreadEvents` selected the
thread's ids and then `GetEvent`'d each one. The console's thread pane and the
forge's reviewer loop both walk whole threads, so a conversation cost its own
length in round trips every time either ran, and the rows could move underneath
it in between. It is one statement now, ordered by `(seq_hlc, id)` and scanned
like `ListEvents`. The check counts the statements that reach the wire through a
counting `database/sql` driver, because the events come back the same either way
and the count is the only thing that tells the two apart.

**LOW - an oversized pull answer was a parse error, forever.** The driver read a
peer's answer through `io.LimitReader(resp.Body, maxSyncBody)`, so a page over
64 MB was cut mid-JSON and surfaced as a syntax error with no cause in it - and
because the cursor only moves on a page that decoded, the next run asked for the
same page and was cut in the same place, for good. It reads one byte past the
limit now and says *the answer exceeds 64 MB*, naming the peer, which is the
answer the push side has always given through `decodeJSONLimit`. An operator can
act on that; they cannot act on `invalid character 'x'`.

**LOW - a non-JSON error body crashed the console's error path.**
`web/src/lib/api.ts` parsed every body as JSON before it looked at the status, so
a proxy's HTML page or a plain-text 502 - the state a real deployment is in
whenever the node is down - threw `Unexpected token '<'`, a `SyntaxError` with no
status on it, past every caller that handles `ApiError`. The parse has its own
`try` now and falls back to `new ApiError(response.status, ...)` with the first
of the body on it, or the status line when there is nothing to quote. The node's
own JSON errors are untouched, which the check asserts beside the fix.

## Phase 6.5: the one the seventh round left

The seventh review closed six defects and named one it could not: a hostile
**pull** peer could rewrite the content of any artifact, grant, task or event
the pulling principal may read. `checkArtifact`, `checkGrant`, `checkTask` and
`checkEventRow` all verify *authorisation* - reach, ownership, self-consistency
- and none of them could verify *authenticity*, because an unsigned row carries
nothing that says who wrote it. Last-writer-wins then makes the rewrite
permanent. The comment on `syncMode` said as much: *closing the gap for good
needs signed rows, which is a bigger change than a check.*

This is that change. What it is and how keys move is above, under **Row
signing**; what follows is the checks, one per property, each verified to fail
on the source it fixes.

**The rewrite, end to end.** Node A writes an artifact and signs it; it
replicates to node B by an ordinary sync; node B - hostile now - rewrites the
title, the body and the status in its own database, keeps `node = 'nodeA'`,
stamps a reading that beats A's, and signs the result with **its own key**,
which is the best a peer that does not hold A's key can do. Node A pulls, and
refuses it: *signature from node nodeA does not verify*. A's copy still says
what A wrote. The same three ways over as a Go test - signed by another node,
not signed at all, and carrying A's signature over a different row - plus one
flipped byte of the title, the body, the owner, the reading and the signature
itself.

**The relay.** Node C is a keypair and a name, which is all a node is to a row.
It signs a row, node B takes it - C's self-signed identity is on the same page,
and B has never heard of C either - and node A pulls that row from B, learns C's
key from the page, verifies C's signature and applies it. Then B edits the row
in transit, and A refuses it: the relay cannot sign for C. Both halves are in
the gate, driven through the two real nodes.

**The two layers compose.** A peer signs a project-wide grant correctly, with
its own key, naming a grantor who holds no principal in the project it opens.
The signature verifies - it really did write it - and `checkGrant` refuses it
anyway. The check asserts the refusal is the authorisation message and not the
signature one, which is what says neither layer swallowed the other.

**No silent rotation.** An identity for a node already known, under a different
key, is refused at the pin and on the wire, and the rows that came with it are
refused too. The key that was here stays, and stays pinned.

**Require-pin.** With `FLOWY_REQUIRE_PINNED_PEERS` set, a row from a node whose
key was only taken on trust is refused and a row from a pinned node is not -
and pinning the first node's key lifts the refusal without anything about its
rows changing.

**The database is part of the signature.** A `jsonb` column does not come back
the way it went in, so an artifact with `fields` or a message with `meta` -
which is every message the chat endpoints write - is signed over the parsed and
re-encoded value. The check writes both, reads them back, pulls them as a peer
would be handed them, and verifies. Without it the assignment thread was the
first thing to stop replicating, which is how it was found.

Existing checks changed with the fix rather than around it. The hand-assembled
deltas in the gate - the forged grant, the read-share rewrite, the re-pointed
task, the tie-break rows - are signed now by the node they name, through `flowy
sign`, so each of them still tests the authorisation rule it was written for
instead of stopping at the new one. The two federated nodes exchange and pin
each other's keys at startup, the way two operators would.

## The eighth round of security fixes

Phase 6.5 was reviewed again, against the signing itself. The canonical encoder
came back clean - length-prefixed, no field that can be re-cut into another -
and the content-rewrite finding it was written for is closed. What came out
behind it is four defects of a different kind: **a signature says who wrote a
row, not what they were allowed to write.** A peer signs its own rows with its
own pinned key, so authenticity is satisfied and authorisation is still the only
thing standing between a valid signature and a capability the peer minted for
itself. Same rule as the rounds before: one check in `run-tests.sh` per defect,
each verified to fail on the source it fixes.

Nothing here changes how a node is configured.

**HIGH - a pulled share of somebody else's artifact was taken on trust.**
`checkGrant`'s pull half asked two things of a share: that it reaches this
principal at all, and - only when the grant said this principal signed it - that
they could have issued it. The reach test is satisfied by naming this principal
as the **subject**, so a share of an artifact owned by somebody else, granted by
anybody at all, matched neither branch and merged. From then on the puller reads
that artifact for good: the per-artifact clause in `ArtifactFilterSQL` asks for
the artifact and the subject and never for the grantor, and the forged row
pushes onward from here like any other. Reproduced live - a grant signed by a
pinned peer, `applied grants: 1`, and `ReadArtifact` handing over a body that
was `ErrNotFound` a moment before. A pulled share is now judged the way a pushed
one is, against the artifact this node holds: the artifact has to be here, has
to be shareable at all, and its `owner_user` has to be the grantor. Only the
last line of the push rule is dropped - the carrier is not asked to be the owner,
because on a pull it never is.

That leaves the two rows needing each other: the artifact is readable here
because of the share, and the share is the owner's to give because of the
artifact. `sharedInDelta` breaks that without believing either row on its own -
an artifact may land somewhere the principal cannot otherwise reach when the
same page carries a share naming this principal as the subject and naming, as
the grantor, the very owner the artifact says it has. Both rows are signed by
the node that wrote them, and a share that came with its own version of a row
that is already here is refused twice over: the artifact because a merge does
not change hands, the share because the owner it claims is not the owner this
node holds.

**MEDIUM - a new task could be minted by anybody who could read the thread.** A
task is a read capability: the tasks clause in `EventFilterSQL` shows a whole
thread to `from_user`, `to_user` and the agent it was delegated to. The new-task
branch of `checkTask` asked only that the carrier was a party to the row, could
read the artifact and could read the thread - never that they could have
**opened** the handoff. `POST /api/assign` requires the assigner to own the
artifact and opens a fresh thread; the merge required neither, and
`assignee_agent` was guarded while `to_user` was checked against nothing. So a
principal who merely reads an artifact could carry in `{from_user: themselves,
to_user: any local user, thread: any thread they can see}` and hand that user
the conversation. Reproduced live - `applied tasks: 1`, and the named outsider
reading the thread's first event. A new task now has to name the artifact's
owner as `from_user`, and the thread it names has to be one nothing has been
said in yet. The second half is federation-safe because the entry that opens a
real handoff is a `task` event, and a minted type is refused at every wire path:
a task that was genuinely replicated arrives with its thread empty. An existing
task's two people were already frozen by the re-point check.

**MEDIUM - a status move and a forge link could land on a deleted artifact.**
`setArtifactStatus` and `setArtifactExternal` were both `UPDATE artifacts ...
WHERE id = $1` with the result thrown away and no tombstone predicate. The
handlers gate on a filtered read, which refuses a tombstoned row - but the read
and the write are two statements, and the owner's delete lands in between. The
move then matched the dead row anyway: a new reading, this node's name and this
node's signature stamped onto a deleted artifact under somebody else's hand, and
- because both operations write their entry in the same transaction - a trail
entry for a transition of an artifact that no longer exists, replicating
outwards. `updateTask` already answers this with `ErrNotFound`; these two sites
were missed. Both carry `AND coalesce(tombstone, false) = false` now and check
`RowsAffected`, and the paired event rolls back with them.

**LOW - a saturated clock handed the same reading out twice.** At `(MaxWallMS,
MaxLogical)` `bump` returned without advancing - which is right, because a
wrapped reading is negative and sorts below every row the node ever wrote - and
`Now` then returned the same packed value again, against a documented invariant
that every reading is strictly greater than the one before it. Two rows would
share it, and `loses` drops the second of them silently. It takes a local wall
clock at about the year 6.6 million to get there, and nothing off the wire can
push it there - `checkReadings` refuses anything past `now + 24h` and `Pack`
clamps below `MaxWallMS` - so it is LOW. But silent is the wrong failure:
`Clock.Now` and `Clock.Pack` return `hlc.ErrSaturated` and no timestamp now, so
the write that wanted a reading fails instead of stamping a duplicate. Every
store write path propagates it; `Update` and `Reading` are unchanged, because
learning what a peer has seen and reporting where the clock stands are not
readings to stamp a row with.

## The ninth round of security fixes

The re-review certified the core: the permission filter is a `WHERE` clause with
nothing filtered after the fact, the SQL is parameterised throughout, the clock
and the id generator fail loud rather than repeat themselves, a row's
authenticity is checked before its authority and keys do not rotate, and both
sync doors hold the push, the pull, the minted types, the ownership and the
project floor. What came out around it is four hardening defects - **four places
where something was accepted, stored or started without being held to the rule
the rest of the node keeps** - and one note about the `go:embed` placeholder,
which is not a security question at all. Same rule as the rounds before: one
check in `run-tests.sh` per defect, each verified to fail on the source it
fixes.

Nothing here changes how a node is configured.

**MEDIUM - a grant's `cap` was accepted unvalidated and consulted by nothing.**
It came off the request body, was normalised only from empty to `read`, and was
then stored, **signed** - it is inside the canonical form - and replicated. No
reader ever looked at it: `CanRead`, `ArtifactFilterSQL`, `EventFilterSQL` and
every share clause in the merge treat a grant that is not tombstoned as a read.
So `cap: "write"`, or ten megabytes of it, was persisted and travelled to every
peer as a capability this node had apparently agreed to, waiting for the first
reader that does look. That is a column that lies and a latent escalation the
day somebody implements cap checks against the values already in the table. The
set is now what is implemented - `read`, and empty meaning `read` - and both
doors ask: `POST /api/grants` answers `400`, and `insertGrant` returns an error,
so the merge holds the same line on a row a peer signed for itself. Widen it
when a second capability exists, not before.

**MEDIUM - `mem_write` was two writes with nothing holding them together.** The
memory item went in through `UpsertArtifact` and the `memory.write` entry
through `AppendEvent`, and a node that stopped between them left a memory with
no record of having been written - permanently, because nothing here comes back
to finish a half-written operation, and the half that landed replicates on its
own, since every row carries its own reading. This is the project's own `inTx`
standard, which `WriteAssignment`, `MoveArtifactStatus`, `LinkArtifactExternal`
and `UpdateTaskEvent` all keep, violated in exactly one place. There is a
`WriteMemory` now: one clock reading taken up front, stamped on both rows, both
written in one transaction, and the entry takes its artifact and project from
the item rather than from the caller - the item's id may only exist once the row
has been filled in.

**MEDIUM/LOW - `flowy mcp` on stdio outlived SIGTERM.** The serve loop was `for
scanner.Scan()`, and a blocked read of stdin is not interruptible: the
`signal.NotifyContext` context was passed to dispatch and never looked at by the
loop, so a terminated server sat in the read until its client closed the pipe.
Clients that kill their server and wait for it waited for their own timeout
instead, and orphans accumulated. The reading is on a goroutine now and the loop
selects on it against `ctx.Done()`, so cancellation returns and `main` exits;
what is half-read on stdin is the client's to resend to whatever it starts next.

**LOW - event parents were stored unvalidated.** `POST /api/events` took
`parents` verbatim, and the chat path read `parents[0]` through the filtered
`ReadEvent` only to decide which thread to inherit, and only when the body named
no thread - so the rest of the list, the whole list on the events endpoint, and
every element once a thread is named went into the DAG unchecked. Nothing leaks:
a parent id is echoed only to somebody who can already read the event carrying
it, and following one is itself a filtered read. What it costs is DAG integrity -
an event can claim descent from ids that are not here, or from a conversation
its writer cannot see, and the console's thread pane and every future reader walk
those edges as structure. Both write paths now check the whole list in one
filtered query, and a parent that is missing and a parent that is out of reach
get the same answer, which is the answer a read of it would give.

**ROBUSTNESS - the `go:embed` placeholder, and a correction to what was claimed
for it.** `console.go` embeds `web/dist` so that `flowy serve` is one file, and a
tree where the console has never been built needs something in that directory or
the pattern matches nothing and the build fails. `web/dist/.gitkeep` is that
something. It carries a line of text saying what it is for, `npm run build`'s
postbuild step writes the same bytes back after vite empties the directory, and
the gate exports the commit and builds it - no `node_modules`, no vite output, no
network - so the tree that is handed over is a tree that is known to build.

The correction: this round was written up as having fixed the file being lost
when the tree is copied out of the sandbox, by making the placeholder non-empty.
It did not. The copy was dropping the file for reasons of its own, and the fix
for that landed in the harness that does the copying - `firecode`, which now
feeds `git ls-files` to `rsync --files-from` - not in this repo. Non-empty
placeholder and non-empty check both stay, because a placeholder that says what
it is for is worth having and the postbuild step has to write back exactly what
is committed. Neither of them delivers anything.

## The tenth round of security fixes

The re-review certified the core again - the filter is a `WHERE` clause with
nothing filtered after the fact, the SQL is parameterised throughout, the clock
and the ids fail loud, the tombstone and TOCTOU holes are shut, and nothing
renders unescaped - and found one real leak with four pieces of hardening around
it. Same rule as the rounds before: one check in `run-tests.sh` per defect, each
verified to fail on the source it fixes.

Nothing here changes how a node is configured.

**HIGH - the event filter's project-wide grant branch had no floor.**
`ArtifactFilterSQL` floors `personal` and `project-only`: no grant reaches
through either. `EventFilterSQL`'s per-artifact share branch carries the same
floor - it joins `artifacts` and refuses one behind it. The branch beside it did
not. A live project-wide edge into the event's project was the whole test, so a
principal of `pb` holding the `pb -> pa` grant was correctly refused
`GET /api/artifact/{id}` for pa's personal and project-only artifacts and then
read **every event about them**: the chat threads, the status trails, the forge
entries, bodies and meta, over `GET /api/events`, `GET /api/inbox`, a room read
and `GET /api/sync/pull` - which is a federated peer holding, for good, event
bodies it could never have pulled row by row. That is the filter's own
documented invariant broken: an artifact behind the floor is meant to be no more
readable event by event than it is row by row. The branch now asks the same
question the share branch asks - an event that **names** an artifact inherits
that artifact's floor - while an event that names none is project chatter and
still crosses, which is what the grant is for.

**MEDIUM - every `500` echoed the raw error.** `writeJSON(w, 500,
errorBody(err.Error()))` was the pattern everywhere, and what it wrote out was
the store's wrapped chain - `store: create artifact: pq: new row for relation
"artifacts" violates check constraint "..."` - table names, column names,
constraint names and statement fragments, to any principal holding any token,
including a federated peer with the most minimal credential here. The forge
handlers could add a child process's stderr on top. A `500` now says `internal
error` and a short `ref`; the whole chain goes to the log under the same `ref`,
which is what the operator greps. `ErrNotFound` and `ErrTaken` are unchanged -
they were never this path - and one case still says something specific: an issue
filed on a tracker before the write here failed names its number, because that
number is the only way anyone finds it again.

**MEDIUM - a limit over the cap silently became the default.** `limit()`
returned 200 both for an absent limit and for one over 1000, so `?limit=5000`
got 200 rows with nothing said about it - and a short page means "that was all
of them" everywhere else here, so a caller reading one stopped at 200 believing
it had everything. Over the cap is now the cap.

**LOW - a body could carry a second JSON value.** The decoder read one value and
never asked whether the reader was exhausted, so
`{"type":"bug"}{"type":"x","visibility":"personal"}` decoded as the first object
and dropped the rest on the floor. `DisallowUnknownFields` - the whole
strict-input guarantee, and the reason a misspelled `visibilty` is an error
rather than a default - only ever looked inside the value it decoded. Anything
after the first value is now the same `400` every other malformed body gets.

**MEDIUM - `mem_write` could promote a personal item into a project.**
`POST /api/artifacts` refuses to give a project-less row a project on update -
"has no project and is its owner's alone; an update cannot move it into ... -
create it there instead". `mem_write`'s update path had no such refusal: the
home came back `nil` for a personal item and was filled in with the token's
project whenever the update named a non-personal scope, so `mem_write {id,
scope: "shared"}` moved a personal item into the caller's project as shared with
nothing said about it. Owner-initiated, so not an escalation, but a floor
crossed silently - the exact "quietly written at the wrong visibility" mistake
the rest of this model is built to refuse. It is refused now, in the same words
the API uses. An update that leaves the scope alone is untouched, and a new item
written at a scope is what a scope is for.

## The eleventh round of security fixes

The re-review certified the core exhaustively clean again and found three
residual defects, all in the event-merge and newly-visible machinery. Two of
them are one class: an event carries four columns that refer to somebody else's
work - `actor`, `artifact`, `thread`, `parents` - and only `thread` and
`parents` were checked on the door they arrived at. Same rule as the rounds
before: one check in `run-tests.sh` per defect, each verified to fail on the
source it fixes.

Nothing here changes how a node is configured, but one of the three makes
`flowy identity pin` matter more than it did - see below.

**HIGH - the pull door took an event's attribution on trust.** A pulled event
was checked for three things - not a minted type, lands somewhere this principal
reads, thread it may write into - and for nothing at all about who it says it is
from. `applyEvent` inserts `actor` and `meta` verbatim, and `speakerStripped`
only ever ran on `POST /api/events`, so a merged event's `meta.actor_user` and
`meta.actor_kind` rode in untouched as well. Row signing does not answer this:
a signature says the node wrote the bytes, not that the actor column is honest.
So a hostile peer served a page with a chat event on it naming somebody else,
signed with its own perfectly good key, and it verified, landed, and rendered
everywhere as that person - permanently, because the log is append-only and has
no delete, and onward, because the next peer pulls it too. Operator pinning did
not help by itself: the forgery is genuinely signed by the pinned peer's key.
Push refused this all along (`checkEvent`: the actor is the pusher) and the API
refused it twice (the actor is the token's, and the meta is stripped). The pull
door was the one left open.

Attribution is now answered as the authenticity question it is, with the one
thing this node decides for itself: whether its operator pinned the writing
node's key by hand. From a **pinned** node an event says who wrote it and is
believed, which is what ordinary federation is made of - alice's message really
does replicate to bob's node under alice's name, meta and all. From a node whose
key merely arrived on a page - trust on first use, which is nobody's decision -
an event may say only what the pulling principal could have said itself: the
actor column has to be that principal or their agent, and the meta may not name
anyone else as the speaker. Refuse-outright rather than relay-stamping, because
a stamp is a thing a reader has to notice; the row does not land at all.

Two consequences worth saying plainly. Stripping the speaker keys out of a
merged event's meta - the other way to close the meta half - was **not** taken:
`meta` is inside the row's signature, so rewriting it on the way in produces a
row that no longer verifies under the `sig` stored beside it, and this node then
serves an unverifiable row to every peer downstream. It would also throw away
the legitimate `actor_kind` that tells a person's message from their agent's.
And the trust that pinning expresses is now load-bearing for the log as well as
for the rows: **pin the peers you replicate from.** `FLOWY_REQUIRE_PINNED_PEERS`
already refuses every unpinned row; without it, an unpinned peer can still relay
your own work back to you and nothing else.

**MEDIUM/HIGH - an event could name an artifact its writer cannot read.**
`handleAppendEvent` validated the thread through `mayWriteThread` and every
parent through `mayNameParents`, and took `req.Artifact` on trust; `checkEvent`
never looked at `e.Artifact` at all, and the pull branch's `eventReadable` is
true for an event in the principal's own project whatever the artifact column
says. The column is not decoration: the per-artifact share clause in
`EventFilterSQL` carries the events about an artifact to everybody it is shared
with, and `GET /api/artifact/{id}/history` is gated on reading the artifact
rather than on reading each event. So a writer holding nothing but a guessed id
could put entries into what that artifact's readers see, and they replicated
from there - injection, plus a cross-project existence oracle with a body
attached. `parents` were closed for exactly this reason, that an edge in the log
is a claim; the artifact column was left. All three doors ask now. On the API it
is the read itself - `ReadArtifact`, and a missing artifact and one out of reach
get the same `404`, the way a parent already did. At both merge doors it is
`artifactClosed`, which has `threadClosed`'s shape for `threadClosed`'s reason:
an artifact that is not on this node is hidden from nobody, and a replicated
event legitimately arrives before - or without - the artifact it names, so what
is refused is an artifact that is here and out of reach.

**MEDIUM - one grant bought a project's worth of statements.** When a fresh
project-wide grant makes more than a page of older artifacts newly visible,
`syncNewlyVisible` read every remaining id in one statement - `OFFSET` with no
`LIMIT` - and then `holdPending` wrote them to `sync_pending` one `INSERT` at a
time, drained one `UPDATE` at a time, inside the request that carried the grant.
One grant row therefore cost the **serving** node O(N) sequential round trips
and an N-row answer, N being whatever the project holds, and the puller's 60s
`syncTimeout` bounds the client and not the server. Any principal allowed to
mint a project-wide grant into its own project and then pull could ask for it.
The rescan now reads a batch at a time by keyset - `(hlc, id) > (...)` rather
than a growing `OFFSET`, which would re-scan what it had already handed back -
and the three `sync_pending` writes go out as one multi-row statement per batch.
Every id is still written down: the debt has to be complete, or the reader is
quietly short of the rows the grant opened. What is bounded is each statement.

## The twelfth round of security fixes

The re-review certified the core clean again and found three defects. The first
is the tenth round's leak on the branch the tenth round did not touch, so it is
not fixed branch by branch this time: the artifact's read rule has exactly one
definition now, and both filters evaluate it. Same rule as the rounds before:
one check in `run-tests.sh` per defect, each verified to fail on the source it
fixes.

Nothing here changes how a node is configured.

**HIGH - the event filter's home-project branch had no artifact floor.** The
tenth round put the floor on the two grant branches and the branch beside them -
`ELSE {a}.project = {project}` - kept none, which is the widest of the three:
every reader in the event's own project takes it, and it hands over every event
in that project unconditionally. So a per-artifact share was a way to publish
somebody else's artifact to a whole project. Artifact `x` lives in `pq`; `u`, who
works in `pp`, holds a share of it by name and nobody else in `pp` reaches it.
`u` posts `{type: note, artifact: x, body: ...}` - `mayNameArtifact` passes,
because `u` really can read `x` - and `handleAppendEvent` puts the event in `u`'s
home project, `pp`. Every other principal of `pp` then reads that body and its
meta over `GET /api/events`, `GET /api/inbox`, a room read and
`GET /api/sync/pull`, while `GET /api/artifact/x` and
`GET /api/artifact/x/history` answer them `404`. The two read surfaces disagree
and the wider one wins, and it replicates from there for good.

The fix is the arrangement rather than the branch. `artifactReachSQL` is the
artifact read rule - personal floor, project match, `project-only` floor, live
project-wide grant, live per-artifact share - as one SQL fragment over one
alias. `ArtifactFilterSQL` is now that fragment, and `EventFilterSQL` evaluates
the same fragment on the artifact an event names, in a clause **outside** the
`CASE`, so no branch of the event filter can be reached without it and a fourth
branch cannot be written without it either. Two consequences worth stating: an
event naming an artifact in a project the reader has an edge into is now
readable only if the reader really reaches that artifact, rather than if the
artifact merely was not behind a floor - a tightening of the tenth round's
approximation; and the tasks clause is deliberately left outside the `AND`,
because a party naming their own artifact in their own handoff thread is
disclosure by a party, not a way round the floor.
`TestEventFloorMatchesTheArtifactFloor` is `TestCanReadMatchesSQL`'s shape for
the second surface and holds the arrangement in place.

**MEDIUM - concurrent filings could orphan an issue on the tracker.** Filing is
three steps - read the artifact, open the issue over the network, write the link
back - and only the read looked at whether the artifact had been filed already.
`setArtifactExternal`'s `UPDATE` was `WHERE id = $1 AND coalesce(tombstone,
false) = false`, with nothing about `external`. Two filings by the owner could
be inside all three steps at once: both passed the up-front check, both minted a
real issue, and both wrote - so the second overwrote the first, and issue #1 was
left open on the tracker with no row anywhere pointing at it. Nothing syncs its
state, nothing pushes a reply to it, and nobody finds it again. The link is now
written under `AND external IS NULL`, and no rows affected is `ErrAlreadyFiled`
rather than success: the loser's transaction takes its filing entry back out
with it and the handler answers the same `409` the up-front check gives, naming
the link that won and the issue this call opened for nothing - which is the only
record of that issue anybody gets. It is the discipline `TombstoneArtifact` and
`UpsertArtifact` already keep: the predicate that decides a write lives in the
statement that writes.

**LOW - `handleChatSay` swallowed a real `ReadEvent` error.** A reply that names
parents and no thread inherits the parent's thread, and the read of that parent
was `if err == nil` - so `ErrNotFound` and a dropped connection or a statement
timeout were the same answer. For the first, a fresh thread is the deliberate
answer and stays that way: the caller named no thread, and a `403` would say the
parent's is one worth guessing. For the second it silently forked the
conversation - a new thread minted, the DAG edge still pointing at the parent,
the reply sitting where nobody reading the thread will find it - and nothing
said the store had been unreachable. The `ThreadHidden` call six lines below has
always told the two apart; this asks the same question and `500`s.

## The thirteenth round of security fixes

The lab's lying-peer adversary was run against a live node with a control: the
same signed delta, offered both ways. Push refused it row for row; pull applied
`{artifacts: 2, events: 4, grants: 1, identities: 2}` of it. Three findings, all
federation provenance, and the first is the shape of the other two. Same rule as
the rounds before: one check in `run-tests.sh` per defect, each verified to fail
on the source it fixes.

Nothing here changes how a node is configured. It does make `flowy identity pin`
matter more again - see the first fix - and `FLOWY_REQUIRE_PINNED_PEERS` now
refuses a key as well as a row.

**HIGH - the provenance check was only wired into push.** `checkArtifact`
refused a row that is not the pusher's own, on both the new-row and the
already-here branch, and both tests were guarded on `mode == modePush`. On a
pull only the reach test and the owner-does-not-change test ran. So a peer
serving a page put a **new** artifact owned by a third party into it, signed
with its own key - authentic, because the peer really did write those bytes -
and it landed with the forged owner, which then holds the update and tombstone
rights the `owner_user` column carries. Grants had half a rule: one opening the
*receiver's* project was refused, one opening that project *outwards* on
somebody else's say-so was applied, which hands this project's work to a peer's.
The eleventh round fixed exactly this on the event door and nowhere else.

The answer cannot be owner-is-puller. Relaying other people's rows is what
federation is, and alice's artifact arriving from alice's node is the point of
the exercise. So the eleventh round's answer is generalised instead:
`pulledParty` asks whether the party a pulled row names - the owner of an
artifact, the actor of an event, the grantor of a grant - is this principal or
its agent, and if it is not, whether the node that authored the row is one the
operator **pinned**. A pinned node is one somebody decided to believe, and
believing a node includes believing what it says about who wrote what. A node
whose key merely turned up on a page may hand over only what this principal
could have handed over itself. It applies to the row that is already here as
well as the new one, because rewriting somebody else's artifact under a node
name the relay can sign for is the same forgery as inventing it. `checkTask`
already answered the question on both doors - a task has to name this principal
as a party either way - and is unchanged.

**HIGH - `created` was not signed, on artifacts or on events.**
`canonicalArtifact` named twenty-one fields and `canonicalEvent` its own set,
and neither named `created`, so the date was outside the signature and an honest
relay could rewrite it. The adversary planted rows dated `2026-06-01` and
`2026-05-20` on a receiver. A signed row carrying an attacker's timestamp is
worse than an unsigned one, because everything around it says authentic, and a
date is not decoration: every list, every digest and every reader orders and
ages by it. `Created` is now a field of `sign.Artifact` and `sign.Event`,
encoded as microseconds since the epoch with a marker for absent - microseconds
because that is the resolution `timestamptz` keeps, so the row still verifies
after the round trip through the column. `grants` and `tasks` have no such
column, so there is nothing there to sign.

That moved where the date comes from. It used to be the column's own `DEFAULT
now()`, filled in after the signing, which is precisely a value nothing signed:
the local writes mint it themselves now (`createdNow`, truncated to the
microsecond) and pass it in, so the row that lands is the row that was signed.
An edit keeps the date the row already has - `upsertArtifact` reads it before it
signs - and a create does not take one from the caller, so backdating your own
artifact through a request body is not a thing either. On the merge, `created`
is carried onto the stored row rather than kept at this node's own clock, or the
row would sit here under a signature over a date it does not have and no peer
downstream could verify it. `flowy sign` dates a row that has none in the same
call, for the same reason.

**MEDIUM - identity provenance on a pulled page.** The adversary's pull applied
two identity rows, which is a peer teaching the receiver which key belongs to
which node. Two of the three rules already held and were left alone: an identity
has to be **self-signed** by the key it carries, so a peer cannot serve an
identity for node C unless it holds C's key, and a second, different key for a
node already here is **refused** whether that node was pinned or taken on trust.
What was missing is the third: under `FLOWY_REQUIRE_PINNED_PEERS` a first-contact
identity was still recorded, and only the rows behind it were refused, one at a
time, by `authentic`. A deployment that will not take a row from an unpinned node
has no business taking the key that would make one verifiable either, so
`applyIdentity` refuses the key itself there. Without that flag, first contact
remains trust-on-first-use: an unknown node's self-signed identity is taken and
marked unpinned. That is the documented residual - a peer can claim a name this
node has never heard of, under its own key - and pinning, or that flag, is what
closes it.

## The fourteenth round of security fixes

The lab ran the lying-peer adversary again, with the control the thirteenth
round earned: the **same signed delta, offered at both doors**. This time both
doors refused - and not the same rows. About four of seven forgeries died on the
pull door, three of seven on the push door, and the two sets were nearly
disjoint. A forgery one door catches is a forgery the other applies, and a peer
picks its door: whatever push will not take, it offers to pull.

That is a design defect rather than three bugs. The two doors were written at
different times against different holes, so what looked like one rule with two
settings was two partial implementations that happened to overlap:

| the row | push used to | pull used to |
| --- | --- | --- |
| artifact owned by a third party | refuse (owner-is-sender) | refuse only since the thirteenth round |
| share whose grantor is the artifact's real owner | refuse (grantor-is-sender) | apply - and federation needs it |
| project-wide grant *out of* the carrier's project | refuse | apply |
| project-wide grant *into* the carrier's project with no local opener | apply | refuse |
| handoff whose `from_user` is a third party | refuse | apply if the carrier is the other party |
| event under a third party's name | refuse | refuse only since the eleventh round |

**The fix is one predicate.** `mayAssert` - the thirteenth round's `pulledParty`,
generalised and moved onto both doors - answers one question about every
replicated row: may a row asserting *this* party be applied here at all? Two
answers, and no third. It is the carrying principal's own row, or their agent's,
and nobody has to be trusted about it; or it is a third party's, and then it is
taken only from an authoring node the **operator pinned**. `syncMode` is gone:
`checkArtifact`, `checkEventRow`, `checkTask` and `checkGrant` take no mode and
have no branch on one, so `SyncApplyAs` and `SyncApplyFrom` differ in where the
delta came from and in nothing else.

The authorisation checks stand on top of it unchanged and on both doors: reach,
owner-does-not-change, no-project-move, a thread you can read, an artifact you
can read, an assignment that is the owner's to make into a thread nobody has
spoken in. Two of them moved rather than changed:

- **a grant's direction.** A project-wide grant's grantor has to hold a
  principal in `to_project` - the project being opened - which is what POST
  `/api/grants` requires of the caller. The pull door asked it only when the
  grant named the carrier's own project, the push door asked something else
  entirely (`to_project` had to *be* the carrier's project, whoever the grantor
  was). It is now asked of every project-wide grant whose `to_project` is a
  project somebody here is in - `projectIsHosted` - because a grant opening a
  project this node hosts reaches this node's work whether or not the carrier is
  in it. A grant between two projects nobody here is in opens nothing here and
  is federation passing through.
- **an event's meta.** A pinned node's word decides the `actor` column, which is
  correct by design and is what makes alice's message arrive on bob's node under
  alice's name. `metaOutrunsTheActor` stops it claiming more than that: a meta
  naming a speaker who is neither the actor nor the person that actor's agent
  acts for is refused when this node can resolve the actor to a user of its own.
  An actor this node has never seen is one the pinned node is entitled to
  describe - agents do not replicate, so an agent's message arrives with an id
  nothing here can resolve.

What this rule *costs* is stated where a deployer will read it before they pin
anything: pinning a node means trusting everything it says about who did what,
including about your own users. See "Keys, and how a node comes to hold one".
The push door is more permissive than it was for exactly that case and no other,
and it was already true of the pull door, which every federating node uses.

**MEDIUM - the version was frozen.** `version` was one constant, and half a
dozen distinct builds reported the same string on `GET /healthz`, `GET
/version`, the MCP `serverInfo` and `flowy version`. "Which build refused that
row" and "what is this peer actually running" are the first questions this kind
of work asks, and the wire could not answer either. The scheme is now
**`release+stamp`**: `release` is the phase, bumped by hand, and `stamp` is the
short commit the build was linked with - `go build -ldflags "-X
main.buildStamp=$(git rev-parse --short HEAD)"`, which is what `run-tests.sh`
does. An unstamped build says `+src` rather than claiming a commit it is not.

## Deployment

**Build it with a stamp.** The version is `release+stamp` - the release is the
phase and the stamp is the build, so build with `go build -ldflags "-X
main.buildStamp=$(git rev-parse --short HEAD)"` and `GET /healthz`, `GET
/version`, the MCP `serverInfo` and `flowy version` all name the commit the
binary came from. A build with no flags reports `+src`.

The store speaks the Postgres wire and nothing else. The spine of `schema.sql`
depends on nothing that is Postgres the storage engine - no extensions, no
partitioning, no identity columns, no triggers, no stored procedures - so
deploying against a SereneDB node is a change of DSN:

```sh
export DATABASE_URL='postgres://user@serenedb-host:5432/flowy?sslmode=disable'
```

The gate itself runs against stock Postgres, which is the point: the SQL has to
be portable enough to pass on both.

**Telemetry is off until it is configured.** `FLOWY_OTLP_ENDPOINT` names a
collector - `http://localhost:4318`, or the `/v1/traces` URL itself - and
without it the spans are recorded in this node's own store and exported
nowhere. Nothing else is needed: the exporter is a bounded queue and one
goroutine, a collector that is down is logged and the spans are dropped rather
than retried forever, and how many were dropped is in the `node` group of
`GET /api/metrics`. Scraping is a read like any other, so a Prometheus job needs
a token:

```yaml
scrape_configs:
  - job_name: flowy
    authorization: { type: Bearer, credentials: <the operator's token> }
    params: { scope: [all] }
    static_configs: [{ targets: ["127.0.0.1:8787"] }]
```

Three tables grow with the watching and nothing prunes them yet: `spans`,
`metric_samples` at one row per series per scope per minute, and
`access_denials` at one row per 401 or 403 - which an unauthenticated flood can
drive, so it is the one to watch on a node open to the internet. All three are
local, never replicated, and hold no fabric row, so a dated `DELETE` is a safe
cron entry:

```sql
DELETE FROM spans          WHERE started < now() - interval '7 days';
DELETE FROM metric_samples WHERE at      < now() - interval '30 days';
DELETE FROM access_denials WHERE at      < now() - interval '30 days';
```

Deleting the samples costs the anomaly pass its baseline, which it will say -
`insufficient samples` - rather than papering over.

The exception is the `SEARCH` section at the bottom of `schema.sql` - a
`tsvector` column and a GIN index, which are Postgres full text and nothing
else. It is quarantined there because it is meant to be deleted: when SereneDB
brings vectors, that section goes and `store.SearchArtifacts` becomes a vector
query. Nothing above it depends on anything below it.

## Phase 10 status

Green from clean. `./run-tests.sh` reports `passed: 384 failed: 0` on a machine
with nothing of a previous run left on it - the 377 Phase 9 ended with, every
one of them still green, plus the 7 the terminal client adds:

- one structural: the package links no database driver and no store, so it is
  an API client and not a second reach into the rows;
- one on the refusal to start with no token anywhere;
- one that seeds a message, a memory and a task for the drive to find;
- the headless drive itself, which is the phase in one check: rooms, the message
  box and the watcher, the inbox, memory search, the timeline, the metrics, two
  resizes and a clean quit, verified against the model afterwards;
- the bad token, which is a status line rather than a stack trace;
- the real pty: two resizes, `q`, the alternate screen left and the terminal's
  `ECHO` and `ICANON` back on;
- and the node still running when the client is gone.

Nothing existing changed shape. There is no new endpoint, no new table and no
new column: every view is a `GET` or a `POST` the console and the MCP server
were already making. What is new in the tree outside `internal/tui` is the
`tui` case in `main.go`, four charmbracelet modules vendored, and `smoke
tui-pty`, which is the pty harness - about 200 lines using the `golang.org/x/sys`
that was already here, because a dependency for opening a pty is more than one
function is worth.

The one thing worth writing down, because it was nearly missed: the first
version of the headless drive waited for the posted message to appear on screen,
and passed - on the box's own echo of what had just been typed. It would have
passed on a client that never posted anything. The wait now ignores any
occurrence that sits immediately after a prompt, and the model's own message
list - which only the room read and the watcher fill - is checked at the end.

## Phase 9 status

Green from clean. `./run-tests.sh` reports `passed: 377 failed: 0` on a machine
with nothing of a previous run left on it - the 359 Phase 8 ended with, every one
of them still green, plus the 18 Phase 9 adds:

- three on the agent kind: the default that makes every existing seed still
  valid and the principal it reaches, the closed set and the person who is not
  an agent, and the capability that has one door;
- two on what a well-formed announcement is and on the quiesce log being minted
  rather than typed;
- two on the console banner: what it paints, and its being gone once the
  announcement is resolved;
- three on quiesce: the ack-required hold and its `409`, the ack that releases
  it, and the drain that releases on a release instead;
- eight across the two nodes Phase 5 already stood up: the post, the scope that
  decides what crosses, the push door, the two store-level door tests, the
  forgery, and both nodes still running afterwards.

The last attempt at this phase broke the federation bring-up and did not see it,
because a node left running from an earlier attempt was still bound to the ports
the gate reaches for. So the run above is the one that counts: every process from
every earlier attempt stopped first, and the ports checked empty before it
started. A green that only reproduces in the shell it was produced in is not a
green.

Nothing existing changed shape. `agents` gains a column with a default rather
than a required field, `POST /api/artifacts` gains one refusal, and the
replication doors gain one predicate that is a no-op for every row that is not
an announcement.

One existing check was fixed rather than added to. "the collector reassembles
both halves into one waterfall" asserted that the spans came back in start order
by string-sorting their RFC3339 times, and Go trims trailing zeros on the way
out - so `…31.641Z` sorts after the strictly later `…31.6415Z`, and the check
failed on a collector that had ordered them correctly, about one run in ten. It
now pads the fraction to nine digits and compares instants. The collector was
never wrong; the assertion was.

## Phase 8 status

Green. `./run-tests.sh` reports `passed: 359 failed: 0` with Go 1.22, Node 22.14
and Postgres 16 - the 333 Phase 7 ended with, every one of them still green, plus
the 26 Phase 8 adds:

- six on the metrics themselves: the groups and what they say about being
  measured, the corpus that is exactly what its principal may list, the personal
  artifact that moves one total, the stranger who cannot widen their view by
  asking, the operator's whole-node view with its denominators, and the numbers
  that were not measured saying so;
- three on the anomaly pass: the refusal below the minimum sample count, the
  verdict once there is a history and the second principal still being refused
  for the same series, and the refusals counted for whoever was refused;
- two on the scrape: the Prometheus text, and the same path being the console
  when nobody sends a token;
- four on the timeline: the four kinds indexed in order, the search and the kind
  that cannot be posted, the filter, and the message box posting into a run;
- three on traces: the shape of one request, the filter over somebody else's
  trace, and the OTLP payload arriving at a collector that is not this node;
- two on the tools: the four being offered, and each answering per token;
- three across the two federated nodes: one trace id in both databases for one
  handoff, the collector reassembling both halves, and the collector naming the
  half it could not reach;
- three on the console: the metrics tab, the traces tab and the timeline
  mounting in a DOM against the live node.

One existing check changed rather than went away: `GET /api/node` reports phase
8 where it reported 6.5.

Two defects were found by these checks while they were being written, and both
are the kind that only a gate finds. The first: `authenticate` resolved the
principal into a context of its own and handed that to the handler, so a handler
that joined the trace a handoff arrived in was moving a span that had already
been written down - and the far node's work sat in a trace of its own. The
second: the middleware that counts refusals read the principal out of the
request, which is the context *before* authentication, so every 403 was counted
as nobody's. Both are fixed, and the checks that found them are in the gate.

## Phase 7 status

Green. `./run-tests.sh` reports `passed: 333 failed: 0` with Go 1.22, Node
22.14 and Postgres 16 - the 329 Phase 7 ended with, plus the 4 the fourteenth
round adds: the one delta refused row for row and reason for reason at both
merge doors, the relay from a pinned node that lands at whichever door it
arrives at, the two builds that report two versions, and the version scheme's
own unit test - and of the 329, the 311 Phase 6.5 ended with, one of which changed
rather than went away (`flowy fuse` was a stub that printed a placeholder and is
now a command that refuses to mount anything it was not told to mount), plus the
18 Phase 7 adds: a real mount in this machine, a file that becomes an indexed
item, the read back, an item written by the tool showing up as a file, the two
project scopes and the grant that reaches one of them, a second principal's
mount, the four refusals, the same bytes written twice, the unlink, the
remount, the queued write cancelled by deleting its file, a `SIGKILL` between
the close and the store write, the reconcile that replays it exactly once, and
memory still being memory with nothing mounted.
Phase 6.5's own 311 were: the 200 checks Phase 6 ended with, all still green,
plus the 12 the first security slice added, the 12 the second one did, the 8
from the third, the 8 from the fourth, the 10 from the fifth, the 6 from the
sixth, the 6 from the seventh - one per defect, and two for the `project-only`
handoff because it is refused at two doors - and the 16 Phase 6.5 adds: two
end-to-end over the two real nodes (the hostile rewrite and the relay), the
canonical encoder's own unit tests, and one Go test per property of the merge -
refusal of a rewritten, unsigned or replayed row, one flipped byte, a validly
signed row that authorisation still refuses, every local write of every
replicated table signed, a signature that survives the database, the key that
arrives with the rows it verifies, no rotation at the pin or over the wire,
require-pin, and a pull that hands over public keys and no private ones, and
the 4 the eighth round adds - the pulled share, the minted task, the two blind
updates and the saturated clock - and the 7 the ninth adds: the grant cap at
both doors, the memory write that cannot log itself, `flowy mcp` under a real
SIGTERM and its loop under a cancelled context, parents on both write paths,
and the committed tree built from a `git archive` with no console build in it,
and the 6 the tenth adds: the event floor over the wire and again as a Go test
holding the two filters together, the opaque `500`, the limit that clamps to the
cap, the second JSON value refused on every write path, and the memory item that
cannot be promoted out of the personal floor by an edit, and the 4 the eleventh
adds: the pulled event under somebody else's name, the artifact column on both
write paths and at both merge doors, and the bounded rescan, and the 6 the
twelfth adds: the event floor over the wire and as a Go test, the artifact floor
the event filter now evaluates, the two concurrent filings and the predicate
that decides them, and the parent the store could not read, and the 6 the
thirteenth adds: the pulled row that is not the authoring party's to assert and
the rewrite of somebody else's artifact, `created` inside the signature as a Go
test and again over the wire between the two nodes, the date a local write
signs, and the identity that is self-signed, never rotated and pinned where that
is required. Each is verified to fail on the source it fixes. Six of the older
checks changed with the fixes rather than around them: the pushed share of
somebody else's artifact is now authored on a node nobody pinned, because from a
pinned one it is a relay of the owner's own grant and lands - which is the
fourteenth round's whole point - a reply to a message its speaker cannot read is
refused outright now rather than quietly opening a thread of its own, which is
the same rule one step earlier, a deleted artifact
now reads as `404` on both nodes with the tombstone asserted through `psql`,
the `?counts=1` health check no longer claims to be reporting the spine tables
to nobody in particular, the phase 6 checks drive the mock forge's control
routes with the operator's token because that is whose they are, and the
hand-driven push check writes its row as the replication principal, because a
push carries the pusher's own rows and somebody else's cross by being pulled.
Phase 6's own 22 were: capability selection, filing, the conflict and
permission cases, the close-to-done move, the reviewer loop in both directions,
the no-op sync, the untouched `gh`, and six `psql` checks over what all of it
wrote. Phases 0 to 5 stayed green throughout, and mostly by construction - the
three endpoints are gated on the permission filter that was already there, a
threaded comment is a Phase 3 chat event, the status move is Phase 4's status
event with one more meta field, and the link replicates because Phase 5 already
merges the row it sits on.

Not here yet, on the forge bridge: the real `gh`/`glab` path is coded and its
argv and parsing are unit-tested, but **it has never run against GitHub or
GitLab from in here** - this environment has no CLI, no credential and no
network, so that half is exercised on a host that has them. There is no
webhook and no poller either: `status` and `sync` are calls somebody (or a cron
entry) makes, so an issue closed on GitHub moves the artifact when the node is
next asked. Filing takes only a repo - no labels, no assignee, no milestone -
pull requests are read only as a `merged` state on the issue endpoint rather
than tracked in their own right, and an artifact can hold one link, so the same
bug cannot be filed into two trackers. The console has no forge view: the
external ref is JSON on the artifact and the threaded comments show up as
ordinary chat, which is most of the value but not the link.

Not here yet, on the agent filesystem: there is no `rename`, so `mv` inside the
mount is `ENOTSUP` - renaming a file would be renaming a row, and it is not
clear yet whether that should also be a `memory.write`. A file opened and closed
with nothing written to it is not a write, so `touch` leaves nothing behind and
`> file` does not empty an item (`rm` is how one goes away). Two mounts of the
same principal on one machine share the queue and would each drain it, which is
safe - the apply is one transaction and the loser sees the row already gone -
but nothing coordinates them. The mount serves one principal, decided by the
token at mount time: there is no way to hold two tokens at once, and the
operator's `?scope=all` deliberately does not reach in. Nothing has been
measured for throughput; the shape it is built for is an agent writing a note,
not a build tree.

Not here yet, elsewhere: sync is a command you
run rather than a loop the node keeps - there is no daemon, no schedule and no
push notification, so two nodes converge when somebody (or a cron entry) says
so. `users` and `agents` do not replicate either, so a person who exists on one
node is copied to the other out of band along with their token. The MCP HTTP
transport answers `POST /mcp` only, with no server-initiated SSE stream;
`/metrics` is a stub over `/healthz`; the console has no MCP-side view of memory
yet, no screen for making an assignment, and no federation view - `GET
/api/peers` is JSON an operator reads with curl. Tasks have no watcher of their
own either: the inbox is fetched on load and after an action, and only the
thread inside a task long-polls.

One thing worth knowing about the cursor model: a `pushed_cursor` is a watermark
over a hybrid logical clock, so it assumes the two nodes' wall clocks are within
the ordinary NTP distance of each other. Two things keep that honest - a node
lifts its clock above the highest reading in its store at startup, and applying
a peer's rows (including an incoming push, which lands in the serving process)
advances it past them - but a node whose clock is hours behind its peer would
mint readings that its own watermark has already passed. What there is now is a
floor under how wrong that can get: a pushed reading more than a day ahead of
this machine is refused rather than merged, and packing clamps rather than
letting a wall reading shift into the sign bit. A peer an hour out still drags
its neighbour an hour forward.

Two things worth knowing about the shape of the console: the bundle is one
600 kB chunk (react-flow and framer-motion are most of it) because nothing is
code-split yet, and there is no browser in the delivery environment, so the
console is exercised by mounting the shipped bundle in jsdom - which runs the
real code and the real API calls, but does not lay anything out.
