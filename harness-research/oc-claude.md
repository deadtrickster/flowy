# opencode - what to take for the harness

Source: `git clone https://github.com/sst/opencode` at `4643e65` (v1.18.18), read at `/tmp/opencode-src`; paths
below are relative to that clone. Bun/TypeScript monorepo, Effect-based, ~25 packages. It is also the runtime
half this fleet runs under, so this is a report on the thing holding the pen - the bias to watch for is treating
its choices as settled because they happen to be load-bearing right now.

## Client/server split

**What it is.** `opencode serve` is a headless HTTP server (`packages/opencode/src/cli/cmd/serve.ts:14`) and
everything else - TUI, desktop, web console, VS Code extension, plugins - is an HTTP client of it. The API is
declared as Effect `HttpApi` groups (`.../server/routes/instance/httpapi/api.ts:52-90`), one per domain
(session, permission, provider, file, tui, question, sync, pty), and OpenAPI falls out of the declaration
(`.../server/server.ts:63-65`).

**How it works.** The server is stateless about which project you mean: every request carries `?directory=` or
`x-opencode-directory` (`.../httpapi/middleware/workspace-routing.ts:87`) and middleware loads or creates that
instance per request (`.../middleware/instance-context.ts:26-34`), so `serve` starts with `instance: false`
(`cli/cmd/serve.ts:12`). Live state reaches clients over one SSE stream at `/event` (`.../handlers/event.ts:22-80`)
with a 10s heartbeat (`:63`). The client package is generated from the API and committed, with a CI check that
regenerating yields no diff (`packages/client/package.json:12-13`). The TUI has no special path in: by default it
starts the server in a worker thread and hands the SDK a `fetch` shim over an RPC bridge against base URL
`http://opencode.internal` (`cli/cmd/tui.ts:238-249`, `cli/tui/worker.ts:31-42`); pass `--port` and the same worker
calls `Server.listen()` (`cli/tui/worker.ts:54-57`) and the same client talks TCP.

**Copy this.** Per-request project routing instead of a server pinned to a cwd. Generated + committed client
with a no-diff CI gate. The in-process transport shim, so CLI-local and CLI-remote are one client. Workspace
routing can also proxy to a workspace on another machine (`.../middleware/workspace-routing.ts:35-40`, the
`Remote` plan) - cheaper than replication when all you want is reach.

**Skip this.** Auth is one shared basic-auth password (`server/auth.ts:19-37`), and with no password set every
route is open - `serve` only warns (`cli/cmd/serve.ts:16`). No principal, so nothing downstream can be filtered
per user. Flowy already has principals and SQL-level read filtering; do not regress to this.

## Providers

**What it is.** A models.dev catalog merged with config and plugin-declared providers
(`provider/provider.ts:1340-1348`, `:1265`); each model record names its ai-sdk npm package in `model.api.npm`.

**How it works.** Bundled loader table first (`provider/provider.ts:108-133`, `:1770`), otherwise
`Npm.add(model.api.npm)` installs the package on demand and dynamically imports it (`:1785-1793`), finding the
factory by the export that starts with `create` (`:1795`). Per-provider auth methods are contributed by plugins
as an `auth` hook with declared prompts and an oauth or api-key flow (`packages/plugin/src/index.ts:88-163`,
consumed at `provider/auth.ts:40-66`), so "a provider with a weird login" is a plugin, not a core change.

**Copy this.** The three-layer catalog - shared model registry, per-provider adapter, per-model override - and auth
as declarative prompt lists, so every client (CLI, web, mobile) renders the login without knowing the provider.
Flowy's spawn-time medium selection wants exactly this shape.

**Skip this.** Installing npm packages at runtime to satisfy a model reference, and finding the factory by
`key.startsWith("create")`. A fixed adapter table with explicit registration cannot execute an arbitrary tarball
because a config string changed.

## Plugins

**What it is.** TypeScript modules resolved from npm or `file://` and dynamically imported into the server
process (`plugin/loader.ts:86-145`). Each exports a function receiving an SDK client, project, directory and a
shell (`packages/plugin/src/index.ts:56-74`) and returns a `Hooks` object.

**How it works.** Hooks are `(input, output) => Promise<void>` and mutate `output` in place; `trigger` runs
every registered hook in registration order over the same object (`plugin/index.ts:282-296`). Coverage is broad:
`chat.params`, `chat.headers`, `tool.execute.before/after`, `tool.definition`, `permission.ask`, `shell.env`,
`event`, plus a `tool` map that adds new tools (`packages/plugin/src/index.ts:222-335`).

**Copy this.** The plugin gets an API client, not internal handles - a plugin is just another client of `/api`,
same rule as your console/TUI/MCP. And `permission.ask` as a hook, so policy ("auto-approve reads under this
path") is a plugin rather than a core config dialect.

**Skip this.** No isolation, no error containment: a hook rejection propagates out of `Effect.promise` and kills the
turn, and nothing orders two plugins mutating the same `output`. Run untrusted hooks out-of-process (at minimum a
per-hook timeout + catch) and make ordering explicit.

## LSP

**What it is.** Language servers spawned lazily per project root, keyed `root + server.id` (`lsp/lsp.ts:267-281`),
with per-language recipes that download or npm-install the server binary (`lsp/server.ts:1-25` and the table below it).

**How it works.** Two call sites matter. `read` warms the file in the background and ignores failures
(`tool/read.ts:117-121`). `edit`/`write`/`apply_patch` touch the file, pull diagnostics, and append them to the
tool result the model sees - `"LSP errors detected in this file, please fix:"` (`tool/edit.ts:196-201`,
`tool/write.ts:74-78`).

**Copy this.** Diagnostics as tool output, not a separate lint step. Closes the loop inside the turn the model is
already in, costs one tool result, needs no new agent affordance.

**Skip this.** Auto-downloading server binaries. In your harness that is the environment's job.

## TUI

**What it is.** Solid + OpenTUI (`packages/tui/package.json:55-66`), ~200 tsx files, a rewrite away from the older
Go/bubbletea TUI.

**How it works.** It holds no domain logic: an SSE subscription feeds a reactive store
(`packages/tui/src/context/sdk.tsx:20-80`, `context/sync.tsx`) with a 16ms coalescing window so a burst of
events is one render (`context/sdk.tsx:66-78`). The reverse direction exists too - the server can drive the TUI
over `/tui/*`: append prompt, submit, open dialogs, toast, select session (`.../httpapi/groups/tui.ts:35-49`).

**Copy this.** Event-stream-into-local-store as the only client state model, plus the batching window.
Server-to-TUI commands are the missing half of remote control - the web/app should be able to type into the CLI
session, not only watch it. Flowy's TUI already reads `/api`; this is the same idea pushed back.

**Skip this.** That control channel is module-level singleton queues (`server/shared/tui-control.ts:11-12`) -
one TUI per server, no addressing. Two attached clients fight. Address channels by client id.

## Permissions and approval

**What it is.** The best-designed part of this codebase for your purposes.

**How it works.** A tool calls `ask`; rules evaluate last-match-wins over wildcards, defaulting to `ask`
(`permission/index.ts:28-38`); `deny` fails immediately, `allow` passes, otherwise the server publishes a
`permission.asked` event and **blocks on a `Deferred`** (`:98-107`). Any client resolves it with
`POST /permission/:id/reply` (`.../groups/permission.ts:31-42`). "Always" pushes a rule into the session's approved
set and auto-resolves every other pending request it now covers (`:145-166`); a reject cascades to the rest of the
session (`:129-138`). `Question` is the same machine for free-text - event plus `Deferred` plus reply
(`question/index.ts:87-131`). Two supporting pieces: shell commands are parsed with tree-sitter so each sub-command
becomes its own permission pattern, with path arguments outside the worktree raising a separate
`external_directory` request (`tool/shell.ts:263-290`); and the pattern "always" remembers is trimmed by a
per-command arity table (`permission/arity.ts:1-9`), so approving `git commit -m x` remembers `git commit`.

**Copy this.** Near enough all of it: ask = event + blocking future + reply endpoint, resolvable by whichever
client answers first. Remote approval with no extra machinery, and the one thing a phone client genuinely must
do. Take the arity table too - approval granularity is the whole UX.

**Skip this.** Approvals live in process memory, dropped on instance dispose (`permission/index.ts:54-61`), and
rules are never persisted. Flowy has an event DAG - write request and reply into it, so an approval survives a
restart and is auditable.

## Running several models at once

**What it is.** Subagents are child sessions, not child processes (`tool/task.ts:156-168`).

**How it works.** An agent definition carries its own model, temperature and permission ruleset
(`agent/agent.ts:35-56`); the child session takes the agent's model or inherits the parent's
(`tool/task.ts:181-184`), so two agents on two providers run concurrently inside one server against one event
log. Spawning is itself permission-gated on `subagent_type` (`tool/task.ts:119-129`), depth-limited (`:111-116`),
and the child's ruleset is the parent's *denies* plus the agent's own - parent allows do not inherit
(`agent/subagent-permissions.ts:14-26`). `background: true` returns immediately and notifies later
(`tool/task.ts:24-29`, behind an experimental flag at `:96-101`).

**Copy this.** Denies inherit, allows do not. Spawn as a permissioned action with the medium as the pattern -
that is your "pluggable mediums, selectable per spawn", and it lets a phone be asked "let this one run on grok?".

**Skip this.** Sync is deliberately single-writer, integer sequence, no causal clock
(`packages/opencode/src/sync/README.md:29-33`) - one device controls, the others replay. Do not trade flowy's
HLC merge down for that: "the human answers from the phone while an agent writes on the laptop" is two writers.

## The one recommendation

Make **every blocking human interaction one primitive**: append a request to the event DAG, block the agent on a
future keyed by its id, let any client - CLI, web, phone, another agent - resolve it by id through `/api`.
opencode has this twice (`permission/index.ts:98-107`, `question/index.ts:96-107`) and it is why its remote
clients are clients rather than viewers. In flowy it comes out strictly better, because the requests are signed
rows in a log that federates and survives a restart instead of a map in one process. Build permission asks,
questions, steers and handoff acceptance on that one primitive before building any second client, and the web
and mobile apps stop being ports of the CLI and become peers of it.
