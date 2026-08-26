package store

import (
	"context"
	"fmt"

	"github.com/lib/pq"
)

// ThreadStanding is a READER'S RELATIONSHIP to the thread a delivered message
// sits in. It is not a property of the message - two seats reading the same line
// have different standings in it - which is why it is filled per delivery rather
// than stored on the event.
//
// WHY THE DELIVERY NEEDS IT. A waiter is handed a line and acts on it without
// anybody looking. The line already says which project's room it came from and
// whether it names an addressee, and neither answers the question that decides
// whether to reply: is this conversation mine? A room is a public square - most
// of what crosses it is two other seats talking - and a reply from a third seat
// that was never in the thread reaches nobody it was meant for.
//
// Measured 2026-08-26: the operator posted a correction into a thread between two
// other agents, in a room this seat watches. The seat read it, built the change,
// and was told "square size wasnt addressed to you". The delivery carried the
// thread's ID and nothing about standing in it, so nothing in the input could
// have said otherwise. Same shape as the project field this door already gained:
// remembering a rule cannot supply a fact the input does not carry.
type ThreadStanding struct {
	// Spoken is whether this reader has already said something in the thread.
	// The strongest single signal that a reply is expected of it.
	Spoken bool `json:"spoken"`
	// RootMine is whether the reader wrote the message the thread hangs off.
	// A thread rooted in your own message is yours even before you reply twice.
	RootMine bool `json:"root_mine"`
	// RepliesTo names the author of the message this one is a direct reply to,
	// when the delivery can see it, so a listener can print "X replying to Y"
	// rather than leaving the reader to guess from adjacency.
	RepliesTo string `json:"replies_to,omitempty"`
	// RepliesToMe is whether that direct parent is this reader's own message.
	RepliesToMe bool `json:"replies_to_me"`
}

// FillThreadStanding sets Standing on every delivered event, for the seat named
// by reader.
//
// MATCHED ON THE SPEAKER'S NAME, not on a user or agent id. A seat is a name on
// this fabric - the same person's token can carry several - and "did THIS seat
// speak here" is the question. The name is where every other surface already
// reads the author from, so a standing computed from it cannot disagree with the
// author line printed beside it.
//
// A READER THAT CANNOT BE RESOLVED GETS AN EMPTY STANDING, not an absent one:
// "you have not spoken here" and "nobody asked" would otherwise arrive
// identically, and the second is the one where a listener should be careful.
func (d *DB) FillThreadStanding(ctx context.Context, p *Principal, reader string, list []*Event) error {
	if reader == "" || len(list) == 0 {
		return nil
	}
	threads := map[string]bool{}
	parents := map[string]bool{}
	for _, e := range list {
		if e == nil {
			continue
		}
		if e.Thread != "" {
			threads[e.Thread] = true
		}
		if len(e.Parents) > 0 && e.Parents[0] != "" {
			parents[e.Parents[0]] = true
		}
	}
	if len(threads) == 0 && len(parents) == 0 {
		return nil
	}

	spoken := map[string]bool{}
	if len(threads) > 0 {
		a := &args{}
		rows, err := d.sql.QueryContext(ctx, `
			SELECT DISTINCT e.thread
			  FROM events e
			 WHERE e.type = 'chat'
			   AND e.thread = ANY(`+a.next(pq.Array(keysOf(threads)))+`)
			   AND e.meta->>'actor_name' = `+a.next(reader), a.vals...)
		if err != nil {
			return fmt.Errorf("store: read thread standing: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				return fmt.Errorf("store: read thread standing: %w", err)
			}
			spoken[t] = true
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store: read thread standing: %w", err)
		}
	}

	// The thread's ROOT is the message whose id the thread is named for, and the
	// direct parents are looked up in the same pass - both are "who wrote this
	// id", and one query answers it for every id in the page.
	want := map[string]bool{}
	for t := range threads {
		want[t] = true
	}
	for pid := range parents {
		want[pid] = true
	}
	author := map[string]string{}
	if len(want) > 0 {
		a := &args{}
		rows, err := d.sql.QueryContext(ctx, `
			SELECT e.id, coalesce(e.meta->>'actor_name', '')
			  FROM events e
			 WHERE e.id = ANY(`+a.next(pq.Array(keysOf(want)))+`)`, a.vals...)
		if err != nil {
			return fmt.Errorf("store: read thread authors: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return fmt.Errorf("store: read thread authors: %w", err)
			}
			author[id] = name
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store: read thread authors: %w", err)
		}
	}

	for _, e := range list {
		if e == nil {
			continue
		}
		st := &ThreadStanding{
			Spoken:   spoken[e.Thread],
			RootMine: e.Thread != "" && author[e.Thread] == reader,
		}
		if len(e.Parents) > 0 && e.Parents[0] != "" {
			if name, seen := author[e.Parents[0]]; seen {
				st.RepliesTo = name
				st.RepliesToMe = name == reader
			}
		}
		e.Standing = st
	}
	return nil
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
