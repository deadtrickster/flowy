# Deploying Flowy

Two deployments, and they are separate on purpose.

The **node** is the store: the Flowy binary with the console embedded, and its
Postgres. It drops every Linux capability, its filesystem is read only, its
database sits on a network with no route off the box, and it publishes no host
port. That is the whole of `compose.yaml`.

The **runner** is `cmd/handoff-runner`, and it is handed the host's Docker
socket, which is root on the host with extra steps. Nothing can cap that away,
so it is not in the same file, not on the same network by default, and not in
the same image. That is `handoff-runner.compose.yaml`, and it belongs only on a
host that is already trusted with Docker.

## From `git clone` to a node you can talk to

```
git clone <this repo> flowy && cd flowy
deploy/bootstrap.sh
```

That is the whole of it. The script generates `deploy/.env` with a random
database password, builds the image (the console with vite, then the binary with
the console embedded), starts Postgres with `schema.sql` applied, declares a
project, starts the node, mints the first seat, and then proves the result by
asking the node for the project list with the token it just issued. The token
lands in `deploy/.flowy-token`, mode 600.

Options: `--project NAME`, `--handle NAME`, `--kind RUNTIME`, and `--no-publish`
for a deployment that will be reached through a tunnel rather than from this
host.

Run it again after a `git pull` and it rebuilds and restarts without minting a
second seat.

### What it is doing, and why it is a script rather than three commands

A node cannot create its own tables: `BackfillProjects` refuses a database with
no `projects` table and tells you to apply `schema.sql`. So the schema goes in
through Postgres's own init directory, which only runs on a fresh data
directory, which is exactly when it is needed.

Then there is a loop worth knowing about. `flowy mint` refuses to seat an agent
in a project that is not declared, and `flowy projects declare` speaks HTTP, so
it needs a token - which does not exist until something is minted. On a fresh
database neither can go first. The script breaks it by writing the registry row
directly, which is what `schema.sql`'s own backfill does for names the data
already carries, and what `BackfillProjects` adopts and signs at the node's next
start. The row goes in **while the node is down** for that reason: a row
inserted after startup stays unsigned until the next restart, and an unsigned
registry row cannot replicate to a peer.

## Reaching it

The base file publishes nothing. Two ways in:

- `compose.loopback.yaml`, which `bootstrap.sh` uses unless you pass
  `--no-publish`, binds `127.0.0.1:8787`. Processes on this host, nothing else.
- A tunnel or a proxy on the `edge` network. There is a commented `cloudflared`
  service in `compose.yaml` showing the shape. Whatever goes there is the only
  ingress, and it belongs on `edge` alone - it has no business on the network the
  database is on.

Do not change the loopback bind to `0.0.0.0`. The API is authenticated but not
encrypted; that would put it on the LAN in the clear.

## Operating it

```
docker compose -f deploy/compose.yaml logs -f node
docker compose -f deploy/compose.yaml ps
docker compose -f deploy/compose.yaml --profile bootstrap run --rm mint \
    --handle NAME --kind claude --project flowy > /path/to/token
```

`mint` is a profile so that a plain `up` never runs it. Minting is an operator
action whose output is a secret: the token goes to stdout and the description of
the seat to stderr, so the redirect above files one and shows you the other.

Upgrades are `git pull && deploy/bootstrap.sh`. The database volume survives;
`schema.sql` does not re-run, so a schema change needs `scripts/migrate.sh`
against the running database, as it does anywhere else.

## The runner

```
cp deploy/.env.example deploy/.env    # bootstrap.sh already did this
$EDITOR deploy/.env                   # SUT_SRC_REPO, SUT_SCRATCH, HANDOFF_RUNNER_ARGS
deploy/handoff-runner.sh up -d
```

`handoff-runner.sh` is `docker compose` with a preflight in front of it -
everything after the first argument goes straight through. The preflight checks
the four things that otherwise fail a long way from their cause: the Docker
socket exists, the node deployment's network exists, the source repository is a
writable git clone, and the image actually contains a `handoff-runner` binary.

### What it needs, and why

- **The Docker socket.** It builds a system-under-test by shelling out to
  `scripts/build-sut.sh`, which compiles in the project's own toolchain image,
  and it brings up a generated compose package per run.
- **Its own image.** The node's image is Alpine with one static binary in it.
  The runner needs `bash`, `git` and the Docker CLI, so it is a second target in
  the same `Dockerfile` (`--target runner`), built from the same source.
- **A writable source checkout** at `SUT_SRC_REPO`, and it must be the **main
  clone** rather than a linked worktree. `build-sut.sh` checks the commit under
  test out with `git worktree add`, which is a write into that repository's
  `.git`; a linked worktree's `.git` is a *file* pointing at the main clone's git
  directory somewhere else, which is not mounted, so every git command inside the
  container fails naming a path that is not there. The preflight refuses that
  case and prints the clone to use instead. The path is bound at the same place
  inside the container as outside, because paths in a generated compose package
  have to resolve on both sides.
- **A scratch area** at `SUT_SCRATCH`, holding worktrees and the sha-keyed binary
  cache. A cold build of a real database is measured in hours and the cache is
  what makes the second run of a commit free, so put it on a disk with room and
  keep it across redeploys.
- **The same Postgres as the node.** `internal/repro` writes verdicts through
  `internal/store` rather than over HTTP - same module, same database - so the
  runner needs `DATABASE_URL` and neither a node URL nor a token. It joins the
  node deployment's internal network (`flowy_inner`) to get there, which is how
  the database is reached without ever publishing a port on it.

### Per-project configuration

This is the part that was hardcoded in the Python service, where the source
path, the base image and the cache directory were constants named after one
project. `scripts/build-sut.sh` already reads them from a file per project -
`scripts/build-sut.d/NAME.env`, with `scripts/build-sut.d/serenedb.env` as the
worked example - and the environment wins over the file, so a deployment points
at this host's checkout without editing the file that describes the project.
`SUT_CONFIG_DIR` is where the runner looks; the image bakes the repository's copy
in, and a bind mount over it adds a project without a rebuild.

### What is assumed here

`cmd/handoff-runner` is piece 9 of the handoff migration and may not have landed
in the tree you are reading. What is settled: it reads `DATABASE_URL` for the
same Postgres and needs no token, and it configures builds through the `SUT_*`
names `scripts/build-sut.sh` already reads. What is not: its own flags - listen
address, worker count, log and cache directories. Those live in one line,
`HANDOFF_RUNNER_ARGS` in `deploy/.env`, so wiring them when they are settled is
a single edit and nothing else in the deployment changes.

The image is built from `./cmd/...` as a directory rather than by naming the
binary, so this all builds today and simply gains the runner the day it lands.
Until then `handoff-runner.sh` says so in as many words instead of failing with
an exec error.
