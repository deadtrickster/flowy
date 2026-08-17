package main

// The report tools. A report is an artifact of type 'report' with no kind and
// no lifecycle: a published document - research findings, a design, a review -
// filed where the project reads it, searched and permission-filtered by exactly
// the code the memory tools already ride. There is no second table and no
// second visibility rule, and the write is the same two rows under one clock
// reading that a memory write is.
//
// Three things make a report a report and not a long memory item:
//
//   - it is born in its project. A memory item defaults to personal because a
//     fact is usually somebody's before it is anybody's; a report exists to be
//     read by the project, so scope=project is the default.
//   - it carries what it was true of. An as_of field names the commit, version
//     or run a report was measured against, and supersedes names the report it
//     replaces - without those a report is a claim with no expiry, and every
//     reader has to guess whether it is current, silently.
//
//     supersedes points backwards, which is the only direction the writer can
//     name. The reader's question is the other one, and it is the one that
//     matters: they have the old document open and nothing on it says a newer
//     one exists. So every read here answers it - replaced_by, derived through
//     the same permission filter as the row it is on, see store.replacedBy.
//   - it has no work lifecycle. Bugs resolve and assign; reports are published
//     and later superseded. That behavior, not the document's genre, is what
//     earns the type - genre rides tags, which are free-form and searched.
//
// The verbs mirror the memory tools - write, read, search, list - so an agent
// that has learned mem_* transfers to report_* with no brief.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// reportType is the artifact type every one of these tools reads and writes.
const reportType = "report"

// maxReportBody is the ceiling on a report body, in bytes. It is generous for
// a document a person reads, and it keeps the searchable thing and the
// readable thing the same thing: the body is what report_search reaches, so a
// corpus that quietly outgrew the row would become a corpus search cannot see.
// Anything larger is an attachment referenced from the body - a summary plus
// the id attachment_write hands back - and the refusal says so rather than
// truncating a document behind the caller's back.
const maxReportBody = 100_000

// reportTools is the report surface, appended in allTools rather than written
// into the memory list, so each surface stays its own file - the same rule the
// observability tools follow.
var reportTools = []tool{
	{
		Name: "report_write",
		Description: "Write a report to the project, or update one by id. " +
			"A report is a finished document - findings, a design, a review - published " +
			"for the project to read, with no work lifecycle. Born at scope=project. " +
			"Say what it is true of with as_of, and which report it supersedes.",
		InputSchema: object(props{
			"title": str("One line, phrased as the document's claim."),
			"body":  str("The document itself, markdown, up to 100KB. Enough to stand alone."),
			"scope": enum("Who may read it. Default project.", memScopes),
			"tags":  strArray("Free-form labels, including the genre - research, design, review. Searched with the title and the body."),
			"as_of": str("What the report is true of: a commit, version or run id. " +
				"Stated on the report so no reader has to guess whether it is current."),
			"supersedes": str("Id of the report this one replaces. The old one stays " +
				"readable and every read of it now says replaced_by, so nobody who " +
				"finds it later reads it as current."),
			"status": str("Optional status, e.g. draft|final, for filtering in lists."),
			"id":     str("Update the report with this id instead of creating one."),
		}, nil),
		call: reportWrite,
	},
	{
		Name: "report_read",
		Description: "Read one report by id. A report you may not read is reported " +
			"exactly as one that does not exist. replaced_by on the answer is the " +
			"newer report that supersedes this one, when there is one you may read.",
		InputSchema: object(props{"id": str("The report's id.")}, []string{"id"}),
		call:        reportRead,
	},
	{
		Name: "report_search",
		Description: "Ranked full-text search over the reports you are allowed to " +
			"read - title, body and tags.",
		InputSchema: object(props{
			"q":     str("What to look for. Plain words, not a query language."),
			"scope": enum("Narrow to one scope.", memScopes),
			"limit": integer("Most results to return. Default 200."),
		}, []string{"q"}),
		call: reportSearch,
	},
	{
		Name:        "report_list",
		Description: "List reports you may read, newest first.",
		InputSchema: object(props{
			"scope": enum("Narrow to one scope.", memScopes),
			"limit": integer("Most reports to return. Default 200."),
		}, nil),
		call: reportList,
	},
}

// reportWriteArgs is what report_write takes.
type reportWriteArgs struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Scope      string   `json:"scope"`
	Status     string   `json:"status"`
	Tags       []string `json:"tags"`
	AsOf       string   `json:"as_of"`
	Supersedes string   `json:"supersedes"`
}

// reportWrite creates a report, or replaces one the principal owns. The rules
// are the memory write's rules, verbatim, because they are the fabric's rules
// and not the memory surface's: an id that names something unreadable is
// refused rather than treated as a create, a report of another type is not
// turned into one, and a principal writes in its own project or not at all.
func reportWrite(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a reportWriteArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if p.UserID == "" {
		return nil, errors.New("this token resolves to no user, so it cannot own a report")
	}

	// A report starts where a memory item ends up: in the project.
	scope, err := oneOf("scope", a.Scope, memScopes, "project")
	if err != nil {
		return nil, err
	}
	visibility := visibilityOf(scope)
	var home *string

	if len(a.Body) > maxReportBody {
		return nil, fmt.Errorf("report body is %d bytes, over the %d ceiling - "+
			"publish a summary as the body and put the full document through "+
			"attachment_write, naming the id it hands back, instead of carrying it "+
			"in the row", len(a.Body), maxReportBody)
	}

	art := &store.Artifact{
		ID:     a.ID,
		Type:   reportType,
		Title:  strings.TrimSpace(a.Title),
		Body:   a.Body,
		Status: a.Status,
		Tags:   a.Tags,
	}

	// What the report is true of rides fields, not columns: as_of and
	// supersedes are provenance, and an update that does not restate them keeps
	// what the report already said.
	var fields map[string]any

	if a.ID != "" {
		old, err := m.db.ReadArtifact(ctx, p, a.ID, false)
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("no such report: %s", a.ID)
		}
		if err != nil {
			return nil, err
		}
		if old.Type != reportType {
			// One namespace per surface: writing through this tool must not
			// pull a bug or a memory item out of the lifecycle it is in.
			return nil, notThere(a.ID)
		}
		if old.OwnerUser != p.UserID {
			return nil, fmt.Errorf("report %s belongs to somebody else", a.ID)
		}
		// An update states what changes; the rest of the document stands.
		if art.Title == "" {
			art.Title = old.Title
		}
		if art.Body == "" {
			art.Body = old.Body
		}
		if a.Status == "" {
			art.Status = old.Status
		}
		if a.Tags == nil {
			art.Tags = old.Tags
		}
		art.Discovery, art.Severity, art.Related = old.Discovery, old.Severity, old.Related
		art.FilePath = old.FilePath
		if len(old.Fields) > 0 {
			if err := json.Unmarshal(old.Fields, &fields); err != nil {
				return nil, fmt.Errorf("report %s carries fields that do not parse: %w", a.ID, err)
			}
		}
		// Where a report lives is not something an update says.
		home = old.Project
	} else if art.Title == "" && strings.TrimSpace(art.Body) == "" {
		return nil, errors.New("a report needs a title or a body")
	}

	if fields == nil {
		fields = map[string]any{}
	}
	if a.AsOf != "" {
		fields["as_of"] = a.AsOf
	}
	if a.Supersedes != "" {
		fields[store.SupersedesField] = a.Supersedes
	}
	if len(fields) > 0 {
		raw, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		art.Fields = raw
	}

	art.OwnerUser = p.UserID
	art.Visibility = visibility
	if visibility == store.VisibilityPersonal {
		art.Project = nil
	} else {
		if p.Project == "" {
			return nil, fmt.Errorf("this token has no project, so it can only write scope=personal, not %s",
				scopeOf(visibility))
		}
		if home == nil || *home == "" {
			if a.ID != "" {
				return nil, fmt.Errorf("report %s has no project and is its owner's alone; "+
					"an update cannot move it into %s as %s - create it there instead",
					a.ID, p.Project, scopeOf(visibility))
			}
			here := p.Project
			home = &here
		}
		if *home != p.Project {
			return nil, fmt.Errorf("report %s lives in project %s, and this token writes in %s",
				art.ID, *home, p.Project)
		}
		art.Project = home
	}

	actor := p.AgentID
	if actor == "" {
		actor = p.UserID
	}
	if err := m.db.WriteMemory(ctx, art, &store.Event{
		Type:  "report.write",
		Room:  "reports",
		Actor: actor,
		Body:  art.Title,
	}); err != nil {
		return nil, err
	}

	// A report is the surface most likely to be read months later by somebody
	// who was not here, which is exactly why it is the worst one to file into
	// demo seed data - see mcp_projects.go.
	return withFixtureWarning(ctx, m, p, map[string]any{"item": art}), nil
}

func reportRead(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if a.ID == "" {
		return nil, errors.New("id is required")
	}

	art, err := m.db.ReadArtifact(ctx, p, a.ID, false)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notThere(a.ID)
	}
	if err != nil {
		return nil, err
	}
	if art.Type != reportType {
		return nil, notThere(a.ID)
	}
	return map[string]any{"item": art}, nil
}

// reportQuery builds the store query every report read shares.
func reportQuery(scope string, limit int) (store.ArtifactQuery, error) {
	q := store.ArtifactQuery{Type: reportType, Limit: limit}
	if scope != "" {
		v, err := oneOf("scope", scope, memScopes, "")
		if err != nil {
			return q, err
		}
		q.Visibility = visibilityOf(v)
	}
	return q, nil
}

func reportSearch(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Q     string `json:"q"`
		Scope string `json:"scope"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Q) == "" {
		return nil, errors.New("q is required")
	}
	q, err := reportQuery(a.Scope, a.Limit)
	if err != nil {
		return nil, err
	}
	q.Query = a.Q

	hits, err := m.db.SearchArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"query": a.Q, "count": len(hits), "items": hits}, nil
}

func reportList(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope string `json:"scope"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q, err := reportQuery(a.Scope, a.Limit)
	if err != nil {
		return nil, err
	}

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(list), "items": list}, nil
}
