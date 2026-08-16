# Review - HARNESS-ARCHITECTURE.md

Reviewer: orchestrator (built flowy, so the review leans hardest where the harness meets flowy's own
internals). 2026-08-16. Not a rubber stamp - the verdict is ship it, and the concerns below are real.

## Verdict

Strong and grounded. The thesis - flowy is the substrate, the harness its agent runtime - is correct
and it is the non-obvious framing that makes the rest cohere. D2-first is the right spine and the
right sequencing. The negative space is argued, not asserted, and the cross-model disagreements are
flagged rather than averaged, which is the discipline this kind of synthesis usually skips. Build it
in the stated order. The concerns are about where the thesis meets flowy's specifics, worst first.

## 1. The request contract meets permission-filtered reads - the central unresolved tension

This is the one that matters and D2 treats it as a corollary when it is a load-bearing problem.

D2's invariant: a request is a pure function of the append-only log plus content-addressed objects,
and anyone holding the log reconstructs it byte-for-byte. But flowy's reads are permission-filtered in
SQL (`internal/store/perm.go` - `CanRead`, `ArtifactFilterSQL`, `EventFilterSQL`), and the filter is a
function of the reader's grants, which change constantly - assignment IS a share-grant. So the rows
that project into a session's prefix are not a function of the log alone; they are a function of
(log, grant-state-at-build-time, principal). Three consequences the doc should own:

- **Reconstruction needs a frozen projection, not just a frozen log.** The independent reconstructor
  "so the live cache cannot vouch for itself" rebuilds through a fresh session - but a fresh session
  at T2 re-runs the permission filter against the grant state at T2, which can differ from T1. Freeze
  the effective grant set (or the resolved visible-id set) into the EpochHeader alongside model and
  tool schemas, and content-address it, or the reconstructor and the `cacheReadTokens > 0` e2e are
  testing a projection that no longer exists.
- **Every grant change to a read resource is a cache bust.** A peer revoking a share, or a new
  assignment, changes the projection - a new epoch by the doc's own rule. On a collaborative fabric
  that is not rare. The warm-prefix promise is real only between grant changes; say so, and measure
  it, rather than implying 90% hit rates a multi-writer permissioned store will not always deliver.
- **Pin the projection to the epoch explicitly.** The doc's own "for flowy" footnote (permission-
  filtered read projecting different rows into the same prefix) is exactly this - promote it from a
  footnote in D2's tactical list to a first-class mechanism in the EpochHeader definition.

## 2. The "pure function of the log" invariant is node-local, not fabric-global

Under HLC federation the log is eventually consistent - two nodes can hold different merged states at
the instant a request is built. "Anyone holding the log reconstructs every request" is true only
within one node's causal cut. D1 already fixes the boundary ("the conversation belongs to the
long-lived node"), which is the right answer - so state it in D2 too: the reconstructor and the
contract are node-local, and the remote client reaching in over the relay does not move the boundary.
Without that sentence the two decisions look like they promise something federation cannot give.

## 3. Signing every intra-turn micro-event on the hot path - cost and tiering

Events are signed rows (`rowsig.go` - `canonicalEvent`/`SignEvent`, verified on the pull door in
`sync.go`). If every tool call, tool result, permission verdict and peer message becomes an event -
which the fabric thesis wants - each is an ed25519 sign + a DAG write + an HLC tick on the turn's hot
path. ed25519 is fast, but this is a per-tool-call volume the current signing path was built for
federation-crossing rows, not intra-turn telemetry. Decide deliberately: does an intra-turn event
need a signature before it crosses a node boundary, or can local-only events carry a lighter tier and
get signed lazily on replication? The doc should name this as a tiering decision, not inherit
full row-signing on the hot path by default.

## 4. Log growth and content-addressed GC - an operational gap

If requests are pure functions of the log forever, the log and the content-addressed object store are
load-bearing for reconstruction indefinitely, and both grow without bound. There is no retention,
log-compaction (distinct from context compaction), or object-GC story. For a single-operator node this
bites in weeks, not years. Name the retention model: what is prunable once no live epoch references it,
and how the reconstructor behaves against a pruned tail.

## 5. D5 is the field's unproven bet and it is sequenced last - de-risk it

The doc says plainly nobody ships AST-anchored edits - opencode has nine fuzzy replacers, grok anchors
on line hashes, dsh stops at LSP. Node-anchored edit *application* (node path + byte range + pre-edit
tree hash in the DAG) is therefore the most speculative component in the whole design, and it is phase
5. Split D5 by proven-ness: aider's `has_error` gate and the ranked repo map are proven and cheap -
take them. Node-anchored *application* is the research bet - ship a proven fallback (a fuzzy/line-hash
replacer) as the v1 default and put AST-anchored application behind a flag until it earns trust on
real edits. Otherwise D5 is the phase most likely to slip, and it is carrying the user's own priority.

## 6. Unattended policy posture - fail-closed needs a pre-authorized floor

"Approval fails closed when nobody is listening" is the right default, but a harness where every
permission ask can block on a sleeping phone is unusable, not safe. The relay + auto-notify handles
delivery; it does not handle the fact that you cannot ping a human for every `ls`. D3 has the mechanism
("Always pushes a rule and sweeps pending") - but the design needs a stated default policy floor: what
is auto-allowed unattended (read-only, in-workspace, no-network) so the phone is asked only for the
genuinely consequential. Fail-closed is the ceiling; the floor is what makes it livable.

## Smaller notes

- **D2 vocabulary is TS; flowy is Go.** `deriveMessages()`, deep-frozen outputs,
  `RuntimeContextProjection.project()` returning `undefined` - the invariant is sound but "deep-freeze"
  has no Go equivalent. Immutability here is by copy and convention; make sure the guarantee survives
  translation rather than being assumed from the borrowed idiom.
- **D2 depends on D4 for tool schemas.** The EpochHeader snapshots tool schemas, but which schemas a
  provider exposes is D4's job. Build D2's mechanics against one hardcoded provider first, then
  generalise when the registry lands - otherwise D2 looks blocked on D4 when it need not be.
- **The loop ladder is a v3 in one list.** Rungs 1-3 plus the day-one token/wall-clock budget are the
  MVP; semantic-stall fingerprints (4) and provider circuit breakers (5) are refinements. The build
  order says rung-by-rung - good - so just mark which rungs are MVP so the ladder is not read as
  all-at-once.

## Cross-model disagreements - my take (you asked)

The adjudication method across all five is sound: binary beats docs (#1), prefer the safer
composition (#2), keep the concept not the name (#3, #5), skip-either-way when the fact itself is
disputed (#4). No quarrel with any of the five calls. Two things to add.

- **#1 generalises into a corpus-wide confidence rule, and it is the most important line in that
  section.** A docs-only reader (scout-glm) was refuted by a binary-inspector (take-3) on a
  security-critical claim. The lesson is not just "docs cannot refute the binary" for that one
  finding - it is that every docs-only claim in the corpus that no second reader checked now sits at
  the same confidence scout-glm's did, which is to say lower than the doc treats it. The disagreements
  you caught are where two readers happened to overlap; the exposure is the docs-only claims where
  they did not. So the question the section should answer: which D1-D5 load-bearing claims are
  docs-only and unconfirmed? The one that matters most is **D2** - you adopt deepseek's request
  contract "verbatim" and call it "the strongest single finding in the corpus." Was that read from
  deepseek's source or binary, or from its docs and blog? If docs-only, the spine of the whole
  architecture rests on exactly the kind of claim #1 just taught us can be wrong, and it earns a
  source-level confirmation before D2 is built on it. Same question for the four-bucket cache
  accounting and the compaction-preserves-the-prefix claims.
- **#4 is deferred, not adjudicated.** gb-glm and gb-claude disagree on whether the Rhai orchestrator
  and the foreign-session scraper exist in the clone at all. "Skip either way" is the right build
  call, but it leaves a factual contradiction on the record - fine to skip both, just do not let "we
  skip it" harden into "it does not exist," because if a later phase ever wants an orchestrator that
  question reopens unresolved.

## What I would not change

D1's outbound-only relay and node-owns-the-conversation. D3's blocking-interaction-as-one-signed-
primitive (writing rows where opencode loses them in memory is strictly better and you already have
the store). The two safety rules and the permission-laundering threat. The whole negative-space
section. And leading the build with D2 - it is the cheapest to get right early and ruinous late, and
that judgement is correct.
