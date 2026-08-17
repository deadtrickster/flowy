package main

// The project surface an agent gets: what projects exist, which one this token
// writes into, and a line on every write that lands in a fixture.
//
// The line is the point, and it is worth being precise about what it does and
// does not do. A day of real shared memory was filed into `pa` - the smoke
// seeder's fixture project - because the default operator token is scoped there
// and nothing anywhere said so. The registry would not have refused that write:
// pa is a legitimate, writable project and the write was valid. What is missing
// is not a rule, it is a sentence at the moment of the write. So mem_write,
// report_write and worklog_append carry one back when the project they landed
// in is a fixture, in the same result as the item, where an agent cannot fail
// to be handed it.
//
// The enumeration is the smaller half. It is permission-filtered like every
// other read - the projects you are in, and the ones on the other end of a live
// grant - and it decides nothing: a project you can see is not a project you
// can write into, because a write lands in your token's own project and nowhere
// else.

import (
	"context"
	"encoding/json"

	"github.com/deadtrickster/flowy/internal/store"
)

// projectTools are appended to the tool list in mcp_tools.go, like the reports
// and the worklog: one surface, one file.
var projectTools = []tool{
	{
		Name: "projects",
		Description: "Which project this token writes into, whether that project is a " +
			"fixture (demo seed data rather than real work), and the projects you may " +
			"see. Everything you write with mem_write, report_write and worklog_append " +
			"lands in the current one - check it before a first write in a new session.",
		InputSchema: object(props{}, nil),
		call:        projectsTool,
	},
}

// projectView is one registry row as an agent gets it back. It is deliberately
// less than the row: an agent needs the name, where it came from and whether it
// is real work, and the reading, the node and the signature are the fabric's
// business rather than the caller's.
type projectView struct {
	ID      string `json:"id"`
	Origin  string `json:"origin,omitempty"`
	Fixture bool   `json:"fixture"`
	Current bool   `json:"current,omitempty"`
}

// projectsTool answers the indicator first and the list second.
func projectsTool(ctx context.Context, m *mcpServer, p *store.Principal, _ json.RawMessage) (any, error) {
	list, err := m.db.ListProjects(ctx, p, false)
	if err != nil {
		return nil, err
	}
	out := make([]projectView, 0, len(list))
	for _, project := range list {
		out = append(out, projectView{
			ID: project.ID, Origin: project.Origin, Fixture: project.Fixture,
			Current: p != nil && project.ID == p.Project,
		})
	}

	answer := map[string]any{"count": len(out), "projects": out}
	if p != nil {
		answer["current"] = p.Project
	}
	if warning := fixtureWarning(ctx, m.db, p); warning != "" {
		answer["warning"] = warning
	}
	return answer, nil
}

// fixtureWarning is the sentence a write into a fixture project comes back
// with, and the empty string everywhere else.
//
// It reads the registry rather than a list of names in this file, because which
// projects are fixtures is a fact about the database and replicates like one -
// a peer's fixture is a fixture here too, and a project that stops being one
// stops warning without a rebuild.
//
// It never fails the call it is attached to. A warning that could turn a
// successful write into an error would be a warning nobody dares attach.
//
// It takes the store rather than the MCP server because the HTTP doors onto the
// same writes answer with the same sentence - see handleWorklogAppend. A warning
// that only one door gives is a warning that depends on which client an agent
// happened to be holding.
func fixtureWarning(ctx context.Context, db *store.DB, p *store.Principal) string {
	if p == nil || p.Project == "" {
		return ""
	}
	project, err := db.Project(ctx, p.Project)
	if err != nil || project == nil || !project.Fixture {
		return ""
	}
	return "this token writes into " + project.ID + ", which is a FIXTURE project - " +
		"demo seed data, not real work. The write landed and is valid. If this is real " +
		"work, it belongs in a real project: ask for a token scoped to one."
}

// withFixtureWarning attaches the warning to a write's result.
//
// It takes the answer the tool already built rather than being called before
// it, so the warning can never be the reason a write does not happen: by the
// time this runs the row is in the database.
func withFixtureWarning(ctx context.Context, m *mcpServer, p *store.Principal,
	answer map[string]any,
) map[string]any {
	if warning := fixtureWarning(ctx, m.db, p); warning != "" {
		answer["warning"] = warning
	}
	return answer
}
