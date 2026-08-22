package store

// The store half of the Phase 7 FUSE mount: the directories the mount is made
// of, and the write-behind queue behind it.
//
// The mount hosts memory where an agent already writes files, so every read
// here is a read the permission filter has already decided - ArtifactFilterSQL,
// the same fragment /api/artifacts and mem_search are narrowed by. There is no
// second reach rule for files. A directory in the mount is a scope, and a scope
// that cannot be read has no directory.
//
// The writes are the other half. They do not happen in the callback - see
// fs_intents in schema.sql - so what is here is an enqueue that commits before
// close(2) returns and an apply that runs afterwards, in one transaction, and
// can be run again after a crash without writing twice.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// FSTypes are the artifact types the mount hosts. Memory is the point of it;
// note is the type the API writes a plain note as, and a note is a file if
// anything is.
//
// It is a closed list because a directory in the mount is a type: hosting
// everything would put transcripts, chat and filed bugs behind a filename and
// an agent's editor, and an editor's temp file is not how a bug should change
// its lifecycle.
var FSTypes = []string{"memory", "note"}

// FSTypeOK reports whether the mount hosts this artifact type.
func FSTypeOK(t string) bool {
	for _, known := range FSTypes {
		if t == known {
			return true
		}
	}
	return false
}

// FSScope is one leaf directory of the mount: /<project>/<user>/<type>.
// A nil Project is the personal floor - project IS NULL - and not a project
// that happens to be unnamed.
type FSScope struct {
	Project *string
	Owner   string
	Type    string
}

// fsProjectSQL is the project half of a scope as a WHERE fragment. The
// personal floor is `project IS NULL`, which is not the same query as an
// equality test against anything, and writing it as one is how a personal row
// stops being personal.
func (s FSScope) projectSQL(a *args, alias string) string {
	if s.Project == nil {
		return " AND " + alias + ".project IS NULL"
	}
	return " AND " + alias + ".project = " + a.next(*s.Project)
}

// fsHostedTypes is the type list as a SQL fragment, so a read of the mount
// cannot see a row of a type the mount does not host.
func fsHostedTypes(a *args, alias string) string {
	holders := make([]string, 0, len(FSTypes))
	for _, t := range FSTypes {
		holders = append(holders, a.next(t))
	}
	return " AND " + alias + ".type IN (" + strings.Join(holders, ", ") + ")"
}

// FSProjects lists the projects that have something in them p may read, newest
// row first is not interesting here so they come back sorted by name. The
// personal floor is not in the list: it is not a project, it has no name in the
// projects column, and the mount gives it a directory of its own.
func (d *DB) FSProjects(ctx context.Context, p *Principal) ([]string, error) {
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT DISTINCT ar.project FROM artifacts ar
		  WHERE coalesce(ar.tombstone, false) = false
		    AND ar.project IS NOT NULL`+fsHostedTypes(a, "ar")+`
		    AND `+filter+`
		  ORDER BY ar.project`, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: fs projects: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return nil, fmt.Errorf("store: fs projects: %w", err)
		}
		out = append(out, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: fs projects: %w", err)
	}
	return out, nil
}

// FSOwners lists the people who own something p may read in one project - the
// second level of the mount. The caller adds itself: a principal has a
// directory to write in whether or not it has written anything yet.
func (d *DB) FSOwners(ctx context.Context, p *Principal, project *string) ([]string, error) {
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	scope := FSScope{Project: project}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT DISTINCT ar.owner_user FROM artifacts ar
		  WHERE coalesce(ar.tombstone, false) = false
		    AND coalesce(ar.owner_user, '') <> ''`+
			fsHostedTypes(a, "ar")+scope.projectSQL(a, "ar")+`
		    AND `+filter+`
		  ORDER BY ar.owner_user`, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: fs owners: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, fmt.Errorf("store: fs owners: %w", err)
		}
		out = append(out, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: fs owners: %w", err)
	}
	return out, nil
}

// FSList returns the artifacts in one leaf directory that p may read, oldest
// first: a directory listing is stable if it is ordered by something that does
// not move, and id is a ULID, so this is creation order.
func (d *DB) FSList(ctx context.Context, p *Principal, s FSScope) ([]*Artifact, error) {
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	query := `SELECT ` + artifactColumns + ` FROM artifacts ar
	           WHERE coalesce(ar.tombstone, false) = false
	             AND ar.type = ` + a.next(s.Type) + `
	             AND coalesce(ar.owner_user, '') = ` + a.next(s.Owner) +
		s.projectSQL(a, "ar") + `
	             AND ` + filter + `
	           ORDER BY ar.id
	           LIMIT ` + a.next(maxLimit)

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: fs list: %w", err)
	}
	defer rows.Close()

	out := []*Artifact{}
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, fmt.Errorf("store: fs list: %w", err)
		}
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: fs list: %w", err)
	}
	return out, nil
}

// FSFind returns the artifact in scope s whose id is id, or ErrNotFound. The
// scope is part of the query rather than checked afterwards: a lookup in one
// directory must not answer with a row that lives in another, or the mount
// would say a personal item is in a project by being asked for it there.
func (d *DB) FSFind(ctx context.Context, p *Principal, s FSScope, id string) (*Artifact, error) {
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	row := d.sql.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts ar
		  WHERE ar.id = `+a.next(id)+`
		    AND coalesce(ar.tombstone, false) = false
		    AND ar.type = `+a.next(s.Type)+`
		    AND coalesce(ar.owner_user, '') = `+a.next(s.Owner)+
			s.projectSQL(a, "ar")+`
		    AND `+filter, a.vals...)

	art, err := scanArtifact(row, nil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: fs find %s: %w", id, err)
	}
	return art, nil
}

// ------------------------------------------------------- the write-behind queue

// FSIntent is one file write, written down before it is applied. See the
// fs_intents comment in schema.sql for why it exists at all.
type FSIntent struct {
	ID        string
	Node      string
	Path      string
	Artifact  string
	OwnerUser string
	// Actor is who the event will name - the agent when the mount holds an
	// agent's token, the user otherwise. It rides on the intent so a reconcile
	// after a restart attributes the write the way the close would have.
	Actor   string
	Project *string
	Type    string
	// Visibility is the scope the path (and, inside a project, the file's own
	// front matter) asked for. Empty means "whatever the row already has",
	// which is what an edit that says nothing about scope means - the same rule
	// mem_write keeps.
	Visibility string
	Name       string
	// FileKey is the files-map key this write targets when the file is a
	// view over one key of an openspec change's row. Empty for an ordinary
	// file, where the whole row is the file's content.
	FileKey string
	Hash    string
	Content string
	Applied *time.Time
	Created time.Time
}

// FSFields is an intent's content once the caller has parsed it. The store does
// not know the file format - front matter is the mount's business - so the
// drainer hands the parse in rather than the queue storing it twice.
type FSFields struct {
	Title string
	Body  string
	Kind  string
	Tags  []string
}

// FSApplyResult says what the drainer did with an intent.
type FSApplyResult string

const (
	// FSApplied - the artifact and its event were written.
	FSApplied FSApplyResult = "applied"
	// FSDuplicate - the store already holds exactly these bytes for this
	// artifact, because this intent, or one with the same hash, has been
	// applied before. This is what makes a replay after a crash idempotent.
	FSDuplicate FSApplyResult = "duplicate"
	// FSSuperseded - the artifact has been deleted since the file was written.
	// A queued write is not a resurrection: the intent is dropped.
	FSSuperseded FSApplyResult = "superseded"
	// FSRefused - the write would have moved somebody else's row, or moved a
	// row between homes. Nothing was written.
	FSRefused FSApplyResult = "refused"
)

// EnqueueFSIntent writes the intent down. It is the durability point of a
// close(2) on the mount: when this returns the write will happen, this time or
// after the next restart, and until it has happened the row says so.
func (d *DB) EnqueueFSIntent(ctx context.Context, in *FSIntent) error {
	if in.ID == "" {
		in.ID = ulid.NewString()
	}
	if in.Node == "" {
		in.Node = d.node
	}
	if in.Artifact == "" {
		return errors.New("store: fs intent with no artifact id")
	}
	// The date is the queue's own bookkeeping, not a fabric row's: nothing
	// signs it and nothing replicates it, so the column's default is fine.
	err := d.sql.QueryRowContext(ctx,
		`INSERT INTO fs_intents (id, node, path, artifact, owner_user, actor, project,
		                         type, visibility, name, file_key, hash, content)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		 RETURNING created`,
		in.ID, in.Node, in.Path, in.Artifact, in.OwnerUser, in.Actor, in.Project,
		in.Type, in.Visibility, in.Name, in.FileKey, in.Hash, in.Content).Scan(&in.Created)
	if err != nil {
		return fmt.Errorf("store: enqueue fs intent: %w", err)
	}
	return nil
}

// PendingFSIntents reads the intents that have not been applied, oldest first.
// Order matters: two writes of one file are two intents, and applying the older
// one last would put the older bytes in the store.
func (d *DB) PendingFSIntents(ctx context.Context, limit int) ([]*FSIntent, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, coalesce(node, ''), coalesce(path, ''), coalesce(artifact, ''),
		        coalesce(owner_user, ''), coalesce(actor, ''), project, coalesce(type, ''),
		        coalesce(visibility, ''), coalesce(name, ''), coalesce(file_key, ''),
		        coalesce(hash, ''), coalesce(content, ''), created
		   FROM fs_intents
		  WHERE applied IS NULL
		  ORDER BY created, id
		  LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("store: pending fs intents: %w", err)
	}
	defer rows.Close()

	out := []*FSIntent{}
	for rows.Next() {
		var (
			in      FSIntent
			project sql.NullString
		)
		if err := rows.Scan(&in.ID, &in.Node, &in.Path, &in.Artifact, &in.OwnerUser,
			&in.Actor, &project, &in.Type, &in.Visibility, &in.Name, &in.FileKey,
			&in.Hash, &in.Content, &in.Created); err != nil {
			return nil, fmt.Errorf("store: pending fs intents: %w", err)
		}
		if project.Valid {
			p := project.String
			in.Project = &p
		}
		out = append(out, &in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: pending fs intents: %w", err)
	}
	return out, nil
}

// FSIntentApplied reports whether one intent has been applied yet, which is
// what a reader of the mount asks to know whether the bytes it is holding are
// still only in the queue.
func (d *DB) FSIntentApplied(ctx context.Context, id string) (bool, error) {
	var applied sql.NullTime
	err := d.sql.QueryRowContext(ctx, `SELECT applied FROM fs_intents WHERE id = $1`, id).
		Scan(&applied)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("store: fs intent %s: %w", id, err)
	}
	return applied.Valid, nil
}

// ApplyFSIntent applies one intent: the artifact, the event that records the
// write, and the intent's own applied stamp, in one transaction.
//
// One transaction, because the three are one fact. A node that stopped between
// the artifact and the event left a memory nothing in the log accounts for -
// and both rows replicate on their own, so a peer would page the log and see a
// write that never happened. Marking the intent applied is in the same
// transaction for the other direction: a stamp that committed before the write
// is a write that is never replayed, and one that committed after it is a write
// that is replayed forever.
//
// The result says which of the four things happened, and only FSApplied wrote
// anything:
//
//   - duplicate: the last intent applied for this artifact carries the same
//     hash and the row is still there. This is the replay after a crash, and
//     it is what makes at-least-once delivery exactly one write.
//   - superseded: the artifact has been deleted. A queued write does not
//     resurrect it - coming back is something to do on purpose.
//   - refused: the row belongs to somebody else, or the intent would move it
//     between a project and the personal floor. The mount checks both before it
//     enqueues; this is the check that runs against the row as it is now,
//     inside the transaction that would do the write.
func (d *DB) ApplyFSIntent(ctx context.Context, in *FSIntent, f FSFields) (FSApplyResult, error) {
	at, err := d.clock.Pack()
	if err != nil {
		return FSRefused, fmt.Errorf("store: apply fs intent: %w", err)
	}

	outcome := FSApplied
	refusal := ""
	err = d.inTx(ctx, "apply fs intent "+in.ID, func(tx *sql.Tx) error {
		decided, held, err := d.fsIntentDecision(ctx, tx, in)
		if err != nil {
			return err
		}
		outcome = decided
		if decided == FSApplied {
			if err := d.fsIntentWrite(ctx, tx, in, f, held, at); err != nil {
				// A refusal is the caller's mistake, and it will be the
				// caller's mistake on every retry - the row is what it is.
				// Retrying forever would wedge the drainer with every intent
				// behind it never draining (the wedge the mount's read-only
				// refusals stood in for until this arm). So the intent is
				// dropped once, the refusal's own sentence recorded on the
				// queue row, and the queue keeps draining. Everything else
				// is a broken node or a broken database: the transaction
				// rolls back and the intent stays pending for the next pass.
				var ref DepRefusal
				if errors.As(err, &ref) {
					outcome = FSRefused
					refusal = err.Error()
				} else {
					return err
				}
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE fs_intents SET applied = now(), refusal = $2
			  WHERE id = $1 AND applied IS NULL`,
			in.ID, sql.NullString{String: refusal, Valid: refusal != ""})
		if err != nil {
			return fmt.Errorf("store: apply fs intent %s: %w", in.ID, err)
		}
		n, err := affectedRows(res)
		if err != nil {
			return fmt.Errorf("store: apply fs intent %s: %w", in.ID, err)
		}
		if n == 0 {
			// Somebody else drained it while this transaction was open. Nothing
			// of ours goes in: the row it would have written is the row they
			// wrote, and two drainers are not two writes.
			return errFSDrained
		}
		return nil
	})
	if errors.Is(err, errFSDrained) {
		return FSDuplicate, nil
	}
	if err != nil {
		return FSRefused, err
	}
	return outcome, nil
}

// errFSDrained rolls back an apply that another drainer had already taken. It
// never leaves ApplyFSIntent.
var errFSDrained = errors.New("store: fs intent was drained by somebody else")

// fsIntentDecision is the half of an apply that decides, inside the
// transaction: what the store holds for this artifact right now, and whether
// this intent may write over it. It returns the row it found, or nil when
// there is none.
func (d *DB) fsIntentDecision(ctx context.Context, tx *sql.Tx, in *FSIntent) (FSApplyResult, *Artifact, error) {
	var (
		owner, project, visibility, typ, kind sql.NullString
		title, body, filePath, fields         sql.NullString
		tombstone                             sql.NullBool
	)
	err := tx.QueryRowContext(ctx,
		`SELECT owner_user, project, visibility, type, kind, title, body,
		        file_path, fields, tombstone FROM artifacts WHERE id = $1`,
		in.Artifact).Scan(&owner, &project, &visibility, &typ, &kind,
		&title, &body, &filePath, &fields, &tombstone)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A new row. Nothing to be duplicate of and nothing to move.
		return FSApplied, nil, nil
	case err != nil:
		return FSRefused, nil, fmt.Errorf("store: apply fs intent %s: %w", in.ID, err)
	}

	held := &Artifact{
		ID:         in.Artifact,
		OwnerUser:  owner.String,
		Visibility: visibility.String,
		Title:      title.String,
		Body:       body.String,
		FilePath:   filePath.String,
	}
	if project.Valid {
		p := project.String
		held.Project = &p
	}
	// Type and kind ride along because the write consults what the row IS
	// (fsIntentWrite, below): the row says what it is, and the header of the
	// file being saved cannot argue. Fields too - a write of a view inside
	// an openspec change edits the row's files map, and that map is the
	// whole of what it writes.
	held.Type = typ.String
	held.Kind = kind.String
	if fields.Valid {
		held.Fields = []byte(fields.String)
	}
	held.Tombstone = tombstone.Bool

	if held.Tombstone {
		return FSSuperseded, held, nil
	}
	if held.OwnerUser != in.OwnerUser {
		return FSRefused, held, nil
	}
	// The floor, in the drainer as well as at the door. A row with no project
	// is its owner's and nobody else's, and an intent cannot give it one:
	// promotion is something to do on purpose, through a tool that says so, not
	// a side effect of saving a file in a different directory. The other
	// direction is refused for the same reason with the sign flipped - taking a
	// project's row away from the project is not a write either.
	if !sameHome(held.Project, in.Project) {
		return FSRefused, held, nil
	}

	// Deduped by hash, against the last thing this queue applied for this row.
	// Not against the file's current bytes: two intents with the same hash are
	// the same write, and a write that reverses one and then repeats it is
	// three different writes even though the first and third look alike.
	var last sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT hash FROM fs_intents
		  WHERE artifact = $1 AND applied IS NOT NULL AND id <> $2
		  ORDER BY applied DESC, created DESC, id DESC
		  LIMIT 1`, in.Artifact, in.ID).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FSRefused, held, fmt.Errorf("store: apply fs intent %s: %w", in.ID, err)
	}
	if last.Valid && last.String == in.Hash && in.Hash != "" {
		return FSDuplicate, held, nil
	}
	return FSApplied, held, nil
}

// fsIntentWrite is the other half: the artifact and the event, stamped with one
// reading, the way WriteMemory writes the same two rows for mem_write.
func (d *DB) fsIntentWrite(ctx context.Context, tx *sql.Tx, in *FSIntent, f FSFields,
	held *Artifact, at int64,
) error {
	visibility := in.Visibility
	if visibility == "" && held != nil {
		// An edit that says nothing about scope keeps the scope the row has,
		// verbatim - including a value written before the scopes were told
		// apart, which is not something to migrate behind somebody's back.
		visibility = held.Visibility
	}
	art := &Artifact{
		ID:         in.Artifact,
		Type:       in.Type,
		Kind:       f.Kind,
		Project:    in.Project,
		OwnerUser:  in.OwnerUser,
		Title:      f.Title,
		Body:       f.Body,
		Tags:       f.Tags,
		Visibility: visibility,
		// The name the file has in the mount. It is what makes the mount able
		// to show the file back under the name it was written as rather than
		// under a ULID nobody typed.
		FilePath: in.Name,
	}
	if held != nil && IsOpenspec(held) {
		// The row says what it is; the header cannot argue. A spec saved
		// without its front matter parses as a note and, without this, the
		// rewrite would husk the row into one. What the file is a view of is
		// decided by the row - the doors and the lifecycle move rows between
		// kinds, a file save does not.
		art.Kind = held.Kind
	}

	// A view write: the file is one key of an openspec change's files map,
	// and the write is of that key, not of the row's own words. The change's
	// title, kind and every other column stay what the row says - the files
	// map is the whole of a change's content (openspec.go), so this is the
	// write that edits it. What the content parses as is the mount's
	// business and deliberately not consulted here: proposal.md is plain
	// prose, and a header inside it is prose too.
	if in.FileKey != "" {
		if held == nil {
			return refuseDep("a file inside an openspec change writes %s, which names no row - "+
				"a view has to view something", in.FileKey)
		}
		if !IsEntityType(held, ChangeKind) {
			return refuseDep("a file inside an openspec change writes %s, which is a %s - "+
				"only a change has files to be views of", in.FileKey, held.Kind)
		}
		files, err := OpenspecFilesOf(held)
		if err != nil {
			return err
		}
		if files == nil {
			files = map[string]string{}
		}
		files[in.FileKey] = in.Content
		if err := setOpenspecFiles(art, files); err != nil {
			return err
		}
		// The row's own words are not what this file views, so they are not
		// what the write touches: everything but the files map comes off the
		// held row, verbatim.
		art.Kind = held.Kind
		art.Title = held.Title
		art.Body = held.Body
		art.FilePath = held.FilePath
	}
	d.fillAt(art, at)
	if err := d.upsertArtifact(ctx, tx, art); err != nil {
		// The row moved under the decision above - it was taken over or deleted
		// between the two statements. Not this node's write to make.
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("store: apply fs intent %s: %w", in.ID, ErrNotFound)
		}
		return err
	}

	e := &Event{
		Type:     "memory.write",
		Room:     "memory",
		Actor:    in.Actor,
		Artifact: art.ID,
		Project:  art.Project,
		SeqHLC:   at,
		Body:     art.Title,
	}
	return d.appendEvent(ctx, tx, e)
}

// sameHome reports whether two projects are the same home, counting NULL - the
// personal floor - as a home of its own rather than as a missing value.
func sameHome(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// DropFSIntent marks an intent applied without applying it. It is for the one
// case that cannot be retried into working: a file whose content the mount
// cannot turn into an item at all, which would otherwise sit at the head of the
// queue refusing forever and holding up every write behind it.
func (d *DB) DropFSIntent(ctx context.Context, id string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE fs_intents SET applied = now() WHERE id = $1 AND applied IS NULL`, id)
	if err != nil {
		return fmt.Errorf("store: drop fs intent %s: %w", id, err)
	}
	return nil
}

// CancelFSIntents takes an owner's queued writes for one artifact off the queue
// without applying them, and says how many it took.
//
// It is what an unlink does with a write that has not been drained yet. The
// alternative is letting the apply refuse it later - which it would, as
// superseded, once the row it names is a tombstone - but that only works for a
// file the store already had. A file that was created and deleted before the
// drainer ever ran names no row at all, so there is nothing for the apply to
// find, and the write would land as a new artifact a second after the caller
// watched it disappear.
//
// The owner is in the statement rather than checked before it: the queue is
// local, but the thing being cancelled is a write that was authorised when it
// was queued, and one principal does not drop another's.
func (d *DB) CancelFSIntents(ctx context.Context, artifact, owner string) (int64, error) {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE fs_intents SET applied = now()
		  WHERE artifact = $1 AND coalesce(owner_user, '') = $2 AND applied IS NULL`,
		artifact, owner)
	if err != nil {
		return 0, fmt.Errorf("store: cancel fs intents for %s: %w", artifact, err)
	}
	n, err := affectedRows(res)
	if err != nil {
		return 0, fmt.Errorf("store: cancel fs intents for %s: %w", artifact, err)
	}
	return n, nil
}
