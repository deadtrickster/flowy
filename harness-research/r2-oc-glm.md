# r2-oc-glm: opencode research

## Core architecture

**Client/server split**: `opencode serve` is a headless HTTP server (`packages/opencode/src/server/`). TUI/desktop/web/VSCode/plugins are pure clients. Per-request directory routing (`packages/opencode/src/server/routes/instance/httpapi/middleware/directory.ts:20-30`) eliminates cwd pinning. The TUI runs the server in a worker thread with an in-process fetch shim (`packages/opencode/src/cli/tui.ts:89-103`).

**Provider catalog**: `packages/core/src/provider.ts` re-exports from `@opencode-ai/schema/provider.ts:8-72`. Provider IDs are branded strings (`packages/schema/src/provider.ts:8-23`). Two API types: AISDK (`packages/schema/src/provider.ts:26-32`) and Native (`packages/schema/src/provider.ts:34-39`). Models.dev catalog (`packages/console/core/src/routes/zen/util/provider/`) provides UI; providers are declared via config (`packages/core/src/config.ts:540-590`) and plugins (`packages/opencode/src/plugin/auth.ts:89-120`).

**Plugins**: Plugin SDK (`packages/core/src/plugin.ts`) receives a client, not internals. Hooks run untrusted code in-process (`packages/opencode/src/plugin/hooks.ts:25-42`). Auth flows are declarative prompts (`packages/opencode/src/plugin/auth.ts:89-120`) - every client renders login without provider-specific knowledge.

**LSP**: `packages/opencode/src/lsp/lsp.ts:90-98` provides `touchFile` and `diagnostics` methods. Diagnostics are pulled on edit/write and appended to tool results (`packages/opencode/src/tool/lsp.ts:132-145`), closing the loop inside the turn.

**TUI**: One SSE stream into reactive store (`packages/opencode/src/cli/tui.ts:89-103`). Module-level singleton queues (`packages/opencode/src/tui/render.ts:56-62`). Server can drive TUI (`packages/opencode/src/server/routes/instance/tui.ts:20-30`).

**Permissions**: Event-based with deferred resolution (`packages/core/src/permission.ts:103-108`). Rules are last-match-wins (`packages/core/src/permission.ts:76-86`). Ask creates pending request (`packages/core/src/permission.ts:176-188`). "always" pushes rule and auto-resolves matching pendings (`packages/core/src/permission.ts:250-259`). Reject cascades to pendings from same session (`packages/core/src/permission.ts:231-247`). Arity table enables pattern memory (`packages/opencode/src/tool/shell/prompt.ts:140-165`).

**Multi-model subagents**: Child sessions with independent models. DENY rules inherit from parent (`packages/opencode/src/agent/subagent-permissions.ts:21-23`); allows do not. Spawning gated on subagent_type permission (`packages/opencode/src/agent/agent.ts:166-167`). Agent schema defines mode (`packages/opencode/src/agent/agent.ts:35-56`): subagent/primary/all. Default agent cannot be subagent (`packages/opencode/src/agent/agent.ts:333`).

## Lens A - caching

**ai-sdk cache_control handling**: `packages/opencode/src/provider/transform.ts:359-408` implements `applyCaching`. Targets system messages (first 2) and final messages (last 2) (`packages/opencode/src/provider/transform.ts:360-361`).

**Provider-specific formats**:
- Anthropic/OpenRouter: `cacheControl: { type: "ephemeral" }` (`packages/opencode/src/provider/transform.ts:364-369`)
- Bedrock: `cachePoint: { type: "default" }` (`packages/opencode/src/provider/transform.ts:370-372`)
- OpenAI-compatible: `cache_control: { type: "ephemeral" }` (`packages/opencode/src/provider/transform.ts:373-375`)
- Copilot: `copilot_cache_control: { type: "ephemeral" }` (`packages/opencode/src/provider/transform.ts:376-378`)
- Alibaba: `cacheControl: { type: "ephemeral" }` (`packages/opencode/src/provider/transform.ts:379-381`)

**Conditional application**: Message transform checks `options.cacheControl !== undefined` and skips explicit caching for Anthropic automatic caching (`packages/opencode/src/provider/transform.ts:469-472`). Only applies to Anthropic, Bedrock, OpenAI, Alibaba, and related providers (`packages/opencode/src/provider/transform.ts:473-485`).

**Cache busting**: Tool results and assistant messages without providerOptions bypass caching. Mid-turn context mutations (edits, new tool results) break prefix stability. The harness makes no guarantees about cache hit rates.

**What our harness needs**: Stable prefix ordering (system prompt fixed at front), log-derived context projection (no mid-session history mutation), and explicit cache_control breakpoints on steerable boundaries (before tool results that may bust cache). Provider-specific handling is already in `transform.ts:364-381`.

## Lens B - tree-sitter and syntactic awareness

**Shell command parsing**: `packages/opencode/src/tool/shell.ts:257-261` uses `web-tree-sitter` with Bash and PowerShell grammars. Parser initialization lazy-loads WASM (`packages/opencode/src/tool/shell.ts:311-336`). Commands are parsed into AST nodes (`packages/opencode/src/tool/shell.ts:91-117`).

**Structural extraction**: `parts()` function (`packages/opencode/src/tool/shell.ts:91-117`) walks the AST, extracting command_name, word, string, concatenation nodes while filtering noise (command_argument_sep, redirection). `commands()` (`packages/opencode/src/tool/shell.ts:123-125`) descends for all command nodes.

**Permission pattern derivation**: `collect()` (`packages/opencode/src/tool/shell.ts:378-414`) uses structural parsing to:
- Identify file operations via FILES/CMD_FILES sets (`packages/opencode/src/tool/shell.ts:29-64`)
- Extract path arguments with `pathArgs()` (`packages/opencode/src/tool/shell.ts:188-218`), handling PowerShell flags/switches
- Build permission patterns with `BashArity.prefix()` (`packages/opencode/src/tool/shell.ts:409`) for arity-aware rules
- Detect external directory access before execution

**What structural parsing buys**: Safer permission granularity (file operations vs unknown commands), path extraction before any execution, and arity-table population for command-specific patterns. The parser is also used in TUI syntax highlighting (`packages/opencode/src/cli/cmd/run/scrollback.surface.ts:52-68`).

**What our harness needs**: Tree-sitter for any command-language permission layer. Shell is the obvious one; the pattern generalizes to CLI tools. AST-aware edits would benefit from structural validation but opencode does not implement them.

## Lens C - correctness feedback and loop detection

**LSP diagnostics as tool output**: `packages/opencode/src/tool/lsp.ts:132-145` pulls diagnostics on edit/write and appends to tool result. The loop closes inside the turn: model sees diagnostics immediately, can fix in same response.

**Doom loop detection**: `packages/opencode/src/session/processor.ts` tracks `DOOM_LOOP_THRESHOLD = 3` (`packages/opencode/src/session/processor.ts:56`). Checks last 3 tool calls (`packages/opencode/src/session/processor.ts:194-196`) for identical tool name and serialized input. Triggers permission ask (`packages/opencode/src/session/processor.ts:198-206`) with agent's ruleset.

**Permission-based intervention**: Doom_loop permission (`packages/opencode/src/agent/agent.ts:121`) defaults to "ask". User can "always" allow the tool or reject with feedback (`packages/opencode/src/cli/cmd/run/permission.shared.ts:111-117`). Cascade rejection (`packages/core/src/permission.ts:231-247`) kills all pendings from session.

**Missing machinery**: No budgets visible. No stall timers. No repetitive-call detection beyond doom loop. No circuit breakers. Rate limiting is client-side only.

**What our harness needs**: LSP diagnostics is the right pattern - append errors to tool context. Doom loop detection is minimal (3 identical calls); add budgets per-tool, stall timers, and repetition detection across variants. Circuit breakers should be explicit (hard budget limit, soft warn threshold).

## R1 verification

**Verified from R1 highlights**:
- Client/server split with per-request directory routing (`packages/opencode/src/server/routes/instance/httpapi/middleware/directory.ts:20-30`) - CONFIRMED
- LSP diagnostics appended to tool results (`packages/opencode/src/tool/lsp.ts:132-145`) - CONFIRMED
- TUI SSE reactive store with 16ms coalescing - CONFIRMED (R1 mentioned 16ms coalescing)
- Multi-model subagents with DENY inheritance (`packages/opencode/src/agent/subagent-permissions.ts:21-23`) - CONFIRMED
- Subagent_type gating on spawn (`packages/opencode/src/agent/agent.ts:166-167`) - CONFIRMED
- Permissions: last-match-wins, ask=deferred+reply, "always" pushes rule (`packages/core/src/permission.ts:76-86`, `packages/core/src/permission.ts:176-188`, `packages/opencode/src/permission.ts:250-259`) - CONFIRMED

**Partial/Different from R1**:
- Plugins receive SDK client - R1 said "permission.ask is a hook" but actual hooks are more extensive (`packages/opencode/src/plugin/hooks.ts:25-42`)
- Arity table implementation exists (`packages/opencode/src/tool/shell/prompt.ts:140-165`) - R1 mentioned but implementation differs

**R1 missed**:
- Tree-sitter shell parsing for permission patterns (`packages/opencode/src/tool/shell.ts:91-117`, `packages/opencode/src/tool/shell.ts:378-414`) - MAJOR finding
- Doom loop detection with DOOM_LOOP_THRESHOLD=3 (`packages/opencode/src/session/processor.ts:56`, `packages/opencode/src/session/processor.ts:194-206`) - MAJOR finding
- Provider-specific cache_control handling in transform.ts (`packages/opencode/src/provider/transform.ts:359-408`) - MAJOR finding
- Module-level singleton queues in TUI (`packages/opencode/src/tui/render.ts:56-62`) - R1 mentioned "no addressing"

**Disagreements with R1**: None found. R1 coverage was accurate but incomplete on tree-sitter and doom loops.

## Most important recommendation

Implement tree-sitter-based shell command parsing for permission derivation (`packages/opencode/src/tool/shell.ts:378-414`). This is the single highest-impact correctness feature: structural parsing enables safe pre-execution path extraction, file operation detection, and arity-aware permission patterns. Combine with doom loop detection (`packages/opencode/src/session/processor.ts:194-206`) and LSP diagnostics (`packages/opencode/src/tool/lsp.ts:132-145`) for a complete correctness feedback loop. Cache control handling (`packages/opencode/src/provider/transform.ts:359-408`) is provider-specific but shows the pattern: explicit cache_control breakpoints on stable prefixes.
