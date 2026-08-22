package store

// WHOSE MOVE IT IS, which is a different fact from who is carrying the work.
//
// THE DEFECT THIS ANSWERS, measured by orchestrator on its own board: the nag
// said "3 row(s) assigned to orchestrator, all open" and all three were
// questions waiting on the operator. A seat blocked on somebody else looks
// identical to a seat sitting on its work - one number moving for two reasons,
// which is evidence for neither, and the same shape this fleet narrowed four
// times in checks the same evening.
//
// THE TWO WAYS TO SAY IT BEFORE WERE BOTH WRONG. Hand the row over, and the
// board says they are CARRYING work they are only answering - four rows landed
// on the operator that way in one evening. Or keep it and put the question in a
// note, which is what made them read four rows to find out they were being
// asked. The fabric had no way to say a row is somebody else's MOVE without
// also saying it is their JOB.
//
// SO IT SITS BESIDE THE ASSIGNEE AND DOES NOT TOUCH IT. The carrier still
// carries it. What is added is who owes the next move, and what they were asked
// - the question rides with the pointer, because a name with no question is a
// row somebody has to open to find out what is wanted, which is the state this
// came from.
//
// WHAT CLEARS IT is the person named writing on the row - and that is a
// READING rather than a write, deliberately.
//
// The obvious build is a ClearWaitingOn that every write path calls. This repo's
// most repeated defect is the sibling of exactly that: a guard applied where its
// author was standing, and the path they were not thinking about left unguarded
// and now MORE dangerous, because the guard being right there in the file reads
// as the hazard being handled. Four instances in one day. A clearing rule spread
// over the note verb, the status verb, the assign verb and whatever is written
// next is that shape with a schedule.
//
// So nothing clears it. AnsweredWaiting asks the log: is there an entry on this
// row, by the principal named, after the moment the question was asked. A write
// path added tomorrow is covered without knowing this exists, which is the
// property a call-every-caller rule cannot have.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// EventTodoWaiting is what asking somebody leaves in the log, and it records
// BOTH ENDS for EventTodoPriority's reason: a pointer that changed with no
// record is an argument nobody can settle later - "I answered that" against "I
// never saw it" - and who was asked what is exactly the kind of thing a reader
// will want attributed.
const EventTodoWaiting = "todo.waiting"

// WaitingOnField is who owes the next move. A handle, like AssigneeField, and
// for the same reason: every coordination question here is "who", and the
// answer has to be a name the roster can resolve rather than a principal id
// nobody can read.
const WaitingOnField = "waiting_on"

// WaitingSinceField is the clock reading the question was asked at, and it is
// what makes the clearing rule a reading. Without it "have they written on this
// row" is answered by every note they ever left, including the one from before
// they were asked.
const WaitingSinceField = "waiting_since"

// AskedField is what they were asked, in the asker's words.
//
// It is a separate key rather than part of the pointer because the two are read
// at different times: the nag counts the pointer and never reads the question,
// and a person opening the row wants the question and already knows the name.
const AskedField = "waiting_asked"

// WaitingOnOf is who owes the next move on this row, or "" when nobody does.
//
// The current value and nothing else - who put it there and when is the log's
// answer, which is the division AssigneeOf makes and for the same reason.
func WaitingOnOf(a *Artifact) string {
	if named := artifactField(a, WaitingOnField); named != nil {
		name, _ := named.(string)
		return strings.TrimSpace(name)
	}
	return ""
}

// AskedOf is what was asked, or "" when nothing was recorded.
func AskedOf(a *Artifact) string {
	if asked := artifactField(a, AskedField); asked != nil {
		text, _ := asked.(string)
		return strings.TrimSpace(text)
	}
	return ""
}

// SetWaitingOn records that a row is waiting on somebody, or takes that back.
//
// An empty `who` clears it, which is how an asker withdraws a question they no
// longer need answered. The person named clearing it is a different act and is
// ClearWaitingOnFrom's - this one is the asker's verb.
func (d *DB) SetWaitingOn(
	ctx context.Context, p *Principal, todo, who, asked string,
) (*Artifact, *Event, error) {
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, nil, fmt.Errorf("store: this token resolves to nobody, so it cannot say " +
			"who is being waited on")
	}

	art, err := d.readWorkItem(ctx, p, strings.TrimSpace(todo))
	if err != nil {
		return nil, nil, err
	}

	// RESOLVED, NOT STORED VERBATIM, and this is not a nicety. A row waiting on
	// a seat called "me" is a row no roster can resolve and no nag can count -
	// the board grew exactly such a seat once, holding one row, and it took a
	// sweep to find. SelfName is where that lesson already lives.
	want := strings.TrimSpace(who)
	if SelfName(want) {
		want = d.seatHandle(ctx, p)
		if want == "" {
			return nil, nil, fmt.Errorf("store: %q means this caller and this caller has no "+
				"handle, so there is no name to wait on", who)
		}
	}

	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, nil, err
	}
	from := WaitingOnOf(art)
	if want == "" {
		// DELETED RATHER THAN SET EMPTY, as the priority field is: an empty
		// string in the column reads as a value to anything counting, and a
		// board reporting "2 waiting on '' " is worse than one reporting none.
		delete(fields, WaitingOnField)
		delete(fields, WaitingSinceField)
		delete(fields, AskedField)
	} else {
		fields[WaitingOnField] = want
		// The reading is taken here rather than from the entry, because the
		// entry's own seq_hlc is assigned as it is written and a question is
		// answered by what comes AFTER it. Equal is not after: an answer and
		// the question it answers cannot share a reading.
		now, err := d.clock.Now()
		if err != nil {
			return nil, nil, fmt.Errorf("store: waiting on %s: %w", art.ID, err)
		}
		fields[WaitingSinceField] = now.Pack()
		if q := strings.TrimSpace(asked); q != "" {
			fields[AskedField] = q
		} else {
			delete(fields, AskedField)
		}
	}
	column, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("store: waiting on %s: %w", art.ID, err)
	}

	meta, err := json.Marshal(map[string]string{
		WaitingOnField: want,
		"from":         from,
		"asked":        strings.TrimSpace(asked),
		"actor_kind":   actorKind,
		"actor_user":   p.UserID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("store: waiting on %s: %w", art.ID, err)
	}
	entry := &Event{
		Type:     EventTodoWaiting,
		Project:  art.Project,
		Room:     categoryRoom(art),
		Thread:   art.ID,
		Artifact: art.ID,
		Actor:    actor,
		Body:     waitingLine(art, from, want, asked),
		Meta:     meta,
	}
	if err := d.SetArtifactFields(ctx, art, column, entry); err != nil {
		return nil, nil, err
	}
	return art, entry, nil
}

// waitingLine is what the log says in words, and it says BOTH ENDS for the
// reason priorityLine does: the name alone is a fact about now, and what a
// reader wants later is what changed.
func waitingLine(a *Artifact, from, to, asked string) string {
	what := "todo"
	if a.Kind == MergeKind {
		what = "merge"
	}
	switch {
	case to == "":
		if from == "" {
			return "this " + what + " is waiting on nobody"
		}
		return "no longer waiting on " + from
	case from == "" && strings.TrimSpace(asked) != "":
		return "waiting on " + to + ": " + strings.TrimSpace(asked)
	case from == "":
		return "waiting on " + to
	default:
		return "waiting on " + to + " (was " + from + ")"
	}
}

// AnsweredWaiting is which of these rows the person named has since written on.
//
// THE CLEARING RULE, AS A READING. A row carries who owes the next move and the
// clock reading it was asked at; this asks the log whether that principal has
// left an entry on the row AFTER that reading. Nothing has to remember to clear
// anything, so a write path added next month is covered by having existed.
//
// STRICTLY AFTER, not at-or-after: the question's own entry shares the row and
// would otherwise answer itself the moment an asker asked about their own row.
//
// The join is handle -> user -> the actor_user an entry carries, because
// waiting_on holds a HANDLE (AssigneeField's reason - a name a roster can
// resolve) while an event holds ids. artifact notes carry actor and actor_user
// both, and actor_user is the only one that survives the agent/user split: an
// operator answering from the console and their agent answering over MCP are
// the same person owing the same answer, and a comparison on actor would call
// only one of them an answer.
//
// One query for the whole board rather than one per row: the nag runs this on
// every tick for every seat, and a per-row read would be a query per open row
// per waiter per ten seconds.
func (d *DB) AnsweredWaiting(ctx context.Context, rows []*Artifact) (map[string]bool, error) {
	answered := map[string]bool{}
	ids := make([]string, 0, len(rows))
	for _, a := range rows {
		if WaitingOnOf(a) != "" {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) == 0 {
		return answered, nil
	}

	// EVERY ROW ASKED ABOUT COMES BACK, answered true or false. A map that held
	// only the answered ones would make "not in the map" mean both "still
	// waiting" and "this node did not look", which is the reading this whole
	// row is about.
	for _, id := range ids {
		answered[id] = false
	}

	// ASKING IS NOT ANSWERING, and this clause is the difference between a
	// working rule and one that clears itself. Measured: A asks A - which is
	// what "waiting on me" is on a row somebody else filed - and the question's
	// OWN entry is a write by the named principal, after the reading, on the
	// row. Without this the count went straight back to zero and the field read
	// as if every question had been answered the instant it was asked.
	//
	// It matters past the self-ask too: an asker restating the question would
	// otherwise answer it.
	query := `
		SELECT DISTINCT a.id
		  FROM artifacts a
		  JOIN users u ON u.handle = a.waiting_on
		  JOIN events e ON e.artifact = a.id
		 WHERE a.id = ANY($1)
		   AND a.waiting_on IS NOT NULL
		   AND e.type <> $2
		   AND e.meta->>'actor_user' = u.id
		   AND e.seq_hlc > (a.fields->>'waiting_since')::bigint`
	found, err := d.sql.QueryContext(ctx, query, pq.Array(ids), EventTodoWaiting)
	if err != nil {
		return nil, fmt.Errorf("store: which waiting rows were answered: %w", err)
	}
	defer found.Close()
	for found.Next() {
		var id string
		if err := found.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: which waiting rows were answered: %w", err)
		}
		answered[id] = true
	}
	if err := found.Err(); err != nil {
		return nil, fmt.Errorf("store: which waiting rows were answered: %w", err)
	}
	return answered, nil
}
