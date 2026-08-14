# flowy - Handoff Fabric node (Phase 4)

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

## Run the gate

```sh
./run-tests.sh
```

It needs `go`, `node` >= 20 with `npm`, a Postgres installation (`initdb`,
`pg_ctl`, `psql`), and `curl` and `jq` for the HTTP checks - no running
database, no systemd. It builds the console first (`npm ci`, Biome, `vite
build`), because `flowy serve` embeds `web/dist`; then it creates a throwaway
cluster in a temp directory on a free port, loads `schema.sql`, builds the
binary, runs the unit tests, starts `flowy serve`, runs the live checks against
it, and tears the whole thing down in a trap. It prints PASS or FAIL per check
and ends with `passed: N failed: M`, exiting non-zero if anything failed. Phase
2 added an MCP section - `flowy mcp --http` on a free port and JSON-RPC piped
into `flowy mcp` on stdin, both transports, one store - and Phase 3 adds the
chat, watcher, inbox and console-routing checks.

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

## Subcommands

| command | what it does |
| --- | --- |
| `flowy serve` | HTTP server, wired to the store, serving the embedded console |
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
| `GET /api/whoami` | the principal this token resolves to |
| `GET /api/node` | this node, its version and its routes |

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

- **You assign what you can read, not what you own.** Passing on a bug that was
  shared with you is the ordinary case, and the share it writes gives away no
  more than the caller already had - read, on that one artifact. A personal
  artifact cannot be assigned at all: it has no project to share it into, and
  the personal floor in the permission filter would refuse it anyway.
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
| `/metrics` | node counts, a stub over `/healthz?counts=1` |

Which means the server has to answer with the app for all of them: `flowy serve`
serves `web/dist` and falls back to `index.html` for **any** non-`/api` GET, so
a reload of `/chat/general` lands back on the room. Unknown `/api/*` paths still
`404` in JSON - a client that asked for JSON and got a 200 of HTML would have to
parse the app to find out it had a typo.

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
| `tasks` | handoffs: one artifact, from one user to another, with a thread and a state of `open`\|`delegated`\|`done` |
| `peers` | replication bookmarks, one row per peer node |

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
- `chat.go` - the room view of the event log: `say`, the room read, the long
  poll and the inbox. It adds one field to `store.EventQuery` (`NotActors`) and
  otherwise reuses the log's cursor, DAG and permission filter as they are.
- `console.go`, `web/` - the console and its serving. `console.go` embeds
  `web/dist`, serves hashed assets immutably and falls back to `index.html` for
  every other non-API path; `web/` is the React app itself.

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

The last thing the gate does is ask git whether the tree it just tested is the
tree on disk: uncommitted changes, or nothing ever committed, is a failure.
`web/node_modules` and everything vite writes into `web/dist` are ignored, so
what the gate builds does not count as a change to the tree.

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

## Phase 4 status

Green. `./run-tests.sh` reports `passed: 139 failed: 0` with Go 1.22, Node 22.14
and Postgres 16 - the 103 checks Phases 0 to 3 ended with, plus 36 for
assignment, delegation, the task inbox, the lifecycle, the status trail and the
two new console routes. Phases 0 to 3 stayed green throughout: an assignment is
a Phase 1 grant, a Phase 3 chat event and one new row, and a transition is one
column and one event.

Not here yet: `flowy fuse` and `flowy sync` still print their placeholder; the
MCP HTTP transport answers `POST /mcp` only, with no server-initiated SSE
stream; `/metrics` is a stub over `/healthz`; the console has no MCP-side view
of memory yet, and no screen for making an assignment - it reads the inbox and
acts on what is in it, and `POST /api/assign` is a call an agent or a curl
makes. Tasks have no watcher of their own either: the inbox is fetched on load
and after an action, and only the thread inside a task long-polls.

Two things worth knowing about the shape of the console: the bundle is one
600 kB chunk (react-flow and framer-motion are most of it) because nothing is
code-split yet, and there is no browser in the delivery environment, so the
console is exercised by mounting the shipped bundle in jsdom - which runs the
real code and the real API calls, but does not lay anything out.
