# Working in this repository

Instructions for an agent with a task in flowy. Short on purpose: everything
here is a thing that has actually gone wrong, not a style preference.

## Build, run, test

```sh
go build ./...
./run-tests.sh                 # the whole suite: stands up its own Postgres and node
./.flowy-gate                  # what the merge queue runs. Same suite, project's own env
cd web && npm ci && npm run build
```

`.flowy-gate` is the project declaring its own gate. If you change what "green"
means, change it there - the drainer runs that file and nothing else.

**Run the WHOLE suite before you file** - and `./run-tests.sh`, not `go test
./...`. Those are not the same measurement: without a database, `go test ./...`
SKIPS every DB-backed test rather than failing, so it goes green on your branch
and red in the gate, and the row cannot tell you why. `run-tests.sh` stands up
its own Postgres, which is the difference.

Not the tests you wrote, either, and not the ones you already knew about. This
repo has source-walking tests that read the code rather than exercise it -
`paramguard`, the advertised-routes walk, `TestEveryRouteSaysWhatItNeeds` - and
the family keeps growing. Two agents on one afternoon both filed after running
their own tests plus the two walks they remembered, and both were caught by the
third.

**Never read an exit code through a pipe.** `./run-tests.sh | tail -20` exits
with tail's status, which is 0 whatever the suite did. Read the suite's own
`passed: N failed: M` line.

## Landing a change

Nobody pushes to master. A branch is filed and a drainer gates and lands it.

```sh
git worktree add ../flowy-<thing> -b <branch> master
# work, commit
git worktree remove ../flowy-<thing>          # BEFORE filing: a held branch cannot be rebased
flowy merge open --branch <branch> --title "..." "what changed"
```

The worktree step is not optional. A branch checked out anywhere blocks the
rebase, and the queue reports it as BLOCKED with a reading that expires 15
minutes later - so it looks like a stall rather than something you can fix.

Once a row is declared its branch is **frozen**: no rebase, no amend, not even
a better version of the same change. Both ends of the comparison have to hold
still or the verdict describes neither.

A red row retries on its own once you push a fix - the red is keyed on branch
sha and target sha, so a new tip is a new question. With nothing to push, though,
neither sha moves, and in a queue of one the target cannot move either: a row red
on a flake nobody is fixing re-asks never. Measured 2026-09-05 on
01M1SD4HYAE9BAA1BYJW821X2Y, where the only candidate in the queue was the red one.

Taking a row out has two verbs and they are not interchangeable.

    flowy merge withdraw --id <row> --note "why"

takes the request out and leaves a tombstone naming who took it back and what they
said. `--note` is required, and it is the only required note on that verb, because
every other queue event leaves an artifact behind - a landing leaves a sha, a red
leaves a verdict - and a withdrawal leaves an absence.

    flowy todo done --id <row>

closes the queue item and says nothing about why. Reach for it when the row itself
is finished, not when a branch is being taken back.

Abandon is a third thing: it gives back the target LOCK and leaves the request
in the queue. It has no `flowy merge` verb - the handle is the door:

    curl -X POST -H "Authorization: Bearer $FLOWY_TOKEN" \
      -d '{"reason":"..."}' $FLOWY_ADDR/api/merge/<row>/abandon

`merge withdraw` prints that same curl when the row you are withdrawing is the
one holding the target, because withdrawing it then would leave the target
reserved with no row left to say why.

## The rules that have teeth

**Absent is not empty.** The single most repeated defect here. A door that
answers `[]` when it cannot see, a field that is `null` for both "no value" and
"never asked", a check that returns "attributed" whether or not it verified -
each one is a wrong answer shaped like a right one. Keep them different at the
wire: 503 for "this node cannot", 200 with `[]` for "nothing to show".

**Assert a difference, not an absolute.** One reading cannot tell a rule being
enforced from a rule that does not exist. Run the query twice, vary only the
thing under test, and assert the two answers differ. A security check here
passed for months because the door ignored the parameter it was testing:
"honoured it and found nothing" and "never looked" are the same 200.

**An argument the callee drops is a lie.** Refuse an unknown parameter rather
than ignoring it. `?tag=x` returning everything, `--jq` swallowed by a flag
parser, a `className` handed to clsx as a function - all silent, all
successful-looking.

**A test that cannot fail is worse than no test.** Prove the red: break the
thing on purpose, watch the test fail, put it back. Restore from a copy, not
`git checkout` - checkout reverts to the index and eats unstaged work.

**Never build a shell command.** Argument vectors only. A prompt a person typed
reaches a subprocess as one argument, and a name containing a backtick is a
name.

**Resolve identifiers, never match prefixes.** ULIDs share a leading timestamp,
so two seats minted in the same minute look identical to the eye. Four
misattributions in one day came from reading actor ids by prefix. Ask the door
that maps ids to names.

## Adding an HTTP route

Four places, and the suite fails if you miss one:

1. `serve.go` - register it, wrapped in `s.operatorOnly(...)` if it needs to be
2. `serve.go` - the advertised route list
3. `paramguard.go` - the query parameters it accepts, exactly
4. `roleguard.go` - `routeNeeds`: `needsWrite`, `needsNothing` or `needsOperator`

`routeNeeds` has no default on purpose. Absent is a mistake; `needsNothing` is
a decision somebody made and can be argued with.

## UI work

Name the Playwright flows in the row **before** writing the component - what is
clicked, what is then seen, and what changed on the node. A flow written first
has to say what the button does, which is the question a dead button never got
asked. Assert geometry or the node's answer, not class names: a check that
looks for a CSS class passes on a pane that renders nothing.

**A new console check goes in `checks.d/console/<name>.sh`**, holding its own
comment, its own function and its own `check` line. run-tests.sh sources the
directory and fails if it is missing, empty, or holds a file that registers
nothing. The old way - appending to the registration list in run-tests.sh - is
what five rebase conflicts in one evening had in common, none of them a
disagreement and one of them eating a closing brace. Two branches adding two
files conflict never.

## Talking to the fleet

Rooms are for a measurement and a decision. Three lines is normal; anything
longer belongs in the row or the commit message, where somebody can choose to
read it. Reply into a thread (`--thread <id>`) rather than starting a new one
for an answer.

Say which state a change is in and never "done": **FILED** (in the queue),
**LANDED** (on master), **LIVE** (the node serves it, `/healthz` version
changed). Only LIVE means somebody can go and look.

**Claim the row before you start, not after.** A sentence in a room is not a
claim - a hook can read the board, nothing can read a sentence. Every
duplicated effort here was prevented by a filed row or by nothing at all.

## When a fix does not appear to work

Ask when that process loaded its code. The node restarts on deploy; long-lived
helpers read their source once at start; a loop somebody typed into a shell can
never be updated by a commit at all. `curl $FLOWY_ADDR/healthz` gives the
version and the uptime, which answers it directly.
