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

// ASKING FOR WORK SOMEBODY ELSE IS CARRYING, WITH A DEADLINE ON THE ASKING.
//
// An agent with an empty queue beside an agent with nine rows is the failure
// this exists for, and it is the one the operator named: rebalancing, by task
// stealing, on mutual agreement. The hard half is not the handover - AssignTodo
// already lets any reader move any assignee, deliberately, and nothing here
// takes that away. The hard half is that MUTUAL AGREEMENT DEADLOCKS WHEN ONE
// PARTY CANNOT ANSWER. An agent that died mid-run, or was decommissioned, or
// whose seat was retired still holds its rows, and a protocol that waits for its
// consent waits forever - which is worse than no protocol, because the work now
// looks negotiated rather than stuck.
//
// So the deadline is the whole point. A request is an ASK with a clock on it: a
// live holder answers in seconds, a dead one never answers, and after the
// deadline the taking is legal AND RECORDED AS HAVING BEEN TAKEN THAT WAY. The
// two outcomes stay distinguishable forever - a step of `yes` is a handover
// somebody agreed to, and `take` is one nobody objected to in time. A reader who
// cares which it was can tell, which is the property that makes an automatic
// escape hatch honest rather than a loophole.
//
// A NEGOTIATION IS EVENTS AND NOTHING ELSE. There is no steal_by column and no
// standing-request blob on the row: the request is a FOLD of the log, the way an
// Assignment is and a dep edge is. That is not tidiness. A relation with two
// parties and provenance cannot live in fields, because fields are last-write-
// wins: two agents asking for the same row at the same moment would overwrite
// each other and the survivor would read as the only one who ever asked. Every
// step is an appended, signed event naming the item, so a race leaves BOTH asks
// in the record and the fold decides which one stands - and the loser can see
// that they lost rather than finding their request had silently never existed.
// (The orchestrator caught this; the first cut of this file did put it in
// fields.)
//
// THE DEADLINE IS STAMPED HERE, NOT SUPPLIED. The caller says how LONG, bounded;
// this file says WHEN, from the node's own clock. A caller who could send the
// instant would send one that has already passed, and the deadline would be a
// formality attached to a taking.
//
// THIS VERB ADDS NO LOCK, and that is a decision rather than an omission. Read
// permission is the bar on assignment (see the head of assign.go) and putting a
// second bar here would mean a row could be assigned by the door that has no
// protocol and not by the one that does - two doors, two rules, and the queue
// would drain through the unguarded one. What this adds is the ASK, the CLOCK
// and the RECORD. A caller that wants to take a row without asking still can,
// exactly as before, and the log will say that is what they did.
//
// WHO MAY ANSWER IS ANY READER, for the same reason. An assignee is a HANDLE and
// not a capability - the fabric has no mapping from the string "flowy-claude" to
// a seat, and inventing one here would be a permission layer over a field that
// grants nothing. So a refusal cannot be restricted to the holder without
// pretending to an identity this node does not have. What IS unforgeable is the
// actor on the entry: every answer records the seat that gave it, so a bad-faith
// "no" is attributable even though it is not preventable. Only the TAKE is
// restricted, and it is restricted by actor rather than by handle: the seat that
// asked is the seat that may take, because that one the node does know.
//
// A REQUEST GOES STALE WHEN THE ROW MOVES. The deadline says nobody answered;
// it does not say the situation is unchanged. If the holder is no longer who the
// request was made against - they handed it on, they put it down, somebody else
// took it - the take is refused and the reason names the new holder. Otherwise a
// request filed against Alice would mature into a taking from Bob, which nobody
// agreed to and nobody was asked about.

// EventTodoSteal is what every step of the negotiation is in the log: the ask,
// the answer and the taking, one type with the step on the meta. It is minted,
// so the only way to get one is through the verb where the rules are.
const EventTodoSteal = "todo.steal"

// StealRoom is where an entry lands when the item names no room of its own. It
// is AssignRoom's rule for the same reason: an entry nobody can find is an entry
// nobody reads.
const StealRoom = "steal"

// The steps. They are words rather than a boolean because "handed over" and "not
// objected to in time" are the two facts this whole file exists to keep apart,
// and a boolean would collapse them the day somebody folds the log.
const (
	StealAsk      = "ask"
	StealYes      = "yes"
	StealNo       = "no"
	StealTake     = "take"
	StealWithdraw = "withdraw"
)

// StealSteps is the vocabulary, for the doors that validate an argument against
// it. ask is absent: it is what an empty step means, because asking is the step
// somebody takes without knowing this list exists.
var StealSteps = []string{StealYes, StealNo, StealTake, StealWithdraw}

// How long the asker may give the holder to answer.
//
// The default is the one number here chosen rather than derived, and it is
// chosen for the case the deadline exists for: a holder that is alive answers in
// seconds, and a holder that is gone is gone. Half an hour is long enough that a
// busy agent finishing a gate is not robbed mid-run, and short enough that a
// decommissioned seat does not park the queue for a shift.
//
// The bounds exist so the clock cannot be made meaningless in either direction.
// A one-second deadline is a taking with a formality attached; a one-year
// deadline is a request that never matures, which is the deadlock this file is
// about, reintroduced by argument.
const (
	DefaultStealWait = 30 * time.Minute
	MinStealWait     = time.Minute
	MaxStealWait     = 24 * time.Hour
)

// StealRequest is the ask that stands on an item: the fold of its log.
type StealRequest struct {
	By    string `json:"by"`
	Actor string `json:"actor,omitempty"`
	From  string `json:"from,omitempty"`
	After string `json:"after"`
	// Mature says the deadline has passed, so the asker may take it. It is
	// computed on the answer rather than left to the client, so that "may I take
	// this" is not a clock comparison every surface implements slightly
	// differently.
	Mature bool `json:"mature"`
	// Entry is the ask this stands on, so a reader can go and see it.
	Entry string `json:"entry,omitempty"`
}

// StealEntry is one step in the log behind a negotiation.
type StealEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Todo string `json:"todo"`
	// Step is ask|yes|no|take|withdraw. Not omitempty: a step that did not say
	// which one it was is a broken record and should read as one.
	Step string `json:"step"`
	By   string `json:"by,omitempty"`
	From string `json:"from,omitempty"`
	// After is the deadline this step set, on an ask, and empty elsewhere.
	After string `json:"after,omitempty"`
	// Reason is what a refusal said, when it said anything.
	Reason    string `json:"reason,omitempty"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// StealResult is what a step answers with: the item, the step recorded, the
// request as it now stands (nil once it is settled), and who the work came off
// when the step moved it.
type StealResult struct {
	Item     *Artifact     `json:"item"`
	Step     StealEntry    `json:"step"`
	Request  *StealRequest `json:"request,omitempty"`
	Assignee string        `json:"assignee"`
	// Held is who the work came off, when a step moved it. It is AssignTodo's
	// field and it is here for AssignTodo's reason: a handover that reports a
	// bare success is how work moves off somebody silently.
	Held string `json:"held,omitempty"`
}

// stealRefusalError is the caller's mistake, on the interface every queue
// refusal already satisfies so that HTTP maps it to 400 and MCP does not report
// it as a broken node.
type stealRefusalError struct{ reason string }

func (e stealRefusalError) Error() string { return e.reason }
func (e stealRefusalError) depRefusal()   {}

func refuseSteal(format string, a ...any) error {
	return stealRefusalError{reason: fmt.Sprintf(format, a...)}
}

// StealWait normalises the deadline an ask was given. Zero is the default rather
// than an instant deadline: a caller that did not say how long meant the usual
// amount of time, not none.
func StealWait(d time.Duration) (time.Duration, error) {
	if d == 0 {
		return DefaultStealWait, nil
	}
	if d < MinStealWait || d > MaxStealWait {
		return 0, refuseSteal("a deadline of %s is outside %s..%s: shorter than the "+
			"minimum is a taking with a formality attached, and longer than the maximum "+
			"is a request that never matures", d, MinStealWait, MaxStealWait)
	}
	return d, nil
}

// FoldStealRequest is the ask that stands, out of a log in order: an ask opens
// one and every other step closes it. nil when nothing is outstanding.
//
// Latest ask wins among concurrent ones, over the order the log is read in,
// which is seq_hlc - TallyOf's rule for the other verb that folds. What matters
// is that the losers are STILL IN THE LOG: a second asker whose request did not
// stand can see that it did not, which is precisely what a field could not have
// told them.
func FoldStealRequest(entries []StealEntry, now time.Time) *StealRequest {
	var open *StealRequest
	for _, e := range entries {
		if e.Step != StealAsk {
			open = nil
			continue
		}
		open = &StealRequest{
			By: e.By, Actor: e.Actor, From: e.From, After: e.After, Entry: e.ID,
		}
	}
	if open == nil {
		return nil
	}
	// An unparseable deadline reads as NOT mature. The alternative - treating it
	// as passed - would turn a corrupt entry into permission to take the row,
	// which is the failure direction that cannot be undone.
	if at, err := time.Parse(time.RFC3339, open.After); err == nil {
		open.Mature = !now.Before(at)
	}
	return open
}

// StealTodo runs one step of the negotiation over one item.
//
// step is ask|yes|no|take|withdraw; empty means ask. by is the handle the work
// would move to and is read only on an ask (every later step is about the
// request already standing, so re-stating who it was for could only disagree
// with it). wait is how long the holder gets, on an ask, and the INSTANT is
// stamped here from the node's clock. reason is free text a refusal carries.
//
// said is an extra event to write in the same transaction, or nil - AssignTodo's
// parameter, for AssignTodo's caller: the room hearing about the ask and the ask
// landing are one thing or they are a message about something that did not
// happen.
func (d *DB) StealTodo(
	ctx context.Context, p *Principal, todo, step, by, reason string,
	wait time.Duration, said *Event,
) (*StealResult, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, "todo.steal")
	defer span.End()

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, refuseSteal("this token resolves to nobody, so it cannot ask for " +
			"anybody's work")
	}
	step = strings.TrimSpace(strings.ToLower(step))
	if step == "" {
		step = StealAsk
	}
	if step != StealAsk && !isStealStep(step) {
		return nil, refuseSteal("%q is not a step in this negotiation: leave it out to ask, "+
			"or one of %s", step, strings.Join(StealSteps, ", "))
	}
	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	held := strings.TrimSpace(AssigneeOf(art))
	log, err := d.StealLog(ctx, p, art.ID)
	if err != nil {
		return nil, err
	}
	standing := FoldStealRequest(log, now)

	// What each step does, decided before anything is written so that a refusal
	// costs no write and a success is one.
	var assignTo string // the handle the work moves to, when it moves
	var moves bool      // whether it moves at all
	var deadline string // the instant an ask sets, stamped here
	switch step {
	case StealAsk:
		// Asking for work nobody is carrying is a step with nothing on the other
		// side of it: there is no holder to agree, no holder to fail to answer,
		// and the deadline would be a wait for nothing. The answer names the verb
		// that does what the caller wants, rather than accepting a request that
		// can only ever mature.
		if held == "" {
			return nil, refuseSteal("nobody is carrying %s, so there is nothing to ask "+
				"for - assign it to yourself", art.ID)
		}
		if by, err = NormalizeAssignee(by); err != nil {
			return nil, err
		}
		if by == "" {
			return nil, refuseSteal("an ask has to say who the work would go to")
		}
		if by == held {
			return nil, refuseSteal("%s already has %s", held, art.ID)
		}
		// A request that is still live belongs to whoever made it. Letting a
		// second asker take its place would reset the clock the first one is
		// waiting on, so the queue's slowest asker would win every race by
		// arriving last. The refusal names who is already waiting, which is the
		// answer that lets the second asker do something about it.
		if standing != nil && !standing.Mature && standing.Actor != actor {
			return nil, refuseSteal("%s already asked for %s and the deadline is %s - "+
				"wait for it or ask them", standing.By, art.ID, standing.After)
		}
		if wait, err = StealWait(wait); err != nil {
			return nil, err
		}
		deadline = now.Add(wait).Format(time.RFC3339)
	case StealYes:
		if standing == nil {
			return nil, refuseSteal("nobody has asked for %s", art.ID)
		}
		assignTo, moves = standing.By, true
	case StealNo:
		if standing == nil {
			return nil, refuseSteal("nobody has asked for %s", art.ID)
		}
	case StealWithdraw:
		if standing == nil {
			return nil, refuseSteal("nobody has asked for %s", art.ID)
		}
		// Withdrawing is the asker taking their own request back, so it is
		// restricted the way the take is: to the seat that made it. Anybody else
		// cancelling somebody's request would be a "no" that does not record
		// itself as one.
		if standing.Actor != actor {
			return nil, refuseSteal("that request was made by another seat - answer it " +
				"with no if you want it dropped, which is recorded as an answer")
		}
	case StealTake:
		if standing == nil {
			return nil, refuseSteal("nobody has asked for %s, so there is nothing "+
				"matured to take", art.ID)
		}
		// The one restriction in this file, and it is by SEAT rather than by
		// handle: a handle is a string anybody may write, and the actor is what
		// this node actually knows.
		if standing.Actor != actor {
			return nil, refuseSteal("%s asked for %s, and the seat that asked is the "+
				"one that may take it", standing.By, art.ID)
		}
		if !standing.Mature {
			return nil, refuseSteal("the deadline on %s is %s and it has not passed - "+
				"that is the whole point of it", art.ID, standing.After)
		}
		// The deadline says nobody answered. It does not say nothing changed.
		if held != standing.From {
			return nil, refuseSteal("%s was asked for while %s had it and %s has it now - "+
				"the request is stale, ask again", art.ID, nobodyOr(standing.From),
				nobodyOr(held))
		}
		assignTo, moves = standing.By, true
	}

	entry, err := stealEvent(art, p, actor, actorKind, step,
		stealWho(step, by, standing), stealFrom(step, held, standing), deadline, reason)
	if err != nil {
		return nil, err
	}
	events := []*Event{entry}
	if said != nil {
		events = append(events, said)
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, err
	}
	if moves {
		if assignTo, err = NormalizeAssignee(assignTo); err != nil {
			return nil, err
		}
		fields[AssigneeField] = assignTo
		// A step that moves the work leaves an ASSIGNMENT entry too, not only a
		// steal entry. Otherwise "who is carrying this and who put them there" -
		// which reads AssignLog and nothing else - would have a hole in it
		// exactly where the work changed hands without being handed over, and
		// every surface built on that log would have to learn about this one.
		claim, err := assignEvent(art, p, actor, actorKind, assignTo, held)
		if err != nil {
			return nil, err
		}
		events = append([]*Event{entry, claim}, events[1:]...)
	}
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("store: steal %s: %w", art.ID, err)
	}
	// EVERY STEP GOES THROUGH THE SAME TRANSACTIONAL WRITE, including the ones
	// that change nothing on the row. Appending the events one at a time would
	// be cheaper and would let a step half-happen: an ask recorded with no
	// message in the room is a deadline the holder was never told about, and a
	// message with no ask behind it is the room told about something that did
	// not happen. Both are worse than the cost, which is a row rewritten with
	// the same fields it had - and even that is arguably right, since a request
	// standing against an item IS a change in its state and the queue watching
	// for movement should see one.
	if err := d.SetArtifactFields(ctx, art, column, events...); err != nil {
		return nil, err
	}
	span.SetArtifact(art.ID)

	out := &StealResult{Item: art, Step: StealEntryOf(entry), Assignee: AssigneeOf(art)}
	if moves {
		out.Held = held
	} else {
		out.Request = FoldStealRequest(append(log, out.Step), now)
	}
	return out, nil
}

// StealRequestOn is the ask standing on one item, as this reader can see it.
func (d *DB) StealRequestOn(
	ctx context.Context, p *Principal, todo string, now time.Time,
) (*StealRequest, error) {
	log, err := d.StealLog(ctx, p, todo)
	if err != nil {
		return nil, err
	}
	return FoldStealRequest(log, now), nil
}

// StealRequests is the ask standing on each of todos, folded per item out of one
// query - Assignments' shape, for Assignments' reason: a queue view asking this
// per row would be a query per row.
func (d *DB) StealRequests(
	ctx context.Context, p *Principal, todos []string, now time.Time,
) (map[string]*StealRequest, error) {
	events, err := d.stealEvents(ctx, p, todos)
	if err != nil {
		return nil, err
	}
	byTodo := map[string][]StealEntry{}
	for _, e := range events {
		entry := StealEntryOf(e)
		byTodo[entry.Todo] = append(byTodo[entry.Todo], entry)
	}
	out := make(map[string]*StealRequest, len(byTodo))
	for todo, entries := range byTodo {
		if open := FoldStealRequest(entries, now); open != nil {
			out[todo] = open
		}
	}
	return out, nil
}

// stealWho is the handle a step is about: the one an ask names, and the one the
// standing request named for every step that answers it. It is read off the
// request rather than off the argument so that an answer cannot record a
// different beneficiary than the one that was asked for.
func stealWho(step, asked string, standing *StealRequest) string {
	if step == StealAsk {
		return asked
	}
	if standing != nil {
		return standing.By
	}
	return ""
}

// stealFrom is who the step is about taking it from: whoever holds it now on an
// ask, and whoever the request was made against afterwards - so a stale take is
// still recorded against the party it was asked of.
func stealFrom(step, held string, standing *StealRequest) string {
	if step == StealAsk {
		return held
	}
	if standing != nil {
		return standing.From
	}
	return held
}

func isStealStep(step string) bool {
	for _, s := range StealSteps {
		if step == s {
			return true
		}
	}
	return false
}

// nobodyOr is the word for an empty handle in a sentence a person reads. The
// queue has one word for this state everywhere else (see NobodyName) and a
// refusal that said "" had it would be the same bug in prose.
func nobodyOr(name string) string {
	if strings.TrimSpace(name) == "" {
		return "nobody"
	}
	return name
}

// stealEvent builds the entry a step is.
func stealEvent(
	art *Artifact, p *Principal, actor, actorKind, step, by, from, after, reason string,
) (*Event, error) {
	fields := map[string]string{
		"step":       step,
		"actor_kind": actorKind,
		"actor_user": p.UserID,
	}
	// Each of these is omitted when it is empty rather than written blank, for
	// the reason held is on the assign entry: present-and-empty means "somebody
	// said nobody" in this fabric, and here it would mean "this step had no such
	// thing" - two facts one encoding cannot carry.
	for k, v := range map[string]string{"by": by, "from": from, "after": after, "reason": reason} {
		if strings.TrimSpace(v) != "" {
			fields[k] = v
		}
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("store: steal %s: %w", art.ID, err)
	}
	return &Event{
		Type:     EventTodoSteal,
		Project:  art.Project,
		Room:     stealRoom(art),
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     stealBody(step, by, from, after),
		Meta:     meta,
	}, nil
}

// StealEntryOf renders one event as the entry it is.
func StealEntryOf(e *Event) StealEntry {
	entry := StealEntry{
		ID: e.ID, Type: e.Type, Todo: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.Step, entry.By, entry.From = meta["step"], meta["by"], meta["from"]
		entry.After, entry.Reason = meta["after"], meta["reason"]
		entry.ActorKind, entry.ActorUser = meta["actor_kind"], meta["actor_user"]
	}
	return entry
}

// StealLog is every step naming this item that p may read, oldest first. It is
// AssignLog for this verb, with the same permission story: the filter is in the
// WHERE clause and it is not a second rule.
func (d *DB) StealLog(ctx context.Context, p *Principal, todo string) ([]StealEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "todo.steal.log")
	defer span.End()

	events, err := d.stealEvents(ctx, p, []string{todo})
	if err != nil {
		return nil, err
	}
	out := make([]StealEntry, 0, len(events))
	for _, e := range events {
		out = append(out, StealEntryOf(e))
	}
	return out, nil
}

// stealEvents reads the steps naming any of todos, in log order, through the
// same event filter every other read of the log uses.
//
// There is no LIMIT on this, for assignEvents' reason: the fold is over the
// WHOLE log for each item, and a page that stopped early would fold a prefix -
// which here would mean an answered request reading as one still standing.
func (d *DB) stealEvents(ctx context.Context, p *Principal, todos []string) ([]*Event, error) {
	if len(todos) == 0 {
		return nil, nil
	}
	return readPage(ctx, d, "steal events", func(a *args) string {
		idsArg := a.next(pq.Array(todos))
		typeArg := a.next(EventTodoSteal)
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ANY(` + idsArg + `) AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

func stealRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return StealRoom
}

// stealBody is what the step reads as on every surface that renders an event
// body and knows nothing about this verb - the timeline, the activity view, the
// TUI. The ask says the deadline because a reader scrolling past it is exactly
// who might answer in time.
func stealBody(step, by, from, after string) string {
	switch step {
	case StealAsk:
		return fmt.Sprintf("%s asked %s for this, until %s", by, nobodyOr(from), after)
	case StealYes:
		return fmt.Sprintf("handed to %s", by)
	case StealNo:
		return fmt.Sprintf("kept: %s was refused", by)
	case StealTake:
		return fmt.Sprintf("taken by %s - the deadline passed unanswered", by)
	case StealWithdraw:
		return fmt.Sprintf("%s withdrew the request", by)
	}
	return step
}
