package store

// A ROW ACCUMULATES WHAT IS LEARNED ABOUT IT.
//
// Until this file a row was fixed at the moment it was filed. The words are the
// author's and only the author may change them (todoedit.go), and only while
// nobody has started the work - which is right, and which leaves nowhere at all
// for the thing that actually happens: somebody else picks the row up, measures
// something, works out the real fix shape, finds what it is blocked on, and has
// no door to put any of it on. It goes in the room instead, scrolls away, and
// the next agent rediscovers it. The stale-tip row was filed and announced twice
// and still cost one agent three gate runs and another a from-scratch
// re-derivation.
//
// AN APPEND IS NOT AN EDIT, and that difference is the whole design:
//
//   - Nothing already written changes. A note is added beside the body; the
//     body, the title and every earlier note stay exactly as they were. So there
//     is no supersede-and-resign question to answer, no lost update to guard
//     against, and no reason to refuse one because somebody picked the row up -
//     the case a note is MOST worth writing in is precisely the one where the
//     work is under way or finished.
//   - It is not the author's alone. Read permission is the whole bar, which is
//     todostatus.go's ruling and the same argument one field along: what is
//     LEARNED about a row is not authorship of it. The seat that measured the
//     thing is routinely not the seat that typed the title, and an author-only
//     append would have blocked exactly the case that produced this file.
//   - It is never editable and never deletable. There is no verb here that
//     rewrites or removes an entry, deliberately: a log that can be tidied is a
//     log a reader has to check against something else. A note that turned out
//     to be wrong is answered by a further note saying so, which is what the
//     record should say anyway.
//
// THE TEXT IS THE EVENT'S BODY, not a key in meta. Every surface that renders an
// event body and knows nothing about this type - the timeline, the console's
// activity view, the TUI - then shows the note itself rather than "a note was
// added", which for this type is the entire content. It is the one entry type
// here whose body is worth reading on its own.
//
// THE ENTRY HANGS OFF THE TODO, which is what makes it reach the people it is
// about: EventFilterSQL gives an event naming an artifact exactly that
// artifact's readers, so whoever can read the row reads every note on it. A
// PROJECTLESS todo cannot make that promise - a projectless event is read back
// by its actor and nobody else - so a note on one is REFUSED rather than written
// somewhere only its writer can see it. That is RecordFindingRun's refusal, for
// RecordFindingRun's reason, and it is the difference between this and the
// status trail: a status also lands on the row, so a personal todo keeps the
// VALUE either way, while a note that nobody but its writer can read is the
// whole content lost silently.
//
// NOTES READ BACK WITH THE ROW. ReadArtifact fills them on a queue item, beside
// the assignee, the category and the raiser, for the reason those three are on
// the row: what a queue is read by has to come back from ONE read, or every
// client decides for itself whether the fact it needs is missing or merely
// somewhere else. A reader who never learns the notes door exists still sees
// what was learned.
//
// ONE LIMIT, INHERITED. The entry is minted - see mintedEventTypes - so it does
// not cross a node boundary in either direction, exactly as an assignment, a
// status move and an edit do not. What that costs here is bigger than it is
// there, because a note is content rather than the record of a value that
// replicates on the row: a peer holds the row and none of its notes. It is
// still right for the reason the rest are minted - the refusals that make an
// entry mean anything are on the verb, and an entry a client could hand over
// would be a note attributed to a seat that never wrote it, on a row that seat
// may not be able to read.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
)

// EventTodoNote is what a note on a row is in the log. It is minted, so the only
// way to get one is to have gone through the verb, which is where the refusals
// are.
const EventTodoNote = "todo.note"

// NoteRoom is where an entry lands when the todo it is about names no room of
// its own. It is StatusRoom's rule, and it exists for the reason that one does:
// an entry nobody can find is an entry nobody reads.
const NoteRoom = "notes"

// NoteEntry is one note: the row it is about, what was learned, who wrote it and
// when.
//
// Note is NOT omitempty. An entry whose text is missing from the answer would
// leave a reader deciding whether nothing was said or whether the node did not
// say it, and for this type the text is the entire entry.
type NoteEntry struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Todo  string `json:"todo"`
	Note  string `json:"note"`
	Actor string `json:"actor"`
	// ActorKind says whether a person or their agent wrote it and ActorUser says
	// which person is behind the seat. Both ride the entry because "the agent
	// that did the work measured this" and "the operator says this" are the two
	// things a reader of a note is telling apart.
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// AppendTodoNote adds a note to a queue item: attributed, timestamped, and
// changing nothing that is already on the row.
//
// The refusals, in the order they are asked, and there are deliberately few:
//
//   - a token that resolves to nobody. An entry carries an actor, and a note
//     nobody wrote is not one.
//   - an empty note. There is nothing to attach and an entry saying so is noise
//     in a log whose whole value is that everything in it was worth writing.
//   - an id that does not name a queue item this principal may READ, answered as
//     an id that is not there - the answer every other queue verb gives, because
//     naming an id here is not a way to find out what else it might be.
//   - a projectless todo. See the head of this file: the note would be readable
//     by whoever wrote it and by nobody else, including the row's author.
//
// It does NOT refuse on status, and it does not take a saw: a note is not
// written against a state, nothing it says can be lost by the row moving under
// it, and the states where it matters most are active and done.
func (d *DB) AppendTodoNote(
	ctx context.Context, p *Principal, todo, note string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "todo.note")
	defer span.End()

	// The seat, by voteActor's rule: an agent is its own party here for the
	// reason it is its own voter. What was learned is learned by the seat that
	// learned it, and p.UserID rides the meta beside it so a reader has both.
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseStatus("this token resolves to nobody, so it cannot write a " +
			"note on a todo")
	}
	text := strings.TrimSpace(note)
	if text == "" {
		return nil, nil, refuseStatus("a note is what was learned about the row, so it cannot " +
			"be empty - say the measurement, the fix shape or what this is blocked on")
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, nil, err
	}
	if art.Project == nil || strings.TrimSpace(*art.Project) == "" {
		return nil, nil, refuseStatus("todo %s has no project and is its owner's alone, so a "+
			"note on it would be readable by whoever wrote the note and by nobody else - not "+
			"even by the row's author. Give the row a project first", art.ID)
	}

	entry, err := noteEntryEvent(art, p, actor, actorKind, text)
	if err != nil {
		return nil, nil, err
	}
	if err := d.AppendEvent(ctx, entry); err != nil {
		return nil, nil, err
	}
	// The row is answered in the shape a read of it would give, which is why the
	// note just written is put on it here: this call did not go back through
	// ReadArtifact, and a client that reads `notes` off the answer would
	// otherwise get the row as it was one entry ago and have no way to tell.
	art.Notes = append(art.Notes, TodoNoteEntryOf(entry))
	span.SetArtifact(art.ID)
	return art, entry, nil
}

// noteEntryEvent builds the entry a note is.
func noteEntryEvent(art *Artifact, p *Principal, actor, actorKind, note string) (*Event, error) {
	meta, err := json.Marshal(map[string]string{
		"actor_kind": actorKind,
		"actor_user": p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: note on %s: %w", art.ID, err)
	}
	return &Event{
		Type:    EventTodoNote,
		Project: art.Project,
		Room:    noteRoom(art),
		Thread:  art.ID,
		// The todo itself, which is what decides who reads the entry: the people
		// who can read the work are the people what was learned about it is for.
		Artifact: art.ID,
		Actor:    actor,
		// The note itself, not a description of one. See the head of this file.
		Body: note,
		Meta: meta,
	}, nil
}

// TodoNoteEntryOf renders one event as the entry it is.
func TodoNoteEntryOf(e *Event) NoteEntry {
	entry := NoteEntry{
		ID: e.ID, Type: e.Type, Todo: e.Artifact, Note: e.Body, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.ActorKind, entry.ActorUser = meta["actor_kind"], meta["actor_user"]
	}
	return entry
}

// TodoNoteLog is every note on this todo that p may read, oldest first - which
// is the order they were learned in, and the order the row's reader wants them
// in. It is TodoEditLog for the other half of the words, with the same
// permission story: the filter is in the WHERE clause and it is not a second
// rule.
func (d *DB) TodoNoteLog(ctx context.Context, p *Principal, todo string) ([]NoteEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "todo.note.log")
	defer span.End()

	events, err := d.todoNoteEvents(ctx, p, []string{strings.TrimSpace(todo)})
	if err != nil {
		return nil, err
	}
	out := make([]NoteEntry, 0, len(events))
	for _, e := range events {
		out = append(out, TodoNoteEntryOf(e))
	}
	return out, nil
}

// fillNotes puts the notes on the rows themselves, beside the body they are
// read under.
//
// It is a QUERY, unlike fillAssignee and its two neighbours, because a note is
// not on the artifact anywhere - it is the log. So it runs in the
// permission-filtered read path only, with its own filter on the events, and one
// query for however many rows it is given rather than one per row.
//
// Only queue items are asked about. A report or a proposal has no note door, so
// asking would be a query per read that can only come back empty.
func (d *DB) fillNotes(ctx context.Context, p *Principal, arts []*Artifact) error {
	ids := make([]string, 0, len(arts))
	byID := make(map[string]*Artifact, len(arts))
	for _, art := range arts {
		if IsQueueItem(art) {
			ids = append(ids, art.ID)
			byID[art.ID] = art
		}
	}
	if len(ids) == 0 {
		return nil
	}
	events, err := d.todoNoteEvents(ctx, p, ids)
	if err != nil {
		return err
	}
	for _, e := range events {
		if art := byID[e.Artifact]; art != nil {
			art.Notes = append(art.Notes, TodoNoteEntryOf(e))
		}
	}
	return nil
}

// todoNoteEvents reads the notes on any of todos, in log order, through the same
// event filter every other read of the log uses.
//
// There is no LIMIT, for todoStatusEvents' reason turned around: that one has
// none because a fold over a prefix is not the state that stands, and this one
// has none because a page that stopped early would drop what was learned last,
// which is usually the part that changed somebody's plan.
func (d *DB) todoNoteEvents(ctx context.Context, p *Principal, todos []string) ([]*Event, error) {
	if len(todos) == 0 {
		return nil, nil
	}
	return readPage(ctx, d, "note events", func(a *args) string {
		idsArg := a.next(pq.Array(todos))
		typeArg := a.next(EventTodoNote)
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ANY(` + idsArg + `) AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// noteRoom is where an entry lands in the log: the room the todo was raised in,
// or the notes room when it was raised in none. It is statusRoom's rule, and it
// exists for the reason that one does.
func noteRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return NoteRoom
}
