# Round 1 highlights - claude-side findings, per topic

Distilled from the four round-1 reports (their full texts live in the round-1 run
trees). Citations abbreviated; the full reports carry them. Round-2 GLM agents: treat
these as claims to verify or challenge against the code, not as facts to repeat - and
spend your depth budget on the three lenses, not on re-deriving this layer.

## Claude Code (scout-claude)

- Hooks: stdin-JSON in, exit-code out. Exit 2 blocks irreversibly; exit 1 fails OPEN
  (crashed hook never wedges); only UserPromptSubmit / UserPromptExpansion /
  SessionStart may inject context. Ship 6 events, not 30.
- Subagents: md + frontmatter in .claude/agents; model resolution env > invocation >
  frontmatter > main - put `provider:` beside `model:` there (pluggable-medium slot).
- MCP: one namespaced tool id (mcp__server__tool) shared by permissions, agent tool
  lists, hook matchers. Trust gate on repo-supplied configs. stdio + http suffice.
- Permissions: evaluation order hooks > deny > ask > mode > allow > canUseTool; rules
  merge across scopes; dontAsk auto-denies so headless never blocks.
- Headless: -p --output-format stream-json (NDJSON); parent_tool_use_id reconstructs
  the subagent tree; --bare = hermetic; subprocess + JSON is the portable way to mount
  another CLI as a medium.
- Daemon: per-user supervisor, roster/jobs as plain files, worktree-per-writer,
  cheap-model row summaries (what a phone list needs).
- Remote: outbound-only relay (no inbound ports); local sockets same-host; a peer's
  message is never consent and commands inside messages are inert text; permission
  relay = short echoed id, first answer wins; rate-limit/dedup/cap loop protection;
  gate on sender identity, not room identity.
- One rec: an addressed NDJSON event stream as the ONLY interface - every interaction
  an event with sender and recipient.

## DeepSeek harness (ds-claude) - repo deepseek-ai/deepseek-harness, Cordis/TS

- Log-derived context: every model request projected from the append-only session log
  via deriveMessages(); invariant "model-visible means logged" is runtime-asserted.
- Inbox verbs: followup (next turn) / steer (next step + wake) / inject (next step).
- Tools: ToolDefinition returns canonical JSON validated against output.schema, with
  PURE render + presentCall/presentResult projections (model text, live card, replayed
  card agree by construction).
- Subagents: ctx.subagents = name-keyed SubagentProvider registry (name,
  capabilities, inheritsParentContext, start; prepareContinuable whose presence IS the
  capability). Capability mismatch = loud typed rejection before start; config
  validated at mount. Shipped: spawn/fork in-process, dsh-sdk, acp, codex,
  claude-code (290 lines shelling to the real CLI). One tool per provider, wording
  derived from inheritsParentContext.
- Provider layer: {provider, model} dispatch; prepareCall binds the handle that runs;
  adapterDefaults marks adapter-supplied fields so a default never fossilizes into
  user intent on model switch.
- Permissioning: approval waterfall FAILS CLOSED when no answerer is connected; two
  independent knobs (approval, sandbox read-only|workspace-write|danger-full-access)
  plus a preset recorded as its own durable event; tool restriction is agent-scoped
  only; no allow-always at all.
- The gap: send_message is depth-1 parent-to-child, delivery-confirmed, NO answer
  channel; list_agents covers children only. Not a chat fabric.
- One rec: build SubagentProvider first, and hang the spawn tree OFF the messaging -
  a provider's job is "start a participant and give it an identity on the fabric".

## grok-build (gb-claude) - repo xai-org/grok-build, Rust ~397k LOC

- NOT an opencode fork: xAI's own codebase that ports codex/opencode TOOLSETS in-tree
  as selectable toolsets (config.rs opencode_toolset/codex).
- One protocol: ACP (JSON-RPC) end to end; TUI/IDE/headless/web are transports. One
  leader process per machine (~/.grok/leader.sock); relay forwards frames VERBATIM
  into the agent (relay.rs to_agent_tx.send(raw_frame)); local disk is truth, a
  cursor (relay_sync.json) replays after reconnect.
- ApiBackend = wire dialect (ChatCompletions|Responses|Messages) with per-dialect
  capability predicates; branch on dialect capability, never provider name.
  auth_provider_command: external cred command with TTL.
- Subagents: 12 built-in types, 3 advertised to the model; model-override PROVENANCE
  (model-chosen vs harness-chosen treated differently); personas as separate
  behavioural overlay axis; Rhai-scripted deterministic orchestrator (skip).
- Interjection envelope for mid-turn human messages: <user_query> frame, "user sent a
  message while you were working" note, 25k truncation, one message per entry, finish-
  your-tasks trailer. "You will get it wrong the first three times."
- Bundle: personas/agents/skills published server-side with checksum manifest so
  local edits are never clobbered.
- Foreign sessions: read-only scrape of Claude/Codex/Cursor SQLite stores (skip -
  a signed shared store is strictly better).
- One rec: ACP as the only agent protocol, relay as a dumb frame forwarder - flowy
  already has the harder half.

## opencode (oc-claude) - repo sst/opencode at 4643e65

- opencode serve: headless HTTP server; TUI/desktop/web/VSCode/plugins are clients.
  Per-request directory routing (no cwd pinning); generated+committed client with
  no-diff CI; TUI runs the server in a worker thread with an in-process fetch shim.
- Providers: models.dev catalog + config + plugin-declared; per-provider auth
  contributed by plugins as declarative prompt flows (every client renders login
  without knowing the provider). Runtime npm install to load a model adapter: skip.
- Plugins receive an SDK CLIENT, not internals; permission.ask is a hook (policy =
  plugin); no isolation/ordering - run untrusted hooks out of process.
- LSP: diagnostics pulled on edit/write and APPENDED to the tool result the model
  sees - the loop closes inside the turn.
- TUI: one SSE stream into a reactive store, 16ms coalescing; server can drive the
  TUI (/tui/*) - but module-level singleton queues, no addressing (fix: client ids).
- Permissions: rules last-match-wins; ask = permission.asked event + blocked
  Deferred + POST /permission/:id/reply from ANY client; "always" pushes a rule and
  auto-resolves pendings it now covers; reject cascades; arity table so approving
  `git commit -m x` remembers `git commit`. Approvals are in-process only (-> write
  them into the DAG).
- Multi-model: subagents are child sessions with their own model; DENIES inherit,
  allows do not; spawning is permission-gated on subagent_type (the medium as the
  pattern); sync is deliberately single-writer (skip - keep HLC two-writer).
- One rec: every blocking human interaction is ONE primitive - append the request to
  the event DAG, block on a future keyed by its id, any client resolves it by id.
