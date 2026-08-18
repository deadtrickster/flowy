package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
)

// DEPENDS-ON between todos, and the ready query that reads it.
//
// The queue is drained by machines: something reads the outstanding work, picks
// what can be started, and starts it. So the queue needs an order - what blocks
// what - and the order has to be safe to automate against, because a wrong
// answer here is not a wrong screen. It is a machine starting work whose
// dependency is not finished.
//
// Three decisions, and all three are load-bearing.
//
// AN EDGE IS AN EVENT, NOT A FIELD. dep.add and dep.remove name both todos, are
// signed, and append. A dependency is a relation with provenance - WHO said A
// blocks B, and WHEN - and a field write records THAT a write happened and not
// WHAT changed, so a column cannot answer it. It is the reasoning a vote is an
// event for, one step on: see the head of proposals.go. room and assignee stay
// fields, because each is a single-valued attribute of one todo and an edge has
// two endpoints.
//
// THE EDGE HANGS OFF THE DEPENDENT, AND THAT IS THE SAFETY PROPERTY. The event's
// artifact column is the BLOCKED todo, never the blocker. EventFilterSQL gives
// an event naming an artifact exactly that artifact's readers - the floor clause
// - so every principal who can read a todo reads every edge into it, whether or
// not they can read what is on the other end. Hanging the edge off the blocker
// instead would hide the edge from precisely the reader who needs it, and that
// reader would then see a todo with no blockers and call it ready. The whole of
// this file is downstream of getting that column the right way round.
//
// READY IS COMPUTED PER READER AND NEVER STORED. ready = every blocker done, AND
// somebody is carrying it - over the reader's own permission-filtered graph. A
// blocker that is UNKNOWN to the reader counts as not done, exactly like one
// that is known and unfinished: a reader who cannot see a blocker cannot confirm
// it is finished, so the node holds. The other version - ignore what you cannot
// see - reads as ready and is how a drainer spawns work whose dependency is not
// done. Two readers therefore disagree about the same todo, correctly, and a
// stored flag would be one answer that is wrong for somebody.
//
// ONE LIMIT, AND IT IS INHERITED RATHER THAN CHOSEN. Both entry types are minted,
// and a minted event does not cross a node boundary in either direction - see
// checkEventRow, which refuses one first and whichever way the row came. So the
// graph is this node's. A peer holding a replicated copy of a todo holds no
// edges into it and will call it ready, exactly as a peer holding a replicated
// proposal reads an empty tally. That is the existing shape of every claim this
// fabric mints, and the alternative is an edge a client can forge, which is
// worse - but it means a queue is drained against ONE node, and pointing a
// drainer at a peer of the node the edges were written on is not safe. Widening
// it is a change to what federation carries, not to this file.

// The two entries an edge leaves in the log. Both are minted - see
// mintedEventTypes - so the only way to get one is to have gone through the
// verb, which is where the refusals are.
const (
	EventDepAdd    = "dep.add"
	EventDepRemove = "dep.remove"
)

// BlockerField is the meta key holding the other end of the edge: the id of the
// todo that blocks the one in the artifact column.
//
// It is an id and NOTHING ELSE - not the blocker's title, not its status, not
// its project. This event is readable by everybody who can read the dependent,
// which by design includes principals who cannot read the blocker, so anything
// copied in here from the blocker's row is a leak across the boundary this
// surface exists to hold. The id is not: telling a reader "something you cannot
// see is in the way" is the point, and it is what makes the node hold.
//
// This is the one field the (project, type, id) ruling (01M08FK999F2JWY9RQV5VC821N)
// does NOT apply to, and on purpose: a Ref's whole point is carrying the
// project alongside the id, and the project is exactly what this key must
// never carry. Widening it to a Ref - here, in the meta, in the replicated
// event - would be the leak this comment already describes, just spelled a
// different way. It stays a bare id. A caller that already has permission to
// read the blocker (readWorkItem's return, for instance) can build a Ref from
// it with RefOf for its own use; that Ref must never be written back into this
// field or handed to a reader who has not independently earned the read.
const BlockerField = "blocker"

// DepRoom is where an edge's entry lands when the todo it is about names no room
// of its own, so that a dependency is somewhere a reader can find it rather than
// in the roomless part of the log.
const DepRoom = "deps"

// MemoryType is the artifact type a queue item is.
//
// It and WorkKinds below are here rather than beside the memory tools that write
// them, because the ready query narrows by both: what is in the queue and what
// the queue orders have to be one answer, and two lists of the same three words
// are two lists that disagree one day.
const MemoryType = "memory"

// WorkKinds are the kinds of item the queue holds.
//
// "merge" is here, and putting it here rather than in a queue of its own is the
// decision worth reading twice. A merge request has a status, waits on things,
// and is carried by somebody - so it is work, and everything this file already
// does about ordering work applies to it unchanged. The alternative is a second
// graph with a second ready query, which means two answers to what can start now
// and no way to tell which one is lying. See mergequeue.go, which adds one
// opinion to a merge item and no new machinery underneath it.
var WorkKinds = []string{"todo", "feature", "handoff", MergeKind, WorkKind, JoinKind}

// DoneStatus is the status that takes an item out of the queue, and - read from
// the other side - the only status that satisfies a dependency.
const DoneStatus = "done"

// maxDepWalk bounds the cycle check. A graph bigger than this is not a queue
// anybody is draining by hand, and a walk with no bound is a write path a
// pathological graph can hang. Reaching it refuses the write rather than
// guessing: the check exists to keep a cycle out, and a check that gave up
// quietly would be worse than not having one.
const maxDepWalk = 5000

// Blocker is one edge as a reader sees it: the todo on the other end, and what
// this reader can say about it.
//
// Known is false when the reader cannot read that todo at all - it may be in
// another project, personal to somebody else, or deleted. Done is then false as
// well, and it is false BECAUSE the reader cannot confirm otherwise rather than
// because anybody looked at a status. The pair is what makes the refusal
// legible: a drainer that skipped this todo can say which id it could not see.
type Blocker struct {
	ID    string `json:"id"`
	Known bool   `json:"known"`
	Done  bool   `json:"done"`
}

// Readiness is one queue item and what stands between it and being started.
//
// Blockers is every live edge, in the order the edges were first added, so that
// a queue which is not draining says why rather than sitting there. A cycle
// shows up here as todos that block each other and are none of them done, which
// is the shape of a queue that has stopped.
// Assignee is NOT omitempty. Nobody carrying this is the state that makes half
// of ready false, so it is a value a reader asked for and got - an absent key
// leaves a client to decide whether it means nobody or means the node did not
// say, which is the two-words-for-one-state problem nobodyWords exists to stop.
// Assignment is who put that name there and when, folded from the assignment log
// for this reader - see assign.go. It is absent on an item whose assignee was
// written before this surface existed, or whose entries came from another node,
// which is why it is a pointer beside the name rather than the name's home: the
// value is on the row and always readable, the provenance is a claim in the log
// and a reader may not have it.
type Readiness struct {
	Item       *Artifact   `json:"item"`
	Ready      bool        `json:"ready"`
	Assignee   string      `json:"assignee"`
	Assignment *Assignment `json:"assignment,omitempty"`
	Blockers   []Blocker   `json:"blockers"`
}

// DepEntry is one entry in the log behind the adjacency: the edge, who said it,
// and when. It is Vote's shape for the same reason - what makes the record worth
// keeping is that a removal does not erase the edge, it appends the fact that
// somebody took it back.
type DepEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Todo      string `json:"todo"`
	Blocker   string `json:"blocker"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// NotATodoError is what an id that does not name a queue item this principal may
// read gets back: one that is not here, one that is out of reach, and one that
// is here and is a report or a proposal are all the same answer, which is the
// answer a read of it would give. Naming an id in an edge is not a way to find
// out what else that id might be.
//
// It unwraps to ErrNotFound so every surface's existing 404 mapping catches it,
// and it names the end that was refused so a caller with two ids knows which of
// them it was.
type NotATodoError struct{ ID string }

func (e NotATodoError) Error() string { return "no such todo: " + e.ID }
func (e NotATodoError) Unwrap() error { return ErrNotFound }

// DepRefusal is what every refusal THE QUEUE VERBS make about the edge or the
// assignment they were asked for satisfies: the caller's mistake, and fixable by
// the caller, as opposed to something that went wrong underneath.
//
// It is an interface rather than a list of types kept beside each surface, so
// that a refusal added here cannot be one that HTTP maps to 400 and MCP reports
// as a broken node - or the other way about. NotATodoError is deliberately NOT
// one of these: an id out of reach is answered as an id that is not there, which
// is a different code and a different sentence everywhere in this fabric.
//
// The assignment verb's refusals satisfy it too - see assign.go. One interface
// rather than one per verb, so both doors keep mapping one list.
type DepRefusal interface {
	error
	depRefusal()
}

// depRefusalError is the plain form, for the refusals that are a sentence and
// nothing more.
type depRefusalError struct{ reason string }

func (e depRefusalError) Error() string { return e.reason }
func (e depRefusalError) depRefusal()   {}

// refuseDep builds one.
func refuseDep(format string, a ...any) error {
	return depRefusalError{reason: fmt.Sprintf(format, a...)}
}

// SelfDepError is a todo named on both ends. It is refused rather than stored
// and ignored: an edge from a thing to itself can never be satisfied, so it is a
// todo that is never ready with nothing on the row to say why.
type SelfDepError struct{ ID string }

func (e SelfDepError) depRefusal() {}

func (e SelfDepError) Error() string {
	return "todo " + e.ID + " cannot depend on itself: an edge to itself is never satisfied, " +
		"so it is a todo that never becomes ready and never says why"
}

// CycleError is an edge that would close a loop in the writer's own graph.
type CycleError struct {
	Todo    string
	Blocker string
	Path    []string
}

func (e CycleError) depRefusal() {}

// The path is spelled out with the relation rather than with an arrow, because
// an arrow between two ids in a dependency graph is read both ways by different
// people and this refusal is the only thing saying which way the loop goes.
func (e CycleError) Error() string {
	return "todo " + e.Todo + " already blocks " + e.Blocker + ": " +
		strings.Join(e.Path, " waits on ") + "; adding this edge would close a cycle, " +
		"and nothing in a cycle is ever ready"
}

// DepStateError is an add of an edge that is already there, or a removal of one
// that is not. Both are refused so that every entry in the log is a real
// transition - a log full of restatements is one a reader has to fold before it
// can say when something actually changed.
type DepStateError struct {
	Todo    string
	Blocker string
	Live    bool
}

func (e DepStateError) depRefusal() {}

func (e DepStateError) Error() string {
	if e.Live {
		return "todo " + e.Todo + " already depends on " + e.Blocker
	}
	return "todo " + e.Todo + " does not depend on " + e.Blocker
}

// AddDep records that todo depends on blocker: todo cannot be started until
// blocker is done.
func (d *DB) AddDep(ctx context.Context, p *Principal, todo, blocker string) (*Event, error) {
	return d.writeDep(ctx, p, EventDepAdd, todo, blocker)
}

// RemoveDep records that it no longer does. It does not delete the edge - there
// is nothing to delete, the edge was an entry - it appends the entry that takes
// it back, and both are in the log afterwards.
func (d *DB) RemoveDep(ctx context.Context, p *Principal, todo, blocker string) (*Event, error) {
	return d.writeDep(ctx, p, EventDepRemove, todo, blocker)
}

// writeDep is both verbs. The refusals, in the order they are asked:
//
//   - a todo named on both ends.
//   - either end unreadable to the writer, or not a queue item. An id is a guess
//     anybody can make, so an edge naming a todo its author could not see is
//     either a dangling pointer or an assertion about somebody else's work - the
//     rule readableMessage keeps for a raising message and UnreadableArtifacts
//     keeps for a worklog reference. The two ends being in DIFFERENT projects is
//     not refused and must not be: cross-project is what this is for, and the
//     writer legitimately sees both ends when a later reader sees one.
//   - a dependent with no project. The entry lands in the artifact's project, and
//     a projectless event is read back by its ACTOR rather than by the artifact's
//     readers - see EventFilterSQL's first branch. So an edge on a personal todo
//     written by somebody's agent would be invisible to the person who owns the
//     todo, and their ready query would see a todo with no blockers and call it
//     ready. That is the exact failure this file exists to rule out, so the write
//     is refused where the invariant cannot be kept.
//   - an add of a live edge, or a removal of one that is not live.
//   - an add that would close a cycle in the writer's graph. It is asked last
//     because it is the walk: an edge that is already there cannot close a loop
//     that is not already closed, so the cheap answer comes first.
func (d *DB) writeDep(ctx context.Context, p *Principal, verb, todo, blocker string) (*Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, verb)
	defer span.End()

	// The seat, by voteActor's rule: an agent is its own party here for the
	// reason it is its own voter. Who said A blocks B is the seat that said it,
	// not the person standing behind the seat.
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, refuseDep("this token resolves to nobody, so it cannot say what blocks what")
	}
	todo, blocker = strings.TrimSpace(todo), strings.TrimSpace(blocker)
	if todo == "" || blocker == "" {
		return nil, refuseDep("a dependency names two todos: the one that is blocked " +
			"and the one blocking it")
	}
	if todo == blocker {
		return nil, SelfDepError{ID: todo}
	}

	dependent, err := d.readWorkItem(ctx, p, todo)
	if err != nil {
		return nil, err
	}
	if _, err := d.readWorkItem(ctx, p, blocker); err != nil {
		return nil, err
	}
	if dependent.Project == nil || *dependent.Project == "" {
		return nil, refuseDep("todo %s has no project and is its owner's alone, so an edge "+
			"into it would be readable by whoever wrote the edge rather than by whoever can "+
			"read the todo - write it at scope=project or scope=shared first", todo)
	}

	live, err := d.liveDeps(ctx, p, []string{todo}, false)
	if err != nil {
		return nil, err
	}
	already := false
	for _, id := range live[todo] {
		if id == blocker {
			already = true
			break
		}
	}
	// An add of an edge that is live, or a removal of one that is not, changes
	// nothing and would go in the log as if it had.
	if already == (verb == EventDepAdd) {
		return nil, DepStateError{Todo: todo, Blocker: blocker, Live: already}
	}
	if verb == EventDepAdd {
		// Does the blocker already depend on this todo? Then the new edge closes
		// the loop. It is asked over the WRITER's graph and can only be: an edge
		// whose ends the writer cannot both see is an edge the writer cannot walk,
		// so a cycle assembled by two principals across a boundary is not caught
		// here. What catches that one is the ready query, where nothing in a cycle
		// is ever ready - and where the blockers come back on the row, so a queue
		// that has stopped says which ids stopped it.
		path, err := d.pathTo(ctx, p, blocker, todo)
		if err != nil {
			return nil, err
		}
		if path != nil {
			return nil, CycleError{Todo: todo, Blocker: blocker, Path: path}
		}
	}

	meta, err := json.Marshal(map[string]string{
		BlockerField: blocker,
		"actor_kind": actorKind, "actor_user": p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: %s %s: %w", verb, todo, err)
	}
	e := &Event{
		Type:    verb,
		Project: dependent.Project,
		Room:    depRoom(dependent),
		Thread:  dependent.ID,
		// The BLOCKED todo, never the blocker. See the head of this file: this
		// column is what decides who reads the edge, and the reader who has to
		// read it is the one holding the todo that is waiting.
		Artifact: dependent.ID,
		Actor:    actor,
		Body:     depBody(verb, blocker),
		Meta:     meta,
	}
	if err := d.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	span.SetArtifact(dependent.ID)
	return e, nil
}

// ReadWorkItem is a permission-filtered read of one queue item: a todo, a
// feature or a handoff this principal may read, and the answer a read of an id
// that is not here would give for anything else.
//
// It is exported for the surfaces that open one item and have to answer for an id
// that is not a queue item the same way every other queue verb does - GET
// /api/todo/{id}/assignee, which reads the assignment log of a todo and must not
// become a way to find out what else an id might be.
func (d *DB) ReadWorkItem(ctx context.Context, p *Principal, id string) (*Artifact, error) {
	return d.readWorkItem(ctx, p, id)
}

// readWorkItem reads one end of an edge: a queue item this principal may read,
// or the answer a read of an id that is not here would give.
//
// Both ends have to be queue items because DEPENDS-ON is an ordering ON THE
// QUEUE. An edge onto a report or a proposal is one the ready query would never
// read - nothing in the queue points at it - so it would be a dependency that
// silently does nothing, which is worse than a refusal.
func (d *DB) readWorkItem(ctx context.Context, p *Principal, id string) (*Artifact, error) {
	art, err := d.ReadArtifact(ctx, p, id, false)
	if errors.Is(err, ErrNotFound) {
		return nil, NotATodoError{ID: id}
	}
	if err != nil {
		return nil, err
	}
	if art.Type != MemoryType || !isWorkKind(art.Kind) {
		return nil, NotATodoError{ID: id}
	}
	return art, nil
}

func isWorkKind(kind string) bool {
	for _, k := range WorkKinds {
		if kind == k {
			return true
		}
	}
	return false
}

// DepLog is every entry naming this todo that p may read, oldest first - so a
// reader sees an edge being added and taken back rather than only the graph it
// ended up as. It is ProposalVotes for the other surface, with the same
// permission story: the filter is in the WHERE clause and it is not a second
// rule.
func (d *DB) DepLog(ctx context.Context, p *Principal, todo string) ([]DepEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "dep.log")
	defer span.End()

	events, err := d.depEvents(ctx, p, []string{todo}, false)
	if err != nil {
		return nil, err
	}
	out := make([]DepEntry, 0, len(events))
	for _, e := range events {
		out = append(out, DepEntryOf(e))
	}
	return out, nil
}

// DepEntryOf renders one event as the entry it is.
func DepEntryOf(e *Event) DepEntry {
	entry := DepEntry{
		ID: e.ID, Type: e.Type, Todo: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.Blocker = meta[BlockerField]
		entry.ActorKind, entry.ActorUser = meta["actor_kind"], meta["actor_user"]
	}
	return entry
}

// LiveDeps folds a todo's entries into the edges that are still live: the latest
// entry per blocker decides, and an edge whose latest entry is a removal is not
// one. First-added order, so the answer is stable and reads the way the log
// does.
//
// It is TallyOf's half of the design: the adjacency is a READING of the log
// rather than a stored number, so nothing has to be kept in step with anything,
// and a peer merging entries out of order converges on the same graph because
// the fold is over seq_hlc and not over arrival.
//
// entries must be in log order, which is what depEvents returns.
func LiveDeps(entries []DepEntry) []string {
	live := map[string]bool{}
	order := []string{}
	for _, entry := range entries {
		if entry.Blocker == "" {
			// An entry this build cannot read the other end of. It is not folded
			// in either direction: inventing an edge would block a todo on
			// nothing, and dropping it silently would be this build deciding a
			// row it does not understand says nothing.
			continue
		}
		if _, seen := live[entry.Blocker]; !seen {
			order = append(order, entry.Blocker)
		}
		live[entry.Blocker] = entry.Type == EventDepAdd
	}
	out := make([]string, 0, len(order))
	for _, id := range order {
		if live[id] {
			out = append(out, id)
		}
	}
	return out
}

// depEvents reads the dep entries naming any of todos, in log order, through the
// same event filter every other read of the log uses.
//
// There is no LIMIT on this, deliberately. The adjacency is a fold over the
// WHOLE log for each todo, and a page that stopped early would fold a prefix -
// an answer that is not the graph. A todo's dep log is a handful of entries; the
// queue is drained by machines and the machine wants the graph, not a page of it.
func (d *DB) depEvents(ctx context.Context, p *Principal, todos []string, scopeAll bool) ([]*Event, error) {
	if len(todos) == 0 {
		return nil, nil
	}
	return readPage(ctx, d, "dep events", func(a *args) string {
		idsArg := a.next(pq.Array(todos))
		typesArg := a.next(pq.Array([]string{EventDepAdd, EventDepRemove}))
		filter := EventFilterSQL(p, "e", a, scopeAll)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ANY(` + idsArg + `) AND e.type = ANY(` + typesArg + `)
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// liveDeps is the adjacency for a set of todos, as this reader sees it: one
// query for the whole set, folded per todo.
//
// A todo with no live edges is absent from the map rather than present and
// empty, which is what makes the ready query's loop the same shape for a todo
// with edges and one without.
func (d *DB) liveDeps(
	ctx context.Context, p *Principal, todos []string, scopeAll bool,
) (map[string][]string, error) {
	events, err := d.depEvents(ctx, p, todos, scopeAll)
	if err != nil {
		return nil, err
	}
	byTodo := map[string][]DepEntry{}
	for _, e := range events {
		entry := DepEntryOf(e)
		byTodo[entry.Todo] = append(byTodo[entry.Todo], entry)
	}
	out := make(map[string][]string, len(byTodo))
	for todo, entries := range byTodo {
		if live := LiveDeps(entries); len(live) > 0 {
			out[todo] = live
		}
	}
	return out, nil
}

// pathTo walks DEPENDS-ON forwards from `from`, looking for `target`. It returns
// the chain from `from` to `target` when there is one and nil when there is not,
// so a refusal can say which way round the loop already goes.
//
// Breadth-first over the reader's own graph, one query per level rather than one
// per node.
func (d *DB) pathTo(ctx context.Context, p *Principal, from, target string) ([]string, error) {
	seen := map[string]bool{from: true}
	came := map[string]string{}
	frontier := []string{from}

	for len(frontier) > 0 {
		if len(seen) > maxDepWalk {
			return nil, fmt.Errorf("store: the dependency graph reachable from %s is over %d "+
				"todos, which is more than this check will walk - the edge is refused rather "+
				"than added unchecked", from, maxDepWalk)
		}
		adjacency, err := d.liveDeps(ctx, p, frontier, false)
		if err != nil {
			return nil, err
		}
		next := []string{}
		for _, todo := range frontier {
			for _, blocker := range adjacency[todo] {
				if seen[blocker] {
					continue
				}
				seen[blocker] = true
				came[blocker] = todo
				if blocker == target {
					return chainTo(came, from, target), nil
				}
				next = append(next, blocker)
			}
		}
		frontier = next
	}
	return nil, nil
}

// chainTo reads the walk back out, from `from` to `target`, in that order.
//
// The bound is not decoration. Every node reached was recorded with the node
// that reached it, so this terminates - but it runs on a write path, and a walk
// that cannot terminate is a request that never answers. A path longer than the
// walk itself is impossible, so hitting the bound means the map was corrupted,
// and a short path in a refusal is better than a hung write.
func chainTo(came map[string]string, from, target string) []string {
	path := []string{target}
	for at := target; at != from && len(path) <= maxDepWalk; {
		at = came[at]
		path = append([]string{at}, path...)
	}
	return path
}

// Ready is the queue as one reader sees it: the outstanding work p may read,
// each item carrying whether it can be started and what is in the way.
//
// Every item comes back, ready or not, because a drainer that is told only "here
// are three ready todos" cannot tell a queue with nothing to do from a queue
// that has stopped. ReadyOnly narrows it for the caller that has already decided
// it does not care why.
//
// The whole thing is three queries: the items through ListArtifacts' filter, the
// edges through EventFilterSQL, the blockers' statuses through
// ArtifactFilterSQL. There is no second read path here - a permission rule
// written once for the queue and once for everything else is two rules.
func (d *DB) Ready(ctx context.Context, p *Principal, q ArtifactQuery) ([]*Readiness, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "todos.ready")
	defer span.End()

	q.Type, q.Kinds, q.Kind = MemoryType, WorkKinds, ""
	q.NotStatus = DoneStatus
	items, err := d.ListArtifacts(ctx, p, q)
	if err != nil {
		return nil, err
	}
	return d.readiness(ctx, p, items, q.ScopeAll)
}

// Readiness is the same answer for one todo, for the surface that opens it.
func (d *DB) Readiness(ctx context.Context, p *Principal, id string) (*Readiness, error) {
	art, err := d.readWorkItem(ctx, p, id)
	if err != nil {
		return nil, err
	}
	out, err := d.readiness(ctx, p, []*Artifact{art}, false)
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// readiness decides, for each item, whether it can be started - and this is the
// function the whole task is about.
//
// An item is ready when somebody is carrying it AND every live edge into it
// points at a todo this reader can read AND has read as done. The unknown
// blocker is the case that matters: it is counted as NOT done, because a reader
// who cannot see a blocker cannot confirm it is finished. A build that skipped
// the edges it could not resolve would answer "ready" here, and the machine
// reading that answer starts work whose dependency is not done.
//
// A blocker that has been deleted is unknown for the same reason and by the same
// query - a tombstoned row is not readable - so a todo does not become ready
// because the thing blocking it was thrown away.
func (d *DB) readiness(
	ctx context.Context, p *Principal, items []*Artifact, scopeAll bool,
) ([]*Readiness, error) {
	ids := make([]string, 0, len(items))
	for _, art := range items {
		if art != nil && art.ID != "" {
			ids = append(ids, art.ID)
		}
	}
	adjacency, err := d.liveDeps(ctx, p, ids, scopeAll)
	if err != nil {
		return nil, err
	}
	wanted := []string{}
	for _, blockers := range adjacency {
		wanted = append(wanted, blockers...)
	}
	status, err := d.statusOf(ctx, p, wanted, scopeAll)
	if err != nil {
		return nil, err
	}
	// Who handed each item to whoever has it. It is a fourth query over the same
	// events table and the same filter, and it is here rather than behind a
	// second call because the queue's first question after "what can I start" is
	// "and whose is this" - a drainer that has to ask again per row asks once per
	// row.
	claims, err := d.Assignments(ctx, p, ids, scopeAll)
	if err != nil {
		return nil, err
	}

	out := make([]*Readiness, 0, len(items))
	for _, art := range items {
		r := &Readiness{
			Item: art, Assignee: AssigneeOf(art),
			Assignment: claims[art.ID], Blockers: []Blocker{},
		}
		clear := true
		for _, id := range adjacency[art.ID] {
			at, known := status[id]
			done := known && at == DoneStatus
			r.Blockers = append(r.Blockers, Blocker{ID: id, Known: known, Done: done})
			if !done {
				clear = false
			}
		}
		// Assigned as well as unblocked. An unowned todo is work nobody has
		// picked up, and handing it to a drainer is how two agents build the same
		// thing - which is what put this file here.
		r.Ready = clear && r.Assignee != ""
		out = append(out, r)
	}
	return out, nil
}

// ReadyOnly keeps the items that can be started. It is a narrowing of an answer
// that was already computed and never a second definition of ready.
func ReadyOnly(rows []*Readiness) []*Readiness {
	out := make([]*Readiness, 0, len(rows))
	for _, r := range rows {
		if r.Ready {
			out = append(out, r)
		}
	}
	return out
}

// statusOf reads the status of each id this principal may read, in one query and
// through the same filter a direct read would use.
//
// An id that is not in the answer is one this reader cannot resolve, and the
// caller must treat that as "not done" rather than as "no such blocker". The map
// says nothing about why - out of reach, never here, deleted - and it must not,
// because those are the same answer everywhere else here.
func (d *DB) statusOf(
	ctx context.Context, p *Principal, ids []string, scopeAll bool,
) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	a := &args{}
	idsArg := a.next(pq.Array(ids))
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT ar.id, coalesce(ar.status, '') FROM artifacts ar
		  WHERE ar.id = ANY(`+idsArg+`) AND coalesce(ar.tombstone, false) = false AND `+filter,
		a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: read blocker status: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, fmt.Errorf("store: read blocker status: %w", err)
		}
		out[id] = status
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read blocker status: %w", err)
	}
	return out, nil
}

// depRoom is where an edge's entry lands in the log: the room the todo was
// raised in, or the deps room when it was raised in none. It is proposalRoom's
// rule, for the reason that one exists - an entry nobody can find is an entry
// nobody reads.
func depRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return DepRoom
}

// depBody is what the entry reads as on every surface that renders an event body
// and knows nothing about this one - the timeline, the console's activity view,
// the TUI.
//
// It names the blocker by ID and never by title, for the reason BlockerField
// holds an id: this entry reaches readers who cannot read the blocker.
func depBody(verb, blocker string) string {
	if verb == EventDepRemove {
		return "no longer depends on " + blocker
	}
	return "depends on " + blocker
}
