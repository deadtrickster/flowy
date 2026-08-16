# r2-oc-claude - opencode

Source: `github.com/sst/opencode` cloned to `/tmp/opencode`, commit `4643e65` (2026-08-14). All
`file:line` are in that clone, rooted at `packages/`. The repo is mid-migration: `opencode/` is the
live app, `core/` a partial V2 rewrite - where they disagree I say so.

## Core areas

**Client/server split.** `opencode serve` starts a headless HTTP server and nothing else;
`opencode/src/cli/cmd/serve.ts:10` notes "Server loads instances per-request via
x-opencode-directory header - no need for an ambient project InstanceContext at startup", so one
server serves many project directories. Auth is HTTP basic from env
(`opencode/src/server/auth.ts:19`, `OPENCODE_SERVER_PASSWORD`); serve prints "Warning:
OPENCODE_SERVER_PASSWORD is not set; server is unsecured." (`serve.ts:16`). Effect `HttpApi` with
generated OpenAPI (`opencode/src/server/server.ts:68`) generates the JS SDK; clients are co-equal -
TUI, `opencode attach <url>` (`opencode/src/cli/cmd/attach.ts:8`), web, desktop, Slack - with mDNS
on the LAN (`opencode/src/server/mdns.ts`). flowy's shape, but one shared password vs ed25519.

**Provider catalog.** Models come from models.dev, mapped by `fromModelsDevModel` /
`fromModelsDevProvider` (`opencode/src/provider/provider.ts:1212`, `:1265`) then merged with config.
Cost (with context tiers, `opencode/src/session/session.ts:383`), limits, capabilities and
`variants` ride the catalog entry; the SDK package is npm-resolved per provider (`model.api.npm`).
The medium is data, not code. MCP: stdio, SSE or StreamableHTTP (`opencode/src/mcp/index.ts:212`).

**Plugins.** Typed hooks, `plugin/src/index.ts:222-334`: `event`, `config`, `tool`, `auth`,
`provider`, `chat.message`, `chat.params`, `chat.headers`, `permission.ask` (auto allow/deny),
`command.execute.before`, `tool.execute.before/after`, `shell.env`, `tool.definition` (rewrite a
tool's description and schema), plus experimental transforms of messages, system prompt and
compaction.

**LSP / TUI.** Passive: after every `edit`/`write`/`apply_patch` diagnostics are appended to that
tool's output (Lens C). Active: a permission-gated `lsp` tool (`opencode/src/tool/lsp.ts:11-21`,
`:55`) - `goToDefinition`, `findReferences`, `hover`, `documentSymbol`, `workspaceSymbol`,
`goToImplementation`, `prepareCallHierarchy`, `incomingCalls`, `outgoingCalls`. The TUI is now
TypeScript/solid on opentui (`tui/package.json`), not the old Go client, and an SDK consumer.

**Permissions (event + deferred + reply).** `opencode/src/permission/index.ts` is the whole model.
`ask` evaluates each pattern against the merged ruleset (`:73`), returns `DeniedError` on deny
(`:76`), skips on allow, else creates a `Deferred`, publishes `Event.Asked` and blocks the tool
fiber on it (`:98-106`). Any client answers over HTTP: `POST /permission/:requestID/reply`
(`opencode/src/server/routes/instance/httpapi/groups/permission.ts:31`), resolving the deferred
(`:142`); on `"always"` it pushes the request's `always` patterns into the session ruleset and
sweeps every other pending request in that session, auto-resolving any now allowed (`:145-166`). A
rejection cascades to all pending requests in the session (`:129-138`) and can carry a message,
becoming a `CorrectedError` (`:125`) - "no, do it this way" is a first-class reply, not a cancel.
Matching is glob (`core/src/util/wildcard.ts:3`) with one quirk: a pattern ending in ` *` also
matches the bare prefix (`:12`), so `git status *` covers `git status`.

**Multi-model subagents.** An agent is a config record with its own `model {providerID, modelID}`,
`prompt`, `permission` ruleset, `steps` cap and `options` (`opencode/src/agent/agent.ts:34-55`).
`task` spawns a child session on the subagent's model, falling back to the parent's
(`opencode/src/tool/task.ts:181`), with `background=true` plus completion notification (`:25-29`)
and `task_id` to resume a prior subagent session (`:47`). Child permissions are derived, not
inherited: only the parent's denies and `external_directory` rules carry down, plus default denies
on `task`/`todowrite` so subagents cannot recursively spawn
(`opencode/src/agent/subagent-permissions.ts:14-27`).

## Lens A - caching

*Where breakpoints go.* `opencode/src/provider/transform.ts:359` `applyCaching` marks exactly four
messages: the first two `system` and the last two non-system (`:360-361`). Provider dialects are a
lookup table (`:363-382`): `anthropic`/`openrouter`/`alibaba` get `cacheControl:{type:"ephemeral"}`,
`bedrock` `cachePoint:{type:"default"}`, `openaiCompatible` snake-case `cache_control`, copilot
`copilot_cache_control`. Anthropic and Bedrock take the marker at *message* level, everything else
on the last content part (`:385-404`). Applied only for Anthropic-family models (`:472-485`) and
skipped when the provider caches automatically - `usesAnthropicAutomaticCaching` (`:469`). OpenAI
and DeepSeek get nothing, which is correct: their caching is automatic and prefix-based.

*Shaping the prefix to fit two breakpoints.* The sharpest engineering here is
`opencode/src/session/llm/request.ts:56-78`. The system prompt is built as one joined string (agent
or model-specific prompt, then env, instructions, MCP instructions, skills), plugins may transform
it, and if that leaves more than two system strings they are collapsed back to two:
`system.push(header, rest.join("\n"))` (`:74-78`). Two system messages, two system breakpoints -
the whole system prompt is always covered.

*Stable vs not.* The env block (`opencode/src/session/system.ts:72-83`) carries cwd, worktree,
platform and `Today's date: ${new Date().toDateString()}` - day-granularity, so stable within a
session but it silently busts the system prefix across midnight. Reminders (plan-mode,
build-switch) are appended as synthetic parts on the **last user message**
(`opencode/src/session/reminders.ts:29-35`), never spliced into history - prefix-safe by
construction. History is rebuilt from the DB each step through `MessageV2.toModelMessagesEffect`,
so replay is deterministic. What busts it: compaction replaces history with a summary, and opt-in
pruning (`opencode/src/session/compaction.ts:272-315`) protects the last `PRUNE_PROTECT = 40_000`
tokens of tool output (`:29`) and rewrites older completed tool results in place -
`part.state.time.compacted = Date.now()` (`:311`) - rendering as
`"[Old tool result content cleared]"` (`opencode/src/session/message-v2.ts:294`). A mid-prefix
mutation: it saves context and destroys the cache below the cut in one move.

*Accounting.* Cache read/write are normalized from a mess of provider shapes
(`session.ts:347-361`: ai-sdk, anthropic, vertex, bedrock, venice), subtracted out of input (`:366`)
and priced separately (`:394-400`). Surfacing is weak: the TUI folds
`input + output + reasoning + cache.read + cache.write` into one "context" number
(`tui/src/component/prompt/index.tsx:271`). Cost is right, but there is no hit-rate anywhere - a
user cannot tell a 90%-cached turn from an uncached one.

*For flowy:* collapse the system prompt to a fixed small number of segments so a fixed number of
breakpoints always covers it, and treat any in-place edit of a stored event's rendered text as
cache-invalidating. The DAG gives deterministic replay free; the risk unique to flowy is a
permission-filtered read projecting *different* rows into the prefix for the same session.

## Lens B - tree-sitter and syntactic awareness

**Shell permission parsing is the real one.** `opencode/src/tool/shell.ts` parses every command
before running it. Three wasm grammars load lazily (`shell.ts:311-336`): `web-tree-sitter` core,
`tree-sitter-bash`, `tree-sitter-powershell`, pinned at `0.25.0` / `0.25.10`
(`opencode/package.json:143-144`). `parse` (`:257`) picks the grammar by shell; the tree is
scope-managed with an explicit `tree.delete()` finalizer (`:622-624`) - wasm trees are not GC'd.
What the AST buys, in `collect` (`:378-414`):

1. **Enumerate the real commands.** `commands()` is `node.descendantsOfType("command")` (`:124`).
   `echo foo && echo bar` yields two permission patterns, not one
   (`opencode/test/tool/shell.test.ts:259-260`); pipelines, `;` lists, subshells and an `if` body
   (`shell.test.ts:282-283`) all decompose. A regex harness either over-approves the whole string
   or mis-splits on a `&&` inside quotes.
2. **Reject things that only look like commands.** `Write-Output ('a' * 3)` must not produce a
   pattern `a * 3` or an always-rule `a *` (`shell.test.ts:704-705`). A token-based parser would
   hand the user a rule that then wildcard-matches unrelated commands. This is the case that makes
   structural parsing a security property, not a nicety.
3. **Typed argument extraction.** `parts()` (`:91`) keeps only `command_name`, `command_name_expr`,
   `word`, `string`, `raw_string`, `concatenation` and drops `command_argument_sep` and
   `redirection` (`:99`) - `> /etc/passwd` is not mistaken for a positional path, while
   `source(node)` climbs to `redirected_statement` (`:119-121`) so the pattern *shown* to the user
   still includes the redirect. PowerShell parameters arrive as `command_parameter` nodes, letting
   `pathArgs` (`:188-218`) separate switches (`-Recurse`) from flags that consume the next token
   (`-Path`, `-Destination`, `-LiteralPath`) - `:65-66`.
4. **Filesystem-escape detection.** For a file-touching command (`FILES`/`CMD_FILES`, `:29-64`)
   each path argument is unquoted, `~`/`$HOME`/`$env:` expanded (`:154-160`), truncated at the
   first glob metacharacter (`prefix`, `:181`), skipped if dynamic (`$(...)`, backticks, `:174`),
   resolved, and if outside the project raises a *separate* `external_directory` permission with a
   directory glob (`:266-279`). Two independent questions - may you run this, may you touch there -
   from one parse.

**Arity: turning a parse into a reusable rule.** The always-pattern is not the literal command.
`BashArity.prefix(tokens)` (`opencode/src/permission/arity.ts:1-9`) does longest-prefix lookup in a
~140-entry arity table and returns that many tokens; shell.ts appends `" *"` (`shell.ts:409`).
`git` is 2, so `git checkout main` → `git checkout *`; `npm run` is 3, so `npm run dev` →
`npm run dev *`; unknown commands default to 1 token; flags never count (`arity.ts:14`). PowerShell
`Remove-Item -Recurse ./x` yields `Remove-Item *` and specifically not `Remove-Item -Recurse *`
(`shell.test.ts:312-313`). This is what makes "always allow" usable - the rule generalizes at the
granularity a human means. Provenance is honest: the table was LLM-generated from a prompt kept
verbatim in the file (`arity.ts:11-23`).

**Elsewhere.** The TUI ships ~20 more grammars as wasm URLs with nvim-treesitter highlight and
locals queries (`tui/src/parsers-config.ts:6-`) - presentation only. Symbol navigation is LSP, not
tree-sitter. Edits are **not** AST-aware: `edit`/`apply_patch` are string/patch operations with LSP
diagnostics as the after-the-fact check.

**The honest negative.** The V2 core rewrite dropped it. `core/src/tool/bash.ts:65-67` carries the
TODOs verbatim - "Port tree-sitter bash / PowerShell parser-based approval reduction", "Port
BashArity reusable command-prefix approvals", "Replace token-based command-argument
external-directory advisories with parser-based detection" - and today tokenizes with a regex,
`shellTokens` (`:77`), with a test asserting those TODOs still exist
(`core/test/tool-bash.test.ts:424`). Tracked-as-missing: load-bearing enough to gate on.

## Lens C - correctness and loop detection

**Correctness feedback inside the turn.** Diagnostics come back as tool output, not a side channel.
After `edit` the file is touched, the LSP queried, and diagnostics appended to the tool result as
"LSP errors detected in this file, please fix:" plus a `<diagnostics file="...">` block of
`ERROR [line:col] message` lines (`opencode/src/tool/edit.ts:197-201`, formatter
`opencode/src/lsp/diagnostic.ts:17-26`). `write` also reports diagnostics in *other* files, capped
at `MAX_PROJECT_DIAGNOSTICS_FILES = 5` (`opencode/src/tool/write.ts:18`, `:79-90`) - an edit that
breaks a downstream caller comes back in the same tool result that made it; `apply_patch` likewise
(`apply_patch.ts:265-300`). The client debounces 150ms and waits up to 5s/10s for diagnostics to
settle (`opencode/src/lsp/client.ts:13-16`) - the model waits for the compiler rather than guessing.
Truncated bash output keeps a full log on disk whose path is returned (`shell.ts:579`), and timeouts
return instructions, not errors ("retry with a larger timeout value", `shell.ts:564`).

**Loop detection: `doom_loop`.** `opencode/src/session/processor.ts:29` `DOOM_LOOP_THRESHOLD = 3`.
On every `tool-call` the processor takes the last 3 parts of the assistant message and checks
whether all three are tool parts with the same tool name and byte-identical input
(`JSON.stringify(part.state.input) === JSON.stringify(input)`, `:356-366`). If so it does not abort
- it raises a permission request with `permission: "doom_loop"` (`:372-379`), blocking on the same
deferred machinery as any approval and surfacing in every client as "Continue after repeated
failures / This keeps the session running despite repeated failures."
(`opencode/src/cli/cmd/run/permission.shared.ts:111-117`). Default `doom_loop: "ask"`
(`opencode/src/agent/agent.ts:121`), settable to allow/deny like any permission key.
**The intervention is a human, not a heuristic** - which fits a harness whose permission layer is
already async and remote-answerable.

**Budgets and stalls.** Per-agent `steps` cap (`agent.ts:54`, `opencode/src/session/prompt.ts:1178`);
on the last step an assistant message carrying `MAX_STEPS_PROMPT` is appended to the request
(`prompt.ts:1281`) so the model wraps up rather than being cut off. Shell default timeout 2 minutes
(`shell.ts:347`). Provider errors retry with jittered exponential backoff, `RETRY_MAX_RETRIES = 5`,
honouring `retry-after`/`retry-after-ms` (`opencode/src/session/retry.ts:26-31`, `:47-60`) against a
regex list of retryable conditions (`:33-40`). Overflow triggers auto-compaction
(`opencode/src/session/overflow.ts:22-33`, fired at `prompt.ts:1163-1166`), and content-filter
finishes surface as errors instead of a silently idle session (`prompt.ts:1297`). Missing: only
byte-identical detection, no cross-message window (the check lives inside one assistant message, so
a loop re-entering through a new turn resets it), and no wall-clock or token budget per session.

## Recommendation

**Put a tree-sitter shell parser plus an arity table on flowy's command door, and make the
resulting rule a signed artifact in the DAG.** opencode's parser earns its keep exactly where a
regex fails and fails *open*: `a && b` decomposing into two independently-approved commands,
`('a' * 3)` not becoming an always-rule `a *`, `> /etc/passwd` not read as an argument, `-Path x`
distinguished from `-Recurse`. flowy is worse-exposed than opencode, because an "always allow" here
is not a process-lifetime toggle - it is a grant that federates, and a mis-parsed grant propagates.
Emit the pattern set (per-command patterns plus the arity-derived `always` prefix) as the permission
request payload, sign the grant, scope it exactly as you scope artifacts. Second, steal `doom_loop`
verbatim: 3 identical consecutive tool calls raises a permission request rather than killing the run
- on flowy that lands in the chat fabric, where any human or agent can unstick it from web or phone.
