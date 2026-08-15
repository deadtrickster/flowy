package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"

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
	tombstone, created, updated, reported, external`

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
		&fields, &clockVal, &nodeCol, &tomb, &a.Created, &a.Updated, &reported, &external}
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
	d.fill(a)

	var fields any
	if len(a.Fields) > 0 {
		fields = []byte(a.Fields)
	}
	err := d.sql.QueryRowContext(ctx,
		`INSERT INTO artifacts (id, type, kind, project, owner_user, title, body, discovery,
		                        status, severity, tags, user_tags, related, visibility,
		                        file_path, fields, hlc, node, tombstone, search)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`)
		 ON CONFLICT (id) DO UPDATE SET
		     type = excluded.type, kind = excluded.kind, project = excluded.project,
		     owner_user = excluded.owner_user,
		     title = excluded.title, body = excluded.body, discovery = excluded.discovery,
		     status = excluded.status, severity = excluded.severity, tags = excluded.tags,
		     user_tags = excluded.user_tags, related = excluded.related,
		     visibility = excluded.visibility, file_path = excluded.file_path,
		     fields = excluded.fields, hlc = excluded.hlc, node = excluded.node,
		     tombstone = excluded.tombstone, search = excluded.search, updated = now()
		  WHERE artifacts.owner_user = excluded.owner_user
		    AND coalesce(artifacts.tombstone, false) = false
		 RETURNING created, updated`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a)).
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
	d.fill(a)

	var fields any
	if len(a.Fields) > 0 {
		fields = []byte(a.Fields)
	}
	err := d.sql.QueryRowContext(ctx,
		`INSERT INTO artifacts (id, type, kind, project, owner_user, title, body, discovery,
		                        status, severity, tags, user_tags, related, visibility,
		                        file_path, fields, hlc, node, tombstone, search)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`)
		 ON CONFLICT (id) DO NOTHING
		 RETURNING created, updated`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a)).
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
func (d *DB) fill(a *Artifact) {
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
	// A fresh reading on every write, including an update - the previous
	// value is what a peer already saw.
	a.HLC = d.clock.Pack()
	a.Node = d.node
	if a.ID == "" {
		a.ID = ulid.NewString()
	}
}

// ArtifactQuery narrows a list or a search. Every field is optional; the
// permission filter is not, and is added by the methods below.
type ArtifactQuery struct {
	Type       string
	Kind       string   // one kind
	Kinds      []string // any of these kinds; ORed with nothing, ANDed with the rest
	Project    string
	Status     string
	NotStatus  string // exclude one status - what "still open" means for a todo
	Visibility string // personal|project|shared - the memory scopes
	Query      string // free text; SearchArtifacts only
	ScopeAll   bool   // ?scope=all - honoured only for the operator principal
	Limit      int
}

const defaultLimit = 200

func (q ArtifactQuery) limit() int {
	if q.Limit > 0 && q.Limit <= 1000 {
		return q.Limit
	}
	return defaultLimit
}

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
	return where
}

// ListArtifacts returns the artifacts p may read, newest first. Tombstoned rows
// are gone from the list; they stay in the table so the delete can replicate.
func (d *DB) ListArtifacts(ctx context.Context, p *Principal, q ArtifactQuery) ([]*Artifact, error) {
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
	art.HLC = d.clock.Pack()
	art.Node = d.node
	res, err := d.sql.ExecContext(ctx,
		`UPDATE artifacts SET tombstone = true, hlc = $2, node = $3, updated = now()
		  WHERE id = $1 AND coalesce(owner_user, '') = $4`, art.ID, art.HLC, art.Node, p.UserID)
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
