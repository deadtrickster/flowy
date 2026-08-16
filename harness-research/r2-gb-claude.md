# r2-gb-claude - xAI grok-build

`git clone https://github.com/xai-org/grok-build` → `/tmp/grok-build`, HEAD `5163763`,
`SOURCE_REV` `84ae1223e57a5048afb570d74d45c051fa604982`; 2716 Rust files, 78 crates. Paths are
relative to that clone, with `crates/codegen/xai-grok-` prefixes abbreviated: `sh/` = `shell/src/`,
`st/` = `sampling-types/src/`, `ws/` = `workspace/src/`, `docs/` = `pager/docs/user-guide/`.

## Core

**One protocol, many transports.** `docs/15-agent-mode.md:3`: "Agent mode runs Grok as a
long-lived server that clients talk to over ACP (JSON-RPC)". Four transports carry identical
frames - "Agent options apply to every transport (`stdio`, `serve`, `headless`, `leader`)"
(`:57`): stdio for IDEs, `serve --bind` for a local WebSocket, `headless --grok-ws-url wss://…`
for the internet relay (`:88-92`), `leader` for a machine-local Unix socket. No transport parses
the payload - leader IPC frames are `ClientMessage::Acp { payload: String }` behind a 4-byte
length prefix, 64 MB cap (`sh/leader/protocol.rs:300-302`, `:8`, `:22`, `:44`). Grok's own extensions
ride the same channel under an `x.ai/` prefix - fs, git, worktree, terminal, session fork,
rewind, compaction, auth (`docs/15-agent-mode.md:144-160`) - and an `on_meta` hook builds a
`tracing::Span` from ACP `_meta` so one trace crosses transports (`…/xai-acp-lib/src/gateway.rs:17`).

**Leader process.** One leader per machine owns agent state; clients are thin ACP peers
(`sh/leader/mod.rs:1-32`, socket `~/.grok/leader.sock`, `connect_or_spawn` `:38-45`). Modes
`Headless` ("uses websocket relay") and `Stdio` ("uses local IPC") (`protocol.rs:112-118`);
registration carries `ClientCapabilities` (`yolo_mode`, `auto_mode`) injected into `session/new`
`_meta` (`:128-137`). Routing: request ids namespaced per client with `|` so clients cannot
collide (`sh/leader/server.rs:37-39`, `:289-302`); a session has many `session_subscribers` and one `session_driver`, transferred to a
survivor on disconnect and evicted when the last leaves (`:1698-1729`); an in-flight
`session/load` buffers live notifications, and on overflow "correctness of the transcript is
preserved by the client's eventId dedup" (`:41-46`).

**Relay as frame forwarder.** Inbound text reaches the agent verbatim - the only inspection is a
JSON-RPC `error.code == -32000` auth check and reading `method` for a log line, then
`to_agent_tx.send(trimmed_end.to_string())` (`sh/agent/relay.rs:512-547`). Reconnect is 1s→60s
doubling backoff (`:41-43`, `:252-360`) plus a read-liveness deadline of 4x the 15s keepalive -
without it a half-open connection means "sessions stay bricked until the process is killed"
(`:20-30`). Private `RelayConfig` fields make "no relay without a session bearer ... a
compile-time guarantee" (`:48-70`). Content is not relay-owned - "Local disk remains the source
of truth", plus a `relay_sync.json` cursor for offline resilience (`sh/relay/mod.rs:5-12`).

**ApiBackend capability predicates.** `ChatCompletions` / `Responses` / `Messages`
(`st/types.rs:1013-1021`), with every behavioural difference a named predicate:
`supports_native_schema()` ("The Messages API does not (a schema there blocks tool use)"),
`requires_reasoning_strip()` ("Only the Messages API rejects thinking blocks sent without a
top-level `thinking` config"), `forwards_prompt_cache_key()` ("Only the Responses mapping sends
it, so a key set elsewhere is inert") (`st/types.rs:1026-1042`). A test walks each backend's real
wire mapping and asserts the predicate agrees, because "a key that never reaches the wire looks
like a 0% cache hit, not a bug" (`st/conversation.rs:2419-2459`). Callers consume the predicate -
structured output picks native schema vs a forced `StructuredOutput` tool (`sh/…/turn.rs:2094-2113`).

**Subagents and personas.** TOML definitions from an xAI-published bundle of "personas, roles,
agents, skills" (`crates/codegen/xai-grok-bundle/src/lib.rs:1`, `:310`) or found locally;
`AgentDefinition` carries prompt mode, toolset, `capability_mode`, `permission_mode`, skills
(`…/xai-grok-agent/src/config.rs:740-780`). Children are real sessions that "share the parent's
hunk tracker, filesystem, terminal, and env" (`sh/agent/subagent/mod.rs:8-12`),
started `New`, `Forked` (parent history as `<background_context>`) or `Resumed` from a peer's
transcript (`:50-64`). Model is per spawn, auto-compact resolves against the subagent's model
(`:66-80`), `max_turns` is definition-then-parent (`:1741-1747`).

**Interjection envelope.** A mid-turn message from any client is `x.ai/interject`, which only
queues - "The session actor drains it at the next safe point in `process_conversation_turn`"
(`sh/extensions/interject.rs:1-5`, handler `:40-58`) - and is race-tolerant: one arriving during
a reconnect-replayed `session/load` waits for the load instead of failing (`:43-48`). The
envelope is a shared crate: "The user sent a message while you were working:" + `<user_query>` +
"Make sure to complete any unfinished tasks from previous turns.", truncated at 25k
(`crates/common/xai-interjection-core/src/format.rs:1-51`); drains are FIFO, "one message per
entry, never merged" (`buffer.rs:26-41`). Safe points: turn-loop top, after a tool batch, before
returning (`turn.rs:2191`, `sh/…/tool_calls.rs:402`, `…/interjection.rs:284-306`). Other clients
get an `x.ai/session/interjection` broadcast so the originating pager dedups its own echo
(`interjection.rs:158-170`).

## Lens A - caching

- **Messages API, explicit breakpoints.** Four `cache_control: ephemeral` slots - last system
  block, conversation tip, previous turn's boundary - because "marking the system prompt alone
  leaves the transcript uncached", the third covers "more than the API's 20 block lookback", and
  "The fourth slot stays free: a gateway that turns on automatic caching takes it, and five is
  rejected outright" (`st/conversation/messages.rs:38-70`, `:5-36`).
- **Responses API, keyed.** `prompt_cache_key` = session id, forwarded only here
  (`sh/…/side_call.rs:52-79`, `st/types.rs:1039`). **Chat Completions: neither** - no key, no
  breakpoints, only whatever the provider does automatically; that asymmetry is what
  `forwards_prompt_cache_key()` makes legible.
- **Side calls ride the parent prefix.** Recap / turn summary / `/btw` replay the parent
  conversation under the parent's key at the same reasoning effort - "Effort changes the prompt
  ahead of the conversation history, so dropping it here would share no prefix with the main
  turn" (`side_call.rs:52-79`) - and strip reasoning only where forced (`:83-90`).
- **No gratuitous prefix rewrites.** Image eviction is hysteretic - below the trigger "every
  image stays in place so the KV-cache prefix is byte-stable across turns"; once fired it
  reclaims to half the limit so "the prefix is rewritten once and then stays stable (cache-warm)
  across many turns" (`crates/codegen/xai-chat-state/src/image_budget.rs:38-61`). A session crossing midnight gets a
  one-shot reminder rather than a rewritten header, "since the cached `<user_info>` prefix keeps
  its startup date to preserve the prompt cache" (`sh/…/reminders.rs:490-493`).
- **Compaction.** Speculative pass-1 summarizes the ~95% prefix into NOTE₁ ten points before the
  auto-compact line (`sh/session/compaction.rs:35-42`, `:175-192`), keyed by `fingerprint_prefix`:
  "A mismatch means the prefix changed (edit / rewind / branch) since pass-1, so the cached NOTE₁
  ... must be dropped" (`:45-64`); its span records hit rate and wasted tokens (`:187-203`).
- **Accounting.** Wire contract as a table in code: ACP `input_tokens` is the full prompt sum
  including cache reads, headless is uncached only with `input_tokens + cache_read +
  cache_creation + output = total_tokens`, and cost counts only when neither `usageIsIncomplete`
  nor `costIsPartial` - "Absence of cost means untrustworthy or unknown - not free"
  (`sh/extensions/notification.rs:66-80`, `:299-341`). TUI prints `Input tokens: N (M cached)`
  (`…/xai-grok-pager/src/app/status_blocks.rs:196-199`); telemetry has a `cache_read` token type
  (`docs/24-monitoring-usage.md:175`).
- **Across a relay reconnect nothing about the prompt changes**, because the leader - not the
  client - owns the conversation; reconnect replays the transcript to the *client*, deduped by
  eventId (`sh/leader/server.rs:41-46`). For our harness: the conversation lives in the
  long-lived process, web/mobile are subscribers not owners, and every steer is an append.

## Lens B - tree-sitter and syntactic awareness

Two grammars: `tree-sitter-bash` in workspace/permissions (`…/xai-grok-workspace/Cargo.toml:68-69`),
`tree-sitter` + rust/ts/python/go/js in the code graph (`…/xai-codebase-graph/Cargo.toml:59-64`).

- **Shell permissions are AST-driven.** `try_parse_word_only_commands_sequence` accepts a script
  only if it is plain commands joined by `&&`, `||`, `;`, `|`, rejecting on
  `tree.root_node().has_error()` and on any named node outside an explicit `ALLOWED_KINDS`
  whitelist - parens, redirections, substitutions, control flow all fall through to a prompt
  (`ws/permission/bash_command_splitting.rs:48-80`). `PlainCommand::spans_whole_script` guards
  grant matching: only a command spanning the whole script may be compared word-wise, else a
  leading `FOO=…` assignment or chained sibling is "silently dropped from the compare, letting an
  env-injected or extended script match a narrower grant" (`:28-45`).
- **Write detection is why the AST pays.** Extraction walks the tree for redirects plus
  per-command writers (`dd of=`, `sort -o`, `tee`, in-place `sed`); the program allowlist is "Not
  exhaustive - redirects are the robust catch-all (caught via the AST for any program)"
  (`ws/permission/shell_access.rs:295-303`, `:578-580`). The LLM auto-permission classifier sits
  on the same parse as "safe fast-paths" (`ws/permission/auto_mode/mod.rs:1-16`).
- **Symbols.** `xai-codebase-graph` is "High-performance code graph generation using tree-sitter
  queries" - goto-definition/references, incremental indexing, mmap'd cache
  (`…/xai-codebase-graph/src/lib.rs:1-12`), fed by fsnotify/git (`ws/fs_notify.rs:8`).
- **Edits are not AST-aware** - the finding. Search/replace and the hashline toolset anchor on
  whitespace-normalized line hashes with chunk or checkpoint fingerprints, not syntax nodes
  (`…/xai-grok-tools/src/implementations/grok_build_hashline/scheme.rs:1-18`); post-edit syntax
  correctness comes from a language server, not the grammar.

## Lens C - correctness and loop detection

- **Diagnostics as tool output.** An `async-lsp` client runs per workspace; after every
  `SearchReplace` the cross-cutting `LspDiagnosticsReminder` notifies the server and drains
  diagnostics into the tool result as a `<system-reminder>`
  (`crates/codegen/xai-grok-tools/src/reminders/lsp_diagnostics.rs:27-45`, `reminders/mod.rs:1-16`).
  Budget 500 ms - "anything scheduled to happen later than this ... answers after the reader has
  already given up" (`…/implementations/lsp/mod.rs:34-41`) - capped at 10 per file / 30 per
  summary (`…/lsp/manager.rs:25-36`). Structured output is validated in-process with `jsonschema` and
  retried (`turn.rs:2088-2113`); goal mode adds an outer verify loop (`sh/session/goal_*.rs`).
- **Repetition detector (pure client).** `IdenticalToolCallRun` hashes each call's signature and
  counts consecutive repeats: nudge at 8, hard stop at 16, 4 for "true no-op" runs (a bash
  `true`) which chain even across differing arguments (`turn.rs:2878-2880`, `:2901-2939`). The
  nudge names tool and run length - "you appear to be stuck in a polling loop" (`:2883-2890`);
  the hard stop ends the turn silently with `_meta.cancellationCategory = "action_stationarity"`
  (`:2117-2151`, `sh/session/commands.rs:69`).
- **Semantic stall + budgets.** Two consecutive identical verifier gap fingerprints mean "the
  model produced no change in the flagged gaps between attempts, so iterating further is futile"
  → `NoProgressPaused`; a run cap → `BackOffPaused` (`sh/session/goal_tracker.rs:16-30`,
  `:49-70`). `max_turns` bounds loop count and reports `"max_turns_reached"`
  (`sh/agent/mvp_agent/mod.rs:513`). API failures go through a shared sliding-window circuit
  breaker with a min-sample floor (`crates/common/xai-circuit-breaker/src/lib.rs:1-11`).
- **Doom loop: server signal, client intervention.** Opt-in via `x-grok-doom-loop-check: <window
  tokens>`; the API reports triggers mid-stream (`response.doom_loop_check`, cumulative) and on
  the terminal response as labels `tail_repetition:{threshold}@{channel}` or
  `low_logprob@{channel}`, where "Presence is itself the detection signal" (`st/doom_loop.rs:1-32`). Everything downstream is the client's: act only on the `thinking`
  channel because "loops in visible output are the user's to judge" (`:82-84`), only at or below
  `max_threshold`, only within a per-turn resample budget (default 2) (`:56-93`); the mid-stream
  abort is armed per attempt and disarmed once the budget is spent "so the final attempt
  completes and can be accepted" (`…/xai-grok-sampler/src/doom_loop.rs:17-52`), with
  near-immediate jittered backoff since "a fresh sample is the remedy - waiting buys nothing"
  (`…/xai-grok-sampler/src/retry.rs:86-99`). Without that server we keep the whole intervention
  side and must supply the signal; the detector needing nothing from a provider is the tool-call
  one above.

## Recommendation

Make the interjection envelope plus safe-point drain the only way a message reaches a working
agent. A chat artifact addressed to a busy agent becomes a queued pending interjection, not a
prompt; the turn loop drains it at the loop top, after a tool batch, and before returning,
wrapping each entry in a fixed envelope (note + `<user_query>` + unfinished-tasks trailer, FIFO,
never merged) and appending it as a synthetic user message; other clients see a broadcast and
dedup their own echo. That is what makes requirement 1 - everyone talks to everyone
while working - compatible with a 90% prefix hit: a steer is an append, so the cached prefix
survives, and one code path gives mid-turn steering, cross-agent chat, and remote-client fan-out.
Pair it with the declared-capability trick - one predicate per medium, tested against the actual
request mapping, so a cache key that never reaches the wire fails a test instead of silently
costing a 0% hit rate.
