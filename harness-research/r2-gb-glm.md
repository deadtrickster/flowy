# r2-gb-glm: xAI grok-build research

## Core architecture

**One-protocol-many-transports (ACP)**: grok-build uses ACP (JSON-RPC) end-to-end. The relay forwards frames verbatim into the agent (`to_agent_tx.send(raw_frame)` at relay.rs:544), and local disk is truth with a cursor (`relay_sync.json`) for replay after reconnect. This matches R1's claim that ACP is the only protocol.

**Leader process**: One leader process per machine at `~/.grok/leader.sock` (leader/mod.rs:8-33). The leader manages the agent state while multiple clients (TUI, IDE, headless) communicate via Unix domain sockets. The relay runs as a background connection regardless of leader mode.

**ApiBackend capability predicates**: Three wire dialects—ChatCompletions, Responses, Messages (types.rs:66-70). Capability predicates include `supports_native_schema()` (Messages API does not) and `requires_reasoning_strip()` (only Messages rejects replayed reasoning blocks) (types.rs:73-81). The system branches on dialect capability, not provider name.

**Subagents**: Three built-in types (general-purpose, explore, plan) as `BuiltinAgentName` enum (builder.rs lines referencing `BuiltinAgentName::GeneralPurpose|Explore|Plan`). User-defined agents discovered from `.grok/agents/` and `.claude/agents/` directories (discovery.rs:18-19), with name-based dedup where project-level shadows built-ins. Only 3 built-in subagent types are advertised to the model.

**Interjection envelope**: Mid-turn human messages wrapped in `<user_query>` with a "user sent a message while you were working" note and 25k truncation threshold (format.rs:4-5, 12-18, 27-39). The envelope includes an "Make sure to complete any unfinished tasks from previous turns" trailer (format.rs:8-9).

## Lens A - caching

**Cache-aligned summarization**: Compaction explicitly preserves tool I/O and images to keep the prefix matching the engine cache (compaction_utils.rs:6-7). The system strips reasoning blocks when the provider rejects mutated thinking blocks (compaction_utils.rs:46-47).

**Prompt cache awareness**: Image budget management considers KV-cache impact—thresholds ensure images stay in place so the KV-cache prefix is stable (image_budget.rs comments). The system avoids re-busting the prompt cache on every turn once content stabilizes.

**Client-side prefix discipline**: On reconnect, the replay cursor ensures prefix continuity. However, the harness does NOT implement explicit cache control breakpoints like Anthropic's `cache_control`. Caching is implicit through stable prefix construction rather than explicit cache management directives.

**Gap**: No explicit cache_control mechanism or provider-specific cache hit accounting surfaced to the user. The system relies on deterministic replay and stable prefix construction rather than explicit cache management like Anthropic's system.

## Lens B - tree-sitter and syntactic awareness

**Structural code navigation**: `xai-codebase-graph` uses tree-sitter for go-to-definition and go-to-references (navigation.rs:1-4). The system maintains a PARSER_CACHE and QUERY_CACHE (manager/builder.rs) for performance.

**Language-aware parsing**: Tree-sitter grammars enable identifier-like node detection (navigation.rs lines referencing `is_identifier_like`). This supports symbol navigation and code understanding beyond text search.

**Gap**: No evidence of tree-sitter for shell command parsing (permissions), AST-aware edits, or syntax-aware diffs. Structural parsing is limited to code navigation, not command interpretation or file editing safety.

## Lens C - correctness feedback and loop detection

**Circuit breaker**: Shared `xai-circuit-breaker` crate with sliding-window algorithm (lib.rs:3-11). Trips when `sample_count >= min_samples AND error_rate >= error_rate_threshold`. Protocol-agnostic `Outcome` classification with HTTP and gRPC helpers.

**Doom loop detection**: Test support includes `response.doom_loop_check` events carrying cumulative triggers (sse.rs lines). The system can detect repetitive patterns and signal doom-loop conditions.

**Cache-aligned correctness feedback**: Compaction preserves tool results and images so the prefix matches what the engine cached, ensuring verification runs against the same context that generated errors.

**Client-side loop detection**: Budget-based token limits (`max_tokens.saturating_sub(...)` in compaction_utils.rs) and repetition guards in test support. However, the doom-loop machinery appears primarily server-side—the client's role is limited to detection and signaling.

**Gap**: No evidence of client-side intervention mechanisms (circuit breakers that actually stop the loop, stall timers, or forced tool-call pattern detection). The client detects but relies on server-side machinery for intervention.

## R1 verification

**Verified claims**:
- ACP as the only protocol: Confirmed—relay forwards frames verbatim (relay.rs:544)
- Leader process per machine: Confirmed—`~/.grok/leader.sock` (leader/mod.rs)
- ApiBackend dialect capability predicates: Confirmed—ChatCompletions/Responses/Messages with `supports_native_schema()` and `requires_reasoning_strip()` (types.rs:66-81)
- Interjection envelope: Confirmed—`<user_query>` with 25k truncation and unfinished-tasks trailer (format.rs)
- Bundle checksum manifest: Confirmed in xai-grok-bundle

**Challenged/missing claims**:
- "Three built-in types, 3 advertised to the model": Found only 3 built-in types total (general-purpose, explore, plan), not a larger subset where only 3 are advertised
- "Rhai-scripted deterministic orchestrator": No evidence found in the cloned code—this may be server-side or removed
- "Foreign sessions: read-only scrape of Claude/Codex/Cursor SQLite": Not found in client code—likely server-side only

**What R1 missed**:
- Circuit breaker implementation (xai-circuit-breaker) is more sophisticated than described
- Tree-sitter usage is limited to code navigation, not shell command parsing
- Cache management is implicit through stable prefixes, not explicit cache_control
- Doom-loop detection exists but is primarily server-side signaling

## One recommendation

**Adopt cache-aligned summarization with explicit stable prefix guarantees**: grok-build's approach of preserving tool I/O and images during compaction to match engine cache behavior is a pattern worth copying. However, add explicit cache_control breakpoints at stable boundaries (like post-compaction) to guarantee cache hits across provider reconnects, not just rely on implicit prefix stability.

Combine this with grok-build's replay cursor mechanism for session continuity—flowy's append-only event DAG already provides the deterministic replay foundation; explicit cache markers would make prefix behavior provider-visible and verifiable.
