package store

// An unfold is how ONE READER chooses to see a thread's replies in the room
// stream. Its twin bookmarks.go answers a different question: a bookmark is a
// pointer to a message a reader wants back later, while an unfold is the shape
// of the stream a reader is standing in.
//
// THE DEFAULT IS FOLDED AND THE STATE IS THE EXCEPTION, which is the whole of
// the operator's answer (01M0WF2T2): the root stays in the stream for everyone,
// replies collapse into it per reader, and a reply addressed to the reader
// surfaces regardless - that last rule is computed at render time from the
// event's addressee, so it costs no state and no door. What needs storing is
// only "this reader asked to see this thread's replies", because a room-wide
// setting would make one reader's preference everybody's visibility.
//
// PRIVATE BY THE FLOOR THAT WAS ALREADY THERE, for bookmarks.go's reason: a
// projectless, roomless event is readable by its author and nobody else, and no
// clause in EventFilterSQL has to learn about it. It carries the THREAD ID and
// nothing copied out of the thread - the transcript is the record, and this log
// is only where the reader's own choice is written down.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// The two entries an unfold leaves in the log. Both are minted - see
// mintedEventTypes - so the only way to get one is through the verb, which is
// where the refusal is: the thread must hold a message the reader can read.
const (
	EventThreadUnfold = "thread.unfold"
	EventThreadFold   = "thread.fold"
)

// UnfoldedField is the meta key holding the id of the thread being unfolded.
const UnfoldedField = "thread"

// UnfoldEntry is one line of a reader's own log: what was unfolded or folded
// back, and when.
type UnfoldEntry struct {
	Thread string `json:"thread"`
	Verb   string `json:"verb"`
	At     string `json:"at"`
	Event  string `json:"event"`
}

// UnfoldEntryOf renders one event as the entry it is.
func UnfoldEntryOf(e *Event) UnfoldEntry {
	entry := UnfoldEntry{
		Verb:  e.Type,
		At:    e.Created.UTC().Format(time.RFC3339Nano),
		Event: e.ID,
	}
	var meta struct {
		Thread string `json:"thread"`
	}
	if len(e.Meta) > 0 {
		_ = json.Unmarshal(e.Meta, &meta)
	}
	entry.Thread = meta.Thread
	return entry
}

// LiveUnfolded folds a reader's log into the set of threads they have
// unfolded. NEWEST FIRST, for LiveBookmarks' reason: the thread a reader
// unfolded a minute ago is the one they are most likely reading. Last write
// wins per thread, so unfolding twice is harmless and a fold followed by an
// unfold is an unfold.
func LiveUnfolded(entries []UnfoldEntry) []string {
	open := map[string]bool{}
	order := []string{}
	for _, e := range entries {
		if e.Thread == "" {
			continue
		}
		add := e.Verb == EventThreadUnfold
		if add {
			for i, id := range order {
				if id == e.Thread {
					order = append(order[:i], order[i+1:]...)
					break
				}
			}
			order = append(order, e.Thread)
		}
		open[e.Thread] = add
	}
	out := make([]string, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		if open[order[i]] {
			out = append(out, order[i])
		}
	}
	return out
}

// UnfoldError is a refusal, said in words a caller can hand to a person.
type UnfoldError struct{ Why string }

func (e UnfoldError) Error() string { return e.Why }

func refuseUnfold(format string, args ...any) error {
	return UnfoldError{Why: fmt.Sprintf(format, args...)}
}

// Unfold records that the principal reading wants this thread's replies drawn
// in the room stream.
func (d *DB) Unfold(ctx context.Context, p *Principal, thread string) (*Event, error) {
	return d.writeUnfold(ctx, p, EventThreadUnfold, thread)
}

// Fold records the opposite: the replies collapse into the thread's head row
// again. A new entry rather than a deletion, for the reason Unbookmark is one:
// the log is the record.
func (d *DB) Fold(ctx context.Context, p *Principal, thread string) (*Event, error) {
	return d.writeUnfold(ctx, p, EventThreadFold, thread)
}

func (d *DB) writeUnfold(ctx context.Context, p *Principal, verb, thread string) (*Event, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, refuseUnfold("this token resolves to nobody, so it cannot unfold anything")
	}
	thread = strings.TrimSpace(thread)
	if thread == "" {
		return nil, refuseUnfold("an unfold names a thread")
	}

	// READABLE, and that is the only rule - the same one bookmarks.go keeps
	// for a message. There is no room check: this state has exactly one
	// reader, the one who just proved they can read the thread, and a thread
	// nobody can read has nothing for the state to act on.
	//
	// "Nothing here" arrives as an empty page for a thread that holds nothing
	// the filter admits. A reader is told the same thing either way: the
	// state is inert without messages to fold, and the refusal keeps the log
	// from filling with ids that name nothing.
	readable, err := d.ListEvents(ctx, p, EventQuery{
		Type:     ChatEventType,
		Thread:   thread,
		ScopeAll: true,
		Limit:    1,
	})
	if err != nil {
		return nil, err
	}
	if len(readable) == 0 {
		return nil, refuseUnfold("no message in thread %s that you can read, so there is nothing to unfold", thread)
	}

	meta, err := json.Marshal(map[string]string{UnfoldedField: thread})
	if err != nil {
		return nil, fmt.Errorf("store: unfold meta: %w", err)
	}
	// NO PROJECT AND NO ROOM, which is what makes it private - see the head
	// comment. Copying the thread's project here would put this row in front
	// of everyone who can read that project.
	e := &Event{Type: verb, Actor: actor, Meta: meta}
	if err := d.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// UnfoldedLog is every entry this reader has written, oldest first.
func (d *DB) UnfoldedLog(ctx context.Context, p *Principal) ([]UnfoldEntry, error) {
	events, err := d.unfoldEvents(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]UnfoldEntry, 0, len(events))
	for _, e := range events {
		out = append(out, UnfoldEntryOf(e))
	}
	return out, nil
}

// UnfoldedOf is the threads this reader has unfolded, newest first.
func (d *DB) UnfoldedOf(ctx context.Context, p *Principal) ([]string, error) {
	entries, err := d.UnfoldedLog(ctx, p)
	if err != nil {
		return nil, err
	}
	return LiveUnfolded(entries), nil
}

// unfoldEvents reads this principal's own entries, both the actor match and
// the permission filter, for bookmarkEvents' reason: the filter is what makes
// the row unreadable to anybody else, and the actor match is what makes this
// list THIS READER'S.
func (d *DB) unfoldEvents(ctx context.Context, p *Principal) ([]*Event, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, nil
	}
	return readPage(ctx, d, "unfold events", func(a *args) string {
		actorArg := a.next(actor)
		typesArg := a.next(pq.Array([]string{EventThreadUnfold, EventThreadFold}))
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.actor = ` + actorArg + ` AND e.type = ANY(` + typesArg + `)
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}
