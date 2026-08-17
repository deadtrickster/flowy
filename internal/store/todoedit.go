package store

// EDITING A TODO NOBODY HAS STARTED, and being TOLD when somebody starts it
// while you are typing.
//
// The room's ask has two halves and the second one is the whole feature. A todo
// is raised in a hurry - a title that turns out to say the wrong thing, a body
// that was written before anybody knew what the work was - and until this file
// the only way to correct it was mem_write, which takes the whole item and has
// no idea what state the queue is in. That is fine while nobody has picked the
// row up, and it is a LOST UPDATE the moment somebody has: the agent that took
// the todo is working from the words that were there when they read it, and a
// rewrite landing underneath them changes the job without telling either party
// anything. Both writes report success. Nothing anywhere records that one of
// them was made against a row that had moved.
//
// So: THE EDIT CARRIES THE STATUS THE EDITOR SAW, and the write is a
// COMPARE-AND-SET against it. If the todo went active between the read and the
// write the update touches nothing, no entry is appended, and the editor is
// handed ErrTodoMoved NAMING WHO PICKED IT UP. A refusal that says who is a
// refusal the editor can act on - they go and talk to that agent - and it is
// the difference between this and a 409 that only says "try again", which
// against a todo somebody is now working on is the wrong advice.
//
// WHY IT CANNOT BE A READ FOLLOWED BY A WRITE. Both writers read "still todo",
// both write, both are told they succeeded. The check has to be in the WHERE of
// the one UPDATE that lands, which is what SetArtifactWordsIf is - see
// workqueue.go, where the same argument is made about a claim and where the
// primitive underneath this one was built.
//
// TITLE AND BODY ARE STILL THE AUTHOR'S. This does not widen who may write the
// words of somebody else's item: todostatus.go's ruling stands, and it is the
// reason the queue metadata and the prose have different doors. What is new is
// that the author's own edit now has a state it is written against. A stranger
// who wants the title changed asks for it; a stranger who wants the work moved
// has had three doors for that since the queue got a lifecycle.
//
// ONLY WHILE IT IS STILL A TODO. Active work belongs to whoever picked it up:
// changing what the job is under them is the thing this file exists to prevent,
// and doing it deliberately is not better than doing it by accident. Done work
// is history. Both are refused by naming the state the item is actually in, so
// the editor knows which conversation to have.
//
// THE EDIT IS AN EVENT, for the reason a status move is one: a column records
// THAT the words changed and the question anybody asks afterwards is WHO changed
// them and WHEN. The entry hangs off the todo, so it reaches exactly the todo's
// readers, and it carries the status the edit was written against - which is the
// record that this write was made with its eyes open.
//
// ONE LIMIT, INHERITED. The entry is minted, so it does not cross a node
// boundary in either direction: the refusal that makes it worth reading is on
// this verb, and an entry a client could hand over would be an edit asserted
// about a row that never moved.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// EventTodoEdit is what an edit of a queue item's words is in the log. It is
// minted, so the only way to get one is to have gone through the verb, which is
// where the compare-and-set is.
const EventTodoEdit = "todo.edit"

// EditRoom is where an entry lands when the todo it is about names no room of
// its own. It is StatusRoom's rule for the other half of the item: an entry
// nobody can find is an entry nobody reads.
const EditRoom = "edit"

// ErrTodoMoved is an edit written against a state the todo has left.
//
// It names WHO, because that is what turns a refusal into something the editor
// can act on. "Try again" is the wrong advice against a todo somebody is now
// working on - the right move is to go and tell them what the title should have
// said - and a message that only reports a conflict cannot say that.
type ErrTodoMoved struct {
	Todo string
	// Saw is the state the edit was written against, and Now is the state the
	// row is actually in. Both are on the error because "you were behind" and
	// "you were editing finished work" are different conversations.
	Saw string
	Now string
	// By is the seat that moved it, empty when nothing in the log says who -
	// a row moved by a write that left no entry behind it.
	By string
}

func (e ErrTodoMoved) Error() string {
	who := e.By
	if who == "" {
		who = "somebody"
	}
	return fmt.Sprintf("todo %s was %s when you started editing and is %s now - %s picked it "+
		"up while you were typing. Nothing was written: your text would have changed the job "+
		"under them. Read it again, and take the change up with %s",
		e.Todo, e.Saw, e.Now, who, who)
}

// depRefusal marks this as the caller's mistake rather than a broken node, so
// HTTP answers 4xx rather than 500. It is the interface every other queue verb
// refuses through.
func (e ErrTodoMoved) depRefusal() {}

// ErrNotYoursToEdit is an edit of somebody else's words.
//
// It is its own error rather than a bare refusal so the door can say the same
// sentence mem_write says - an item's words are its author's, and the queue
// metadata on it is not - and point at the doors that DO work for a principal
// who only wants the work moved.
type ErrNotYoursToEdit struct {
	Todo  string
	Owner string
}

func (e ErrNotYoursToEdit) Error() string {
	return fmt.Sprintf("todo %s belongs to somebody else, so its words are not yours to "+
		"change: an item's words are its author's. What kind of work it is, who is carrying "+
		"it and where it has got to are not - POST /api/todo/%s/assignee, "+
		"/api/todo/%s/category and /api/artifact/%s/status work for any principal who can "+
		"READ it", e.Todo, e.Todo, e.Todo, e.Todo)
}

func (e ErrNotYoursToEdit) depRefusal() {}

// EditEntry is one entry in the log behind an edit: the todo, what changed, the
// state the edit was written against, who made it and when.
type EditEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Todo string `json:"todo"`
	// Saw is the status the edit was written against. It is not omitempty: an
	// entry that left it out would leave a reader deciding whether the edit was
	// unguarded or whether the node simply did not say.
	Saw   string `json:"saw"`
	Title bool   `json:"title"`
	Body  bool   `json:"body"`
	// Was is the title the edit replaced, kept because the thing a reader of
	// this log wants is what the row used to say - the new title is on the row
	// and the old one is nowhere else. An edit that left the title alone carries
	// none.
	Was       string `json:"was,omitempty"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// EditTodo replaces the title and body of a queue item NOBODY HAS STARTED, and
// refuses when somebody started it while the editor was typing.
//
// title and body are pointers: nil is "this edit says nothing about it and it
// stands", which is not the same as an empty string. A body may legitimately be
// emptied - somebody pasted the wrong paragraph in - and a write that treated
// "" as "leave it" would be a success envelope that changed something other
// than what it was asked to.
//
// saw is the status the editor read, and it is REQUIRED. An edit with no state
// behind it is exactly the blind write this verb exists to replace, and
// defaulting it here would put the lost update back one call further down.
//
// The refusals, in the order they are asked:
//
//   - a token that resolves to nobody. An entry carries an actor, and an edit
//     nobody made is not one.
//   - an edit that changes nothing, and a title emptied to nothing.
//   - a saw that is not one of the three, or is one of the two that are not
//     todo. Active work belongs to whoever picked it up.
//   - an id that does not name a queue item this principal may READ, answered
//     as an id that is not there.
//   - an item whose words are somebody else's.
//   - and last, the one that matters: the row moved. Once as a courtesy from
//     the read, for the better message, and once as the guard on the write,
//     which is what makes it true.
func (d *DB) EditTodo(
	ctx context.Context, p *Principal, todo string, title, body *string, saw string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "todo.edit")
	defer span.End()

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseStatus("this token resolves to nobody, so it cannot edit a todo")
	}
	if title == nil && body == nil {
		return nil, nil, refuseStatus("an edit has to say what it changes: a title, a body, " +
			"or both")
	}
	if title != nil && strings.TrimSpace(*title) == "" {
		return nil, nil, refuseStatus("a todo's title is how anybody finds it again, so an " +
			"edit cannot empty it - a body may be emptied, a title may not")
	}
	// The state the editor read, validated against the same vocabulary every
	// other door validates against: a guard written from a word this node does
	// not know is a guard that never holds, which would refuse every edit and
	// look like a race.
	seen, err := NormalizeTodoStatus(saw)
	if err != nil {
		return nil, nil, err
	}
	if seen != TodoStatus {
		return nil, nil, refuseStatus("an edit is written against work nobody has started, "+
			"and this one says it saw %q. A row that has been picked up or finished is not "+
			"one to reword underneath whoever did the work: say what you meant to them, or "+
			"move it back to %s first", seen, TodoStatus)
	}

	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, nil, err
	}
	if art.OwnerUser != p.UserID {
		return nil, nil, ErrNotYoursToEdit{Todo: art.ID, Owner: art.OwnerUser}
	}
	// THE COURTESY CHECK. Everything this asks, the guard asks again in the
	// WHERE - see ClaimWork, where the same pair is written down. It is here so
	// that the ordinary case, where the todo moved some time ago rather than in
	// the last millisecond, gets the same sentence as the race does instead of a
	// worse one.
	if now := TodoStatusOf(art); now != seen {
		return nil, nil, d.movedUnder(ctx, p, art, seen, now)
	}

	was := art.Title
	newTitle, newBody := art.Title, art.Body
	if title != nil {
		newTitle = strings.TrimSpace(*title)
	}
	if body != nil {
		newBody = *body
	}
	entry, err := editEntryEvent(art, p, actor, actorKind, seen, was, title != nil, body != nil)
	if err != nil {
		return nil, nil, err
	}
	// THE GUARD IS THE FEATURE. It reads the column the way TodoStatusOf reads
	// it - a row with nothing in it is outstanding work - so an item written
	// before anything set a status is editable rather than permanently refused
	// by a guard that could not match.
	guard := `coalesce(nullif(lower(btrim(status)), ''), '` + TodoStatus + `') = '` +
		sqlLiteral(seen) + `'`
	if err := d.SetArtifactWordsIf(ctx, art, newTitle, newBody, guard, entry); err != nil {
		if errors.Is(err, ErrGuardFailed) {
			// Somebody won between the read and the write, which is exactly the
			// window this exists for. Nothing was written. Say who.
			fresh, ferr := d.GetArtifact(ctx, art.ID)
			if ferr != nil {
				return nil, nil, ErrTodoMoved{Todo: art.ID, Saw: seen, Now: "somewhere else"}
			}
			return nil, nil, d.movedUnder(ctx, p, fresh, seen, TodoStatusOf(fresh))
		}
		return nil, nil, err
	}
	span.SetArtifact(art.ID)
	return art, entry, nil
}

// movedUnder builds the refusal, naming whoever moved the row.
//
// WHO IS READ FROM THE LOG rather than from a column, because the column says
// only where the work is and the editor's question is who to talk to. The
// status log's latest entry is the seat that made the move; a row moved by a
// write that left no entry falls back to whoever is carrying it, and then to
// nobody at all - a refusal that names the wrong agent would be worse than one
// that names none.
func (d *DB) movedUnder(
	ctx context.Context, p *Principal, art *Artifact, saw, now string,
) error {
	moved := ErrTodoMoved{Todo: art.ID, Saw: saw, Now: now}
	if log, err := d.TodoStatusLog(ctx, p, art.ID); err == nil {
		if state := LatestTodoStatus(log); state != nil && state.Status == now {
			moved.By = state.By
		}
	}
	if moved.By == "" {
		moved.By = AssigneeOf(art)
	}
	return moved
}

// editEntryEvent builds the entry an edit leaves in the log.
func editEntryEvent(
	art *Artifact, p *Principal, actor, actorKind, saw, was string, title, body bool,
) (*Event, error) {
	meta := map[string]string{
		"saw":        saw,
		"actor_kind": actorKind,
		"actor_user": p.UserID,
	}
	if title {
		meta["title"] = "1"
		// The words that are about to stop being on the row. Everything else an
		// edit changed can be read off the item afterwards; what it replaced
		// cannot be read off anything.
		meta["was"] = was
	}
	if body {
		meta["body"] = "1"
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("store: edit of %s: %w", art.ID, err)
	}
	return &Event{
		Type:    EventTodoEdit,
		Project: art.Project,
		Room:    editRoom(art),
		Thread:  art.ID,
		// The todo itself, which is what decides who reads the entry: the people
		// who can read the work are the people a change to it is about.
		Artifact: art.ID,
		Actor:    actor,
		Body:     editBody(title, body),
		Meta:     encoded,
	}, nil
}

// TodoEditEntryOf renders one event as the entry it is.
func TodoEditEntryOf(e *Event) EditEntry {
	entry := EditEntry{
		ID: e.ID, Type: e.Type, Todo: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.Saw, entry.Was = meta["saw"], meta["was"]
		entry.Title, entry.Body = meta["title"] == "1", meta["body"] == "1"
		entry.ActorKind, entry.ActorUser = meta["actor_kind"], meta["actor_user"]
	}
	return entry
}

// TodoEditLog is every edit entry naming this todo that p may read, oldest
// first - so a reader sees what the row used to say rather than only what it
// says now. It is TodoStatusLog for the other half of the item, with the same
// permission story: the filter is in the WHERE clause and it is not a second
// rule.
func (d *DB) TodoEditLog(ctx context.Context, p *Principal, todo string) ([]EditEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "todo.edit.log")
	defer span.End()

	events, err := d.todoEditEvents(ctx, p, todo)
	if err != nil {
		return nil, err
	}
	out := make([]EditEntry, 0, len(events))
	for _, e := range events {
		out = append(out, TodoEditEntryOf(e))
	}
	return out, nil
}

// todoEditEvents reads the entries naming one todo, in log order, through the
// same event filter every other read of the log uses.
func (d *DB) todoEditEvents(ctx context.Context, p *Principal, todo string) ([]*Event, error) {
	if strings.TrimSpace(todo) == "" {
		return nil, nil
	}
	return readPage(ctx, d, "edit events", func(a *args) string {
		idArg := a.next(strings.TrimSpace(todo))
		typeArg := a.next(EventTodoEdit)
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ` + idArg + ` AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// editRoom is where an entry lands in the log: the room the todo was raised in,
// or the edit room when it was raised in none. It is statusRoom's rule, and it
// exists for the reason that one does.
func editRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return EditRoom
}

// editBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI. It names WHICH words moved, because "the title was rewritten"
// and "a paragraph was added" are different facts to somebody scanning a room.
func editBody(title, body bool) string {
	switch {
	case title && body:
		return "edited title and body"
	case title:
		return "edited title"
	default:
		return "edited body"
	}
}
