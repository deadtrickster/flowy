package main

// The worklog's MCP surface: two tools over the write and the read that live in
// worklog.go.
//
// There is nothing here but the tool declarations and two adapters. The write,
// the reference check, the subject resolution and the refusal wording are
// worklog.go's, and MCP is one caller of that path rather than a parallel
// implementation of it - which is what makes "the HTTP door refuses what this
// tool refuses" one fact instead of two that have to be kept in step. See the
// header of worklog.go for what the worklog is and why it is shaped this way.

import (
	"context"
	"encoding/json"

	"github.com/deadtrickster/flowy/internal/store"
)

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
			"subject": str("The seat whose work this entry is about, when it is not " +
				"yours - a user id or an agent id. You stay the entry's author and it " +
				"is marked VOUCHED: written by you, about their shift. Use it when you " +
				"are recording a run on its behalf, and never to write as somebody " +
				"else. Leave it out for your own account of your own shift."),
			"run":    str("The run the work was done in, when the entry is about one."),
			"verify": str("What the gate said about that run - what a reader deciding whether to trust this entry is deciding about."),
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

// worklogAppend is the tool over appendWorklogEntry.
func worklogAppend(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a worklogAppendArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	e, err := appendWorklogEntry(ctx, m.db, p, a)
	if err != nil {
		return nil, err
	}
	// The entry, and the fixture line when this seat is writing its chronology
	// into demo seed data - see mcp_projects.go.
	return withFixtureWarning(ctx, m, p, map[string]any{"entry": entryOf(e)}), nil
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
