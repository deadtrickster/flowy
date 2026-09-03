# Identity and access

Written 2026-08-20, after the operator said: *"tokens for automations including
agents, otherwise normal ownership and collaboration - i will invite other
humans to projects"*, and *"normally systems like this have a token ledger -
people create tokens with names scopes and expirations"*.

This is the design, the conversion order, and what each step must NOT do. It is
written down rather than agreed in chat because three of us re-derived the same
facts four times today, and a room message is not a place anybody re-reads.

## What is here now, measured

    tokens(token, user_id, agent_id, project)      -- the whole table
    users(...)                                     -- global role, no project
    projects(id, origin, ...)                      -- no owner
    token_projects(token, project)                 -- reach, written only by mint
    room_members(project, room, principal, role)   -- people in rooms
    (nothing)                                      -- people in projects

    auth.go:196   a bearer token resolves through PrincipalForToken - carries a project
    auth.go:219   a cookie session resolves to Principal{UserID} - NO project, ever

No door creates, lists or revokes a token; `flowy mint` from a shell is the only
way one exists. `mint` replaces a token's whole reach, so widening a seat used
to mean minting a new credential and redistributing it - fixed separately by
`POST /api/agent/{id}/projects`.

## The shape this should be

Three nouns, and the whole design is keeping them apart.

**A person** is the identity. They log in, they own things, they are invited to
projects, and their rights differ per project.

**A project** is the boundary. It has an owner, and people belong to it with a
role that says what they may do *there*.

**A token** is something a person issues for an automation. It has a name, a
scope, an expiry, a created and a last-used time, and it can be revoked one at
a time. It ACTS FOR the person, or for a machine identity they own; it is not
itself an identity. It is never how a human logs in, and it is never how a
human changes project.

*(That paragraph is @orchestrator's, verbatim, and so is the sentence below.)*

> **If a token is the only way to be somebody, then every person is an
> automation.**

That is the defect in one line. The operator did not ask for a project
switcher; they asked to stop being a machine. Step 6 below - an agent stops
being an identity and becomes a thing a person runs - is the same statement
from the other end.

The inversion this replaces: today a token IS the identity - paste it and you
ARE that agent. That is why the console can only give you a project by holding
an agent's credential, and why "switch projects" has no meaning for a person.

**A brief** is a named, reusable context an agent is declared from - "architect",
"reviewer". It is what an agent is FOR: the prompt, the rules, the tools it may
use. It is not what the agent may reach, and it is not a principal. The operator
asked for this on 01M0G28CJK7KAPB1M0CMRKR5Y7, calling it an identity: *"I declare
agents with need context ... context defines identity - architect, reviewer and
so on. so when I declare agent I can either choose it or have my own. note that
it might be useful to have this identities globally too"*.

*It is called a brief here and not an identity, and that is a question for the
operator rather than a decision taken.* This document already says a person IS
the identity, and step 6 says agents STOP being identities. A fourth noun called
identity, owned by agents, contradicts both sentences on the page it is written
on. The operator's meaning is not in doubt - a reusable context - only the word
is, so the word is the thing to settle before this ships.

### Tables

    project_members(project, user_id, role, invited_by, joined)
        the missing one. A person's rights are per project, so the role lives
        HERE. If it is ever added to `users` instead, everyone gets one role
        everywhere and "some readonly, some cannot close" cannot be expressed.

    tokens(token, owner_user, agent_id, name, scope, expires, created,
           last_used, revoked)
        the ledger. `owner_user` is who issued it - a token with no owner is
        one nobody can be asked about. `revoked` rather than DELETE, so a key
        that was used yesterday can still be explained today.

    agent_briefs(id, owner_project, name, body, created, revoked)
        the fourth noun. `owner_project` is NULLABLE and that is the whole of
        the global-versus-per-project question: a brief owned by no project is
        one any project may declare from. One table with a nullable owner rather
        than two kinds, because two kinds drift and then need reconciling.
        `revoked` rather than DELETE, for the same reason tokens are: a brief an
        agent was declared from has to stay explainable after somebody retires
        it.

    agents(..., declared_from, declared_body)
        the brief is COPIED into the agent at declaration, not referenced. If it
        were referenced, editing "reviewer" would change every live agent using
        it, including ones mid-run - the brief-is-true-when-issued problem the
        helper preamble already documents. `declared_from` keeps the provenance
        so a panel can say which agents came from a brief and which have since
        drifted from it, and revoking a brief then orphans nobody.

    projects(..., owner_user)
        somebody is responsible for each one.

### Roles

Whatever the names, the rule is: **the door enforces it or it does not exist.**
A person labelled readonly who can still raise a row is worse than no roles at
all - the label says one thing, the node does another, and only the node is
real. So role names ship WITH the checks that mean them, never before.

### The name "reviewer" is already taken, and it is taken by a permission

Measured on master at dc8b3c9, before designing anything, because the operator's
examples were "architect, reviewer and so on" and one of those already exists.

    agents.agent_kind    worker | reviewer | system | monitor    schema.sql:86

`agent_kind` is not a label. It is read at `announce.go:106`, where
`MayAnnounceFederation` decides whether an agent may post the one thing on this
node that every node in the fabric then shows to everybody. So the column the
new noun would most naturally be folded into is a permission column, and
"reviewer" is already a value in it.

Two consequences, and the second is a defect on master today.

**A brief must not be stored in `agent_kind`.** They read as the same field -
both are "what this agent is for", both have a value called reviewer - and
merging them means choosing a context at declaration silently moves a
capability. That is the boundary going advisory again, which is the failure this
whole document exists to prevent.

**`reviewer` is a shipped role name no door checks.** `AgentKindReviewer` occurs
twice in the tree: its own definition at `internal/store/store.go:211` and the
validity map at `:222`. Nothing else reads it. `MayAnnounceFederation` returns
true for system and monitor only, so an agent minted `--agent-kind reviewer` is
indistinguishable from a worker at every door on the node. It is exactly what
"What each step must not do" already forbids - a role name shipped before the
doors that mean it - and it was already here before this row was filed. Either a
door checks it or it comes out of the enum; leaving a name that looks like a
capability and is not is how the next person plans against a boundary that is
not there.

## Conversion, in order

Each step is landable on its own and leaves the fleet working. Nothing here
requires re-minting a running seat, which is the constraint that makes it safe
to do while four agents are using the node.

**1. A person belongs to projects.** `project_members`, and a cookie session
resolves to a principal that carries the project the person is in. Today it
carries none, which is the root of every "scoping is terrible" symptom.
*In flight: 01M0FP0ZMM.*

**2. Invite a person to a project.** The door, and the console control. Rooms
already have invite; projects have nothing.

**3. Roles that doors check.** One role name at a time, each landing with the
checks that enforce it. Readonly first, because it is the one that fails safe.

**4. The token ledger.** `tokens` grows owner, name, scope, expiry, revoked;
doors to create, list and revoke; a page that shows them. `mint` stays as the
bootstrap path - the first operator has to come from somewhere - and every
token after that is issued by a person through the ledger.

**5. Expiry that is enforced.** Refusing an expired token at resolution, not a
column that records an intention. An expiry nothing checks is a label.

**6. Agents stop being identities.** An agent becomes a thing a person runs,
holding a token that acts for them. This is last because everything else has to
exist first, and because it is the step that changes what every existing
credential means.

**7. Briefs.** `agent_briefs`, the copy-at-declaration path, and the tab the
operator asked for. This is last and it is deliberately after step 6: a brief
describes what an agent is for, and until an agent has stopped being an identity
there is no clean place to put that without it being mistaken for one. Nothing
in this step touches reach - if choosing a brief can widen a seat, the step has
been done wrong.

## What each step must not do

- **Do not ship role names before the doors check them.** (Step 3.) This is
  already violated on master: `agent_kind` accepts `reviewer` and no door reads
  it. See the section above.
- **Do not give a brief any reach.** A brief is what an agent is for; a token
  is what it may touch. If choosing "architect" changes what a seat can read,
  the two nouns have been collapsed and the boundary is advisory again.
- **Do not store a brief in `agent_kind`.** That column is checked at
  `announce.go:106`. It looks like the right home and it is a permission.
- **Do not reference a brief from a running agent.** Copy it at declaration, or
  editing one rewrites the instructions of agents already mid-run.
- **Do not put a role on `users`.** Per-project rights need a per-project row.
- **Do not make `mint` additive.** It replaces a token's whole reach on
  purpose, so that re-minting cannot widen a credential by accident. Widening
  is `POST /api/agent/{id}/projects` and is a different verb saying a different
  thing.
- **Do not delete revoked tokens.** A key that was used yesterday has to be
  explainable today.
- **Do not break the bearer path while the console is converted.** Four agents
  and the drainer authenticate that way right now; the person path is added
  beside it, and the bearer path stops being the console's identity only at
  step 6.

## Why this is written down

Three seats measured the same facts four times today, each concluding
correctly and none of it surviving the conversation. A room message is read
once by whoever is awake. A row is read by whoever picks it up. This is the
part that is neither - the reasoning behind the order, which somebody will want
in a week when a step looks arbitrary.
