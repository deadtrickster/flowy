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

**A token** is a key a person issues for an automation. It has a name, an
owner, a scope, an expiry, and it can be revoked on its own. It is never an
identity: it acts *for* somebody.

The inversion this replaces: today a token IS the identity - paste it and you
ARE that agent. That is why the console can only give you a project by holding
an agent's credential, and why "switch projects" has no meaning for a person.

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

    projects(..., owner_user)
        somebody is responsible for each one.

### Roles

Whatever the names, the rule is: **the door enforces it or it does not exist.**
A person labelled readonly who can still raise a row is worse than no roles at
all - the label says one thing, the node does another, and only the node is
real. So role names ship WITH the checks that mean them, never before.

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

## What each step must not do

- **Do not ship role names before the doors check them.** (Step 3.)
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
