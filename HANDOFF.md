# Flowy - session handoff

For a session working directly in this repo (`~/Projects/flowy`). Written 2026-08-15 by the
orchestrator, which built Flowy phase-by-phase over firecode and now stays on the fc chat room to
migrate handoffs into the live fabric. Division of labour from here: this repo session works on the
Flowy code; the orchestrator stays on chat and files existing handoffs into the running node.

## What Flowy is

A local-first agentic handoff fabric / agentic-Jira: Go + Postgres-wire, one binary (`flowy`) with
`serve · mcp · fuse · sync · tui` subcommands. Everything is a typed, scoped, tagged artifact over an
event DAG; permission-checked reads/writes; ed25519 row-signing; federation; MCP shared-memory; a
React console and a co-equal terminal TUI. HEAD is `f7e16c7` - the whole architecture is built and
folded (foundations, spine+permissions, memory MCP, chat+console, Jira, federation, forge,
row-signing, FUSE, observability, announcements/system-agents, TUI), every slice gated green from a
clean run by the harness, hardened over 14 fix rounds plus a blind adversarial pass and a live
lying-peer run. Design docs: architecture https://claude.ai/code/artifact/135b22fe-8cc2-4d00-8669-50acf8c5369f
, build plan https://claude.ai/code/artifact/552056e6-699a-4798-b135-32257adf80fb .

## Build and test

```
go build -o /tmp/flowy .          # or ./cmd/smoke for the seed helper
go vet ./... && gofmt -l .        # must be clean
./run-tests.sh                    # the gate - stands up its OWN throwaway pg + serve, ~377 checks
```
Gate discipline that caught a real false pass at the end: trust the harness verdict, not an agent's
self-report; verify FROM CLEAN (kill stray pg_ctl/serve, check ports empty first) - a run that reuses
its own leftover state tests that its run passed, not that the gate passes.

## The live dogfood node (already running)

A persistent, seeded `flowy serve` you can point the TUI/console at, separate from the gate's
throwaway. It lives at `~/Projects/flowy-dogfood/` (built binary, `smoke`, `ids` = seeded
users/agents/tokens, `PG_DSN`, logs, pids; `serene-data/` and `probe_gaps.py` are the SereneDB
gap-probe, not the backend).

- serve: `http://192.168.1.55:8787` (node `dogfood`, LAN-bound on the wired interface). Health:
  `curl 192.168.1.55:8787/healthz`. Loopback 8787 is no longer served - serve takes one
  listen address. LAN-exposed: the API is auth-gated by bearer token; the console shell and
  `/healthz` answer unauthenticated, as they always have.
- backend: Postgres 18 in docker `flowy-dogfood-pg` on `127.0.0.1:5433`; data at
  `~/Projects/flowy-dogfood/pgdata18`, mounted at `/var/lib/postgresql` (the 18 image keeps data in
  a subdirectory - the 17-style `/var/lib/postgresql/data` mount is an error in 18+). Migrated from 17 by
  dump-and-restore (`dump-pg17.sql` kept beside it); the old 17 container is `flowy-dogfood-pg17-kept`
  (stopped) and the old `pgdata/` dir is untouched - drop both once 18 has settled.
- token: operator token at `~/.config/flowy/token`. More scoped tokens in `~/Projects/flowy-dogfood/ids`.
- **projects: write real work to `flowy`, not to `pa`.** `~/.config/flowy/token` is scoped
  to `pa`, which is the *smoke seeder's fixture project* (`cmd/smoke/main.go` makes alice
  and operator in `pa`, bob in `pb`, a third token in `pc`). Writing through the default
  token therefore files real artifacts into demo seed data, which is what happened to the
  first batch of shared memory here. `~/.config/flowy/token-flowy` is the same operator
  principal scoped to project `flowy`; use it for memory, reports and the worklog.
- **the project entity now says so, and it did not before.** There is a `projects` table:
  a project has to be declared before anything can be written into it, `pa`/`pb`/`pc` are
  flagged as fixtures, and every surface says which project a token writes into - `flowy
  projects` on the command line, the TUI status line (red, `@pa [FIXTURE]`), the console's
  token bar, `GET /api/whoami`, and a `warning` beside anything `mem_write`,
  `report_write` or `worklog_append` files into a fixture. It would not have refused the
  `pa` write - `pa` is a legitimate writable project - it makes it visible at the moment it
  is made. Registry rows are signed and replicate; identity is the name, and the
  canonicalised git remote on the row is what decides whether two nodes' `flowy` is one
  project. See the project entity section in docs/reference.md.
- **declare a project before pointing a token at it.** `flowy projects declare --project
  <name> --origin <remote>` (the remote is read from the work tree if you leave it out).
  `tokens.project` and `agents.project` are foreign keys into the registry, so a token
  scoped to an undeclared project is refused by the database as well as by the node.
- Moving an artifact between projects is a **rewrite, never an UPDATE**. `project` is inside
  the signed payload (`internal/sign/sign.go:101`, asserted by `sign_test.go:53`), so
  `UPDATE artifacts SET project=…` produces rows whose signatures no longer verify - forged
  rows, by the node's own definition. Re-file through the normal write path and tombstone
  the original.
- TUI: `~/Projects/flowy-dogfood/flowy tui --url http://192.168.1.55:8787` - the default url is
  loopback, and FLOWY_ADDR lives only in the unit env, not an interactive shell.
  Keys: tab/digit switch view, j/k move, / search, i post, ? help, q quit.
- serve runs under the `systemd --user` unit `flowy-dogfood.service` (env in
  `~/Projects/flowy-dogfood/serve.env`, linger on - survives logout and reboot). Logs:
  `journalctl --user -u flowy-dogfood.service`. Restart: `systemctl --user restart
  flowy-dogfood.service`. Do NOT start `flowy serve` by hand against 8787 - the unit owns the
  port now; a stray process collides with it.
- rebuild the dogfood binary after code changes here: build `web/dist` first
  (`cd web && npm ci && npm run build`), then `go build -o ~/Projects/flowy-dogfood/flowy .`
  **from the repo root** (a `cd web && … && go build .` builds the `web` directory and fails
  with "no Go files"), then restart the unit - `go:embed` bakes in whatever `web/dist` holds
  at build time, and a build from a bare tree serves "console not built".
- **a schema change needs `schema.sql` applied to the live database BEFORE the restart, and
  `scripts/deploy.sh` now does that for you.** It runs `scripts/migrate.sh` against the DSN
  it reads out of `serve.env` (or `PG_DSN`, or `$FLOWY_DATABASE_URL`), after the binary is
  built and verified and before the unit is restarted, and prints which objects that added.
  A deploy that cannot find a DSN refuses rather than restarting onto whatever the database
  happens to hold. To migrate without deploying: `scripts/migrate.sh <dsn>`, or the long way,
  `docker exec -i flowy-dogfood-pg psql -U flowy -d flowy < schema.sql`. Dump first if it is
  not routine: `docker exec flowy-dogfood-pg pg_dump -U flowy -d flowy > dump-<what>.sql`.
  Skipping it used to crash-loop the unit (`Restart=on-failure` retries forever) on whatever
  the new code read first.
- **the gate can now see schema drift, which it could not before.** It used to build its
  database from `schema.sql` on every run and nothing else, so it had never seen a database
  older than the binary - which is why the `refused_authorship` outage passed 547 checks
  twice and then took the node down. The `an older database meets this binary` section builds
  a database from `schema.sql` as of an earlier commit, applies `scripts/migrate.sh` to it,
  and asserts the result is structurally identical to a fresh database and serves a real
  read. Point it at what the node is actually running with
  `FLOWY_BASELINE_REV=$(cat ~/Projects/flowy-dogfood/.deployed-commit) ./run-tests.sh`.
  The thing it is really guarding is `CREATE TABLE IF NOT EXISTS`: a column added inside a
  table body and nowhere else is a no-op on every database that already has the table, so it
  works on a fresh database, passes every other check in the gate, and 500s on the node.
  Add the matching `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` and the check goes green.

## Backend: SereneDB is the target, but not ready yet

Dogfooding Flowy against SereneDB (commit `7ef9ecba`) could not bring serve up - three blockers,
written up in `~/Projects/serenedb/SERENEDB-DOGFOOD-GAPS-HANDOFF.md`:
1. UPDATE and DELETE fail over pg-wire (engine rewrites every mutation to `SELECT ... ctid` and can't
   resolve `ctid`) - blocks any app that writes rows. Most severe.
2. `jsonb` not a registered type (serenedb#613) - the backend owner's todo.
3. `tsvector` type and `to_tsvector()` missing - the store's full-text search.
So the live node runs on stock Postgres 17 for now; point it back at SereneDB as each gap closes.

## The fc chat room (how to reach the orchestrator)

Spawns fold into this repo now (`/tmp/firecode-scratch/flowy` is a symlink to `~/Projects/flowy`).
The orchestrator and claude-host share a room:
```
firecode chat --read                       # last messages
firecode chat --as <you> --to orchestrator "text"   # no $, backticks, or (parens) - trips a perms prompt
```
Keep an inbox waiter armed in the background so you hear replies:
`firecode chat --inbox --as <you>` (re-arm each time it returns - the Stop hook nags if it lapses).

## Open items

- Migrate existing handoffs into the live fabric as `bug`/`note` artifacts (orchestrator's job, on
  chat). Set `FLOWY_FORGE_REPOS` on the serve if you want forge filing too.
- Reboot durability: DONE - pg auto-restarts (`--restart unless-stopped`), serve is under
  `flowy-dogfood.service` (`systemd --user`, linger on).
- jsonb (#613) is the user's todo; the ctid UPDATE/DELETE break and tsvector are open SereneDB findings.
