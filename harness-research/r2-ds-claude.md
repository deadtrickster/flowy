# r2-ds-claude - DeepSeek Harness (`dsh`)

Source: `github.com/deepseek-ai/deepseek-harness`, cloned to `/tmp/dsh`, pinned at `47f943859` (`release(dsh):
0.1.0-rc.5`). TypeScript pnpm monorepo, ~270 packages, MIT, developer preview - "**THERE WILL BE
COMPATIBILITY-BREAKING CHANGES**" (`README.md:11`). All `file:line` relative to that checkout.

## Core areas

**Everything is a plugin.** `README.md:7`: "It uses an architecture where **everything is a plugin**, and is powered
by [Cordis]". Cordis is a DI/effect container - services live on `ctx.<key>`, plugin effects are fiber-scoped and
disposed with it, a deployment is a `cordis.yml` list of `{id, name, config}` rows. Every capability splits three
ways: Service Definition / Service Provider / Consumer (`packages/lsp/lsp/README.md:11-15`).

**Agent loop.** `packages/core/agent-loop/README.md:7` - "This is the only package in the harness that contains
concrete loop logic. Everything else is an abstract service or a plugin against extension points." Lifecycle is
session → turn → step; each step claims an inbox batch, runs `agent/pre-step`, opens `step/start`, assembles prompt +
tools, runs the `agent/request` waterfall over a frozen config seed, writes a `request/header`, then builds and
deep-freezes `GenerateOptions` (`packages/core/agent-loop/src/agent.ts:458-491`).

**Tool definition and pipeline.** `packages/core/tools/README.md:5`: register schema + executor, then every call runs
`tools/pre-execute` (allow/deny gate) → monotonic guards → `tools/execute` (around-dispatch wrapper for
timeout/retry/metrics) → `tools/post-execute` (inspect/replace result, attach context) → `finalizeContent` →
observe-only `tools/result`. Registration layer is the calling context's scope: plugin context = global, `agent.ctx` =
that agent alone. `ctx.tools.restrict()` masks tools per agent but is explicitly not security - "This is live
visibility composition, not an authority boundary" (`:22`). Presentation is per-agent switchable: `native` function
calling, `code` (one reserved `run_code` transport), or `both` (`:13`).

**Subagents (the provider registry).** `ctx.subagents` = `registerProvider`/`getProvider`/`list`/`start`/
`startContinuable`/`followup`/`interrupt`/`reportFrom`, with providers advertising start-time `capabilities`
(`outputSchema`, `depthLimit`, `toolFilter`, `persona`) so an unsupported request is rejected before child creation
(`packages/subagent/subagent/README.md:13-36`). Shipped providers are exactly the seed's "pluggable mediums" (each
quote from that package's `README.md:5`): `subagent-spawn-in-process` (fresh in-process `Agent`),
`subagent-fork-in-process` (seeded with the parent's completed turns), `subagent-acp` ("drives any Agent Client
Protocol agent"), `subagent-dsh-sdk` ("the child is a full peer harness"), `subagent-claude-code` ("invokes the
official Claude Agent SDK in the delegating Session's workspace"), `subagent-codex` ("starts the official `codex
app-server --stdio` command").

**Provider abstraction.** `ctx.llm` is the seam; `llm-deepseek` (hand-written wire client) and `llm-pi-ai` are
deliberate twins - the latter is the "design-verification twin of dsh-llm-deepseek"
(`packages/llm/llm-pi-ai/package.json:3`) over `@earendil-works/pi-ai` (`:45`). Routes are config, not code: "an
OpenAI-compatible gateway, a self-hosted server, or a provider newer than the installed catalog is configuration
rather than a code change" (`llm-pi-ai/README.md:5`).

**Config, permissioning, extensibility.** Per-call policy is the `tools/pre-execute` waterfall plus guards. Consent is
a separate channel-neutral seam - `ctx.approval.request()` → `allowed-once | rejected | cancelled | unavailable`,
which "fail[s] closed" and whose grant "applies only to the requested action", appending paired
`approval/asked`/`approval/decided` audit events while the model sees only the tool outcome
(`packages/interaction/user-approval/README.md:5,7`). Confinement is separate again: `bash-sandbox` fails closed with
`SANDBOX_UNAVAILABLE`, "never a silent unconfined run" (`packages/shell/bash-sandbox/README.md:9`). Extension is
native cordis events; Claude Code and Codex hook configs run only as a compatibility bridge - "**The bridge exists
only as a compatibility path for the mapped CC command-hook subset**"
(`packages/hooks/hooks-claude-code/README.md:7`).

## Lens A - caching (headline)

dsh treats prefix-cache stability as a *derived property of an architectural invariant*, not a feature. This is the
most transferable idea in the repo.

**The invariant.** `.agents/notes/implemented/architecture/2026-07-05-reconstructable-requests.md:17` -
"**Model-visible ⟺ durably referenced.** Anything that reaches a model request must be reconstructable from the
session log and the immutable content-addressed objects it references. The checkable consequence: anyone holding the
log, its referenced attachment objects, and the pinned code version reconstructs every loop request byte-for-byte."
Then `:19` - "Prefix-cache stability is corollary #1, not the headline: an append-only log projected by a per-node
pure function yields requests that are append-extensions of their predecessors whenever the header is unchanged -
**stability is emergent, not managed**." Corollary #2 is byte-exact replay; #3 is resume/fork with attributable drift.

**Mechanism.** History is `Session.deriveMessages()`, a cached per-event projection over deep-frozen messages -
"mutating logged history through a projection is unrepresentable (it throws)" (`:23`). Everything not history lives in
an `EpochHeader` - call config, rendered system prompt, tool schemas - written as a *full* `request/header` snapshot
with reason `initial`/`resume`/`change` (`:25`; `packages/core/agent-loop/src/agent.ts:458-469`). The built request is
`markAgentLoopRequest(deepFreeze({...}))` (`agent.ts:486`), so no listener can rewrite a request the log already
explains. A per-call mutable config was rejected: "a listener flips the model per call with zero accounting, silently
abandoning the provider cache this design exists to protect" (`:41`).

**Enforced, not conventional.** A companion registers with `ctx.invariants` and "independently rebuilds each loop
request through a fresh `Session`, so the live cache cannot vouch for itself" (`:31`). A with-key e2e proves the
provider actually caches: `packages/core/agent-loop/tests/request-cache.e2e.ts:93` asserts `cacheReadTokens > 0` on
every request after the first across a two-step tool turn plus a follow-up turn, with the system prompt sized so "the
shared request prefix comfortably spans the provider's cache-block granularity (64 tokens)" (`:24`).

**Steers and tool results do not bust the prefix.** Injected context is append-only: "`agent.inject()` and tool
`additionalContexts` enter the inbox for a later claim, while `agent/pre-step` returns context that must settle with
the current claimed batch. Each entered value is a durable sourced `user/message`, **paid once and prefix-cached
thereafter**" (`:50`). The open step is the reconstruction boundary - injection after the atomic claim joins the
*next* request rather than mutating this one (`:29`). Mutable runtime state (approval policy, cwd) is never
interpolated into the system prompt; it is a "cache-safe runtime-context snapshot" appended after retained history
*only when its text changed* - `RuntimeContextProjection.project()` returns `undefined` when `retained?.text ===
snapshot` (`packages/core/agent-loop/src/runtime-context.ts:64-75`;
`packages/interaction/user-approval/README.md:11`). What genuinely costs full price is enumerated and logged (`:51`):
compaction, a real prompt/tool/config change, or a process boundary with drift.

**Compaction reuses the warm prefix.** "Every compaction therefore paid full prompt-processing cost for the whole
replayed history twice" - the summarizer's own system prompt invalidated the entire cached prefix
(`.agents/notes/implemented/bug-fix/2026-07-21-compaction-summary-prefix-cache-reuse.md:9`). The fix replays the last
routed request's prefix and appends the directive as a trailing user message, so "only that instruction, and the
summary output, is uncached" (`packages/compaction/compaction-basic/README.md:156`). Compaction still owns its cost -
"Replacing rather than append-only. Each checkpoint invalidates reuse from the first replaced history token" (`:103`);
tool-result pruning is the same shape.

**Every package declares its cache effect, and CI checks it.** A process note mandates three ordered H4 fields per
Model Experience surface - `What the model sees`, `Token effect`, `KV Cache effect` - where "The cache field
distinguishes append-only growth, a stable repeated prefix, replacement of earlier tokens, and an independent model
request... 'Does not invalidate' means the package preserves an already-reusable prefix, not that a provider promises
a cache hit" (`.agents/notes/implemented/process/2026-07-12-package-model-experience-contract.md:15`).
`scripts/verify-package-readme-model-experience.ts:17,419` is the gate; 215 of 268 package READMEs carry a `#### KV
Cache effect` heading, e.g. `packages/core/system-prompt/README.md:69` "Prefix-stable while identity, persona,
variables, section text, and order render identically" (`:83`, same for tool schemas).

**Accounting and surfacing.** Four disjoint buckets - `uncachedInputTokens`, `outputTokens`, `cacheReadTokens`,
`cacheWriteTokens` (`packages/llm/token-meter/src/projection.ts:13-18`). `contextPressure.projectedTokens` is "what
the NEXT request's prompt would cost": the last provider sample plus heuristic repricing of only the surface delta, so
it "stays anchored to the provider while still reacting the moment a compaction shadows a span" (`:36-45`). The web UI
renders `Cache hit {percent}%` (`packages/client/ui-conversation/src/client/locales.ts:231`) as `cacheReadTokens /
(uncached + cacheRead + cacheWrite)` (`.../chat/StatsLine.tsx:109-123`).

**Provider-specific handling - and the one real gap.** DeepSeek's wire `prompt_tokens` *includes* hits, so the adapter
subtracts: `inputTokens: usage.prompt_tokens - (cacheRead ?? 0)` (`packages/llm/llm-deepseek/src/translate.ts:46-59`).
There is **no Anthropic-style `cache_control` anywhere in the repo**; the block-level `CacheHint` surface was
deliberately deleted - "neither adapter read `.cache`: DeepSeek prompt caching is automatic, so the adapters map
`prompt_cache_hit_tokens` OUT of responses without ever sending a hint IN. This was Anthropic-style `cache_control`
surface with no provider that could honor it"
(`.agents/notes/archived/simplification/2026-07-04-prune-producerless-vocabulary-variants.md:12`). dsh optimizes
purely for *automatic* prefix caching (DeepSeek, OpenAI); explicit breakpoint placement for Anthropic is delegated
wholesale to pi-ai.

## Lens B - tree-sitter and syntactic awareness

**dsh contains no tree-sitter, no `ast-grep`, no grammar of any kind.** A repo-wide search for
`tree-sitter|treesitter|web-tree-sitter|ast-grep` across `*.ts`, `*.json`, `*.md`, `*.toml` returns zero hits at
`47f943859`. That is a finding, not an omission.

Structural awareness lives at two other layers. **Semantic, via LSP:** `ctx.lsp` exposes "exactly four semantic
operations - `goToDefinition`, `findReferences`, `goToImplementation`, `hover` - and no generic JSON-RPC escape hatch"
(`packages/lsp/lsp/README.md:15`), surfaced as one read-only tool owning coordinate conversion and result limits
(`packages/lsp/tool-lsp/README.md:9`). **Syscall-level instead of syntactic, for shell:** dsh does not parse the
command string to decide permissions - "per-call allow/deny/ask policy is the `tools/pre-execute` waterfall"
(`packages/shell/tool-bash/README.md:49`). Confinement is `native/landlock-run`, "a self-restrict-then-exec Landlock
launcher (~300 lines of C11 over the raw kernel UAPI)... It installs a Landlock ruleset on itself and `exec`s the
wrapped command; the ruleset is inherited across `execve`... Fail-closed: if the kernel cannot enforce, it exits
without running the command" (`native/landlock-run/README.md:7`). The model requests escalation structurally -
`sandbox_permissions` plus a mandatory `justification`, strict widening checked at execution
(`packages/shell/tool-bash/README.md:24-25`) - rather than the harness inferring intent from syntax.

The trade: an opaque command under a kernel allow-list needs no grammar per shell and is safer than a parsed command
under a pattern allow-list; the cost is granularity - dsh cannot express "allow `git status`, ask for `git push`",
which is exactly what tree-sitter buys opencode. **For flowy:** take kernel confinement as the *authority* boundary
and add tree-sitter shell parsing only as a *UX* layer, never as the security boundary.

## Lens C - correctness feedback and loop detection

**Correctness inside the turn.** Read-before-edit with CAS: `fs-observation-policy` decides `fs/edit-intent` as
"Unseen → `FS_NOT_OBSERVED`; observed absent → `FS_NOT_FOUND`; observed present → `{version: vObserved}` as the CAS
basis" (`packages/fs/fs-observation-policy/README.md:37`), so a stale edit fails loudly instead of clobbering. Errors
reach the model as stable normalized strings enumerated verbatim in the package README
(`packages/shell/tool-bash/README.md:125`). Code Mode lets the model compose tool calls in real TypeScript/Python
through one `run_code` transport (`packages/core/tools/README.md:5,13`), and `ctx.invariants` is a configurable
registry of package-owned runtime checks a deployment can switch on
(`packages/runtime-diagnostics/invariants/README.md`). **Gap:** the LSP seam ships navigation only - no diagnostics
operation, no type errors returned as tool output, no test-runner-in-the-loop package anywhere. Biggest correctness
hole relative to opencode.

**Loop detection.** `dsh-repeat-tool-reminder` is "An advisory loop-breaker, not a model-facing tool"
(`packages/guard/repeat-tool-reminder/README.md:5`). Signal: chain key is `(tool name, canonical arguments)`,
canonicalized by deep key-sort + `JSON.stringify` (`:25`); default `thresholds: [3, 5, 8]` (`:13`), first a short
nudge, later ones naming the tool, run length, and truncated arguments. Two details worth stealing: "**Untracked calls
are transparent to the chain**... bookkeeping tools interleaved into a loop must not launder it" (`:27`), and
"**Denied calls count.** Detection sits on `tools/post-execute`, which also runs for calls a `tools/pre-execute`
listener denied - a model hammering a denied call is exactly the loop worth breaking" (`:28`). The intervention rides
`additionalContexts` into a sourced injected `user/message` - model-visible, attributable, reconstructable, "with no
new session event" (`:33`) - and append-only, so it costs no cache (`:55`, `:79`). Stated limits: exact-match only,
chains do not survive compaction or resume, and "**Advisory only** - escalating to `block` at a high threshold is not
implemented" (`:85-87`).

**Budgets and stalls.** `timeout-policy` arms a per-call cooperative deadline from the tool's own declared
`ToolDefinition.timeoutMs` and returns a structured `TOOL_TIMEOUT` (`packages/guard/timeout-policy/README.md:5`).
`llm-retry` applies per-provider retry through the `agent/request-error` waterfall - two retries for
`EMPTY_RESPONSE|RATE_LIMIT|SERVER|TIMEOUT|TRANSPORT`, 500ms→10s backoff, 10% jitter
(`packages/llm/llm-retry/README.md:5,7`). `maxGoalRounds` caps autonomous continuation, explicitly a "**Round cap, not
resource budget**" (`packages/goal/goal-round-driver/README.md:63`). At the loop level, though: "**No built-in turn
budget** - tool calls or steering continue the current turn; a policy that bounds runaway turns must cancel from an
existing lifecycle extension point such as `agent/turn-stopping`" (`packages/core/agent-loop/README.md:134`). No token
budget, no wall-clock cap, no step cap, no circuit breaker.

## The one recommendation

Adopt dsh's invariant verbatim as flowy's request contract: **a model request must be a pure function of the
append-only event log plus content-addressed objects, and nothing may mutate a built request.** flowy already has the
hard part - an append-only signed event DAG with typed artifacts - so `deriveMessages()` over that DAG plus an
`EpochHeader` event (model, rendered system prompt, tool schemas) buys prefix-cache stability, byte-exact replay, and
forkable sessions as corollaries of one rule rather than three features to maintain. Two cheap pieces make it real
rather than aspirational: the independent reconstructor that rebuilds each request from the log through a fresh
session so the live cache cannot vouch for itself, and a with-key e2e asserting `cacheReadTokens > 0` on every request
after the first. Then enforce it socially the way dsh does - a `KV Cache effect` paragraph in every plugin's README,
checked in CI. This matters more for flowy than for dsh: the built-in cross-agent chat fabric is the natural
prefix-buster, and the rule that fixes it is that a message arriving mid-turn joins the *next* step's claimed batch as
an appended `user/message`, never rewriting the current request.
