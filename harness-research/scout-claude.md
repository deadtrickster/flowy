# scout-claude: Claude Code from the public record

What Claude Code actually does, sourced. Every claim carries a URL and a quote. Read against the seed: built-in chat fabric, pluggable subagent mediums, CLI/web/app as co-equal clients of one API.

## 1. Hooks — https://code.claude.com/docs/en/hooks

**What it is.** ~30 named lifecycle events, each dispatching a `command`, `http`, `mcp_tool`, `prompt`, or `agent` handler. Three cadences: "once per session: `SessionStart` and `SessionEnd` ... once per turn: `UserPromptSubmit`, `Stop`, and `StopFailure` ... on every tool call inside the agentic loop: `PreToolUse` and `PostToolUse`".

**How it works.** "Command hooks receive JSON data via stdin and communicate results through exit codes, stdout, and stderr. HTTP hooks receive the same JSON as the POST request body."
- exit 0 = proceed. "For most events, stdout is written to the debug log but not shown in the transcript. The exceptions are `UserPromptSubmit`, `UserPromptExpansion`, and `SessionStart`, where Claude Code adds plain-text stdout as context that Claude can see and act on."
- exit 2 = block, and JSON cannot rescue it: "even a JSON `permissionDecision` of `\"allow\"` can't override it." Per event: `PreToolUse` "Blocks the tool call", `Stop` "Prevents Claude from stopping", `PostToolUse` cannot block but "Shows stderr to Claude; the tool already ran".
- anything else fails open: "Claude Code treats exit code 1 as a non-blocking error and proceeds with the action, even though 1 is the conventional Unix failure code." (`WorktreeCreate` inverts this - any non-zero aborts.)
- stdout is parsed by first character: "**Starts with `{`**: Claude Code parses it as JSON." Output "capped at 10,000 characters". A timed-out hook "doesn't block the tool call".

**Copy this.** The stdin-JSON/exit-code contract - implementable in any language, no SDK. One distinguished blocking code separate from generic failure, so a crashed hook fails open. Exactly three events that may inject context, as the only injection path. HTTP hooks sharing the schema: that is a remote policy surface for the web/app clients for free.

**Skip this.** Thirty events. Ship six: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Stop`, `SessionEnd`. `PostToolBatch`, `MessageDisplay`, `CwdChanged`, `ElicitationResult` are retrofits onto an existing codebase. Also skip `suppressOutput`, which "Has no effect".

## 2. Subagents — https://code.claude.com/docs/en/sub-agents

**What it is.** "Each subagent runs in its own context window with a custom system prompt, specific tool access, and independent permissions." Markdown + YAML frontmatter in `.claude/agents/` (project) or `~/.claude/agents/` (user), plus `--agents '<json>'` for session-only definitions that "exist only for that session and aren't saved to disk".

**How it works.** "Only `name` and `description` are required." `tools:` narrows ("Inherits every tool available to subagents if omitted"); `model:` takes "`sonnet`, `opus`, `haiku`, `fable`, a full model ID ... or `inherit`". Resolution order: env `CLAUDE_CODE_SUBAGENT_MODEL` > per-invocation `model` > frontmatter > main conversation. Isolation is genuine: "It doesn't see your conversation history, the skills you've already invoked, or the files Claude has already read." The escape hatch is the fork: "A fork is a subagent that inherits the entire conversation so far instead of starting fresh."

**Copy this.** File-on-disk definitions with hot reload ("Claude Code detects the change within a few seconds ... with no restart needed"); identity from the `name` field, not the path. The four-level model-resolution chain is where your *pluggable medium* selector belongs - put `provider: glm` beside `model:` and inherit the same precedence, because per-spawn override is already proven here. Keep `--agents` JSON: a remote client must be able to define an agent without writing to the operator's disk.

**Skip this.** Delegation by description ("Claude uses each subagent's description to decide when to delegate"). It is unpredictable enough that they added `@`-mentions and `--agent` on top. Make spawning explicit and typed in your API.

## 3. MCP client integration — https://code.claude.com/docs/en/mcp

**What it is.** Claude Code as an MCP *client* over stdio/http/sse/ws. Config is strict: "A JSON entry that has a `url` but no `type` is a configuration error".

**How it works.** Three scopes - local (`~/.claude.json`, one project), project (`.mcp.json`, "Check `.mcp.json` into version control"), user (all projects). Repo-supplied servers need consent, showing as "``⏸ Pending approval (run `claude` to approve)``". Tools are namespaced `mcp__<server>__<tool>`, and that exact string is what "permission rules, a skill's `allowed-tools` list, a subagent's `tools` field, or a hook matcher" must use. Capabilities are live: "Claude Code supports MCP `list_changed` notifications". Reconnect is bounded - "up to five attempts, starting at a one-second delay and doubling each time. ... Stdio servers are local processes and are not reconnected automatically."

**Copy this.** Namespaced tool IDs as one key across permissions, agent tool lists, and hook matchers: one string, one grammar. Per-server `timeout` plus an idle timeout that "aborts with an error instead of waiting for the wall-clock limit". An explicit trust gate on repo-supplied server configs.

**Skip this.** Four transports. stdio + streamable-http is enough; their own docs say "The SSE (Server-Sent Events) transport is deprecated", and ws "supports neither" OAuth nor the `--transport` flag.

## 4. Permission modes and settings layering

**What it is.** A session-wide mode crossed with allow/ask/deny rules. Modes: `default`/Manual, `plan`, `acceptEdits`, `auto` (a model classifier), `dontAsk`, `bypassPermissions` (https://code.claude.com/docs/en/permission-modes).

**How it works.** The evaluation order is spelled out for the SDK: hooks → deny rules → ask rules → permission mode → allow rules → `canUseTool` callback (https://code.claude.com/docs/en/agent-sdk/permissions). The consequences they document are the interesting part: "A hook that returns `allow` does not skip the deny and ask rules"; "**Auto-approved tools never reach `canUseTool`.**"; "`allowed_tools` does not constrain `bypassPermissions`." `dontAsk` is the headless mode - "Claude Code auto-denies every tool call that would otherwise prompt you" - and "the session never waits for input". Settings precedence: "1. **Managed** (highest) ... 2. **Command line arguments** ... 3. **Local** ... 4. **Project** ... 5. **User** (lowest)", except that "Permission rules merge across scopes instead" of overriding. Reload is live: "edits to most keys apply to the running session without a restart. This includes `permissions`, `hooks`" (https://code.claude.com/docs/en/settings).

**Copy this.** The six-step order, verbatim, written down before any code - it is what makes remote approval safe to reason about. Merge-don't-override for rule lists. A `dontAsk` equivalent so a headless run can never block. Live settings reload.

**Skip this.** Six modes and the classifier. Start with three: manual, accept-edits, deny-unlisted. Skip the whole managed-settings apparatus (MDM plists, HKLM registry, `managed-settings.d/` merge ordering) - you are one user, not an IT department.

## 5. Headless / print mode and the Agent SDK — https://code.claude.com/docs/en/headless

**What it is.** `claude -p` plus Python/TS libraries: "The Agent SDK gives you the same tools, agent loop, and context management that power Claude Code."

**How it works.** `--output-format` is `text | json | stream-json`, where "`stream-json`: newline-delimited JSON for real-time streaming" and "The last line of the stream is a `result` message with the final response text, cost, and session metadata". `--json-schema` forces validated output into a `structured_output` field. Sessions resume by ID from any directory. Subagent traffic is attributable: messages "appear in the stream as `assistant` and `user` messages whose `parent_tool_use_id` field is the ID of the tool call that spawned the subagent", so you "can rebuild the full nesting tree by following those IDs". `--bare` skips "auto-discovery of hooks, skills, plugins, MCP servers, auto memory, and CLAUDE.md" and "is the recommended mode for scripted and SDK calls". Their own cross-language answer: "To drive the same agent loop from another language, [run the CLI as a subprocess] with the `-p` flag and `--output-format json`."

**Copy this.** NDJSON as *the* wire format - one stream feeds terminal, web, app, and tests. `parent_tool_use_id`, an event-parent pointer, so nested agent output reconstructs into a tree; flowy's event DAG already has that shape. A hermetic mode that loads nothing implicit. Subprocess-plus-JSON as the portable integration path: that is precisely how you plug in glm, grok, opencode, or a local model as mediums.

**Skip this.** Wrapping their SDK as your loop. Their own harness post makes the argument against it: the default setup "needs to both plan and execute in the same context window", and static workflows "must anticipate every edge case", against named failure modes including "Claude's tendency to prefer its own results or findings, especially when asked to verify or judge them" (https://claude.com/blog/a-harness-for-every-task-dynamic-workflows-in-claude-code). Take the wire format, not the runtime.

## 6. Background agents and the daemon — https://code.claude.com/docs/en/agent-view

**What it is.** `claude agents` over a per-user supervisor: "Background sessions are hosted by a per-user supervisor process, separate from your terminal and from agent view. The supervisor starts automatically the first time you background a session or open agent view, and you don't manage it directly."

**How it works.** Dispatch with `claude --bg "<prompt>"`; manage with `claude attach|logs|stop|respawn|rm` and `claude daemon status`. State is plain files - `~/.claude/daemon/roster.json`, `~/.claude/jobs/<id>/state.json`, `~/.claude/daemon.log` - and "Session state persists on disk through auto-updates and supervisor restarts." Write isolation is automatic: "Before editing files, Claude moves the session into an isolated git worktree under `.claude/worktrees/`, so parallel sessions can read the same checkout but each writes to its own." Row summaries are cheap: "generated by a Haiku-class model". Stated limit: "**Sessions are local**: background sessions run on your machine."

**Copy this.** A supervisor that outlives every client, with roster and per-job state as inspectable files - that, not the TUI, is what makes CLI, web, and app co-equal. Worktree-per-writer. `attach`/`logs`/`stop` as first-class API verbs rather than TUI-only affordances. Cheap-model one-line summaries, which is what a phone list actually needs.

**Skip this.** Destroying worktrees with the session ("Claude-created worktrees are deleted with the session in agent view"). Keep them and collect on an explicit command.

## 7. Remote control and the chat fabric

**Remote Control** drives a *local* session from claude.ai/code or the mobile app. "Your local Claude Code session makes outbound HTTPS requests only and never opens inbound ports on your machine. When you start Remote Control, it registers with the Anthropic API and polls for work." Transcripts centralize - "the session transcript ... is stored on Anthropic servers ... lets the session reconnect after a network drop" - while "Execution and filesystem access stay on your machine." The best detail: "after you answer several permission prompts in a session, an **Approve tool calls from your phone** notification shows the session URL" (https://code.claude.com/docs/en/remote-control).

**Cross-session messaging** is agent-to-agent chat. "Claude uses two tools for this: `ListAgents` to discover which agents it can reach, and `SendMessage` to deliver a message to one of them by name." Transport is local-first: on this machine, "Over a per-session socket, never through Anthropic servers"; other machines route over their Remote Control connection. Discovery is the filesystem: "Each session registers itself in files on disk and binds its inbox socket there", with `CLAUDE_CODE_MESSAGING_SOCKET` and `CLAUDE_CODE_MESSAGING_TOKEN` exported to hooks and Bash so scripts can post in. And a message is not consent: "a message from another session never counts as your consent, so it can't answer a pending permission prompt on your behalf", plus "Commands don't run: a command in the message's text, such as `/compact`, arrives as plain text" (https://code.claude.com/docs/en/cross-session-messaging).

**Channels** push external events in. A channel is an MCP server declaring `capabilities.experimental['claude/channel']` and emitting `notifications/claude/channel` with `content` and `meta`, arriving in context as `<channel source="webhook" severity="high">...</channel>`. Two-way channels expose a reply tool; `claude/channel/permission` opts into permission relay, where Claude Code sends a `permission_request` carrying a five-letter `request_id`, the human replies `yes <id>` from the far side, and "Claude Code applies whichever answer arrives first and closes the other". Stated plainly: "An ungated channel is a prompt injection vector" (https://code.claude.com/docs/en/channels-reference).

**Copy this.** (a) Outbound-only from the agent host: the relay is a rendezvous, the machine opens no ports. (b) Local sockets between same-host peers, relay only across a machine boundary - flowy's HLC federation is the same instinct one tier up. (c) Message ≠ consent, and commands inside messages are inert text; two one-line rules that kill a class of agent-to-agent privilege laundering. (d) Permission relay with a short echoed request ID and first-answer-wins between terminal and phone - the remote-control primitive worth building first. (e) Loop protection: Claude Code "rate-limits repeated messages per sender, drops identical repeats arriving within a short window, and caps accepted messages waiting for Claude to read them at 50 per session". (f) Allowlist on identity: "Gate on the sender's identity, not the chat or room identity ... In group chats, these differ."

**Skip this.** Three overlapping mechanisms plus a fourth mailbox: agent teams keep mail at `~/.claude/teams/{team-name}/inboxes/{agent-name}.json` and remain "experimental and disabled by default" (https://code.claude.com/docs/en/agent-teams). Your seed says the fabric is built in - build *one* addressed bus with humans and agents as peers, and make channels and teams views over it. Skip mandatory server-side transcripts: keep relay persistence optional, since flowy already stores signed, permission-filtered artifacts locally.

## The one recommendation

Make an addressed NDJSON event stream the harness's only interface, with the message envelope in it from day one. Claude Code got to a stream where every event carries `session_id` and `parent_tool_use_id`, then bolted chat on beside it - per-session sockets, MCP notifications, JSON mailbox files - three transports, three security models, one still experimental. You have none of that compatibility debt. Define one append-only event stream in which a tool call, a subagent's output, a prompt typed on a phone, a peer agent's message, and a permission verdict are all events with a sender and a recipient; then CLI, web, app, and every pluggable medium are readers and writers of that stream instead of features layered on it. flowy's event DAG with ed25519 row signing and permission-filtered reads is most of that substrate already. The missing pieces are per-event addressing and the two rules Anthropic learned the hard way: a peer's message is never consent, and commands inside messages are inert text.
