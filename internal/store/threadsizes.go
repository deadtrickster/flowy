package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
)

// ThreadSizes is how many messages each of these threads holds, as this reader
// may see them.
//
// WHY THE NODE ANSWERS THIS AND NOT THE CONSOLE. The console has a page of a
// room, not a room: it opens on a bounded window and pages backwards on scroll.
// Counting replies from what happens to be on screen gives a number that is
// wrong in the direction nobody checks - a thread of twelve, three of whose
// messages are in the window, reads as "2 replies", and the reader who has been
// shown a number has stopped wondering. So the count comes from the log, where
// the whole thread is.
//
// IT IS ASKED ONCE PER PAGE, never per message, for the reason reactionsFor
// gives: a room read of fifty messages that asked fifty times would make the
// cheapest signal in the room the most expensive thing on the screen.
//
// ONLY MESSAGES COUNT. Every event in a room carries a thread - a reaction, a
// raise, a landing announcement - and counting all of them would tell a reader
// "8 replies" about a thread with one reply and seven acks on it. The type is
// the same one the transcript draws, so the number means the thing the reader
// can go and read.
func (d *DB) ThreadSizes(
	ctx context.Context, p *Principal, threads []string, chatType string, scopeAll bool,
) (map[string]int, error) {
	ids := make([]string, 0, len(threads))
	seen := map[string]bool{}
	for _, id := range threads {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	// NO THREADS IS AN EMPTY MAP, not a query. It is also not an error: a page
	// with nothing on it is what an empty room returns, and the caller draws
	// nothing either way.
	if len(ids) == 0 {
		return map[string]int{}, nil
	}

	ctx, span := otel.Start(ctx, otel.KindQuery, "chat.threadsizes")
	defer span.End()

	a := &args{}
	idsArg := a.next(pq.Array(ids))
	typeArg := a.next(chatType)
	filter := EventFilterSQL(p, "e", a, scopeAll)
	query := `SELECT e.thread, count(*) AS n
	            FROM events e
	           WHERE e.thread = ANY(` + idsArg + `) AND e.type = ` + typeArg + `
	             AND ` + filter + `
	           GROUP BY e.thread`
	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		span.Fail("the thread sizes did not run")
		return nil, fmt.Errorf("store: thread sizes: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var thread string
		var n int
		if err := rows.Scan(&thread, &n); err != nil {
			return nil, fmt.Errorf("store: thread sizes: %w", err)
		}
		out[thread] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: thread sizes: %w", err)
	}
	// A THREAD WITH NOTHING VISIBLE IN IT IS ABSENT, not zero, and the caller
	// must not turn one into the other. Every id here came off a message the
	// reader is looking at, so its own thread has at least that message in it;
	// a missing key means the count could not be taken, and "0 replies" is a
	// claim this cannot make.
	return out, nil
}
