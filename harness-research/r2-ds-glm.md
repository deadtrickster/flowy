# DeepSeek harness research - r2-ds-glm

## Core architecture

DeepSeek harness is a Cordis-based plugin system where "everything is a plugin" (README.md:7). The core agent loop drives sessions through turn/step boundaries, with every model request derived from an append-only session log via `deriveMessages()` (packages/core/session/src/index.ts:726-747). The invariant "model-visible means logged" is enforced - anything reaching a model must be reconstructable from the log (docs/architecture.md:96).

**Agent loop**: ReactLoopAgent implements the Agent interface (packages/core/agent-loop/src/agent.ts:64-97), managing phases (idle/maintenance/running) and scoped agent contexts. Turn flow follows `turn/start → agent/pre-step → step/start → agent/request → tool/call* → step/end → turn/end` (docs/architecture.md:67-82).

**Tool definition**: ToolDefinition extends ToolSchema with mandatory `output: ToolOutputDefinition` declaring canonical JSON Schema and a PURE `render()` projection (packages/core/tools/src/index.ts:222-288). Tools support `presentCall`/`presentResult` for UI projections and `finalizeContent` for last-mile content transformation. The tool registry executes through `tools/pre-execute → tools/execute → tools/post-execute → tools/result` waterfalls (packages/core/tools/src/index.ts:152-197).

**Subagents**: `ctx.subagents` is a name-keyed SubagentProvider registry supporting multiple named providers (docs/subsystems/subagent.md:1-8). Providers advertise static capability flags (`outputSchema`, `depthLimit`, `toolFilter`, `persona`) checked before start (docs/subsystems/subagent.md:27-33). Two modes: one-shot (via `SubagentProvider.start`) and continuable background children managed by an activation manager (docs/subsystems/subagent.md:115-156).

**Provider layer**: `{provider, model}` dispatch through the LLM adapter registry (docs/architecture.md:40-41). `adapterDefaults` marks adapter-supplied fields so defaults never fossilize into user intent (packages/core/agent-loop/src/agent.ts:54-61).

**Permissioning**: Approval waterfall "fails closed" when no answerer is connected (R1-HIGHLIGHTS.md:48). Two independent knobs: approval (allow/ask/deny) and sandbox (read-only|workspace-write|danger-full-access). Tool restriction is agent-scoped only; no allow-always mechanism (R1-HIGHLIGHTS.md:48-51).

**Extensibility**: Cordis patch system - bundles declare config rows, patches target rows by id and replace entire config (docs/architecture.md:20-28). Profiles compose ordered bundle layers with home-level cordis.patch.yml overlays.

## Lens A - caching (headline feature)

DeepSeek's caching strength is **automatic context caching** through log-derived context projection. Unlike Anthropic's explicit `cache_control` breakpoints, DeepSeek's provider handles caching transparently:

**Stable prefix ordering**: Every model request is projected from the append-only session log via `deriveMessages()` (packages/core/session/src/index.ts:726-747). The surface layer maintains `nodes: readonly number[]` in stable model-visible order (packages/core/session/src/surface.ts:136-142). This deterministic replay maximizes cache hits - the same log prefix always derives identical message arrays.

**Automatic provider caching**: DeepSeek adapter accounts for cached vs uncached tokens in usage reporting. The translate function subtracts `cached_tokens` from `prompt_tokens` to report disjoint counts: `inputTokens: usage.prompt_tokens - (cacheRead ?? 0)` (packages/llm/llm-deepseek/src/translate.ts:46-62). Wire format reports both `prompt_cache_hit_tokens` and `prompt_cache_miss_tokens` (packages/llm/llm-deepseek/src/types.ts).

**Cache-bust prevention**: The "model-visible means logged" invariant (docs/architecture.md:96) ensures mid-session history mutation cannot happen - all context is projected from the immutable log. Surface replacement operations shadow ranges rather than mutate in place (packages/core/session/src/surface.ts:44-68), preserving prefix stability.

**What our harness must guarantee**: Steer or tool results must not bust a 90% prefix. DeepSeek achieves this by (1) never mutating logged events, (2) projecting context deterministically from log position, (3) using surface replacement only for model-visible content. Any context injection goes through `Agent.inject()` which creates new inbox events rather than rewriting history.

## Lens B - tree-sitter and syntactic awareness

**DeepSeek has minimal tree-sitter/structural parsing**. Searches for "tree-sitter" and "treesitter" across packages/ return no results. The harness relies on:

**LSP for semantic navigation**: The LSP capability seam exposes four operations (`goToDefinition`, `findReferences`, `goToImplementation`, `hover`) through a generic stdio provider (docs/subsystems/lsp.md:1-19). Providers register by branded id with exclusive extension mappings; the seam exposes no protocol types or generic JSON-RPC escape hatch.

**What structural parsing buys**: LSP provides precise symbol navigation but NOT syntax-aware edits, permission granularity, or shell-command parsing. The tool layer focuses on JSON Schema validation (packages/core/tools/src/schema.ts) and pure content projection rather than AST manipulation.

**What our harness needs**: For shell-command permission patterns, we'd need tree-sitter bash grammar. For AST-aware edits, we'd need language-specific tree-sitter grammars and edit building. DeepSkip doesn't provide these - it stops at LSP-level semantic navigation.

## Lens C - correctness feedback and loop detection

**Correctness feedback loops**:

**LSP as tool result**: LSP tool returns structured results to the model (locations, hover content) but LSP diagnostics are NOT automatically fed back as tool results. The tool provides four precision operations (docs/subsystems/lsp.md:19-20) but no automatic diagnostic integration.

**Tool validation**: Tool definitions enforce JSON Schema validation with `valueSchemaSpecToJsonSchema` and `validateJsonSchemaValue` (packages/core/tools/src/index.ts:66-98). The execution pipeline validates arguments before tool body dispatch.

**Loop detection**:

**Budget mechanisms**: Agent loop exposes `maxParallelToolCalls` (packages/core/agent-loop/src/constants.ts:6) but no explicit turn/step budgets or repetition detection in the core loop.

**No apparent doom-loop detection**: Searches for "repetition", "stuck", "doom", "circuit breaker" in agent-loop core show no dedicated loop-breaking machinery. The loop relies on step boundaries and tool exhaustion but doesn't detect repetitive tool-call patterns.

**Cancellation**: AbortSignal propagates through `ToolExecution.signal` (packages/core/tools/src/index.ts:337) but this is caller-initiated, not automatic loop detection.

**What our harness should do**: Implement repetitive tool-call detection (e.g., same tool + args pattern), rate caps per tool, and stall timers. Add LSP diagnostic integration as automatic tool-result feedback. Close the correctness loop with type errors and lint results fed back mid-turn.

## R1 verification

**Verified claims**:

✓ **Log-derived context**: `deriveMessages()` projects from append-only log (packages/core/session/src/index.ts:726-747). Surface projection maintains stable ordering (packages/core/session/src/surface.ts:136-142).

✓ **ToolDefinition structure**: ToolDefinition extends ToolSchema with mandatory output schema and PURE render (packages/core/tools/src/index.ts:222-288). presentCall/presentResult projections confirmed.

✓ **Subagent registry**: `ctx.subagents` is name-keyed SubagentProvider registry (docs/subsystems/subagent.md:1-8). Capability flags validated before start.

✓ **Provider layer**: `{provider, model}` dispatch confirmed with adapterDefaults mechanism (packages/core/agent-loop/src/agent.ts:54-61).

✓ **Permissioning fails closed**: Approval waterfall and sandbox knobs confirmed (R1-HIGHLIGHTS.md:48-51).

**Challenged/clarified claims**:

✗ **Inbox verbs**: R1 mentions "followup/steer/inject" but code shows `Agent.followup()` as the primary continuation mechanism. `Agent.inject()` exists for context injection but "steer" as "next step + wake" doesn't match the actual API surface directly.

✗ **The gap - send_message limitations**: R1 correctly identifies send_message as depth-1 parent-to-child, but the actual gap is wider. DeepSeek's subagent system has `listChildren()`/`listDescendants()` for enumeration (docs/subsystems/subagent.md:287-290) and `SubagentRuntime.interrupt()` for stopping (docs/subsystems/subagent.md:144), but no true chat fabric - the model-facing tools provide delegation control only.

**What R1 missed**:

- **Continuable children**: Major feature - background subagents with Activations, cold resume, and ownership graph (docs/subsystems/subagent.md:115-156). R1 focused on one-shot but continuable is the more sophisticated mode.

- **Surface replacement**: Log can shadow ranges via surface operations rather than pure append (packages/core/session/src/surface.ts:44-68). This is key to cache-bust prevention.

- **Automatic context caching**: DeepSeek's automatic caching (vs Anthropic explicit) is the real headline advantage, not just "log-derived context."

## One most important recommendation

**Build a true chat fabric, not a delegation tree**. DeepSeek's subagent system excels at parent-child delegation but lacks peer-to-peer messaging. The "gap" identified in R1 is real: send_message is depth-1 parent-to-child only, with no answer channel. Flowy should implement:

1. **Room-based addressing**: Every agent and human gets an identity in a shared room. Messages carry explicit `from`/`to` addressing.

2. **Bidirectional messaging**: Unlike DeepSeek's send_message, support reply channels so children can message parents without new turns.

3. **Peer discovery**: list_agents should cover all participants, not just descendants. Enable horizontal collaboration.

4. **Combine with DeepSeek's strengths**: Keep the log-derived context projection and automatic caching, but layer chat fabric on top rather than inside the subagent seam.

The fabric becomes the primitive; subagents are just one addressing mode (`spawn:child` vs `message:peer`). This gives cross-agent chat (THE user requirement) while preserving DeepSeek's excellent caching and context projection.
