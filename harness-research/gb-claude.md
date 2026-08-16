# grok-build (xAI) - what to take for the harness

Source: `git clone https://github.com/xai-org/grok-build` (HEAD `5163763e`, "Synced from monorepo",
monorepo rev in `SOURCE_REV`). Rust, ~397k LOC of `.rs` outside `third_party/`, ~80 crates, Apache-2.0.
Read-only release: `CONTRIBUTING.md:3` - no external PRs, no CLA. Announced at
https://x.ai/news/grok-build-open-source . Everything below is cited to the clone at `/tmp/grok-build`
unless it is a URL.

**The brief's premise is wrong and that matters.** grok-build is not an opencode fork or derivative.
It is xAI's own Rust codebase that *ports codex and opencode tool implementations in-tree* as
selectable toolsets (`README.md:135`, `crates/codegen/xai-grok-agent/src/config.rs:544`
`opencode_toolset()`, `:1581` `codex()`, `:1588` `opencode()`). The `xai/grok-build-0.1` model id in
opencode is opencode listing xAI's model, not shared lineage.

## Knowable boundary

Open: the whole client - agent loop, TUI, tools, sampler, config, relay client. Closed: the server
side. `prod/mc/cli-chat-proxy-types/` ships only the ~4k lines of wire *types* for the "cli chat
proxy"; the proxy itself, the relay/session backend behind `https://grok.com/build/{id}`
(`crates/codegen/xai-grok-shell/src/relay/sync.rs:27-32`), the cloud sandbox
(`src/remote/agent.rs`), the subagent-bundle publisher, and the doom-loop signal producer are not in
the tree. Model weights obviously not. Context on the release: it landed days after a researcher
showed v0.2.93 uploading whole repos (SSH keys included) to an xAI GCS bucket
(https://devops.com/xai-open-sources-grok-build-coding-agent-after-cloud-upload-exposes-ssh-keys-repos/).
In this tree the default trace bucket is a compile-time `option_env!` that is `None` unless the build
sets it (`src/upload/gcs.rs:115-119`) - so the shipped-binary behaviour is not verifiable from source.

## One protocol, three transports (the big one)

**What.** Everything is ACP (agentclientprotocol.com, JSON-RPC) end to end. The agent is
`session/acp_session*`; the TUI, IDE extensions and headless CLI are all ACP clients.
**How.** A single leader process per machine owns agent state at `~/.grok/leader.sock`; TUI (stdio),
IDE (stdio) and headless (websocket) all attach to it (`src/leader/mod.rs:1-35` has the ASCII
diagram). `grok agent stdio` and `grok agent serve --bind --secret` expose the same thing
(`docs/user-guide/15-agent-mode.md:14-18`). Remote control is *the same protocol over a different
socket*: the local agent holds a WebSocket to the relay and forwards inbound frames verbatim into the
agent - `src/agent/relay.rs:483` splits the socket, `:538-545` logs each frame as `acp_inbound::{method}`
and pipes the raw text to `to_agent_tx`. No second API. Local disk stays source of truth; a disk
cursor (`relay_sync.json`) replays after reconnect (`src/relay/sync.rs:1-10, :77-80`).
**Copy.** The invariant: define the agent protocol once, then make CLI, web and mobile *transports*
of it, never re-implementations. Copy the offline cursor + "local disk is truth, relay is a mirror"
split - it is what makes a phone client safe on a train.
**Skip.** The single-leader-per-machine lock. Flowy already has a server; one leader per *node* with
sessions as artifacts is a better fit than a Unix socket and a PID lock.

## Pluggable mediums

**What.** Two orthogonal axes: *provider* (which API you call) and *agent definition* (which
prompt/toolset the child runs).
**How.** `[model_providers.<id>]` carries `base_url`, `api_key`/`env_key`, `extra_headers`,
`query_params`, `env_http_headers`, `auth_provider` and `api_backend`
(`src/agent/model_providers.rs:7-24`). `ApiBackend` is the wire dialect -
`ChatCompletions | Responses | Messages` - with per-dialect capability predicates
(`crates/codegen/xai-grok-sampling-types/src/types.rs:1013-1042`: Messages can't do native response
schemas so structured output falls back to a tool; only Messages needs reasoning stripped; only
Responses forwards the prompt cache key). Docs show Anthropic, OpenAI, Ollama and llama.cpp configs
side by side (`docs/user-guide/11-custom-models.md:202-266`). Credentials can come from an external
command with a TTL (`auth_provider_command`, `11-custom-models.md:368`; validation at
`model_providers.rs:26-64`). Agent definitions are portable markdown in `.grok/agents/*.md` deserialized
into `AgentDefinition` (`xai-grok-agent/src/config.rs:740-800`) with `tool_config`, `capability_mode`,
`permission_mode`, `skills`, `effort`, `max_turns`, `isolation`, `background`.
**Copy.** `ApiBackend`-style capability predicates. This is the single most reusable idea in the
provider layer: don't branch on provider *name*, branch on named capabilities of the wire dialect, and
keep the fallback (schema-via-tool) next to the predicate that triggers it. Also copy
`auth_provider_command` - it is how you plug an arbitrary local/enterprise medium in without shipping
code for it.
**Skip.** Their three-dialect closure is xAI-shaped. A harness that wants opencode or another *CLI*
as a medium needs a process-level adapter, which grok-build does not have (see below).

## Subagents

**What.** In-process child sessions, own context window, summary back to the parent.
**How.** `spawn_subagent(subagent_type, model?, background?, ...)`; `model` is validated against the
live catalog and carries a *provenance* flag so a model-chosen override is treated differently from a
harness-chosen one (`crates/codegen/xai-grok-tools/src/implementations/grok_build/task/types.rs:155-164`,
gate at `src/agent/subagent/handle_request.rs:12-24`). A coordinator actor owns lifecycle;
`ShellChildRunner` plugs the `!Send` local runner into it
(`src/agent/mvp_agent/subagent_coordinator.rs:1-30`). Twelve built-in agent types
(`config.rs:683-697`) but only three are advertised to the model as subagent types
(`general-purpose`, `explore`, `plan` - `config.rs:728-731`). *Personas* are a separate axis: a
behavioural overlay injected as a `<system-reminder>`, resolvable per subagent type, with its own
model override and a documented precedence order (`docs/user-guide/16-subagents.md:72,102,128`).
Per-type routing lives in config: `[subagents.models] explore = "..."` (`16-subagents.md:245-249`).
`xai-workflow` is a Rhai-scripted deterministic orchestrator over subagents, with a journal for
resume and hard caps (`MAX_PARALLEL`, `DEFAULT_AGENT_BUDGET=128`,
`crates/codegen/xai-workflow/src/lib.rs:8-18`).
**Copy.** Three things. (1) Advertise a *small* set of subagent types to the model while keeping many
resolvable by name - the model picks from 3, the operator from 12. (2) Model-override provenance -
you will want to refuse a model-requested medium switch that you'd accept from config. (3) The
persona/agent split: identity (tools, model, prompt) versus overlay (tone, output contract). It maps
cleanly onto flowy artifacts.
**Skip.** The Rhai workflow DSL. Flowy already has an event DAG and tasks; a second scripting
runtime is a liability.

## Cross-agent / human chat

**What.** No general agent-to-agent bus. Four narrower surfaces that together cover most of what a
chat fabric is used for.
**How.** *Interjection*: a human message arriving mid-turn is buffered and drained at the next safe
point as a synthetic user message wrapped in `<user_query>` with a "the user sent a message while you
were working" note and a "finish your unfinished tasks" trailer
(`crates/common/xai-interjection-core/src/format.rs:1-50`, `buffer.rs:22-40`). *Prompt queue*: queued
prompts merge under explicit gate rules (`crates/codegen/xai-prompt-queue/src/lib.rs:1-10`).
*Dashboard*: every top-level session in the process, with peek / reply / attach / dispatch from one
screen (`docs/user-guide/23-dashboard.md:1-8`). *Foreign sessions*: bounded, metadata-only, read-only
listing of Claude Code, Codex and Cursor sessions off their own SQLite stores
(`crates/codegen/xai-grok-foreign-sessions/src/lib.rs:8-45`), plus a Claude settings importer that
emits a TOML patch (`src/claude_import.rs:1-22`). Crash recovery via `~/.grok/active_sessions.json`
keyed on live PIDs (`crates/codegen/xai-grok-active-sessions/src/lib.rs:1-3`).
**Copy.** The interjection envelope, close to verbatim. It is the hardest-won detail here: the note,
the `<user_query>` frame, the 25k truncation, one message per entry never merged, and the trailer that
stops a steer from dropping in-flight work. Any chat fabric that can talk to a *working* agent needs
exactly this and will get it wrong the first three times.
**Skip.** Reading other agents' SQLite files. Flowy's answer - every agent writes signed events into
one store - is strictly better than scraping Claude's DB read-only and hoping the schema holds.

## CLI/TUI structure

Ratatui, with xAI's own inline/textarea forks (`xai-ratatui-inline`, `xai-ratatui-textarea`) and a PTY
test harness (`xai-grok-pager-pty-harness`, 12k LOC). The pager is 511k LOC - bigger than the agent
runtime (392k) and the tools (139k) combined. Skip the scale; copy the fact that they can drive the
real TUI under a PTY in tests, which is what flowy's teatest gate already does.

## Proprietary wiring

*Doom loop*: the **server** emits loop-detection signals inside the SSE stream; the client parses them,
applies a recovery policy and can abort mid-stream, disarming the abort once a retry budget is spent
(`crates/codegen/xai-grok-sampler/src/doom_loop.rs:1-25`). *Bundle*: personas, roles, agents and skills
are published by xAI and cached under `<grok home>/bundled`, checksum-tracked so hand-edited files are
never overwritten (`crates/codegen/xai-grok-bundle/src/lib.rs:1-8`). *Strict harness*: agents whose
prompt or toolset is curated are flagged, so advertised tools match what the model was trained on
(`config.rs:1450-1457`, `inject_default_tools` at `config.rs:784`). The last one is the real
model-side coupling: xAI's models are trained against specific tool schemas, and the harness renames
tools per agent to match (`config.rs:145-170`: `bash` → `run_terminal_command`, `task` →
`spawn_subagent`).
**Copy.** The bundle's checksum manifest - push agent definitions from the server without ever
clobbering a local edit. That is a fabric feature and flowy is the right shape for it.
**Skip.** Doom-loop-as-server-signal (you don't own a model server) and telemetry upload entirely.

## The one recommendation

**Make ACP-over-transport the harness's only agent protocol, and make the relay a dumb frame
forwarder.** grok-build's TUI, IDE, headless CLI and web client are not four integrations; they are
one JSON-RPC surface reached over stdio, a Unix socket, or a WebSocket, and the relay's entire job on
the inbound path is `to_agent_tx.send(raw_frame)` (`src/agent/relay.rs:538-545`). Flowy already has
the harder half - one `/api`, permission-filtered, signed, with console and TUI as co-equal clients.
Adopt ACP as the *session* protocol on top of it rather than inventing one, keep local disk (or the
local node) as source of truth with a resumable cursor, and the web and mobile remote controls stop
being a port of the CLI and become the same client at a different address.
