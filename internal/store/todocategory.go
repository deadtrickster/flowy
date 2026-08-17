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

// WHAT KIND OF WORK A TODO IS, and who said so.
//
// The queue has always had free labels on it - tags, many per item, no schema,
// anybody's word for anything - and they are the right shape for what they are.
// What they cannot do is be COUNTED or ROUTED. "how much of this queue is bugs"
// over a tag column answers whatever the last agent felt like typing: bug, bugs,
// Bug, defect, broken. Every one of those reads as a different population, none
// of them is wrong, and nothing downstream can act on the set.
//
// So a todo also carries ONE value out of a CLOSED set, and the whole of what
// makes it worth having is that the set is closed: an unknown word is REFUSED
// and not stored. A vocabulary that accepts anything is tags with extra steps.
//
// IT IS CALLED category HERE AND "Kind" ON SCREEN, and that is deliberate. A
// todo already IS kind=todo one level up - Artifact.Kind is what says this row is
// a queue item rather than a note or a handoff - so a second field called kind
// would be the same word meaning two things on one row. That defect cost this
// room three separate misreadings in one day (status at the top level against
// assignee down in fields; a reader-name against a principal), every one of them
// a read that SUCCEEDED and was about the wrong thing. The operator never types
// the internal name, so the console labels it "Kind" and the store keeps a word
// that can only mean one thing.
//
// The overlap that remains is harmless for the same reason: a memory item of
// kind=feature carrying category=chore is a queue item somebody filed as a
// feature and classified as a chore, and neither word had to be reinterpreted to
// read that sentence.
//
// ABSENT IS A VALUE. The whole queue predates this field and none of it is
// backfilled: an unclassified todo reads, lists, sorts and drains exactly as it
// did yesterday, and CategoryOf answers "" for it. Nothing refuses a row for
// having no category, and nothing invents one - guessing bug from a title is how
// a count becomes fiction. Setting one to empty is likewise allowed and is how a
// misclassification is taken back; it is not an unknown word, it is the value the
// rest of the queue already has.
//
// READ PERMISSION IS THE BAR, AND THERE IS NO SECOND ONE. Whoever can read a
// todo may classify it. That is AssignTodo's rule and SetTodoStatus's rule, and
// it is right here for their reason: WHAT KIND OF WORK THIS IS, IS A CLAIM ABOUT
// THE WORK AND NOT ABOUT THE AUTHOR'S WORDS. The agent that opened the row and
// found a bug underneath is the one in a position to say so, and it is routinely
// not whoever typed the title. It hands the classifier nothing - the permission
// filter has never looked at this key - so the widest it reaches is "whoever can
// see the work can say what kind of work it is", and a principal who cannot see
// it gets the answer a read of it would give. No new grant and no new column, for
// the reason there is none behind an assignee: a permission layer over something
// that grants nothing is a layer to maintain and nothing to protect.
//
// TITLE AND BODY STAY THE AUTHOR'S, untouched. Only the queue metadata changes
// hands - who is carrying it, where it has got to, and now what kind of thing it
// is. mem_write still refuses a stranger rewriting the words, loudly.
//
// IT SITS WHERE THE OTHER TWO SIT. The value rides fields and is lifted onto the
// row at read time by fillCategory, beside Status and Assignee, so ONE read
// answers all three. That is e891944's finding and it is not re-litigated here:
// queue facts kept in two shapes are read wrong by clients that roll their own
// accessor, and every one of those reads looks like a success.
//
// A CLASSIFICATION IS AN EVENT, and the value on the row is the head of it. Same
// shape as an assignment and a status move, for their reason: the column records
// THAT it changed, and the question worth asking is who called this a bug, and
// when - because a reclassification is an argument somebody had. The entry names
// the todo, the category and the one it came from, is signed, and appends. Value
// and entry go in one transaction under one clock reading, so the row and the
// fold cannot disagree.
//
// ONE LIMIT, INHERITED. The entry is minted - see mintedEventTypes - so it does
// not cross a node boundary, exactly as a dep edge, a vote, an assignment and a
// status move do not. A peer holds the category (it is on the row, and rows
// replicate) and none of the entries behind it, so "who called it a bug" is a
// question answered on the node it was called one on.

// EventTodoCategory is what a classification is in the log. It is minted, so the
// only way to get one is to have gone through the verb, which is where the
// refusal is.
const EventTodoCategory = "todo.category"

// CategoryRoom is where an entry lands when the todo it is about names no room
// of its own. It is AssignRoom's and StatusRoom's rule for the third piece of
// queue metadata: an entry nobody can find is an entry nobody reads.
const CategoryRoom = "category"

// CategoryField is what kind of work a todo is, in fields beside the room, the
// assignee and the message it was raised out of.
//
// It rides fields for the reason those do, and with the same consequence: the
// permission filter never looks at this key, so classifying an item hands nobody
// anything and takes nothing away.
//
// A key that is present and empty and a key that is not there read the same -
// unclassified - because unlike an assignee there is no second place to look and
// so nothing for a present-but-empty key to have to outrank. The distinction
// AssigneeOf keeps exists to beat a stale OWNER line in the body; there is no
// body convention for this and there is not going to be one.
const CategoryField = "category"

// The ontology. FOUR WORDS, and the bar for a fifth is that somebody can say
// what the system would DO differently with it - route it, count it, pick a
// reviewer - because a word with no processing behind it is a tag, and tags
// already exist and are better at being tags.
//
// They divide the queue by what the work IS, which is the division a reader
// makes anyway when deciding what to pick up:
//
//   - bug: something is broken and was not meant to be. The distinction that
//     earns its place - it is the one an operator scans for first.
//   - feature: something new that did not exist.
//   - chore: work that has to happen and changes nothing anybody asked for -
//     upgrades, cleanups, migrations, the gate.
//   - question: it is not yet known what the work is. It ends in an answer and
//     usually in another todo, and a queue that cannot say this files unanswered
//     questions as features and then wonders why they never ship.
const (
	CategoryBug      = "bug"
	CategoryFeature  = "feature"
	CategoryChore    = "chore"
	CategoryQuestion = "question"
)

// TodoCategories is the whole vocabulary, in the order a reader scans it: what
// is broken first, then what is new, then what merely has to happen, then what
// is not yet a piece of work at all. An error message listing them reads the way
// the console draws them.
var TodoCategories = []string{CategoryBug, CategoryFeature, CategoryChore, CategoryQuestion}

// CategoryEntry is one entry in the log behind a classification: the todo, what
// it was called, what it was called before, who said so and when.
//
// It is StatusEntry's shape for StatusEntry's reason - a reclassification does
// not erase the one before it, it appends the fact that somebody disagreed.
type CategoryEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Todo string `json:"todo"`
	// Category is NOT omitempty, and neither is From: an entry that left one out
	// would leave a client deciding whether it means unclassified or means the
	// node did not say, which is the two-words-for-one-state problem nobodyWords
	// exists to stop.
	Category string `json:"category"`
	// From is what it was, and is empty for a todo that had no category - which
	// is every todo raised before this field, so it is the ordinary first move.
	From      string `json:"from"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// TodoCategoryStanding is the fold of that log: what the todo is called, and the
// entry that called it that.
//
// Latest wins, over seq_hlc, so two seats classifying the same todo converge on
// one answer whichever order the entries were read in. It is Assignment's and
// TodoState's half of the design - a READING of the log rather than a second
// stored copy of it.
type TodoCategoryStanding struct {
	Category string `json:"category"`
	From     string `json:"from"`
	// By is the seat that made the call, and ByUser the person behind it. Both
	// are on the answer because "the agent that opened it found a bug" and "the
	// operator filed it as a chore" are the two things a reader is telling apart.
	By     string `json:"by"`
	ByKind string `json:"by_kind,omitempty"`
	ByUser string `json:"by_user,omitempty"`
	At     string `json:"at"`
	Entry  string `json:"entry"`
}

// categoryRefusalError is what every refusal this verb makes ABOUT THE
// CLASSIFICATION IT WAS ASKED FOR satisfies: the caller's mistake, and fixable
// by the caller.
//
// It is DepRefusal's interface rather than a fourth one, so that a refusal added
// to any queue verb cannot be one that HTTP maps to 400 and MCP reports as a
// broken node. NotATodoError is deliberately not one of them here either: an id
// out of reach is answered as an id that is not there.
type categoryRefusalError struct{ reason string }

func (e categoryRefusalError) Error() string { return e.reason }
func (e categoryRefusalError) depRefusal()   {}

func refuseCategory(format string, a ...any) error {
	return categoryRefusalError{reason: fmt.Sprintf(format, a...)}
}

// NormalizeTodoCategory validates the category a write asks for and returns it
// as the node stores it.
//
// THE REFUSAL IS THE FEATURE. Anything outside the vocabulary comes back as an
// error and is not stored, wherever it arrived from - a set that quietly accepts
// "defect" holds two words for one population, and the count that was the reason
// for having a closed set at all is then wrong in a way nobody can see. It is
// NormalizeTodoStatus's rule, and it is here rather than beside a door because
// every door calls it: HTTP, the memory tools, and the verb itself.
//
// Case and surrounding space are the caller's typing rather than a different
// category, so they come off: a queue holding "Bug" beside "bug" is the same
// split one keystroke down.
//
// EMPTY IS ACCEPTED and means unclassified. It is not an unknown word - it is
// the value every todo written before this field has - and it is how somebody
// takes back a classification they got wrong, which has to be possible or the
// first careless write is permanent.
func NormalizeTodoCategory(asked string) (string, error) {
	category := strings.ToLower(strings.TrimSpace(asked))
	if category == "" {
		return "", nil
	}
	for _, known := range TodoCategories {
		if category == known {
			return category, nil
		}
	}
	return "", refuseCategory("%q is not a kind of work this queue has: one of %s, "+
		"or empty for unclassified. It is a CLOSED set on purpose - a word outside it "+
		"would be a row nothing can count or route, which is what the free-form tags "+
		"are for", asked, strings.Join(TodoCategories, ", "))
}

// CategoryOf is what kind of work a todo says it is, or "" for one nobody has
// classified - which is legal, is most of this queue, and is not a thing to
// repair on the way out.
//
// There is no fallback to the body and no guess from the title. A category that
// was inferred is a number in a count that nobody asserted, and the whole point
// of the closed set is that every value in it was chosen by somebody who is on
// the record for choosing it.
func CategoryOf(a *Artifact) string {
	named := artifactField(a, CategoryField)
	if named == nil {
		return ""
	}
	category, _ := named.(string)
	return strings.ToLower(strings.TrimSpace(category))
}

// SetTodoCategory classifies a queue item and records who classified it: the
// value on the row and the entry in the log, in one write.
//
// The refusals, in the order they are asked:
//
//   - a token that resolves to nobody. An entry carries an actor, and a
//     classification nobody made is not one.
//   - a word that is not in the vocabulary.
//   - an id that does not name a queue item this principal may READ. One that is
//     not here, one that is out of reach, and one that is here and is a bug or a
//     report are all the same answer - the answer a read of it would give -
//     because naming an id here is not a way to find out what else it might be.
//
// It does NOT refuse a restatement, which is AssignTodo's and SetTodoStatus's
// rule: saying a bug is still a bug is somebody agreeing out loud, the fold is
// latest-wins, and a restatement costs a reader nothing.
func (d *DB) SetTodoCategory(
	ctx context.Context, p *Principal, todo, asked string,
) (*Artifact, *Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "todo.category")
	defer span.End()

	// The seat, by voteActor's rule: an agent is its own party here for the
	// reason it is its own voter. Who called this a bug is the seat that called
	// it one, not the person standing behind the seat - and p.UserID rides the
	// meta beside it so a reader has both.
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, refuseCategory("this token resolves to nobody, so it cannot say " +
			"what kind of work a todo is")
	}
	category, err := NormalizeTodoCategory(asked)
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
	from := CategoryOf(art)
	fields[CategoryField] = category
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: categorise %s: %w", art.ID, err)
	}

	entry, err := categoryEvent(art, p, actor, actorKind, from, category)
	if err != nil {
		return nil, nil, err
	}
	// One transaction, one clock reading, both rows or neither: a category with
	// no entry behind it is a call nobody can trace, and an entry with no value
	// behind it is a log that lies. Nothing here ever comes back to finish a
	// half-written operation.
	if err := d.SetArtifactFields(ctx, art, column, entry); err != nil {
		return nil, nil, err
	}
	art.Category = category
	span.SetArtifact(art.ID)
	return art, entry, nil
}

// TodoCategoryEntryEvent builds the entry a classification leaves in the log,
// for a write that sets the category as PART of a larger write of the same row.
//
// It exists for one caller: mem_write, which writes the whole item in one
// statement and whose author may state what it is. Going through SetTodoCategory
// there would write the row twice, so the entry is built here instead and handed
// to WriteMemory, which appends it in the same transaction as the item. Same
// builder, so the log behind a category is complete whichever door set it - a
// value on a row with no entry behind it would make the provenance this file
// exists for a thing that is sometimes there.
//
// The caller has already normalised the word and has already settled that the row
// is theirs to write: this builds an entry, it does not decide anything. On a
// create the artifact has no id yet - it is minted inside the write - so the entry
// names its own id as its thread rather than the todo's, and WriteMemory fills in
// the artifact column once the id exists.
func TodoCategoryEntryEvent(art *Artifact, p *Principal, from, to string) (*Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, refuseCategory("this token resolves to nobody, so it cannot say " +
			"what kind of work a todo is")
	}
	return categoryEvent(art, p, actor, actorKind, from, to)
}

// categoryEvent builds the entry a classification is.
func categoryEvent(art *Artifact, p *Principal, actor, actorKind, from, to string) (*Event, error) {
	meta, err := json.Marshal(map[string]string{
		CategoryField: to,
		"from":        from,
		"actor_kind":  actorKind,
		"actor_user":  p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: categorise %s: %w", art.ID, err)
	}
	return &Event{
		Type:    EventTodoCategory,
		Project: art.Project,
		Room:    categoryRoom(art),
		Thread:  art.ID,
		// The todo itself, which is what decides who reads the entry: the people
		// who can read the work are the people its classification is about.
		Artifact: art.ID,
		Actor:    actor,
		Body:     categoryBody(from, to),
		Meta:     meta,
	}, nil
}

// TodoCategoryEntryOf renders one event as the entry it is.
func TodoCategoryEntryOf(e *Event) CategoryEntry {
	entry := CategoryEntry{
		ID: e.ID, Type: e.Type, Todo: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.Category, entry.From = meta[CategoryField], meta["from"]
		entry.ActorKind, entry.ActorUser = meta["actor_kind"], meta["actor_user"]
	}
	return entry
}

// LatestTodoCategory folds a todo's entries into the classification that stands:
// the last one wins. nil when there are none, which is a todo nobody has
// classified THROUGH THIS VERB - it may still carry a category written by a peer
// whose entries did not travel, which is CategoryOf's business and not this
// fold's.
//
// entries must be in log order, which is what todoCategoryEvents returns.
func LatestTodoCategory(entries []CategoryEntry) *TodoCategoryStanding {
	if len(entries) == 0 {
		return nil
	}
	last := entries[len(entries)-1]
	return &TodoCategoryStanding{
		Category: last.Category, From: last.From, By: last.Actor, ByKind: last.ActorKind,
		ByUser: last.ActorUser, At: last.Created, Entry: last.ID,
	}
}

// TodoCategoryLog is every entry naming this todo that p may read, oldest first
// - so a reader sees the argument about what this work is rather than only where
// it landed. It is AssignLog and TodoStatusLog for the third piece of queue
// metadata, with the same permission story: the filter is in the WHERE clause
// and it is not a second rule.
func (d *DB) TodoCategoryLog(ctx context.Context, p *Principal, todo string) ([]CategoryEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "todo.category.log")
	defer span.End()

	events, err := d.todoCategoryEvents(ctx, p, []string{todo}, false)
	if err != nil {
		return nil, err
	}
	out := make([]CategoryEntry, 0, len(events))
	for _, e := range events {
		out = append(out, TodoCategoryEntryOf(e))
	}
	return out, nil
}

// todoCategoryEvents reads the entries naming any of todos, in log order,
// through the same event filter every other read of the log uses.
//
// There is no LIMIT on this, for depEvents' reason: the fold is over the WHOLE
// log for each todo, and a page that stopped early would fold a prefix - an
// answer that is not the classification that stands.
func (d *DB) todoCategoryEvents(
	ctx context.Context, p *Principal, todos []string, scopeAll bool,
) ([]*Event, error) {
	if len(todos) == 0 {
		return nil, nil
	}
	return readPage(ctx, d, "category events", func(a *args) string {
		idsArg := a.next(pq.Array(todos))
		typeArg := a.next(EventTodoCategory)
		filter := EventFilterSQL(p, "e", a, scopeAll)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ANY(` + idsArg + `) AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// categoryRoom is where an entry lands in the log: the room the todo was raised
// in, or the category room when it was raised in none. It is assignRoom's and
// statusRoom's rule, and it exists for the reason those do.
func categoryRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return CategoryRoom
}

// categoryBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one - the timeline, the console's activity
// view, the TUI.
//
// It names both ends the way statusBody does, because "filed as a bug" and
// "was called a feature and is now called a bug" are different facts and only
// the pair says which happened. Unclassified is said in words at either end: a
// sentence with a hole in it reads as a rendering fault.
func categoryBody(from, to string) string {
	switch {
	case from == "" && to == "":
		return "left unclassified"
	case from == "":
		return "filed as " + to
	case to == "":
		return "unfiled from " + from
	default:
		return from + "->" + to
	}
}
