package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

// The keys a withdrawal rides in the artifact's fields: who took the row back,
// and when. They are the work queue's DidField and DidAtField wearing another
// name, and deliberately so - a finished work item and a withdrawn artifact are
// the same idea twice, a tombstone that carries WHO and WHEN so that "somebody
// did this" reads differently from "this never happened". Two mechanisms for
// one idea is how the two answers drift apart.
//
// They are in fields rather than in columns for the reason closed_at is: this
// is what became of the row, not who may see it.
const (
	WithdrawnByField = "withdrawn_by"
	WithdrawnAtField = "withdrawn_at"
)

// Withdrawal is what a reader is told about a row that was taken back: that it
// was, by whom, and when. Not the body, not the title, not the fields - none of
// that survives being withdrawn as far as a reader is concerned, and handing it
// over here would undo the whole point of the row no longer being the artifact.
type Withdrawal struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Kind string `json:"kind,omitempty"`
	// At is when the row was withdrawn, off WithdrawnAtField - and off the
	// row's updated column for a tombstone written before this key existed, or
	// merged from a peer that does not write it. The column is the fallback and
	// not the source: updated moves again every time a merge touches the row,
	// so a tombstone that has since replicated would otherwise report the last
	// time anything happened to it as the moment it was taken back.
	At time.Time `json:"at"`
	// Actor is the seat that withdrew it - the agent when the token named one,
	// the person behind it otherwise, which is voteActor's rule. Empty for a
	// tombstone that predates the key, and a reader is told when rather than
	// told a wrong who.
	Actor string `json:"actor,omitempty"`
}

// markWithdrawn stamps who is taking the row back and when onto its fields, and
// returns the column to write.
//
// It runs BEFORE the row is signed, because the node signature covers fields -
// see sign.CanonicalArtifact - so a withdrawal stamped after signing would be a
// row every peer refuses. The owner's own signature covers none of these, so it
// travels on untouched, exactly as it does through a status move.
func markWithdrawn(art *Artifact, p *Principal) ([]byte, error) {
	fields, err := ArtifactFields(art)
	if err != nil {
		return nil, err
	}
	if actor, _ := voteActor(p); actor != "" {
		fields[WithdrawnByField] = actor
	}
	fields[WithdrawnAtField] = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("store: withdraw %s: %w", art.ID, err)
	}
	return raw, nil
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
		`SELECT ar.id, ar.type, coalesce(ar.kind, ''), ar.updated,
		        ar.fields->>'`+WithdrawnByField+`', ar.fields->>'`+WithdrawnAtField+`'
		   FROM artifacts ar
		  WHERE ar.id = `+idArg+`
		    AND coalesce(ar.tombstone, false) = true
		    AND `+filter,
		a.vals...)

	w := &Withdrawal{}
	var updated sql.NullTime
	var actor, at sql.NullString
	if err := row.Scan(&w.ID, &w.Type, &w.Kind, &updated, &actor, &at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: read withdrawn %s: %w", id, err)
	}
	w.Actor = actor.String
	if updated.Valid {
		w.At = updated.Time
	}
	// The stamped moment wins over the column whenever the row carries one, for
	// the reason on Withdrawal.At. A stamp that does not parse is treated as a
	// stamp that is not there rather than as a broken row: the reader still gets
	// an answer, and the answer is still the truth to within a merge.
	if at.Valid {
		if parsed, err := time.Parse(time.RFC3339Nano, at.String); err == nil {
			w.At = parsed
		}
	}
	return w, nil
}
