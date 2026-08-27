package flowy

// The whose-move tool: say a row is waiting on somebody, over MCP.
//
// Tool declaration and one adapter, nothing else. Resolving a self-name to a
// handle, refusing a caller that resolves to nobody, deleting the keys rather
// than storing an empty and the entry left in the log are all
// store.SetWaitingOn's - the rule the note, category and assignment tools
// already follow, and the reason todowaiting.go carries the reasoning.
//
// THIS SURFACE IS WHY THE ROW WAS RAISED. A spawned agent has no CLI and no
// browser; MCP is the whole of what it can reach. Being blocked on somebody's
// answer with no way to say so is precisely the state that produced "3 rows
// assigned to orchestrator, all open" while none of the three was work it could
// do - and the alternative it reached for was handing the row to the person it
// was asking, which tells the board they are carrying work they are answering.

import (
	"context"
	"encoding/json"

	"github.com/deadtrickster/flowy/internal/store"
)

// waitingTools is the whose-move surface, appended in allTools so each surface
// stays its own file.
var waitingTools = []tool{
	{
		Name: "todo_waiting_on",
		Description: "Say that a row is waiting on SOMEBODY ELSE'S move, and what you asked " +
			"them. This does NOT hand the row over: you are still carrying it, and the " +
			"only thing that changes is that the board stops counting it as work waiting " +
			"for you and starts counting it as an answer owed by them. Use it the moment " +
			"you ask a question you cannot proceed without - of the operator, of another " +
			"agent - instead of assigning them the row, which says they are doing the work " +
			"rather than answering about it, and instead of leaving the question in a note, " +
			"which nothing counts. It CLEARS ITSELF: any note or write on the row by the " +
			"person you named is their answer, and the row goes back to being your work " +
			"without you doing anything. Pass an empty waiting_on to take a question back " +
			"you no longer need answered. 'me' resolves to your own handle. Naming somebody " +
			"hands them no access, exactly as an assignee does not - if they cannot already " +
			"read the row they will never be told they were asked.",
		InputSchema: object(props{
			"todo": str("The row's id."),
			"waiting_on": str("Whose move it is - a handle, as the roster spells it. " +
				"Empty takes the question back."),
			"asked": str("What you asked them, in your words. Optional, and worth " +
				"writing: a name with no question is a row somebody has to open and " +
				"work out what is wanted, which is the state this replaces."),
		}, []string{"todo", "waiting_on"}),
		call: todoWaitingOn,
	},
}

// todoWaitingOn is the tool over store.SetWaitingOn.
func todoWaitingOn(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Todo      string `json:"todo"`
		WaitingOn string `json:"waiting_on"`
		Asked     string `json:"asked"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	art, entry, err := m.db.SetWaitingOn(ctx, p, a.Todo, a.WaitingOn, a.Asked)
	if err != nil {
		return nil, err
	}
	// WHAT WAS STORED, read off the row rather than echoed from the request: a
	// self-name resolves in the store, so "me" and the handle it became are the
	// same call and only one of them is the answer. An echo would hand back a
	// name no roster can resolve and the caller would have no way to tell.
	return withFixtureWarning(ctx, m, p, map[string]any{
		"item": art, "waiting_on": store.WaitingOnOf(art), "asked": store.AskedOf(art),
		"entry": entry,
	}), nil
}
