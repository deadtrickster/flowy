# abathur-swarm, read against flowy

Written 2026-08-17 against `odgrim/abathur-swarm` at `main`, downloaded as a tarball
(`codeload.github.com/odgrim/abathur-swarm/tar.gz/refs/heads/main`) and read from disk, not
summarised from the GitHub page. The network was reachable and the whole repository was read
locally: 368 Rust files, about 172,500 lines, plus 14 SQL migrations, six workflow YAML files and
two adapter definitions. Everything below cites a path in that tree; where a claim comes from the
README rather than from code it says so.

Repository facts, for calibration. Created 2025-10-09, last push 2026-04-20 - about four months
before this was written. 1,504 commits, of which `odgrim` has 1,488, `claude` 12 and `polishfreak`
4, so it is a single-author project with an agent committing under its own name. Five stars, two
forks, twelve open issues, no open pull requests. Releases run to v0.8.0 on 2026-03-23 and the
changelog has a large `[Unreleased]` section after it. CI (`.github/workflows/ci.yml`) builds,
runs `cargo test --tests --locked` and `cargo clippy -- -D warnings`; there are roughly 1,669 test
functions in-tree.

**There is a great deal more here than the row assumed, and a great deal of it is theory.** This is
not a weekend toy. It is also not a project whose end-to-end behaviour anybody has published: the
only tests that actually drive a real agent are `#[ignore]`d and require a Claude CLI on PATH, and
the SWE-bench harness under `benchmarks/swe_bench/` has no committed results. The honest reading is
that abathur is a serious, heavily-built, largely agent-written single-author system with real
operational machinery inside it and a large speculative layer bolted on top, and that the two are
worth separating carefully before taking anything.

## What it is, in three sentences

Abathur is a Rust binary (`abathur`) that runs a persistent orchestrator process which decomposes
submitted tasks into a DAG, spawns `claude -p` subprocesses to execute the leaves in per-task git
worktrees, and merges the results back through a two-stage merge queue. Its state is one SQLite
file (`.abathur/abathur.db`) with 14 migrations, its control flow is an in-process event bus with
58 handler modules under `src/services/builtin_handlers/`, and its agent population is
bootstrapped from exactly one hardcoded template - the Overmind - which then creates every other
agent at runtime through an MCP API. On top of that sits a "convergence engine" that treats goals
as attractors which never complete, classifies each task's trajectory as converging, oscillating,
diverging or plateaued, and an evolution loop that scores agent templates by success rate and
files work to rewrite the ones that underperform.

## The coordination model, against ours

The shapes are genuinely different, and the difference is not a detail. Abathur is **one process
that owns the queue and spawns the workers**. Flowy is **a store that several independently-started
agents write to**. Almost everything below follows from that.

### Who assigns work

In abathur, an LLM decomposes and the orchestrator dispatches. `src/services/meta_planner.rs` and
`src/services/llm_planner.rs` turn a submitted task into a `DecompositionPlan` of `TaskSpec`s with
dependency edges; the Overmind picks which workflow spine applies
(`src/domain/models/specialist_templates.rs`, 2,022 lines, mostly prompt text). Dispatch is then
mechanical and has two paths that deliberately overlap: `TaskReadySpawnHandler` reacts to a
`TaskReady` event, and `ReadyTaskPollingHandler`
(`src/services/builtin_handlers/ready_task_polling.rs:47`) polls `get_ready_tasks(100)` on a timer
and pushes ids into the same spawn channel. Its doc comment says why - the event is broadcast on a
channel that can drop, so the poll is the safety net. Readiness itself is recomputed by
`FastReconciliationHandler` every ~15s and `ReconciliationHandler` every ~5min, which walk Pending
and Blocked tasks, look at their dependencies, and move them.

In flowy, nothing assigns. `internal/store/deps.go` computes ready per reader and never stores it:
a todo is ready when every blocker is done **and somebody is carrying it**, over that reader's own
permission-filtered graph, with an unknown blocker counting as not-done. Work is picked up by an
agent that read the queue, or handed over by `POST /api/assign` and the `assignee` field. There is
no process whose job is to decide what runs next.

The practical difference: abathur can start work with nobody watching, because the orchestrator is
the thing that is watching. Flowy cannot, and does not try to - the four seats are started by a
person or a harness and then coordinate. That is a deliberate difference and not a gap.

### How conflicting claims resolve

Abathur has a real answer and it is one line of SQL.
`src/adapters/sqlite/task_repository.rs:401`:

```sql
UPDATE tasks
   SET status = 'running', agent_type = ?, version = version + 1,
       updated_at = ?, started_at = ?
 WHERE id = ? AND status = 'ready'
```

`rows_affected() == 0` returns `Ok(None)` and the caller does not get the task. Two orchestrator
paths racing for the same row produce exactly one winner and the loser is told so. Underneath that,
every other `task_repo.update()` carries `WHERE version = ?` optimistic locking and callers handle
`DomainError::ConcurrencyConflict` by re-reading and retrying (`update_with_retry` in
`src/services/builtin_handlers/mod.rs`). There is also a softer read-then-write `claim_task` in
`src/services/task_service/lifecycle.rs:16` that rejects a claim when the task is not Ready, but it
is not the atomic path; the middleware comment at
`src/services/swarm_orchestrator/middleware/guardrails_check.rs:35` says outright that the atomic
claim is the orchestrator's job.

Flowy has no such thing, on purpose, and the reasoning is written down in
`internal/store/todostatus.go`: status is a signed event, read permission is the only bar, and **a
restatement is accepted rather than refused**. Two agents that both say `active` on the same todo
both succeed, the fold is latest-wins, and the second one silently overwrites the first's assignee.
Nothing anywhere refuses on the grounds that somebody else already has it. That is coherent with
the rest of the fabric - a status is a claim about the work, and refusing a claim would be
refusing somebody's honest statement - but it means "two agents pick up the same row" is a race we
resolve by convention and by the chat room, not by the store. This is the sharpest single contrast
in the two designs.

### What happens when an agent dies mid-task

In abathur an agent is a subprocess. `src/adapters/substrates/claude_code.rs` builds a `claude`
command line with `--max-turns`, `--model`, `--system-prompt`, `--allowedTools`,
`--disallowedTools`, any `--mcp-config`, and `-p <prompt>`, runs it with the worktree as cwd, and
tees its stream-json stdout to a per-task log. So a dead agent is a non-zero exit or a closed pipe,
observed directly by the parent, and the task fails and retries under `max_retries`.

The interesting case is the orchestrator itself dying with tasks in flight, and there are three
overlapping recoveries:

- `StartupCatchUpHandler` (`src/services/builtin_handlers/startup_catch_up.rs`) fires on
  `OrchestratorStarted`, lists every `Running` task, and treats any whose `started_at` is older
  than the stale cutoff (or missing) as orphaned: `retry_count += 1`, transition to `Failed`, and
  emit `TaskFailed` with the error string `"orchestrator-restart: task was running during
  shutdown"`. The cause is written into the record rather than inferred later.
- `ReconciliationHandler` (`reconciliation.rs:115`) runs the same idea on a timer for a live
  process, with tiers: at 50% of `stale_task_timeout_secs` it emits `TaskRunningLong`, at 80%
  `TaskRunningCritical` plus a human escalation, at 100% it fails the task. Tasks parked in a
  workflow state get half that budget.
- `TaskSLAEnforcementHandler` (`task_sla_enforcement.rs`) is separate and deadline-driven rather
  than staleness-driven: warning, critical and breached tiers off `task.deadline`, with
  `HumanEscalationRequired` on breach when configured.

Flowy has nothing here. A todo moved to `active` by an agent whose microVM dies stays `active`
forever, carrying an assignee who no longer exists. Worse, `deps.go` requires "somebody is carrying
it" for readiness, so the dead agent's todo is not merely stale - it is holding its dependents out
of everybody's ready list, and no surface says why. We do have the raw material for a fix and it is
better than a timeout: `internal/store/inbox.go` records presence from the inbox poll, with the
honest caveat written in the file that the node sees the polling and not the listener, and that a
`WaiterForked` row is attached, fresh and able to wake nobody.

## Worth stealing

Five, in the order I would do them.

### 1. An exclusive pickup that can be refused

**What it is.** `claim_task_atomic` above. The point is not the SQL, it is that the queue has one
operation which can answer "no, somebody else has it", and that the answer is a value the caller
must handle rather than a warning printed beside a write that happens anyway.

**Where it would go.** `internal/store/todostatus.go`, beside the refusals already in
`refuseStatus`, as a distinct verb - not as a change to the existing status move. The existing
move must keep taking restatements, for the reasons the file's header sets out. What is missing is
a second door: something like `todo_pickup`, which is a status move to `active` that additionally
asserts nobody else was carrying it, implemented as a conditional insert against the current fold
head, and which returns a refusal naming the current carrier when it loses. The MCP verb goes in
`mcp_assign.go` or beside the todo tools in `mcp_tools.go`; the console's room panel already writes
assignee, so it has an obvious caller.

**What it would cost.** More than it looks. Flowy's status is a fold over signed events, not a
column with a `WHERE` clause, so "conditional on the current head" has to be expressed against the
event log - either a `WHERE NOT EXISTS` over later events for that artifact, or a serialisable
transaction that re-reads the head. The federation case is worse: two nodes can each grant an
exclusive pickup and only discover it at sync, so the honest scope is **exclusive within a node,
advisory across nodes**, and the refusal text has to say that. Also needs a way to break a stale
claim, which is item 2. Call it a day of work for the single-node case, plus a real argument about
what it means after a merge.

### 2. A reaper for work nobody is carrying any more

**What it is.** `StartupCatchUpHandler` plus the tiered staleness in `ReconciliationHandler`. Two
properties worth copying exactly: the recovery writes **why** into the record (the
`"orchestrator-restart: ..."` string, not a bare status flip), and it warns twice before it acts.

**Where it would go.** A periodic sweep in `serve.go` beside the other background work, reading
`internal/store/inbox.go`'s presence rows and joining them against `kind: todo, status: active`
items. For each active todo whose assignee has had no poll for longer than a threshold, append a
signed note event on the artifact and surface it - the console's room panel and the TUI both
already draw todos, so the render is a badge, not a new view. **I would not have it move the
status**, at least not first: our whole model says a status is somebody's claim, and a sweeper that
silently sets `todo` is the node inventing a claim on a principal's behalf. Say "nobody has been
listening for this for 40 minutes" and let a person or an agent decide.

**What it would cost.** Small, and the value is immediate, because right now a dead seat's todo
blocks its dependents invisibly. The one honest difficulty is that presence is not liveness, which
`inbox.go:201-218` already says at length: a forked waiter polls happily and can wake nobody. So
the threshold catches a dead VM but not a wedged agent, and the badge text must not overclaim -
"no poll since X" rather than "this agent is dead".

### 3. Cost tiers in the gate, with a blocking early exit

**What it is.** `src/services/overseers/` is seven small subprocess wrappers - `compilation`,
`type_check`, `build`, `lint`, `security_scan`, `test_suite`, `acceptance_test` - each parsing its
tool's output into a typed signal, sorted into three cost tiers. `cluster.rs:98` runs the cheap
tier, calls `has_blocking_failures`, and if the build or the type check failed it returns
immediately without running the moderate or expensive tiers. `traits.rs:39` defines "blocking"
narrowly and says why: there is no point running tests when the thing does not compile.

**Where it would go.** `run-tests.sh`. Today the gate is one long script that stands up its own
Postgres, builds, and runs everything, and its verdict is pass or fail. Two changes: declare the
phases in cost order with an explicit early exit when `go build`, `go vet` or `gofmt -l` fails, and
record **which phase failed** in the run record that `mcp_mergequeue.go:184` already declares via
`merge_gate`. A gate run that says "failed at build" is a different fact to an operator than one
that says "failed at the federation checks", and the merge queue currently cannot tell them apart.

**What it would cost.** Half a day for the phase split, plus a field on the gate run. The real
benefit is not wall-clock - a broken build already fails fast - it is that the merge queue's
`gate_run` gains a shape, and `GatedTipField`/`GateRunField` in `internal/store/mergequeue.go`
become answerable in more detail than green/red.

### 4. Cost and time as an input to whether work starts at all

**What it is.** Three cooperating mechanisms. `migrations/014_quiet_windows.sql` stores named
recurring windows as `start_cron`/`end_cron` plus an IANA timezone, during which the swarm does
not dispatch. `src/services/budget_tracker.rs` tracks consumption windows and derives a pressure
level; `BudgetConfig` (`src/services/config.rs:512`) then maps pressure to a concurrency ceiling -
`max_agents_normal` through `max_agents_critical` - and the convergence loop terminates early with
`BudgetDenied` above 95% consumed rather than spending the last of the budget on one task.
`src/services/guardrails.rs` holds the hard limits beside it: tokens per hour, concurrent agents,
and a default deny list of `.env`, `*.key`, `*.pem`, `**/secrets/**`.

The detail that makes this credible is the author's own `abathur.toml` at the repository root: a
single quiet window named `workday`, `0 5 * * 2-6` to `0 18 * * 2-6` in `America/Los_Angeles`,
described as "Pause during normal working hours". They run the swarm overnight on their own
repository and get out of its way during the day. That is a dogfooding artifact, not a feature
list.

**Where it would go.** Flowy has no notion of spend anywhere - `metrics.go` and the observability
surface count requests and rows, not tokens or dollars. The smallest useful version is not a budget
engine: it is a project-scoped policy artifact that says when work should not be started, read by
whatever drains the queue, and rendered in the console and the TUI status line the way the fixture
warning already is. `internal/store/projects.go` is where a project-level policy belongs.

**What it would cost.** The window part is cheap and self-contained. The budget part is not, and I
would not do it: it needs every agent to report its own token spend honestly, and a fabric whose
premise is signed provenance should not build a control loop on unverifiable self-reports. Take
the clock, leave the accounting.

### 5. Per-phase tool grants, including forbidding the agent's own orchestration tools

**What it is.** `.abathur/workflows/code.yaml` declares four phases - research, plan, implement,
review - and each names its `tools:` list, a `read_only` flag, its `dependency` (root or
sequential), and whether it is a `verify` or `gate` phase. Those grants reach the subprocess as
`--allowedTools`. Separately, `src/domain/models/substrate.rs:117` defines `BLOCKED_TOOLS` and
passes it as `--disallowedTools` on every single invocation: `Task`, `TodoWrite`, `TodoRead`,
`TaskCreate`, `TaskUpdate`, `TaskList`, `TaskGet`, `TaskStop`, `TaskOutput`, `TeamCreate`,
`TeamDelete`, `SendMessage`, `EnterPlanMode`, `ExitPlanMode`, `Skill`. That is the sharp idea. The
agent is forbidden its own orchestration surface, so it cannot quietly spawn a private subagent
tree or keep a private todo list beside the fabric's. All coordination has to go through the store,
because the alternative has been taken away.

**Where it would go.** Two places, and they are different in kind. The advisory half belongs in
`internal/store/assign.go` and `POST /api/assign`: an assignment could carry the phase and the
grant, so a seat handed a research task is told, in the artifact, that it is reading only. The
enforceable half belongs in `mcp_tools.go`: flowy's own MCP surface can genuinely refuse - a token
or an assignment marked read-only gets `mem_write` and `report_write` refused with a reason, the
same way `memWriteQueueOnly` already refuses a stranger rewriting somebody's title.

**What it would cost.** The advisory half is a field and a render, an afternoon. The enforceable
half is a day and needs care about which verbs count - `worklog_append` on a read-only seat is
arguably fine, and a refusal that stops an agent recording what it found would be worse than the
thing it prevents. Note the limit honestly: flowy cannot stop a client from using its own
`TodoWrite`, because flowy does not launch the client. We can only make our own door refuse and
make the grant visible on the record.

## What we should not take

**The convergence engine.** `src/domain/models/convergence/` is twelve files and
`src/services/convergence_engine/` another eight plus a test directory, behind an 86 KB
specification in `specs/convergence-attractors.md` and a 51 KB one in
`specs/convergence-task-integration.md`. It classifies each task's trajectory as `FixedPoint`,
`LimitCycle`, `Divergent`, `Plateau` or `Indeterminate`, detects cycles by Jaccard similarity on
character bigrams of overseer output signatures at periods 2, 3 and 4 with a 0.85 threshold, calls
a plateau below `PLATEAU_EPSILON = 0.02` mean absolute delta, and selects strategies by Thompson
sampling over an exploration weight. Then `policy.rs` says, of its own acceptance threshold: "This
threshold is used for monitoring and diagnostics. It does **not** gate finality decisions - the
LLM-based intent verifier is the sole authority on whether a trajectory has converged." So the
numerical apparatus is instrumentation wearing the clothes of a controller, and the actual decision
is an LLM reading the work and saying yes. The one genuinely useful idea inside it is cheap and
separable: notice when successive attempts produce the same failure signature and stop. That is
worth a dozen lines in a retry path, not a subsystem.

**Goals that are never complete.** `src/services/intent_verifier/mod.rs` states the doctrine:
"Goals are convergent attractors - they guide work but are never completed." It is a defensible
idea for a standing objective like "keep the codebase tested", and abathur pairs it with a
convergence check that runs every four hours. For us it is the wrong direction of travel.
`internal/store/todostatus.go` exists precisely because work that could not be declared finished
produced five duplicated builds in a day, and its answer was to make `done` a verb somebody says
with a signature behind it. A queue whose items converge instead of closing is the state we just
climbed out of.

**Agent evolution and self-rewriting prompts.** `migrations/013_template_stats.sql` records
success rate, average turns and average tokens per template version; `evolution_loop/evaluate.rs`
fires `LowSuccessRate`, `VeryLowSuccessRate` or `Regression` triggers and, with
`auto_revert_enabled`, rolls a template back to its previous version. Two reasons to leave it.
First, the measured quantity is the task outcome, and the task outcome comes substantially from the
agent's own `task_status` call plus an LLM verifier, so the loop optimises a self-report - the
exact thing the gate discipline in `HANDOFF.md` was written to stop us trusting. Second, the loop
closes by string matching: `evolution_triggered_template_update.rs:93` decides whether to act with
`trigger.contains("Low success rate")` on a human-readable message, then files a task titled
"Refine agent template: X" for an agent to rewrite the prompt. A control loop whose condition is a
substring of its own log line, acting by asking an LLM to edit the prompt that produced the
measurement, is not a thing to reproduce over signed artifacts.

**An LLM resolving merge conflicts and then landing.** README:219 and
`swarm_orchestrator/specialist_triggers.rs` escalate a persistent conflict to a merge conflict
specialist agent. Our merge queue exists to enforce one rule - a branch may only land on the tip
its gate actually measured - and `internal/store/mergequeue.go` refuses rather than warns when the
gated tip and the landing tip differ. An agent that resolves a conflict has by definition produced
code no gate has seen; letting it proceed to a merge is the failure mode the file's header
describes, dressed up as a resolution. Re-gate and re-queue, always.

**The Overmind creating agents at runtime.** Only the Overmind, an aggregator and a triage agent
are seeded; every specialist is created by the Overmind at runtime through the Agents MCP API, and
`meta_planner.rs` has an `auto_generate_agents` switch to do it automatically. The only brake is
`[spawn_limits]`. For a fabric where identity is a signing key and every row names its author, an
agent population that grows itself is a population of principals nobody minted. `internal/mint`
and `identity.go` are the shape of our answer and it is the right one.

**Memory that decays on its own.** Three tiers - working, episodic, semantic - with a decay rate,
a prune threshold and a maintenance daemon that drops items below tier thresholds. In a
permission-filtered store this is the wrong default: an item that decayed away is indistinguishable
from an item that was never written, and from an item you were not allowed to read. The `withheld`
and `refused` counters on `todos` exist because we decided that distinction has to be visible.
There is one good idea in this subsystem worth remembering separately -
`migrations/005_distinct_accessors.sql` requires promotion between tiers to be justified by several
**distinct** accessors, so one agent in a loop cannot promote its own note. If we ever automate
`personal` to `project` promotion, that is the constraint to copy.

**The architecture as a whole.** Hexagonal layering, ports for everything, a domain layer that
cannot see infrastructure - and it did not prevent `src/cli/commands/swarm.rs` reaching 100 KB,
`src/services/event_reactor.rs` 80 KB, or `specialist_templates.rs` 112 KB. Fifty-eight event
handler modules with priorities, circuit breakers, error strategies and watermarks is a lot of mechanism
to keep honest, and the changelog's own entry about replacing "the prior helpers god-module" says
the mechanism did not hold the line by itself. Flowy is one Go binary with a store package and
files that argue for themselves in prose. That is working; do not trade it for ports.

## What I could not determine

- **Whether it works end to end.** The CI comment in `.github/workflows/ci.yml` says the only
  ignored tests are in `tests/e2e_swarm_integration_test.rs` and need a working Claude CLI on PATH,
  so nothing in CI ever runs a real agent through a real worktree to a real merge. I read the code
  and it is coherent; I did not run it, and neither does its own CI.
- **How well it performs.** `benchmarks/swe_bench/` is a complete-looking harness -
  `dataset.py`, `instance.py`, `runner.py`, `predictions.py` - with no results committed anywhere
  in the tree. There is no number in this repository about how often the swarm finishes a task.
- **Whether the evolution loop has ever actually improved a template.** I traced the trigger and
  the request lifecycle; the act of rewriting is a submitted task for an agent to perform. Whether
  that has ever run to completion, and what it produced, is not visible in the repository.
- **What the convergence machinery does in practice.** Attractor classification, basin width and
  Thompson sampling are implemented and unit-tested, but every test I read constructs its
  observations synthetically. I could not tell whether real overseer output produces stable
  classifications or noise.
- **Why it stopped.** Last push 2026-04-20 after a very high commit rate, with a large
  `[Unreleased]` changelog section and twelve open issues. Paused, finished, or abandoned is not
  something the repository says, and I am not going to guess from the gap.
- **How much of it is agent-written, and whether that matters.** The commit distribution, the
  volume, the `specs/T10-...`/`T11-...` filenames and the uniform doc-comment register all suggest
  most of this was written by the swarm on itself. I mention it because it bears on how to read
  the theory-heavy parts, not as a criticism - but I could not confirm it, and the `claude`
  contributor account has only 12 of the 1,504 commits.
