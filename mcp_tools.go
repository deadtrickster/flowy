package main

// The memory tools. A memory item is an artifact of type 'memory' with a kind,
// so it is stored, searched and - above all - permission-filtered by exactly
// the code Phase 1 already has. There is no second table and no second
// visibility rule: the personal floor that holds for a bug holds here, and the
// grant that opens a project opens the memories in it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// memoryType is the artifact type every one of these tools reads and writes.
const memoryType = "memory"

// The scopes, in the order they widen: personal is the floor no grant reaches
// through, project is everyone in the project and nobody else, shared is the
// project plus whoever holds a grant or a share.
var memScopes = []string{"personal", "project", "shared"}

// scopeVisibility is the visibility each scope is stored as.
//
// project is not stored as 'project'. That value has always meant "the project,
// and whoever the project's grants reach", because it is what an artifact
// written over the API gets by default and a cross-project grant has always
// reached those - so an item written here at scope=project was readable by
// exactly the people the scope said it was not, and an agent choosing the
// narrower of two scopes got the wider one. The store has a value that means
// what this scope says, and this is what writes it.
var scopeVisibility = map[string]string{
	"personal": store.VisibilityPersonal,
	"project":  store.VisibilityProjectOnly,
	"shared":   store.VisibilityShared,
}

// The kinds. todos looks at the last three.
var memKinds = []string{"note", "todo", "feature", "handoff"}

// workKinds are the kinds that describe outstanding work.
var workKinds = []string{"todo", "feature", "handoff"}

// tool is one MCP tool: what a client is told about it, and what runs.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	call        func(ctx context.Context, m *mcpServer, p *store.Principal, args json.RawMessage) (any, error)
}

// tools is the whole surface. Adding one here adds it to tools/list.
var tools = []tool{
	{
		Name: "mem_write",
		Description: "Write a memory item to shared memory, or update one by id. " +
			"Scope decides who can ever read it: personal (only you and your agents), " +
			"project (your project and nobody else, grants included), " +
			"shared (promoted across a grant). " +
			"Store durable facts, decisions and handoffs, not transcripts.",
		InputSchema: object(props{
			"title": str("One line, phrased as the claim being remembered."),
			"body":  str("The fact itself, and enough context for someone who was not here."),
			"scope": enum("Who may read it. Default personal.", memScopes),
			"kind":  enum("What the item is for. Default note.", memKinds),
			"tags":  strArray("Free-form subject labels; searched with the title and the body."),
			"status": str("Optional lifecycle status. Set \"done\" to take a todo off the " +
				"todo list."),
			"id": str("Update the item with this id instead of creating one."),
		}, nil),
		call: memWrite,
	},
	{
		Name: "mem_read",
		Description: "Read one memory item by id. An item you may not read is reported " +
			"exactly as an item that does not exist.",
		InputSchema: object(props{"id": str("The item's id.")}, []string{"id"}),
		call:        memRead,
	},
	{
		Name: "mem_search",
		Description: "Ranked full-text search over the memory you are allowed to read - " +
			"title, body and tags. Search here before asking a person.",
		InputSchema: object(props{
			"q":     str("What to look for. Plain words, not a query language."),
			"scope": enum("Narrow to one scope.", memScopes),
			"kind":  enum("Narrow to one kind.", memKinds),
			"limit": integer("Most results to return. Default 200."),
		}, []string{"q"}),
		call: memSearch,
	},
	{
		Name:        "mem_list",
		Description: "List memory items you may read, newest first.",
		InputSchema: object(props{
			"scope": enum("Narrow to one scope.", memScopes),
			"kind":  enum("Narrow to one kind.", memKinds),
			"limit": integer("Most items to return. Default 200."),
		}, nil),
		call: memList,
	},
	{
		Name: "todos",
		Description: "Outstanding work you may see: todo, feature and handoff items " +
			"whose status is not done.",
		InputSchema: object(props{"scope": enum("Narrow to one scope.", memScopes)}, nil),
		call:        todosTool,
	},
}

// toolSpecs is what tools/list answers; the handler funcs are not part of it.
func toolSpecs() []tool { return tools }

func toolByName(name string) (tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return tool{}, false
}

// ------------------------------------------------------------ input schemas

type props map[string]any

func object(p props, required []string) map[string]any {
	schema := map[string]any{"type": "object", "properties": map[string]any(p)}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func strArray(desc string) map[string]any {
	return map[string]any{
		"type": "array", "description": desc,
		"items": map[string]any{"type": "string"},
	}
}

func enum(desc string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

// ------------------------------------------------------------------- tools

type memWriteArgs struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Scope  string   `json:"scope"`
	Kind   string   `json:"kind"`
	Status string   `json:"status"`
	Tags   []string `json:"tags"`
}

// memWrite creates a memory item, or replaces one the principal owns.
//
// An id that names something the principal cannot read is refused rather than
// treated as a create with a caller-chosen id: an agent that guessed an id
// would otherwise overwrite a memory it was never allowed to see. Ids for new
// items are minted here, as ULIDs, which is the only way to get one.
func memWrite(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a memWriteArgs
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if p.UserID == "" {
		return nil, errors.New("this token resolves to no user, so it cannot own a memory item")
	}

	scope, err := oneOf("scope", a.Scope, memScopes, "personal")
	if err != nil {
		return nil, err
	}
	kind, err := oneOf("kind", a.Kind, memKinds, "note")
	if err != nil {
		return nil, err
	}
	// What the scope is stored as. An update that names no scope keeps the
	// item's own visibility instead - see below.
	visibility := visibilityOf(scope)

	art := &store.Artifact{
		ID:     a.ID,
		Type:   memoryType,
		Kind:   kind,
		Title:  strings.TrimSpace(a.Title),
		Body:   a.Body,
		Status: a.Status,
		Tags:   a.Tags,
	}

	if a.ID != "" {
		old, err := m.db.ReadArtifact(ctx, p, a.ID, false)
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("no such memory item: %s", a.ID)
		}
		if err != nil {
			return nil, err
		}
		if old.Type != memoryType {
			// Readable, owned, and not a memory item. These tools have one
			// namespace, and writing through them must not be a way to turn a
			// bug into a note - which is what it was: the update path rewrote
			// type as well as everything else, and the artifact left the
			// lifecycle it was in. Same answer as an id that is not there,
			// which is what mem_read gives for the same row.
			return nil, notThere(a.ID)
		}
		if old.OwnerUser != p.UserID {
			return nil, fmt.Errorf("memory item %s belongs to somebody else", a.ID)
		}
		// An update states what changes; the rest of the item stands.
		if art.Title == "" {
			art.Title = old.Title
		}
		if art.Body == "" {
			art.Body = old.Body
		}
		if a.Kind == "" {
			art.Kind = old.Kind
		}
		if a.Status == "" {
			art.Status = old.Status
		}
		if a.Tags == nil {
			art.Tags = old.Tags
		}
		if a.Scope == "" {
			// An update that says nothing about scope keeps the one the item
			// has, verbatim - including a visibility written before this
			// distinction existed, which is not something to migrate behind
			// somebody's back.
			visibility = old.Visibility
		}
		art.Discovery, art.Severity, art.Related = old.Discovery, old.Severity, old.Related
		art.FilePath, art.Fields = old.FilePath, old.Fields
	} else if art.Title == "" && strings.TrimSpace(art.Body) == "" {
		return nil, errors.New("a memory item needs a title or a body")
	}

	art.OwnerUser = p.UserID
	art.Visibility = visibility
	if visibility == store.VisibilityPersonal {
		// The floor is a property of the row: no project, so no grant can
		// reach it, whatever anyone writes into grants afterwards.
		art.Project = nil
	} else {
		// You write where you are. A principal's home project is the only
		// project it can put a row into - a memory written anywhere else would
		// not be readable by the agent that wrote it.
		if p.Project == "" {
			return nil, fmt.Errorf("this token has no project, so it can only write scope=personal, not %s",
				scopeOf(visibility))
		}
		home := p.Project
		art.Project = &home
	}

	if err := m.db.UpsertArtifact(ctx, art); err != nil {
		return nil, err
	}

	// The write is also a fact about the fabric: it goes in the log, so a peer
	// paging events sees that memory moved without diffing the table.
	actor := p.AgentID
	if actor == "" {
		actor = p.UserID
	}
	if err := m.db.AppendEvent(ctx, &store.Event{
		Type:     "memory.write",
		Project:  art.Project,
		Room:     "memory",
		Actor:    actor,
		Artifact: art.ID,
		Body:     art.Title,
	}); err != nil {
		return nil, err
	}

	return map[string]any{"item": art}, nil
}

func memRead(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
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
	if art.Type != memoryType {
		// It exists and is readable, but it is not memory. Same answer as an
		// id that is not there, so this tool has one namespace and no way to
		// enumerate another one.
		return nil, notThere(a.ID)
	}
	return map[string]any{"item": art}, nil
}

// notThere is the only thing an unreadable id ever gets back.
func notThere(id string) error { return fmt.Errorf("no such memory item: %s", id) }

func memSearch(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Q     string `json:"q"`
		Scope string `json:"scope"`
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Q) == "" {
		return nil, errors.New("q is required")
	}
	q, err := memQuery(a.Scope, a.Kind, a.Limit)
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

func memList(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope string `json:"scope"`
		Kind  string `json:"kind"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q, err := memQuery(a.Scope, a.Kind, a.Limit)
	if err != nil {
		return nil, err
	}

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(list), "items": list}, nil
}

func todosTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope string `json:"scope"`
		Limit int    `json:"limit"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q, err := memQuery(a.Scope, "", a.Limit)
	if err != nil {
		return nil, err
	}
	q.Kinds = workKinds
	q.NotStatus = "done"

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count": len(list), "items": list}, nil
}

// memQuery builds the store query every memory read shares: type 'memory',
// optionally narrowed by scope and kind. The permission filter is not in here
// because it is not optional - the store puts it in the WHERE clause itself.
func memQuery(scope, kind string, limit int) (store.ArtifactQuery, error) {
	q := store.ArtifactQuery{Type: memoryType, Limit: limit}
	if scope != "" {
		v, err := oneOf("scope", scope, memScopes, "")
		if err != nil {
			return q, err
		}
		q.Visibility = visibilityOf(v)
	}
	if kind != "" {
		k, err := oneOf("kind", kind, memKinds, "")
		if err != nil {
			return q, err
		}
		q.Kind = k
	}
	return q, nil
}

// visibilityOf is the visibility a scope is stored as. Anything that is not one
// of the scopes is passed through: an item written before this distinction
// existed carries a visibility that is not a scope, and reading it back is not
// the moment to change what it means.
func visibilityOf(scope string) string {
	if v, ok := scopeVisibility[scope]; ok {
		return v
	}
	return scope
}

// scopeOf names the scope a visibility is, for a message an agent reads.
func scopeOf(visibility string) string {
	for scope, v := range scopeVisibility {
		if v == visibility {
			return scope
		}
	}
	return visibility
}

// oneOf validates an enumerated argument, substituting fallback when it is
// absent. A misspelled scope is refused rather than defaulted: defaulting it
// would quietly write at the wrong visibility, which is the one mistake here
// that cannot be taken back.
func oneOf(name, got string, allowed []string, fallback string) (string, error) {
	if got == "" {
		return fallback, nil
	}
	for _, a := range allowed {
		if got == a {
			return got, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s, not %q", name, strings.Join(allowed, ", "), got)
}
