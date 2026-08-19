package store

// A reaction is an ack that does not cost a message.
//
// Every agent in this fleet pays a whole line to say "seen", and four agents
// acking one decision is four lines nobody needed to read. A reaction is the
// same signal at the size it deserves: one emoji, on one message, by one
// principal, and the room stays readable.
//
// It is an EVENT, for the reason a pin is one - who acked and when is the fact
// worth keeping, and a counter on a row cannot answer either - and it carries
// the message it is about in PARENTS rather than in meta. Two things follow
// from that and both are the point. The DAG is where this fabric already keeps
// "descends from", so a reaction is not a new kind of edge; and every door that
// writes an event already refuses parents the writer cannot read, so the one
// refusal that matters is enforced on the raw events door as well as on the
// verb. A rule that only the verb applies is a rule the next door forgets.
//
// NOT MINTED, unlike a pin, and the difference is what an ack is. A minted type
// is one a handler of this node writes and no peer may hand over - which is
// right for a claim about this node and wrong for a claim about a person, since
// an agent on another node acks the same message from where it sits. The
// refusal a pin gets from minting, this gets from parents.
//
// Retraction is a second event, never a deletion. A decision that was acked and
// then un-acked is a different history from one nobody ever acked, and the log
// is the record.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lib/pq"
)

// The two entries a reaction leaves in the log.
const (
	EventReactionAdd    = "reaction.add"
	EventReactionRemove = "reaction.remove"
)

// MaxReactionRunes is the ceiling on the emoji itself.
//
// It is not decoration and it is not a guess at how long an emoji is. Without a
// cap, `reaction` is a second chat with no length limit and no renderer willing
// to say so: a body of ten kilobytes arrives, the room draws it beside a
// message as though it were a face, and every reader of that room pays for it.
// Eight runes is past the longest emoji anybody actually sends - a family
// sequence with skin tones is seven - and far short of a sentence.
const MaxReactionRunes = 8

// ReactionError is a refusal to react, in words a caller can hand to a person.
type ReactionError struct{ Why string }

func (e ReactionError) Error() string { return e.Why }

func refuseReaction(format string, args ...any) error {
	return ReactionError{Why: fmt.Sprintf(format, args...)}
}

// ReactionBodyRefusal answers why a reaction event's body is not one, or "" when
// it is.
//
// It is a function rather than a check inside the verb because a reaction
// arrives at three doors: the verb below, POST /api/events, and a replicated
// delta from a peer. The verb is the only one of the three this node controls,
// and a rule the other two do not apply is a rule a hostile peer and a
// hand-written POST both walk past. So it is stated once and asked at each.
func ReactionBodyRefusal(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "a reaction is an emoji and this one is empty"
	}
	if !utf8.ValidString(trimmed) {
		return "a reaction is text and this one is not valid utf-8"
	}
	if n := utf8.RuneCountInString(trimmed); n > MaxReactionRunes {
		return fmt.Sprintf("a reaction is %d runes and the ceiling is %d - "+
			"say it in the room if it needs a sentence", n, MaxReactionRunes)
	}
	if strings.ContainsAny(trimmed, "\n\r\t") {
		return "a reaction is one glyph on one line"
	}
	return ""
}

// Reaction is one emoji on one message and everybody who put it there.
//
// Actors rather than a count, and the noun is load-bearing: "3" answers how
// many and never who, and in a room of four agents who is the entire signal -
// an ack from the seat that has to act is worth more than three from seats that
// do not. The console draws the count and names them on hover; both readings
// come off this.
type Reaction struct {
	Emoji  string   `json:"emoji"`
	Actors []string `json:"actors"`
	// Mine says this reader is one of them, so a console can draw the control
	// as pressed without having to know its own principal id.
	Mine bool `json:"mine"`
}

// React puts an emoji on a message, or takes this principal's own off it.
func (d *DB) React(
	ctx context.Context, p *Principal, room, message, emoji string, on bool,
) (*Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, refuseReaction("this token resolves to nobody, so it cannot react to anything")
	}
	room, message = strings.TrimSpace(room), strings.TrimSpace(message)
	emoji = strings.TrimSpace(emoji)
	if room == "" || message == "" {
		return nil, refuseReaction("a reaction names a room and a message in it")
	}
	if why := ReactionBodyRefusal(emoji); why != "" {
		return nil, refuseReaction("%s", why)
	}

	// READABLE, AND IN THIS ROOM - the same two refusals a pin gets and for the
	// same reasons. The first is ordinary; the second keeps a room's reactions
	// answerable from that room's log, so a reader is never shown an ack on a
	// message they cannot open.
	// ErrNotFound AND nil, because "nothing here" arrives as both: an id
	// nothing answers to is ErrNotFound, and a row the filter hides is nil. A
	// reader is told the same thing either way, which is the point - saying
	// which would be telling them a message exists that they may not see.
	//
	// Returning the store error unchanged made this a 500, so a caller with a
	// stale id was told the node was broken and invited to retry the one thing
	// that will never work. Measured against a running node; `flowy identity
	// pin`'s sibling path has the same shape and is refused the same way now.
	source, err := d.ReadEvent(ctx, p, message)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if source == nil || errors.Is(err, ErrNotFound) {
		return nil, refuseReaction(
			"no message %s that you can read, so there is nothing to react to", message)
	}
	if source.Room != room {
		return nil, refuseReaction("message %s was said in %q, not in %q - a reaction belongs "+
			"to the room the message is in", message, source.Room, room)
	}

	verb := EventReactionAdd
	if !on {
		verb = EventReactionRemove
	}
	// The speaker's kind, stamped the way every other minted entry stamps it,
	// so a console can tell an agent's ack from its user's without a second
	// lookup per reaction.
	meta, err := json.Marshal(map[string]string{"actor_kind": actorKind})
	if err != nil {
		return nil, fmt.Errorf("store: reaction meta: %w", err)
	}
	e := &Event{
		Type:    verb,
		Project: source.Project,
		Room:    room,
		Thread:  source.Thread,
		Parents: []string{message},
		Actor:   actor,
		Body:    emoji,
		Meta:    meta,
	}
	if err := d.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// ReactionsOn is what is on each of these messages, as this reader sees it.
//
// One query for a whole page rather than one per message: a room read of fifty
// messages that asked fifty times would make the reaction the most expensive
// thing on the screen, and the cheapest signal in the room is the one that must
// not be.
//
// It reads through the ordinary event filter, so a reaction by somebody in a
// project this reader is not in is not visible here - which is the same rule
// that decides whether they can see the message at all.
func (d *DB) ReactionsOn(
	ctx context.Context, p *Principal, messages []string,
) (map[string][]Reaction, error) {
	ids := make([]string, 0, len(messages))
	seen := map[string]bool{}
	for _, id := range messages {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return map[string][]Reaction{}, nil
	}

	events, err := readPage(ctx, d, "reaction events", func(a *args) string {
		idsArg := a.next(pq.Array(ids))
		typesArg := a.next(pq.Array([]string{EventReactionAdd, EventReactionRemove}))
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.type = ANY(` + typesArg + `) AND e.parents && ` + idsArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
	if err != nil {
		return nil, err
	}
	return foldReactions(events, ids, p), nil
}

// foldReactions turns the log into what is on the messages now.
//
// LAST WRITE WINS PER (message, actor, emoji), by the reading the row carries
// rather than by the order rows came back, because replication brings a peer's
// reactions in whenever it brings them and the answer must not depend on that.
// It is the same fold LivePins does one key wider, and the same reason: a
// re-react after a retraction has to work, and a duplicate has to be harmless.
//
// The ORDER of the emoji on a message is the order each was FIRST put there, so
// a row of reactions does not reshuffle under a reader when somebody adds a
// second of one that is already up.
func foldReactions(events []*Event, wanted []string, p *Principal) map[string][]Reaction {
	want := map[string]bool{}
	for _, id := range wanted {
		want[id] = true
	}
	type key struct{ message, emoji, actor string }
	on := map[key]bool{}
	order := map[string][]string{}
	seenEmoji := map[string]bool{}
	actorOrder := map[string][]string{}
	seenActor := map[string]bool{}

	for _, e := range events {
		emoji := strings.TrimSpace(e.Body)
		if emoji == "" || e.Actor == "" {
			continue
		}
		for _, message := range e.Parents {
			if !want[message] {
				continue
			}
			if !seenEmoji[message+"\x00"+emoji] {
				seenEmoji[message+"\x00"+emoji] = true
				order[message] = append(order[message], emoji)
			}
			ak := message + "\x00" + emoji + "\x00" + e.Actor
			if !seenActor[ak] {
				seenActor[ak] = true
				actorOrder[message+"\x00"+emoji] = append(actorOrder[message+"\x00"+emoji], e.Actor)
			}
			on[key{message, emoji, e.Actor}] = e.Type == EventReactionAdd
		}
	}

	me, _ := voteActor(p)
	out := map[string][]Reaction{}
	for message, emojis := range order {
		for _, emoji := range emojis {
			r := Reaction{Emoji: emoji}
			for _, actor := range actorOrder[message+"\x00"+emoji] {
				if !on[key{message, emoji, actor}] {
					continue
				}
				r.Actors = append(r.Actors, actor)
				if actor != "" && actor == me {
					r.Mine = true
				}
			}
			// An emoji everybody has taken back is not drawn. The log still
			// holds both entries, which is the record; the room shows what is
			// on the message now.
			if len(r.Actors) > 0 {
				out[message] = append(out[message], r)
			}
		}
	}
	return out
}
