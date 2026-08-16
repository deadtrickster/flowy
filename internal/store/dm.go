package store

import (
	"context"
	"fmt"

	"github.com/lib/pq"
)

// Direct messages, from the store's side.
//
// The read rule is in perm.go, where it belongs and where it is one clause. What
// is here is the other half - what the write path has to know before it puts a
// private message into a thread somebody already started - because a DM thread
// is only private if EVERY row in it is, and because the party set must not grow
// after the first message.
//
// Neither of these is a permission check and neither may be mistaken for one. A
// principal that cannot read a thread is refused by ThreadHidden before any of
// this runs; what these stop is a party WIDENING a conversation - which the
// filter cannot see, because each row it judges is addressed to exactly one
// person and looks perfectly private on its own.

// PrivateThread is what a thread looks like to the send path.
//
// Participants is every principal named on the thread, as actor or as addressee.
// It is the set a reply may address and no more: the first message fixes it at
// two, and every message after that has to name somebody already in it, so the
// set a person was told about when they started the conversation is the set it
// still has when it ends.
//
// NamedByTask is the one that would otherwise be a silent leak. The tasks clause
// in EventFilterSQL is OR-ed onto the end of the whole predicate and ADDS
// readers: every event whose thread a tasks row names is readable by the parties
// to that task. So a "private" message dropped into a handoff thread would be
// read by the assigner, the assignee and the delegated agent - none of whom the
// sender named, and none of whom the sender would be shown. A DM never joins one.
type PrivateThread struct {
	// Exists says the thread has at least one event in it. A thread with none is
	// nobody's yet, and every conversation starts as one.
	Exists bool
	// Private says it exists and every event in it is a direct message - the
	// same answer ThreadIsPrivate gives, from the same fragment. A thread with
	// one room message in it is a room conversation, and a private reply into it
	// would be private in the sense that only its author could see it, which is
	// not what the person writing it would think they had done.
	Private     bool
	NamedByTask bool
	// Participants is deduped and unordered.
	Participants []string
}

// HasParty reports whether id is one of the thread's participants. An empty id
// is nobody: an agentless token carries no agent id, and matching on "" would
// make every thread's participant set include it.
func (t *PrivateThread) HasParty(id string) bool {
	if id == "" {
		return false
	}
	for _, party := range t.Participants {
		if party == id {
			return true
		}
	}
	return false
}

// ReadPrivateThread reads what the send path needs to know about a thread.
//
// It asks the log without the permission filter, on purpose and safely: it is
// only ever consulted to REFUSE, and the caller has already put the writer
// through ThreadHidden - so a principal that reaches this has been shown every
// row it counts. Filtering here instead would be worse than useless: a thread
// half of which the writer cannot see would come back looking like a private
// conversation between the writer and one other person, which is the exact
// mistake this exists to catch.
func (d *DB) ReadPrivateThread(ctx context.Context, thread string) (*PrivateThread, error) {
	out := &PrivateThread{}
	if thread == "" {
		return out, nil
	}

	rows, err := d.sql.QueryContext(ctx,
		`SELECT e.actor, coalesce(e.addressee, '') FROM events e WHERE e.thread = $1`, thread)
	if err != nil {
		return nil, fmt.Errorf("store: read private thread %s: %w", thread, err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var actor, addressee string
		if err := rows.Scan(&actor, &addressee); err != nil {
			return nil, fmt.Errorf("store: read private thread %s: %w", thread, err)
		}
		out.Exists = true
		for _, party := range []string{actor, addressee} {
			if party == "" || seen[party] {
				continue
			}
			seen[party] = true
			out.Participants = append(out.Participants, party)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read private thread %s: %w", thread, err)
	}

	// The same question both merge doors ask, from the same fragment: two ideas
	// of what makes a thread private is one too many, and they would drift in
	// the direction of the one that was easier to write.
	if out.Private, err = threadIsPrivate(ctx, d.sql, thread); err != nil {
		return nil, err
	}
	err = d.sql.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM tasks t WHERE t.thread = $1)`, thread).Scan(&out.NamedByTask)
	if err != nil {
		return nil, fmt.Errorf("store: read private thread %s: %w", thread, err)
	}
	return out, nil
}

// ThreadIsPrivate reports whether thread is a private conversation: it holds at
// least one event and every event in it is a direct message.
//
// An empty thread is not private, and that is the useful answer rather than a
// technicality: a thread nobody has said anything in is nobody's yet, and every
// conversation - private or not - starts as one.
func (d *DB) ThreadIsPrivate(ctx context.Context, thread string) (bool, error) {
	return threadIsPrivate(ctx, d.sql, thread)
}

// threadIsPrivate is the same question against whatever is in hand: the merge
// asks it inside its transaction, the handlers ask it of the pool.
func threadIsPrivate(ctx context.Context, q querier, thread string) (bool, error) {
	if thread == "" {
		return false, nil
	}
	var private bool
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM events e WHERE e.thread = $1)
		    AND NOT EXISTS (SELECT 1 FROM events e
		                     WHERE e.thread = $1 AND NOT `+privateEventSQL("e")+`)`,
		thread).Scan(&private)
	if err != nil {
		return false, fmt.Errorf("store: read thread %s: %w", thread, err)
	}
	return private, nil
}

// PublicParents returns the ids in parents that name an event this principal may
// read and that is NOT a direct message. Duplicates are collapsed and the
// caller's order is kept, so a refusal names the first one the writer wrote.
//
// A direct message descends from a direct message or from nothing. The edge in
// parents is a claim about what came before what, and a private message hanging
// off a room message is a claim neither side can read: the room cannot see the
// child, and the private thread cannot see the parent it is drawn from. Keeping
// the DAG closed is what makes "this thread is private" a property of the whole
// thread rather than of each row in it - which is the property ReadPrivateThread
// then gets to check.
//
// It says nothing about parents the caller cannot read. UnreadableParents is
// that question and is asked first, so the two refusals stay distinguishable:
// "that is not yours to name" and "that is not a private message" are different
// mistakes and a writer fixes them differently.
func (d *DB) PublicParents(ctx context.Context, p *Principal, parents []string) ([]string, error) {
	if len(parents) == 0 || p == nil {
		return nil, nil
	}
	a := &args{}
	idsArg := a.next(pq.Array(parents))
	filter := EventFilterSQL(p, "e", a, false)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT e.id FROM events e
		  WHERE e.id = ANY(`+idsArg+`) AND `+filter+` AND `+privateEventSQL("e"), a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: read private parents: %w", err)
	}
	defer rows.Close()

	private := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: read private parents: %w", err)
		}
		private[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read private parents: %w", err)
	}

	var out []string
	seen := map[string]bool{}
	for _, id := range parents {
		if private[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}
