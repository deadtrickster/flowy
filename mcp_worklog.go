package main

// The worklog tools. A worklog entry is an event of type 'worklog' in the
// project's worklog room - not an artifact - and that is the whole of the
// design rather than an implementation detail.
//
// What the worklog is for: an agent taking over a seat needs to know what the
// last few seats did and where they stopped. Without it that recovery is done
// by reading somebody else's session transcript off disk, which is 2,581 lines
// of one agent's turns to learn how the gate is run.
//
// Three decisions, in the order they matter:
//
//   - it is events, not a new artifact type. An append-only per-project stream
//     is what the event DAG already is: two seats appending at once produce two
//     rows and no conflict, and the log's cursor, its permission filter and its
//     replication carry the worklog with no second copy of any of them. A
//     worklog artifact would be one document that concurrent seats edit, which
//     is the two-doors problem the reports surface already refused once.
//   - every entry carries an actor. Which seat wrote it is the first thing the
//     next one asks, so the actor is the token's - the agent it names, or the
//     user behind it - exactly as a chat message's is, and there is no argument
//     for it. An entry that could name somebody else is an entry nobody can
//     trust.
//   - entries reference artifacts by id, never by prose. refs is a list of
//     artifact ids, checked against the writer's own read filter before the
//     entry is written, which is what keeps the worklog an index into the
//     fabric rather than a second copy of it. A ref the writer cannot read is
//     refused the way a parent they cannot read is - see mayNameParents, and
//     store.UnreadableArtifacts, which is the same query for the other column.
//
// The read shape is recent-N, newest first: what happened lately, which is the
// handoff read. Search of the whole history is mem_search and report_search's
// job, and the timeline's q; a worklog that grew a query language would be a
// second corpus rather than the front page of one.
//
// It is not memory, and the two must not be merged. A memory item is a durable
// fact that gets revised - one row per fact, edited in place as it changes. An
// entry here is a moment: what changed, what is next, and what it was true of,
// never edited afterwards, because a chronology that can be rewritten is not a
// chronology. Same store, same permission filter, two read shapes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// worklogEventType is what an entry is in the log, and worklogRoom is where the
// entries sit - a room of their own, so the timeline and the chat views can
// tell a seat's log from a conversation without reading each row's type.
const (
	worklogEventType = "worklog"
	worklogRoom      = "worklog"
)

// maxWorklogField is the ceiling on what an entry may say in one field. It is
// small on purpose: an entry is an index into the fabric, so a wall of text
// here is a document that belongs in a report, or a fact that belongs in
// memory, with the entry pointing at it by id. The refusal says which.
const maxWorklogField = 4_000

// maxWorklogRefs is how many artifacts one entry may reference. An entry that
// points at fifty things is not an index into what happened, it is a table of
// contents for the project.
const maxWorklogRefs = 50

// maxWorklogBranch is the ceiling on the branch or worktree an entry names. It
// is a git ref or a directory, not a sentence: something longer than this is a
// description of where the work was, and a description belongs in what.
const maxWorklogBranch = 200

// defaultWorklogRead is how many entries a read hands back when the caller asks
// for no particular number - enough for a fresh seat to see the last few shifts
// and short enough to be read rather than skimmed.
const defaultWorklogRead = 20

// worklogTools is the worklog surface, appended in allTools rather than written
// into the memory list, so each surface stays its own file - the same rule the
// reports and observability tools follow. The verbs are shaped like mem_* and
// report_*, so an agent that has learned either transfers with no brief.
var worklogTools = []tool{
	{
		Name: "worklog_append",
		Description: "Append an entry to this project's worklog: what changed and " +
			"what is next, as of a commit, version or run id. The entry is stamped " +
			"with which seat wrote it. Reference the work by artifact id in refs " +
			"rather than describing it - the worklog is an index into what is here, " +
			"not a second copy of it. Append before you stop.",
		InputSchema: object(props{
			"what": str("What changed. One or two sentences, in the past tense, for " +
				"somebody who was not here."),
			"next": str("What the next seat should pick up, and anything in the way of it."),
			"as_of": str("What the entry is true of: a commit, a version or a run id. " +
				"Stated on the entry so no reader has to date it by guesswork."),
			"branch": str("The branch or worktree this shift worked in, if it worked in " +
				"one. Several seats run at once on separate branches, so a reader " +
				"narrowing to one has to be able to tell them apart."),
			"refs": strArray("Artifact ids this entry is about - the bug, the report, " +
				"the memory item. Ids you cannot read are refused."),
		}, []string{"what"}),
		call: worklogAppend,
	},
	{
		Name: "worklog_read",
		Description: "The most recent worklog entries you may read, newest first. " +
			"This is the first thing to read when you pick up a seat: what the last " +
			"few sessions did, where they stopped, and the ids of the work they mean.",
		InputSchema: object(props{
			"limit": integer("How many entries to return. Default 20."),
		}, nil),
		call: worklogRead,
	},
}

// worklogAppendArgs is what worklog_append takes.
type worklogAppendArgs struct {
	What   string   `json:"what"`
	Next   string   `json:"next"`
	AsOf   string   `json:"as_of"`
	Branch string   `json:"branch"`
	Refs   []string `json:"refs"`
}

// worklogEntry is one entry as a reader gets it back: the stream's own fields,
// plus where in the log it sits and who wrote it.
type worklogEntry struct {
	ID      string   `json:"id"`
	Actor   string   `json:"actor"`
	Project *string  `json:"project"`
	What    string   `json:"what"`
	Next    string   `json:"next,omitempty"`
	AsOf    string   `json:"as_of,omitempty"`
	Branch  string   `json:"branch,omitempty"`
	Refs    []string `json:"refs"`
	SeqHLC  int64    `json:"seq_hlc"`
	Node    string   `json:"node"`
	Created string   `json:"created"`
}

// worklogAppend writes one entry.
//
// It appends and never updates: there is no id argument, because an entry is a
// statement about a moment and a moment that can be edited afterwards is not
// one. Something that turned out to be wrong is corrected by the next entry
// saying so, which is what a chronology is.
func worklogAppend(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a worklogAppendArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	// Every entry carries an actor, so a token that resolves to nobody cannot
	// write one. This is the same bar reportWrite keeps for ownership, and here
	// it is the invariant itself rather than a consequence of one.
	if p == nil || p.UserID == "" {
		return nil, errors.New("this token resolves to no user, so it cannot write a worklog entry")
	}

	what := strings.TrimSpace(a.What)
	if what == "" {
		return nil, errors.New("an entry says what changed: what is required")
	}
	next := strings.TrimSpace(a.Next)
	for _, field := range []struct{ name, value string }{{"what", what}, {"next", next}} {
		if len(field.value) > maxWorklogField {
			return nil, fmt.Errorf("%s is %d bytes, over the %d ceiling - an entry indexes "+
				"what happened; write the document with report_write or the fact with "+
				"mem_write and reference it from refs",
				field.name, len(field.value), maxWorklogField)
		}
	}

	// Where the shift worked, when it worked somewhere in particular. It is
	// optional and stays optional: an entry written off a branch is still an
	// entry, and a reader narrowing by branch is narrowing rather than filing.
	branch := strings.TrimSpace(a.Branch)
	if len(branch) > maxWorklogBranch {
		return nil, fmt.Errorf("branch is %d bytes, over the %d ceiling - name the ref "+
			"or the worktree, and put the story in what", len(branch), maxWorklogBranch)
	}

	refs, err := worklogRefs(ctx, m, p, a.Refs)
	if err != nil {
		return nil, err
	}

	// The actor, and the kind of thing it is, the way chat stamps it: a console
	// or a TUI reading the timeline tells a person from the agent working for
	// them without a join per row.
	actor, actorKind := chatActor(p)
	meta, err := json.Marshal(map[string]any{
		"actor_kind": actorKind,
		"actor_user": p.UserID,
		"what":       what,
		"next":       next,
		"as_of":      strings.TrimSpace(a.AsOf),
		"branch":     branch,
		"refs":       refs,
	})
	if err != nil {
		return nil, err
	}

	// An entry lands in the principal's home project, like every other write, so
	// the worklog is per-project the same way a room is. A token with no project
	// writes an entry of its own that only it can read, which is what a
	// principal with no project has for everything else here.
	var project *string
	if p.Project != "" {
		home := p.Project
		project = &home
	}

	e := &store.Event{
		Type:    worklogEventType,
		Project: project,
		Room:    worklogRoom,
		Actor:   actor,
		Body:    worklogBody(what, next),
		Meta:    meta,
	}
	if err := m.db.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	// The entry, and the fixture line when this seat is writing its chronology
	// into demo seed data - see mcp_projects.go.
	return withFixtureWarning(ctx, m, p, map[string]any{"entry": entryOf(e)}), nil
}

// worklogRefs checks the artifact ids an entry references, and returns them
// cleaned up: blanks dropped, duplicates collapsed, the writer's order kept.
//
// A ref the caller cannot read is refused rather than stored, and the refusal
// names it. That is the invariant the surface exists to keep - an entry
// referencing work its author could not see is not an index into the fabric,
// it is an assertion about somebody else's - and it is the same check
// mayNameParents makes of an edge in the DAG, for the same reason: an id is a
// guess anybody can make.
func worklogRefs(ctx context.Context, m *mcpServer, p *store.Principal, asked []string) ([]string, error) {
	refs := make([]string, 0, len(asked))
	seen := map[string]bool{}
	for _, ref := range asked {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	if len(refs) > maxWorklogRefs {
		return nil, fmt.Errorf("an entry references %d artifacts, over the %d ceiling - "+
			"name the ones this shift was about", len(refs), maxWorklogRefs)
	}
	if len(refs) == 0 {
		return refs, nil
	}

	unreadable, err := m.db.UnreadableArtifacts(ctx, p, refs)
	if err != nil {
		return nil, err
	}
	if len(unreadable) > 0 {
		return nil, fmt.Errorf("ref %s is not an artifact you can read; a worklog entry "+
			"points at work that is in front of you, by id", unreadable[0])
	}
	return refs, nil
}

// worklogRead is the handoff read: the newest entries, newest first.
func worklogRead(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Limit int `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if a.Limit <= 0 {
		a.Limit = defaultWorklogRead
	}

	list, err := m.db.RecentEvents(ctx, p, store.EventQuery{
		Type:  worklogEventType,
		Limit: a.Limit,
	})
	if err != nil {
		return nil, err
	}
	entries := make([]worklogEntry, 0, len(list))
	for _, e := range list {
		entries = append(entries, entryOf(e))
	}
	return map[string]any{"count": len(entries), "entries": entries}, nil
}

// entryOf renders one event as the entry it is.
//
// What the entry said is read off meta, where the write put it, and the body is
// the fallback: an event of this type that arrived without meta - from a peer
// running a build that predates this surface, say - is still an entry and still
// says something, and dropping it from the read would be a gap in a chronology
// with nothing to say there was one.
func entryOf(e *store.Event) worklogEntry {
	entry := worklogEntry{
		ID: e.ID, Actor: e.Actor, Project: e.Project, What: e.Body,
		Refs: []string{}, SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format("2006-01-02T15:04:05.999999Z07:00"),
	}
	var fields map[string]json.RawMessage
	if len(e.Meta) == 0 || json.Unmarshal(e.Meta, &fields) != nil {
		return entry
	}
	if what := metaString(fields, "what"); what != "" {
		entry.What = what
	}
	entry.Next, entry.AsOf = metaString(fields, "next"), metaString(fields, "as_of")
	entry.Branch = metaString(fields, "branch")
	if raw, ok := fields["refs"]; ok {
		var refs []string
		if err := json.Unmarshal(raw, &refs); err == nil && refs != nil {
			entry.Refs = refs
		}
	}
	return entry
}

// worklogBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI. What changed comes first because that is the line those views
// show.
func worklogBody(what, next string) string {
	if next == "" {
		return what
	}
	return what + "\n\nnext: " + next
}
