package store

// Attachments: an artifact with bytes.
//
// The artifact is the attachment - type, title, project, owner, visibility -
// and it is stored, listed and permission-filtered by exactly the code every
// other artifact rides. What is here is the payload and the two operations over
// it, and both are written so that the bytes cannot be reached by a route the
// artifact's own read filter does not decide.
//
// See the attachment_bytes table in schema.sql for why the payload is not in
// artifacts.body and not in the event log.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/deadtrickster/flowy/internal/otel"
)

// ErrNoBytes is what a read of an attachment whose artifact is here and whose
// content is not comes back as.
//
// It is deliberately not ErrNotFound. The two say different things and only one
// of them is about permission: ErrNotFound means the caller may not read this,
// or there is nothing to read, and the caller may not tell which - that is the
// whole of the read rule. This one is said only after the filter has already
// let the caller through, so it discloses nothing they did not have, and it is
// the honest answer for the case the fabric will produce: the artifact row
// replicates to a peer and the content does not.
var ErrNoBytes = errors.New("store: attachment has no bytes on this node")

// WriteAttachment writes the attachment artifact, its bytes and the event that
// records the write, in one transaction and under one clock reading - the way
// WriteMemory writes an item and its entry.
//
// It is a create and never an update. The artifact goes in through
// createArtifact, so an id already in the table is ErrTaken with nothing
// written, and there is no path here that replaces the bytes of an attachment
// that already exists. That is the point: somebody was handed an id and a
// digest for these bytes, and a log that changes under a reader who is
// debugging against it is the same failure as a log that was truncated on the
// way in - except that this one happens after they checked.
func (d *DB) WriteAttachment(ctx context.Context, a *Artifact, content []byte, e *Event) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "attachment.write")
	defer func() {
		span.SetArtifact(a.ID)
		span.End()
	}()
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: write attachment: %w", err)
	}
	d.fillAt(a, at)
	e.SeqHLC = at
	e.Artifact, e.Project = a.ID, a.Project

	return d.inTx(ctx, "write attachment "+a.ID, func(tx *sql.Tx) error {
		if err := d.createArtifact(ctx, tx, a); err != nil {
			return err
		}
		// A plain INSERT: the artifact create above has already established
		// that this id is new, so a collision here is this node contradicting
		// itself and is not something to paper over with ON CONFLICT.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO attachment_bytes (artifact, content) VALUES ($1, $2)`,
			a.ID, content); err != nil {
			return fmt.Errorf("store: write attachment %s: %w", a.ID, err)
		}
		return d.appendEvent(ctx, tx, e)
	})
}

// withExtra is a row scan with more destinations on the end of it, so a query
// that selects the artifact columns plus something else can still be read by
// scanArtifact rather than by a second hand-written copy of that column list.
type withExtra struct {
	sc    scanner
	extra []any
}

func (w withExtra) Scan(dest ...any) error { return w.sc.Scan(append(dest, w.extra...)...) }

// ReadAttachment returns the attachment and its bytes, if p may read it.
//
// One statement, and the permission filter is in the same WHERE clause as the
// content - the rule SearchArtifacts is written to for the same reason. The
// bytes are a new read path onto rows that already have a read rule, and a new
// path that fetched the payload by primary key and asked about permission
// separately would be a second, hand-written idea of that rule: two predicates
// drift, and the one that would have drifted here decides whether one agent
// reads another's captured output.
//
// The content is reached by subquery rather than by a join, and that is not a
// style choice: artifactColumns is one unqualified list shared by every read
// here, and attachment_bytes has a created column of its own, so joining the
// two makes `created` ambiguous and the statement is refused by the database.
// The way out of that with a join is a second, qualified copy of the column
// list, and a copy is the thing that drifts. Both subqueries are lookups on a
// primary key.
//
// "You may not read this" and "the content is not on this node" stay two
// answers, and which one a caller gets is decided by the filter first: no row
// at all is ErrNotFound, exactly as a read of the artifact would give, and a
// row with no content is ErrNoBytes and is only ever said to somebody the
// filter already let through.
func (d *DB) ReadAttachment(ctx context.Context, p *Principal, id string) (*Artifact, []byte, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "attachment.read")
	span.SetArtifact(id)
	defer span.End()

	a := &args{}
	idArg := a.next(id)
	filter := ArtifactFilterSQL(p, "ar", a, false)
	row := d.sql.QueryRowContext(ctx,
		`SELECT `+artifactColumns+`,
		        (SELECT ab.content FROM attachment_bytes ab WHERE ab.artifact = ar.id),
		        EXISTS (SELECT 1 FROM attachment_bytes ab WHERE ab.artifact = ar.id)
		   FROM artifacts ar
		  WHERE ar.id = `+idArg+` AND coalesce(ar.tombstone, false) = false AND `+filter,
		a.vals...)

	var (
		content  []byte
		hasBytes bool
	)
	art, err := scanArtifact(withExtra{sc: row, extra: []any{&content, &hasBytes}}, nil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: read attachment %s: %w", id, err)
	}
	if !hasBytes {
		return art, nil, ErrNoBytes
	}
	// A zero-length bytea scans as nil, and nil is what "there was no row"
	// looks like to the caller. has_bytes is the column that tells them apart,
	// so an empty payload comes back as an empty slice rather than as nothing.
	if content == nil {
		content = []byte{}
	}
	return art, content, nil
}

// AttachmentsMetaKey is the meta key a message's carried attachments ride in:
// the artifact ids, space separated, in the order they were named. Like
// mentions and citations it is inside the signature, because a relay that
// could rewrite which files a message carried would be a relay choosing what
// somebody was recorded as having handed over.
const AttachmentsMetaKey = "attachments"
