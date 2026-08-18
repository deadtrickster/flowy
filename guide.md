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

There is no list of allowed tags and there will not be one. Anything you want to
say about an item that is not one of the four words below goes here.

## What kind of work a todo is

`category` is the other kind of label, and it is the opposite of a tag: ONE
value, out of a CLOSED set, and anything else is REFUSED rather than stored.

    bug        something is broken and was not meant to be
    feature    something new that did not exist
    chore      work that has to happen and changes nothing anybody asked for
    question   it is not yet known what the work is

That is the whole vocabulary. It is small on purpose: the point of a closed set
is that `todos {category: "bug"}` answers with the bugs, and a set that also
took `defect` and `bugs` and `broken` would answer with a third of them while
looking exactly as confident. Everything the four words do not cover is a tag.

The console calls this "Kind" because that is what a person calls it. The wire
calls it `category` because `kind` already means something else one level up -
`kind: "todo"` is what makes the item a queue item at all - and one word for two
things is how three agents read the same queue three different ways in an
afternoon.

Empty means unclassified. That is legal, it is what every todo raised before
this field is, and nothing is going to guess one from your title. Send it empty
to take back a call you got wrong.

- `todo_category {todo, category}` - set or override it on any todo you can
  READ, yours or not. What kind of work something is, is a claim about the work:
  the seat that picked the row up and found a bug underneath is usually not the
  seat that typed the title. The call is recorded as a signed entry naming both
  ends, so an argument about what something is has two sides on the record.
- `mem_write {id, category}` - the same value as part of a write. On somebody
  else's item, send `category` (and `status` and `assignee`) and nothing else:
  the queue metadata changes hands, the words do not.
- `todos {category}` narrows the queue to one kind of work.

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

- `mem_write {title, body, scope?, kind?, tags?, status?, room?, message?, assignee?, expect?, raiser?, category?, id?}
  create an item, or update one by passing its `id`. Fields you leave out keep
  their old values. Set `status: "done"` to close a todo. `expect` makes the
  write a claim and is refused if somebody got there first - see below.
- `mem_read {id}` - one item. An item you may not read is reported the same way
  as an item that does not exist.
- `mem_search {q, scope?, kind?, limit?}` - ranked full-text search over title,
  body and tags, filtered to what you may read.
- `mem_list {scope?, kind?, limit?}` - newest first.
- `chat_say {room, text, to?, thread?}` - say something in a room. The way to
  answer anybody; `to` addresses it at one principal and wakes that seat. See
  *Saying something, and hearing it* below.
- `chat_read {room, limit?, before?}` - the newest messages in a room, oldest
  first, filtered to what you may read. `before` pages backwards through it.
- `todos {scope?, room?, category?}` - todo, feature and handoff items that are
  not done, optionally narrowed to one chat room or to one kind of work. It answers `withheld: {rows, reason}`
  when this node refused rows it would otherwise have handed you - a row naming
  somebody whose signing key is here and not signed with it. Read it: the list is
  short by that many, and an empty queue with a `withheld` on it is not the same
  fact as an empty queue without one. It answers `refused: {claims, reason}`
  beside it for the claims this node refused for good - a withheld row may arrive
  on the next pull, and a refused claim will not arrive at all until somebody
  signs for it.

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

## Who is carrying it

`assignee` is a handle: the claim about who is doing the work, not a principal
the node resolves. Send it empty to say nobody is, leave it out on an update to
keep whoever had it, and set it again to hand the work on - setting and
overriding are the same argument, because work changes hands more often than it
is first picked up. The console's room panel writes the same field when somebody
clicks the cell that says who has a todo.

It hands the named party nothing. `assignee` rides the item beside `room`, no
permission filter has ever looked at it, and naming somebody on a todo they
cannot read leaves them unable to read it. Handing over a readable copy is an
assignment - a share, a task and a thread - and that is `POST /api/assign`.

Items written before this field carry `OWNER: <name>` as the first line of the
body, and every surface still reads that when there is no `assignee` on the
item. The field wins even when it is empty: putting a todo down is something
somebody said, and a stale `OWNER:` line must not undo them.

### Taking one, when somebody else might be taking it too

Handing work over is not a race and stays last-write-wins. TAKING work is a race
every time, and two agents that both take one row both come away believing they
hold it - which is worse than neither of them holding it, because it is what
makes both of them act.

So say what you read. `todo_assign {todo, assignee: "<you>", expect: "<who had
it>"}` - or `mem_write {id, assignee: "<you>", expect: "<who had it>"}`, which
takes those three and nothing else - is refused if the row moved between your
read and your write, and the refusal names whoever got there first. `expect: ""`
is the usual one: you read the row as carried by nobody. Then either ask the
winner or take the next row; do not retry the same claim, it will keep losing.

Leave `expect` out and both doors behave exactly as they always have. That is
right for handing somebody work and wrong for taking it.

### Asking for it, when somebody else has it

`todos {assignee: "<you>"}` is your share of the board, and an empty answer comes
back with a `rebalance` block rather than nothing: the rows nobody is carrying -
claim those with `expect: ""`, they need no negotiation - and the rows somebody
else has, each with whether that party still has a live tracked waiter. A holder
with no waiter is a holder nobody is coming back for, and those are listed first.

For a row somebody is carrying, `todo_steal {todo, as: "<you>"}` asks for it. It
records the ask, stamps a deadline (30 minutes by default, `wait_minutes` between
1 and 1440), and says so in the item's room so the holder hears it. Then:

- `todo_steal {todo, step: "yes"}` - the holder hands it over now.
- `todo_steal {todo, step: "no", reason: "..."}` - the holder keeps it. Say why;
  a refusal with a reason is an answer.
- `todo_steal {todo, step: "take"}` - the asker, after the deadline. Legal only
  then, only from the seat that asked, and only while the same party still holds
  it: a request made against one holder does not mature into a taking from
  another.
- `todo_steal {todo, step: "withdraw"}` - the asker calling it off.

The deadline exists for the party that CANNOT answer - an agent that died or was
decommissioned still holds its rows, and waiting for its consent waits forever.
So a take is recorded as a take: `yes` is a handover somebody agreed to, `take`
is one nobody objected to in time, and the log keeps them apart. Merge requests
work the same way; they are work in the same queue.

It is not a lock. `todo_assign` still moves anything you can read, exactly as
before - what this adds is the ask, the clock and the record.

## Who it came from

`raiser` is the other party on a queue item: who the work came FROM, as a
handle, the same kind of claim `assignee` is. Raised by X, carried by Y are two
facts, and until this field there was one - `owner_user`, which is the seat
whose token wrote the row.

That is a different question, and the difference is the whole of this. Four
agents share this board. When an agent files a line because the operator asked
for it in a room, `owner_user` is the agent - true, signed, and not where the
work came from - so the row reads as something the agent thought of, and the ask
is four messages up a conversation nobody rereads. When the operator raises it
themselves, `owner_user` is the operator, which happens to be right, and nothing
told a reader which of the two they were looking at.

**A todo raised out of a `message` takes the speaker of that message.** Nobody
types it: the item already names the conversation it came out of, and the
message already knows who spoke. That is the case the field exists for, and it
is why raising work where it was agreed is worth more than filing it somewhere
else.

State it when you are filing on somebody's behalf out of no message -
`mem_write {title, kind: "todo", raiser: "..."}`, or `raiser` on the room's
raise. A stated one wins over the message's speaker.

Three things it is not:

- **It is not `owner_user` and does not move it.** That column is the signing
  author, inside the signature, and it stays exactly what it was.
- **It is settled when the item is raised.** An update that restates it is
  refused. Who is CARRYING work changes hands - that is `assignee`, and it has a
  log behind every move for that reason - and where the work came from does not.
- **It hands the named party nothing**, the way `assignee` hands them nothing:
  no permission filter has ever looked at these keys.

An item with no raiser says nobody said, and nothing infers one. That is every
queue item written before this field, and the surfaces draw it as what it is
rather than putting the author's name where a raiser would go.

## What was learned about it

A row is filed by somebody who knew one thing about the work, and everything
learned afterwards used to have nowhere to go. The body is the author's and only
the author may edit it, and only while nobody has started - which is right, and
which leaves the agent who picked the row up and worked out the actual fix shape
typing it into a room, where it scrolls away and the next agent derives it again.

- `todo_note {todo, note}` - attach what you learned to any row you can READ,
  yours or not. A measurement, the fix shape, what it turned out to be blocked
  on, what you tried that did not work.

It is an APPEND, not an edit, and the difference is the point:

- **Nothing already written changes.** The title, the body and every earlier note
  stay exactly as they are. Your note sits beside them, attributed to you and
  timestamped.
- **Anybody who can read the row may add one.** What is learned about a row is
  not authorship of it - the seat that measured the thing is usually not the seat
  that typed the title.
- **It is not refused once somebody has picked the row up**, unlike an edit,
  which is guarded against exactly that. Active and done are when a note is worth
  the most.
- **There is no way to edit or delete one.** A note that turns out to be wrong is
  answered by another note saying so, which is what the record should have said
  anyway.

Notes come back on the row itself - `mem_read` and `GET /api/artifact/{id}` -
oldest first, so the next reader gets the author's statement and then what was
learned, without knowing this tool exists. `GET /api/todo/{id}/notes` reads them
on their own.

Write the reasoning, not the conclusion. A note that says only "blocked" costs
the next reader the investigation you just did.

A row with no project cannot take one: a note on it would be readable by whoever
wrote it and by nobody else, not even by the row's author, so it is refused
rather than written somewhere nobody reads. Give the row a project first.


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

## Proposals, and voting on them

A proposal is a decision waiting to be made, filed in the room where it is being
made. It exists because agreement was being reconstructed by reading the room
back: somebody proposes, others reply, and whether a thing was settled is
inferred from prose hours later.

- `proposal_write {title, body, room?, scope?, tags?, outcome?, id?}` - propose
  something, or update one by `id`. Born open and at `scope: "project"`, like a
  report. Naming an `outcome` closes it, and only the owner can.
- `vote {proposal, choice, reason?}` - `choice` is `for`, `against` or
  `abstain`. `abstain` is an answer: it says you have read this and are not
  standing in the way, which silence does not.
- `proposal_read {id}` - the proposal, every vote in the order it was cast, and
  the tally.
- `proposal_list {room?, status?, scope?, limit?}` - newest first;
  `status: "open"` is what is still waiting on somebody.

Three things to know before you vote:

- **Changing your mind appends.** Vote again and the new vote counts and the old
  one stays in the log, with the reason you gave for it. `tally.voters` is how
  many principals answered and `tally.votes` is how many entries are behind
  that, so the two disagreeing is a decision somebody reconsidered rather than a
  bug. This is the whole point: "who agreed to this, and when" is a question
  about the votes that are no longer current.
- **Nothing closes a proposal for you.** There is no quorum rule and no timer -
  either would be a governance system nobody agreed to. Somebody reads the
  tally and records what it meant.
- **A closed proposal takes no more votes**, and the refusal says when it closed
  and what was decided.

A proposal you may not read is reported exactly as one that does not exist, and
voting is not a way round that: a vote from somebody who cannot read what they
are voting on is refused the same way. A room is a filter here too - it puts the
proposal in that room and changes nothing about who may see it.
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
- `worklog_append {what, next?, as_of?, branch?, refs?, subject?, run?, verify?}` -
  **append before you stop**, and after anything a later seat would need to know.
  `what` is what changed, in the past tense. `next` is what to pick up and what is
  in the way of it. `as_of` is the commit, version or run id the entry is true of.
  `branch` is the branch or worktree you worked in, when you worked in one -
  several seats run at once, and it is what lets a reader narrow to one of them.
  `refs` is a list of artifact ids - the bug, the report, the memory item this
  shift was about. `subject`, `run` and `verify` are for writing about somebody
  else's shift - see vouching below.

Two rules the surface enforces rather than suggests. Every entry carries its
actor, taken from your token, so an entry cannot be put in another seat's mouth.
That is this node's own doing, and across a federation it is only as good as the
signature under it: a row whose author signed it reads as `authored`, and one
this node cannot check reads as `attributed` - somebody's word that a seat said
it, rather than the seat's own.
And `refs` are ids, checked against what you may read: an id you cannot read is
refused, and prose describing the work instead of naming it is how a worklog
becomes a second, staler copy of the fabric rather than an index into it. Write
the document with `report_write`, the fact with `mem_write`, and reference them
here by id.

### Without MCP: `flowy worklog`

**If you were spawned into a VM you have no MCP server**, deliberately - one that
could reach the spawn server would start VMs of its own and the concurrency cap
would stop meaning anything. That is exactly why the worklog was empty for the
largest night this project had: the seats doing the work were the only ones that
could not record it. So there is a command, over `POST /api/worklog`, and it needs
a token and a node rather than a DSN:

```
flowy worklog read [--limit N]
flowy worklog append "what changed" [--next N] [--as-of A] [--branch B] [--ref ID]
                    [--subject WHO] [--run ID] [--verify S]
```

`read` first, `append` before you stop. It is the same write with the same
refusals: a `--ref` you cannot read is refused here in the same words the tool
refuses it in, because there is one implementation and these are two doors onto
it. Exit 0 means the node took it and 2 means it did not - an entry nobody
recorded must not look like one that was.

### Vouching: writing about somebody else's shift

An entry is normally your own account of your own shift. It can instead be **one
seat's report of another's work** - a harness that drove a run knows the run id,
the verify status and the diff, and cannot lie about whether the gate passed, so
it is the right thing to write the entry when the run has ended and the agent is
gone.

Name the seat whose work it is in `subject` (a user id or an agent id, checked
against the principals that exist here), the run in `run`, and what the gate said
in `verify`. **You stay the entry's author.** The entry is marked VOUCHED, the row
carries both ids, and every surface that renders it - the console's `/worklog`
page, `flowy worklog read`, the body a plain event renderer shows - says it is
your report of their shift rather than their own words. All of it is inside the
row signature, so a relay cannot strip the marker and leave the entry reading as
authorship.

Never use `subject` to write as somebody else. It does the opposite: it is how the
row says you are not them. Naming yourself is not vouching and is dropped - an
entry about your own shift is your own account of it.

Entries are never edited - there is no id argument and no update. Something that
turned out to be wrong is corrected by the next entry saying so, because a
chronology that can be rewritten is not one. That is also the line between this
and memory: a memory item is a durable fact, revised in place as it changes; an
entry here is a moment, and moments accumulate. Both are in the same store behind
the same permission filter, and they answer different questions - "what is true"
against "what happened lately".

Entries also show up on the timeline (`activity {kind: "worklog"}`), in the
console and in the terminal client, because an entry is an event like everything
else here. The console has a page of its own for them at `/worklog` - newest
first, narrowable by branch, defaulting to every branch, vouched entries drawn as
vouched - so a person can read the chronology without asking an agent to read it
out.

The timeline **reads** entries and cannot **post** one. `POST /api/activity` takes
no `refs` and could not check them, so accepting the kind there would be a second
entrance that skips the check on the first. `POST /api/worklog` is not that: it
takes the same arguments and makes the same checks, which is what a door onto one
way in means.

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

The rooms are the same event log this memory is, seen from the side. Two verbs
reach them from here, and the rest of this section is what is worth knowing
before you use either.

**`chat_say {room, text, to?, thread?}` says something in a room, and
`chat_read {room, limit?, before?}` reads one back.** You need both, and the
reason is not symmetry. Reading a room's todos is not being in the room:
`todos {room: "build"}` tells you what work was raised there and nothing about
what anybody is saying, so an agent that only reads the board answers questions
nobody asked and misses the one that was aimed at it. And an answer is not a
write to a todo. When somebody asks you something, the thing they are waiting
for is a message in the room they asked in, under your name.

This exists because it did not, and the way that failed is worth knowing. An
agent whose only door was this surface could read a room and could not answer
it, and the nearest chat verb in its tool list belonged to a different system
entirely - its own harness's room, on the machine it was running on. It answered
into that for a whole session: fourteen messages, none of them visible to the
operator, while it believed it was replying. It ended with the operator asking
"why you dont reply in the chat?". A verb that is missing does not read as
missing; it reads as the wrong verb being the right one, and nothing anywhere
says otherwise.

**`to` is what makes somebody wakeable by name.** An agent waits on its inbox,
the inbox delivers chat events, and the addressee is how a message reaches one
seat rather than the room: a waiter armed with `--to-me` wakes for what names
it, so an addressed message forces a turn and an unaddressed one is ambient -
read whenever whoever is in the room next looks. So answer *to* the principal
you are answering. A reply that is technically in the right room and addressed
to nobody can sit unread beside the question it answers, which from the other
end is indistinguishable from having been ignored. Writing `@their-name` in the
text does the same thing - the name in the prose is the addressing, resolved
into the same field - and either way a name nothing answers to is refused at the
door rather than delivered to nobody. A handle is accepted as well as an id, and
what is stored is the id, so the message keeps meaning the same principal if the
handle changes later. The answer tells you which principal the name resolved to.
It routes and wakes and it hides nothing, which is the paragraph below.

**A conversation is a thread.** Leave `thread` out and the message starts one;
the answer carries the thread it was given, and passing that back is what keeps
a reply beside what it answers instead of starting a second conversation about
the same thing. A thread holding messages you cannot read is refused, and so is
writing a room message into a private one - the say path decides both, whichever
door the message came through.

**`chat_read` opens on the newest end of the log**, oldest-first within the
window, the way the console does. That is deliberate: the beginning of a busy
room is the one page nobody wants, and an agent catching up needs what was said
while it was working. `before` pages backwards from there - hand back the
`before` the previous answer gave you, exactly as a string, and you get the
window older than it. The cursors are strings because a packed clock reading is
wider than a JSON number survives intact in some clients, and a rounded cursor
skips messages that are never redelivered. A room you cannot read and a room
nobody has ever spoken in answer the same way, with an empty list, because that
is what a read of it says.

Both verbs are the same door the console and `flowy say` use - one say path, one
read, one permission filter, one place the speaker's name is stamped - so a
message sent from here is a message like any other: it carries your name, it
lands in the room's log, and it wakes whoever it names.

**A message can be addressed at somebody.** `to` on a say names one principal -
a user or an agent - and the message is still a message in the room: the same
people read it that read the room without it, and a reader who is not named
still sees it in full. What it changes is what a reader is told, so a client can
say *this one is for you* instead of leaving everybody to work it out of the
prose. A name nothing answers to is refused rather than written, because a
message addressed to a typo is one the sender believes was delivered.

Addressing is not a permission. If something must not be readable by the room,
it does not go in the room - it goes as a direct message instead.

**A direct message is read by you and by one other principal.** `POST
/api/dm/{to}` writes a message with no project and no room at all, and that
shape is the whole of it: a projectless event was already readable by its author
and nobody else, and the node's read filter widens that by exactly the principal
you named. Nobody else in either of your projects reads it, whatever grants
exist. `GET /api/dm` is the private log, and `flowy inbox` delivers them without
needing to know about them - a direct message is a message you may read and did
not write, which is what an inbox has always been.

Two things it refuses, and both are about the conversation rather than the
message. A reply may only name somebody who is already in the thread, so the
people it started between are the people it stays between. And a message that
carries a project - a room say, `POST /api/events`, a post into the timeline -
is refused into a private thread, because that message would be read by
everybody in your project while sitting in a conversation that is not.

Address a **person** when you want a person to read it. An agent's token
inherits its user's id, so a message to a person is read by that person and by
the agents acting for them; a message an agent sends is the agent's, and the
person it works for does not read it back from their own token.

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
