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

// WHO IS CARRYING A TODO, and who said so.
//
// A todo is not the property of whoever typed it. One agent files the queue and
// the whole room drains it: the operator hands work out, an agent claims a task
// off the queue, and an agent that has to stop hands what it was carrying to the
// next one. So the assignee is the one part of a queue item that changes hands,
// and it changes hands without asking the author.
//
// Four decisions, and each of them is a thing that went wrong first.
//
// READ PERMISSION IS THE BAR, AND THERE IS NO SECOND ONE. Whoever can read a
// todo may set or override its assignee. That is handleArtifactStatus's rule -
// a participant who cannot say "I am on this" has to ask somebody else to say it
// for them - and it is the right rule here for the same reason: the assignee is
// a NAME IN fields, not a capability. Nothing in the permission filter has ever
// looked at it, so the widest this reaches is "whoever can see the plan can edit
// who is on the plan", and a principal who cannot see the todo gets the answer a
// read of it would give. There is deliberately no grant type and no new column:
// a permission layer over a field that grants nothing would be a layer to
// maintain and nothing to protect.
//
// AN ASSIGNMENT IS AN EVENT, and the value on the row is the head of it. The
// entry names the todo and the name, is signed, and appends - so the log says
// WHO handed the work over and WHEN, which a column cannot answer: a field write
// records THAT something changed, not who changed it. That is the reasoning
// behind an edge being an event, one field along - see the head of deps.go - and
// the entry here is DepEntry's shape for exactly that reason. What is different
// is that the value ALSO lands on the row, in the same transaction and under one
// clock reading, the way a status move writes the column and the trail entry
// together (see MoveArtifactStatus). An assignee is single-valued and every
// surface in this fabric already reads it off fields - the panel, the queue, the
// FUSE mount, the ready query - so the fold and the row cannot disagree, because
// one verb writes both or neither. A reader wanting the current value reads the
// row; a reader asking who put it there reads the log.
//
// THE ENTRY HANGS OFF THE TODO, which is what makes it readable by the people it
// is about. EventFilterSQL gives an event naming an artifact exactly that
// artifact's readers, so every principal who can read the todo reads every
// assignment of it - including the one the operator made about somebody else's
// item. A projectless todo is the exception and it is the inherited one: a
// projectless event is read back by its ACTOR rather than by the artifact's
// readers, so an assignment of a personal todo is provenance its own author can
// read and their agent cannot. It is not refused the way a projectless
// dependency is, because the VALUE is on the row either way and a personal todo
// has one reader-set anyway - what degrades is who can read the entry behind it,
// not who can see who is carrying the work.
//
// THE REST OF THE ITEM IS STILL THE AUTHOR'S. Title and body are somebody's
// words about their own work and mem_write refuses to let anybody else edit
// them - loudly, which is the other half of this change. Only the assignee
// changes hands, and this verb is the only thing that moves it.
//
// ONE LIMIT, AND IT IS INHERITED RATHER THAN CHOSEN. The entry is minted - see
// mintedEventTypes - so it does not cross a node boundary in either direction,
// exactly as a dep edge and a vote do not. A peer therefore holds the assignee
// (it is on the row, and rows replicate) and none of the entries behind it, so
// "who assigned this" is a question answered on the node the claim was made on.
// Widening that is a change to what federation carries, not to this file.

// EventTodoAssign is what an assignment is in the log. It is minted, so the only
// way to get one is to have gone through the verb, which is where the refusal is.
const EventTodoAssign = "todo.assign"

// AssignRoom is where an entry lands when the todo it is about names no room of
// its own, so an assignment is somewhere a reader can find it rather than in the
// roomless part of the log. It is DepRoom's rule for the other queue verb.
const AssignRoom = "assign"

// MaxAssigneeName is the longest name a write may hand a todo. A handle around
// here is a word, and a panel column is narrow: the bar exists so that a body
// pasted into the box lands as a refusal rather than as a row nobody can read.
const MaxAssigneeName = 64

// AssignEntry is one entry in the log behind an assignment: the todo, the name,
// who said it, and when.
//
// It is DepEntry's shape for DepEntry's reason - what makes the record worth
// keeping is that an override does not erase the previous claim, it appends the
// fact that somebody took the work off whoever had it.
type AssignEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Todo string `json:"todo"`
	// Assignee is NOT omitempty. Nobody carrying this is a state somebody chose,
	// and an absent key would leave a client to decide whether it means nobody
	// or means the node did not say - the two-words-for-one-state problem
	// nobodyWords exists to stop.
	Assignee string `json:"assignee"`
	// Held is who was carrying it immediately BEFORE this entry, empty when
	// nobody was. It is on the entry rather than computed by a reader because
	// the log is the only place the previous holder still exists after the
	// field has moved on, and because a caller deciding whether a handover was
	// contested should not have to reconstruct it from two reads.
	Held      string `json:"held,omitempty"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// Assignment is the fold of that log: who is carrying the todo, and the entry
// that put them there.
//
// Latest wins, over seq_hlc, so two seats claiming the same todo converge on one
// answer whichever order the entries were read in. It is TallyOf's half of the
// design - a READING of the log rather than a second stored copy of it.
type Assignment struct {
	Assignee string `json:"assignee"`
	// By is the seat that made the claim, and ByUser is the person behind it.
	// Both are on the answer because "an agent claimed this" and "the operator
	// handed it over" are the two things a reader is telling apart.
	By     string `json:"by"`
	ByKind string `json:"by_kind,omitempty"`
	ByUser string `json:"by_user,omitempty"`
	// Held is who this claim took it FROM - empty when it was unowned. A client
	// that wanted an uncontested claim compares this against what it expected
	// and can say "you took this from X" instead of reporting a bare success.
	Held  string `json:"held,omitempty"`
	At    string `json:"at"`
	Entry string `json:"entry"`
}

// AssignRefusal is what every refusal this verb makes ABOUT THE ASSIGNMENT IT
// WAS ASKED FOR satisfies: the caller's mistake, and fixable by the caller.
//
// It is DepRefusal's interface rather than a second one, so that a refusal added
// to either queue verb cannot be one that HTTP maps to 400 and MCP reports as a
// broken node. NotATodoError is deliberately not one of them here either: an id
// out of reach is answered as an id that is not there.
type assignRefusalError struct{ reason string }

func (e assignRefusalError) Error() string { return e.reason }
func (e assignRefusalError) depRefusal()   {}

func refuseAssign(format string, a ...any) error {
	return assignRefusalError{reason: fmt.Sprintf(format, a...)}
}

// NormalizeAssignee validates a name a write hands a todo and returns it as the
// node stores it.
//
// Empty is the ordinary case and means nobody: putting work down is something
// somebody does on purpose, and it is this argument with nothing in it rather
// than a second verb. So are the words the queue has always used for nobody -
// they come back as the empty name, which is what makes every surface say one
// word for one state. See NobodyName.
//
// It is here rather than beside a door because both doors and the verb itself
// call it: a name that is a paragraph must be refused wherever it arrives.
func NormalizeAssignee(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || NobodyName(name) {
		return "", nil
	}
	if strings.ContainsAny(name, "\n\r\t") || len(name) > MaxAssigneeName {
		return "", refuseAssign("%q is not a name: an assignee is a handle of at most %d "+
			"characters on one line", name, MaxAssigneeName)
	}
	return name, nil
}

// AssignTodo records who is carrying a todo: the name on the row and the entry
// in the log, in one write.
//
// The refusals, in the order they are asked:
//
//   - a token that resolves to nobody. An entry carries an actor, and an
//     assignment nobody made is not one.
//   - a name that is not a name.
//   - an id that does not name a queue item this principal may READ. One that is
//     not here, one that is out of reach, and one that is here and is a report or
//     a proposal are all the same answer - the answer a read of it would give -
//     because naming an id here is not a way to find out what else it might be.
//
// It does NOT refuse a restatement. Handing a todo to whoever already has it is
// somebody saying it is still theirs, and the room door has always accepted it
// (it says so in the room each time); refusing it here would make one door
// refuse what the other one accepts. So the log holds restatements, and the fold
// is latest-wins rather than a diff.
//
// said is an extra event to write in the same transaction, or nil. It exists for
// exactly one caller - the room's panel, which announces the handover as an
// ordinary chat message in the thread the todo was raised out of - and it is a
// parameter rather than a second write so that the room hearing about it and the
// assignment landing are one thing. Nothing else passes it.
// seatHandle is the handle this principal's seat speaks under, or "" when it
// has none. The assignee field holds a name, not an id, so "is the caller the
// holder" is a handle comparison, and a seat with no handle is never the
// holder. Swallowed errors read the same way: a handle that cannot be read
// cannot authorize a takeover, which is the strict direction.
func (d *DB) seatHandle(ctx context.Context, p *Principal) string {
	if p == nil || p.UserID == "" {
		return ""
	}
	var handle string
	_ = d.sql.QueryRowContext(ctx,
		`SELECT coalesce(handle, '') FROM users WHERE id = $1`, p.UserID).Scan(&handle)
	return strings.TrimSpace(handle)
}

func (d *DB) AssignTodo(
	ctx context.Context, p *Principal, todo, asked string, said *Event,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "todo.assign")
	defer span.End()

	// The seat, by voteActor's rule: an agent is its own party here for the
	// reason it is its own voter. Who handed the work over is the seat that did
	// it, not the person standing behind the seat - and p.UserID rides the meta
	// beside it so a reader has both.
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseAssign("this token resolves to nobody, so it cannot say " +
			"who is carrying a todo")
	}
	name, err := NormalizeAssignee(asked)
	if err != nil {
		return nil, nil, err
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, nil, err
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	// WHO HAD IT BEFORE, and a held row is TAKEN, not overwritten.
	//
	// This verb was report-only - the answer said who had it before, and any
	// write moved it - because refusing seemed to break what assignment is for:
	// the operator handing work out, an agent picking up what somebody
	// abandoned. Neither breaks by naming who the taker takes FROM, which is
	// what expect is. What the report-only version actually enabled, measured
	// twice in one morning: a claim written WITHOUT expect - the very writer the
	// CAS exists to guard - lands over a guarded claim, and the board reads the
	// accident as the holder. The guard was optional and the unguarded path was
	// the default.
	//
	// So: an unheld row takes any write; the holder moves their own row as they
	// like - release it, hand it on, renew it - and anybody ELSE changing who
	// carries it is refused, named the holder and told the way through:
	// expect:that-holder, which is the handover. Every path that worked still
	// works; what stops working is moving a row whose holder you could not be
	// bothered to read.
	held := strings.TrimSpace(AssigneeOf(art))
	if held != "" && held != name && d.seatHandle(ctx, p) != held {
		return nil, nil, refuseAssign(fmt.Sprintf(
			"todo %s is carried by %s - a held row moves by naming its holder: pass expect:%s to take it over",
			art.ID, held, held))
	}
	// Written whenever it was asked for, including empty: the key being there at
	// all is what says somebody decided, and what outranks a stale OWNER line in
	// the body. See AssigneeOf.
	fields[AssigneeField] = name
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: assign %s: %w", art.ID, err)
	}

	entry, err := assignEvent(art, p, actor, actorKind, name, held)
	if err != nil {
		return nil, nil, err
	}
	events := []*Event{entry}
	// PUTTING WORK DOWN MOVES BOTH FACTS. A row nobody is carrying cannot be
	// `active` - see queuecoherence.go - so a release takes it back to `todo` in
	// this same write, and leaves the status entry that says it did. The
	// alternative, refusing the release, would mean an agent that cannot finish
	// also cannot hand back, and holds the row forever.
	//
	// There is no case for leaving it active: the only way a row stays active is
	// that somebody is on it, and naming that somebody is a handover rather than
	// a release. It moves nothing on a row that was todo or done.
	status := putDownStatus(art, name)
	if status != "" {
		back, err := statusEntryEvent(art, p, actor, actorKind, ActiveStatus, status)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, back)
	}
	if said != nil {
		events = append(events, said)
	}
	// One transaction, one clock reading, both rows or neither: an assignment
	// with no entry behind it is a handover nobody can trace, and an entry with
	// no assignment behind it is a log that lies. Nothing here ever comes back to
	// finish a half-written operation.
	if err := d.SetArtifactFieldsAndStatusIf(ctx, art, column, status, "", events...); err != nil {
		return nil, nil, err
	}
	span.SetArtifact(art.ID)
	return art, entry, nil
}

// AssignEntryEvent builds the entry an assignment leaves in the log, for a write
// that sets the assignee as PART of a larger write of the same row.
//
// It exists for one caller: mem_write, which writes the whole item in one
// statement and whose author may state who is carrying it. Going through
// AssignTodo there would write the row twice, so the entry is built here instead
// and handed to WriteMemory, which appends it in the same transaction as the
// item. Same builder, so the log behind an assignee is complete whichever verb
// moved it - a value on a row with no entry behind it would make the provenance
// this file exists for a thing that is sometimes there.
//
// The caller has already normalised the name and has already settled that the row
// is theirs to write: this builds an entry, it does not decide anything. On a
// create the artifact has no id yet - it is minted inside the write - so the entry
// names its own id as its thread rather than the todo's, and WriteMemory fills in
// the artifact column once the id exists.
func AssignEntryEvent(art *Artifact, p *Principal, name string) (*Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, refuseAssign("this token resolves to nobody, so it cannot say " +
			"who is carrying a todo")
	}
	// The previous holder, read off the row this write is about to replace. On a
	// CREATE there is no previous holder and AssigneeOf answers empty, which is
	// the right answer rather than a special case: nothing was taken from anybody.
	return assignEvent(art, p, actor, actorKind, name, strings.TrimSpace(AssigneeOf(art)))
}

// assignEvent builds the entry an assignment is.
func assignEvent(art *Artifact, p *Principal, actor, actorKind, name, held string) (*Event, error) {
	// held rides the meta so the entry records what the claim TOOK IT FROM. It is
	// omitted when nobody held it, because a key that is present and empty means
	// "somebody said nobody" everywhere else in this file, and here it would mean
	// "there was no previous holder" - two different facts one encoding cannot
	// carry, which is the nobodyWords problem one field along.
	fields := map[string]string{
		AssigneeField: name,
		"actor_kind":  actorKind,
		"actor_user":  p.UserID,
	}
	if held != "" {
		fields["held"] = held
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("store: assign %s: %w", art.ID, err)
	}
	return &Event{
		Type:    EventTodoAssign,
		Project: art.Project,
		Room:    assignRoom(art),
		Thread:  art.ID,
		// The todo itself, which is what decides who reads the entry: the people
		// who can read the work are the people the handover is about.
		Artifact: art.ID,
		Actor:    actor,
		Body:     assignBody(name),
		Meta:     meta,
	}, nil
}

// AssignEntryOf renders one event as the entry it is.
func AssignEntryOf(e *Event) AssignEntry {
	entry := AssignEntry{
		ID: e.ID, Type: e.Type, Todo: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.Assignee = meta[AssigneeField]
		entry.ActorKind, entry.ActorUser = meta["actor_kind"], meta["actor_user"]
		// Absent on entries written before this field existed, and absent when
		// nobody held it - both read as empty, which is the same answer and the
		// true one: this entry took the work from nobody it can name.
		entry.Held = meta["held"]
	}
	return entry
}

// LatestAssignment folds a todo's entries into the claim that stands: the last
// one wins. nil when there are none, which is a todo nobody has claimed THROUGH
// THIS VERB - it may still carry an assignee written before this surface existed,
// which is AssigneeOf's business and not this fold's.
//
// entries must be in log order, which is what assignEvents returns.
func LatestAssignment(entries []AssignEntry) *Assignment {
	if len(entries) == 0 {
		return nil
	}
	last := entries[len(entries)-1]
	return &Assignment{
		Assignee: last.Assignee, By: last.Actor, ByKind: last.ActorKind,
		ByUser: last.ActorUser, Held: last.Held, At: last.Created, Entry: last.ID,
	}
}

// AssignLog is every entry naming this todo that p may read, oldest first - so a
// reader sees the work changing hands rather than only whose it is now. It is
// DepLog for the other queue verb, with the same permission story: the filter is
// in the WHERE clause and it is not a second rule.
func (d *DB) AssignLog(ctx context.Context, p *Principal, todo string) ([]AssignEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "todo.assign.log")
	defer span.End()

	events, err := d.assignEvents(ctx, p, []string{todo}, false)
	if err != nil {
		return nil, err
	}
	out := make([]AssignEntry, 0, len(events))
	for _, e := range events {
		out = append(out, AssignEntryOf(e))
	}
	return out, nil
}

// Assignments is the standing claim on each of todos, as this reader sees it:
// one query for the whole set, folded per todo. A todo with no entries is absent
// from the map rather than present and empty.
func (d *DB) Assignments(
	ctx context.Context, p *Principal, todos []string, scopeAll bool,
) (map[string]*Assignment, error) {
	events, err := d.assignEvents(ctx, p, todos, scopeAll)
	if err != nil {
		return nil, err
	}
	byTodo := map[string][]AssignEntry{}
	for _, e := range events {
		entry := AssignEntryOf(e)
		byTodo[entry.Todo] = append(byTodo[entry.Todo], entry)
	}
	out := make(map[string]*Assignment, len(byTodo))
	for todo, entries := range byTodo {
		if latest := LatestAssignment(entries); latest != nil {
			out[todo] = latest
		}
	}
	return out, nil
}

// assignEvents reads the entries naming any of todos, in log order, through the
// same event filter every other read of the log uses.
//
// There is no LIMIT on this, for depEvents' reason: the fold is over the WHOLE
// log for each todo, and a page that stopped early would fold a prefix - an
// answer that is not the claim that stands.
func (d *DB) assignEvents(
	ctx context.Context, p *Principal, todos []string, scopeAll bool,
) ([]*Event, error) {
	if len(todos) == 0 {
		return nil, nil
	}
	return readPage(ctx, d, "assign events", func(a *args) string {
		idsArg := a.next(pq.Array(todos))
		typeArg := a.next(EventTodoAssign)
		filter := EventFilterSQL(p, "e", a, scopeAll)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ANY(` + idsArg + `) AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// assignRoom is where an entry lands in the log: the room the todo was raised
// in, or the assign room when it was raised in none. It is depRoom's rule, and
// it exists for the reason that one does - an entry nobody can find is an entry
// nobody reads.
func assignRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return AssignRoom
}

// assignBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI.
//
// It is deliberately not the sentence the room door says in the room ("moved X
// from A to B"): that one is a message about a conversation's plan and this one
// is the record on the item, and a reader looking at both should not be left
// wondering whether the handover happened twice.
func assignBody(name string) string {
	if name == "" {
		return "assigned to nobody"
	}
	return "assigned to " + name
}
