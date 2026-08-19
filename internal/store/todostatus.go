package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
)

// WHERE A TODO IS, and who said so.
//
// A todo is the one item in this fabric whose entire purpose is to be FINISHED,
// and until this file it was the one item that could not be. The lifecycle in
// lifecycle.go is the issue workflow - open, triaged, in-progress, in-review,
// done - and it is a bug's, so POST /api/artifact/{id}/status answered a todo
// with "a memory has no lifecycle; bug, feature, note and task do". The other
// door, mem_write, took a status and refused everybody except the author. So the
// agent that had DONE the work could not say so on either door when somebody
// else had raised the row, and shipped work went on advertising itself as open -
// which is how one queue produced five duplicated builds in a day.
//
// THE MODELLING CHOICE: MEMORY GAINS A LIFECYCLE FOR THE KINDS THE QUEUE HOLDS,
// and a todo stays a memory item. The alternative - a type of its own - was
// looked at and is a rewrite of everything that already works: the permission
// filter, the memory tools' namespace, the FUSE mount, the ready query, the
// panels, and every row written since todos existed. And it would buy nothing,
// because what a todo was missing was never a type. It was a VERB. The status
// column already holds this vocabulary on those rows and every surface already
// reads it - see DoneStatus, which is what satisfies a dependency, and the three
// words the console and the TUI paint - so the lifecycle here is the one the
// queue has always had in fact, with a verb behind it and a record under it.
//
// THE VOCABULARY IS THE QUEUE'S, NOT THE ISSUE WORKFLOW'S. todo, active, done,
// and any of the three from any of the others. It is deliberately not a line the
// way statusFlow is: a queue item is picked up, put down and picked up again,
// and REOPENING is the case the room asked for by name - work that was called
// done and turned out not to be. A workflow that could not walk backwards would
// leave that as a new todo, and the trail of what actually happened to the first
// one would end at a lie.
//
// A restatement is accepted rather than refused, which is AssignTodo's rule and
// not writeDep's. Saying a todo is still active is somebody saying it still
// stands - the queue door has always taken it - and the fold is latest-wins, so
// a restatement costs a reader nothing. An edge is different because an edge is
// a set membership, and adding what is already in a set changes nothing anybody
// could have meant.
//
// READ PERMISSION IS THE BAR, AND THERE IS NO SECOND ONE. Whoever can read a
// todo may move its status, exactly as whoever can read it may say who is
// carrying it. STATUS IS A CLAIM ABOUT THE WORK AND NOT ABOUT THE TEXT: it is
// somebody saying the thing is finished, and the person in a position to say
// that is whoever did it, not whoever typed the row. It hands the mover nothing
// - the permission filter has never looked at the status column - so the widest
// this reaches is "whoever can see the work can say where the work is", and a
// principal who cannot see it gets the answer a read of it would give. There is
// no new grant and no new column, for the reason there is none behind an
// assignee: a permission layer over something that grants nothing is a layer to
// maintain and nothing to protect.
//
// TITLE AND BODY STAY THE AUTHOR'S. Only the queue metadata changes hands - who
// is carrying it, and where it is. A stranger may say "this is done"; they may
// not rewrite what you wrote, and mem_write refuses that loudly - see
// memWriteQueueOnly, which is the other half of this change.
//
// A MOVE IS AN EVENT, and the value on the row is the head of it. That is
// AssignTodo's shape, one field along, and for AssignTodo's reason: a column
// records THAT something changed, and the question a queue actually asks is WHO
// closed this and WHEN. The entry names the todo, the status it went to and the
// one it came from, is signed, and appends - so a todo closed, reopened and
// closed again says so three times. The value ALSO lands on the row, in the same
// transaction and under one clock reading, because every surface in this fabric
// reads the status off the row: the ready query, the panels, the FUSE mount, the
// TUI. One verb writes both or neither, so the fold and the row cannot disagree.
//
// THE ENTRY HANGS OFF THE TODO, which is what makes it readable by the people it
// is about - EventFilterSQL gives an event naming an artifact exactly that
// artifact's readers. A projectless todo is the inherited exception, as it is
// for an assignment: the entry is read back by its ACTOR, so a personal todo's
// trail is provenance its own author can read and their agent cannot. It is not
// refused, because the VALUE is on the row either way and a personal todo has
// one reader-set anyway.
//
// ONE LIMIT, INHERITED. The entry is minted - see mintedEventTypes - so it does
// not cross a node boundary in either direction, exactly as a dep edge, a vote
// and an assignment do not. A peer holds the status (it is on the row, and rows
// replicate) and none of the entries behind it, so "who closed this" is a
// question answered on the node it was closed on.

// EventTodoStatus is what a queue move is in the log. It is minted, so the only
// way to get one is to have gone through the verb, which is where the refusal is.
const EventTodoStatus = "todo.status"

// StatusRoom is where an entry lands when the todo it is about names no room of
// its own. It is AssignRoom's rule for the other half of the queue metadata: an
// entry nobody can find is an entry nobody reads.
const StatusRoom = "status"

// The queue's statuses. DoneStatus is in deps.go and stays there: it is what
// satisfies a dependency, which is the other half of what done MEANS here, and
// two spellings of one word would be two ideas of when work is finished.
const (
	// TodoStatus is raised and not started. It is what an unstated status reads
	// as, so a row written before anything had a status is outstanding work
	// rather than work in an unknown state.
	TodoStatus = "todo"
	// ActiveStatus is somebody is on it.
	ActiveStatus = "active"
)

// QueueStatuses is the whole vocabulary, in the order a queue is read in:
// what is in flight, what is waiting, what is finished. The order is the one the
// panels sort by, so an error message listing them reads the way the screen does.
var QueueStatuses = []string{ActiveStatus, TodoStatus, DoneStatus}

// StatusEntry is one entry in the log behind a move: the todo, where it went,
// where it came from, who said so and when.
//
// It is AssignEntry's shape for AssignEntry's reason - what makes the record
// worth keeping is that a reopen does not erase the closure before it, it
// appends the fact that somebody said the work was not done after all.
type StatusEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Todo string `json:"todo"`
	// Status is NOT omitempty, and neither is From: an entry that left one of
	// them out would leave a client deciding whether it means "nowhere" or means
	// the node did not say, which is the two-words-for-one-state problem
	// nobodyWords exists to stop.
	Status string `json:"status"`
	// From is where it was, and is empty for a row that had no status at all -
	// the first move of an item written before this field was set.
	From      string `json:"from"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// TodoState is the fold of that log: where the todo is, and the entry that put
// it there.
//
// Latest wins, over seq_hlc, so two seats moving the same todo converge on one
// answer whichever order the entries were read in. It is Assignment's half of
// the design - a READING of the log rather than a second stored copy of it.
type TodoState struct {
	Status string `json:"status"`
	From   string `json:"from"`
	// By is the seat that made the move, and ByUser the person behind it. Both
	// are on the answer because "the agent that built it closed it" and "the
	// operator closed it" are the two things a reader is telling apart.
	By     string `json:"by"`
	ByKind string `json:"by_kind,omitempty"`
	ByUser string `json:"by_user,omitempty"`
	At     string `json:"at"`
	Entry  string `json:"entry"`
}

// statusRefusalError is what every refusal this verb makes ABOUT THE MOVE IT WAS
// ASKED FOR satisfies: the caller's mistake, and fixable by the caller.
//
// It is DepRefusal's interface rather than a third one, so that a refusal added
// to any queue verb cannot be one that HTTP maps to 400 and MCP reports as a
// broken node. NotATodoError is deliberately not one of them here either: an id
// out of reach is answered as an id that is not there.
type statusRefusalError struct{ reason string }

func (e statusRefusalError) Error() string { return e.reason }
func (e statusRefusalError) depRefusal()   {}

func refuseStatus(format string, a ...any) error {
	return statusRefusalError{reason: fmt.Sprintf(format, a...)}
}

// NormalizeTodoStatus validates the status a write asks for and returns it as
// the node stores it.
//
// Case and surrounding space are the caller's typing rather than a different
// state, so they are taken off: a queue holding "Done" and "done" is a queue
// where half the dependencies are satisfied and the panels disagree about why.
// Anything outside the vocabulary is REFUSED rather than stored - the whole
// point of a lifecycle is that a reader can tell finished from unfinished, and a
// status only this one caller understands is a row nothing can act on.
//
// It is here rather than beside a door because every door calls it: HTTP, the
// memory tools, and the verb itself.
func NormalizeTodoStatus(asked string) (string, error) {
	status := strings.ToLower(strings.TrimSpace(asked))
	if status == "" {
		return "", refuseStatus("a status says where the work is: one of %s",
			strings.Join(QueueStatuses, ", "))
	}
	for _, known := range QueueStatuses {
		if status == known {
			return status, nil
		}
	}
	return "", refuseStatus("%q is not a status a queue item has: one of %s",
		asked, strings.Join(QueueStatuses, ", "))
}

// TodoStatusOf is where a queue item says it is. A row with nothing in the
// column is outstanding work - raising something is saying it has to happen, and
// an item written before anything set a status is not in an unknown state.
//
// It is the queue's answer to statusOf, which reads a blank issue as open. The
// two vocabularies are separate on purpose and this is the seam between them.
func TodoStatusOf(a *Artifact) string {
	if a == nil {
		return TodoStatus
	}
	if s := strings.ToLower(strings.TrimSpace(a.Status)); s != "" {
		return s
	}
	return TodoStatus
}

// IsQueueItem reports whether an artifact is one the queue holds, and therefore
// one this lifecycle is about. It is the same question readWorkItem asks after
// its read, exported for the doors that have already read the row and have to
// decide which lifecycle they are looking at.
func IsQueueItem(a *Artifact) bool {
	return a != nil && a.Type == MemoryType && isWorkKind(a.Kind)
}

// SetTodoStatus moves a queue item and records who moved it: the status on the
// row and the entry in the log, in one write.
//
// The refusals, in the order they are asked:
//
//   - a token that resolves to nobody. An entry carries an actor, and a closure
//     nobody made is not one.
//
//   - a status that is not one of the three.
//
//   - an id that does not name a queue item this principal may READ. One that is
//     not here, one that is out of reach, and one that is here and is a bug or a
//     report are all the same answer - the answer a read of it would give -
//     because naming an id here is not a way to find out what else it might be.
//     A bug HAS a lifecycle and it is the issue workflow's; the door sends it
//     there rather than this verb taking it.
//
//   - a close with nothing said. See below.
//
// It does NOT refuse a restatement, and it does not refuse a move backwards. See
// the head of this file: reopening is the case this was asked for.
//
// SAID is what was measured, written in the same transaction as the closure.
//
// COUNTED, on one day: every row closed took two calls, POST
// /api/todo/{id}/note and then POST /api/artifact/{id}/status, and one seat
// closed nine that way. The note is what makes the row worth reading in a week
// - what was measured, what was left undone, which sha - and the status is
// bookkeeping. Two calls made the valuable half the optional one, and the
// failure is silent: a row closed with nothing said looks exactly like a row
// closed with a measurement until somebody opens it.
//
// So a move to done is REFUSED when nothing is said and the row carries no note
// already. The refusal is the point rather than the convenience - it is the
// only thing that stops "close it with what you measured" depending on the
// person remembering. Either fix satisfies it, and the message says both,
// because a seat that noted first and closed second was never the failure this
// is about.
//
// NOT on the way to todo or active. Picking a row up and putting it down are
// not claims that anything was learned, and a rule that fired on every move
// would be answered with the word "wip" nine times a day - which is the state
// this file exists to keep readable.
//
// NOT on the other doors that close a row, deliberately: /api/work/{id}/done
// takes `did` and mem_write writes the body in the same call, so both already
// say something. This is the verb whose whole content was a word.
func (d *DB) SetTodoStatus(
	ctx context.Context, p *Principal, todo, asked string, said ...string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "todo.status")
	defer span.End()

	// The seat, by voteActor's rule: an agent is its own party here for the
	// reason it is its own voter. Who closed the work is the seat that closed it,
	// not the person standing behind the seat - and p.UserID rides the meta
	// beside it so a reader has both.
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseStatus("this token resolves to nobody, so it cannot say " +
			"where a todo is")
	}
	status, err := NormalizeTodoStatus(asked)
	if err != nil {
		return nil, nil, err
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, nil, err
	}
	note := strings.TrimSpace(strings.Join(said, "\n"))
	if IsSilentClose(art, status, note) {
		return nil, nil, RefuseSilentClose(art)
	}

	from := TodoStatusOf(art)
	entry, err := statusEntryEvent(art, p, actor, actorKind, from, status)
	if err != nil {
		return nil, nil, err
	}
	// The note rides the same write, and it is built here rather than through
	// AppendTodoNote because that verb owns its own transaction: going through
	// it would put the measurement in one write and the closure in another,
	// which is the two-call shape this argument exists to remove. Same builder,
	// so a note written by closing is the same entry as a note written on its
	// own - see noteEntryEvent, and the projectless refusal it inherits.
	written := []*Event{entry}
	var noted *Event
	if note != "" {
		if noted, err = TodoNoteEntryEvent(art, p, note); err != nil {
			return nil, nil, err
		}
		written = append(written, noted)
	}
	// One transaction, one clock reading, all of them or none: a closure with no
	// entry behind it is work nobody can trace, and an entry with no closure
	// behind it is a log that lies. Nothing here ever comes back to finish a
	// half-written operation.
	if err := d.MoveArtifactStatus(ctx, art, status, written...); err != nil {
		return nil, nil, err
	}
	// The row is answered as a read of it would give, which is why the note just
	// written goes on it here: this call did not go back through ReadArtifact,
	// and a caller reading `notes` off the answer would otherwise get the row as
	// it was one entry ago with no way to tell. AppendTodoNote's rule.
	if noted != nil {
		art.Notes = append(art.Notes, TodoNoteEntryOf(noted))
	}
	span.SetArtifact(art.ID)
	return art, entry, nil
}

// TodoStatusEntryEvent builds the entry a move leaves in the log, for a write
// that sets the status as PART of a larger write of the same row.
//
// It exists for one caller: mem_write, which writes the whole item in one
// statement and whose author may state where it is. Going through SetTodoStatus
// there would write the row twice, so the entry is built here instead and handed
// to WriteMemory, which appends it in the same transaction as the item. Same
// builder, so the log behind a status is complete whichever door moved it - a
// value on a row with no entry behind it would make the provenance this file
// exists for a thing that is sometimes there.
//
// The caller has already normalised the status and has already settled that the
// row is theirs to write: this builds an entry, it does not decide anything. On a
// create the artifact has no id yet - it is minted inside the write - so the entry
// names its own id as its thread rather than the todo's, and WriteMemory fills in
// the artifact column once the id exists.
func TodoStatusEntryEvent(art *Artifact, p *Principal, from, to string) (*Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, refuseStatus("this token resolves to nobody, so it cannot say where a todo is")
	}
	return statusEntryEvent(art, p, actor, actorKind, from, to)
}

// statusEntryEvent builds the entry a move is.
func statusEntryEvent(art *Artifact, p *Principal, actor, actorKind, from, to string) (*Event, error) {
	meta, err := json.Marshal(map[string]string{
		"status":     to,
		"from":       from,
		"actor_kind": actorKind,
		"actor_user": p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: status of %s: %w", art.ID, err)
	}
	return &Event{
		Type:    EventTodoStatus,
		Project: art.Project,
		Room:    statusRoom(art),
		Thread:  art.ID,
		// The todo itself, which is what decides who reads the entry: the people
		// who can read the work are the people its state is about.
		Artifact: art.ID,
		Actor:    actor,
		Body:     statusBody(from, to),
		Meta:     meta,
	}, nil
}

// TodoStatusEntryOf renders one event as the entry it is.
func TodoStatusEntryOf(e *Event) StatusEntry {
	entry := StatusEntry{
		ID: e.ID, Type: e.Type, Todo: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.Status, entry.From = meta["status"], meta["from"]
		entry.ActorKind, entry.ActorUser = meta["actor_kind"], meta["actor_user"]
	}
	return entry
}

// LatestTodoStatus folds a todo's entries into the state that stands: the last
// one wins. nil when there are none, which is a todo nobody has moved THROUGH
// THIS VERB - it still carries a status on the row, which is TodoStatusOf's
// business and not this fold's.
//
// entries must be in log order, which is what todoStatusEvents returns.
func LatestTodoStatus(entries []StatusEntry) *TodoState {
	if len(entries) == 0 {
		return nil
	}
	last := entries[len(entries)-1]
	return &TodoState{
		Status: last.Status, From: last.From, By: last.Actor, ByKind: last.ActorKind,
		ByUser: last.ActorUser, At: last.Created, Entry: last.ID,
	}
}

// TodoStatusLog is every entry naming this todo that p may read, oldest first -
// so a reader sees the work being picked up, finished and reopened rather than
// only where it is now. It is AssignLog for the other half of the queue
// metadata, with the same permission story: the filter is in the WHERE clause
// and it is not a second rule.
func (d *DB) TodoStatusLog(ctx context.Context, p *Principal, todo string) ([]StatusEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "todo.status.log")
	defer span.End()

	events, err := d.todoStatusEvents(ctx, p, []string{todo}, false)
	if err != nil {
		return nil, err
	}
	out := make([]StatusEntry, 0, len(events))
	for _, e := range events {
		out = append(out, TodoStatusEntryOf(e))
	}
	return out, nil
}

// todoStatusEvents reads the entries naming any of todos, in log order, through
// the same event filter every other read of the log uses.
//
// There is no LIMIT on this, for depEvents' reason: the fold is over the WHOLE
// log for each todo, and a page that stopped early would fold a prefix - an
// answer that is not the state that stands.
func (d *DB) todoStatusEvents(
	ctx context.Context, p *Principal, todos []string, scopeAll bool,
) ([]*Event, error) {
	if len(todos) == 0 {
		return nil, nil
	}
	return readPage(ctx, d, "status events", func(a *args) string {
		idsArg := a.next(pq.Array(todos))
		typeArg := a.next(EventTodoStatus)
		filter := EventFilterSQL(p, "e", a, scopeAll)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ANY(` + idsArg + `) AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// statusRoom is where an entry lands in the log: the room the todo was raised
// in, or the status room when it was raised in none. It is assignRoom's rule,
// and it exists for the reason that one does.
func statusRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return StatusRoom
}

// statusBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI.
//
// It names both ends, the way the issue workflow's trail does ("open->triaged"),
// because "closed" and "closed something that was already closed" are different
// facts and only the pair says which happened.
func statusBody(from, to string) string {
	if from == "" {
		return "status " + to
	}
	return from + "->" + to
}

// IsSilentClose reports whether this move takes a queue item off the queue and
// says nothing about it: a close, with no note on the call and none already on
// the row.
//
// It is exported and it is a PREDICATE rather than a rule buried in one verb,
// because there is more than one door that closes a row - the queue verb here
// and mem_write, which writes the whole item in one statement - and a rule
// enforced at one of them is a rule with a way around it. Same question, same
// answer, whichever door was knocked on.
//
// Only done, and only for a queue item. See SetTodoStatus for both reasons.
func IsSilentClose(art *Artifact, status, note string) bool {
	return IsQueueItem(art) &&
		status == DoneStatus &&
		strings.TrimSpace(note) == "" &&
		len(art.Notes) == 0
}

// RefuseSilentClose is what every door says when it is asked to close a row
// with nothing said. One sentence, in one place, so that the console, the CLI,
// MCP and the drainer cannot each explain it differently.
func RefuseSilentClose(art *Artifact) error {
	return refuseStatus("closing %s says the work is finished and says nothing about it - a "+
		"row closed with nothing said reads in a week exactly like one closed with a "+
		"measurement. Say what was measured, what is left undone, or which sha carries it: "+
		"pass a note with this move, or write one on the row first", art.ID)
}
