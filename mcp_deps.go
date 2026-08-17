package main

// The dependency tools. An edge between two todos is an event and the ready
// query is a reading of the reader's own graph - see internal/store/deps.go,
// which is where the rules are and why they are those rules.
//
// The verbs mirror the proposal surface, so an agent that has learned vote and
// proposal_read transfers with no brief: the write is a verb rather than a field
// on an update, because what is being recorded is WHO said A blocks B and WHEN,
// and there is nothing to hand back in and overwrite. dep_remove appends the
// entry that takes an edge back; it does not delete anything, and the log holds
// both afterwards.
//
// ready is the one an unattended drainer calls. It is why the rest of this
// exists: something reads the queue and starts a VM per item that can be
// started, so "can be started" has to be an answer that is safe when it is
// wrong in the direction it is most likely to be wrong - a blocker the reader
// cannot see.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// depTools is the dependency surface, appended in allTools rather than written
// into the memory list, so each surface stays its own file.
var depTools = []tool{
	{
		Name: "dep_add",
		Description: "Say that one todo cannot be started until another is done. " +
			"The edge is recorded as a signed entry naming both, so who said it and when " +
			"stay readable, and taking it back later appends rather than erases. Both ids " +
			"must be work items you can read; they may be in different projects, which is " +
			"the point. A todo cannot depend on itself, and an edge that would close a " +
			"cycle in the graph you can see is refused.",
		InputSchema: object(props{
			"todo":    str("The todo that is blocked."),
			"blocker": str("The todo it is waiting on."),
		}, []string{"todo", "blocker"}),
		call: depAdd,
	},
	{
		Name: "dep_remove",
		Description: "Say that a todo no longer depends on another one. It appends the " +
			"entry that takes the edge back - the original edge stays in the log, because " +
			"the record of who ordered the queue is the reason this is not a field.",
		InputSchema: object(props{
			"todo":    str("The todo that was blocked."),
			"blocker": str("The todo it was waiting on."),
		}, []string{"todo", "blocker"}),
		call: depRemove,
	},
	{
		Name: "dep_list",
		Description: "One todo's dependencies: whether it can be started, every todo still " +
			"in the way, and the log the graph was folded out of. A blocker you cannot read " +
			"comes back as known=false and counts as NOT done - it holds the todo, because " +
			"you cannot confirm something you cannot see is finished.",
		InputSchema: object(props{"todo": str("The todo's id.")}, []string{"todo"}),
		call:        depList,
	},
	{
		Name: "ready",
		Description: "The queue, in dependency order: outstanding work you may read, each " +
			"item saying whether it can be started now and what is in the way. Ready means " +
			"every todo blocking it is done AND somebody is carrying it. This is computed " +
			"for YOU - a blocker outside your reach holds its todo for you and not for " +
			"somebody who can see it, and the two answers are both correct. Use this rather " +
			"than todos before picking up work.",
		InputSchema: object(props{
			"room":  str("Only the items raised in this chat room."),
			"scope": enum("Narrow to one scope.", memScopes),
			"ready": boolean("Only the items that can be started right now. Default false, " +
				"which returns everything with its blockers, so a queue that has stopped " +
				"says why."),
			"limit": integer("Most items to return. Default 200."),
		}, nil),
		call: readyTool,
	},
}

// depArgs is what both writes take: the two ends.
type depArgs struct {
	Todo    string `json:"todo"`
	Blocker string `json:"blocker"`
}

func (a depArgs) ends() (todo, blocker string) {
	return strings.TrimSpace(a.Todo), strings.TrimSpace(a.Blocker)
}

func depAdd(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a depArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	todo, blocker := a.ends()
	e, err := m.db.AddDep(ctx, p, todo, blocker)
	if err != nil {
		return nil, err
	}
	return depAnswer(ctx, m, p, e)
}

func depRemove(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a depArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	todo, blocker := a.ends()
	e, err := m.db.RemoveDep(ctx, p, todo, blocker)
	if err != nil {
		return nil, err
	}
	return depAnswer(ctx, m, p, e)
}

// depAnswer is what a write hands back: the entry, and the state it leaves the
// todo in.
//
// The second half is there so that adding a blocker and finding the todo still
// not ready is one call rather than two - and so that an agent sees the shape of
// the answer it will get from ready, including a blocker it cannot resolve.
func depAnswer(ctx context.Context, m *mcpServer, p *store.Principal, e *store.Event) (any, error) {
	ready, err := m.db.Readiness(ctx, p, e.Artifact)
	if err != nil {
		return nil, err
	}
	log, err := m.db.DepLog(ctx, p, e.Artifact)
	if err != nil {
		return nil, err
	}
	return withFixtureWarning(ctx, m, p, map[string]any{
		"entry": store.DepEntryOf(e), "deps": ready, "log": log,
	}), nil
}

func depList(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Todo string `json:"todo"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(a.Todo)
	if id == "" {
		return nil, errors.New("todo is required")
	}
	ready, err := m.db.Readiness(ctx, p, id)
	if err != nil {
		return nil, err
	}
	log, err := m.db.DepLog(ctx, p, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deps": ready, "log": log}, nil
}

func readyTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Room  string `json:"room"`
		Scope string `json:"scope"`
		Ready bool   `json:"ready"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q, err := memQuery(a.Scope, "", a.Limit)
	if err != nil {
		return nil, err
	}
	// The room narrows and does not widen, exactly as it does for todos: without
	// it this is the whole queue, items with a room and items without.
	if q.Room, err = roomArg(a.Room); err != nil {
		return nil, err
	}

	rows, err := m.db.Ready(ctx, p, q)
	if err != nil {
		return nil, err
	}
	ready := len(store.ReadyOnly(rows))
	if a.Ready {
		rows = store.ReadyOnly(rows)
	}
	return map[string]any{"count": len(rows), "ready": ready, "items": rows}, nil
}
