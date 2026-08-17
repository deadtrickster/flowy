package main

// The classification tool: what kind of work a todo is, over MCP.
//
// There is nothing here but the tool declaration and one adapter. The write, the
// closed vocabulary, the read-is-the-bar rule, the entry it leaves in the log and
// the refusal wording are store.SetTodoCategory's, and this is one caller of that
// path rather than a parallel implementation of it - which is what makes "the HTTP
// door refuses what this tool refuses" one fact instead of two that have to be kept
// in step. See the header of internal/store/todocategory.go for what a category is
// and why it is shaped this way, and category.go for the door beside this one.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// categoryTools is the classification surface, appended in allTools rather than
// written into the memory list, so each surface stays its own file - the rule the
// deps, assignment, worklog and report tools already follow.
var categoryTools = []tool{
	{
		Name: "todo_category",
		Description: "Say what KIND of work a todo is - yours or anybody else's - out of a " +
			"CLOSED set: " + strings.Join(store.TodoCategories, ", ") + ". Anything else is " +
			"REFUSED, which is the point: the set is small and fixed so the queue can be " +
			"counted and routed by it, and that is exactly what the free-form tags cannot " +
			"do. Use tags for everything the set does not cover - they are unlimited and " +
			"nothing here refuses them. ANY principal who can read a todo may set or " +
			"override its category: what kind of work something is, is a claim about the " +
			"work, and the seat that picked it up and found a bug underneath is usually not " +
			"the one that typed the title. Send it empty to say unclassified, which is what " +
			"most of this queue is and is not an error. The call is recorded as a signed " +
			"entry naming both ends, so who called it what and when stay readable, and an " +
			"override appends rather than erases. It does not touch the item's title or " +
			"body, which are its author's.",
		InputSchema: object(props{
			"todo": str("The todo's id."),
			"category": enumOrEmpty("What kind of work it is. Empty means unclassified.",
				store.TodoCategories),
		}, []string{"todo", "category"}),
		call: todoCategory,
	},
}

// enumOrEmpty is an enum that also takes the empty string, because unclassified
// is a value here rather than a silence and a schema that left it out would make
// taking back a wrong call the one thing a well-behaved client cannot do.
func enumOrEmpty(desc string, values []string) map[string]any {
	return map[string]any{
		"type": "string", "description": desc,
		"enum": append(append([]string{}, values...), ""),
	}
}

// todoCategory is the tool over store.SetTodoCategory.
//
// category is a required argument here and a pointer on mem_write, and the
// difference is deliberate: on an update that also carries a title and a status,
// an absent category has to mean "keep whatever it is filed as", while a verb whose
// whole job is to set it has nothing to do when it is not told one. Empty is still
// a value - it is how a classification is taken back.
func todoCategory(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Todo     string `json:"todo"`
		Category string `json:"category"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	art, _, err := m.db.SetTodoCategory(ctx, p, a.Todo, a.Category)
	if err != nil {
		return nil, err
	}
	// The item, what it is, who said so, the log behind it, and the vocabulary
	// itself - so an agent that guessed wrong reads what it may say instead out of
	// the same answer. The fixture line rides along for the reason every other
	// write's does: see mcp_projects.go.
	view, err := viewCategory(ctx, m.db, p, art)
	if err != nil {
		return nil, err
	}
	return withFixtureWarning(ctx, m, p, map[string]any{
		"item": view.Item, "category": view.Category,
		"standing": view.Standing, "log": view.Log, "vocabulary": view.Vocabulary,
	}), nil
}
