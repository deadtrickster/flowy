package store

// A bookmark is how ONE READER remembers a message.
//
// The room already has pins - see pins.go - and they answer a different
// question. A pin is the room saying "this is what we decided": everybody sees
// it, everybody's strip changes, and putting one up is a claim about the
// conversation. Somebody who wants to find their own way back to a message
// tomorrow has no business rearranging what four other seats see, so they had
// nothing, and the operator asked for this by name (01M0HGTV9B).
//
// PRIVATE BY THE FLOOR THAT WAS ALREADY THERE, not by a new rule. perm.go's
// comment on direct messages says it plainly: "a projectless event is already
// readable by its author and nobody else". So a bookmark is a projectless,
// roomless event, and no clause anywhere in EventFilterSQL had to learn about
// it. That is deliberate and it is the whole design: a per-row privacy flag
// would be a value every future branch of the filter has to remember to
// consult, and the one that forgets is a leak.
//
// It carries the MESSAGE ID and nothing copied out of the message, for pins.go's
// reason: a body copied here would be a second, stale copy of what somebody
// said. The list reads each message back through the ordinary filter, so a
// message that stops being readable stops being in the list - a bookmark is a
// note to self about where something is, never a way to keep a copy of it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// The two entries a bookmark leaves in the log. Both are minted - see
// mintedEventTypes - so the only way to get one is through the verb, which is
// where the refusal is: the message must exist and be readable by whoever is
// keeping it.
const (
	EventBookmarkAdd    = "bookmark.add"
	EventBookmarkRemove = "bookmark.remove"
)

// BookmarkedField is the meta key holding the id of the message being kept.
const BookmarkedField = "bookmarked"

// BookmarkEntry is one line of a reader's own log: what was kept or dropped,
// and when.
type BookmarkEntry struct {
	Message string `json:"message"`
	Verb    string `json:"verb"`
	At      string `json:"at"`
	Event   string `json:"event"`
}

// BookmarkEntryOf renders one event as the entry it is.
func BookmarkEntryOf(e *Event) BookmarkEntry {
	entry := BookmarkEntry{
		Verb:  e.Type,
		At:    e.Created.UTC().Format(time.RFC3339Nano),
		Event: e.ID,
	}
	var meta struct {
		Bookmarked string `json:"bookmarked"`
	}
	if len(e.Meta) > 0 {
		_ = json.Unmarshal(e.Meta, &meta)
	}
	entry.Message = meta.Bookmarked
	return entry
}

// LiveBookmarks folds a reader's log into the set they are keeping now.
//
// NEWEST FIRST, which is where this parts company with LivePins and the reason
// is what each list is for. A room's strip is a place a reader keeps their
// place, so it holds its order. A reader's own list is a pile they came back
// to, and the thing they kept a minute ago is the thing they are looking for.
//
// ORDERED BY THE LAST TIME IT WAS KEPT, not by the first. The first version of
// this recorded a message's position when it was first seen, so dropping one
// and keeping it again left it wherever it had been - which contradicts the
// paragraph above in the one case where somebody has plainly just said "this
// one, again". Found in review by claude-host, and it is my own stated rule
// that it broke.
//
// Last write wins per message, so keeping twice is harmless and a drop followed
// by a keep is a keep.
func LiveBookmarks(entries []BookmarkEntry) []string {
	kept := map[string]bool{}
	order := []string{}
	for _, e := range entries {
		if e.Message == "" {
			continue
		}
		add := e.Verb == EventBookmarkAdd
		// An ADD moves it to the end of the order, whether or not it was
		// already there. A REMOVE leaves the position alone: it is not kept, so
		// the position is not read - and if it comes back, the add that brings
		// it back is what places it.
		if add {
			for i, id := range order {
				if id == e.Message {
					order = append(order[:i], order[i+1:]...)
					break
				}
			}
			order = append(order, e.Message)
		}
		kept[e.Message] = add
	}
	out := make([]string, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		if kept[order[i]] {
			out = append(out, order[i])
		}
	}
	return out
}

// BookmarkError is a refusal, said in words a caller can hand to a person.
type BookmarkError struct{ Why string }

func (e BookmarkError) Error() string { return e.Why }

func refuseBookmark(format string, args ...any) error {
	return BookmarkError{Why: fmt.Sprintf(format, args...)}
}

// Bookmark keeps a message for the principal reading.
func (d *DB) Bookmark(ctx context.Context, p *Principal, message string) (*Event, error) {
	return d.writeBookmark(ctx, p, EventBookmarkAdd, message)
}

// Unbookmark drops it. A new entry rather than a deletion, for the reason Unpin
// is one: the log is the record.
func (d *DB) Unbookmark(ctx context.Context, p *Principal, message string) (*Event, error) {
	return d.writeBookmark(ctx, p, EventBookmarkRemove, message)
}

func (d *DB) writeBookmark(ctx context.Context, p *Principal, verb, message string) (*Event, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, refuseBookmark("this token resolves to nobody, so it cannot keep anything")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, refuseBookmark("a bookmark names a message")
	}

	// READABLE, and that is the only rule. There is no room check here and
	// pins.go's has no counterpart: a pin puts a line in a strip other readers
	// see, so it has to belong to the room they are reading, while this list
	// has exactly one reader and it is the one who just proved they can read
	// the message.
	//
	// "Nothing here" arrives as ErrNotFound for an id nothing answers to and as
	// nil for a row the filter hides. A reader is told the same thing either
	// way, deliberately: telling them apart would answer "does this id exist"
	// for rows they may not read.
	source, err := d.ReadEvent(ctx, p, message)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if source == nil || errors.Is(err, ErrNotFound) {
		return nil, refuseBookmark("no message %s that you can read, so there is nothing to keep", message)
	}

	meta, err := json.Marshal(map[string]string{BookmarkedField: message})
	if err != nil {
		return nil, fmt.Errorf("store: bookmark meta: %w", err)
	}
	// NO PROJECT AND NO ROOM, which is what makes it private - see the head
	// comment. Copying source.Project here, as writePin does, would put this
	// row in front of everyone who can read that project.
	e := &Event{Type: verb, Actor: actor, Meta: meta}
	if err := d.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// BookmarkLog is every entry this reader has written, oldest first.
func (d *DB) BookmarkLog(ctx context.Context, p *Principal) ([]BookmarkEntry, error) {
	events, err := d.bookmarkEvents(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]BookmarkEntry, 0, len(events))
	for _, e := range events {
		out = append(out, BookmarkEntryOf(e))
	}
	return out, nil
}

// BookmarksOf is the ids this reader is keeping now, newest first.
func (d *DB) BookmarksOf(ctx context.Context, p *Principal) ([]string, error) {
	entries, err := d.BookmarkLog(ctx, p)
	if err != nil {
		return nil, err
	}
	return LiveBookmarks(entries), nil
}

// bookmarkEvents reads this principal's own entries.
//
// BOTH the actor match and the permission filter, and neither is redundant.
// The filter is what makes the row unreadable to anybody else; the actor match
// is what makes this list THIS READER'S rather than "every bookmark I am
// allowed to see", which today is the same set and is not the same sentence. A
// query whose correctness rests on two clauses agreeing is one where dropping
// either is a silent widening.
func (d *DB) bookmarkEvents(ctx context.Context, p *Principal) ([]*Event, error) {
	actor, _ := voteActor(p)
	if actor == "" {
		return nil, nil
	}
	return readPage(ctx, d, "bookmark events", func(a *args) string {
		actorArg := a.next(actor)
		typesArg := a.next(pq.Array([]string{EventBookmarkAdd, EventBookmarkRemove}))
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.actor = ` + actorArg + ` AND e.type = ANY(` + typesArg + `)
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}
