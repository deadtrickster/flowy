# The harness - my architecture

Written by flowy-glm 2026-08-16, from sixteen grounded sources: four round-1 reports
(claude), eight round-2 reports (claude + glm, three lenses), the tree-sitter-for-codegen
run, and the claude-host practitioner thread. Citations point at the reports under
`harness-research/` and the run trees; the claims inside them carry file:line / URL.

The goal, from the user: a custom CLI coding-agent harness with remote control via web
and mobile app. The seed: cross-agent chat fabric built in; pluggable mediums for
subagents; CLI-first with web and app as co-equal clients.

## Thesis

**Flowy is the substrate; the harness is its agent runtime.** One signed, addressed,
append-only event fabric carries every interaction - a tool call, a subagent's output, a
prompt typed on a phone, a peer agent's message, a permission verdict - and every
participant (human, CLI, web, app, claude, glm, grok, opencode, a local model) is a
reader and a writer of that stream, not a feature layered on it. Four independent
research lines converged on this from different directions: scout-claude and scout-glm
both name the addressed event stream as their one recommendation; ds-claude says "hang
the spawn tree off the messaging"; ds-glm says "subagents are just one addressing mode -
`spawn:child` vs `message:peer`"; grok-build proves one-protocol-many-transports at
397k LOC of Rust; opencode proves blocking-interaction-as-event is what makes remote
clients peers rather than viewers.

Flowy already has the hard 80%: typed scoped artifacts over an event DAG, reads
permission-filtered in SQL, ed25519 row signing, HLC federation, one `/api` with
console/TUI/MCP as three clients. What follows is the 20% that turns it into an agent
harness, and each piece is grounded in what the fleet read.

## Five load-bearing decisions

### D1 - One protocol, many transports

Adopt grok-build's invariant (`r2-gb-claude.md`): define the agent protocol once, then
make CLI, TUI, web and app *transports* of it, never re-implementations. Grok's relay
forwards frames verbatim into the agent (`relay.rs to_agent_tx.send(raw_frame)`); the
relay's entire inbound job is one `send()`. Our equivalent: the event stream over
`/api`, with NDJSON as the wire format (scout-claude: `stream-json` + the last line a
`result` message; `parent_tool_use_id` reconstructs the subagent tree - flowy's event
DAG already has the shape). Local clients hit `/api` directly; remote web/app reach it
through an outbound-only relay (scout-claude: the machine opens no inbound ports, the
relay is a rendezvous); reconnect replays from a cursor, dedup by event id (gb: "local
disk is truth, relay is a mirror" + `relay_sync.json`).

The conversation belongs to the long-lived node, never to the client (`r2-gb-claude`
Lens A): reconnect changes nothing about the prompt, so a phone on a train is safe.

### D2 - The request contract (the caching spine)

Adopt deepseek's invariant verbatim (`r2-ds-claude.md`, Lens A - the strongest single
finding in the corpus): **a model request must be a pure function of the append-only
event log plus content-addressed objects, and nothing may mutate a built request.**
"Model-visible means durably referenced; anyone holding the log reconstructs every
request byte-for-byte. Prefix-cache stability is corollary #1 - **stability is
emergent, not managed**."

**Source-level verification (post-review, 2026-08-16):** per the orchestrator's
confidence rule - a docs-only read was refuted by a binary read once already - every
load-bearing sub-claim of D2 was re-verified in a fresh shallow clone by flowy-glm,
not taken from any report: `deriveMessages()` at `packages/core/session/src/index.ts:726`
with the projection rule documented at `surface.ts:74`; the frozen request builder
(`deepFreeze` + `markAgentLoopRequest`) at `agent.ts:428,486`; the **independent
reconstructor as real code** at `packages/core/agent-loop/src/invariant.ts:39`
(rebuilds through a fresh `deriveMessages()` - the reports knew this only from dsh's
internal notes); the with-key e2e asserting `expect(usage!.cacheReadTokens ?? 0)
.toBeGreaterThan(0)` at `tests/request-cache.e2e.ts:93`; the four disjoint token
buckets at `token-meter/src/projection.ts:14-17`; the retained-text
RuntimeContextProjection at `runtime-context.ts:19-27`; and the compaction prefix
replay in `compaction-basic` ("reuses the provider's warm prefix cache instead of
invalidating it" - in-repo docs describing the engine, corroborated by the code
around it). D2 is code-read, twice, by independent readers.

Three amendments the review forced, now part of the contract:

- **The contract is node-local and grant-frozen.** The projection is a function of
  (log, grant-state-at-epoch, principal), not the log alone - flowy's reads are
  permission-filtered in SQL and grants change constantly (assignment IS a
  share-grant). So the EpochHeader carries, content-addressed, the effective grant
  set (or resolved visible-id set) it was built under; a fresh reconstructor at T2
  must rebuild against the *frozen* projection or it tests a projection that no
  longer exists. Every grant change to a read resource is a new epoch - the
  warm-prefix promise holds *between* grant changes, and the hit-rate metric says so
  rather than implying a constant 90%. And under HLC federation the invariant holds
  within one node's causal cut: the conversation belongs to the node (D1), and the
  relay does not move that boundary.
- **Signing is tiered, not uniform.** Full ed25519 row-signing on every intra-turn
  micro-event would put a sign + DAG write + HLC tick on the tool-call hot path,
  a volume the signing path was built for federation-crossing rows, not telemetry.
  Local-only events carry a lighter tier and are signed lazily when they cross a
  node boundary; the tier is a property of the event recorded in it.
- **The log needs a retention story.** If requests are functions of the log forever,
  the log and the object store are load-bearing indefinitely and grow unboundedly.
  What is prunable is what no live epoch references; the reconstructor's behavior
  against a pruned tail (reconstruct-forward-from-checkpoint, bounded
  byte-exactness) is named before the first GB, not after.

**Translation note:** the borrowed vocabulary is TypeScript (`deepFreeze`,
`undefined`); in Go the guarantee is immutability by copy and convention at the
projection boundary - the copy must be made where the freeze is claimed, or the
idiom imports a guarantee the language does not provide.

Mechanically:
- `deriveMessages()` over the event DAG, cached projection, deep-frozen outputs.
- An `EpochHeader` event (model, provider, rendered system prompt, tool schemas)
  written as a *full snapshot* with reason `initial`/`resume`/`change`; anything not
  history lives in the epoch, so a config change is a new epoch, not a mutated prefix.
- An **independent reconstructor** companion that rebuilds each request through a
  fresh session "so the live cache cannot vouch for itself", and a with-key e2e
  asserting `cacheReadTokens > 0` on every request after the first.
- Injections (steers, peer messages, tool context) are appended `user/message`
  events claimed by the *next* step's batch - "paid once and prefix-cached
  thereafter". The open step is the reconstruction boundary.
- Mutable runtime state (approval policy, cwd) never interpolates into the system
  prompt; it is a runtime-context snapshot appended only when its text changed (ds
  `RuntimeContextProjection.project()` returns `undefined` on unchanged text).

On top of the invariant, the tactical layer, from the other systems:
- Breakpoints: opencode marks exactly four messages (first two system, last two
  non-system) and collapses the system prompt to two strings so two breakpoints always
  cover it (`r2-oc-claude`); grok keeps a fourth slot free for gateway auto-caching.
  Branch on *dialect capability*, never provider name (gb `ApiBackend` predicates,
  each tested against the real wire mapping - "a key that never reaches the wire looks
  like a 0% cache hit, not a bug").
- Compaction reuses the warm prefix: ds replays the last routed prefix and appends the
  directive as a trailing user message; grok's pass-1 NOTE is keyed by prefix
  fingerprint and dropped on mismatch; grok preserves tool I/O and images during
  compaction so the prefix still matches the engine cache (`r2-gb-glm`).
- Hysteresis for evictions (gb images: rewrite once, then stable for many turns);
  midnight gets a one-shot reminder, never a rewritten header.
- Deferred tool descriptions (scout-glm: Claude Code keeps MCP tool descriptions out
  of the prefix by default) - our tool schemas ride the EpochHeader, so a tool-set
  change is an epoch change, priced honestly. They froze tool-schema bytes mid-session
  and removed dynamic content from tool descriptions for exactly this reason
  (take-3, CHANGELOG).
- Refuse rather than allow-and-cost: Claude Code's mid-session edits to CLAUDE.md or
  output style **do not apply** until the next clear - "the edit also doesn't apply"
  (take-3). Prefer rewind-to-a-cached-prefix over compact (`/rewind` "truncates back
  so the next request hits the earlier cache entry"). And on fan-out: "briefly hold
  all but the first so their first requests can read the prefix the first agent
  cached" - our parallel researchers should inherit a warm prefix, not each pay full
  price.
- Accounting: four disjoint buckets (`uncachedInput`, `output`, `cacheRead`,
  `cacheWrite`), hit-rate surfaced (`cacheRead / (uncached + cacheRead + cacheWrite)`,
  ds renders "Cache hit {percent}%" in the UI; opencode folds everything into one
  number - do not copy that). Every plugin declares its "KV Cache effect" in docs,
  CI-checked, the way 215 of 268 ds packages do.
- The cache-busting taxonomy, written down (scout-glm Lens A): model switch, effort
  change, tool-set change (epoch), compaction, mid-prefix mutation. In flowy one more:
  a permission-filtered read projecting *different rows into the same session's
  prefix* - the request contract must pin the projection to the epoch too
  (`r2-oc-claude` Lens A, "for flowy" note).

### D3 - The addressed fabric (the chat spine)

Every participant gets an identity on the fabric; a provider's job is "start a
participant and give it an identity" (ds-claude's one rec). Parent, child, peer, human
on a phone, agent in another VM - all addresses.

- **Mid-turn messages are interjections, not prompt rewrites.** Grok's envelope,
  verbatim (`r2-gb-claude`): queued at safe points (loop top, after a tool batch,
  before returning), FIFO, one message per entry never merged, 25k truncation, fixed
  frame - "the user sent a message while you were working" + `<user_query>` + "make
  sure to complete any unfinished tasks". This is what makes requirement 1 compatible
  with a 90% prefix hit: a steer is an append.
- **Blocking interaction is one primitive** (oc's one rec): append a request event to
  the DAG, block the agent on a future keyed by its id, any client resolves it by id
  over `/api`. Permission asks, questions, steers, handoff acceptance - all the same
  primitive, signed, auditable, restart-surviving (opencode keeps them in process
  memory and loses them; we write rows). "Always" pushes a rule and sweeps every
  pending it now covers; reject cascades and can carry a corrective message
  ("no, do it this way" is a first-class reply).
- **Two safety rules, non-negotiable** (scout-claude): a peer's message is never
  consent - it cannot answer a pending permission on your behalf; commands inside
  messages are inert text. Plus: rate-limit per sender, dedup identical repeats, cap
  the inbox at 50, gate on *sender identity*, not room identity. Claude Code also
  forbids asking *another* session for an action denied in your own - permission
  laundering is a named threat (take-3).
- **Inbound is three-valued and outbound is classified** (take-3): `crossSessionInbound`
  deliver/hold/refuse; and a cheap second model reviews every outbound message to
  another agent before delivery - "the classifier also reviews each message Claude
  sends with SendMessage", the exact surface they chose to classify. A chat message
  carries text plus an artifact *reference*, never a body - "flowy's MCP shared
  memory should return artifact refs, not bodies."
- **The sibling roster** (take-3): a subagent's initial context lists `main` and every
  other named agent in the session - one block that makes the in-session chat fabric
  legible to the model. Our fabric gives every participant this for free.
- **Approval fails closed when nobody is listening** (ds: missing answerer denies) -
  that is the *ceiling*. The *floor* is what makes it livable (review #6): a stated
  pre-authorized unattended policy - read-only, in-workspace, no-network actions
  auto-allow - so the phone is asked only for the genuinely consequential. You
  cannot ping a human for every `ls`.
- **Permission relay for remote approval** (scout): short echoed request id, first
  answer wins across terminal/phone/web; auto-notify the phone after several answered
  prompts in a session.

### D4 - Pluggable mediums (the provider seam)

- The seam is deepseek's `SubagentProvider` registry (ds-claude: "the piece dsh got
  right that nothing else in this space has"): name-keyed coexistence, capability
  flags (`outputSchema`, `depthLimit`, `toolFilter`, `persona`) checked **before**
  start with a loud typed rejection, config validated at mount, one model-facing tool
  per provider whose description derives from `inheritsParentContext` so the wording
  is truthful. dsh mounts claude-code as a 290-line file that shells out to the real
  CLI - subprocess + JSON is the portable medium path (scout: their own docs say to
  drive other loops this way).
- Branch on capability, not name (gb `ApiBackend`: `supports_native_schema()`,
  `requires_reasoning_strip()`, `forwards_prompt_cache_key()`), each predicate tested
  against the dialect's actual wire mapping.
- `adapterDefaults` (ds): mark which fields the adapter supplied so a default never
  fossilizes into user intent across a model switch.
- Medium as a permission pattern (oc): spawning is gated on the subagent type, so a
  phone can be asked "let this one run on grok?" - and **denies inherit down the
  spawn tree, allows never do**.
- Model-override provenance (gb): a model-chosen medium switch is treated differently
  from a harness-chosen one; refuse the former where you'd accept the latter.
- Continuable children (ds-glm's find, missed in round 1): background subagents with
  activations, cold resume, and an ownership graph - the `prepareContinuable`-style
  capability whose *presence is the capability*.

### D5 - The codegen layer (the user's correction: tree-sitter for generation, permissions second)

The field's dirty secret, from three independent reads: **nobody ships AST-anchored
edits** - opencode has *nine* fuzzy replacers, grok anchors on line hashes, dsh stops
at LSP. Meanwhile aider proves the structural layer pays: 58 `.scm` def/ref query tags,
personalized PageRank over the symbol graph, and a *measured* token-budget binary
search (`r2-ts-claude`). And grok builds a tree-sitter code graph - then never shows
it to the model.

Our design, from r2-ts-claude's recommendation - **split by proven-ness** (review #5):
the *proven and cheap* half ships now - the ranked repo map (aider: 58 `.scm` tags,
personalized PageRank, measured token binary-search) and the `has_error` syntax gate
(aider's `basic_lint` is the only implementation in the field). The *research bet* is
node-anchored edit application: parse, locate by node path + byte range, patch,
incremental reparse, store node path + byte range + pre-edit tree hash in the DAG -
nobody ships it, so it goes behind a flag with a proven fuzzy/line-hash replacer as
the v1 default until it earns trust on real edits. Symbol-graph retrieval feeds the
model relevant code; the graph is *shown to the model*, unlike grok's. ast-grep-style
YAML structural rules are the deterministic fallback when the model proposes a
pattern rather than a patch - with MCP dry-run before apply.

Permissions get the leftovers, and that is deliberate: tree-sitter bash/powershell
parsing for the permission *UX* - and every shipped system that does it does it
fail-closed: opencode's decomposition + arity table (`git commit -m x` remembers
`git commit`), grok's `spans_whole_script` guard, and - settled by take-3's capped
binary inspection against scout-glm's docs-only read - **Claude Code too**: bundled
tree-sitter grammars, a bash-analysis module walking `pipeline`/`redirected_statement`/
`command_substitution` nodes with typed refusals ("awk program contains system() which
executes arbitrary commands"), wrapper stripping (`timeout`/`nice`/`nohup`), one rule
per subcommand, and "when Claude Code can't fully parse a command, it asks for
approval". The *authority* boundary stays kernel-level confinement (ds Landlock:
fail-closed, self-restrict-then-exec, "never a silent unconfined run"). A mis-parsed
grant in our harness federates - so parsing may shape what the human is asked, never
what the sandbox allows.

## The correctness and loop ladder (Lens C)

Inside the turn:
- Diagnostics as tool output (oc: pull after edit/write, append to the same result,
  cap at 5 files, debounce 150ms, wait up to 10s - "the model waits for the compiler
  rather than guessing"; gb: 500ms budget, 10/file cap).
- CAS read-before-edit (ds: unseen file -> `FS_NOT_OBSERVED`; stale observed version
  fails loudly instead of clobbering).
- Structured output validated in-process and retried (gb jsonschema + retry).
- The `has_error` gate above - cheap, synchronous, pre-LSP.

Loops - a ladder, not a switch, assembled from the best rungs (rungs 1-3 plus the
day-one budgets are the MVP; 4-5 are refinements - review's smaller note):
1. Advisory nudge at 3 identical calls (ds `repeat-tool-reminder`, thresholds [3,5,8],
   riding `additionalContexts` - cache-free by construction; **denied calls count**;
   untracked bookkeeping calls are transparent to the chain).
2. A *permission ask* at 3 byte-identical calls (oc `doom_loop` - "the intervention is
   a human, not a heuristic"; on our fabric it lands wherever a human is).
3. Hard stop at 16 (gb: nudge at 8, stop at 16, 4 for true no-ops; end the turn with
   an explicit cancellation category, don't corpse it silently).
4. Semantic stall detection (gb verifier gap fingerprints: "no change in the flagged
   gaps between attempts -> iterating further is futile" -> pause).
5. Circuit breakers on provider errors (gb sliding window, min-sample floor); retry
   with jittered backoff honouring `retry-after` (oc), near-immediate resample
   "because a fresh sample is the remedy - waiting buys nothing" (gb).
6. Budgets: turns (`max_turns` with a wrap-up prompt on the last step - oc's
   `MAX_STEPS_PROMPT`), tokens, USD (`--max-budget-usd` halts new spawns), per-session
   caps per tool (Claude Code: 200 subagents, 20 concurrent, 200 searches), cgroup
   memory limits on runaway builds, subagent depth 3.

Take-3's finding over all of it: the shipped answer to doom loops is **cheap counters
at every recursion boundary, not a cleverness** - 8 consecutive stop-hook blocks
(then the harness overrides and ends the turn), 200 spawns, 20 concurrent, 50 queued
inbound messages, N dollars, N turns, plus byte-identical dedup. Put a counter on
every recursion boundary - spawn, stop-hook continue, inbound message, tool retry -
and make each counter *state why it fired* into the transcript so the model can adapt
rather than just stop. The one place a model earns its keep: the outbound classifier
review of cross-agent messages (above), which counters cannot do.
7. A per-turn *token and wall-clock* budget is the explicit gap in ds and oc - we ship
   one from day one.

## Remote control surface

- Outbound-only relay; no inbound ports (scout). The node polls/registers; execution
  stays local.
- Permission relay with the short echoed id, first answer wins (scout).
- Cheap-model one-line summaries per session for the phone list (scout: daemon row
  summaries are Haiku-class); the full tree on demand.
- Server-to-client control as addressed channels, not singletons (oc's `/tui/*` lesson:
  module-level queues mean two attached clients fight - address by client id).
- A supervisor that outlives every client, roster and job state as inspectable files
  (scout daemon) - in our case rows in the store, which is strictly better.
- Worktree-per-writer for parallel sessions (scout/oc); destroy only on explicit
  command (scout's skip note).

## What to skip (the negative space, argued)

- **Cordis-style total pluggability.** dsh pays with a docs tree holding a module
  graph, capability-seam graph, config catalog, event producer/consumer map and a
  postmortem directory - "that is the real cost of no privileged core" (ds-claude).
  One operator, one Go binary: a plugin DI container costs more than it returns.
- **Rhai/DSL orchestrators** (gb). The event DAG plus tasks is already the workflow
  substrate; a second scripting runtime is a liability.
- **Runtime npm installs to satisfy a model reference** (oc) - a config string must
  not execute an arbitrary tarball. Fixed adapter table.
- **Scraping other agents' SQLite files** (gb foreign sessions). Signed shared store
  is strictly better than read-only schema roulette.
- **Server-side doom-loop signals** (gb) - we don't own a model server; the whole
  client-side ladder above is what's knowable.
- **Thirty hook events, six permission modes, MDM plumbin.** Six events
  (SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop, SessionEnd), three
  modes (manual, accept-edits, deny-unlisted). Exit 2 blocks irreversibly, exit 1
  fails open, exactly three events inject context (scout).
- **Single-writer sync** (oc). The phone answering while the laptop writes is two
  writers; HLC keeps that (flowy already has it).
- **Anthropic SDK as our loop.** "Take the wire format, not the runtime" (scout) -
  and their own harness post argues against same-context plan+execute.

## Cross-model disagreements (flagged, not averaged)

- **ADJUDICATED, take-3 wins**: scout-glm read Claude Code's bash permission parsing
  as text-based pattern matching ("no tree-sitter in evidence", from docs + CHANGELOG
  searches); take-3 inspected the shipped binary with capped greps and found bundled
  tree-sitter grammars plus a bash-analysis module with typed refusal reasons and
  fail-closed behavior on unparseable input. Lesson recorded: a docs-only read cannot
  refute what is only in the binary. The architecture above carries the corrected
  finding.
- scout-glm corrects r1-scout-claude's permission evaluation order (SDK doc's
  `hooks > deny > ask > mode > allow > canUseTool` vs main docs' `deny > ask > allow`
  with hooks as a separate filter). Treat the SDK ordering as SDK-specific; our
  order: deny > ask > allow, hooks as an independent pre-filter that cannot override
  a deny.
- gb-glm found three built-in agent types where gb-claude cited twelve defined, three
  advertised (config.rs list vs the `BuiltinAgentName` enum) - likely definition-list
  vs advertised-enum; the design takeaway (advertise few, resolve many) survives
  either reading.
- gb-glm found no Rhai orchestrator or foreign-session scraper in the client clone;
  gb-claude cited both with paths. **Deferred, not adjudicated** (orchestrator's
  note): the fact itself is disputed - "we skip both" must not harden into "neither
  exists"; if a later phase wants an orchestrator, the question reopens unresolved.
- ds-glm challenges r1's "steer" inbox verb; the API surface shows `followup()` and
  `inject()`. The three-verb *concept* (next-turn / wake-now / no-wake) is what we
  keep; the name is theirs.

## Build order

1. **The request contract** (D2) over the existing event DAG: `deriveMessages`,
   EpochHeader event (model, tool schemas, *frozen grant projection*), the
   reconstructor, the `cacheReadTokens > 0` e2e. Build it against one hardcoded
   provider first; generalise when the D4 registry lands - D2 must not look blocked
   on D4. Everything else sits on this; it is also the cheapest to get right early
   and ruinous late.
2. **The addressed fabric** (D3): interjection envelope + safe-point drain, the
   request/future permission primitive, sender identity, the two safety rules.
3. **The provider registry** (D4): claude, glm, grok-build, opencode, local - each a
   provider; capability flags; subprocess+JSON where the medium is a CLI.
4. **The relay** (remote web/app): outbound-only, cursor replay, permission relay,
   cheap summaries. The console and TUI already exist as `/api` clients; the app is a
   third.
5. **The codegen layer** (D5): the `.scm` stream, repo map, node-anchored edits, the
   `has_error` gate, then the shell-parsing permission UX.
6. **The loop ladder** rung by rung, starting with nudge and permission-ask.

Each phase rides the same gate discipline the flowy phases used: build, verify from
clean, trust the harness line over the agent's word.
