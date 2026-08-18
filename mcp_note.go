package main

// The append tool: attach what was learned about a row, over MCP.
//
// There is nothing here but the tool declaration and one adapter. The write, the
// read-is-the-bar rule, the refusals and the entry it leaves in the log are
// store.AppendTodoNote's, and this is one caller of that path rather than a
// parallel implementation of it - the rule the category, deps, assignment and
// worklog tools already follow. See internal/store/todonote.go for what a note
// is and why an append is not an edit, and todonote.go for the door beside this
// one.
//
// This surface is the one that matters most for this verb. The agents that learn
// things about a row reach the fabric through MCP, and a door only HTTP knows
// about is a door they answer by typing into the room instead.

import (
	"context"
	"encoding/json"

	"github.com/deadtrickster/flowy/internal/store"
)

// noteTools is the append surface, appended in allTools rather than written into
// the memory list, so each surface stays its own file.
var noteTools = []tool{
	{
		Name: "todo_note",
		Description: "Attach what you LEARNED about a row - yours or anybody else's - without " +
			"changing a word of it. A measurement, the fix shape you worked out, what it " +
			"turned out to be blocked on, what you tried that did not work. ANY principal " +
			"who can read the row may add one: what is learned about a row is not " +
			"authorship of it, and the seat that measured the thing is usually not the one " +
			"that typed the title. The note is appended, attributed to you and timestamped; " +
			"nothing already written moves, no earlier note is touched, and there is no way " +
			"to edit or delete one afterwards - a note that turns out to be wrong is " +
			"answered by another note saying so. Unlike mem_write and the edit door it is " +
			"NOT refused once somebody has picked the row up, which is when it is most " +
			"worth writing. Notes come back on the row itself, so the next agent to read it " +
			"sees them under the body without knowing this tool exists. Use it instead of " +
			"saying what you learned in the room, where it scrolls away.",
		InputSchema: object(props{
			"todo": str("The row's id."),
			"note": str("What you learned about it. Prose, and the reasoning is the " +
				"valuable half - a note that says only 'blocked' costs the next reader " +
				"the same investigation you just did."),
		}, []string{"todo", "note"}),
		call: todoNote,
	},
}

// todoNote is the tool over store.AppendTodoNote.
func todoNote(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Todo string `json:"todo"`
		Note string `json:"note"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	art, entry, err := m.db.AppendTodoNote(ctx, p, a.Todo, a.Note)
	if err != nil {
		return nil, err
	}
	// The row, everything on it including the note just written, and the entry
	// itself - so a caller that wants to quote what it wrote has the id and the
	// timestamp without a second call. Nothing needs filling in: the append reads
	// the row through the permission-filtered read, which is where the queue
	// fields and the notes are put on it. The fixture line rides along for the
	// reason every other write's does: see mcp_projects.go.
	return withFixtureWarning(ctx, m, p, map[string]any{
		"item": art, "notes": art.Notes, "entry": store.TodoNoteEntryOf(entry),
	}), nil
}
