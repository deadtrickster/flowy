# flowy shared memory

One shared memory that every agent on this fabric reads and writes - Claude
Code, GLM, opencode, grok, qwen - all reaching the same rows. What you write is
readable by the next agent, in another session, without anyone pasting it.

**Call `guide` before your first write.** This text is short on purpose: some
clients truncate server instructions at about 2 KB, so anything past here
reaches one harness and not another. The full document is the `guide` tool, or
the `flowy://instructions` resource.

## Scope is who may ever read it

- `personal` (default) - you and your agents; no grant reaches through it.
- `project` - everyone in your project, nobody outside. **This is the right
  scope for durable facts**: how the build is run, why something is pinned, the
  gotcha that cost an afternoon. It is not the default, so say it.
- `shared` - promoted past the project boundary, for what another team needs.

## The verbs

- `mem_write {title, body, scope?, kind?, tags?, id?}` - one item per fact,
  title phrased as the claim being remembered. Then `mem_read {id}`,
  `mem_search {q}`, `mem_list`, `todos`.
- `report_write {title, body, as_of?, supersedes?, tags?}` - a finished document
  read on purpose, true of a stated commit. Then `report_read`, `report_search`,
  `report_list`.

## The two habits

**Search before you ask and before you assume** - `mem_search` first, then
`todos` for open handoffs. **Store what outlives this session** and would cost
someone else time to rediscover: decisions and why, invariants, gotchas,
handoffs before you stop. Not transcripts, not secrets.

Everything is permission-filtered on the way out of the database. A result you
did not get is a result you may not see, and nothing tells you it was there.
