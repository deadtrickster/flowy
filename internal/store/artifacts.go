package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// searchConfig is the text search configuration the search column is built
// with. It has to be named explicitly rather than left to default_text_search_
// config, because a query built with one configuration does not match a vector
// built with another.
const searchConfig = "english"

// artifactSearchSQL is the expression that fills artifacts.search. The node
// writes the column in the same statement that writes the row - see the SEARCH
// section of schema.sql for why it is neither a generated column nor a trigger.
const artifactSearchSQL = "to_tsvector('" + searchConfig + "', $%d)"

// searchText is everything about an artifact that is worth matching on. tags
// are folded in with the prose so that a tag and a word in the body are found
// the same way, and discovery is in here so a word that appears only in what an
// agent discovered still finds the artifact.
func searchText(a *Artifact) string {
	parts := make([]string, 0, 4+len(a.Tags)+len(a.UserTags))
	parts = append(parts, a.Title, a.Body, a.Discovery)
	parts = append(parts, a.Tags...)
	parts = append(parts, a.UserTags...)
	return strings.Join(parts, " ")
}

// artifactColumns is the read list, in the order scanArtifact expects.
const artifactColumns = `id, type, kind, project, owner_user, title, body, discovery, status,
	severity, tags, user_tags, related, visibility, file_path, fields, hlc, node,
	tombstone, created, updated, reported, external, sig, author_sig, authorship`

// scanner is what both *sql.Row and *sql.Rows satisfy.
type scanner interface{ Scan(dest ...any) error }

// scanArtifact reads one row of artifactColumns, optionally followed by a rank.
func scanArtifact(sc scanner, rank *float64) (*Artifact, error) {
	var (
		a                                          Artifact
		typeCol, kind, project, owner, title, body sql.NullString
		disc, status, severity, vis, filePath      sql.NullString
		nodeCol, authorship                        sql.NullString
		fields, external                           []byte
		clockVal                                   sql.NullInt64
		tomb, reported                             sql.NullBool
	)
	dest := []any{&a.ID, &typeCol, &kind, &project, &owner, &title, &body, &disc, &status, &severity,
		pq.Array(&a.Tags), pq.Array(&a.UserTags), pq.Array(&a.Related), &vis, &filePath,
		&fields, &clockVal, &nodeCol, &tomb, &a.Created, &a.Updated, &reported, &external, &a.Sig,
		&a.AuthorSig, &authorship}
	if rank != nil {
		dest = append(dest, rank)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	// One of the two things this node can say, whatever the column holds - see
	// authorshipOr and scanEvent, which does the same.
	a.Authorship = authorshipOr(authorship.String)
	if project.Valid {
		p := project.String
		a.Project = &p
	}
	a.Type, a.Kind = typeCol.String, kind.String
	a.OwnerUser, a.Title, a.Body = owner.String, title.String, body.String
	a.Discovery, a.Status, a.Severity = disc.String, status.String, severity.String
	a.Visibility, a.FilePath, a.Node = vis.String, filePath.String, nodeCol.String
	a.HLC, a.Tombstone, a.Reported = clockVal.Int64, tomb.Bool, reported.Bool
	if len(fields) > 0 {
		a.Fields = json.RawMessage(fields)
	}
	// A forge link that will not parse is a link this node cannot act on, and
	// it is not a reason to fail the read of the artifact it is attached to.
	if len(external) > 0 {
		var ref ExternalRef
		if err := json.Unmarshal(external, &ref); err == nil && ref.Repo != "" {
			a.External = &ref
		}
	}
	return &a, nil
}

// UpsertArtifact writes an artifact, creating it when the id is new and
// replacing it when it is not. Either way the row is stamped with a fresh hlc
// reading and this node's name, so the write orders against writes from other
// nodes. Personal artifacts are forced to have no project, which is what makes
// the personal floor in CanRead an invariant of the data rather than a promise
// of the API.
//
// reported and external are not in the column list and are not in the ON
// CONFLICT clause: the forge link is written by SetArtifactExternal alone, so
// editing the title of a bug that has been filed cannot unfile it.
//
// The update branch only fires for the owner, and only on a row that is not
// deleted. An id is a guess anybody can make, and an unconditional ON CONFLICT
// DO UPDATE turns a guessed id into a takeover: every column, including
// owner_user, project and visibility, is replaced by whoever wrote last. So the
// ownership test is here rather than in the handler that happens to have read
// the row first - a caller who cannot read the row cannot be told about it
// either, and gets ErrNotFound, exactly as a read of it would. The tombstone
// test is the same rule for a row that has been deleted: it is not readable, so
// it is not writable, and an edit is not how something comes back.
//
// This is the update. A caller that did not read the row first wants
// CreateArtifact, which will not touch one that is already here.
func (d *DB) UpsertArtifact(ctx context.Context, a *Artifact) error {
	if err := d.fill(a); err != nil {
		return err
	}
	return d.upsertArtifact(ctx, d.sql, a)
}

// upsertArtifact is UpsertArtifact against whatever is in hand - the pool for a
// write on its own, a transaction for one that is half of an operation - and
// with the reading already stamped on the row by fill or fillAt.
func (d *DB) upsertArtifact(ctx context.Context, q execer, a *Artifact) error {
	// A local write lands in a project that was declared, or it does not land.
	// It is asked here rather than at the handler because every surface writes
	// through this one function - the API, the memory tools, the reports, the
	// FUSE drainer - and a rule kept per surface is a rule the next surface
	// forgets. See store.requireProject.
	if err := requireProjectPtr(ctx, q, a.Project); err != nil {
		return err
	}
	// The date the row will carry, decided before it is signed rather than by
	// the column's default afterwards - see createdNow. An update keeps the date
	// the row already has: an edit is not a new artifact, and the value has to
	// be the stored one or the signature would be over a date the row does not
	// have and no peer downstream could verify it.
	var held sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT created FROM artifacts WHERE id = $1`, a.ID).Scan(&held)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		a.Created = createdNow()
	case err != nil:
		return fmt.Errorf("store: upsert artifact: %w", err)
	case held.Valid:
		a.Created = held.Time
	default:
		a.Created = createdNow()
	}
	if err := d.signArtifact(ctx, q, a); err != nil {
		return err
	}

	var fields any
	if len(a.Fields) > 0 {
		fields = []byte(a.Fields)
	}
	err = q.QueryRowContext(ctx,
		`INSERT INTO artifacts (id, type, kind, project, owner_user, title, body, discovery,
		                        status, severity, tags, user_tags, related, visibility,
		                        file_path, fields, hlc, node, tombstone, search, sig, created,
		                        author_sig, authorship)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, $21, $22, $23, $24)
		 ON CONFLICT (id) DO UPDATE SET
		     type = excluded.type, kind = excluded.kind, project = excluded.project,
		     owner_user = excluded.owner_user,
		     title = excluded.title, body = excluded.body, discovery = excluded.discovery,
		     status = excluded.status, severity = excluded.severity, tags = excluded.tags,
		     user_tags = excluded.user_tags, related = excluded.related,
		     visibility = excluded.visibility, file_path = excluded.file_path,
		     fields = excluded.fields, hlc = excluded.hlc, node = excluded.node,
		     tombstone = excluded.tombstone, search = excluded.search, sig = excluded.sig,
		     author_sig = excluded.author_sig, authorship = excluded.authorship,
		     created = excluded.created, updated = now()
		  WHERE artifacts.owner_user = excluded.owner_user
		    AND coalesce(artifacts.tombstone, false) = false
		 RETURNING created, updated`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a), a.Sig,
		a.Created, a.AuthorSig, authorshipOr(a.Authorship)).
		Scan(&a.Created, &a.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		// The id is taken, and not by this owner. Nothing was written.
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: upsert artifact: %w", err)
	}
	return nil
}

// WriteMemory writes a memory item and the event that records the write, in one
// transaction and under one clock reading.
//
// The memory tools' write is two rows, like a status move and like an
// assignment: the item, and the entry in the log that says memory moved. Written
// one statement at a time they could disagree - a node that stopped between them
// left an item with nothing in the log behind it, permanently, because nothing
// here ever comes back to finish a half-written operation. Worse, the half
// replicates on its own: each row carries its own reading and a peer merges what
// it is given, so a peer paging the log sees a memory that was never written and
// a memory table that says otherwise.
//
// The event's artifact and project are taken from the item rather than from the
// caller: the id may only exist once fillAt has minted it, and an entry that
// named anything else would not be a record of this write.
//
// More than one event is allowed and means "these happened as one thing", the
// way SetArtifactFields takes several: a write that also says who is carrying the
// item leaves the assignment entry here, in the same transaction, so the log
// behind an assignee can never be missing the write that moved it. The first
// event shares the row's reading and the rest take their own - see
// SetArtifactFields, where that rule and the cursor it protects are written down.
func (d *DB) WriteMemory(ctx context.Context, a *Artifact, events ...*Event) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "memory.write")
	defer span.End()
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: write memory: %w", err)
	}
	d.fillAt(a, at)
	for i, e := range events {
		if i == 0 {
			e.SeqHLC = at
		}
		e.Artifact, e.Project = a.ID, a.Project
	}

	return d.inTx(ctx, "write memory "+a.ID, func(tx *sql.Tx) error {
		if err := d.upsertArtifact(ctx, tx, a); err != nil {
			return err
		}
		for _, e := range events {
			if err := d.appendEvent(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetArtifactFields replaces one artifact's fields column and writes the events
// that record the change, in one transaction and under one clock reading.
//
// It is setArtifactStatus's shape rather than an upsert, and for the same
// reason: this changes one column, and replacing the row would make "somebody
// took a todo" look to every peer that merges it like a rewrite of the whole
// artifact - title, body, tags and all - by whichever node the assignment
// happened to be made on.
//
// fields is inside the signature (see sign.CanonicalArtifact), so the row is
// re-signed here: a fields column that moved without the signature moving with
// it is a row that no longer verifies, which is a forgery by this node's own
// definition of one.
//
// A deleted artifact has no fields to set. The handlers gate on a filtered
// read, and the read is not the write - between the two the owner's delete can
// land - so the predicate says so and ErrNotFound is what the caller would have
// got had the delete landed a moment earlier.
//
// More than one event is allowed and means "these happened as one thing": an
// assignment is the entry that records it AND, when the room's panel is the door,
// the message that tells the room - see AssignTodo. Written one statement at a
// time they could disagree, and a node that stopped between them would leave a
// handover the room was never told about, permanently.
func (d *DB) SetArtifactFields(
	ctx context.Context, a *Artifact, fields json.RawMessage, events ...*Event,
) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "artifact.fields")
	defer func() {
		span.SetArtifact(a.ID)
		span.End()
	}()
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: set fields of %s: %w", a.ID, err)
	}
	a.Fields = fields
	a.HLC = at
	a.Node = d.node
	if err := d.signArtifact(ctx, d.sql, a); err != nil {
		return err
	}
	// Each event's artifact and project are taken from the item rather than from
	// the caller, as WriteMemory takes them: an entry naming anything else
	// would not be a record of this write, and a projectless event is readable
	// by its own actor and nobody else (see EventFilterSQL) - which for a
	// message announcing a change to a room's plan is the room never hearing.
	//
	// The FIRST event shares the row's reading, because it is the record OF this
	// write and the two are one point in the order. Any event after it is left
	// unstamped and takes the next reading in appendEvent, deliberately: a log
	// cursor is a seq_hlc and it is exclusive, so two events sharing one reading
	// are two events a page boundary can fall between - and the second of them
	// would then be skipped by every reader paging forwards, silently and
	// permanently.
	for i, e := range events {
		if i == 0 {
			e.SeqHLC = at
		}
		e.Artifact, e.Project = a.ID, a.Project
	}

	return d.setArtifactFields(ctx, a, "", events...)
}

// ErrGuardFailed is a conditional write whose condition did not hold: the row is
// there and readable, and it is not in the state the caller wrote against.
//
// It is its own error because the caller is the only one who can say what the
// failure MEANS. To a work queue's take it means somebody else got there first
// and this caller must be told they lost; to anything else it may mean retry.
var ErrGuardFailed = errors.New("store: the row was not in the state this write required")

// SetArtifactFieldsIf is SetArtifactFields with a COMPARE-AND-SET.
//
// guard is a boolean SQL fragment over the artifacts row, ANDed into the WHERE
// of the one UPDATE this makes, and it must be a literal in this package rather
// than anything off the wire. When it does not hold the write touches nothing,
// no event is appended, and the answer is ErrGuardFailed.
//
// A read-then-write cannot do this. Two callers both read "nobody has it",
// both write, both are told they succeeded, and the queue has manufactured the
// confidence that makes both of them act - which is the failure the work queue
// exists to prevent and the reason this is one statement rather than two.
func (d *DB) SetArtifactFieldsIf(
	ctx context.Context, a *Artifact, fields json.RawMessage, guard string, events ...*Event,
) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "artifact.fields.cas")
	defer func() {
		span.SetArtifact(a.ID)
		span.End()
	}()
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: set fields of %s: %w", a.ID, err)
	}
	a.Fields = fields
	a.HLC = at
	a.Node = d.node
	if err := d.signArtifact(ctx, d.sql, a); err != nil {
		return err
	}
	for i, e := range events {
		if i == 0 {
			e.SeqHLC = at
		}
		e.Artifact, e.Project = a.ID, a.Project
	}
	return d.setArtifactFields(ctx, a, guard, events...)
}

// setArtifactFields is the one write both doors make. The row has already been
// stamped and signed by the caller.
func (d *DB) setArtifactFields(
	ctx context.Context, a *Artifact, guard string, events ...*Event,
) error {
	return d.inTx(ctx, "set fields of "+a.ID, func(tx *sql.Tx) error {
		var column any
		if len(a.Fields) > 0 {
			column = []byte(a.Fields)
		}
		where := `id = $1 AND coalesce(tombstone, false) = false`
		if guard != "" {
			where += " AND " + guard
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE artifacts SET fields = $2, hlc = $3, node = $4, sig = $5, updated = now()
			  WHERE `+where,
			a.ID, column, a.HLC, a.Node, a.Sig)
		if err != nil {
			return fmt.Errorf("store: set fields of %s: %w", a.ID, err)
		}
		n, err := affectedRows(res)
		if err != nil {
			return fmt.Errorf("store: set fields of %s: %w", a.ID, err)
		}
		if n == 0 {
			return missingOrGuarded(ctx, tx, a.ID, guard, "set fields")
		}
		for _, e := range events {
			if err := d.appendEvent(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// missingOrGuarded says WHICH OF THE TWO a guarded write that touched nothing
// hit, because they are different facts and the caller acts differently on
// them: the row is gone, or the row is here and is not in the state this write
// required. A guarded write that reported ErrNotFound would tell a work queue
// its item had vanished when somebody had simply taken it, and would tell an
// editor their todo was deleted when somebody had started it.
//
// An unguarded write has only one of the two available to it, so it is answered
// as absent without the second query.
func missingOrGuarded(ctx context.Context, tx *sql.Tx, id, guard, what string) error {
	if guard != "" {
		var here bool
		if err := tx.QueryRowContext(ctx,
			`SELECT true FROM artifacts WHERE id = $1 AND coalesce(tombstone, false) = false`,
			id).Scan(&here); err == nil && here {
			return fmt.Errorf("%w: artifact %s", ErrGuardFailed, id)
		}
	}
	return fmt.Errorf("store: %s: %w: artifact %s", what, ErrNotFound, id)
}

// SetArtifactWordsIf replaces an artifact's title and body under a
// COMPARE-AND-SET, and writes the events that record the change, in one
// transaction and under one clock reading.
//
// It is SetArtifactFieldsIf for the other half of an item - the WORDS rather
// than the queue metadata - and it exists for the same reason: an edit written
// against a row that has moved is a lost update, and a read-then-write cannot
// tell one from an ordinary write. The editor read "nobody has started this",
// somebody started it, and the edit lands on top of whatever the person who
// picked it up was working from - with a 200 telling both of them they
// succeeded. Here the guard is in the WHERE, so a moved row touches nothing, no
// event is appended, and the answer is ErrGuardFailed for the caller to turn
// into a sentence naming who moved it.
//
// guard is a boolean SQL fragment over the artifacts row and must be a literal
// in this package rather than anything off the wire.
//
// THREE COLUMNS MOVE THAT A STATUS MOVE LEAVES ALONE. search, because a title
// nobody can find is worse than the old one; author_sig, because title and body
// are INSIDE the owner's own signature - see sign.CanonicalArtifactAuthorship -
// so an edit that carried the old author signature forward would leave a row
// that no longer verifies, which is this fabric's own definition of a forgery;
// and authorship with it, since what this node can say about the row is decided
// by whether it could make that signature. signArtifact settles both.
func (d *DB) SetArtifactWordsIf(
	ctx context.Context, a *Artifact, title, body, guard string, events ...*Event,
) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "artifact.words.cas")
	defer func() {
		span.SetArtifact(a.ID)
		span.End()
	}()
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: set words of %s: %w", a.ID, err)
	}
	a.Title, a.Body = title, body
	a.HLC = at
	a.Node = d.node
	// The pool, not a transaction: the signature is settled before inTx opens
	// one below, so there is nothing in hand yet to sign against.
	if err := d.signArtifact(ctx, d.sql, a); err != nil {
		return err
	}
	// The first event shares the row's reading and the rest take their own, for
	// the reason written down in SetArtifactFields: two events under one reading
	// are two events a page boundary can fall between.
	for i, e := range events {
		if i == 0 {
			e.SeqHLC = at
		}
		e.Artifact, e.Project = a.ID, a.Project
	}

	return d.inTx(ctx, "set words of "+a.ID, func(tx *sql.Tx) error {
		where := `id = $1 AND coalesce(tombstone, false) = false`
		if guard != "" {
			where += " AND " + guard
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE artifacts
			    SET title = $2, body = $3, search = `+fmt.Sprintf(artifactSearchSQL, 4)+`,
			        hlc = $5, node = $6, sig = $7, author_sig = $8, authorship = $9,
			        updated = now()
			  WHERE `+where,
			a.ID, a.Title, a.Body, searchText(a), a.HLC, a.Node, a.Sig, a.AuthorSig,
			authorshipOr(a.Authorship))
		if err != nil {
			return fmt.Errorf("store: set words of %s: %w", a.ID, err)
		}
		n, err := affectedRows(res)
		if err != nil {
			return fmt.Errorf("store: set words of %s: %w", a.ID, err)
		}
		if n == 0 {
			return missingOrGuarded(ctx, tx, a.ID, guard, "set words")
		}
		for _, e := range events {
			if err := d.appendEvent(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// CreateArtifact writes an artifact that is not here yet, and never writes over
// one that is. An id already in the table comes back as ErrTaken with nothing
// written at all.
//
// It exists because "the read found nothing" and "there is nothing there" are
// not the same sentence. A read is permission-filtered, so an artifact in
// another project, or a deleted one, reads as absent - and a create that
// treated absent as free took the update branch of the upsert on the strength
// of owning the row it could not see. The owner of a personal artifact in one
// project, holding a token for another, could POST its id and watch it move
// project, lose every field the request left out and come back from the dead.
// The id is the only thing a caller can be told about a row it cannot read, and
// even that is told as a 404.
func (d *DB) CreateArtifact(ctx context.Context, a *Artifact) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "artifact.create")
	defer func() {
		span.SetArtifact(a.ID)
		span.End()
	}()
	if err := d.fill(a); err != nil {
		return err
	}
	return d.createArtifact(ctx, d.sql, a)
}

// createArtifact is CreateArtifact against whatever is in hand, with the
// reading already stamped on the row - the pool for a create on its own, a
// transaction for one that is half of an operation. It is upsertArtifact's
// counterpart, and it is split out for the same reason: an operation that
// writes an artifact and something else as one thing has to write both through
// the same tx, and the alternative is a second copy of this INSERT.
func (d *DB) createArtifact(ctx context.Context, q execer, a *Artifact) error {
	if err := requireProjectPtr(ctx, q, a.Project); err != nil {
		return err
	}
	// Minted here and signed with the row, not left to the column - see
	// createdNow. A create is always a new row, so there is no stored date to
	// keep and none is taken from the caller.
	a.Created = createdNow()
	if err := d.signArtifact(ctx, q, a); err != nil {
		return err
	}

	var fields any
	if len(a.Fields) > 0 {
		fields = []byte(a.Fields)
	}
	err := q.QueryRowContext(ctx,
		`INSERT INTO artifacts (id, type, kind, project, owner_user, title, body, discovery,
		                        status, severity, tags, user_tags, related, visibility,
		                        file_path, fields, hlc, node, tombstone, search, sig, created,
		                        author_sig, authorship)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, $21, $22, $23, $24)
		 ON CONFLICT (id) DO NOTHING
		 RETURNING created, updated`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a), a.Sig,
		a.Created, a.AuthorSig, authorshipOr(a.Authorship)).
		Scan(&a.Created, &a.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaken
	}
	if err != nil {
		return fmt.Errorf("store: create artifact: %w", err)
	}
	return nil
}

// fill is what both writes do to an artifact before it goes in: force the
// personal floor to be a property of the row, mint an id when the caller left
// one out, and stamp a fresh reading and this node's name.
func (d *DB) fill(a *Artifact) error {
	// A fresh reading on every write, including an update - the previous value
	// is what a peer already saw. A clock that has none left to give is the one
	// thing that stops the write here: two rows under one reading is a merge
	// quietly keeping one of them.
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	d.fillAt(a, at)
	return nil
}

// fillAt is fill under a reading already taken. An operation that writes an
// artifact and an event as one thing takes the reading once and stamps it on
// both, so a peer merging them sees them at the same point in the order rather
// than as two writes that happened to be near each other.
func (d *DB) fillAt(a *Artifact, at int64) {
	if a.Visibility == "" {
		a.Visibility = "project"
	}
	if a.Visibility == "personal" {
		a.Project = nil
	}
	if a.Project == nil {
		// A row with no project cannot be a project artifact: there is no
		// project to read it. That is true of 'shared' and of anything else
		// written over a NULL project, not only of the two project-scoped
		// visibilities - CanRead and ArtifactFilterSQL both stop at the owner
		// on project IS NULL whatever the column says, so the column says it
		// too rather than describing a reach the row does not have.
		a.Visibility = VisibilityPersonal
	}
	a.HLC = at
	a.Node = d.node
	if a.ID == "" {
		a.ID = ulid.NewString()
	}
}

// RoomField is where an artifact records the chat room it belongs to: a key in
// fields, the way as_of and supersedes ride a report, rather than a column.
//
// It is a filter and nothing else. A todo raised in #build is the same
// project-scoped row it would be with no room on it, readable by exactly the
// principals that could read it before - the permission filter never looks at
// this key, and a room is not a visibility. What it buys is the panel beside a
// conversation: which of the project's todos came out of this room.
const RoomField = "room"

// RoomOf is the room an artifact belongs to, or "" for one that names none.
//
// It is one function rather than one per surface because the key is one key:
// a todo, a proposal and the entry a dependency leaves in the log all read it,
// and three readers of the same key are three chances to disagree about what an
// absent room means.
func RoomOf(a *Artifact) string {
	room, _ := artifactField(a, RoomField).(string)
	return room
}

// ArtifactFields reads an artifact's fields as a map a write can edit one key of,
// and an empty one for a row that carries none. Unlike artifactField below it
// REFUSES fields that do not parse rather than reading them as absent: a caller
// about to write the column back would otherwise drop every key it did not know
// about, which is a row silently losing its room and the message it was raised
// from.
func ArtifactFields(a *Artifact) (map[string]any, error) {
	fields := map[string]any{}
	if a == nil || len(a.Fields) == 0 {
		return fields, nil
	}
	if err := json.Unmarshal(a.Fields, &fields); err != nil {
		return nil, fmt.Errorf("store: artifact %s carries fields that do not parse: %w", a.ID, err)
	}
	if fields == nil {
		// A fields column holding a literal `null` parses into a nil map, and a
		// caller that wrote a key into that would panic.
		fields = map[string]any{}
	}
	return fields, nil
}

// artifactField reads one key out of an artifact's fields, and nil for a row
// that carries none or carries JSON that does not parse. A row whose fields are
// unreadable is a row that says nothing about this key, which is what every
// caller here already does with one.
func artifactField(a *Artifact, key string) any {
	if a == nil || len(a.Fields) == 0 {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal(a.Fields, &fields); err != nil {
		return nil
	}
	return fields[key]
}

// SupersedesField is where a report names the report it replaces: a key in
// fields, beside as_of, for the reason RoomField is one.
//
// It points backwards, from the new document at the old one, because that is
// the only direction the writer can name - the replacement exists and the
// thing it replaces already did. The question a reader has is the other one:
// they are looking at a report and want to know whether it still stands. No
// column on the old row answers that, and nothing writes one, so it is derived
// on the way out - see replacedBy.
//
// The value is a bare id, and under the (project, type, id) ruling
// (01M08FK999F2JWY9RQV5VC821N) it stays one HERE: this key rides inside
// fields on a signed, replicated artifact row, and every node and every row
// already written agrees on that shape - widening it breaks federation.
// What must NOT stay a bare id is what NEW code does with the answer once
// replacedBy has resolved it: replacedBy already has the replacement row in
// hand, permission-checked, and a caller assuming the replacement shares the
// original's project and type - rather than reading them off that row with
// RefOf - is exactly the guess this ruling exists to rule out. (A supersedes
// chain is not guaranteed to stay inside one project or one artifact type;
// nothing here enforces either.)
const SupersedesField = "supersedes"

// MessageField is the chat message an artifact was raised out of, kept beside
// the room. It is the link a ticket in another system loses: the item says what
// is to be done and the message says what was being talked about when somebody
// decided it had to be.
const MessageField = "message"

// AssigneeField is who is carrying the work, beside the room it was agreed in.
//
// It is a claim somebody made, not a principal: a handle, whatever the person
// or agent doing the work is called around here, and the node resolves it to
// nothing. owner_user is already on the row and answers a different question -
// who wrote it - which for a whole queue filed by one operator is the same id
// every time and tells nobody who has what.
//
// It rides fields for the reason the room does, and with the same consequence:
// the permission filter never looks at this key, so naming somebody in it hands
// them nothing. Being assigned a todo you cannot read leaves you unable to read
// it; the surface that hands over a readable copy of something is an assignment
// and a share and a task, and it is POST /api/assign, which is a different verb
// on purpose.
//
// A key that is present and empty is not the same as no key at all: it is
// somebody saying out loud that nobody is carrying this, and it has to outrank
// the `OWNER:` line that the queue was written with before this field existed.
// So a read that finds the key takes it verbatim, and only a read that finds no
// key at all falls back to the body.
const AssigneeField = "assignee"

// nobodyWords are the ways this queue has said "nobody is carrying this". They
// all collapse to the empty assignee, so every surface says ONE word for one
// state.
//
// Raised as a todo through the panel itself: 'todo list has "unowned" and
// "unassigned" - looks identical'. Two words for one state read as two states,
// and a reader goes looking for a distinction that is not there.
//
// It is here rather than beside the HTTP surface that first had it because the
// ready query reads the same key: whether a todo is carried is half of whether
// it can be started, and a queue that answered "nobody" on one surface and "n/a"
// on another would be a queue where one of the two is ready and the other is
// not. The console keeps its own list in web/src/lib/todos.ts for the bodies
// that were written before the field existed.
var nobodyWords = map[string]bool{
	"?": true, "-": true, "none": true, "nobody": true,
	"tbd": true, "unassigned": true, "unowned": true, "n/a": true,
}

// NobodyName reports whether name is one of them.
func NobodyName(name string) bool { return nobodyWords[strings.ToLower(strings.TrimSpace(name))] }

// AssigneeOf is who a todo says is carrying it: the field if it has one, and the
// body's OWNER line if it does not.
//
// The order is the compatibility. Every todo in this queue was written before
// there was a field, with `OWNER: <name>` as the first line of the body, and
// those still read the way they always did. But a key that is there wins even
// when it is empty - somebody said out loud that nobody is carrying this, and a
// read that fell through to a stale OWNER line would quietly undo them.
//
// This is the current value and nothing else. WHO put it there and WHEN is the
// log's answer, not the row's - see AssignTodo, which writes the two together so
// that this function and that log cannot disagree.
func AssigneeOf(a *Artifact) string {
	if named := artifactField(a, AssigneeField); named != nil {
		name, _ := named.(string)
		return strings.TrimSpace(name)
	}
	if a == nil {
		return ""
	}
	return ownerLine(a.Body)
}

// ownerLine reads the convention: `OWNER: <name>` as the FIRST line of the body.
// Further down it is a sentence about somebody else's item, not a claim about
// this one, which is the same read the TUI and the console each make.
func ownerLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if rest, found := strings.CutPrefix(line, "OWNER:"); found {
			name := strings.TrimSpace(rest)
			if NobodyName(name) {
				return ""
			}
			return name
		}
		if line != "" {
			return ""
		}
	}
	return ""
}

// ArtifactQuery narrows a list or a search. Every field is optional; the
// permission filter is not, and is added by the methods below.
type ArtifactQuery struct {
	Type      string
	Kind      string   // one kind
	Kinds     []string // any of these kinds; ORed with nothing, ANDed with the rest
	Project   string
	Status    string
	NotStatus string // exclude one status - what "still open" means for a todo
	Room      string // the chat room the artifact was raised in - fields->>'room'
	// Category narrows to one kind of work out of the closed set - see
	// TodoCategories. This is the routing half of what a closed set is FOR: "give
	// me the bugs" has to be a query the node answers rather than a filter each
	// client writes over a free-text column, or the ontology is decoration.
	Category   string
	Visibility string // personal|project|shared - the memory scopes
	Query      string // free text; SearchArtifacts only
	ScopeAll   bool   // ?scope=all - honoured only for the operator principal
	Limit      int
}

const defaultLimit = 200

// maxLimit is the most rows one page of a list or a search will hand back,
// however many were asked for.
const maxLimit = 1000

// clampLimit turns an asked-for page size into the one that runs: absent means
// the default, and more than the cap means the cap.
//
// Over the cap used to mean the default, which reads as an answer and is not
// one: a caller asking for 5000 got 200 rows with nothing said about it, and a
// short page means "that was all of them" everywhere else here - so it stopped
// at 200 believing it had the lot. Clamping to the cap keeps the ceiling and
// keeps a short page meaning what it means.
func clampLimit(asked int) int {
	switch {
	case asked <= 0:
		return defaultLimit
	case asked > maxLimit:
		return maxLimit
	default:
		return asked
	}
}

func (q ArtifactQuery) limit() int { return clampLimit(q.Limit) }

// PageLimit is that same number, for a caller that has to know whether a full
// page means "that is all of them". Limit as asked is not it: zero means the
// default and over the cap means the cap, so a caller comparing against the
// asked-for number would call every default-sized answer truncated. Exported
// rather than duplicated, because a second copy of clampLimit's rule is a second
// thing to keep in step.
func (q ArtifactQuery) PageLimit() int { return q.limit() }

// narrow appends the caller's filters - the ones that are about what they asked
// for rather than what they may see.
func (q ArtifactQuery) narrow(a *args, alias string) string {
	where := ""
	if q.Type != "" {
		where += " AND " + alias + ".type = " + a.next(q.Type)
	}
	if q.Project != "" {
		where += " AND " + alias + ".project = " + a.next(q.Project)
	}
	if q.Status != "" {
		where += " AND " + alias + ".status = " + a.next(q.Status)
	}
	if q.Kind != "" {
		where += " AND " + alias + ".kind = " + a.next(q.Kind)
	}
	if len(q.Kinds) > 0 {
		holders := make([]string, 0, len(q.Kinds))
		for _, k := range q.Kinds {
			holders = append(holders, a.next(k))
		}
		where += " AND " + alias + ".kind IN (" + strings.Join(holders, ", ") + ")"
	}
	if q.Visibility != "" {
		where += " AND " + alias + ".visibility = " + a.next(q.Visibility)
	}
	if q.NotStatus != "" {
		// coalesce, because a row that was never given a status is not done.
		where += " AND coalesce(" + alias + ".status, '') <> " + a.next(q.NotStatus)
	}
	if q.Room != "" {
		// The room the artifact was raised in, out of fields. A row with no
		// room drops out of a narrowed list and stays in every unnarrowed one,
		// which is what makes this a filter: the todos that predate the field
		// are global, and they are still on the page that shows all of them.
		where += " AND " + alias + ".fields->>'" + RoomField + "' = " + a.next(q.Room)
	}
	if q.Category != "" {
		// What kind of work it is, out of fields, and narrowing exactly as the
		// room does: a todo nobody classified drops out of a narrowed list and
		// stays in every unnarrowed one. The index that keeps this off a
		// sequential scan is artifacts_category_idx, and it is partial for the
		// same reason the supersedes one is - the rows carrying the key are the
		// minority while the queue catches up.
		where += " AND " + alias + ".fields->>'" + CategoryField + "' = " + a.next(q.Category)
	}
	return where
}

// ListArtifacts returns the artifacts p may read, newest first. Tombstoned rows
// are gone from the list; they stay in the table so the delete can replicate.
func (d *DB) ListArtifacts(ctx context.Context, p *Principal, q ArtifactQuery) ([]*Artifact, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "artifacts.list")
	defer span.End()
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, q.ScopeAll)
	query := `SELECT ` + artifactColumns + `
	            FROM artifacts ar
	           WHERE coalesce(ar.tombstone, false) = false
	             AND ` + filter + q.narrow(a, "ar") + `
	           ORDER BY ar.updated DESC, ar.id DESC
	           LIMIT ` + a.next(q.limit())

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		span.Fail("the list did not run")
		return nil, fmt.Errorf("store: list artifacts: %w", err)
	}
	defer rows.Close()

	out := []*Artifact{}
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, fmt.Errorf("store: list artifacts: %w", err)
		}
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list artifacts: %w", err)
	}
	if err := d.replacedBy(ctx, p, out, q.ScopeAll); err != nil {
		return nil, err
	}
	fillAssignee(out)
	fillCategory(out)
	return out, nil
}

// fillAssignee puts the current assignee on the row itself, beside Status.
//
// No query: the value is already on the artifact, either in the fields key or in
// the body's legacy OWNER line, and AssigneeOf is the one function that decides
// between them. Calling it here rather than leaving every reader to look means
// there is one answer to "who has this" instead of one per client - and the
// clients that rolled their own got it wrong, which is why this exists.
//
// It is filled beside ReplacedBy, in the permission-filtered read paths only, so
// a derived value never reaches the sync paths or the signature.
func fillAssignee(arts []*Artifact) {
	for _, art := range arts {
		if art != nil {
			art.Assignee = AssigneeOf(art)
		}
	}
}

// fillCategory puts what kind of work the item is on the row itself, beside
// Status and Assignee.
//
// It is fillAssignee one field along and for fillAssignee's reason: the three
// facts a queue is read by must come back in one shape from one read, or each
// client digs for the ones that are nested and some of them dig wrong. No query
// - the value is already on the artifact, under CategoryField.
//
// Called beside fillAssignee in the permission-filtered read paths only, so a
// derived value never reaches the sync paths or the signature. An item nobody
// has classified is left empty, which is what it is.
func fillCategory(arts []*Artifact) {
	for _, art := range arts {
		if art != nil {
			art.Category = CategoryOf(art)
		}
	}
}

// replacedBy fills ReplacedBy on every artifact in arts that a readable
// artifact supersedes, and leaves the rest empty.
//
// The pointer is written on the newer row under SupersedesField, so the
// backward question is a lookup by that key, and it is asked here rather than
// through a door of its own for one reason: ArtifactFilterSQL. The answer is
// another artifact's id, and handing it to a reader who may not read the
// replacement would leak both that it exists and what it is called, out of a
// row they are entitled to see. So the filter is on the REPLACEMENT, in the
// same WHERE clause as the match. A reader who cannot reach the newer report
// is told nothing about it, which is the answer they already get everywhere
// else.
//
// It fills ReplacedByRef in the same pass, off the same row, because the id
// alone does not tell a reader where the replacement lives - see the field's
// comment in store.go. The query already has the replacement row; reading its
// project and type out of it costs nothing here and is the only place that
// knows them.
//
// One query for the whole page rather than one per row, and none at all when
// the page is empty.
func (d *DB) replacedBy(ctx context.Context, p *Principal, arts []*Artifact, scopeAll bool) error {
	ids := make([]string, 0, len(arts))
	for _, art := range arts {
		if art != nil && art.ID != "" {
			ids = append(ids, art.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	a := &args{}
	idsArg := a.next(pq.Array(ids))
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT ar.fields->>'`+SupersedesField+`', ar.id, ar.project, ar.type
		   FROM artifacts ar
		  WHERE ar.fields->>'`+SupersedesField+`' = ANY(`+idsArg+`)
		    AND coalesce(ar.tombstone, false) = false
		    AND `+filter+`
		  ORDER BY ar.updated ASC, ar.id ASC`,
		a.vals...)
	if err != nil {
		return fmt.Errorf("store: read what replaced these artifacts: %w", err)
	}
	defer rows.Close()

	// Oldest first and overwritten as they come, so a report that was replaced
	// twice names the newest replacement - which is the one worth reading, and
	// the one that is itself unreplaced.
	type replacement struct {
		id  string
		ref string
	}
	newer := map[string]replacement{}
	for rows.Next() {
		var old, id, typ string
		var project sql.NullString
		if err := rows.Scan(&old, &id, &project, &typ); err != nil {
			return fmt.Errorf("store: read what replaced these artifacts: %w", err)
		}
		found := replacement{id: id}
		// project/type/id, the three segments the console's route is made of,
		// built from THE REPLACEMENT's own row rather than from the row being
		// read - a supersedes chain is not held to one project or one type, and
		// the caller has no way to tell it left either. A replacement personal
		// to its author has no project, and a reference without one names no
		// route anybody else can follow, so it gets no ref at all: the id is
		// still there, and a reader is told the truth rather than sent
		// somewhere wrong.
		if project.Valid && project.String != "" && typ != "" {
			found.ref = project.String + "/" + typ + "/" + id
		}
		newer[old] = found
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: read what replaced these artifacts: %w", err)
	}
	for _, art := range arts {
		if art != nil {
			found := newer[art.ID]
			art.ReplacedBy = found.id
			art.ReplacedByRef = found.ref
		}
	}
	return nil
}

// Ranked is an artifact with the search score that found it.
type Ranked struct {
	*Artifact
	Rank float64 `json:"rank"`
}

// SearchArtifacts ranks the artifacts p may read against a free text query.
// The permission filter is in the same WHERE clause as the match, so an
// artifact the principal cannot see does not occupy a result slot and does not
// influence anything - filtering afterwards would leak both.
func (d *DB) SearchArtifacts(ctx context.Context, p *Principal, q ArtifactQuery) ([]Ranked, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "artifacts.search")
	defer span.End()
	if strings.TrimSpace(q.Query) == "" {
		return []Ranked{}, nil
	}
	a := &args{}
	tsq := "plainto_tsquery('" + searchConfig + "', " + a.next(q.Query) + ")"
	filter := ArtifactFilterSQL(p, "ar", a, q.ScopeAll)
	query := `SELECT ` + artifactColumns + `, ts_rank_cd(ar.search, ` + tsq + `) AS rank
	            FROM artifacts ar
	           WHERE coalesce(ar.tombstone, false) = false
	             AND ar.search @@ ` + tsq + `
	             AND ` + filter + q.narrow(a, "ar") + `
	           ORDER BY rank DESC, ar.updated DESC, ar.id DESC
	           LIMIT ` + a.next(q.limit())

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: search artifacts: %w", err)
	}
	defer rows.Close()

	out := []Ranked{}
	for rows.Next() {
		var rank float64
		art, err := scanArtifact(rows, &rank)
		if err != nil {
			return nil, fmt.Errorf("store: search artifacts: %w", err)
		}
		out = append(out, Ranked{Artifact: art, Rank: rank})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: search artifacts: %w", err)
	}
	// A hit says whether it still stands, because a search is where a stale
	// report is most likely to be found: the words that matched are in the old
	// document as readily as in the one that replaced it.
	found := make([]*Artifact, 0, len(out))
	for _, hit := range out {
		found = append(found, hit.Artifact)
	}
	if err := d.replacedBy(ctx, p, found, q.ScopeAll); err != nil {
		return nil, err
	}
	fillAssignee(found)
	fillCategory(found)
	return out, nil
}

// ReadArtifact returns the artifact only if p may read it. A row that exists
// but is out of reach comes back as ErrNotFound, the same as a row that was
// never there: the caller has no way to tell, and neither does its user.
//
// A deleted artifact is one of those rows. The tombstone stays in the table so
// the delete can replicate as a fact rather than as an absence - see
// TombstoneArtifact - but it is not the artifact any more, and every read went
// through here: a deleted bug was still readable by id, still had a status to
// move, still had a history and could still be filed as an issue. Worse, an
// edit of one was a resurrection - the update stamped a fresh reading that beat
// the delete on every peer - so deleting something and editing it back was a
// race the writer won. Coming back is a thing to do on purpose, not a side
// effect of a write that never mentioned it.
func (d *DB) ReadArtifact(ctx context.Context, p *Principal, id string, scopeAll bool) (*Artifact, error) {
	// The span names the artifact it is about, which is the link between a
	// trace and the row the work produced: an agent's transcript, the bug it
	// filed, the memory it wrote.
	ctx, span := otel.Start(ctx, otel.KindQuery, "artifact.read")
	span.SetArtifact(id)
	defer span.End()
	a := &args{}
	idArg := a.next(id)
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	row := d.sql.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts ar
		  WHERE ar.id = `+idArg+` AND coalesce(ar.tombstone, false) = false AND `+filter,
		a.vals...)

	art, err := scanArtifact(row, nil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read artifact %s: %w", id, err)
	}
	// Whoever opened this is the reader the mark is for: a superseded report
	// read on its own page is the case where "it has been replaced" changes
	// what somebody does next.
	if err := d.replacedBy(ctx, p, []*Artifact{art}, scopeAll); err != nil {
		return nil, err
	}
	fillAssignee([]*Artifact{art})
	fillCategory([]*Artifact{art})
	return art, nil
}

// UnreadableArtifacts returns the ids that do not name an artifact this
// principal may read - one that is not here, one that has been deleted, and one
// that is here and out of reach, which are the same answer for the same reason
// every filtered read gives it. Duplicates are collapsed and the caller's order
// is kept, so a refusal names the first one they wrote.
//
// It is UnreadableParents for the other column: an id in a list somebody handed
// over, checked through the read filter before it is stored as a reference. The
// worklog is what wants it - an entry references the work it is about by
// artifact id rather than describing it in prose, which is only an index into
// the fabric if the writer could read what it points at. An id is a guess
// anybody can make, and a reference to a row the writer cannot see is either a
// dangling pointer or an assertion about somebody else's work.
//
// One query for the whole list, so a long list is not a query per id.
func (d *DB) UnreadableArtifacts(ctx context.Context, p *Principal, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if p == nil {
		return ids, nil
	}
	a := &args{}
	idsArg := a.next(pq.Array(ids))
	filter := ArtifactFilterSQL(p, "ar", a, false)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT ar.id FROM artifacts ar
		  WHERE ar.id = ANY(`+idsArg+`) AND coalesce(ar.tombstone, false) = false AND `+filter,
		a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: read artifact refs: %w", err)
	}
	defer rows.Close()

	readable := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: read artifact refs: %w", err)
		}
		readable[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read artifact refs: %w", err)
	}

	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		if readable[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// TombstoneArtifact marks an artifact deleted and bumps its clock, so the
// delete orders after the writes it removes and can replicate as a row rather
// than as an absence. It returns ErrNotFound when p cannot read the artifact.
//
// The update names the caller as well as the id, and a delete is the owner's.
// Two reasons, and the second is the one that bites: the read and the update
// are not one statement, so a merge can land between them and change the row's
// owner, and the delete would then be carried out on the strength of a read of
// somebody else's artifact. The predicate is in the statement that writes, so
// the row it finds is the row it was allowed to find - and when it finds none,
// that is ErrNotFound, which is what a read of a row the caller may not touch
// says too.
func (d *DB) TombstoneArtifact(ctx context.Context, p *Principal, id string) (*Artifact, error) {
	art, err := d.ReadArtifact(ctx, p, id, false)
	if err != nil {
		return nil, err
	}
	if p == nil || p.UserID == "" {
		return nil, ErrNotFound
	}
	art.Tombstone = true
	at, err := d.clock.Pack()
	if err != nil {
		return nil, fmt.Errorf("store: tombstone artifact %s: %w", id, err)
	}
	art.HLC = at
	art.Node = d.node
	// WHO TOOK IT BACK, AND WHEN, travel in the row itself - see markWithdrawn.
	// A tombstone that records only that it is a tombstone can be read back as
	// "gone" and nothing else, and "somebody withdrew this at 23:14" is a
	// different answer from "this never happened" only if the row is carrying
	// the difference. It is stamped before the signature because the signature
	// covers fields.
	fields, err := markWithdrawn(art, p)
	if err != nil {
		return nil, err
	}
	art.Fields = fields
	// The delete is a write like any other and travels as a row, so it is signed
	// as one: a tombstone nobody signed is a delete a peer could have invented.
	if err := d.signArtifact(ctx, d.sql, art); err != nil {
		return nil, err
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE artifacts SET tombstone = true, hlc = $2, node = $3, sig = $5, fields = $6,
		        updated = now()
		  WHERE id = $1 AND coalesce(owner_user, '') = $4`,
		art.ID, art.HLC, art.Node, p.UserID, art.Sig, []byte(art.Fields))
	if err != nil {
		return nil, fmt.Errorf("store: tombstone artifact %s: %w", id, err)
	}
	n, err := affectedRows(res)
	if err != nil {
		return nil, fmt.Errorf("store: tombstone artifact %s: %w", id, err)
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return art, nil
}
