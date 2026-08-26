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
	tombstone, created, updated, reported, external, sig, author_sig, authorship,
	started, last_worked, sig_form`

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
		started, lastWorked                        sql.NullTime
		sigForm                                    sql.NullString
	)
	dest := []any{&a.ID, &typeCol, &kind, &project, &owner, &title, &body, &disc, &status, &severity,
		pq.Array(&a.Tags), pq.Array(&a.UserTags), pq.Array(&a.Related), &vis, &filePath,
		&fields, &clockVal, &nodeCol, &tomb, &a.Created, &a.Updated, &reported, &external, &a.Sig,
		&a.AuthorSig, &authorship, &started, &lastWorked, &sigForm}
	if rank != nil {
		dest = append(dest, rank)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	// One of the two things this node can say, whatever the column holds - see
	// authorshipOr and scanEvent, which does the same.
	a.Authorship = authorshipOr(authorship.String)
	// Empty stays empty and means v1 - see Artifact.SigForm. It is not
	// normalised to the constant here, because "the row said nothing" and "the
	// row said v1" are the same answer to a verifier and a different answer to
	// anybody asking when this row was last signed.
	a.SigForm = sigForm.String
	if project.Valid {
		p := project.String
		a.Project = &p
	}
	a.Type, a.Kind = typeCol.String, kind.String
	a.OwnerUser, a.Title, a.Body = owner.String, title.String, body.String
	a.Discovery, a.Status, a.Severity = disc.String, status.String, severity.String
	a.Visibility, a.FilePath, a.Node = vis.String, filePath.String, nodeCol.String
	a.HLC, a.Tombstone, a.Reported = clockVal.Int64, tomb.Bool, reported.Bool
	// NULL stays absent rather than becoming the zero time: never-started and
	// started-at-the-epoch are different answers, and only one of them is true
	// of a row nobody has picked up.
	if started.Valid {
		at := started.Time.UTC()
		a.Started = &at
	}
	if lastWorked.Valid {
		at := lastWorked.Time.UTC()
		a.LastWorked = &at
	}
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

	// A ROW WITH NO TAGS IS AN EMPTY LIST, NOT null.
	//
	// pq.Array leaves the destination nil for a NULL column and for an empty
	// one, so every artifact that nobody has tagged, cross-referenced or
	// related came back with three nil slices - and Go marshals a nil slice as
	// null. Measured 2026-08-20 on the live node: 200 rows, all three fields
	// null on every one of them.
	//
	// A FRESH ROW IS AN EMPTY STATE and it exists on a node full of data, which
	// is what makes this the half a fresh-node walk cannot see: the walk asks
	// GETs on a node with nothing in it, and a POST answer describing a row
	// created a moment ago carries the same nil with none of the emptiness.
	// @flowy-claude found it on the attachment door answering a create.
	//
	// Defaulted HERE because scanArtifact is the one place every read path goes
	// through - the alternative is each caller remembering, which is the same
	// convention that produced the nil in the first place.
	if a.Tags == nil {
		a.Tags = []string{}
	}
	if a.UserTags == nil {
		a.UserTags = []string{}
	}
	if a.Related == nil {
		a.Related = []string{}
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
	// And a queue row lands holding a pair that can be true. Asked here for the
	// reason the project is asked here: every local write of a row goes through
	// one of four statements, and a rule kept per surface is a rule the next
	// surface forgets - this one was forgotten by every door that could set a
	// status without being able to set an assignee. See checkQueueRow.
	if err := checkQueueRow(a); err != nil {
		return err
	}
	// And a merge request says which branch it would land, asked here for that
	// same reason - the rule used to live at the memory door alone, and the
	// HTTP door wrote rows the queue could never drain. See checkMergeRow.
	if err := checkMergeRow(a); err != nil {
		return err
	}
	// And an openspec row holds its own shape - asked here for the same reason:
	// every surface writes through one of the same statements, and a rule kept
	// per surface is a rule the next surface forgets. See checkOpenspecRow.
	// Its tasks.md is annotated with line ids first, so the shape check and
	// the signature both cover the file as it will be stored. See
	// prepareChangeWrite.
	if err := d.prepareChangeWrite(ctx, q, a); err != nil {
		return err
	}
	if err := checkOpenspecRow(a); err != nil {
		return err
	}
	// And a dashboard declares tiles the renderer can draw, and a metric is a
	// reading with a name - asked here for the same reason: every surface
	// writes through one of the same statements. See dashboards.go.
	if err := checkDashboardRow(a); err != nil {
		return err
	}
	if err := checkMetricRow(a); err != nil {
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
		                        author_sig, authorship, started)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, $21, $22, $23, $24,
		         CASE WHEN $9 = '`+ActiveStatus+`' THEN now() END)
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
		     created = excluded.created, updated = now(),
		     -- A ROW CAN BE BORN ACTIVE, so this door owes the stamp too.
		     -- POST /api/artifacts takes a status, so a client can create a row
		     -- straight into active, or update one into it, without passing the
		     -- transition verb - and started would then be null on exactly the
		     -- rows a board shows as running.
		     --
		     -- Once-only, on the same condition setArtifactStatus uses:
		     -- rewriting a row that is already active is not a restart.
		     --
		     -- last_worked is deliberately NOT moved here. An upsert is ANY
		     -- write, a rename included, and counting it would rebuild the
		     -- problem updated already has, which is why these columns exist.
		     started = CASE
		         WHEN excluded.status = '`+ActiveStatus+`' AND artifacts.started IS NULL
		         THEN now() ELSE artifacts.started END
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
	// The row is stored; its derived todos now owe it a re-sync. On the
	// caller's execer, so a write that is half of an operation derives as
	// part of it.
	if err := d.syncOpenspec(ctx, q, a); err != nil {
		return err
	}
	return d.pruneAfterMetric(ctx, q, a)
}

// pruneAfterMetric is the retention hook, asked at the three statements that
// write a row - the same three checkMetricRow guards. It no-ops for everything
// that is not a reading.
//
// ON THE CALLER'S EXECER, so a prune that is half of an operation is part of it,
// the same rule syncOpenspec keeps two lines above every call to this.
//
// A PRUNE FAILURE FAILS THE WRITE, and that is the uncomfortable choice made
// deliberately. The alternative - log it and carry on - means a store that
// silently grows without bound while every push reports success, and the first
// symptom is a full disk rather than a refused write. A refusal names itself.
func (d *DB) pruneAfterMetric(ctx context.Context, q execer, a *Artifact) error {
	if !IsMetric(a) {
		return nil
	}
	name := MetricNameOf(a)
	if name == "" {
		return nil
	}
	return d.pruneSeries(ctx, q, name, RetentionOf(a))
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
	if err := stampEvents(a, at, events); err != nil {
		return err
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
	return d.writeArtifactFields(ctx, a, fields, "", "", events...)
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
	return d.writeArtifactFields(ctx, a, fields, "", guard, events...)
}

// SetArtifactFieldsAndStatusIf writes the fields column AND the status column as
// one statement, under one guard and one clock reading.
//
// It exists because the queue has one fact that lives in both: `active` means
// somebody is on it, so a write that says nobody is on it any more has to move
// where the work is in the same breath - see queuecoherence.go. Two writes could
// not do it. The gap between them is a row that is active and unowned, which is
// the state this whole change exists to make unreachable, and a node that
// stopped in the gap would leave one there permanently.
//
// status empty leaves the column alone, which is what every other caller of the
// fields writers wants: a handover changes who is carrying the work and says
// nothing about where the work is.
//
// IT MAY NOT MOVE A ROW TO `active`, and that refusal is the whole of the
// single-writer rule made enforceable.
//
// A transition is setArtifactStatus's: it stamps `started` the first time a row
// goes active, moves `last_worked`, and asks checkQueueRow whether the move is
// coherent. None of that happens here, so a row activated through this door
// would be active with no start - which is exactly the state the board showed
// this morning and could not explain.
//
// No caller passes it today: putDownStatus returns `todo` or nothing (it fires
// only when a claim is a RELEASE), and join passes `done`. So this starts green
// and stays green - it is a rule stated where the next caller will meet it,
// rather than a property that happens to hold and would break silently. The
// alternative is a second stamping site, which is how one fact ends up with two
// homes that disagree.
func (d *DB) SetArtifactFieldsAndStatusIf(
	ctx context.Context, a *Artifact, fields json.RawMessage, status, guard string,
	events ...*Event,
) error {
	if status == ActiveStatus {
		return fmt.Errorf("store: %s cannot be moved to %s through the fields writer - "+
			"a transition belongs to MoveArtifactStatus, which stamps started and "+
			"checks the row is coherent", a.ID, ActiveStatus)
	}
	return d.writeArtifactFields(ctx, a, fields, status, guard, events...)
}

// stampEvents puts the row's identity on every event of a write, and refuses a
// nil one instead of dereferencing it.
//
// THE FIRST EVENT SHARES THE ROW'S READING, because it is the record OF this
// write and the two are one point in the order. Any event after it is left
// unstamped and takes the next reading in appendEvent, deliberately: a log
// cursor is a seq_hlc and it is exclusive, so two events sharing one reading
// are two events a page boundary can fall between - and the second of them
// would then be skipped by every reader paging forwards, silently and
// permanently.
//
// Each event's artifact and project are taken from the item rather than from
// the caller, as WriteMemory takes them: an entry naming anything else would
// not be a record of this write, and a projectless event is readable by its own
// actor and nobody else (see EventFilterSQL) - which for a message announcing a
// change to a room's plan is the room never hearing.
//
// A NIL EVENT IS A CALLER'S MISTAKE AND IT NOW SAYS SO. Three writers had this
// loop copied out and all three dereferenced whatever they were handed, so one
// nil in the slice was a panic inside an http handler: the caller read an EOF
// with nothing to go on while the trace sat in a log on whichever box served
// the request. Measured on 2026-08-20 - ClaimTodo takes its extra event as a
// variadic, handleTodoAssign passed the nil that claimHeardIn answers for a row
// raised in NO ROOM, and a variadic cannot tell "I passed you nothing" from "I
// passed you one nothing". Every guarded claim on an off-board row crashed.
//
// One function rather than the fix written out three times: this loop is one
// rule about how a write records itself, and three copies of it are three
// places for the next rule to be added to only two.
func stampEvents(a *Artifact, at int64, events []*Event) error {
	for i, e := range events {
		if e == nil {
			return fmt.Errorf("store: write %s: event %d is nil - an absent "+
				"event is no element, not a nil one", a.ID, i)
		}
		if i == 0 {
			e.SeqHLC = at
		}
		e.Artifact, e.Project = a.ID, a.Project
	}
	return nil
}

// writeArtifactFields is the one path all three take: stamp the row, sign it,
// stamp the events, write.
//
// The span is named by whether there is a guard, because that is the difference
// a reader of a trace is telling apart - a fields write and a compare-and-set
// are two things that fail for two reasons.
func (d *DB) writeArtifactFields(
	ctx context.Context, a *Artifact, fields json.RawMessage, status, guard string,
	events ...*Event,
) error {
	name := "artifact.fields"
	if guard != "" {
		name = "artifact.fields.cas"
	}
	ctx, span := otel.Start(ctx, otel.KindIngest, name)
	defer func() {
		span.SetArtifact(a.ID)
		span.End()
	}()
	at, err := d.clock.Pack()
	if err != nil {
		return fmt.Errorf("store: set fields of %s: %w", a.ID, err)
	}
	a.Fields = fields
	if status != "" {
		a.Status = status
	}
	a.HLC = at
	a.Node = d.node
	// The pair the row will hold, asked before it is signed: the signature is
	// over the row, and a row that must not exist must not be signed into
	// existence. See checkQueueRow.
	if err := checkQueueRow(a); err != nil {
		return err
	}
	// And a merge request says which branch it would land, asked here for that
	// same reason - the rule used to live at the memory door alone, and the
	// HTTP door wrote rows the queue could never drain. See checkMergeRow.
	if err := checkMergeRow(a); err != nil {
		return err
	}
	// And an openspec row holds its own shape - asked here for the same reason:
	// every surface writes through one of the same statements, and a rule kept
	// per surface is a rule the next surface forgets. See checkOpenspecRow.
	// Its tasks.md is annotated with line ids first, so the shape check and
	// the signature both cover the file as it will be stored. See
	// prepareChangeWrite.
	if err := d.prepareChangeWrite(ctx, d.sql, a); err != nil {
		return err
	}
	if err := checkOpenspecRow(a); err != nil {
		return err
	}
	// And a dashboard declares tiles the renderer can draw, and a metric is a
	// reading with a name - asked here for the same reason: every surface
	// writes through one of the same statements. See dashboards.go.
	if err := checkDashboardRow(a); err != nil {
		return err
	}
	if err := checkMetricRow(a); err != nil {
		return err
	}
	if err := d.signArtifact(ctx, d.sql, a); err != nil {
		return err
	}
	if err := stampEvents(a, at, events); err != nil {
		return err
	}
	return d.setArtifactFields(ctx, a, status != "", guard, events...)
}

// setArtifactFields is the one write all three doors make. The row has already
// been stamped and signed by the caller.
//
// withStatus says whether the status column is part of this write. It is a flag
// rather than a value because the value is already on the row - the caller put
// it there before signing, and reading it from two places is two chances for the
// signature to be over a row the database does not have.
func (d *DB) setArtifactFields(
	ctx context.Context, a *Artifact, withStatus bool, guard string, events ...*Event,
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
		// sig_form travels with sig, always: the two are one fact, and a row
		// whose signature was remade while the form column kept its old value
		// is a row nothing downstream can verify. It is written even though
		// this build only ever signs v1, because the drift would be invisible
		// until the day a second form exists.
		set := `fields = $2, hlc = $3, node = $4, sig = $5, sig_form = $6, updated = now()`
		args := []any{a.ID, column, a.HLC, a.Node, a.Sig, a.SigForm}
		if withStatus {
			set = `fields = $2, hlc = $3, node = $4, sig = $5, sig_form = $6, status = $7,
			       updated = now()`
			args = append(args, a.Status)
		}
		res, err := tx.ExecContext(ctx, `UPDATE artifacts SET `+set+` WHERE `+where, args...)
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
		// The row moved; its derived todos now owe it a re-sync, in this
		// same transaction - a fields write that is also a tasks.md edit is
		// one operation, not two. See deriveChange.
		if err := d.syncOpenspec(ctx, tx, a); err != nil {
			return err
		}
		return d.pruneAfterMetric(ctx, tx, a)
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
	if err := stampEvents(a, at, events); err != nil {
		return err
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
			        sig_form = $10, updated = now()
			  WHERE `+where,
			a.ID, a.Title, a.Body, searchText(a), a.HLC, a.Node, a.Sig, a.AuthorSig,
			authorshipOr(a.Authorship), a.SigForm)
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
	// A queue row is raised holding a pair that can be true, exactly as one is
	// updated holding one. See checkQueueRow: a row created active with nobody
	// on it is the same lie whether it arrived that way or got there later.
	if err := checkQueueRow(a); err != nil {
		return err
	}
	// And a merge request says which branch it would land, asked here for that
	// same reason - the rule used to live at the memory door alone, and the
	// HTTP door wrote rows the queue could never drain. See checkMergeRow.
	if err := checkMergeRow(a); err != nil {
		return err
	}
	// And an openspec row holds its own shape - asked here for the same reason:
	// every surface writes through one of the same statements, and a rule kept
	// per surface is a rule the next surface forgets. See checkOpenspecRow.
	// Its tasks.md is annotated with line ids first, so the shape check and
	// the signature both cover the file as it will be stored. See
	// prepareChangeWrite.
	if err := d.prepareChangeWrite(ctx, q, a); err != nil {
		return err
	}
	if err := checkOpenspecRow(a); err != nil {
		return err
	}
	// And a dashboard declares tiles the renderer can draw, and a metric is a
	// reading with a name - asked here for the same reason: every surface
	// writes through one of the same statements. See dashboards.go.
	if err := checkDashboardRow(a); err != nil {
		return err
	}
	if err := checkMetricRow(a); err != nil {
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
		                        author_sig, authorship, started)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, $21, $22, $23, $24,
		         -- Born active, same rule as the upsert: a row created straight
		         -- into active has started now, or the board shows it running
		         -- with no answer to since-when.
		         CASE WHEN $9 = '`+ActiveStatus+`' THEN now() END)
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
	// The row is stored; its tasks.md now derives its todos. On the caller's
	// execer, so a create that is half of an operation derives as part of it.
	if err := d.syncOpenspec(ctx, q, a); err != nil {
		return err
	}
	return d.pruneAfterMetric(ctx, q, a)
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

// fieldEq is the clause that narrows on one key inside the fields JSON, in the
// shape the PARTIAL index over that key can actually be used for.
//
// MEASURED, and it is why this function exists rather than a bare `->>`
// comparison written at each site. Three keys carry an expression index -
// supersedes, category, and now room - and every one of them is partial:
//
//	CREATE INDEX ... ON artifacts ((fields ->> 'category')) WHERE fields ? 'category';
//
// A partial index is only available to a query the planner can prove satisfies
// its predicate, and the clause each site built was just
// `fields->>'category' = $1`. Nothing in that says the key is PRESENT, so
// Postgres refused all three indexes and scanned. On a schema-loaded database
// with enable_seqscan off - which forces the planner to use any index it
// legally can - the plan was still `Seq Scan ... cost=10000000000`. With the
// existence test in front of it the same index gives `Index Scan using
// artifacts_category_idx`, Index Cond exact.
//
// So they had been decoration since the day they landed, and the comments
// beside them said otherwise. Adding the room index is what surfaced it: the
// new one failed the same way the old ones had been failing silently.
//
// IT DOES NOT CHANGE WHAT THE FILTER MEANS. `fields ? 'key'` is NULL for a row
// whose fields column is NULL and false for a row without the key, and
// `fields->>'key' = $1` was already NULL or false for exactly those rows. The
// clause narrows to the same set; it is only shaped so the index is reachable.
func fieldEq(alias, key, arg string) string {
	return alias + ".fields ? '" + key + "' AND " + alias + ".fields->>'" + key + "' = " + arg
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

// SupersedesOf reads the row this one replaces, or "" when it replaces nothing.
//
// A reader rather than a second fields lookup at each call site, for the reason
// RoomOf and BranchOf are: a key spelled by hand in three places is a key
// misspelled in one of them eventually.
func SupersedesOf(a *Artifact) string {
	return strings.TrimSpace(artifactString(a, SupersedesField))
}

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

// AssigneeOf is who a todo says is carrying it: the field, and nothing else.
//
// IT USED TO FALL BACK TO THE BODY'S `OWNER:` LINE, and that fallback is gone.
// The line is authorship from before claims existed - nothing has written one
// since assignment became an event - and reading it as a claim is a real
// incident rather than a hypothetical: it is how three rows that nobody was
// carrying were read as held, and how the seat that then reassigned them
// believed it was taking free work.
//
// Removed on a measurement rather than on the argument. Of 192 todos on the
// live node, 45 carry no assignee field and 28 of those carry an OWNER line -
// and ALL 28 ARE DONE. The one open row without a field has no line at all. So
// the fallback answers for closed rows only, and what it answers there is
// "the author", which the assign and done events say properly, with a seat and
// a moment attached.
//
// A key that is there wins even when it is empty, which is unchanged and is the
// other half of the same rule: somebody saying out loud that nobody is carrying
// this must not be undone by a read that goes looking for another source.
//
// This is the current value and nothing else. WHO put it there and WHEN is the
// log's answer, not the row's - see AssignTodo, which writes the two together so
// that this function and that log cannot disagree.
func AssigneeOf(a *Artifact) string {
	if named := artifactField(a, AssigneeField); named != nil {
		name, _ := named.(string)
		return strings.TrimSpace(name)
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
	// Assignee narrows to the work one seat is carrying, off the COLUMN rather
	// than out of the fields blob.
	//
	// The column is generated from fields->>'assignee' - see schema.sql - so
	// this is a second READING of one fact rather than a second fact: the
	// signed payload is still where the value lives, nothing in Go writes the
	// column, and a replicated row recomputes it here instead of taking a
	// peer's word for it.
	//
	// Why it is a query at all: two agents misread this board in one afternoon
	// because status was a column and the carrier was one level down, and a
	// filter that has to dig into JSON is a filter every client writes for
	// itself and one of them writes wrongly.
	Assignee string
	// Unassigned narrows to the rows NOBODY is carrying, which is a different
	// question from Assignee being empty: empty means the caller did not ask.
	// Set from any of the words this queue has used for that state - see
	// NobodyName - so one state has one answer however it was typed.
	Unassigned bool
	Room       string // the chat room the artifact was raised in - fields->>'room'
	// Category narrows to one kind of work out of the closed set - see
	// TodoCategories. This is the routing half of what a closed set is FOR: "give
	// me the bugs" has to be a query the node answers rather than a filter each
	// client writes over a free-text column, or the ontology is decoration.
	Category string
	// Tags narrows to the artifacts carrying EVERY one of these labels. Two
	// tags mean AND rather than OR, because that is what stacked filters mean
	// to the person clicking them: picking `serenedb` and then `ragflow` asks
	// for the rows that are both, and a second click that WIDENED the answer
	// would be the same wrong-answer-shaped-like-a-right-one this filter exists
	// to stop.
	//
	// A tag matches either column of labels - tags and user_tags - because
	// every reader here already draws them as one list (todoTags in the console,
	// the TUI's artifact line), so the chip somebody clicked may have come from
	// either. A filter that knew only about `tags` would answer nothing for half
	// the chips it was offered, and an empty page reads as "there are none".
	Tags       []string
	Visibility string // personal|project|shared - the memory scopes
	Query      string // free text; SearchArtifacts only
	ScopeAll   bool   // ?scope=all - honoured only for the operator principal
	Limit      int
	// QueuedOrder answers OLDEST FIRST, by the order the rows were created.
	//
	// The default here is `updated DESC`, which is right for a board somebody
	// is browsing - what changed lately is what they came to see - and wrong
	// for anything that CONSUMES a list in turn. A queue sorted by last write
	// is not a queue: any write reorders it, and none of the writes are about
	// readiness.
	//
	// Measured on 2026-08-18. The merge queue read this list, the drainer took
	// the first row it could work, and so: a row jumped to the head the moment
	// it was filed, a declare moved a row being gated ahead of every row never
	// tried, and batch/orchestrator-evening - queued at 19:55 and touched by
	// nobody since - was last in every answer for two hours. Its turn was not
	// coming, it was arriving only when the queue emptied.
	//
	// created ASC rather than a position column: the moment a row was queued is
	// a fact about it that no later write changes, which is the whole property
	// the sort needs and the one `updated` does not have.
	QueuedOrder bool
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

// order is the sort this query runs under, as SQL.
//
// IT IS INSIDE THE LIMIT, which is the half worth stating: the LIMIT applies to
// the sorted rows, so a queued-order read of a queue longer than one page hands
// back the OLDEST page - the rows next to be worked - rather than the most
// recently written ones re-sorted after the fact. Sorting in Go after the query
// would have looked identical on a five-row queue and dropped the oldest rows
// on a long one, which is the version of this bug that would not have been
// noticed.
func (q ArtifactQuery) order(alias string) string {
	// PRIORITY LEADS BOTH ORDERS, and that is a change to the DEFAULT list as
	// well as to the queued one.
	//
	// Measured while building it: the room's todo panel and GET /api/artifacts
	// take the default - most recently touched first - so a ranking applied only
	// to the queued order was invisible in the two places anybody looks. A
	// priority nothing sorts by is a label.
	//
	// Within a rank the existing order is untouched: recency for the default,
	// age for the queue. So a board with nothing ranked reads exactly as it did.
	if q.QueuedOrder {
		// PRIORITY FIRST, THEN AGE. The queue was strictly oldest-first, which
		// is a fair rule and not an answer to "what should I do next" - the
		// operator asked for one on the board, with sixteen unowned rows on it
		// and nothing saying which of them they wanted.
		//
		// The CASE is priorityRank in SQL, and the two must agree: now, next,
		// UNJUDGED, later. An unranked row sorts above `later` deliberately -
		// it may be the most urgent thing here and nobody has looked, while a
		// `later` row has somebody's decision on it. An unknown word sorts with
		// the unjudged for the same reason PriorityRankOf does: a value from a
		// newer peer must not silently reorder this board.
		//
		// Age still breaks every tie, so within a rank the queue is exactly
		// what it was.
		return priorityOrderSQL(alias) + ", " + alias + ".created ASC, " + alias + ".id ASC"
	}
	return priorityOrderSQL(alias) + ", " + alias + ".updated DESC, " + alias + ".id DESC"
}

// priorityOrderSQL is priorityRank as a sort key over the fields column. It is
// generated from the same map the Go side reads, so a fourth word cannot be
// added to one and forgotten in the other - which is how a board comes to have
// two orders depending on who asked.
func priorityOrderSQL(alias string) string {
	col := "lower(trim(coalesce(" + alias + ".fields->>'" + PriorityField + "', '')))"
	sql := "CASE " + col
	for _, word := range TodoPriorities {
		sql += fmt.Sprintf(" WHEN '%s' THEN %d", word, priorityRank[word])
	}
	return sql + fmt.Sprintf(" ELSE %d END ASC", priorityRank[""])
}

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
	if q.Assignee != "" {
		where += " AND " + alias + ".assignee = " + a.next(q.Assignee)
	}
	// NOBODY IS A STATE, NOT A NAME. "which rows is nobody carrying" is the
	// question the board is read for, and an absent parameter cannot ask it: an
	// empty Assignee means "do not filter", so a caller who wanted the unowned
	// rows and got every row would be reading the whole queue as unclaimed.
	//
	// Both halves of empty, because both exist: the column is generated from
	// fields->>'assignee', so a row with no key at all is NULL and a row saying
	// out loud that nobody has it is the empty string. One state, one answer.
	if q.Unassigned {
		where += " AND (" + alias + ".assignee IS NULL OR " + alias + ".assignee = '')"
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
		where += " AND " + fieldEq(alias, RoomField, a.next(q.Room))
	}
	if q.Category != "" {
		// What kind of work it is, out of fields, and narrowing exactly as the
		// room does: a todo nobody classified drops out of a narrowed list and
		// stays in every unnarrowed one. artifacts_category_idx is what keeps
		// it off a sequential scan, and it is partial for the same reason the
		// supersedes one is - the rows carrying the key are the minority while
		// the queue catches up. See fieldEq for why the clause is shaped the
		// way it is, and for the measurement that says this comment was a
		// wish until now.
		where += " AND " + fieldEq(alias, CategoryField, a.next(q.Category))
	}
	for _, tag := range q.Tags {
		// One clause per tag, so several are ANDed - see the field. The two
		// label columns are concatenated rather than tested separately because
		// they are one list to every reader, and coalesce because a row with no
		// tags at all has NULL there, and NULL || anything is NULL.
		where += " AND " + a.next(tag) + " = ANY(coalesce(" + alias + ".tags, '{}'::text[])" +
			" || coalesce(" + alias + ".user_tags, '{}'::text[]))"
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
	           ORDER BY ` + q.order("ar") + `
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
	fillRoom(out)
	fillCategory(out)
	fillRaiser(out)
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

// fillRoom puts the room the work was raised in on the row itself, beside
// Assignee and Category.
//
// It is fillAssignee one field along and for its reason: the facts a queue is
// read by have to come back in one shape from one read. A reader that had to
// dig `room` out of the fields blob is a reader that has to know the key, and a
// second reader that spells it differently is the disagreement RoomOf exists to
// prevent.
//
// The room is the WORK boundary - which board this is on - as distinct from the
// project, which is the permission boundary. See 01M0E26T9T. Every per-room
// projection reads this.
func fillRoom(arts []*Artifact) {
	for _, art := range arts {
		if art != nil {
			art.Room = RoomOf(art)
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

// fillRaiser puts who the work came from on the row itself, beside Assignee.
//
// It is fillAssignee one field along, with one difference that is the whole of
// what a raiser is: there is nothing to fall back to. AssigneeOf reads a legacy
// OWNER line for the rows written before its field existed; a row written
// before THIS field simply does not say where its work came from, and the
// answer is empty rather than owner_user - which is the row's signing author
// and a different question. See RaiserField.
//
// Called beside fillAssignee in the permission-filtered read paths only, so a
// derived value never reaches the sync paths or the signature.
// FillDerived puts the derived queue fields on rows a WRITE is about to answer
// with, which the read paths do for rows they return and a write path cannot
// borrow.
//
// The three fills below run inside the permission-filtered reads, deliberately,
// so a derived value never reaches sync or the signature. A door that CREATES a
// row answers with the artifact it just wrote, which never went through those
// reads - so `fields.raiser` was set and `item.raiser` came back null, and the
// gate caught exactly that. Every client then has to decide whether a null
// top-level field means "no raiser" or "this response does not carry it", which
// is the ambiguity these fields exist to remove.
//
// Exported because the doors live in package main. It takes the row it is given
// and asks it nothing else - there is no query here and no permission decision,
// because the caller has already made both by writing the row.
func FillDerived(arts ...*Artifact) {
	fillAssignee(arts)
	fillRoom(arts)
	fillCategory(arts)
	fillRaiser(arts)
}

func fillRaiser(arts []*Artifact) {
	for _, art := range arts {
		if art != nil {
			art.Raiser = RaiserOf(art)
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
		  WHERE ar.fields ? '`+SupersedesField+`'
		    AND ar.fields->>'`+SupersedesField+`' = ANY(`+idsArg+`)
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
	fillRoom(found)
	fillCategory(found)
	fillRaiser(found)
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
	fillRoom([]*Artifact{art})
	fillCategory([]*Artifact{art})
	fillRaiser([]*Artifact{art})
	// And what has been learned about it since it was filed. It is on the
	// single-row read and not on the list beside it: whoever opened one row is
	// the reader the notes are for, and a list carrying every note on every row
	// would pay for them on every queue draw. See fillNotes.
	if err := d.fillNotes(ctx, p, []*Artifact{art}); err != nil {
		return nil, err
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
		        sig_form = $7, updated = now()
		  WHERE id = $1 AND coalesce(owner_user, '') = $4`,
		art.ID, art.HLC, art.Node, p.UserID, art.Sig, []byte(art.Fields), art.SigForm)
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

// ArtifactVocabulary is which values of one column this reader would actually
// find something under, with how many rows each - "the kinds there are", asked
// of the data rather than of a list somebody maintains.
//
// WHY IT IS NOT A CONSTANT SET. `category` is closed and is checked against a
// list, and api.go says why: "give me the bugs" is only a question with an
// answer if there is one word for bugs. `kind` is not that. Measured on the
// dogfood node 2026-08-20: todo, merge, binary, note, node, text - and 25 rows
// carrying no kind at all. A hardcoded list would refuse callers asking about
// values that genuinely exist.
//
// WHY IT IS PERMISSION-FILTERED, through the same clause the list itself uses:
// the answer to "what kinds are there" must be the answer to "what kinds would
// I see", or it is a hint that leads a reader to a filter which then returns
// nothing - which is where they started. It also means two people on one node
// get different vocabularies, honestly.
//
// COLUMN IS A CALLER-SUPPLIED IDENTIFIER and is therefore checked against a
// closed set here rather than interpolated. There is no way to parameterise a
// column name in SQL, so the only safe version of this function is one that
// cannot be handed an arbitrary string.
func (d *DB) ArtifactVocabulary(
	ctx context.Context, p *Principal, column string, scopeAll bool,
) (map[string]int, error) {
	var col string
	switch column {
	case "kind":
		col = "ar.kind"
	case "type":
		col = "ar.type"
	default:
		return nil, fmt.Errorf("store: %q is not a column with a vocabulary", column)
	}
	ctx, span := otel.Start(ctx, otel.KindQuery, "artifacts.vocabulary")
	defer span.End()
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	query := `SELECT ` + col + ` AS v, count(*) AS n
	            FROM artifacts ar
	           WHERE coalesce(ar.tombstone, false) = false
	             AND ` + col + ` IS NOT NULL AND ` + col + ` <> ''
	             AND ` + filter + `
	           GROUP BY v
	           ORDER BY n DESC`
	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		span.Fail("the vocabulary did not run")
		return nil, fmt.Errorf("store: artifact vocabulary: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var v string
		var n int
		if err := rows.Scan(&v, &n); err != nil {
			return nil, fmt.Errorf("store: artifact vocabulary: %w", err)
		}
		out[v] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: artifact vocabulary: %w", err)
	}
	return out, nil
}
