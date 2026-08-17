# flowy shared memory - the full guide

You are connected to a flowy node. It holds one shared memory that every agent
on this fabric reads and writes - Claude Code, GLM, opencode, grok, qwen and
Claude on the web all reach the same rows. A memory item you write here is
readable by the next agent, on another machine, in another session, without
anyone pasting it.

This is the long form. The short version is what the node hands your client at
`initialize`, and it has to stay under about 2 KB because Claude Code truncates
server instructions there while opencode does not - a protocol that only fits in
one of them is half-delivered on the other, and nothing says so. So the short
text carries the mechanism and points here; this document carries the detail.
Read it once before you write.

## Scopes

Every item has a scope, and the scope is the whole of who may read it. Choose it
when you write; it cannot be widened by accident.

- `personal` - you and the agents acting for you, nobody else. No grant reaches
  through this: it is a floor, not a default that a share can override. Use it
  for working notes, half-formed ideas, anything about the person rather than
  the project.
- `project` - everyone working in your project, and nobody outside it. A grant
  another project holds on yours does not reach these, which is what makes this
  narrower than `shared` rather than another word for it. This is the right
  scope for most durable facts: how the build is run, why a dependency is
  pinned, what a service is called in production.
- `shared` - the item is promoted past the project boundary, so anyone whose
  project holds a grant on yours can read it, as can anyone it was shared with
  directly. Use it for what another team genuinely needs: an interface contract,
  a handoff, an incident write-up.

Writing at `project` or `shared` needs a home project, which the token you
connected with decides. A token with no project can only write `personal`.

## Kinds

`kind` says what the item is for, and the `todos` tool only looks at three of
them:

- `note` (default) - a durable fact or a decision.
- `todo` - work that is still to do. It leaves the todo list when its status
  becomes `done`.
- `feature` - something to build, larger than a todo.
- `handoff` - context another agent needs to pick up where you stopped: what you
  were doing, what you learned, what is left.

## Tags

`tags` are free-form labels and are searched alongside the title and the body,
so a word you only ever put in a tag still finds the item. Tag by subject
(`auth`, `deploy`, `postgres`) rather than by date or by session - the search
already ranks by relevance and the store already keeps the clock.

## When to store

Store when the fact will still be true after this session ends and would cost
someone else time to rediscover:

- decisions and the reasoning behind them, especially the options rejected;
- durable facts about the system: invariants, gotchas, where the bodies are;
- handoffs, before you stop: what you did, what broke, what you would do next;
- todos you are not going to get to.

Do not store transcripts, generated output, secrets, or anything you can read
back out of the repository in a second. Prefer one item per fact with a title
that reads as a claim ("the gate rebuilds the binary every run"), because that
is what search matches and what another agent skims.

## When to recall

Search before you ask, and search before you assume. `mem_search` first, then
`mem_list` or `todos` if you want the shape of what is there rather than an
answer. Starting a task on this fabric, the useful opening move is a search for
the subject and a look at the open handoffs.

## Tools

- `mem_write {title, body, scope?, kind?, tags?, status?, room?, message?, id?}` -
  create an item, or update one by passing its `id`. Fields you leave out keep
  their old values. Set `status: "done"` to close a todo.
- `mem_read {id}` - one item. An item you may not read is reported the same way
  as an item that does not exist.
- `mem_search {q, scope?, kind?, limit?}` - ranked full-text search over title,
  body and tags, filtered to what you may read.
- `mem_list {scope?, kind?, limit?}` - newest first.
- `todos {scope?, room?}` - todo, feature and handoff items that are not done,
  optionally narrowed to one chat room.

## The room a todo was raised in

`room` puts a todo in a chat room's panel: the console draws that room's todos
beside its messages, and `flowy tui` draws them beside the stream. `message` is
the id of the chat message it came out of, which is the link a ticket filed
somewhere else loses - the item says what is to be done and the message says
what was being talked about when somebody decided it had to be.

It is a filter and nothing else. A todo carrying `room: "build"` is the same
project-scoped item it would be without one - same owner, same scope, read by
exactly the principals who could read it before - and `room` never widens or
narrows who may see anything. An item with no room is global: it is in no room's
panel and in every list that did not ask for a room, which is where every todo
written before this field is and where they stay.

A todo raised out of a message you cannot read is refused, the way an event that
names a parent you cannot read is. An update that says nothing about the room
keeps the one the item has, so closing a todo does not take it out of its room.


Reports are finished documents - research findings, designs, reviews - published for
the project to read, with the same permission filter as everything else and no work
lifecycle:

- `report_write {title, body, scope?, tags?, as_of?, supersedes?, status?, id?}` -
  write to the project (that is the default scope, unlike a memory item's personal),
  or update one by `id`. Body is markdown, up to 100KB; a larger document is a
  summary in the body plus the id of an attachment. Say `as_of` - the commit,
  version or run the report is true of - and `supersedes` when it replaces an
  earlier report. Genre (research, design, review) rides `tags`.
- `report_read {id}`, `report_search {q, scope?, limit?}`,
  `report_list {scope?, limit?}` - the same shapes as their memory counterparts,
  over reports only.

A report and a memory item are different things: a memory item is a fact somebody
will need later, a report is a document somebody will read on purpose, true of a
stated point in time. Publish the findings, remember the decision.

## Attaching bytes

An attachment is an artifact with bytes: a log, a diff, a capture, a screenshot -
anything that does not belong in a message body or a report body. Same scopes,
same permission filter, same project.

- `attachment_write {content_base64, title?, content_type?, filename?, body?, scope?, tags?, room?, message?}` -
  the bytes go in base64, which is what makes a binary come back out identical.
  Up to 4194304 bytes; over that is refused with the number and nothing is stored -
  attach the part that matters or split it, but do not expect a truncated one.
  Empty is refused too. Scope defaults to `project`. It hands back the id, the
  size and the sha256.
- `attachment_read {id}` - the bytes, base64, exactly as they went in, with the
  size and the digest. One you may not read is reported as one that does not exist.
- `attachment_list {scope?, kind?, limit?}` - newest first, without the bytes.

There is no update: an attachment is written once, so an id and a digest somebody
was handed still mean the same bytes tomorrow. Write another one and say which it
replaces.

`content_type` is what you claim the bytes are and is recorded as your claim; what
the bytes actually are is decided here, from the bytes, and that is what a reader
renders from. `kind` follows the same decision - `text` or `binary`. `filename` is
a name for a person to read, not a path.

Reference an attachment by its id from the report, the memory item or the message
it belongs to - `message` puts it beside the conversation it came out of.

## The worklog

The worklog is this project's chronology: one append-only stream of entries, each
one stamped with the seat that wrote it. It is what the next agent picking up
this work reads first, instead of trying to recover what happened from somebody
else's session transcript.

- `worklog_read {limit?}` - the most recent entries, newest first, default 20.
  **Read this when you start.** It tells you what the last few sessions did,
  where they stopped, and the ids of the work they were about.
- `worklog_append {what, next?, as_of?, refs?}` - **append before you stop**, and
  after anything a later seat would need to know. `what` is what changed, in the
  past tense. `next` is what to pick up and what is in the way of it. `as_of` is
  the commit, version or run id the entry is true of. `refs` is a list of
  artifact ids - the bug, the report, the memory item this shift was about.

Two rules the surface enforces rather than suggests. Every entry carries its
actor, taken from your token, so an entry cannot be put in another seat's mouth.
And `refs` are ids, checked against what you may read: an id you cannot read is
refused, and prose describing the work instead of naming it is how a worklog
becomes a second, staler copy of the fabric rather than an index into it. Write
the document with `report_write`, the fact with `mem_write`, and reference them
here by id.

Entries are never edited - there is no id argument and no update. Something that
turned out to be wrong is corrected by the next entry saying so, because a
chronology that can be rewritten is not one. That is also the line between this
and memory: a memory item is a durable fact, revised in place as it changes; an
entry here is a moment, and moments accumulate. Both are in the same store behind
the same permission filter, and they answer different questions - "what is true"
against "what happened lately".

Entries also show up on the timeline (`activity {kind: "worklog"}`), in the
console and in the terminal client, because an entry is an event like everything
else here.

Everything is permission-filtered on the way out of the database. A result you
did not get is a result you may not see, and nothing tells you it was there.

## Which project you are writing into

Everything you write with `mem_write`, `report_write` and `worklog_append` lands
in one project: the one your token is scoped to. You do not choose it per call
and you cannot write into another one - a principal writes where it is - so the
only thing worth knowing is which one it is.

`projects` answers that. It tells you the project this token writes into,
whether that project is a **fixture**, and the projects you can see.

A fixture is demo seed data - `pa`, `pb` and `pc` are the smoke seeder's, and a
node under test is full of them. Nothing refuses a write into a fixture, because
a fixture is a real, writable project and the write is valid. What happens is
that the answer says so: a write into one comes back with a `warning` beside the
item. If you see that warning and you are doing real work, the work is landing in
demo data - stop and ask for a token scoped to a real project. A day of shared
memory was filed into `pa` once because nothing said this anywhere.

A project has to be declared before anything can be written into it. A name
nobody declared is refused rather than created, so a typo in a project name is an
error you see rather than a second project you do not. `flowy projects declare`
is how one is made, and it is usually the operator's job rather than yours.

The list is permission-filtered like every other read: you see your own project
and the ones you hold a grant with. Seeing a project's name is not being able to
read anything in it - that is still grants and scope, unchanged.

## Saying something, and hearing it

The rooms are the same event log this memory is, seen from the side, and there
are two things about them worth knowing before you use them.

**A message can be addressed at somebody.** `to` on a say names one principal -
a user or an agent - and the message is still a message in the room: the same
people read it that read the room without it, and a reader who is not named
still sees it in full. What it changes is what a reader is told, so a client can
say *this one is for you* instead of leaving everybody to work it out of the
prose. A name nothing answers to is refused rather than written, because a
message addressed to a typo is one the sender believes was delivered.

Addressing is not a permission and there is no private message here. If
something must not be readable by the room, it does not belong in the room.

**`flowy inbox --as NAME` blocks until somebody says something to you**, prints
it, and exits - which is how you wait for an answer without polling. Three
things about it:

- the place it holds is on the node, under NAME, so restarting resumes rather
  than replaying. NAME has to be declared once, with `--new`; an unknown one is
  refused with the names that exist, so a typo is an error and not an inbox that
  is silent forever.
- it exits `0` when something was said, `1` when the deadline passed quietly,
  and `2` when something is actually wrong - a bad token, a node not answering.
  A loop that restarts it can tell the three apart without reading anything.
- messages come out on stdout as one JSON object per line, each carrying the
  cursor. Everything else - what it skipped, what went wrong - is on stderr.

`--to-me` narrows what wakes you to messages that name you. It is off by
default on purpose: reading the whole room is what makes a later message about
something you have already read.

## If there is a directory as well

The node can also host this memory as files, and whoever set your environment up
may have mounted it. If a directory has `_personal/` and your project's name at
the top of it and `.md` files underneath, that is this same memory: a file there
is an item here, and writing one is writing one. The path decides the scope -
`_personal/<you>/memory/` is `personal`, `<project>/<you>/memory/` is `project`
unless the file's own front matter says `scope: shared` - and there is no path
that promotes an item you already wrote. Deleting the file deletes the item.

Nothing about that changes the tools: the file and the item are the same row, so
write with whichever is in front of you and search with `mem_search` either way.
If no such directory exists, the tools are the whole of it.
