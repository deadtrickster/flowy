# r2-scout-glm: Claude Code from the public record

## Core areas

### Hooks (stdin-JSON in, exit-code out)
Hooks run shell commands, HTTP endpoints, or LLM prompts at specific lifecycle points. Event types: `SessionStart`, `UserPromptSubmit` (after prompt submit, before Claude processes), `UserPromptExpansion` (slash command expansion), `PreToolUse`/`PostToolUse`, `Stop`, `SessionEnd` (code.claude.com/docs/en/hooks). Input interface: JSON via stdin with fields like `session_id`, `prompt_id`, `cwd`, `permission_mode`, `tool_name`, `tool_input`. **Exit code 2 blocks irreversibly** - on `PreToolUse` it blocks the tool call, on `UserPromptSubmit` it erases the prompt, on `UserPromptExpansion` it blocks expansion. Other exit codes (1, 127, etc.) are non-blocking errors by default. **Only three events can inject context via stdout**: `UserPromptSubmit`, `UserPromptExpansion`, `SessionStart` (code.claude.com/docs/en/hooks). Hook resolution: managed > user > project > local > plugin > skill > subagent.

### Subagents (md + frontmatter in .claude/agents)
Subagents are Markdown files with YAML frontmatter defining `name`, `description`, `model`, `tools`, `permissionMode`, `skills`, `mcpServers`, `hooks` (code.claude.com/docs/en/sub-agents). **Model resolution**: `CLAUDE_CODE_SUBAGENT_MODEL` env > per-invocation model parameter > subagent's `model` frontmatter > main conversation's model (code.claude.com/docs/en/sub-agents). This is where a `provider:` field would sit for pluggable mediums - R1 got this right. File locations priority: managed settings > `--agents` CLI flag > `.claude/agents/` > `~/.claude/agents/` > plugin `agents/`. **Fork mode** (`subagent_type: "fork"`) inherits full conversation + prompt cache; regular subagents start fresh. Fork mode is on by default in interactive sessions (CHANGELOG.md v2.1.232).

### MCP client (mcp__server__tool namespacing)
MCP tools use namespaced pattern `mcp__<server>__<tool>` shared by permissions, agent tool lists, hook matchers (code.claude.com/docs/en/hooks). **Trust gate**: repo-supplied MCP configs require trust confirmation (CHANGELOG.md shows multiple trust fixes for nested repos, symlinks). Both stdio and HTTP transports supported. Tool search defers MCP tool descriptions by default on supported models, keeping them out of the prompt prefix and preserving cache (code.claude.com/docs/en/prompt-caching). Dynamic tool updates from servers can invalidate cache when tools load into prefix.

### Permissions (evaluation order and settings layering)
Rules evaluate: **deny > ask > allow** (code.claude.com/docs/en/permissions). R1's "hooks > deny > ask > mode > allow > canUseTool" doesn't match docs - docs say deny/ask/allow in that order, with hooks being a separate pre-filter via `PreToolUse`. Settings layering: managed > user > project > local (code.claude.com/docs/en/settings). **`dontAsk` auto-denies** so headless never blocks (code.claude.com/docs/en/permissions). Scoped deny rules like `Bash(rm *)` leave the tool available and block matching calls; bare tool name like `Bash` removes it entirely from context.

### Headless/print + SDK
**`-p --output-format stream-json`** produces NDJSON output (code.claude.com/docs/en/cli-reference). `parent_tool_use_id` reconstructs the subagent tree. `--bare` = hermetic mode (docs). Subprocess + JSON is the portable way to mount another CLI as a medium - R1 confirmed.

### Daemon and Remote Control
R1 described "per-user supervisor, roster/jobs as plain files" and "outbound-only relay" for Remote Control. CHANGELOG.md v2.1.232-2.1.234 shows extensive Remote Control work: **`SendMessage` and `ListAgents`** for cross-session messaging, outbound-only relay with rate-limit/dedup/cap loop protection, permission relay via short echoed id (CHANGELOG.md). Remote Control persists across 30-minute network blips (v2.1.232).

## Lens A - Prompt caching

### Stable prefix ordering and cache control
Claude Code orders each request to maximize prefix hits: **System prompt (core instructions, tool definitions) > Project context (CLAUDE.md, auto memory, rules) > Conversation** (messages, responses, tool results) (code.claude.com/docs/en/prompt-caching). Changes to system prompt invalidate everything; conversation changes keep earlier layers cached. **Explicit cache breakpoints**: Claude Code marks cacheable blocks appropriately; provider-specific handling (Anthropic explicit vs DeepSeek automatic vs OpenAI automatic) lives in the infrastructure serving the model (code.claude.com/docs/en/prompt-caching).

### What busts cache
- Model switch: Each model has its own cache; `/opusplan` toggles between Opus/Sonnet (code.claude.com/docs/en/prompt-caching)
- Effort level change: Each effort has its own cache per model (code.claude.com/docs/en/prompt-caching)
- Fast mode enable: Adds request header that's part of cache key (code.claude.com/docs/en/prompt-caching)
- MCP server connect/disconnect: Only when tools load into prefix (non-deferred). Deferred tools (default on supported models) preserve cache (code.claude.com/docs/en/prompt-caching)
- Plugin enable/disable: Only when plugin provides MCP servers with non-deferred tools (code.claude.com/docs/en/prompt-caching)
- Denying entire tool: `Bash` or `Bash(*)` removes tool from system prompt layer (code.claude.com/docs/en/prompt-caching)
- `/compact`: Replaces message history with summary, invalidates conversation layer (code.claude.com/docs/en/prompt-caching)
- Upgrade: New system prompt/tool definitions rebuild cache (code.claude.com/docs/en/prompt-caching)

### Cache-safe actions
- **Editing files**: File changes trigger `<system-reminder>` notice, not retroactive history change (code.claude.com/docs/en/prompt-caching)
- **CLAUDE.md edits mid-session**: Don't apply until next `/clear`/`/compact`/restart (code.claude.com/docs/en/prompt-caching)
- **Skills/commands**: Append as user messages, don't disturb prefix (code.claude.com/docs/en/prompt-caching)
- **Permission mode changes**: Cache-safe (except `opusplan` model switch) (code.claude.com/docs/en/prompt-caching)
- **Subagent spawning**: Appends to conversation, keeps parent's prefix intact (code.claude.com/docs/en/prompt-caching). Forks share parent's cache.

### Provider specifics and TTL
- **Claude subscription**: Automatic 1-hour TTL (code.claude.com/docs/en/prompt-caching)
- **API key/3P provider**: 5-minute TTL default, override with `ENABLE_PROMPT_CACHING_1H=1` (code.claude.com/docs/en/prompt-caching)
- **Gateways**: Cache lives wherever gateway forwards; if gateway rejects cache breakpoint, Claude Code retries without it (code.claude.com/docs/en/prompt-caching)

**R1 verification**: R1 didn't cover the layered ordering (system/project/conversation), the specific cache-busting actions, or the provider-specific TTL behavior. Lens A adds substantial depth.

## Lens B - Tree-sitter and syntactic awareness

### Bash command parsing for permissions
Permission checking uses **pattern matching on shell commands**, not full tree-sitter parsing (code.claude.com/docs/en/hooks). The `if` field uses permission rule syntax matching against commands: assignments stripped (`FOO=bar git push` matches `Bash(git *)`), subcommands checked (`npm test && git push`), nested commands examined (`$(rm -rf /)`). However, this is **regex/glob pattern matching**, not structural AST parsing. CHANGELOG.md v2.1.232 notes "Bash command parsing of non-ASCII characters to match real shell word boundaries" - suggesting text-based parsing.

### Security guidance plugin: LLM-based, not structural
The security-guidance plugin has three layers: (1) pattern warnings via regex, (2) **LLM diff review** sending diffs to Opus 4.7, (3) agentic commit review using Read/Grep/Glob (security-guidance/README.md). The review prompt explicitly mentions checking "regexes, URL/path parsers, allowlists, content-type checks, decoders, **AST/shell parsers**" for parser differentials (security-guidance/hooks/review_api.py:88). But this is an **LLM instructed to look for these patterns**, not Claude Code itself parsing structurally.

### No tree-sitter in evidence
Extensive search of CHANGELOG.md (500+ lines of grep output) and plugin code shows **no references to tree-sitter**, no AST parsing for permission granularity, no syntax-aware diffs. The bash permission checking is text-based pattern matching. Edit/Write tools operate on file paths and string replacement, not ASTs.

**R1 verification**: R1 mentioned "bash command parsing for permissions" but implied structural parsing. Lens B finds this is text-based pattern matching, not tree-sitter. **Finding: Claude Code lacks tree-sitter/syntactic awareness** - this is a gap relative to systems like opencode (which R1 says "parses shell commands with tree-sitter for permission patterns").

## Lens C - Correctness feedback and loop detection

### Correctness feedback loops
- **Security guidance plugin**: Post-turn diff review sends findings back to Claude via Stop hook, feeding high-severity issues back into the same turn (security-guidance/README.md)
- **LSP diagnostics**: CHANGELOG.md v2.1.232 shows "multi-second UI stalls after editing a file with thousands of IDE diagnostics while the IDE extension is connected" - LSP diagnostics flow back through the extension, but docs don't show this as tool-result feedback inside CLI turns
- **No verify gates in CLI**: No evidence of verify gates, diff previews, or assertion tooling in CLI flow from docs/CHANGELOG

### Loop detection mechanisms
Claude Code implements **multiple budget/cap mechanisms** to detect and break loops:
- **`CLAUDE_CODE_MAX_SUBAGENTS_PER_SESSION`**: Default 200, stops runaway delegation loops; `/clear` resets budget (CHANGELOG.md)
- **`CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS`**: Default 20, caps concurrent spawns (CHANGELOG.md)
- **`CLAUDE_CODE_MAX_WEB_SEARCHES_PER_SESSION`**: Default 200, stops runaway search loops (CHANGELOG.md)
- **`--max-budget-usd`**: Stops at cap; new spawns denied, running background agents halted (CHANGELOG.md v2.1.232 fix)
- **Subagent depth limit**: Default 3 layers, fork mode inherits depth (CHANGELOG.md)
- **Stall guards**: "60-second stall guard" for AWS credential resolution (CHANGELOG.md), "30-second connect timeout" for MCP
- **Rate limiting with bounded backoff**: Web search/fetch (CHANGELOG.md)
- **cgroup memory limits**: `CLAUDE_CODE_TOOL_MEMORY_LIMIT` for runaway builds (CHANGELOG.md v2.1.233)

### Doom-loop signals
- **Auto-mode consecutive-block limit**: CHANGELOG.md v2.1.232 mentions "auto mode counting a safety-filter refusal of its own permission check toward the consecutive-block limit"
- **Crash loops**: "background workers crash-looping when a client resets its connection" (CHANGELOG.md)
- **Retry loops**: "Fixed a retry loop that re-sent identical doomed requests after a context-overflow error" (CHANGELOG.md v2.1.232)
- ** stalls**: Multiple fixes for "UI stalls", "event-loop stalls", "indefinite hang" scenarios (CHANGELOG.md)

**R1 verification**: R1 mentioned "rate caps" and "stall timers" generically. Lens C finds specific mechanisms: per-session caps on WebSearch/subagents, budget enforcement, consecutive-block tracking in auto mode. **Finding: Claude Code has loop detection via caps/budgets but minimal in-turn correctness feedback** - LSP diagnostics flow through IDE extension, not CLI tool results. Security-guidance plugin provides one pattern (Stop hook review) but this is plugin-level, not core harness.

## R1 verification

### R1 findings that held
- **Hooks stdin-JSON/exit 2 blocking**: Confirmed. Exit 2 blocks irreversibly on key events (code.claude.com/docs/en/hooks)
- **UserPromptSubmit/UserPromptExpansion/SessionStart as context-injection events**: Confirmed (code.claude.com/docs/en/hooks)
- **Subagent model resolution**: Confirmed env > invocation > frontmatter > main (code.claude.com/docs/en/sub-agents)
- **MCP namespacing (`mcp__server__tool`)**: Confirmed (code.claude.com/docs/en/hooks)
- **Trust gate on repo-supplied configs**: Confirmed via CHANGELOG.md trust fixes
- **Settings layering (managed > user > project > local)**: Confirmed (code.claude.com/docs/en/settings)
- **dontAsk auto-denies for headless**: Confirmed (code.claude.com/docs/en/permissions)
- **stream-json NDJSON with parent_tool_use_id**: Confirmed (code.claude.com/docs/en/cli-reference)
- **Fork inherits cache, regular subagents separate**: Confirmed (code.claude.com/docs/en/prompt-caching)
- **Outbound-only relay, rate-limit/dedup/cap protection**: Confirmed (CHANGELOG.md Remote Control work)
- **Permission relay as short echoed id**: Confirmed (CHANGELOG.md cross-session messaging)

### R1 findings that need correction
- **Hooks evaluation order**: R1 said "hooks > deny > ask > mode > allow > canUseTool". Docs show: deny > ask > allow for permission rules; hooks are a separate `PreToolUse` filter that can inject `permissionDecision` (code.claude.com/docs/en/permissions, code.claude.com/docs/en/hooks)
- **Bash parsing for permissions**: R1 implied structural parsing; actual implementation is text-based pattern matching (regex/glob), not tree-sitter (Lens B finding)
- **"Only UserPromptSubmit/UserPromptExpansion/SessionStart may inject context"**: This held correct, but R1 didn't cover that `UserPromptExpansion` can also block via `decision: "deny"` (code.claude.com/docs/en/hooks)

### What R1 missed
- **Prompt caching details**: Layered ordering (system/project/conversation), cache-busting actions, provider-specific TTL (Lens A)
- **Deferred MCP tools**: Default behavior that keeps MCP tools out of prefix to preserve cache (Lens A)
- **Loop detection mechanisms**: Specific caps (WebSearch 200, subagents 200/session, concurrent 20), budget enforcement, consecutive-block tracking (Lens C)
- **Security-guidance plugin pattern**: Stop hook feeding LLM review findings back into same turn (Lens C)
- **Fork mode**: On-by-default behavior inheriting full conversation + cache (CHANGELOG.md v2.1.232)
- **Cross-session SendMessage/ListAgents**: Messaging fabric across sessions on same machine (CHANGELOG.md v2.1.232)

## Most important recommendation

**Build an addressed event stream as the primitive interface**, not an afterthought. R1 recommended "addressed NDJSON event stream as the ONLY interface" - this remains right. Claude Code's Remote Control (`SendMessage`, `ListAgents`) and hooks (stdin-JSON in, JSON out) are halfway there, but they're layered atop a CLI-first model. For a harness that puts cross-agent chat fabric first (seed requirement #1), every interaction should be an event with sender/recipient from the ground up. The event stream becomes the canonical representation: CLI, TUI, web, and app are all views onto the same event log. This gives you message relay, cross-session messaging, permission audit, and compaction/replay for free. Claude Code's Remote Control work proves this pattern works; the lesson is to architect it in from day one, not bolt it onto a CLI session model.

Source: code.claude.com/docs (all URLs cited), CHANGELOG.md v2.1.224-v2.1.233, security-guidance plugin README.md and hooks/review_api.py
