package main

// The assignment tool: who is carrying a todo, over MCP.
//
// There is nothing here but the tool declaration and one adapter. The write, the
// read-is-the-bar rule, the entry it leaves in the log and the refusal wording are
// store.AssignTodo's, and this is one caller of that path rather than a parallel
// implementation of it - which is what makes "the HTTP door refuses what this tool
// refuses" one fact instead of two that have to be kept in step. See the header of
// internal/store/assign.go for what an assignment is and why it is shaped this
// way, and assign.go for the doors beside this one.

import (
	"context"
	"encoding/json"

	"github.com/deadtrickster/flowy/internal/store"
)

// assignTools is the assignment surface, appended in allTools rather than written
// into the memory list, so each surface stays its own file - the rule the deps,
// worklog and report tools already follow.
var assignTools = []tool{
	{
		Name: "todo_assign",
		Description: "Say who is carrying a todo - yours or anybody else's. ANY " +
			"principal who can read a todo may set or override its assignee: that is " +
			"the operator handing work out, you claiming a task off the queue, and you " +
			"handing what you are carrying to the next seat. Send the assignee empty to " +
			"put the work down. The claim is recorded as a signed entry naming the todo " +
			"and the name, so who handed it over and when stay readable, and an override " +
			"appends rather than erases. It hands the named party NOTHING - who may read " +
			"the item is unchanged - and it does not touch the item's title or body, " +
			"which are its author's. Use this rather than mem_write for an item you did " +
			"not write: mem_write refuses one that is not yours.",
		InputSchema: object(props{
			"todo": str("The todo's id."),
			"assignee": str("Who is carrying it: a handle, as a claim about the work. " +
				"Empty means nobody is."),
		}, []string{"todo", "assignee"}),
		call: todoAssign,
	},
}

// todoAssign is the tool over store.AssignTodo.
//
// assignee is a required argument here and a pointer on mem_write, and the
// difference is deliberate: on an update that also carries a title and a status,
// an absent assignee has to mean "keep whoever has it", while a verb whose whole
// job is to move the assignee has nothing to do when it is not told one. Empty is
// still a value - it is how work is put down.
func todoAssign(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Todo     string `json:"todo"`
		Assignee string `json:"assignee"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	art, _, err := m.db.AssignTodo(ctx, p, a.Todo, a.Assignee, nil)
	if err != nil {
		return nil, err
	}
	// The item, who has it, who said so, and the log behind it - so an agent
	// claiming a task sees in one call that it landed and that nobody else had
	// claimed it first. The fixture line rides along for the reason every other
	// write's does: see mcp_projects.go.
	view, err := viewAssignment(ctx, m.db, p, art)
	if err != nil {
		return nil, err
	}
	return withFixtureWarning(ctx, m, p, map[string]any{
		"item": view.Item, "assignee": view.Assignee,
		"assignment": view.Assignment, "log": view.Log,
	}), nil
}
