# r2-scout-claude - Claude Code from the public record

Sources: `code.claude.com/docs/en/*` (fetched 2026-08-16), `anthropics/claude-code`
CHANGELOG.md (raw, 513KB; line numbers cited), and the shipped binary at
`/opt/firecode/config/bin/claude`, inspected with capped `strings` greps.

## Hooks and exit-2

30 events, three cadences - "once per session: `SessionStart` and `SessionEnd`; once per turn: `UserPromptSubmit`, `Stop`, and `StopFailure`; on every tool call inside the agentic loop: `PreToolUse` and `PostToolUse`" (docs/en/hooks). Five handler types: `command`, `http`, `mcp_tool`, `prompt` (single-turn model call), `agent` (spawns a subagent).

Exit-2 semantics: "Exit 2 means a blocking error. On events that can block, exit 2 blocks whether or not you print JSON: even a JSON `permissionDecision` of `"allow"` can't override it." The footgun is exit 1: "Without valid JSON on stdout, Claude Code treats exit code 1 as a non-blocking error and proceeds with the action, even though 1 is the conventional Unix failure code." One exception - "`WorktreeCreate`, where any non-zero exit code aborts worktree creation." Stdout is parsed as JSON only if its first non-whitespace char is `{`. Blocking is per-event: `PostToolUse` "Shows stderr to Claude; the tool already ran"; `PermissionRequest` ignores exit 2 ("Deny through the `decision` object instead"); `PostToolBatch` "Stops the agentic loop before the next model call".

**Copy:** two contracts only (exit-2 blocks / exit-0+JSON decides); the `if` field carrying one permission rule (`"Bash(git *)"`) so a hook needn't re-parse tool input; and the output cap - "Hook output strings, including `additionalContext`, `systemMessage`, and plain stdout, are capped at 10,000 characters. Output that exceeds this limit is saved to a file and replaced with a preview and file path." **Skip:** 30 events. Start with PreToolUse, PostToolUse, UserPromptSubmit, Stop, SessionStart/End.

## Subagents

Markdown + YAML frontmatter in `.claude/agents/`, `~/.claude/agents/`, plugin `agents/`, or `--agents` JSON. Relevant fields: `model` ("`sonnet`, `opus`, `haiku`, `fable`, a full model ID ... or `inherit`"), `permissionMode`, `maxTurns`, `mcpServers`, `hooks`, `effort`, `isolation: worktree`, `memory` ("Persistent memory scope: `user`, `project`, or `local`. Enables cross-session learning") (docs/en/sub-agents).

"Each subagent starts with a fresh, isolated context window. It doesn't see your conversation history ... The exception is a fork, which inherits the parent conversation instead of starting fresh." Depth 3: "At the depth limit, Claude Code withholds the `Agent` tool from every subagent except a fork." Concurrency: "when 20 subagents are running in a session, spawning another with the Agent tool fails with `Concurrent subagent limit reached`, and the error tells Claude not to retry."

**Copy:** the per-spawn `model` field *is* our pluggable-mediums requirement, and the frontmatter file is the plugin format - no registry code. Also the **sibling roster**: a subagent's initial context includes "a system reminder listing `main` and every other named agent in the session" - one block that makes an in-session chat fabric legible to the model. **Skip:** the 5-tier managed/plugin precedence ladder.

## MCP client

stdio / `http` (alias `streamable-http`) / deprecated `sse` / `ws`. The load-bearing design is **tool search**: "Tool search keeps MCP context usage low by deferring tool definitions until Claude needs them. Only tool names and server instructions load at session start" (mcp.md:1150). Output is bounded: warning over 10,000 tokens, "limits output to 25,000 tokens by default" (`MAX_MCP_OUTPUT_TOKENS`), and oversized results "are persisted to disk and replaced with a file reference in the conversation" (mcp.md:1035), with servers raising their own ceiling via `_meta["anthropic/maxResultSizeChars"]` up to 500,000 chars.

**Copy:** deferred loading + persist-to-disk-with-reference. Both are cache preservation (Lens A) as much as context economy - flowy's MCP shared memory should return artifact refs, not bodies.

## Permission modes and settings layering

Modes: `default`(=`manual`), `acceptEdits`, `plan`, `auto`, `dontAsk`, `bypassPermissions`. Rules evaluate "deny, then ask, then allow. The first match in that order determines the outcome, and rule specificity doesn't change the order" (permissions:37) - a broad deny cannot carry allowlist exceptions. Layering: "managed settings highest: no other level, including command line arguments, can override a managed permission rule ... If a tool is denied at any level, no other level can allow it" (permissions:514-516). Hooks compose in a defined order: "Claude Code evaluates deny and ask rules regardless of what a PreToolUse hook returns ... A hook that exits with code 2 stops the tool call before permission rules are evaluated" (permissions:409-413).

**Copy:** deny > ask > allow with no specificity tiebreak, and deny-wins-across-layers - the only layering rule explainable in one sentence. flowy already enforces reads in SQL; same shape.

## Headless / print + SDK

`claude -p`; `--output-format text|json|stream-json`; `--json-schema` puts schema-conformant output "in the `structured_output` field" (headless.md:125); `--include-partial-messages` for token streaming. Lifecycle contract worth stealing: "If you stop a `claude -p` run with SIGTERM ... Claude Code aborts the in-progress turn, terminates the process tree of any running Bash command, runs `SessionEnd` hooks, and exits with code 143" (headless.md:71). Feature detection is capability-based: the init event "carries an optional `capabilities` array of strings naming the protocol behaviors this Claude Code version implements ... Check it to feature-detect instead of comparing version strings" (headless.md:204).

**Copy:** the `capabilities` array. flowy's one `/api` with three clients will version-skew; this is cheaper than version negotiation.

## Background daemon

"Background sessions are hosted by a per-user supervisor process, separate from your terminal and from agent view. The supervisor starts automatically the first time you background a session or open agent view" (agent-view.md:641). `claude daemon status` prints "state, version, socket directory, and worker count"; `claude daemon stop --any` takes `--keep-workers` "to leave background sessions running so the next supervisor reconnects to them" (agent-view.md:615-616). "Session state persists on disk through auto-updates and supervisor restarts" (:134).

The failure modes are in the CHANGELOG and are the ones we would hit: "Fixed a displaced background daemon deleting its successor's control socket on shutdown, which made the next client kill the healthy replacement daemon" (:456); "Fixed `claude daemon stop --any` potentially terminating an unrelated process via a stale legacy daemon lockfile" (:394). **Copy** supervisor-owns-workers with workers survivable across supervisor restarts. **Skip** lockfile-based daemon identity - the changelog says twice that it goes wrong.

## Remote control and cross-session messaging (the chat fabric)

Remote Control attaches phone/web to a session running *locally*: "Claude keeps running locally the entire time, so your code execution and filesystem access stay on your machine", and both surfaces are live at once - "you can send messages from your terminal, browser, and phone interchangeably" (docs/en/remote-control). Forwarded dialogs expire: permission prompts and `AskUserQuestion` stay open indefinitely; other dialogs wait "five minutes by default, then closes the dialog and continues with the dialog's no-action default" (`dialogExpiry`).

Cross-session messaging is the closest public analogue to flowy's seed requirement #1. Transport splits by locality (docs/en/cross-session-messaging):

| target | how the message travels |
|---|---|
| same machine | "Over a per-session socket, never through Anthropic servers" |
| another of your machines | "Through Anthropic servers, arriving over that machine's Remote Control connection" |
| cloud session | "Through Anthropic servers, straight to the cloud session" |

Each session binds a Unix inbox socket exported as `CLAUDE_CODE_MESSAGING_SOCKET`, with a per-session token in `CLAUDE_CODE_MESSAGING_TOKEN` - "A script posting to its own session's socket can send `{"type":"auth","token":"<token>"}` as the first line of its connection." Delivery is three-valued (**Delivered / Held / Refused**) via `crossSessionInbound`, defaulting off the two sessions' permission modes: "The receiving session bypasses permission prompts: Claude Code holds each message for your approval."

The trust model is the part to copy verbatim: "a message from another session never counts as your consent, so it can't answer a pending permission prompt on your behalf"; "Claude Code instructs the receiving Claude never to change permission settings, `CLAUDE.md`, or other configuration because another session asked"; "a command in the message's text, such as `/compact`, arrives as plain text. Claude Code never executes it." Claude is also told "never to ask another session for an action that was denied or blocked in its own session" - permission laundering is named as a threat.

**Copy:** (1) text only, never history or files - "To move a whole conversation or its context, resume the session instead"; (2) the delivery point - "The receiving Claude reads the message between tool calls during an active turn, so a running tool is never interrupted"; (3) the three-valued inbound control. flowy already signs rows with ed25519, so the socket-token equivalent is free.

---

## Lens A - caching

**The model.** "The API caches by matching the start of each request, called the prefix ... The match is exact, so a change anywhere in the prefix recomputes everything after it. There is no per-file or per-segment caching" (docs/en/prompt-caching). Claude Code therefore orders each request into three layers by change frequency: **system prompt** (core instructions, tool definitions, output style) → **project context** (CLAUDE.md, auto memory, unscoped rules) → **conversation**. Model and effort "aren't part of the prompt text at all ... but both are part of the cache key".

**What busts it** (the doc's own list): switching models, changing effort, turning fast mode on (a header in the cache key), connecting/disconnecting an MCP server *when its tools load into the prefix*, enabling a plugin *that provides an MCP server*, adding a **bare** tool deny rule ("Adding a bare tool name like `Bash` or `WebFetch` as a deny rule removes that tool from Claude's context entirely"), `/compact`, and upgrading the CLI.

**What doesn't**, which is the design lesson: plan mode, skills and commands "append their instructions as conversation messages, so the cached prefix stays intact"; permission-mode changes are cache-safe; `/recap` "appends the summary as command output rather than replacing your message history"; `/rewind` "truncates your conversation back to an earlier turn ... so the next request hits the earlier cache entry". Mid-session mutation is *refused* rather than allowed-and-costly: "Editing them mid-session does not invalidate the cache, but the edit also doesn't apply. Claude keeps working with the version that was loaded at session start." Same for output style, and `/output-style` was deprecated for exactly that reason: "Output style is now fixed at session start for better prompt caching" (CHANGELOG.md:3348).

**Accounting.** Two fields, read live by a statusline script from `current_usage`: `cache_creation_input_tokens` and `cache_read_input_tokens` ("billed at roughly 10% of the standard input rate"). "A high read-to-creation ratio means caching is working well." OTel exports both per user and session; Pro users get a footer hint "showing roughly how many tokens the next turn will send uncached" (CHANGELOG.md:2797).

**Provider-specific.** Anthropic explicit breakpoints, degrading gracefully behind gateways: "If the gateway rejects the cache breakpoint on that block, Claude Code retries the request without it and leaves that block uncached for the rest of the conversation." TTL is 1h automatic on subscription, 5m on API key, with `ENABLE_PROMPT_CACHING_1H` / `FORCE_PROMPT_CACHING_5M`. Scope is per-machine-per-directory because "The system prompt embeds the working directory, platform, shell, OS version, and auto memory paths"; the SDK escape hatch is `--exclude-dynamic-system-prompt-sections` (CHANGELOG.md:2655). Fan-out: "Claude Code briefly holds all but the first so their first requests can read the prefix the first agent cached."

**What OUR harness must guarantee.** (1) Three-layer append-only projection from the event DAG - a steer is a new event at the tail, never a rewrite. (2) Tool schemas frozen per session: they fixed "prompt cache misses in long sessions caused by tool schema bytes changing mid-session" (:2875) and removed "dynamic content from tool descriptions" (:2931). (3) Per-spawn medium selection happens *at spawn* only - model is in the cache key. (4) Steers and chat messages append as conversation, exactly like skills do. (5) Prefer rewind-to-a-cached-prefix over compact. (6) Surface the read:creation ratio in the CLI *and* both remote clients.

## Lens B - tree-sitter and syntactic awareness

**Present, and load-bearing for permissions.** The binary bundles tree-sitter grammars (`strings /opt/firecode/config/bin/claude` yields `tree-sitter`, `tree-sitter-cli`, `tree-sitter-json`, `tree-sitter-kotlin`, `tree-sitter-typescript`, `tree-sitter-yaml`) plus a bash-analysis module exporting `parseCommand`, `parseCommandRaw`, `findCommandNode`, `extractCommandArguments`, a telemetry event `tengu_tree_sitter_parse_abort`, and a 10,000-char cap (`Add=1e4`) matching the doc: "Commands longer than 10,000 characters always prompt because they exceed what the analysis parses" (permissions:210).

What the AST buys: the analyzer walks `pipeline`, `redirected_statement`, `variable_assignment`, `command_substitution`, `heredoc_redirect` nodes and maintains a static env-var map, refusing with typed reasons - `"IFS assignment changes word-splitting - cannot model statically"`, `"'jobs -x' executes its argument as a command - cannot be statically analyzed"`, `"awk program contains system() which executes arbitrary commands"`, `"Shell keyword '${i}' as command name - tree-sitter mis-parse"`. That is fail-closed on parse ambiguity, not best-effort regex, and the docs agree: "when Claude Code can't fully parse a command, it asks for approval instead of treating the command as read-only" (permissions:210).

Granularity follows from the AST: "Claude Code is aware of shell operators, so a rule like `Bash(safe-cmd *)` won't give it permission to run the command `safe-cmd && other-cmd`. The recognized command separators are `&&`, `||`, `;`, `|`, `|&`, `&`, and newlines. A rule must match each subcommand independently" (permissions:179). Wrapper stripping is structural: "a rule like `Bash(npm test *)` also matches `timeout 30 npm test`" - `timeout`, `time`, `nice`, `nohup`, `stdbuf`, `command`, `builtin`, `noglob`, with the query form `command -v` deliberately not stripped (:188). Approvals decompose: "approving `git status && npm test` saves a rule for `npm test`" (:182). PowerShell has its own: "Claude Code parses the PowerShell AST and checks each command in a compound command independently" (:255). Parse trees leak if unmanaged - two fixes name them (CHANGELOG.md:3717, :4223).

**Notably absent:** no AST-aware editing. Edit is "exact string replacement ... It doesn't use regex or fuzzy matching" (tools-reference.md:191). Structural code understanding is delegated to LSP, not tree-sitter.

**What OUR harness needs.** tree-sitter-bash on the permission door, fail-closed on unparseable, one rule per subcommand, wrapper stripping. This is the highest-value machinery in this report to copy verbatim: a regex bash allowlist is a security hole, and the AST version is a solved problem with a published node-type list.

## Lens C - correctness support and loop detection

**Correctness feedback closing inside the turn.**
- **LSP diagnostics pushed after every edit.** "The LSP tool gives Claude code intelligence from a running language server. After each file edit, it automatically reports type errors and warnings so Claude can fix issues without a separate build step" (tools-reference.md:269). Per-plugin `.lsp.json` with `diagnostics`: "Whether to push diagnostics into Claude's context after edits (default `true`)" (plugins-reference.md:247). Official plugins: `pyright-lsp`, `typescript-lsp`, `rust-analyzer-lsp`.
- **Tool errors returned as actionable text.** "A pattern, glob, or file type that ripgrep rejects returns an error that includes ripgrep's diagnostic, so Claude can correct the input and search again" (tools-reference.md:255).
- **Edit preconditions force grounding.** Match must be exact and unique; on a non-unique `old_string` "Claude either supplies a longer string with enough surrounding context to pin down one occurrence, or sets `replace_all: true`" (:197).
- **PostToolUse as the lint gate** - the documented example matcher is `Edit|Write` running `lint-check.sh`; on PostToolUse exit 2 "Shows stderr to Claude".
- **A second model reviews actions.** "A separate classifier model reviews actions before they run, blocking anything that escalates beyond your request, targets unrecognized infrastructure, or appears driven by hostile content Claude read" (permission-modes.md:256). It also gates the chat fabric: "The classifier also reviews each message Claude sends to another agent with `SendMessage`, plain or structured, before Claude Code delivers it" (:260). It honours stated intent - "If you tell Claude 'don't push' ... the classifier blocks matching actions even when the default rules would allow them" - with an honest caveat: "Boundaries are not stored as rules. The classifier re-reads them from the transcript on each check, so a boundary can be lost if context compaction removes the message that stated it" (:388).

**Loop detection - the concrete signals and interventions.**

| signal | intervention | source |
|---|---|---|
| Stop hook blocks repeatedly | "Claude Code overrides the hook and ends the turn after 8 consecutive blocks"; hooks receive `stop_hook_active`, "`true` when Claude Code is already continuing as a result of a stop hook" | hooks.md:2379 |
| Runaway delegation | "a per-session cap on subagent spawns (default 200, override with `CLAUDE_CODE_MAX_SUBAGENTS_PER_SESSION`) to stop runaway delegation loops; `/clear` resets the budget" | CHANGELOG.md:484 |
| Concurrency | 20 concurrent subagents; "the error tells Claude not to retry" | sub-agents |
| Spend | "`--max-budget-usd` ... once the cap is reached, new spawns are denied and running background agents are halted" | CHANGELOG.md:380 |
| Per-agent turns | `maxTurns` frontmatter, `--max-turns` | sub-agents |
| Message ping-pong | "Claude Code rate-limits repeated messages per sender, drops identical repeats arriving within a short window, and caps accepted messages waiting for Claude to read them at 50 per session. A message loop between two sessions therefore stops on its own." | cross-session-messaging |
| Doomed retries | "Fixed a retry loop that re-sent identical doomed requests after a context-overflow error with a large thinking budget" | CHANGELOG.md:342 |
| Denial churn | telemetry `tengu_auto_mode_denial_limit_exceeded` | binary strings |
| Runaway build | "opt-in memory cgroup support for Bash tool commands on Linux (`CLAUDE_CODE_TOOL_MEMORY_LIMIT`) so a runaway build can't stall the session" | CHANGELOG.md:7 |

Also in the binary as telemetry names: `tengu_event_loop_stall`, `tengu_duplicate_tool_use_id`, `tengu_file_read_dedup`, `tengu_attention_budget_cycle`. There is **no public evidence of semantic repetitive-tool-call detection**. Every documented brake is a counter or a budget (8 blocks, 200 spawns, 20 concurrent, 50 queued, N dollars, N turns) plus dedup of byte-identical payloads. That is itself the finding: the shipped answer to doom loops is cheap counters at every recursion boundary, not a cleverness.

**What OUR harness should do.** Put a counter on every recursion boundary - spawn, stop-hook continue, inbound message, tool retry - and make each one state *why it fired* into the transcript so the model can adapt rather than just stop. Then add the two things most harnesses skip: diagnostics pushed automatically after every edit, and a cheap second-model review on the outbound edge of the chat fabric, which is exactly the surface Anthropic chose to classify.

---

## Single most important recommendation

**Make the chat fabric append-only at the tail of each agent's context, and make every inbound message text-only, three-valued (deliver / hold / refuse), and unable to grant permission.**

flowy's differentiator is built-in cross-agent chat - and that is precisely the feature that can destroy the two things which make a harness usable: the prompt cache and the permission boundary. Claude Code handles both hazards, but separately: caching by refusing mid-session mutation outright ("the edit also doesn't apply") and appending everything else as conversation; safety by the flat rule that "a message from another session never counts as your consent". flowy has to solve them together, because unlike Claude Code our messages arrive constantly and by design.

Concretely: a chat message is one event at the DAG tail, projected into the conversation layer only, never into system prompt or project context; it carries text plus an artifact *reference*, never an artifact body; it is classified on the outbound edge and gated by `crossSessionInbound` semantics on the inbound edge; and it is rate-limited with identical-repeat dedup so two of our own agents cannot spin each other. Get that right and a 90% prefix survives an arbitrarily chatty room.
