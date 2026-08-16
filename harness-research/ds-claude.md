# DeepSeek Harness (`dsh`) - research for the flowy harness

Source: `github.com/deepseek-ai/deepseek-harness`, cloned to `/tmp/dsh` at commit `47f9438` (0.1.0-rc.5,
developer preview). Official DeepSeek AI repo, MIT, TypeScript, ~2085 non-test `.ts` files over 50 package
groups. Found on the first web search - no substitute needed. Every claim below cites that checkout.
It is built on Cordis, a plugin/DI framework: "Every part of the product is a plugin, including the model adapter,
the tool registry, the session log, and the agent loop itself" (`docs/architecture.md:11`). That is both the
design and the risk.

## 1. Agent loop

`ReactLoopAgent` (`packages/core/agent-loop/src/agent.ts:64`) drives one session; a *step* is one model
request plus its tool calls, a *turn* is zero or more steps (`agent.ts:246`). The loop holds no
conversation array - every request is projected from the append-only session log via
`session.deriveMessages()` (`agent.ts:341`). The invariant is stated outright: "Model-visible means logged.
Anything that reaches a model request must be reconstructable from the log, and a runtime invariant asserts
it" (`docs/architecture.md:96`). Durable events are `turn/*`, `step/*`, `user/message`, `assistant/chunk`,
`assistant/message`, `tool/call`, `tool/result` (`agent.ts:255,279,283,349,381`; `tool-calls.ts:263,281`).
Raw chunks are kept beside the assembled message, linked by `sourceEventSeqs`, so replay and UI stay
byte-faithful (`agent.ts:349,389`).

Input arrives through one `Inbox` with three verbs differing only in when the message is claimed: `followup`
-> next turn, `steer` -> next step + wake, `inject` -> next step, no wake (`agent.ts:122-132`). Extension
points are waterfalls: `agent/pre-step` rewrites or rejects claimed messages (`:234`), `agent/request`
proposes provider/model/effort (`:438`), `agent/request-error` decides retry (`:357`), `agent/turn-stopping`
can keep a turn alive (`:296`).

**Copy.** Log-derived context with that invariant - flowy already has an append-only event DAG, so it is
nearly free and fork/resume/replay/audit fall out of it. The three inbox verbs. And `request/header` +
`request/context` events recording exactly which provider, model, system prompt and tool set produced each
assistant message (`agent.ts:458-483`): provenance for the model call, on a repo that already signs rows.
**Skip.** The phase state machine (`Phase` union `agent.ts:38`, `wakeDriver` latching `:172`,
`raceAbortCall`/`releaseAbandoned` `agent-loop/src/index.ts:109`) - ~200 lines of cancellation bookkeeping
that exists only because dsh survives plugin hot-reload mid-turn.

## 2. Tool definition

`ToolDefinition` (`packages/core/tools/src/index.ts:222`) is name, description, parameter schema, a
mandatory `output` contract, and `execute(args, exec)`. The distinctive part is `ToolOutputDefinition`
(`:212`): the body returns *only a canonical JSON value*
validated against `output.schema`, and a separate pure `render(args, value)` projects it into model-facing
content. Two more pure projections, `presentCall(args)` and `presentResult(args, result)` (`:279,:287`),
describe the pending and completed UI card - "pure and side-effect-free: a UI may call it during live
streaming AND a session-log replay". One author writes one thing and gets model text, a live card, and a
replayed card that provably agree.

Execution is `tools/pre-execute` (policy) -> `tools/execute` (around-dispatch: timeout, retry, metrics) ->
`tools/post-execute` (result rewrite) -> `tools/result` emit (`:152,163,175,197`). Wrappers may replace
`exec.signal` but not call identity, and the registry re-fuses the caller signal so a wrapper cannot detach
cancellation (`:157,391`). Metadata deliberately hidden from the model: `timeoutMs` - "it is NEVER sent to
the model - `schemas()` whitelists only name/description/parameters" (`:255`) - and
`isConcurrencySafe(args)` (`:269`).

**Copy.** The value/render split and the two presentation projections. flowy's console, TUI and MCP are
three clients of one API; pure render functions mean all three render and replay identically with no
per-client tool code.
**Skip.** Code Mode (`packages/core/tools/src/code-mode.ts:20`) - the model writes a TypeScript program
calling `await tools.name(args)` and only what it prints returns. Clever, but it needs a worker-thread
runtime, a nested scheduler, deterministic sub-call ids and its own event pair
(`packages/core/tools/src/types.ts:40,56`). Second system.

## 3. Subagents and parallel work - the most relevant part

**Within a step.** `executeToolCalls` (`agent-loop/src/tool-calls.ts:59`) classifies each call; `parallel`
calls join a rolling pool bounded by `maxParallelToolCalls` (default 10, `constants.ts:6`), `exclusive`
calls run alone and form a barrier (`tool-calls.ts:88-91`). Dispatch overlaps but *results commit in model
order* - `commitReady` advances only across contiguous slots (`:146-159`). Calls are re-classified before
start, so registering an exclusive tool mid-step creates a barrier (`:203`). On abort, unstarted calls get
synthetic error results so the transcript stays valid (`:249`).

**Across agents** - the seed's requirement 2, almost exactly. `ctx.subagents` is the one seam where
"multiple provider implementations coexist in one context, registered by name"
(`docs/subsystems/subagent.md:5`); every other seam is single-provider. `SubagentProvider`
(`packages/subagent/subagent/src/types.ts:285`) is four members: `name`, `capabilities`,
`inheritsParentContext`, `start(request)`, plus an optional `prepareContinuable` whose *presence is the
capability*.

Shipped providers: `spawn-in-process`, `fork-in-process`, `dsh-sdk`, `acp` (Agent Client Protocol), `codex`,
and `claude-code` - the last invokes `@anthropic-ai/claude-agent-sdk`'s `query()` and puts the real Claude
Code CLI process under the shared subprocess owner (`subagent-claude-code/src/run.ts:1-17`, registered at
`subagent-claude-code/src/index.ts:111`).

Capability negotiation fails loud, never degrades silently: `{outputSchema, depthLimit, toolFilter,
persona}`, and a request needing one the provider lacks is rejected
`SubagentError('UNSUPPORTED_CAPABILITY')` before `start` (`docs/subsystems/subagent.md:13`).
Misconfiguration fails at *mount*: a numeric `maxDepth` on a provider without `depthLimit` throws when the
tool plugin loads (`tool-subagent/src/index.ts:285-289`). The model-facing surface is one tool *per
configured provider*, not one tool with a provider argument - config takes `provider: string` (`:31`), the
tool mounts when that provider appears (`:440`), and its description is derived from
`provider.inheritsParentContext` so the wording is truthful about whether the child sees the parent's
history (`:211`).

**Copy.** All of it: the provider shape, capability flags with fail-loud rejection, mount-time validation,
one tool per provider with provider-derived wording, and out-of-process providers that shell out to a
competitor's CLI. Plus the ordered-commit scheduler.
**Skip / go further.** dsh's "chat fabric" is not one. `send_message` delivers to a background *subagent by
id*, "becomes the subagent's next turn", "returns no answer from the subagent - only confirmation that the
message was delivered" (`tool-subagent-control/src/index.ts:27-33`); `list_agents` covers children or
descendants only, and `send_message` works only at depth-1 (`list-agents.ts:104`). A mailbox down a tree,
not peer-to-peer - requirement 1 is strictly more than dsh has. Take the mailbox semantics (queued as next
turn, delivery-confirmed not answer-bearing, authoritative check at send) and put them on flowy's scoped
artifact/event fabric instead of a parent-child edge.

## 4. Provider / model abstraction

`ctx.llm.registerAdapter(providers, adapter)` (`packages/llm/llm/src/index.ts:338`) - one adapter serves a
list of route names; the DeepSeek adapter registers the single route `deepseek-official`
(`llm-deepseek/src/index.ts:47,256`). A call is `{provider, model, ...}` (`llm/src/call-config.ts:23`)
resolved by `prepareCall` (`llm/src/index.ts:779`) into a `PreparedLlmCall` carrying the adapter's defaults,
context window and retry policy. The loop binds the stream to that exact prepared call -
`preparedCall?.stream(request) ?? ctx.llm.stream(request)` (`agent.ts:345`) - so the adapter that resolved
the defaults is the one that runs. `adapterDefaults` marks which fields the adapter supplied rather than the
user, and `requestProposal` (`agent.ts:55`) strips exactly those before the next step's config is proposed,
so an adapter default never fossilizes into user intent across a model switch. Cross-cutting behavior rides
alongside as siblings: `llm-retry`, `token-meter`.

**Copy.** `{provider, model}` as the dispatch unit, `prepareCall` returning a bound handle, and
`adapterDefaults` - subtle, and you will regret not having it the first time a user switches model
mid-session. **Skip:** nothing, this layer is clean.

## 5. Config and permissioning

Boot composes an ordered plugin tree: bundles in profile order, then the profile's `cordis.patch.yml`, then
the home-level one, then `--patch` (`docs/architecture.md:27`); `dsh --profile web --dump-config` prints the
tree you actually boot (`:32`). Separately `ctx.settings` is the *user* layer: schema defaults, then the
registrant's composition `base`, then the user section (`packages/settings/settings/src/index.ts:2-5,104`).
A descriptor exposes all three so a form can mark what the user overrode and what reset returns to
(`:474-481`); `role('secret')` fields are stripped for wire consumers (`:95`).

Permissioning is three independent things, deliberately not one:

- `ctx.approval` - a waterfall seam. `PreToolDecision` includes `{kind: 'ask', reason?}`
  (`tools/src/index.ts:591`), resolved opportunistically through `ctx.get('approval')`, and it **denies when
  no answerer exists** (`:1693-1697`; "missing answerers fail closed",
  `packages/interaction/user-approval/src/index.ts:2-3`). Outcomes are `allowed-once | rejected | cancelled
  | unavailable` (`:82`) - no allow-always; grants apply only to the requested action. Policy is per-session
  and durable via an `approval/policy` event (`:142-146`).
- `ctx.sandbox` - `read-only | workspace-write | danger-full-access`
  (`packages/sandbox/sandbox/src/index.ts:29`), enforced by wrapping argv before spawn (`confine(argv,
  policy)`, `:175`), with a typed `SANDBOX_UNAVAILABLE` rather than a silent downgrade when no backend works
  (`:119-134`).
- `ctx.permissionPresets` - the user-facing bow: `workspace-write` = workspace-write + ask,
  `danger-full-access` = danger-full-access + never
  (`packages/interaction/permission-presets/src/index.ts:168-169`). The preset choice is recorded as its own
  durable `permission/preset` event *in addition to* both knob writes, "to preserve user intent when two
  presets share a bundle" (`:2-6,50`).

Tool visibility is scoped and a global restriction is refused: `tools.restrict()` throws unless called on an
agent-scoped context, because "a context-global restriction would mask every agent"
(`tools/src/index.ts:1071-1074`).

**Copy.** Fail-closed approval when no answerer is connected - for a harness whose approver may be a phone
that is asleep, that is the only safe default. The two-knobs-plus-preset structure with the preset recorded
separately. Scoped-only tool restriction. The three-layer settings descriptor, which is what lets web and
mobile render one settings form. **Skip:** nothing is wrong, but dsh has no allow-always grant at all - too
strict for a personal harness. If you add one, make it a durable scoped artifact, not process memory.

## 6. Extensibility

Registration uniformly returns a disposer - `tools.register` (`tools/src/index.ts:1037`),
`subagents.registerProvider` (`subagent/src/index.ts:369`) - and unloading a plugin unwinds its effects
(`docs/architecture.md:11`). Cross-cutting concerns attach as listeners, not core patches:
`packages/guard/timeout-policy` is a `tools/execute` wrapper, and a per-agent `Scope` means agent-scoped
listeners see only that agent's calls (`tools/src/index.ts:159`). Skills are filesystem `SKILL.md` files
with YAML frontmatter requiring `name` and `description`, warned-and-ignored when malformed rather than
fatal (`packages/skill/skill-filesystem/src/index.ts:672,807-813`). And dsh runs *unmodified Claude Code and
Codex hooks* through bridge plugins mapping foreign payloads onto its own extension points, honouring
everything except `updatedInput` (`packages/hooks/hooks-claude-code/src/index.ts:1-11`) - a cheap way to
inherit somebody else's ecosystem.

**Copy.** Disposer-returning registration everywhere, and the Claude Code hook format as flowy's hook
format. **Skip.** Cordis itself. dsh pays for total pluggability with a `docs/` tree holding a module graph, a
capability-seam graph, a config catalog, an event producer/consumer map, a glossary, and a `postmortem/`
directory whose first entry is a default export silently dropping plugin `inject` metadata
(`docs/postmortem/0001-acp-default-export-drops-inject.md`, cited from
`packages/subagent/subagent-acp/src/index.ts:5`). That is the real cost of "no privileged core". flowy is
one person's harness - a Go single binary with a SQL-enforced permission model - and a plugin DI container
would cost more than it returns.

## The one recommendation

**Build `SubagentProvider` first, and make the chat fabric a peer network rather than dsh's parent-child
mailbox.**

The registry (`subagent/src/types.ts:285`) is the piece dsh got right that nothing else in this
space has: four members, name-keyed coexistence, capability flags checked before dispatch with a loud typed
rejection, validated at mount rather than at call. It is what makes `subagent-claude-code` - a plugin that
shells out to Anthropic's CLI - a 290-line file rather than a fork. Requirement 2 of the seed is that
interface.

But adopt it knowing where dsh stops. Its providers return a run up a tree edge, and its inter-agent
messaging is depth-1 parent-to-child with no answer channel (`tool-subagent-control/src/index.ts:27-33`,
`list-agents.ts:104`). flowy already has what dsh lacks: signed, scoped, permission-filtered artifacts over
an event DAG that federates. So invert the composition - don't hang messaging off the spawn tree, hang the
spawn tree off the messaging. A provider's job becomes *start a participant and give it an identity on the
fabric*; after that, parent, child, peer, human on a phone, and an agent in another VM are all just
addresses. That gets requirements 1 and 2 out of one mechanism, and 3 for free, because a remote client and
a subagent are then the same kind of thing to the API.
