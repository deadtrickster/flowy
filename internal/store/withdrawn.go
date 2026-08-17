package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// WITHDRAWN, AS A THING A READER CAN BE TOLD.
//
// A tombstoned row already survives - TombstoneArtifact marks it and every read
// carries coalesce(tombstone,false)=false, so a delete replicates as a fact
// rather than as an absence, and a peer that already holds the row cannot be
// made to forget it. That half was right from the start.
//
// The half that was missing is the READ. ReadArtifact returns ErrNotFound for a
// withdrawn row, deliberately and for a good reason recorded at artifacts.go:914
// - a deleted bug must stop BEING the artifact, or it still has a status to move
// and an edit resurrects it. That reason stands and this file does not touch it.
//
// What this adds is the sentence afterwards. Tonight twenty minutes went into
// ids that answered 404, and the actual cause was visibility=personal. The door
// gives one status code to three different truths:
//
//	never existed
//	withdrawn, and you could have read it
//	exists, and you may not read it
//
// AND THE THIRD MUST STAY INDISTINGUISHABLE FROM THE FIRST. Saying "this exists
// but is not for you" is an existence oracle: ids are guessable, so anyone could
// enumerate what a project holds by asking for ids and reading which refusal
// comes back. claude-host proposed distinguishing all three, then withdrew it
// himself, and flowy-claude reached the same constraint independently. So there
// are exactly two safe answers, and WHICH ONE YOU GET IS DECIDED BY THE
// PERMISSION CHECK RUNNING FIRST:
//
//	410, with who withdrew it and when - only to a principal the row would
//	     otherwise have been readable by
//	404 - never existed, AND exists-but-out-of-reach, forever identical
//
// Getting that order wrong is the leak, and it is one clause.

// Withdrawal is what a reader is told about a row that was taken back: that it
// was, by whom, and when. Not the body, not the title, not the fields - none of
// that survives being withdrawn as far as a reader is concerned, and handing it
// over here would undo the whole point of the row no longer being the artifact.
type Withdrawal struct {
	ID    string    `json:"id"`
	Type  string    `json:"type"`
	Kind  string    `json:"kind,omitempty"`
	At    time.Time `json:"at"`
	Actor string    `json:"actor,omitempty"`
}

// ErrWithdrawn is returned when the id names a row this principal COULD have
// read and which has been taken back. Callers turn it into 410; everything else
// stays 404.
var ErrWithdrawn = errors.New("store: artifact withdrawn")

// ReadWithdrawn answers "was this withdrawn, and may I be told so".
//
// It is asked ONLY after a normal read has come back ErrNotFound, which is what
// keeps it cheap and what keeps it honest: it never widens what a reader can
// see, it only explains an absence the reader has already hit.
//
// The permission filter is the same one every other read uses - not a relaxed
// copy of it. A copy is how the leak gets reintroduced by somebody tidying up
// six months from now, so this deliberately calls ArtifactFilterSQL rather than
// spelling out a condition of its own.
//
// Returns ErrNotFound when the row never existed, when it exists and is not
// withdrawn, or when the reader could not have read it anyway. All three are one
// answer on purpose.
func (d *DB) ReadWithdrawn(ctx context.Context, p *Principal, id string, scopeAll bool) (*Withdrawal, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "artifact.read.withdrawn")
	span.SetArtifact(id)
	defer span.End()

	a := &args{}
	idArg := a.next(id)
	// THE PERMISSION CLAUSE IS IN THE SAME WHERE AS THE TOMBSTONE TEST, so a row
	// out of reach cannot be distinguished from a row that is not there: both
	// return no rows, and the caller cannot tell which happened. That is the
	// leak-proof shape, and it is why this is one query rather than two.
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	row := d.sql.QueryRowContext(ctx,
		`SELECT ar.id, ar.type, coalesce(ar.kind, ''), ar.updated
		   FROM artifacts ar
		  WHERE ar.id = `+idArg+`
		    AND coalesce(ar.tombstone, false) = true
		    AND `+filter,
		a.vals...)

	w := &Withdrawal{}
	var updated sql.NullTime
	if err := row.Scan(&w.ID, &w.Type, &w.Kind, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: read withdrawn %s: %w", id, err)
	}
	if updated.Valid {
		w.At = updated.Time
	}
	return w, nil
}
