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
	tombstone, created, updated, reported, external, sig`

// scanner is what both *sql.Row and *sql.Rows satisfy.
type scanner interface{ Scan(dest ...any) error }

// scanArtifact reads one row of artifactColumns, optionally followed by a rank.
func scanArtifact(sc scanner, rank *float64) (*Artifact, error) {
	var (
		a                                          Artifact
		typeCol, kind, project, owner, title, body sql.NullString
		disc, status, severity, vis, filePath      sql.NullString
		nodeCol                                    sql.NullString
		fields, external                           []byte
		clockVal                                   sql.NullInt64
		tomb, reported                             sql.NullBool
	)
	dest := []any{&a.ID, &typeCol, &kind, &project, &owner, &title, &body, &disc, &status, &severity,
		pq.Array(&a.Tags), pq.Array(&a.UserTags), pq.Array(&a.Related), &vis, &filePath,
		&fields, &clockVal, &nodeCol, &tomb, &a.Created, &a.Updated, &reported, &external, &a.Sig}
	if rank != nil {
		dest = append(dest, rank)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
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
	if err := d.signArtifact(ctx, a); err != nil {
		return err
	}

	var fields any
	if len(a.Fields) > 0 {
		fields = []byte(a.Fields)
	}
	err = q.QueryRowContext(ctx,
		`INSERT INTO artifacts (id, type, kind, project, owner_user, title, body, discovery,
		                        status, severity, tags, user_tags, related, visibility,
		                        file_path, fields, hlc, node, tombstone, search, sig, created)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, $21, $22)
		 ON CONFLICT (id) DO UPDATE SET
		     type = excluded.type, kind = excluded.kind, project = excluded.project,
		     owner_user = excluded.owner_user,
		     title = excluded.title, body = excluded.body, discovery = excluded.discovery,
		     status = excluded.status, severity = excluded.severity, tags = excluded.tags,
		     user_tags = excluded.user_tags, related = excluded.related,
		     visibility = excluded.visibility, file_path = excluded.file_path,
		     fields = excluded.fields, hlc = excluded.hlc, node = excluded.node,
		     tombstone = excluded.tombstone, search = excluded.search, sig = excluded.sig,
		     created = excluded.created, updated = now()
		  WHERE artifacts.owner_user = excluded.owner_user
		    AND coalesce(artifacts.tombstone, false) = false
		 RETURNING created, updated`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a), a.Sig,
		a.Created).
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
func (d *DB) WriteMemory(ctx context.Context, a *Artifact, e *Event) error {
	ctx, span := otel.Start(ctx, otel.KindIngest, "memory.write")
	defer span.End()
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: write memory: %w", err)
	}
	d.fillAt(a, at)
	e.SeqHLC = at
	e.Artifact, e.Project = a.ID, a.Project

	return d.inTx(ctx, "write memory "+a.ID, func(tx *sql.Tx) error {
		if err := d.upsertArtifact(ctx, tx, a); err != nil {
			return err
		}
		return d.appendEvent(ctx, tx, e)
	})
}

// SetArtifactFields replaces one artifact's fields column and writes the event
// that records the change, in one transaction and under one clock reading.
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
func (d *DB) SetArtifactFields(ctx context.Context, a *Artifact, fields json.RawMessage, e *Event) error {
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
	if err := d.signArtifact(ctx, a); err != nil {
		return err
	}
	// The event's artifact and project are taken from the item rather than from
	// the caller, as WriteMemory takes them: an entry naming anything else
	// would not be a record of this write, and a projectless event is readable
	// by its own actor and nobody else (see EventFilterSQL) - which for a
	// message announcing a change to a room's plan is the room never hearing.
	e.SeqHLC = at
	e.Artifact, e.Project = a.ID, a.Project

	return d.inTx(ctx, "set fields of "+a.ID, func(tx *sql.Tx) error {
		var column any
		if len(a.Fields) > 0 {
			column = []byte(a.Fields)
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE artifacts SET fields = $2, hlc = $3, node = $4, sig = $5, updated = now()
			  WHERE id = $1 AND coalesce(tombstone, false) = false`,
			a.ID, column, a.HLC, a.Node, a.Sig)
		if err != nil {
			return fmt.Errorf("store: set fields of %s: %w", a.ID, err)
		}
		n, err := affectedRows(res)
		if err != nil {
			return fmt.Errorf("store: set fields of %s: %w", a.ID, err)
		}
		if n == 0 {
			return fmt.Errorf("store: set fields: %w: artifact %s", ErrNotFound, a.ID)
		}
		return d.appendEvent(ctx, tx, e)
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
	if err := d.signArtifact(ctx, a); err != nil {
		return err
	}

	var fields any
	if len(a.Fields) > 0 {
		fields = []byte(a.Fields)
	}
	err := q.QueryRowContext(ctx,
		`INSERT INTO artifacts (id, type, kind, project, owner_user, title, body, discovery,
		                        status, severity, tags, user_tags, related, visibility,
		                        file_path, fields, hlc, node, tombstone, search, sig, created)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, $21, $22)
		 ON CONFLICT (id) DO NOTHING
		 RETURNING created, updated`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a), a.Sig,
		a.Created).
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

// ArtifactQuery narrows a list or a search. Every field is optional; the
// permission filter is not, and is added by the methods below.
type ArtifactQuery struct {
	Type       string
	Kind       string   // one kind
	Kinds      []string // any of these kinds; ORed with nothing, ANDed with the rest
	Project    string
	Status     string
	NotStatus  string // exclude one status - what "still open" means for a todo
	Room       string // the chat room the artifact was raised in - fields->>'room'
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
	return out, nil
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
	// The delete is a write like any other and travels as a row, so it is signed
	// as one: a tombstone nobody signed is a delete a peer could have invented.
	if err := d.signArtifact(ctx, art); err != nil {
		return nil, err
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE artifacts SET tombstone = true, hlc = $2, node = $3, sig = $5, updated = now()
		  WHERE id = $1 AND coalesce(owner_user, '') = $4`,
		art.ID, art.HLC, art.Node, p.UserID, art.Sig)
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
