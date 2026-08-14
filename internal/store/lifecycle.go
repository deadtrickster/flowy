package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SetArtifactStatus moves one artifact's status and nothing else. It is not an
// upsert: a transition is a change to one column plus an event that records it,
// and replacing the row would make a status move look like a rewrite of the
// artifact to every peer that merges it.
//
// The clock moves, because the status is part of what replicates.
func (d *DB) SetArtifactStatus(ctx context.Context, art *Artifact, status string) error {
	art.Status = status
	art.HLC = d.clock.Pack()
	art.Node = d.node
	_, err := d.sql.ExecContext(ctx,
		`UPDATE artifacts SET status = $2, hlc = $3, node = $4, updated = now() WHERE id = $1`,
		art.ID, art.Status, art.HLC, art.Node)
	if err != nil {
		return fmt.Errorf("store: set status of %s: %w", art.ID, err)
	}
	return nil
}

// ArtifactEvents reads the events of one type that name an artifact, in log
// order. It does not filter by principal: the callers gate on being able to
// read the artifact itself, which is the same question asked once instead of
// once per event, and it keeps an audit trail whole rather than showing each
// reader the half of it that was written from their own project.
func (d *DB) ArtifactEvents(ctx context.Context, artifact, eventType string) ([]*Event, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+eventColumns+`
		   FROM events e
		  WHERE e.artifact = $1 AND e.type = $2
		  ORDER BY e.seq_hlc, e.id`, artifact, eventType)
	if err != nil {
		return nil, fmt.Errorf("store: events for %s: %w", artifact, err)
	}
	defer rows.Close()

	out := []*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: events for %s: %w", artifact, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: events for %s: %w", artifact, err)
	}
	return out, nil
}

// LatestThreadEvent is the last event in a thread, which is what the next one
// names as its parent when it is continuing the conversation rather than
// branching off a particular message. ErrNotFound when the thread is empty.
func (d *DB) LatestThreadEvent(ctx context.Context, thread string) (*Event, error) {
	e, err := scanEvent(d.sql.QueryRowContext(ctx,
		`SELECT `+eventColumns+`
		   FROM events e
		  WHERE e.thread = $1
		  ORDER BY e.seq_hlc DESC, e.id DESC
		  LIMIT 1`, thread))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest event in thread %s: %w", thread, err)
	}
	return e, nil
}

// LatestArtifactEvent is the last event of a type about an artifact, which is
// what the next one names as its parent. ErrNotFound when there is none yet -
// the first transition of an artifact opens the trail rather than continuing it.
func (d *DB) LatestArtifactEvent(ctx context.Context, artifact, eventType string) (*Event, error) {
	e, err := scanEvent(d.sql.QueryRowContext(ctx,
		`SELECT `+eventColumns+`
		   FROM events e
		  WHERE e.artifact = $1 AND e.type = $2
		  ORDER BY e.seq_hlc DESC, e.id DESC
		  LIMIT 1`, artifact, eventType))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: latest %s event for %s: %w", eventType, artifact, err)
	}
	return e, nil
}
