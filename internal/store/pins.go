package store

// A pin is how a room remembers its decisions.
//
// The room is a log and the log is long: by the time somebody asks "what did we
// agree about the waiter", the answer is four hundred messages back and nobody
// is going to scroll for it. A pin says THIS ONE, and the room view keeps it
// where a new reader lands.
//
// It is an EVENT rather than a column on the message, for the reason an edge in
// the queue is an event: who pinned it and when is the fact worth keeping, and a
// boolean on a row cannot answer either. It also means a pin is subject to the
// same visibility rules as everything else here - a reader who cannot see the
// message cannot see that it was pinned, because the pin carries the message id
// and nothing copied out of it.
//
// Room-scoped, not global: pinning is a claim about one conversation. The same
// message cited into another room is not pinned there, and a room's strip is
// answerable from that room's log alone.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// The two entries a pin leaves in the log. Both are minted - see
// mintedEventTypes - so the only way to get one is to have gone through the
// verb, which is where the refusals are: the message must exist, be readable,
// and be in the room being pinned.
const (
	EventPinAdd    = "pin.add"
	EventPinRemove = "pin.remove"
)

// PinnedField is the meta key holding the id of the message being pinned.
//
// The id and nothing else. A copy of the body here would be a second, stale
// copy of what somebody said, and it would be readable by anyone who can read
// the pin - which is the same leak the blocker field refuses for the same
// reason. The strip reads the message itself, through the same filter every
// other reader goes through.
const PinnedField = "pinned"

// PinEntry is one line of the log behind a room's strip: what was pinned, by
// whom, when, and whether this entry put it up or took it down.
type PinEntry struct {
	Message string `json:"message"`
	Verb    string `json:"verb"`
	Actor   string `json:"actor"`
	Kind    string `json:"actor_kind,omitempty"`
	At      string `json:"at"`
	Event   string `json:"event"`
}

// PinEntryOf renders one event as the entry it is.
func PinEntryOf(e *Event) PinEntry {
	entry := PinEntry{
		Verb:  e.Type,
		Actor: e.Actor,
		At:    e.Created.UTC().Format(time.RFC3339Nano),
		Event: e.ID,
	}
	var meta struct {
		Pinned    string `json:"pinned"`
		ActorKind string `json:"actor_kind"`
	}
	if len(e.Meta) > 0 {
		_ = json.Unmarshal(e.Meta, &meta)
	}
	entry.Message = meta.Pinned
	entry.Kind = meta.ActorKind
	return entry
}

// LivePins folds a room's pin log into the set that is up now, oldest first.
//
// Last write wins per message, which is what makes unpin-then-pin-again work
// and what makes a duplicate pin harmless. The ORDER is the order each message
// was FIRST pinned, not the order of the last event about it: a strip that
// reshuffles itself when somebody re-pins an old decision is a strip nobody can
// keep their place in.
func LivePins(entries []PinEntry) []string {
	up := map[string]bool{}
	order := []string{}
	for _, e := range entries {
		if e.Message == "" {
			continue
		}
		if _, seen := up[e.Message]; !seen {
			order = append(order, e.Message)
		}
		up[e.Message] = e.Verb == EventPinAdd
	}
	out := make([]string, 0, len(order))
	for _, id := range order {
		if up[id] {
			out = append(out, id)
		}
	}
	return out
}

// PinError is a refusal to pin, said in words a caller can hand to a person.
type PinError struct{ Why string }

func (e PinError) Error() string { return e.Why }

func refusePin(format string, args ...any) error {
	return PinError{Why: fmt.Sprintf(format, args...)}
}

// Pin puts a message up in the room it was said in.
func (d *DB) Pin(ctx context.Context, p *Principal, room, message string) (*Event, error) {
	return d.writePin(ctx, p, EventPinAdd, room, message)
}

// Unpin takes it down. It is a new entry rather than a deletion, because the
// log is the record: a decision that was pinned for a day and then taken down
// is a different history from one that was never pinned.
func (d *DB) Unpin(ctx context.Context, p *Principal, room, message string) (*Event, error) {
	return d.writePin(ctx, p, EventPinRemove, room, message)
}

func (d *DB) writePin(ctx context.Context, p *Principal, verb, room, message string) (*Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, refusePin("this token resolves to nobody, so it cannot pin anything")
	}
	room, message = strings.TrimSpace(room), strings.TrimSpace(message)
	if room == "" || message == "" {
		return nil, refusePin("a pin names a room and a message in it")
	}

	// READABLE, AND IN THIS ROOM. The first refusal is the ordinary one; the
	// second is what makes a pin room-scoped rather than a global flag wearing
	// a room's name. Pinning a message from another room into this one would
	// put a line in this strip that this room's readers may not be able to
	// open.
	source, err := d.ReadEvent(ctx, p, message)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, refusePin("no message %s that you can read, so there is nothing to pin", message)
	}
	if source.Room != room {
		return nil, refusePin("message %s was said in %q, not in %q - a pin belongs to the room "+
			"the message is in", message, source.Room, room)
	}

	meta, err := json.Marshal(map[string]string{
		PinnedField:  message,
		"actor_kind": actorKind,
	})
	if err != nil {
		return nil, fmt.Errorf("store: pin meta: %w", err)
	}
	e := &Event{
		Type:    verb,
		Project: source.Project,
		Room:    room,
		Actor:   actor,
		Meta:    meta,
	}
	if err := d.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// PinLog is every pin entry for a room, oldest first, as this reader sees it.
func (d *DB) PinLog(ctx context.Context, p *Principal, room string) ([]PinEntry, error) {
	events, err := d.pinEvents(ctx, p, room)
	if err != nil {
		return nil, err
	}
	out := make([]PinEntry, 0, len(events))
	for _, e := range events {
		out = append(out, PinEntryOf(e))
	}
	return out, nil
}

// PinnedIn is the ids that are up in a room now.
func (d *DB) PinnedIn(ctx context.Context, p *Principal, room string) ([]string, error) {
	entries, err := d.PinLog(ctx, p, room)
	if err != nil {
		return nil, err
	}
	return LivePins(entries), nil
}

func (d *DB) pinEvents(ctx context.Context, p *Principal, room string) ([]*Event, error) {
	if strings.TrimSpace(room) == "" {
		return nil, nil
	}
	return readPage(ctx, d, "pin events", func(a *args) string {
		roomArg := a.next(room)
		typesArg := a.next(pq.Array([]string{EventPinAdd, EventPinRemove}))
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.room = ` + roomArg + ` AND e.type = ANY(` + typesArg + `)
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}
