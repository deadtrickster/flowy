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
	"time"

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

// The kinds. todos looks at every one of them except "note".
var memKinds = []string{"note", "todo", "feature", "handoff", store.MergeKind}

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
			"status": str("Where a queue item is: todo, active or done. Set \"done\" to take " +
				"a todo off the todo list, and \"todo\" to reopen one that was not finished " +
				"after all. Any principal who can READ a todo may move it - saying work is " +
				"done is a claim about the WORK, not about the text - so on somebody else's " +
				"item send {id, status} and nothing else."),
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
			"category": enumOrEmpty("What KIND of work a queue item is, out of a closed "+
				"set - anything else is refused. It is what the queue is counted and routed "+
				"by; use tags for every other label, they are free-form and unlimited. "+
				"Empty means unclassified, which is legal and is what most items are. "+
				"Leaving it out on an update keeps what the item is filed as.",
				store.TodoCategories),
			"branch": str("MERGE REQUESTS ONLY. The branch this would land. A merge " +
				"request that does not say one is refused, and these four are refused " +
				"on any other kind - nothing would read them there."),
			"target": str("MERGE REQUESTS ONLY. What it lands on. Default master."),
			"gated_tip": str("MERGE REQUESTS ONLY. The commit THE GATE ACTUALLY " +
				"MEASURED - not what the branch was cut from, not what it hopes to " +
				"land on. It is the one thing that decides whether this may land: if " +
				"the target has moved since, the merge is refused and the branch is " +
				"re-gated. Filing it without one is normal; landing without one is not."),
			"gate_run": str("MERGE REQUESTS ONLY. The run that produced the verdict, so " +
				"a claim of green points at the log that says so rather than at " +
				"somebody's memory of it."),
			"id": str("Update the item with this id instead of creating one. An item " +
				"somebody else wrote takes status, assignee and category - the queue " +
				"metadata anybody who can read it may move - and refuses everything else."),
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
		Description: "Outstanding work you may see: todo, feature, handoff and merge " +
			"items whose status is not done. Narrow to one room to get that room's " +
			"plan, or to one kind to get the merge queue. Narrow to `assignee` to get " +
			"what one party is carrying - and when that comes back empty, the answer " +
			"carries a `rebalance` block: what the rest of the board is carrying, who " +
			"has it, and whether that party is still listening. An agent with nothing " +
			"to do beside an agent with nine rows is a queue that has stopped " +
			"balancing itself, so this endpoint offers rather than waiting to be asked.",
		InputSchema: object(props{
			"scope": enum("Narrow to one scope.", memScopes),
			"room":  str("Only the items raised in this chat room."),
			"assignee": str("Only the items this party is carrying - a handle. The " +
				"empty string means the items NOBODY is carrying, which is a different " +
				"question from leaving this out and is worth asking first: unowned work " +
				"needs no negotiation. Leave it out for the whole board."),
			"category": enum("Only the items filed as this kind of work. This is what "+
				"the closed set is for: ask for the bugs and get the bugs.",
				store.TodoCategories),
			"kind": enum("Only items of this kind. \"merge\" is the merge queue - a "+
				"merge request is work like anything else and waits on the same "+
				"dependency edges, so this is one queue with two views rather than "+
				"two queues. Leave it out for the whole board.", store.WorkKinds),
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
		len(mergeTools)+len(assignTools)+len(stealTools)+len(chatTools)+len(categoryTools)+
		len(attachmentTools)+len(worklogTools)+len(projectTools)+len(observabilityTools))
	out = append(out, tools...)
	out = append(out, reportTools...)
	out = append(out, proposalTools...)
	out = append(out, depTools...)
	out = append(out, mergeTools...)
	out = append(out, assignTools...)
	out = append(out, stealTools...)
	out = append(out, chatTools...)
	out = append(out, categoryTools...)
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
	// A pointer for the same reason, one field along: "" is unclassified, which
	// is a value somebody may choose and is what the whole queue already is, so
	// an update that leaves the argument out has to keep the item filed as
	// whatever it is filed as.
	Category *string `json:"category"`
	// What a MERGE request is about: the branch, and the verdict that measured
	// it. Plain strings rather than pointers, because unlike assignee and
	// category there is no meaningful "set it back to empty" here - a merge
	// request with no branch is not a merge request, and a gate verdict is
	// something that happened rather than something anybody clears.
	//
	// GatedTip is the tip THE GATE MEASURED, and it is the only one of these
	// that decides anything: see store.MergeAdmissible.
	Branch   string `json:"branch"`
	Target   string `json:"target"`
	GatedTip string `json:"gated_tip"`
	GateRun  string `json:"gate_run"`
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
	// The closed set is checked wherever the word arrives, exactly as the status
	// vocabulary is: a category only this one door understands is a row nothing
	// downstream can count, which is the one thing this field exists to prevent.
	// See store.NormalizeTodoCategory.
	category := ""
	if a.Category != nil {
		if category, err = store.NormalizeTodoCategory(*a.Category); err != nil {
			return nil, err
		}
	}
	var fields map[string]any
	// Where the item was before this write, which is what the status entry names
	// as the end it came from. Empty on a create, and empty for a row that never
	// had a status at all.
	var was string
	// And what it was filed as, for the entry the classification leaves. Empty on
	// a create and on every row raised before this field, which is most of them.
	var wasCategory string

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
			// Readable, and not this principal's to REWRITE - which is not the
			// same as not theirs to move. An item's words are its author's; the
			// queue metadata on it is the room's, so this write goes down the
			// path that changes that and nothing else. See memWriteQueueOnly,
			// which refuses everything else loudly.
			return memWriteQueueOnly(ctx, m, p, old, a)
		}
		was = old.Status
		// And what it was filed as, which is the end the classification entry
		// names as where it came from.
		wasCategory = store.CategoryOf(old)
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

	// A queue item's status is a lifecycle state and not free text, wherever it
	// arrives: one vocabulary, checked in one place, so that a todo written here
	// and a todo closed through POST /api/artifact/{id}/status cannot end up in
	// two states no reader can compare. See store.NormalizeTodoStatus. A note's
	// status is still whatever its author typed - a note is not in a lifecycle.
	if a.Status != "" && isWorkKind(art.Kind) {
		if art.Status, err = store.NormalizeTodoStatus(a.Status); err != nil {
			return nil, err
		}
	}
	// And a work item raised here with NO status at all starts at the beginning
	// of the lifecycle, exactly as one raised through POST /api/chat/{room}/todo
	// does - see todos.go, which has defaulted it since the door was written.
	//
	// This one did not, so every todo filed through MCP landed with status "",
	// which is not a state any reader can compare: it is not todo, so a board
	// filtering for outstanding work skips it, and it is not done either. Five
	// items sat on the operator's board that way tonight, and the complaint they
	// produced - "why do I still see items that are not moving" - was read three
	// times as agents forgetting to set a status. Nobody forgot. THE DOOR NEVER
	// SET ONE.
	//
	// Create only. An update that restates nothing keeps what the row has,
	// including empty, because healing a stale status silently on an unrelated
	// edit moves other people's work behind their backs.
	if art.Status == "" && a.ID == "" && isWorkKind(art.Kind) {
		art.Status = store.TodoStatus
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
	// Same rule for the category: written when it was stated, including empty,
	// and an update that says nothing about it keeps what the item is filed as.
	if a.Category != nil {
		if fields == nil {
			fields = map[string]any{}
		}
		fields[store.CategoryField] = category
	}
	// What a merge request is about. These four go through the same door as
	// everything else and are refused on anything that is not a merge item,
	// rather than stored quietly on a todo where nothing will ever read them: a
	// field that lands somewhere it is never consulted is indistinguishable from
	// a field that was ignored, and today has enough of those.
	if err := mergeFields(art, &fields, a); err != nil {
		return nil, err
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
	// And a write that says WHERE a queue item is leaves the status entry, in the
	// same transaction and for the same reason: the author closing their own todo
	// here and somebody else closing it through the status route are the same
	// claim, and a status that sometimes has an entry behind it is a log that
	// cannot answer who finished the work. See store.TodoStatusEntryEvent.
	//
	// Only for the kinds the queue holds. A note's status is a word on a row that
	// nothing acts on - it is not in a lifecycle, and giving it a trail would be
	// inventing one here rather than in the store where it belongs.
	if a.Status != "" && isWorkKind(art.Kind) {
		// Where it came from. Empty on a create - there was no previous state,
		// and the entry says so rather than inventing one - and the word the
		// queue reads a blank column as on an update of a row that never had a
		// status set.
		from := was
		if a.ID != "" && from == "" {
			from = store.TodoStatus
		}
		entry, err := store.TodoStatusEntryEvent(art, p, from, art.Status)
		if err != nil {
			return nil, err
		}
		events = append(events, entry)
	}
	// And a write that says WHAT KIND of work a queue item is leaves the
	// classification entry, in the same transaction and for the third time for
	// the same reason: the author filing their own todo as a bug here and
	// somebody else reclassifying it through todo_category are the same claim,
	// and a value that sometimes has an entry behind it is a log that cannot
	// answer who called this a bug. See store.TodoCategoryEntryEvent.
	//
	// Only for the kinds the queue holds. A note is not work: filing one as a
	// chore would be inventing an ontology for a thing that is not in the queue.
	if a.Category != nil && isWorkKind(art.Kind) {
		entry, err := store.TodoCategoryEntryEvent(art, p, wasCategory, category)
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

// memWriteQueueOnly is mem_write on an item this principal did not write.
//
// THE QUEUE METADATA CHANGES HANDS AND THE WORDS DO NOT. A todo is not the
// property of whoever typed it: the room drains the queue, so who is carrying an
// item and whether it is finished are claims about the WORK, and the principal
// in a position to make them is whoever did the work rather than whoever raised
// the row. That is the ruling behind todo_assign, and closing is the other half
// of it - the half that was missing, which left the agent that had built and
// deployed a thing unable to say so on either door because somebody else had
// raised the line. Read permission is the whole bar, and both verbs ask it
// again themselves.
//
// TITLE, BODY, TAGS, KIND, SCOPE AND WHERE IT WAS RAISED ARE THE AUTHOR'S. They
// are somebody's words about their own work, and a stranger saying "this is
// done" is not a stranger rewriting what you wrote. Stating one of them here is
// refused rather than dropped: a write that silently kept the old title would be
// a success envelope that changed something other than what it was asked to,
// which is the same lie as one that changed nothing.
//
// EVERY REFUSAL IS A PROTOCOL ERROR. It is a forbidden refusal, so it arrives as
// an error with a code rather than as a result with a flag - see forbidden in
// mcp.go, and the three hours that distinction cost once.
//
// The two verbs are two writes, and each leaves its own entry. They are not one
// transaction because they are not one claim: "b-drainer has it" and "it is
// done" are separate facts with separate records, and either one landing without
// the other is a true statement about the queue rather than half of a broken
// one.
func memWriteQueueOnly(
	ctx context.Context, m *mcpServer, p *store.Principal, old *store.Artifact, a memWriteArgs,
) (any, error) {
	var stated []string
	for _, field := range []struct {
		name  string
		given bool
	}{
		{"title", strings.TrimSpace(a.Title) != ""},
		{"body", a.Body != ""},
		{"tags", a.Tags != nil},
		{"kind", a.Kind != ""},
		{"scope", a.Scope != ""},
		{"room", a.Room != ""},
		{"message", a.Message != ""},
	} {
		if field.given {
			stated = append(stated, field.name)
		}
	}
	if len(stated) > 0 {
		// The sentence names the doors that DO work, because half of what went
		// wrong here was that the thing being attempted is allowed: an item's
		// words are its author's, and where the work has got to is not.
		return nil, refuseForbidden("memory item %s belongs to somebody else, so its %s "+
			"%s not yours to change: an item's words are its author's. The queue metadata "+
			"on it is not - mem_write {id, status}, {id, assignee} and {id, category}, "+
			"todo_assign, todo_category, and POST /api/artifact/%s/status all work for any "+
			"principal who can READ the todo. "+
			"This write stated more than that, so none of it was made",
			a.ID, strings.Join(stated, ", "), plural(len(stated), "is", "are"), a.ID)
	}
	if !isWorkKind(old.Kind) {
		return nil, refuseForbidden("memory item %s belongs to somebody else and is a %s, "+
			"which the queue does not hold: nothing on it changes hands. A todo, a feature "+
			"or a handoff carries a status and an assignee that anybody who can read it may "+
			"move", a.ID, kindOrType(old))
	}
	if a.Assignee == nil && a.Status == "" && a.Category == nil {
		return nil, refuseForbidden("memory item %s belongs to somebody else, so this write "+
			"has to say which piece of the queue metadata it is moving: status, assignee, "+
			"category, or any of them. Its title and body are its author's", a.ID)
	}

	art := old
	if a.Category != nil {
		// First, because it is the one most likely to be refused: a write that
		// names a category outside the vocabulary should not have moved the
		// assignee on its way to finding that out.
		filed, _, err := m.db.SetTodoCategory(ctx, p, a.ID, *a.Category)
		if err != nil {
			return nil, err
		}
		art = filed
	}
	if a.Assignee != nil {
		moved, _, err := m.db.AssignTodo(ctx, p, a.ID, *a.Assignee, nil)
		if err != nil {
			return nil, err
		}
		art = moved
	}
	if a.Status != "" {
		moved, _, err := m.db.SetTodoStatus(ctx, p, a.ID, a.Status)
		if err != nil {
			return nil, err
		}
		// The status verb reads the row again through the same filter before it
		// writes, so what it hands back already carries the assignment above it:
		// a caller that moved two things reads both of them back.
		art = moved
	}
	return withFixtureWarning(ctx, m, p, map[string]any{"item": art}), nil
}

// kindOrType names what an item is, for a refusal that has to say why the queue
// does not hold it.
func kindOrType(art *store.Artifact) string {
	if art.Kind != "" {
		return art.Kind
	}
	return art.Type
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
		return nil, m.notThereOrWithdrawn(ctx, p, a.ID)
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

// notThereOrWithdrawn is notThere, except for the one case a reader is entitled
// to hear about: an id THIS principal could have read, which was taken back.
//
// It is the MCP door's half of what GET /api/artifact/{id} answers with 410, and
// it is here because this is the door an agent actually knocks on. Twenty
// minutes went into ids that came back "no such memory item" - the cause was
// visibility=personal, and the reply could not say so.
//
// It still cannot say so, and must not: exists-but-not-for-you stays word for
// word identical to never-existed, because an id is guessable and a reply that
// distinguished them would let anybody enumerate what a project holds. The only
// thing that changes the answer is ReadWithdrawn's permission filter, which is
// the same filter the read that just failed used.
func (m *mcpServer) notThereOrWithdrawn(ctx context.Context, p *store.Principal, id string) error {
	wd, err := m.db.ReadWithdrawn(ctx, p, id, false)
	if err != nil || wd == nil {
		return notThere(id)
	}
	who := wd.Actor
	if who == "" {
		who = "somebody"
	}
	return fmt.Errorf("memory item %s was withdrawn by %s at %s - it is not there to read, "+
		"and it is not coming back by being written to",
		id, who, wd.At.UTC().Format(time.RFC3339))
}

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
		Scope    string `json:"scope"`
		Room     string `json:"room"`
		Category string `json:"category"`
		Kind     string `json:"kind"`
		Limit    int    `json:"limit"`
		// A POINTER, because "" is a question: the items nobody is carrying.
		// Absent and empty are two different asks and a plain string could only
		// carry one of them - the nobodyWords problem in argument form.
		Assignee *string `json:"assignee"`
	}
	if err := decodeParams(raw, &a); err != nil {
		return nil, err
	}
	q, err := memQuery(a.Scope, "", a.Limit)
	if err != nil {
		return nil, err
	}
	q.Kinds = workKinds
	// One queue, two views. The merge queue is not a separate list - a merge
	// request is work, ordered by the same dependency edges as everything else -
	// so the split the console needs is a NARROWING of this read rather than a
	// second one. kind: "merge" is the merge tab; kind left out is the whole
	// board, which is what a flattened view is.
	//
	// A kind outside the vocabulary is refused rather than answered with an empty
	// list, because "there is no such kind" and "there is no work of that kind"
	// are different facts and today cost hours of confusing them.
	if a.Kind != "" {
		if !isWorkKind(a.Kind) {
			return nil, fmt.Errorf("%q is not a kind of work this queue holds: one of %s",
				a.Kind, strings.Join(workKinds, ", "))
		}
		q.Kinds = []string{a.Kind}
	}
	q.NotStatus = store.DoneStatus
	// The room narrows and does not widen: without it this is the whole queue,
	// items with a room and items without, exactly as it has always been.
	if q.Room, err = roomArg(a.Room); err != nil {
		return nil, err
	}
	// And so does the category, through the same door every write of one goes
	// through: asking for a kind of work that is not in the vocabulary is a
	// refusal that names the vocabulary, rather than an empty list that reads
	// like "there are no bugs".
	if q.Category, err = store.NormalizeTodoCategory(a.Category); err != nil {
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
	// WHOSE WORK, filtered here rather than in the WHERE clause.
	//
	// The assignee a reader sees is AssigneeOf's answer, and AssigneeOf falls back
	// to an `OWNER:` line in the body for every row written before the field
	// existed. A SQL narrow over fields->>'assignee' would silently miss those,
	// which is the failure this codebase keeps meeting from the other side: a true
	// number about the wrong population. So the board is read as it always was and
	// narrowed by the one function that knows what carrying something means.
	//
	// The cost is that the page limit applies BEFORE the narrowing, so a narrowed
	// answer can be short because the page was full rather than because that is
	// all of them. It is reported rather than hidden - `truncated` - because a
	// short list means "that was all of them" everywhere else here.
	board := list
	truncated := false
	if a.Assignee != nil {
		want, err := store.NormalizeAssignee(*a.Assignee)
		if err != nil {
			return nil, err
		}
		truncated = len(board) >= q.PageLimit()
		mine := make([]*store.Artifact, 0, len(board))
		for _, art := range board {
			if store.AssigneeOf(art) == want {
				mine = append(mine, art)
			}
		}
		list = mine
	}
	out := map[string]any{"count": len(list), "items": list}
	if truncated {
		out["truncated"] = true
		out["truncated_note"] = "the page filled before this was narrowed, so there may " +
			"be more of this party's items past it - ask again with a larger limit"
	}
	// NOTHING TO DO IS A QUESTION, NOT AN ANSWER. An agent that asks for its work
	// and is handed an empty list has no next move, so the empty list is where the
	// offer belongs: here is what everybody else is carrying, here is who has not
	// been heard from, and here is the verb that asks them for it. See
	// internal/store/steal.go - the offer is made here and every rule is there.
	if a.Assignee != nil && len(list) == 0 {
		if offer := rebalanceOffer(ctx, m, p, board, *a.Assignee); offer != nil {
			out["rebalance"] = offer
		}
	}
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
