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
//
// A deleted artifact has no status to move, so the write says so rather than
// matching the dead row anyway. The handlers gate on a filtered read, and the
// read is not the write: between the two, the owner's delete can land, and the
// move would then stamp a new reading and this node's signature onto a
// tombstoned row on somebody else's behalf - and MoveArtifactStatus would
// append an entry for a transition of an artifact that is not there. ErrNotFound
// is what the caller would have got had the delete landed a moment earlier.
func (d *DB) SetArtifactStatus(ctx context.Context, art *Artifact, status string) error {
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: set status of %s: %w", art.ID, err)
	}
	return d.setArtifactStatus(ctx, d.sql, art, status, at)
}

// setArtifactStatus is the one statement, against whatever is in hand and at
// whatever reading the caller has already taken.
func (d *DB) setArtifactStatus(
	ctx context.Context, q execer, art *Artifact, status string, at int64,
) error {
	art.Status = status
	art.HLC = at
	art.Node = d.node
	// The row this node is about to have is the row it signs: a status move
	// changes one column and the reading, and both are inside the signature.
	if err := d.signArtifact(ctx, q, art); err != nil {
		return err
	}
	res, err := q.ExecContext(ctx,
		// author_sig is not in the SET list and must not be: a status move is
		// written by a party rather than by the owner, and status is outside what
		// the owner signs precisely so that this write carries their signature
		// forward instead of invalidating it. See
		// sign.CanonicalArtifactAuthorship.
		`UPDATE artifacts SET status = $2, hlc = $3, node = $4, sig = $5, updated = now()
		  WHERE id = $1 AND coalesce(tombstone, false) = false`,
		art.ID, art.Status, art.HLC, art.Node, art.Sig)
	if err != nil {
		return fmt.Errorf("store: set status of %s: %w", art.ID, err)
	}
	n, err := affectedRows(res)
	if err != nil {
		return fmt.Errorf("store: set status of %s: %w", art.ID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: set status: %w: artifact %s", ErrNotFound, art.ID)
	}
	return nil
}

// MoveArtifactStatus moves an artifact's status and writes the event that
// records the move, in one transaction and under one clock reading.
//
// The trail is the point of the lifecycle: "in-review" is worth something only
// because there is an entry saying who moved it there. Two statements with a
// gap in the middle meant the two could disagree - a status with no entry
// behind it if the append failed, which nothing ever notices and nothing ever
// repairs. They are one write now, and they carry the same reading, so a peer
// merging them sees the move and its record at the same point in the order.
func (d *DB) MoveArtifactStatus(
	ctx context.Context, art *Artifact, status string, e *Event,
) error {
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: move status of %s: %w", art.ID, err)
	}
	e.SeqHLC = at

	return d.inTx(ctx, "move status of "+art.ID, func(tx *sql.Tx) error {
		if err := d.setArtifactStatus(ctx, tx, art, status, at); err != nil {
			return err
		}
		return d.appendEvent(ctx, tx, e)
	})
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
