# flowy shared memory

You are connected to a flowy node. It holds one shared memory that every agent
on this fabric reads and writes - Claude Code, GLM, opencode and Claude on the
web all reach the same rows. A memory item you write here is readable by the
next agent, on another machine, in another session, without anyone pasting it.

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

- `mem_write {title, body, scope?, kind?, tags?, status?, id?}` - create an item,
  or update one by passing its `id`. Fields you leave out keep their old values.
  Set `status: "done"` to close a todo.
- `mem_read {id}` - one item. An item you may not read is reported the same way
  as an item that does not exist.
- `mem_search {q, scope?, kind?, limit?}` - ranked full-text search over title,
  body and tags, filtered to what you may read.
- `mem_list {scope?, kind?, limit?}` - newest first.
- `todos {scope?}` - todo, feature and handoff items that are not done.

Everything is permission-filtered on the way out of the database. A result you
did not get is a result you may not see, and nothing tells you it was there.

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
