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
const memoryType = store.MemoryType

// The scopes, in the order they widen: personal is the floor no grant reaches
// through, project is everyone in the project and nobody else, shared is the
// project plus whoever holds a grant or a share.
//
// The list and the visibility each scope is stored as live in the store, beside
// the visibilities themselves - see store.MemScopes and VisibilityForScope. The
// FUSE mount takes a scope from a path and a line of front matter rather than
// from a tool argument, and it has to reach the same three columns these do.
var memScopes = store.MemScopes

// The kinds. todos looks at the last three.
var memKinds = []string{"note", "todo", "feature", "handoff"}

// workKinds are the kinds that describe outstanding work. The list is the
// store's, because the ready query narrows by it too: what is in the queue and
// what the queue orders have to be one answer.
var workKinds = store.WorkKinds

// isWorkKind reports whether a kind is one the queue holds - and therefore one
// that can be carried by somebody. It reads the list above rather than spelling
// the three words out again.
func isWorkKind(kind string) bool {
	for _, k := range workKinds {
		if kind == k {
			return true
		}
	}
	return false
}

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
			"room": str("The chat room this belongs to, e.g. general. It puts the item in " +
				"that room's panel and narrows nothing else: who may read it is unchanged. " +
				"Leave it out and the item is the project's, which is where every item " +
				"written before this field is."),
			"message": str("Id of the chat message that raised this - the conversation it " +
				"came out of, kept on the item. A message you cannot read is refused."),
			"assignee": str("Who is carrying this: a handle, as a claim about the work. " +
				"Send it empty to say nobody is. It hands the named party nothing - " +
				"who may read the item is unchanged - and leaving it out on an update " +
				"keeps whatever the item already said."),
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
			"whose status is not done. Narrow to one room to get that room's plan.",
		InputSchema: object(props{
			"scope": enum("Narrow to one scope.", memScopes),
			"room":  str("Only the items raised in this chat room."),
		}, nil),
		call: todosTool,
	},
	{
		Name: "guide",
		Description: "The full guide to this shared memory: scopes, kinds, tags, when " +
			"to store and when to recall. Read it before your first write - the " +
			"instructions your client was handed are the short form and may have " +
			"been truncated or dropped before they reached you.",
		InputSchema: object(props{}, nil),
		call:        guideTool,
	},
}

// toolSpecs is what tools/list answers; the handler funcs are not part of it.
//
// The observability tools are appended rather than written into the list above
// so that the memory surface and the watching surface stay two files: one is
// what an agent stores and recalls, the other is what it asks about the fabric
// itself.
func toolSpecs() []tool { return allTools() }

// allTools is every tool this server serves.
func allTools() []tool {
	out := make([]tool, 0, len(tools)+len(reportTools)+len(proposalTools)+len(depTools)+
		len(assignTools)+len(attachmentTools)+len(worklogTools)+len(projectTools)+
		len(observabilityTools))
	out = append(out, tools...)
	out = append(out, reportTools...)
	out = append(out, proposalTools...)
	out = append(out, depTools...)
	out = append(out, assignTools...)
	out = append(out, attachmentTools...)
	out = append(out, worklogTools...)
	out = append(out, projectTools...)
	out = append(out, observabilityTools...)
	return out
}

func toolByName(name string) (tool, bool) {
	for _, t := range allTools() {
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

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
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
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Scope   string   `json:"scope"`
	Kind    string   `json:"kind"`
	Status  string   `json:"status"`
	Tags    []string `json:"tags"`
	Room    string   `json:"room"`
	Message string   `json:"message"`
	// A pointer, because "" is a value here and not a silence: an update that
	// leaves the argument out keeps whoever is carrying the item, and one that
	// sends it empty says nobody is. Every other string on this struct means
	// "unstated" when it is empty, which is why this one cannot be a string.
	Assignee *string `json:"assignee"`
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
	// Where a non-personal item lives. A create writes where the token is; an
	// update keeps the home the item already had - see below.
	var home *string

	art := &store.Artifact{
		ID:     a.ID,
		Type:   memoryType,
		Kind:   kind,
		Title:  strings.TrimSpace(a.Title),
		Body:   a.Body,
		Status: a.Status,
		Tags:   a.Tags,
	}

	// Where the item belongs in the conversation rides fields, not columns, the
	// way as_of and supersedes ride a report - see mcp_reports.go. An update
	// that does not restate them keeps what the item already said.
	room, err := roomArg(a.Room)
	if err != nil {
		return nil, err
	}
	if err := readableMessage(ctx, m.db, p, a.Message); err != nil {
		return nil, err
	}
	assignee := ""
	if a.Assignee != nil {
		if assignee, err = store.NormalizeAssignee(*a.Assignee); err != nil {
			return nil, err
		}
	}
	var fields map[string]any

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
			// Readable, and not this principal's to change. It is a FORBIDDEN
			// refusal rather than an ordinary tool error, which is what makes it
			// arrive as an error rather than inside a success envelope - see
			// forbidden in mcp.go for what that cost. The sentence names the door
			// that does work, because "you may not edit this" is only half an
			// answer when the thing the caller was trying to do is allowed: an
			// item's words are its author's, and who is carrying it is not.
			return nil, refuseForbidden("memory item %s belongs to somebody else, so its "+
				"title, body, tags and status are not yours to change. Who is CARRYING it "+
				"is: use todo_assign (or POST /api/todo/%s/assignee) - any principal who "+
				"can read a todo may set or override its assignee", a.ID, a.ID)
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
		art.FilePath = old.FilePath
		if len(old.Fields) > 0 {
			if err := json.Unmarshal(old.Fields, &fields); err != nil {
				return nil, fmt.Errorf("memory item %s carries fields that do not parse: %w", a.ID, err)
			}
		}
		// Where the item lives is not something an update says. It used to be
		// rewritten to the token's own project every time, so an owner holding
		// tokens in two projects moved their own item out of one and into the
		// other by editing its title - silently, and past the rule POST
		// /api/artifacts and the merge both keep, which is that a principal
		// writes in its own project or not at all. An item that came from
		// somewhere else keeps that home and is refused below.
		home = old.Project
	} else if art.Title == "" && strings.TrimSpace(art.Body) == "" {
		return nil, errors.New("a memory item needs a title or a body")
	}

	fields = withRoom(fields, room, strings.TrimSpace(a.Message))
	// Written whenever it was stated, including empty: the key being there at
	// all is what says somebody decided, and what outranks a stale OWNER line
	// in the body. An update that says nothing about it keeps what is there.
	if a.Assignee != nil {
		if fields == nil {
			fields = map[string]any{}
		}
		fields[store.AssigneeField] = assignee
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
		if home == nil || *home == "" {
			// An item with no project is its owner's and nobody else's - the
			// read filter's first branch, whatever the visibility column says.
			// Giving it one hands the row to a project, and an update does not
			// do that: naming a scope on an edit used to be enough, so
			// {id, scope: "shared"} on a personal item moved it into the
			// caller's project as shared with nothing said about it - the same
			// silent widening POST /api/artifacts refuses in the same words.
			// An update that stays personal is fine, and a new item written at
			// a scope is what the scope is for.
			if a.ID != "" {
				return nil, fmt.Errorf("memory item %s has no project and is its owner's alone; "+
					"an update cannot move it into %s as %s - create it there instead",
					a.ID, p.Project, scopeOf(visibility))
			}
			// A create: it lands where the token writes.
			here := p.Project
			home = &here
		}
		if *home != p.Project {
			return nil, fmt.Errorf("memory item %s lives in project %s, and this token writes in %s",
				art.ID, *home, p.Project)
		}
		art.Project = home
	}

	// The write is also a fact about the fabric: it goes in the log, so a peer
	// paging events sees that memory moved without diffing the table. The item
	// and the entry are one write - two statements with a gap in the middle
	// meant a memory could end up with no record of having been written, and
	// nothing repairs that afterwards - so they go in together, under one
	// reading, the way a status move and its trail entry do.
	actor := p.AgentID
	if actor == "" {
		actor = p.UserID
	}
	events := []*store.Event{{
		Type:  "memory.write",
		Room:  "memory",
		Actor: actor,
		Body:  art.Title,
	}}
	// A write that says who is carrying a queue item leaves the assignment entry
	// too, in the same transaction. The author setting an assignee here and
	// somebody else setting one through todo_assign are the same claim, and a
	// value that sometimes has an entry behind it and sometimes does not is a log
	// that cannot answer the question it exists for - see store.AssignEntryEvent.
	// Only for the kinds the queue holds: a note is not work, and the assignment
	// log is the queue's.
	if a.Assignee != nil && isWorkKind(art.Kind) {
		entry, err := store.AssignEntryEvent(art, p, assignee)
		if err != nil {
			return nil, err
		}
		events = append(events, entry)
	}
	if err := m.db.WriteMemory(ctx, art, events...); err != nil {
		return nil, err
	}

	// The item, and - when this token writes into a fixture - the sentence
	// nobody was shown the day a week of real memory went into pa. See
	// mcp_projects.go.
	return withFixtureWarning(ctx, m, p, map[string]any{"item": art}), nil
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

// guideTool hands back the long form of the instructions.
//
// It reads no row and takes no argument, and it is a tool rather than only a
// resource because a tool is what an agent reaches for. The two ways this text
// gets lost on the way to a model are both silent - Claude Code truncates
// instructions at about 2 KB, and opencode drops a server's instructions
// entirely when all of its tools are disabled by permission - so the copy that
// matters is the one an agent can ask for on purpose.
func guideTool(_ context.Context, _ *mcpServer, _ *store.Principal, _ json.RawMessage) (any, error) {
	return map[string]any{"guide": guide}, nil
}

func todosTool(ctx context.Context, m *mcpServer, p *store.Principal, raw json.RawMessage) (any, error) {
	var a struct {
		Scope string `json:"scope"`
		Room  string `json:"room"`
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
	q.NotStatus = store.DoneStatus
	// The room narrows and does not widen: without it this is the whole queue,
	// items with a room and items without, exactly as it has always been.
	if q.Room, err = roomArg(a.Room); err != nil {
		return nil, err
	}

	list, err := m.db.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	// The same statement the console's queue makes, to the reader who acts on
	// this one. An agent draining the queue is exactly who must not be handed a
	// shorter list silently: it starts a run per ready item, so a row this node
	// refused for authorship and said nothing about is work that never happens
	// and nobody knows it was dropped. See store.WithheldAuthorship.
	withheld, err := m.db.WithheldAuthorship(ctx, p, false)
	if err != nil {
		return nil, err
	}
	refused, err := m.db.RefusedAuthorship(ctx, p, false)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"count": len(list), "items": list}
	if withheld != nil {
		out["withheld"] = withheld
	}
	// The claims this node refused for good. An agent draining the queue is the
	// reader who most needs the difference: a withheld row may turn up on the
	// next pull, and a refused claim will not turn up at all until somebody
	// signs for it. See store.RefusedAuthorship.
	if refused != nil {
		out["refused"] = refused
	}
	return out, nil
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

// visibilityOf is the visibility a scope is stored as, and scopeOf names the
// scope a visibility is, for a message an agent reads. Both are the store's
// mapping and not a second one.
func visibilityOf(scope string) string { return store.VisibilityForScope(scope) }

func scopeOf(visibility string) string { return store.ScopeForVisibility(visibility) }

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
