package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
)

// eventColumns is the read list, in the order scanEvent expects.
const eventColumns = `id, type, project, room, thread, parents, actor, artifact, seq_hlc,
	node, body, meta, created`

// scanEvent reads one row of eventColumns.
func scanEvent(sc scanner) (*Event, error) {
	var (
		e                              Event
		typeCol, project, room, thread sql.NullString
		actor, artifact, nodeCol, body sql.NullString
		meta                           []byte
		seq                            sql.NullInt64
	)
	err := sc.Scan(&e.ID, &typeCol, &project, &room, &thread, pq.Array(&e.Parents), &actor,
		&artifact, &seq, &nodeCol, &body, &meta, &e.Created)
	if err != nil {
		return nil, err
	}
	if project.Valid {
		p := project.String
		e.Project = &p
	}
	e.Type, e.Room, e.Thread = typeCol.String, room.String, thread.String
	e.Actor, e.Artifact, e.Node, e.Body = actor.String, artifact.String, nodeCol.String, body.String
	e.SeqHLC = seq.Int64
	if len(meta) > 0 {
		e.Meta = json.RawMessage(meta)
	}
	if e.Parents == nil {
		e.Parents = []string{}
	}
	return &e, nil
}

// EventQuery narrows a read of the log. Since pages by seq_hlc, which is the
// same cursor peer replication will use: strictly greater, so a caller can hand
// back the last value it saw.
//
// NotActors drops events written by the named actors. It is what an inbox is:
// everything you may see that you did not write yourself. It is a filter in the
// query rather than a loop over the result, so paging by Since and Limit still
// counts the rows the caller actually gets.
type EventQuery struct {
	Thread    string
	Room      string
	Type      string
	Since     int64
	NotActors []string
	ScopeAll  bool
	Limit     int
}

func (q EventQuery) limit() int {
	if q.Limit > 0 && q.Limit <= 1000 {
		return q.Limit
	}
	return defaultLimit
}

// ListEvents returns the events p may read, in log order. The log is
// append-only, so this is the only read it needs: ordering by seq_hlc then id
// is total, and it agrees with the order the events were appended in.
func (d *DB) ListEvents(ctx context.Context, p *Principal, q EventQuery) ([]*Event, error) {
	a := &args{}
	filter := EventFilterSQL(p, "e", a, q.ScopeAll)
	where := ""
	if q.Thread != "" {
		where += " AND e.thread = " + a.next(q.Thread)
	}
	if q.Room != "" {
		where += " AND e.room = " + a.next(q.Room)
	}
	if q.Type != "" {
		where += " AND e.type = " + a.next(q.Type)
	}
	if q.Since > 0 {
		where += " AND e.seq_hlc > " + a.next(q.Since)
	}
	for _, actor := range q.NotActors {
		if actor != "" {
			where += " AND coalesce(e.actor, '') <> " + a.next(actor)
		}
	}

	query := `SELECT ` + eventColumns + `
	            FROM events e
	           WHERE ` + filter + where + `
	           ORDER BY e.seq_hlc, e.id
	           LIMIT ` + a.next(q.limit())

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()

	out := []*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list events: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	return out, nil
}
