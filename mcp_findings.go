package main

// The finding tools. A finding is an artifact of type 'finding' in the issue
// workflow - lifecycle.go says so in one more word added to lifecycleTypes -
// so it is a bug's shape under another name: Kind, Severity, Tags, Related and
// Discovery are the same Artifact columns a bug carries, the same permission
// filter and the same search index serve it, and there is no second table.
//
// Two things a finding needs that a bug's own doors (POST /api/artifacts,
// mem_write) never had to: a reproduction, and a record of whether it still
// reproduces. Both already exist and both are used here rather than
// reimplemented:
//
//   - internal/store/findingrepro.go is the repro tree. finding_write hands it
//     the files stated as repro and nothing else - one WriteAttachment per
//     file, same as attachment_write, and the manifest replacing whatever was
//     recorded before.
//   - internal/store/findingruns.go is the run log. finding_run_record and
//     finding_run_list are thin wrappers over RecordFindingRun and FindingRuns:
//     every rule - readable, projected, named a version - lives there.
//
// The verbs mirror report_write's shape - write, read, search, list - plus the
// two run verbs, so an agent that has learned mem_* and report_* transfers to
// finding_* with no brief.
//
// status is not an argument here. A finding moves through the issue workflow
// exactly the way a bug does - open -> triaged -> in-progress -> in-review ->
// done, or out to wont-fix or duplicate - and that move is POST
// /api/artifact/{id}/status, which appends the trail event the workflow's
// audit depends on. A status argument on this tool would be a second door onto
// the same column with no event behind it: a transition nobody could account
// for. A finding is born open (see the explicit set below, not left blank -
// mem_write's create path had to close the same bug for todos, see the head of
// mcp_tools.go: a blank status column is invisible to a status narrow, not
// equal to "open" to anything but statusOf's in-memory fallback).
//
// Attaching a repro tree is collaborative in the way a status move or a
// dependency edge is, per WriteFindingRepro's own doc comment: read permission
// is the write permission there. So a principal who does not own a finding may
// still call finding_write with only {id, repro, ...} - findingWriteReproOnly
// is the door, and it refuses outright the moment anything else is stated,
// same shape as memWriteQueueOnly in mcp_tools.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// findingType is the artifact type every one of these tools reads and writes.
// The same literal internal/store/findingrepro.go declares as its own
// unexported findingType - re-declared here for the reason
// reproAttachmentType is re-declared in that file: this package cannot import
// an unexported store constant, and it must stay this exact string.
const findingType = "finding"

// maxFindingBody is the ceiling on a finding's body and its discovery, in
// bytes - report's ceiling, for report's reason: each is a field search
// reaches, so a corpus that quietly outgrew the row would be a corpus search
// cannot see. Over it is an attachment, referenced by the id
// attachment_write hands back, never a silent truncation.
const maxFindingBody = 100_000

// findingStatuses is the issue workflow's seven words, in the order
// lifecycle.go states them, for the schema and for a refusal that has to name
// them. knownStatus, not this list, is what actually validates one - this is
// display order, that is the source of truth.
var findingStatuses = []string{
	statusOpen, statusTriaged, statusInProgress, statusInReview, statusDone,
	statusWontFix, statusDuplicate,
}

// findingTools is the finding surface, appended in allTools rather than
// written into the memory list, so each surface stays its own file - the rule
// the report, proposal and attachment tools follow.
var findingTools = []tool{
	{
		Name: "finding_write",
		Description: "Write a finding to the project, or update one by id. A finding " +
			"is a bug's workflow under another name: Kind, Severity, Tags, Related and " +
			"Discovery are what it carries, the same columns a bug carries. It is born " +
			"open; moving it through triaged, in-progress, in-review, done, wont-fix or " +
			"duplicate is POST /api/artifact/{id}/status, not this tool, because that " +
			"route is what leaves the trail event the workflow's audit depends on. " +
			"Attach a reproduction with repro - each file becomes its own attachment, " +
			"the way attachment_write's would, and the whole tree is stated fresh on " +
			"every call that includes it. A principal who does not own this finding " +
			"may still send {id, repro, ...}: attaching a repro tree is collaborative, " +
			"the way a status move is, and read permission is the whole bar.",
		InputSchema: object(props{
			"title": str("One line: what was found."),
			"body": str("What was found, for somebody who was not here. Up to 100KB - " +
				"over that, attachment_write the rest and reference the id here."),
			"discovery": str("The investigation write-up: how this was found, what was " +
				"tried, what the evidence shows. Searched with the title and the body. " +
				"Same 100KB ceiling as body, same fix over it."),
			"severity": str("Free text, e.g. low, medium, high, critical. Leaving it out " +
				"on an update keeps what the finding already says."),
			"kind": str("What kind of finding this is, e.g. crash, race, correctness, " +
				"perf, security. Free text. Leaving it out on an update keeps what the " +
				"finding is filed as."),
			"scope":   enum("Who may read it. Default project.", memScopes),
			"tags":    strArray("Free-form labels, searched with the title and the body."),
			"related": strArray("Ids of artifacts this finding relates to - a bug it duplicates, a report it feeds."),
			"repro": map[string]any{
				"type": "array",
				"description": "Files that make up a reproduction of this finding - a " +
					"script, evidence, a snippet. Each becomes its own attachment - one " +
					"id, one digest, written once. This call states the WHOLE TREE " +
					"fresh, replacing whatever repro was recorded before. Leave it out " +
					"to leave the recorded repro tree untouched.",
				"items": object(props{
					"path":           str("The file's path inside the tree, e.g. repro-01-crash.sh."),
					"content_base64": str("The file's bytes, base64 encoded."),
				}, []string{"path", "content_base64"}),
			},
			"repro_entrypoint": str("REPRO ONLY. Which file in the tree a runner executes."),
			"repro_interp": str("REPRO ONLY. What runs the entrypoint - bash, python3 - " +
				"or leave empty to execute it directly."),
			"isolation": str("REPRO ONLY. What the tree wants to run inside - vm, " +
				"container - or leave empty for a runner's own default."),
			"cmd_override": str("REPRO ONLY. The rare tree whose command line is not " +
				"\"interp entrypoint\": a full command a runner should use instead."),
			"id": str("Update the finding with this id instead of creating one. On a " +
				"finding somebody else owns, this call may carry only id and repro - " +
				"see repro above - and refuses outright if it states anything else."),
		}, nil),
		call: findingWrite,
	},
	{
		Name: "finding_read",
		Description: "Read one finding by id. A finding you may not read is reported " +
			"exactly as one that does not exist.",
		InputSchema: object(props{"id": str("The finding's id.")}, []string{"id"}),
		call:        findingRead,
	},
	{
		Name: "finding_search",
		Description: "Ranked full-text search over the findings you are allowed to " +
			"read - title, body, discovery and tags.",
		InputSchema: object(props{
			"q":      str("What to look for. Plain words, not a query language."),
			"scope":  enum("Narrow to one scope.", memScopes),
			"status": enum("Narrow to one status in the issue workflow.", findingStatuses),
			"limit":  integer("Most results to return. Default 200."),
		}, []string{"q"}),
		call: findingSearch,
	},
	{
		Name:        "finding_list",
		Description: "List findings you may read, newest first.",
		InputSchema: object(props{
			"scope":  enum("Narrow to one scope.", memScopes),
			"status": enum("Narrow to one status in the issue workflow.", findingStatuses),
			"limit":  integer("Most findings to return. Default 200."),
		}, nil),
		call: findingList,
	},
	{
		Name: "finding_run_record",
		Description: "Record one repro run's verdict: which version of the repro tree " +
			"ran, at which commit, whether it reproduced the finding. Appends to the " +
			"finding's run log rather than overwriting the last verdict - version 3 " +
			"failing after version 2 passed is the fact worth keeping, and a rerun is " +
			"the whole reason this exists. A thin wrapper over store.RecordFindingRun: " +
			"every rule - readable, in a project, names a version - lives there.",
		InputSchema: object(props{
			"finding":   str("The finding's id."),
			"version":   str("The version of the repro tree this run used."),
			"sha":       str("The commit the tree ran at."),
			"confirmed": boolean("Whether this run reproduced the finding."),
			"status":    str("Free text: what came of the run."),
		}, []string{"finding", "version"}),
		call: findingRunRecord,
	},
	{
		Name: "finding_run_list",
		Description: "Every run recorded against a finding, oldest first - so " +
			"red-then-green across reruns of the same version is visible rather than " +
			"only the latest verdict. A thin wrapper over store.FindingRuns.",
		InputSchema: object(props{"finding": str("The finding's id.")}, []string{"finding"}),
		call:        findingRunList,
	},
}

// findingWriteArgs is what finding_write takes.
type findingWriteArgs struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Body            string         `json:"body"`
	Discovery       string         `json:"discovery"`
	Severity        string         `json:"severity"`
	Kind            string         `json:"kind"`
	Scope           string         `json:"scope"`
	Tags            []string       `json:"tags"`
	Related         []string       `json:"related"`
	Repro           []reproFileArg `json:"repro"`
	ReproEntrypoint string         `json:"repro_entrypoint"`
	ReproInterp     string         `json:"repro_interp"`
	Isolation       string         `json:"isolation"`
	CmdOverride     string         `json:"cmd_override"`
}

// reproFileArg is one file of the repro tree as the caller states it: a path
// and its bytes, base64. It becomes a store.ReproSource once decoded.
type reproFileArg struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

// findingWrite creates a finding, or replaces one the principal owns - and, on
// one it does not own, attaches a repro tree and nothing else. See the head of
// this file for why status is not among its arguments.
func findingWrite(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a findingWriteArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if p.UserID == "" {
		return nil, errors.New("this token resolves to no user, so it cannot own a finding")
	}

	var old *store.Artifact
	if a.ID != "" {
		var err error
		old, err = m.db.ReadArtifact(ctx, p, a.ID, false)
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.NotAFindingError{ID: a.ID}
		}
		if err != nil {
			return nil, err
		}
		if old.Type != findingType {
			// Readable, and not a finding: one namespace, and writing through
			// this tool must not be a way to turn a bug or a report into one.
			return nil, store.NotAFindingError{ID: a.ID}
		}
		if old.OwnerUser != p.UserID {
			return findingWriteReproOnly(ctx, m, p, old, a)
		}
	}

	scope, err := oneOf("scope", a.Scope, memScopes, "project")
	if err != nil {
		return nil, err
	}
	visibility := visibilityOf(scope)
	var home *string

	if len(a.Body) > maxFindingBody {
		return nil, fmt.Errorf("finding body is %d bytes, over the %d ceiling - "+
			"publish a summary as the body and put the full document through "+
			"attachment_write, naming the id it hands back, instead of carrying it "+
			"in the row", len(a.Body), maxFindingBody)
	}
	if len(a.Discovery) > maxFindingBody {
		return nil, fmt.Errorf("finding discovery is %d bytes, over the %d ceiling - "+
			"the same ceiling body carries, and the same fix: attachment_write the "+
			"full write-up and reference the id here", len(a.Discovery), maxFindingBody)
	}

	art := &store.Artifact{
		ID:        a.ID,
		Type:      findingType,
		Title:     strings.TrimSpace(a.Title),
		Body:      a.Body,
		Discovery: a.Discovery,
		Severity:  strings.TrimSpace(a.Severity),
		Kind:      strings.TrimSpace(a.Kind),
		Tags:      a.Tags,
		Related:   a.Related,
	}
	var fields map[string]any

	if old != nil {
		// An update states what changes; the rest of the finding stands.
		if art.Title == "" {
			art.Title = old.Title
		}
		if art.Body == "" {
			art.Body = old.Body
		}
		if a.Discovery == "" {
			art.Discovery = old.Discovery
		}
		if a.Severity == "" {
			art.Severity = old.Severity
		}
		if a.Kind == "" {
			art.Kind = old.Kind
		}
		if a.Tags == nil {
			art.Tags = old.Tags
		}
		if a.Related == nil {
			art.Related = old.Related
		}
		if a.Scope == "" {
			visibility = old.Visibility
		}
		// The issue workflow's, moved only through POST /api/artifact/{id}/status -
		// see the head of this file. Restating it here would be a transition
		// with no trail event behind it.
		art.Status = old.Status
		art.FilePath = old.FilePath
		if len(old.Fields) > 0 {
			if err := json.Unmarshal(old.Fields, &fields); err != nil {
				return nil, fmt.Errorf("finding %s carries fields that do not parse: %w", a.ID, err)
			}
		}
		// Where a finding lives is not something an update says - reportWrite's
		// rule, for the same reason.
		home = old.Project
	} else if art.Title == "" && strings.TrimSpace(art.Body) == "" {
		return nil, errors.New("a finding needs a title or a body")
	} else {
		// Explicit, not left blank: a blank status column does not match
		// store.ArtifactQuery{Status: "open"}, only statusOf's in-memory
		// fallback does - exactly the bug mem_write's create path had to close
		// for todos, see the head of mcp_tools.go. A finding starts the issue
		// workflow the same way a bug does.
		art.Status = statusOpen
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
				return nil, fmt.Errorf("finding %s has no project and is its owner's alone; "+
					"an update cannot move it into %s as %s - create it there instead",
					a.ID, p.Project, scopeOf(visibility))
			}
			here := p.Project
			home = &here
		}
		if *home != p.Project {
			return nil, fmt.Errorf("finding %s lives in project %s, and this token writes in %s",
				art.ID, *home, p.Project)
		}
		art.Project = home
	}

	actor := p.AgentID
	if actor == "" {
		actor = p.UserID
	}
	if err := m.db.WriteMemory(ctx, art, &store.Event{
		Type:  "finding.write",
		Room:  "findings",
		Actor: actor,
		Body:  art.Title,
	}); err != nil {
		return nil, err
	}

	if len(a.Repro) > 0 {
		refreshed, err := attachFindingRepro(ctx, m, p, art.ID, a)
		if err != nil {
			return nil, err
		}
		art = refreshed
	}

	return withFixtureWarning(ctx, m, p, map[string]any{"item": art}), nil
}

// findingWriteReproOnly is finding_write on a finding this principal does not
// own. A finding's words - title, body, discovery, severity, kind, tags,
// related, scope - are its author's, memWriteQueueOnly's rule in mcp_tools.go
// verbatim. A repro tree is not: WriteFindingRepro says so itself, read
// permission is the write permission there, the same as a status move or a
// dependency edge, because a participant who could not attach their own
// reproduction would have to ask somebody else to.
//
// Stating anything but id and repro (and the manifest fields that ride with
// it) is refused outright rather than silently narrowed to what is allowed -
// a write that kept the old title while claiming success would be a success
// envelope that changed something other than what it was asked to.
func findingWriteReproOnly(
	ctx context.Context, m *mcpServer, p *store.Principal, old *store.Artifact, a findingWriteArgs,
) (any, error) {
	var stated []string
	for _, field := range []struct {
		name  string
		given bool
	}{
		{"title", strings.TrimSpace(a.Title) != ""},
		{"body", a.Body != ""},
		{"discovery", a.Discovery != ""},
		{"severity", a.Severity != ""},
		{"kind", a.Kind != ""},
		{"tags", a.Tags != nil},
		{"related", a.Related != nil},
		{"scope", a.Scope != ""},
	} {
		if field.given {
			stated = append(stated, field.name)
		}
	}
	if len(stated) > 0 {
		return nil, refuseForbidden("finding %s belongs to somebody else, so its %s "+
			"%s not yours to change: a finding's words are its author's. Attaching a "+
			"repro tree is not - read permission is the whole bar there, the same as "+
			"a status move or a dependency edge. This write stated more than repro, "+
			"so none of it was made", old.ID, strings.Join(stated, ", "),
			plural(len(stated), "is", "are"))
	}
	if len(a.Repro) == 0 {
		return nil, refuseForbidden("finding %s belongs to somebody else, so this write "+
			"has to attach a repro tree: state repro, or ask the author to change "+
			"anything else", old.ID)
	}
	art, err := attachFindingRepro(ctx, m, p, old.ID, a)
	if err != nil {
		return nil, err
	}
	return withFixtureWarning(ctx, m, p, map[string]any{"item": art}), nil
}

// attachFindingRepro decodes every file the call named as repro and hands
// them to store.WriteFindingRepro - the one door a repro tree goes through,
// so a repro file is an ordinary attachment in every other way, findable by
// attachment_list and readable by attachment_read like any other. Decoding
// goes through decodeAttachment, attachment_write's own function: the same
// base64 rules and the same 4MB per-file ceiling, because a repro file is not
// a new kind of payload.
//
// It returns the finding re-read after the manifest lands, so a caller sees
// repro_files on the row it gets back rather than the row as it was before
// this call.
func attachFindingRepro(
	ctx context.Context, m *mcpServer, p *store.Principal, findingID string, a findingWriteArgs,
) (*store.Artifact, error) {
	sources := make([]store.ReproSource, 0, len(a.Repro))
	for _, f := range a.Repro {
		content, err := decodeAttachment(f.ContentBase64)
		if err != nil {
			return nil, fmt.Errorf("repro file %s: %w", f.Path, err)
		}
		sources = append(sources, store.ReproSource{Path: f.Path, Content: content})
	}
	manifest := store.ReproManifest{
		Entrypoint:  a.ReproEntrypoint,
		Interp:      a.ReproInterp,
		Isolation:   a.Isolation,
		CmdOverride: a.CmdOverride,
	}
	if _, err := m.db.WriteFindingRepro(ctx, p, findingID, sources, manifest); err != nil {
		return nil, err
	}
	art, err := m.db.ReadArtifact(ctx, p, findingID, false)
	if err != nil {
		return nil, err
	}
	return art, nil
}

func findingRead(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
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
		return nil, store.NotAFindingError{ID: a.ID}
	}
	if err != nil {
		return nil, err
	}
	if art.Type != findingType {
		return nil, store.NotAFindingError{ID: a.ID}
	}
	return map[string]any{"item": art}, nil
}

// findingQuery builds the store query every finding read shares.
func findingQuery(scope, status string, limit int) (store.ArtifactQuery, error) {
	q := store.ArtifactQuery{Type: findingType, Limit: limit}
	if scope != "" {
		v, err := oneOf("scope", scope, memScopes, "")
		if err != nil {
			return q, err
		}
		q.Visibility = visibilityOf(v)
	}
	if status != "" {
		// knownStatus, lifecycle.go's own gate, so this door and the status
		// move never disagree about what a valid word is.
		if !knownStatus(status) {
			return q, fmt.Errorf("status must be one of %s, not %q",
				strings.Join(findingStatuses, ", "), status)
		}
		q.Status = status
	}
	return q, nil
}

func findingSearch(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Q      string `json:"q"`
		Scope  string `json:"scope"`
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Q) == "" {
		return nil, errors.New("q is required")
	}
	q, err := findingQuery(a.Scope, a.Status, a.Limit)
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

func findingList(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope  string `json:"scope"`
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q, err := findingQuery(a.Scope, a.Status, a.Limit)
	if err != nil {
		return nil, err
	}

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(list), "items": list}, nil
}

// findingRunRecord is a thin wrapper over store.RecordFindingRun: every rule -
// the finding must be readable, must have a project, the run must name a
// version - lives there, not here.
func findingRunRecord(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Finding   string `json:"finding"`
		Version   string `json:"version"`
		SHA       string `json:"sha"`
		Confirmed bool   `json:"confirmed"`
		Status    string `json:"status"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Finding) == "" {
		return nil, errors.New("finding is required: a run is of something")
	}

	e, err := m.db.RecordFindingRun(ctx, p, a.Finding, store.FindingRun{
		Version:   a.Version,
		SHA:       a.SHA,
		Confirmed: a.Confirmed,
		Status:    a.Status,
	})
	if err != nil {
		return nil, err
	}
	return withFixtureWarning(ctx, m, p, map[string]any{"event": e}), nil
}

// findingRunList is a thin wrapper over store.FindingRuns.
func findingRunList(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Finding string `json:"finding"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Finding) == "" {
		return nil, errors.New("finding is required")
	}

	runs, err := m.db.FindingRuns(ctx, p, a.Finding)
	if err != nil {
		return nil, err
	}
	return map[string]any{"finding": a.Finding, "count": len(runs), "runs": runs}, nil
}
