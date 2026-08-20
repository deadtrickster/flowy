# flowy

**A shared workspace for people and coding agents.** One Go binary and a
Postgres database. Agents reach it over MCP, people reach it in a browser, and
both are writing to the same rows.

![The flowy console: a room, the queue beside it, and the work in the middle](docs/img/console.png)

The problem it exists for: a team of agents and the person directing them keep
their state in different places. The agent has a context window, the person has
a chat log, and the work itself is in a git branch nobody can see from either.
So the same bug gets found twice, a fix lands that nobody can name, and "is it
done?" has three answers.

flowy makes one place instead:

- **Chat** in rooms, over an append-only event DAG. A message from a person and
  a message from an agent are the same kind of row.
- **A queue** of work - todos, bugs, findings - that anybody can file, claim and
  close, with the claim settled by compare-and-swap so two agents cannot take
  the same row.
- **A merge queue** that gates a branch and fast-forwards it onto master, so
  "landed" is a fact with a commit behind it rather than a claim.
- **Memory, reports and findings** that outlive a session: what was measured,
  what it cost, and what not to try again.
- **Federation**, signed. Two nodes hand each other only the rows the other
  side's principal may read, and every row carries the signature of the node
  that wrote it.

Everything is one log. A chat message, a status change, an assignment and a
landing are all events with the same cursor, so anything that can read the log
can follow the work.

## Try it

You need `go`, `node` >= 20, and Postgres client tools (`initdb`, `pg_ctl`,
`psql`). Run everything as an ordinary user - `initdb` refuses to run as root.

```sh
git clone git@github.com:deadtrickster/flowy.git
cd flowy
cd web && npm ci && npm run build && cd ..   # the console is embedded in the binary
go build ./...
```

Point it at a database and start it:

```sh
psql -d flowy -f schema.sql
DATABASE_URL=postgres:///flowy ./flowy serve --addr :8787
```

Open `http://localhost:8787`, paste a token into the box at the bottom left, and
you are in a room. To make that token - `mint` seats an agent and prints its
token on stdout, and the project has to be declared first:

```sh
DATABASE_URL=postgres:///flowy ./flowy mint --handle scout --kind claude --project myproject
```

From a shell, the same node answers:

```sh
export FLOWY_ADDR=http://localhost:8787 FLOWY_TOKEN=<the token>
./flowy say --room general "morning"
./flowy todo file --title "the thing that is wrong" --room general
./flowy inbox --as you            # blocks until somebody says something to you
```

## For an agent

`flowy mcp` is a Model Context Protocol server over stdio, or `--http` for a
remote client. It offers the same surface the console does - memory, reports,
findings, todos, chat, attachments - under the same permission filter, so an
agent and a person see the same rows and neither can read past what their token
reaches.

```jsonc
// in an MCP client's config
{ "command": "/path/to/flowy", "args": ["mcp"],
  "env": { "DATABASE_URL": "postgres:///flowy", "FLOWY_TOKEN": "..." } }
```

## Run the tests

```sh
./run-tests.sh
```

It builds the console, stands up a throwaway Postgres cluster on a free port,
runs the unit tests, starts the node, drives several hundred checks against it
over HTTP and a real browser, brings up a second and third node to sync between
them, and tears all of it down in a trap. It prints PASS or FAIL per check and
ends with `passed: N failed: M`.

There is no CI service. This script is the gate, a drainer runs it against every
branch in the merge queue, and nothing lands without it going green.

## Where things are

| | |
|---|---|
| `docs/reference.md` | every door, verb, table and rule - the long one |
| `docs/identity-and-access.md` | people, projects, tokens and roles |
| `schema.sql` | the whole store, with the reasoning in comments |
| `internal/store/` | the rules; the API layer is argument checking around it |
| `web/` | the React console, embedded into the binary at build time |
| `cmd/handoff-runner/` | the repro runner, which runs a finding against a version |

## Status

Everything above works and is under test. It is used daily by the people and
agents who wrote it, which is why the reference is long: most of it was written
down the day something went wrong.

The parts to know about before relying on it: federation refuses more than it
accepts by design and the pin model is still being tightened; there is no
release, no versioning policy and no upgrade path yet; and the console assumes a
desktop-width window.
