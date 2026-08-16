# Round 2 research brief - read this, then execute YOUR section

You are one of sixteen researchers over two rounds (four topics; each topic runs on a
claude agent and a glm agent; round 1 is done, you cannot see it, work blind). You work
in an isolated VM over a copy of the flowy repo. You have MCP tools in this VM:
source-grep (ripgrep/read over the host's project repos) and oracle-ask (a docs corpus) -
use them where they help. Everything you need is in this brief; nobody can correct you
mid-run.

THE USER'S GOAL: a custom CLI coding-agent harness for their own use, with REMOTE
CONTROL via web and mobile app.

THE SEED (non-negotiable requirements):
1. Cross-agent chat fabric BUILT IN - every agent and human in the system can talk to
   every other while working.
2. Pluggable mediums for subagents - claude, glm, grok, opencode, local, selectable
   per spawn.
3. CLI-first; web and app are co-equal remote clients of one API.

OUR EXISTING DESIGN (read it, in this repo): README.md documents flowy - typed scoped
artifacts over an append-only event DAG, permission-filtered reads enforced in SQL,
ed25519 row signing, HLC federation, MCP shared memory, one /api with console/TUI/MCP
as three clients. HANDOFF.md has current state.

## THREE MANDATORY CROSS-CUTTING LENSES

Round 1 covered each system's core. Round 2 adds three lenses; each gets its own
section in your report, applied to YOUR system, with the same file:line / URL + quote
grounding as everything else. A lens your system genuinely lacks is a finding - say so.

### Lens A - caching
How does this system maximize provider prompt-cache hits? Stable prefix ordering,
log-derived context projection, no mid-session history mutation, deterministic replay,
checkpoint/resume reuse. Where are cached vs uncached tokens accounted and surfaced to
the user? What is provider-specific (deepseek automatic context caching vs anthropic
explicit cache_control breakpoints vs openai automatic)? What must OUR harness
guarantee so a steer or a tool result does not bust a 90 percent prefix? The user
heard the deepseek harness excels here - if your system is deepseek, this lens is why
you were spawned; make it precise.

### Lens B - tree-sitter and syntactic awareness
Where does the system parse code or commands STRUCTURALLY rather than as text?
Tree-sitter grammars (for shell-command permission patterns, AST-aware edits,
syntax-aware diffs, symbol navigation), language servers, any grammar-driven layer.
What does structural parsing buy - safer edits, permission granularity, diagnostics?
What does OUR harness need?

### Lens C - how the harness helps the LLM generate CORRECT code, and detects loops
Correctness: what feedback loops close on the model inside the turn - LSP diagnostics
returned as tool output, type errors fed back, tests run in the loop, verify gates,
diff previews, assertion tooling? Loop detection: how does it notice and break a
stuck agent - repetitive tool-call detection, doom-loop signals, budgets, stall
timers, circuit breakers? What are the concrete signals and the concrete
interventions? What should OUR harness do?

## Topics

### r2-scout-claude / r2-scout-glm - Claude Code from the public record
Official docs (code.claude.com/docs, docs.anthropic.com/en/docs/claude-code),
CHANGELOG.md in anthropics/claude-code, Anthropic engineering posts. Core areas:
hooks (exit-2 semantics), subagents, MCP client, permission modes and settings
layering, headless/print + SDK, background daemon, remote control (web + app,
cross-session messaging, channels). THEN the three lenses: prompt caching in Claude
Code (how the CLI marks cache breakpoints, what busts them), syntactic awareness
(any structural parsing - bash command parsing for permissions, AST edits?), and
correctness/loop machinery (verification, linters, rep-suppression, rate caps).

### r2-ds-claude / r2-ds-glm - DeepSeek's agent harness
Find it (deepseek-ai GitHub org; the user calls it "the deepseek harness"), clone
under /tmp, NOT into this repo. Core: agent loop, tool definition, subagents
(SubagentProvider registry), provider abstraction, config/permissioning,
extensibility. THEN the three lenses - lens A is your headline: the caching story in
full. Tree-sitter/structural parsing wherever present. Correctness feedback and loop
detection in the loop.

### r2-gb-claude / r2-gb-glm - xAI grok-build
github.com/xai-org/grok-build, clone under /tmp. Core: one-protocol-many-transports
(ACP), leader process, relay as frame forwarder, ApiBackend capability predicates,
subagents/personas, interjection envelope. THEN the three lenses: caching (client-side
prefix discipline across a relay reconnect), structural parsing (Rust tooling,
tree-sitter anywhere), and correctness/loops - the doom-loop machinery is
server-side here; find what the CLIENT can do without a server (budgets, repetition
detection) and say what is knowable.

### r2-oc-claude / r2-oc-glm - opencode
github.com/sst/opencode, clone under /tmp. Core: client/server split, provider
catalog, plugins, LSP, TUI, permissions (event + deferred + reply), multi-model
subagents. THEN the three lenses: caching (ai-sdk cache_control handling, what the
harness does to prefixes), tree-sitter (opencode parses shell commands with
tree-sitter for permission patterns - dig into that machinery and anything else
structural), correctness/loops (LSP diagnostics as tool output is the known one -
find the rest: repetition guards, budgets).

## FOR THE GLM TWINS ONLY (r2-*-glm)

You start one round ahead: read harness-research/R1-HIGHLIGHTS.md, especially your
topic's section - those are round-1 (claude-side) findings, cited to code. Treat them
as claims to VERIFY or CHALLENGE against the code you clone, not facts to repeat:
where your reading differs, the disagreement is the valuable part and must be stated
openly with your citations. Spend the depth you would otherwise spend re-deriving the
basics on the three lenses - that is why round 2 exists. Your report gains one
mandatory section: "R1 verification" - which round-1 highlights held, which did not,
what round 1 missed.

## Deliverable (every slug)

harness-research/<your-slug>.md, under ~180 lines. Structure: your topic's core
areas, then Lens A, Lens B, Lens C as titled sections, then your single most
important recommendation for the user's harness. Every claim carries file:line from
code you cloned or URL + quote. No unsourced best practices. Write nothing outside
harness-research/. Commit:
git add harness-research/ && git commit -m "research: <your-slug>"

When done or blocked: firecode chat --as <your-slug> --to flowy-glm "done - one
line" (or "blocked - why"), then exit.
