package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/hlc"
	"github.com/deadtrickster/flowy/internal/otel"
)

// Replication, the store half.
//
// A node syncs with a peer by reading the peer's rows since a cursor and
// applying them locally, and by handing the peer its own rows since a second
// cursor. Both halves go through the same two pieces of code: SyncPull, which
// is a permission-filtered read of every replicated table, and syncApply, which
// is the merge - reached as SyncApplyFrom on the half this node asked for and
// as SyncApplyAs on the half that turned up, which name where a delta came from
// and run the same rules over it. See the note on the two doors below.
//
// The merge rules, which are the whole of the consistency story:
//
//   - events are append-only, so an event that is already here is already
//     right: insert when the id is new, ignore it otherwise. Nothing about an
//     event is ever updated, including by replication.
//   - artifacts, tasks and grants are last-writer-wins by hlc, and by node name
//     when two readings tie. An incoming row replaces the local one when it
//     orders after it, so the two nodes pick the same winner whichever order the
//     rows arrive in and however many times they arrive. The tie matters:
//     a packed hlc carries a wall reading and a logical counter and nothing
//     about who made it, so two nodes writing in the same millisecond with the
//     same counter produce two readings that are equal and two rows that are
//     not. Comparing on the hlc alone, each node refuses the other's row and
//     they stay different forever, silently. The node name is the tiebreak that
//     makes the order total, and any order both sides agree on will do. A
//     tombstone is a column on the row rather than an
//     absence, so a delete wins over an older write for exactly the same
//     reason - and loses to a newer one, which is what makes an edit after a
//     delete on another node come back rather than vanish.
//   - an hlc is never lowered. The WHERE on the upsert is the only place that
//     is decided.
//
// Applying a remote row also advances this node's clock past the reading it
// carries, so the next local write orders after everything it has seen. That is
// what keeps causality: an edit made here after pulling a peer's edit is
// strictly newer than the peer's, and wins the next merge.

// defaultSyncLimit is how many rows per table one page carries.
const defaultSyncLimit = 500

// maxSyncLimit caps what a peer may ask for in one page.
const maxSyncLimit = 5000

// SyncQuery is one page of a delta: everything the principal may read whose
// clock reading is strictly greater than Since.
type SyncQuery struct {
	Since int64
	Limit int
}

func (q SyncQuery) limit() int {
	if q.Limit > 0 && q.Limit <= maxSyncLimit {
		return q.Limit
	}
	return defaultSyncLimit
}

// SyncSet is one delta, in both directions: what GET /api/sync/pull answers and
// what POST /api/sync/push carries.
//
// HWM is the high water mark: the cursor a puller may store once it has applied
// the set. It is the greatest hlc in the set, except when a table filled its
// page - then it is the smallest of the truncated tables' greatest readings, so
// that nothing above the cursor was left behind. Rows below it that were
// already applied simply arrive again, and applying them again does nothing.
type SyncSet struct {
	Artifacts []*Artifact `json:"artifacts"`
	Events    []*Event    `json:"events"`
	Tasks     []*Task     `json:"tasks"`
	Grants    []Grant     `json:"grants"`
	// Projects are the registry rows: the referent every other row's project
	// column points at. They replicate because `project` is already inside the
	// signed payload of the rows that carry one, so a registry that stayed
	// local would leave the referent local while every reference to it is
	// federated - drift by construction.
	//
	// They are omitted when empty so that a page from a node that predates the
	// registry and a page from one that has no projects to offer look the same
	// on the wire.
	Projects []*Project `json:"projects,omitempty"`
	// Identities are the public keys this node holds, and they ride on every
	// page rather than being fetched separately: a row can only be verified by
	// the node that wrote it, and on a relayed page that node is neither end of
	// this exchange. They are not fabric rows - they are not counted, they do
	// not move a cursor, and they carry no reading of their own - so Len and
	// Counts are about the four tables, as they always were.
	Identities []NodeIdentity `json:"identities,omitempty"`
	HWM        int64          `json:"hwm"`
}

// Len is the number of rows in the set.
func (s *SyncSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Artifacts) + len(s.Events) + len(s.Tasks) + len(s.Grants) + len(s.Projects)
}

// Counts breaks the set down by table, which is what the driver and the push
// endpoint report.
func (s *SyncSet) Counts() map[string]int {
	if s == nil {
		return map[string]int{}
	}
	return map[string]int{
		"artifacts": len(s.Artifacts),
		"events":    len(s.Events),
		"tasks":     len(s.Tasks),
		"grants":    len(s.Grants),
		"projects":  len(s.Projects),
	}
}

// GrantFilterSQL narrows the grants a principal may replicate: the ones that
// name their project on either side of the edge, the ones that share something
// with them, and the ones they issued.
//
// A grant is a capability, and a capability nobody in this conversation holds
// is nobody's business - a peer that could pull every grant on the node would
// learn the shape of every project boundary it is not part of.
func GrantFilterSQL(p *Principal, alias string, a *args) string {
	if p == nil {
		return "FALSE"
	}
	user := a.next(p.UserID)
	// The set. A grant names two projects and a credential reaches several, so
	// "is either end mine" is a membership - and the `<> ''` guards go with the
	// equality they belonged to: an empty array matches nothing, which is what
	// they were there to ensure.
	projects := a.next(pq.Array(p.Reach()))
	return `(` + alias + `.to_project = ANY(` + projects + `)
	      OR ` + alias + `.from_project = ANY(` + projects + `)
	      OR ` + alias + `.subject = ` + user + ` AND ` + user + ` <> ''
	      OR ` + alias + `.granted_by = ` + user + ` AND ` + user + ` <> '')`
}

// SyncPull reads one page of everything p may see that is newer than q.Since.
//
// The permission filter is the one every other read uses - the same
// ArtifactFilterSQL, the same EventFilterSQL, the same party test on a task - so
// replication carries exactly what the peer's principal could have read one row
// at a time over the API, and a cross-project grant is what opens a project up
// to another node for the same reason it opens it up to another agent.
//
// Tombstoned rows are included on purpose: a delete has to travel, and it
// travels as a row.
func (d *DB) SyncPull(ctx context.Context, p *Principal, q SyncQuery) (*SyncSet, error) {
	ctx, span := otel.Start(ctx, otel.KindSync, "sync.delta")
	defer span.End()
	if p == nil {
		return nil, errors.New("store: sync pull without a principal")
	}
	limit := q.limit()
	set := &SyncSet{Artifacts: []*Artifact{}, Events: []*Event{}, Tasks: []*Task{}, Grants: []Grant{},
		Projects: []*Project{}}

	// Every page carries the keys, because a page can carry a third node's rows
	// - A pulls from B a row C wrote - and the puller cannot verify those
	// without C's key. They are public halves and self-signed, so handing them
	// to whoever is asking gives away nothing and lets a relay work.
	identities, err := d.SharableIdentities(ctx)
	if err != nil {
		return nil, err
	}
	set.Identities = identities

	// A cursor at or above the mark a pending row was handed over under is the
	// reader saying it applied that page. Settle those first, so this pull
	// does not hand back what the last one already delivered.
	key := pendingKey(p)
	if err := d.ackPending(ctx, key, q.Since); err != nil {
		return nil, err
	}

	// hwm tracks the greatest reading in the set; capped tracks the smallest
	// greatest reading among the tables that filled their page.
	var hwm int64
	capped := int64(0)
	note := func(last int64, n int) {
		if last > hwm {
			hwm = last
		}
		// A page that filled may have left rows above it, so the cursor cannot
		// go past the reading it stopped at. It is >= rather than == because a
		// page that fills goes on to finish the reading it stopped in - see
		// pageOf - so it comes back a row or two longer than the limit.
		if n >= limit && last > 0 && (capped == 0 || last < capped) {
			capped = last
		}
	}

	// The registry first, because it is what the rows below it point at: a peer
	// that takes this page learns the project before it learns the artifacts in
	// it, and does not have to record the name as merely observed.
	projects, err := d.syncProjects(ctx, p, q.Since, limit)
	if err != nil {
		return nil, err
	}
	set.Projects = projects
	if n := len(projects); n > 0 {
		note(projects[n-1].HLC, n)
	}

	arts, err := d.syncArtifacts(ctx, p, q.Since, limit)
	if err != nil {
		return nil, err
	}
	set.Artifacts = arts
	if n := len(arts); n > 0 {
		note(arts[n-1].HLC, n)
	}

	events, err := d.syncEvents(ctx, p, q.Since, limit)
	if err != nil {
		return nil, err
	}
	set.Events = events
	if n := len(events); n > 0 {
		note(events[n-1].SeqHLC, n)
	}

	tasks, err := d.syncTasks(ctx, p, q.Since, limit)
	if err != nil {
		return nil, err
	}
	set.Tasks = tasks
	if n := len(tasks); n > 0 {
		note(tasks[n-1].HLC, n)
	}

	grants, err := d.syncGrants(ctx, p, q.Since, limit)
	if err != nil {
		return nil, err
	}
	set.Grants = grants
	if n := len(grants); n > 0 {
		note(grants[n-1].HLC, n)
	}

	// A grant that is newer than the cursor can make an artifact that is older
	// than the cursor readable for the first time. The cursor is a clock
	// reading and the artifact's reading did not move when it was shared, so
	// paging by "newer than the cursor" alone would step straight over it and
	// never come back: the peer would hold the grant and not the thing it
	// grants. These rows go in below the high water mark and do not move it,
	// so once the grant itself is under the cursor the extra scan stops.
	newly, over, err := d.syncNewlyVisible(ctx, p, q.Since, limit, grants)
	if err != nil {
		return nil, err
	}
	set.Artifacts = append(set.Artifacts, newly...)

	// What that rescan could not carry is written down rather than dropped.
	// One grant can open a whole project, and a project is bigger than a page.
	if len(over) > 0 {
		if err := d.holdPending(ctx, key, over); err != nil {
			return nil, err
		}
	}
	pending, err := d.drainPending(ctx, p, key, limit)
	if err != nil {
		return nil, err
	}
	set.Artifacts = append(set.Artifacts, pending...)

	set.HWM = hwm
	if capped > 0 && capped < hwm {
		set.HWM = capped
	}
	if len(pending) > 0 {
		// The debt is only settled when the reader comes back with a cursor
		// past the mark it was handed the rows under, so the mark has to move
		// even on a page that carried nothing new of its own. Nothing is
		// skipped by moving it one: a page that capped nothing has already
		// handed over everything above the cursor.
		if capped == 0 && set.HWM <= q.Since {
			set.HWM = q.Since + 1
		}
		if err := d.markPendingSent(ctx, key, ids(pending), set.HWM); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// ids is the id list of a page of artifacts.
func ids(arts []*Artifact) []string {
	out := make([]string, 0, len(arts))
	for _, a := range arts {
		out = append(out, a.ID)
	}
	return out
}

// ------------------------------------------------------------ paging a table
//
// Every table is paged the same way: everything above a cursor, in (reading,
// id) order, capped at a limit. The sort key is two columns and the cursor is
// one, and the gap between them is where rows used to disappear.
//
// Two rows can carry the same reading - two nodes writing in the same
// millisecond, or one handoff stamping its three rows together - and a LIMIT
// that falls between them hands over the first and reports its reading as the
// high water mark. The next pull asks for what is strictly greater than that,
// and the second row is never offered again: not delayed, dropped, and
// silently. The same hole in ListEvents is a chat message that never arrives.
//
// So a page that fills goes on to read the rest of the reading it stopped in.
// The cursor it then reports names a reading every row of which has been handed
// over, which is what an integer cursor has to mean for paging by it to be
// safe. A tie is bounded by how many nodes wrote in one instant, so finishing
// one costs a handful of rows and not a second page.

// tieAt is where a page stopped: the reading its last row carried, and that
// row's id. It is the half of the sort key a cursor cannot carry.
type tieAt struct {
	hlc int64
	id  string
}

// above is the row test one read uses. Without a tie it is everything after the
// cursor; with one it is the rest of the reading the page stopped in.
func above(reading, id string, since int64, tie *tieAt, a *args) string {
	if tie == nil {
		return reading + " > " + a.next(since)
	}
	return reading + " = " + a.next(tie.hlc) + " AND " + id + " > " + a.next(tie.id)
}

// below is above read the other way, for the one read that walks backwards -
// see EventsBefore. Without a tie it is everything before the cursor; with one
// it is the rest of the reading a DESCENDING page stopped in, which is the rows
// at that reading sorting below the last id it took.
//
// It is a second function rather than a sign on above's comparison because the
// tie clause is not symmetric in a way a flag would express: which side of the
// id a tie completion wants depends on which direction the page was read in,
// and a caller passing the wrong one gets a page that silently drops rows.
func below(reading, id string, before int64, tie *tieAt, a *args) string {
	if tie == nil {
		return reading + " < " + a.next(before)
	}
	return reading + " = " + a.next(tie.hlc) + " AND " + id + " < " + a.next(tie.id)
}

// limitSQL is a LIMIT clause, or nothing at all: the tie read is bounded by the
// one reading it asks for rather than by a count, because half a tie is the bug
// it is there to close.
func limitSQL(a *args, limit int) string {
	if limit <= 0 {
		return ""
	}
	return `
	           LIMIT ` + a.next(limit)
}

// pageOf reads one page and, when it filled, the rest of the reading it stopped
// in. query builds the statement for one read; at is a row's sort key.
func pageOf[T any](
	ctx context.Context, d *DB, what string, limit int,
	query func(a *args, tie *tieAt, limit int) string,
	scan func(scanner) (T, error),
	at func(T) (int64, string),
) ([]T, error) {
	out, err := readPage(ctx, d, what, func(a *args) string { return query(a, nil, limit) }, scan)
	if err != nil || limit <= 0 || len(out) < limit {
		// Short of the limit, so there was nothing after it to cut: what came
		// back is every row above the cursor, ties and all.
		return out, err
	}
	reading, id := at(out[len(out)-1])
	rest, err := readPage(ctx, d, what,
		func(a *args) string { return query(a, &tieAt{hlc: reading, id: id}, 0) }, scan)
	if err != nil {
		return nil, err
	}
	return append(out, rest...), nil
}

// readPage runs one statement and collects what it returns.
func readPage[T any](
	ctx context.Context, d *DB, what string,
	query func(a *args) string, scan func(scanner) (T, error),
) ([]T, error) {
	a := &args{}
	statement := query(a)
	rows, err := d.sql.QueryContext(ctx, statement, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	defer rows.Close()

	out := []T{}
	for rows.Next() {
		row, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: %s: %w", what, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}

func (d *DB) syncArtifacts(ctx context.Context, p *Principal, since int64, limit int) ([]*Artifact, error) {
	return pageOf(ctx, d, "sync artifacts", limit,
		func(a *args, tie *tieAt, lim int) string {
			return `SELECT ` + artifactColumns + `
	            FROM artifacts ar
	           WHERE ` + above("ar.hlc", "ar.id", since, tie, a) + `
	             AND ` + ReplicableArtifactSQL("ar") + `
	             AND ` + ArtifactFilterSQL(p, "ar", a, false) + `
	           ORDER BY ar.hlc, ar.id` + limitSQL(a, lim)
		},
		func(sc scanner) (*Artifact, error) { return scanArtifact(sc, nil) },
		func(art *Artifact) (int64, string) { return art.HLC, art.ID })
}

// syncNewlyVisible reads the artifacts below the cursor that the grants in this
// page have just opened up to p: the ones a fresh share names, and the ones in
// a project a fresh project-wide grant reaches into.
//
// Only grants that widen p's own view count. A share of somebody else's to
// somebody else changes nothing about what p may read, and re-scanning for it
// would be a page of rows p is about to be refused anyway.
//
// It returns the page and, when there was more than a page of it, the ids of
// everything it left behind. Those cannot be found again by paging forward -
// their readings are below the cursor and the grant that opened them is about
// to be below it too - so the caller writes them down in sync_pending and
// hands them over on later pulls. Dropping them silently is how a peer ends up
// holding a project-wide grant and a fraction of the project.
func (d *DB) syncNewlyVisible(
	ctx context.Context, p *Principal, since int64, limit int, grants []Grant,
) ([]*Artifact, []string, error) {
	var shared, opened []string
	for _, g := range grants {
		if g.Tombstone || g.HLC <= since {
			continue
		}
		switch {
		case g.Artifact != "":
			if p.UserID != "" && g.Subject == p.UserID {
				shared = append(shared, g.Artifact)
			}
		default:
			if p.Project != "" && g.FromProject == p.Project && g.ToProject != "" {
				opened = append(opened, g.ToProject)
			}
		}
	}
	if len(shared) == 0 && len(opened) == 0 {
		return nil, nil, nil
	}

	// where is built twice - once for the page, once for what is beyond it -
	// so it is a function of the parameter list it is spliced into.
	where := func(a *args) string {
		sinceArg := a.next(since)
		reach := []string{}
		if len(shared) > 0 {
			reach = append(reach, "ar.id IN ("+placeholders(a, shared)+")")
		}
		if len(opened) > 0 {
			reach = append(reach, "ar.project IN ("+placeholders(a, opened)+")")
		}
		return `ar.hlc <= ` + sinceArg + `
		    AND (` + strings.Join(reach, " OR ") + `)
		    AND ` + ReplicableArtifactSQL("ar") + `
		    AND ` + ArtifactFilterSQL(p, "ar", a, false)
	}

	a := &args{}
	query := `SELECT ` + artifactColumns + `
	            FROM artifacts ar
	           WHERE ` + where(a) + `
	           ORDER BY ar.hlc, ar.id
	           LIMIT ` + a.next(limit)

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, nil, fmt.Errorf("store: sync newly visible artifacts: %w", err)
	}
	defer rows.Close()

	out := []*Artifact{}
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("store: sync newly visible artifacts: %w", err)
		}
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: sync newly visible artifacts: %w", err)
	}
	if len(out) < limit {
		return out, nil, nil
	}

	// The page filled, so there may be more of it. The rest is read as ids in
	// the same order, from where the page stopped: they are what the caller
	// owes this reader.
	//
	// It is read a batch at a time rather than in one statement. One
	// project-wide grant can open a whole project, and this runs on the serving
	// node inside the request that carried the grant - so an unbounded read here
	// is a peer choosing how much of this node's memory and how long a statement
	// it likes, by minting one grant into its own project and then pulling. What
	// is collected is still every id, because the debt has to be complete or the
	// reader is quietly short of the rows the grant opened; what is bounded is
	// each statement.
	last := tieAt{hlc: out[len(out)-1].HLC, id: out[len(out)-1].ID}
	overflow := []string{}
	for {
		over := &args{}
		clause := where(over)
		batch, err := d.readCursorIDs(ctx, `SELECT ar.hlc, ar.id
		                                      FROM artifacts ar
		                                     WHERE `+clause+`
		                                       AND (ar.hlc, ar.id) > (`+
			over.next(last.hlc)+`, `+over.next(last.id)+`)
		                                     ORDER BY ar.hlc, ar.id
		                                     LIMIT `+over.next(syncBatch), over.vals, &last)
		if err != nil {
			return nil, nil, fmt.Errorf("store: sync newly visible overflow: %w", err)
		}
		overflow = append(overflow, batch...)
		if len(batch) < syncBatch {
			return out, overflow, nil
		}
	}
}

// readCursorIDs runs a query whose two columns are a reading and an id,
// collects the ids and leaves the sort key of the last row in at, which is
// where the next batch starts. A keyset rather than an OFFSET: the offset form
// re-scans everything it has already handed back, so a project big enough to
// need the batching is a project the batching then walks over and over.
func (d *DB) readCursorIDs(
	ctx context.Context, query string, vals []any, at *tieAt,
) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, query, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var (
			hlc int64
			id  string
		)
		if err := rows.Scan(&hlc, &id); err != nil {
			return nil, err
		}
		at.hlc, at.id = hlc, id
		out = append(out, id)
	}
	return out, rows.Err()
}

// readIDs runs a query whose one column is an id and collects it.
func (d *DB) readIDs(ctx context.Context, query string, vals []any) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx, query, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------- what did not fit
//
// sync_pending is the debt a pull owes a reader: artifacts a grant made
// readable below that reader's cursor and that did not fit in the page the
// grant arrived on. It is a table rather than a cleverer cursor because one
// integer cannot say "everything above here, and also these forty thousand
// older rows you have just been let into".
//
// The rows are keyed by principal and not by peer node: a pull knows which
// principal is asking, and that is all it knows. Two machines replicating as
// the same principal therefore share one debt, and each drains the part the
// other has not - which is right for the same reason the permission filter is:
// they are the same reader.

// syncBatch is how many ids one statement of the rescan reads, and how many
// one write of sync_pending carries. It is the bound on the work a single grant
// can make the serving node do in one go: the ids still all get written down,
// but as a few hundred per statement rather than a whole project in one.
//
// It is a var so the tests can shrink it and watch the batching happen on a
// handful of rows instead of on a few thousand. Nothing else writes to it.
var syncBatch = 500

// pendingKey is the reader a pending row is owed to. Unit separators, so a
// principal cannot be forged out of two others by choosing an id with a
// separator in it.
func pendingKey(p *Principal) string {
	return p.UserID + "\x1f" + p.AgentID + "\x1f" + p.Project
}

// ackPending forgets what the reader has demonstrably applied: a row handed
// over under a mark the reader has now passed is a row it holds.
func (d *DB) ackPending(ctx context.Context, key string, since int64) error {
	if since <= 0 {
		return nil
	}
	_, err := d.sql.ExecContext(ctx,
		`DELETE FROM sync_pending
		  WHERE principal = $1 AND coalesce(sent_hwm, 0) > 0 AND coalesce(sent_hwm, 0) <= $2`,
		key, since)
	if err != nil {
		return fmt.Errorf("store: sync pending ack: %w", err)
	}
	return nil
}

// holdPending records artifacts owed to a reader. An id already on the list
// keeps the mark it was last sent under rather than being reset: it is the same
// debt, and re-arming it would stop it ever being settled.
//
// One statement per batch, not one per id. The list is as long as the project a
// grant just opened, and a round trip each was the same hazard the rescan's own
// read had: the serving node doing work proportional to a whole project inside
// one request, with nothing on its side bounding it.
func (d *DB) holdPending(ctx context.Context, key string, artifacts []string) error {
	return d.eachBatch(artifacts, func(batch []string) error {
		a := &args{}
		principal := a.next(key)
		values := make([]string, 0, len(batch))
		for _, id := range batch {
			values = append(values, "("+principal+", "+a.next(id)+", 0)")
		}
		_, err := d.sql.ExecContext(ctx,
			`INSERT INTO sync_pending (principal, artifact, sent_hwm) VALUES `+
				strings.Join(values, ", ")+
				` ON CONFLICT (principal, artifact) DO NOTHING`, a.vals...)
		if err != nil {
			return fmt.Errorf("store: sync pending hold: %w", err)
		}
		return nil
	})
}

// eachBatch calls fn with the non-empty ids, syncBatch at a time. It is what
// keeps the three sync_pending writes to a bounded statement each rather than
// to one statement each.
func (d *DB) eachBatch(ids []string, fn func([]string) error) error {
	batch := make([]string, 0, syncBatch)
	for _, id := range ids {
		if id == "" {
			continue
		}
		batch = append(batch, id)
		if len(batch) == syncBatch {
			if err := fn(batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) == 0 {
		return nil
	}
	return fn(batch)
}

// drainPending reads a page of what the reader is owed, through the same
// permission filter every other read uses - a share revoked since the row was
// written down is a row that is no longer owed, and it is struck off rather
// than handed over.
func (d *DB) drainPending(
	ctx context.Context, p *Principal, key string, limit int,
) ([]*Artifact, error) {
	owed, err := d.readIDs(ctx,
		`SELECT artifact FROM sync_pending WHERE principal = $1 ORDER BY artifact LIMIT $2`,
		[]any{key, limit})
	if err != nil {
		return nil, fmt.Errorf("store: sync pending read: %w", err)
	}
	if len(owed) == 0 {
		return nil, nil
	}

	a := &args{}
	query := `SELECT ` + artifactColumns + `
	            FROM artifacts ar
	           WHERE ar.id IN (` + placeholders(a, owed) + `)
	             AND ` + ReplicableArtifactSQL("ar") + `
	             AND ` + ArtifactFilterSQL(p, "ar", a, false) + `
	           ORDER BY ar.hlc, ar.id`
	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: sync pending drain: %w", err)
	}
	defer rows.Close()

	out := []*Artifact{}
	held := map[string]bool{}
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, fmt.Errorf("store: sync pending drain: %w", err)
		}
		held[art.ID] = true
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sync pending drain: %w", err)
	}

	gone := []string{}
	for _, id := range owed {
		if !held[id] {
			gone = append(gone, id)
		}
	}
	if err := d.dropPending(ctx, key, gone); err != nil {
		return nil, err
	}
	return out, nil
}

// markPendingSent records the mark a page of owed rows went out under, which is
// what the next pull settles them against.
func (d *DB) markPendingSent(ctx context.Context, key string, artifacts []string, hwm int64) error {
	if hwm <= 0 {
		return nil
	}
	return d.eachBatch(artifacts, func(batch []string) error {
		a := &args{}
		mark := a.next(hwm)
		principal := a.next(key)
		_, err := d.sql.ExecContext(ctx,
			`UPDATE sync_pending SET sent_hwm = `+mark+
				` WHERE principal = `+principal+
				` AND artifact IN (`+placeholders(a, batch)+`)`, a.vals...)
		if err != nil {
			return fmt.Errorf("store: sync pending mark: %w", err)
		}
		return nil
	})
}

// dropPending strikes rows off the list.
func (d *DB) dropPending(ctx context.Context, key string, artifacts []string) error {
	return d.eachBatch(artifacts, func(batch []string) error {
		a := &args{}
		principal := a.next(key)
		_, err := d.sql.ExecContext(ctx,
			`DELETE FROM sync_pending WHERE principal = `+principal+
				` AND artifact IN (`+placeholders(a, batch)+`)`, a.vals...)
		if err != nil {
			return fmt.Errorf("store: sync pending drop: %w", err)
		}
		return nil
	})
}

// placeholders records each value and returns the comma-separated placeholders
// that read them back, for an IN list.
func placeholders(a *args, values []string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, a.next(v))
	}
	return strings.Join(out, ", ")
}

func (d *DB) syncEvents(ctx context.Context, p *Principal, since int64, limit int) ([]*Event, error) {
	return pageOf(ctx, d, "sync events", limit,
		func(a *args, tie *tieAt, lim int) string {
			return `SELECT ` + eventColumns + `
	            FROM events e
	           WHERE ` + above("e.seq_hlc", "e.id", since, tie, a) + `
	             AND ` + EventFilterSQL(p, "e", a, false) + `
	           ORDER BY e.seq_hlc, e.id` + limitSQL(a, lim)
		},
		scanEvent,
		func(e *Event) (int64, string) { return e.SeqHLC, e.ID })
}

func (d *DB) syncTasks(ctx context.Context, p *Principal, since int64, limit int) ([]*Task, error) {
	return pageOf(ctx, d, "sync tasks", limit,
		func(a *args, tie *tieAt, lim int) string {
			return `SELECT ` + taskColumns + `
	            FROM tasks t
	           WHERE ` + above("t.hlc", "t.id", since, tie, a) + `
	             AND ` + taskPartySQL(p, "t", a) + `
	           ORDER BY t.hlc, t.id` + limitSQL(a, lim)
		},
		func(sc scanner) (*Task, error) { return scanTask(sc, false) },
		func(t *Task) (int64, string) { return t.HLC, t.ID })
}

func (d *DB) syncGrants(ctx context.Context, p *Principal, since int64, limit int) ([]Grant, error) {
	return pageOf(ctx, d, "sync grants", limit,
		func(a *args, tie *tieAt, lim int) string {
			return `SELECT g.id, g.from_project, g.to_project, coalesce(g.subject, ''),
	                 coalesce(g.artifact, ''), coalesce(g.cap, 'read'), coalesce(g.granted_by, ''),
	                 coalesce(g.hlc, 0), coalesce(g.node, ''), coalesce(g.tombstone, false), g.sig
	            FROM grants g
	           WHERE ` + above("coalesce(g.hlc, 0)", "g.id", since, tie, a) + `
	             AND ` + GrantFilterSQL(p, "g", a) + `
	           ORDER BY g.hlc, g.id` + limitSQL(a, lim)
		},
		func(sc scanner) (Grant, error) {
			var g Grant
			err := sc.Scan(&g.ID, &g.FromProject, &g.ToProject, &g.Subject, &g.Artifact,
				&g.Cap, &g.GrantedBy, &g.HLC, &g.Node, &g.Tombstone, &g.Sig)
			return g, err
		},
		func(g Grant) (int64, string) { return g.HLC, g.ID })
}

// syncProjects pages the registry, narrowed by the same filter the enumeration
// uses: the principal's own project, and the ones on the other end of a live
// grant edge. A peer is handed the names it is already working across and no
// list of what else this node holds.
func (d *DB) syncProjects(ctx context.Context, p *Principal, since int64, limit int) ([]*Project, error) {
	return pageOf(ctx, d, "sync projects", limit,
		func(a *args, tie *tieAt, lim int) string {
			return `SELECT ` + projectColumns + `
	            FROM projects p
	           WHERE ` + above("coalesce(p.hlc, 0)", "p.id", since, tie, a) + `
	             AND ` + ProjectFilterSQL(p, "p", a, false) + `
	           ORDER BY p.hlc, p.id` + limitSQL(a, lim)
		},
		scanProject,
		func(project *Project) (int64, string) { return project.HLC, project.ID })
}

// SyncResult is what one merge did: the rows it applied, and the rows it
// refused because the principal that pushed them had no business writing them.
// The reasons are carried back so a peer that is being refused can be told why
// rather than left to guess from a count.
type SyncResult struct {
	Applied map[string]int `json:"applied"`
	Refused map[string]int `json:"refused"`
	Reasons []string       `json:"reasons,omitempty"`
}

// tableIdentities is the key the identity half of a delta is counted under. It
// is in the same two maps as the four tables so that a refused key is a refused
// row as far as the driver is concerned: the cursor holds, the run stops, and
// the reason is in the report. A peer that has started serving a different key
// for a node is not a peer to go on pulling from as if nothing had happened.
const tableIdentities = "identities"

// maxReasons caps what a refusal reports, so a page of bad rows answers with a
// readable message rather than a page of its own.
const maxReasons = 8

// ErrBadReading is what a delta carrying a clock reading no clock could have
// made comes back as. It is refused whole: a single poisoned reading, merged,
// lifts this node's clock past everything it will ever write again.
var ErrBadReading = errors.New("store: clock reading is not believable")

// The two doors, and why they now ask one question.
//
// A delta arrives one of two ways. POST /api/sync/push is a delta that turned
// up unasked; a pull is a delta this node went and fetched from a peer its
// operator named. Those used to be two rules - push asked "could the pushing
// principal have written this row one at a time over the API", pull asked "does
// this row land inside the world that principal already has here" - and the two
// rules were written at different times against different holes. The result was
// not one rule with two settings. It was two partial implementations that
// overlapped: a forged owner was refused on push and taken on pull, a share
// granted by the artifact's real owner was taken on pull and refused on push, a
// grant out of this principal's project was refused on push and taken on pull.
// A forgery either door catches is a forgery the other applies, and a delta can
// be offered at whichever door does not look.
//
// So provenance - who a row names as the party behind it - is one predicate,
// mayAssert, evaluated the same way on both doors for all four row types. The
// authorisation checks that stand on top of it are the same on both doors too:
// reach, owner-does-not-change, no-project-move, a grant's direction. What is
// left of the difference between the doors is the entry point and its
// bookkeeping, which is where it belongs.
//
// None of it is the authenticity question, and none of it ever was. Every row
// goes through the signature check first - see authentic - because "may this
// principal hand me this row" and "did the node named on this row write it" are
// different questions with different answers, and a peer that answers the first
// honestly can still forge the second. Authorisation stops a peer minting
// beyond its rights; authenticity stops it impersonating a node. Both ship.
//
// And there is a THIRD question, which the two above cannot answer between
// them: did the person named as the author write it? A node signature is the
// node's word, and mayAssert reads a pinned node's word as good enough for a
// third party's row - which it has to, because relaying other people's rows is
// what federation is. So a pinned peer could write rows attributed to anybody,
// this node's own people included, and every surface rendered them as that
// person's own word. Authorship gets its own signature, made with the
// principal's key rather than any node's, and its own check - see authorshipOf
// in principal.go, run in the verify step beside authentic, on both doors and
// whatever principal is carrying the page. Being asked of every row whoever
// carries it is the point: the old rule only looked when the row named somebody
// other than the carrier, so a node syncing AS the impersonated principal
// walked straight past it.

// SyncApply merges a delta into this node with no principal at all, and reports
// how many rows of each table it actually changed. A row that lost its merge -
// an older artifact, an event that is already here - is received and not
// applied, which is what makes a second push of the same set report zeros.
//
// The whole set lands in one transaction: a peer's page is either applied or
// not, so a driver that dies mid-page resumes from a cursor that still
// describes the database.
//
// It is this node's own administration - a local merge, and the operator's
// token, which already reads everything here through ?scope=all. Everything
// that comes off a wire goes through SyncApplyAs or SyncApplyFrom.
func (d *DB) SyncApply(ctx context.Context, in *SyncSet) (map[string]int, error) {
	res, err := d.syncApply(ctx, nil, in)
	if err != nil {
		return nil, err
	}
	return res.Applied, nil
}

// SyncApplyAs merges a delta somebody pushed at this node, as p, and refuses
// the rows p had no business handing over.
//
// A peer authenticates as a principal, and the rows it hands over are rows that
// principal is claiming. Without any check at all, a push was a way around
// every rule the rest of the node keeps: a grant into a project the pusher has
// no say over, a personal artifact belonging to somebody else, a rewrite of a
// row the pusher cannot even read. Merging is last-writer-wins, so a forged row
// with a high enough reading wins and stays won.
//
// It is the same merge SyncApplyFrom runs, under the same rules - see the note
// on the two doors above. This one exists to name where the delta came from and
// to be the thing the push endpoint calls.
//
// The operator is not filtered: an operator token is this node's own
// administration, and it already reads everything here through ?scope=all.
func (d *DB) SyncApplyAs(ctx context.Context, p *Principal, in *SyncSet) (*SyncResult, error) {
	if p == nil {
		return nil, errors.New("store: sync apply without a principal")
	}
	if p.Operator {
		p = nil
	}
	return d.syncApply(ctx, p, in)
}

// SyncApplyFrom merges a delta this node pulled from a peer, as the principal
// the pull token resolves to.
//
// The pull side used to merge whatever came back with no check of any kind: a
// peer trusted to be read from was thereby trusted to write anything at all,
// including a grant that opens a project up to itself, which the next pull then
// carries out of the door. Then it grew checks of its own, and they were not
// the push door's - so the same delta, the same bytes, the same signature, was
// refused one way and applied the other. A correct peer is unaffected either
// way: every row it serves is one that principal may read there, and it lands
// here in the same world.
func (d *DB) SyncApplyFrom(ctx context.Context, p *Principal, in *SyncSet) (*SyncResult, error) {
	if p == nil {
		return nil, errors.New("store: sync apply without a principal")
	}
	if p.Operator {
		p = nil
	}
	return d.syncApply(ctx, p, in)
}

// syncRow is one row of a delta with the three questions the merge asks of it:
// whether applying it would change anything at all, whether the principal that
// handed it over had any business writing it, and what writing it does.
type syncRow struct {
	table string
	hlc   int64
	node  string
	// settled is a row that needs nothing further: applied, or ignored because
	// it loses its merge.
	settled bool
	why     string
	// verify is the authenticity question, asked of every row before anything
	// else looks at it: the node named on the row signed the row.
	verify    func(context.Context, *sql.Tx) (string, error)
	unchanged func(context.Context, *sql.Tx) (bool, error)
	check     func(context.Context, *sql.Tx) (string, error)
	apply     func(context.Context, *sql.Tx) (int, error)
}

// syncPasses is how many times the merge walks the rows it has not settled.
//
// A delta is a set and not a sequence: an artifact can need the share that
// opens it, and a share can need the artifact it shares, and one page carries
// both. One pass in a fixed order decides one of those orders and refuses the
// other, so the merge goes round again over what it refused and stops as soon
// as a pass changes nothing.
const syncPasses = 3

func (d *DB) syncApply(ctx context.Context, p *Principal, in *SyncSet) (*SyncResult, error) {
	ctx, span := otel.Start(ctx, otel.KindSync, "sync.merge")
	defer span.End()
	res := &SyncResult{
		Applied: map[string]int{
			"artifacts": 0, "events": 0, "tasks": 0, "grants": 0, "projects": 0,
			tableIdentities: 0,
		},
		Refused: map[string]int{
			"artifacts": 0, "events": 0, "tasks": 0, "grants": 0, "projects": 0,
			tableIdentities: 0,
		},
	}
	if in == nil || (in.Len() == 0 && len(in.Identities) == 0) {
		return res, nil
	}
	// Before anything is merged, and before a single reading reaches the clock.
	if err := checkReadings(in); err != nil {
		return nil, err
	}

	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: sync apply: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only when Commit did not happen

	// The keys first: a page can carry the identity of the node whose rows are
	// on it, and a row cannot be verified before its node's key is here. They go
	// in the same transaction as the rows, so a page that fails leaves neither.
	for i := range in.Identities {
		id := &in.Identities[i]
		n, why, err := d.applyIdentity(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if why != "" {
			res.Refused[tableIdentities]++
			if len(res.Reasons) < maxReasons {
				res.Reasons = append(res.Reasons, why)
			}
			continue
		}
		res.Applied[tableIdentities] += n
	}

	// Grants first: a grant is what makes the rows that follow readable, and a
	// row that lands in a project this principal has no reach into is refused.
	// Then artifacts, which a task is about, then tasks, which open a thread,
	// then the events in it.
	// The project names the rows that actually landed carry. They are collected
	// as each row is applied rather than read off the delta, because a row that
	// was refused is not a name this node is holding - see ObserveProjects.
	var named []string
	note := func(name string) { named = append(named, name) }

	rows := make([]*syncRow, 0, in.Len())
	// The registry before everything, because it is what the rest of the page
	// points at: a project applied on this pass is a project the artifacts
	// below it land in as a declared name rather than an observed one.
	for _, project := range in.Projects {
		rows = append(rows, &syncRow{
			table: "projects", hlc: project.HLC, node: project.Node,
			verify: func(ctx context.Context, tx *sql.Tx) (string, error) {
				return d.authentic(ctx, tx, project.Node, canonicalProject(project), project.Sig,
					"project "+project.ID)
			},
			unchanged: func(ctx context.Context, tx *sql.Tx) (bool, error) {
				return projectLoses(ctx, tx, project)
			},
			check: func(ctx context.Context, tx *sql.Tx) (string, error) {
				return checkProject(ctx, tx, p, project)
			},
			apply: func(ctx context.Context, tx *sql.Tx) (int, error) {
				return upsertProject(ctx, tx, project)
			},
		})
	}
	for i := range in.Grants {
		g := &in.Grants[i]
		rows = append(rows, &syncRow{
			table: "grants", hlc: g.HLC, node: g.Node,
			verify: func(ctx context.Context, tx *sql.Tx) (string, error) {
				return d.authentic(ctx, tx, g.Node, canonicalGrant(g), g.Sig, "grant "+g.ID)
			},
			unchanged: func(ctx context.Context, tx *sql.Tx) (bool, error) {
				return loses(ctx, tx, "grants", g.ID, g.HLC, g.Node)
			},
			check: func(ctx context.Context, tx *sql.Tx) (string, error) {
				return checkGrant(ctx, tx, p, g)
			},
			apply: func(ctx context.Context, tx *sql.Tx) (int, error) {
				note(g.FromProject)
				note(g.ToProject)
				return applyGrant(ctx, tx, g)
			},
		})
	}
	for _, art := range in.Artifacts {
		// Whether the share that opens this row is on the page with it, which is
		// the only reason a row lands somewhere the principal cannot otherwise
		// reach. Worked out once, off the delta, so the passes below cannot make
		// it depend on the order rows happen to settle in.
		shared := sharedInDelta(p, art, in.Grants)
		rows = append(rows, &syncRow{
			table: "artifacts", hlc: art.HLC, node: art.Node,
			verify: func(ctx context.Context, tx *sql.Tx) (string, error) {
				// THE FORM THE ROW NAMES, and a refusal when this node does
				// not know it. Falling back to v1 for an unrecognised name
				// would let the sender pick its own verifier.
				msg, err := canonicalArtifact(art)
				if err != nil {
					return "artifact " + art.ID + ": " + err.Error(), nil
				}
				why, err := d.authentic(ctx, tx, art.Node, msg, art.Sig,
					"artifact "+art.ID)
				if why != "" || err != nil {
					return why, err
				}
				// And then the other signature, which answers the other
				// question: the node said it wrote the bytes, and this says
				// whether the owner said they are theirs. A refusal here is
				// written into the withheld ledger by the same call, so that a
				// read on this side can say the list is short and why.
				mark, why, err := authorshipOf(ctx, tx, withheldRow{
					kind: withheldArtifact, id: art.ID, principal: art.OwnerUser,
					project: art.Project, visibility: art.Visibility, claimed: art.Kind,
					node: art.Node, hlc: art.HLC,
				}, canonicalArtifactAuthorship(art.OwnerUser, art), art.AuthorSig)
				if why != "" || err != nil {
					return why, err
				}
				art.Authorship = mark
				return "", nil
			},
			unchanged: func(ctx context.Context, tx *sql.Tx) (bool, error) {
				return loses(ctx, tx, "artifacts", art.ID, art.HLC, art.Node)
			},
			check: func(ctx context.Context, tx *sql.Tx) (string, error) {
				return checkArtifact(ctx, tx, p, art, shared)
			},
			apply: func(ctx context.Context, tx *sql.Tx) (int, error) {
				if art.Project != nil {
					note(*art.Project)
				}
				return applyArtifact(ctx, tx, art)
			},
		})
	}
	for _, t := range in.Tasks {
		rows = append(rows, &syncRow{
			table: "tasks", hlc: t.HLC, node: t.Node,
			verify: func(ctx context.Context, tx *sql.Tx) (string, error) {
				return d.authentic(ctx, tx, t.Node, canonicalTask(t), t.Sig, "task "+t.ID)
			},
			unchanged: func(ctx context.Context, tx *sql.Tx) (bool, error) {
				return loses(ctx, tx, "tasks", t.ID, t.HLC, t.Node)
			},
			check: func(ctx context.Context, tx *sql.Tx) (string, error) {
				return checkTask(ctx, tx, p, t)
			},
			apply: func(ctx context.Context, tx *sql.Tx) (int, error) {
				note(t.Project)
				return applyTask(ctx, tx, t)
			},
		})
	}
	for _, e := range in.Events {
		rows = append(rows, &syncRow{
			table: "events", hlc: e.SeqHLC, node: e.Node,
			verify: func(ctx context.Context, tx *sql.Tx) (string, error) {
				why, err := d.authentic(ctx, tx, e.Node, canonicalEvent(e), e.Sig, "event "+e.ID)
				if why != "" || err != nil {
					return why, err
				}
				// The node's word that it relayed this, then the actor's own
				// word that they wrote it. The second is the one the log needs:
				// a signature from a pinned relay says nothing whatever about
				// whose name is in the actor column. An event carries no
				// visibility of its own, so the ledger's reach for it is its
				// project - see withheldRow.
				mark, why, err := authorshipOf(ctx, tx, withheldRow{
					kind: withheldEvent, id: e.ID, principal: e.Actor,
					project: e.Project, claimed: e.Type, node: e.Node, hlc: e.SeqHLC,
				}, canonicalEventAuthorship(e.Actor, e), e.AuthorSig)
				if why != "" || err != nil {
					return why, err
				}
				e.Authorship = mark
				return "", nil
			},
			unchanged: func(ctx context.Context, tx *sql.Tx) (bool, error) {
				return eventIsHere(ctx, tx, e.ID)
			},
			check: func(ctx context.Context, tx *sql.Tx) (string, error) {
				return checkEventRow(ctx, tx, p, e)
			},
			apply: func(ctx context.Context, tx *sql.Tx) (int, error) {
				if e.Project != nil {
					note(*e.Project)
				}
				return applyEvent(ctx, tx, e)
			},
		})
	}

	// The highest reading actually written, and the node that wrote it. The
	// clock is moved once, after the commit: see below.
	var (
		highest     int64
		highestNode string
	)
	for pass := 0; pass < syncPasses; pass++ {
		moved := false
		for _, row := range rows {
			if row.settled {
				continue
			}
			// Authenticity first, and before the merge order is even
			// consulted: a row whose signature does not verify is not a row
			// this node has any business reasoning about. It is refused
			// whatever it would have done to what is here.
			if row.verify != nil {
				why, err := row.verify(ctx, tx)
				if err != nil {
					return nil, err
				}
				if why != "" {
					row.why = why
					continue
				}
			}
			// A row that loses its merge is not a write, so it is not a
			// refusal either: it is a delta being replayed. Deciding that
			// first is what keeps a peer's own rows coming back at it from
			// being reported - and retried - as rows it was refused.
			old, err := row.unchanged(ctx, tx)
			if err != nil {
				return nil, err
			}
			if old {
				row.settled, row.why = true, ""
				continue
			}
			why, err := row.check(ctx, tx)
			if err != nil {
				return nil, err
			}
			if why != "" {
				row.why = why
				continue
			}
			n, err := row.apply(ctx, tx)
			if err != nil {
				return nil, err
			}
			res.Applied[row.table] += n
			if row.hlc > highest {
				highest, highestNode = row.hlc, row.node
			}
			row.settled, row.why, moved = true, "", true
		}
		if !moved {
			break
		}
	}

	for _, row := range rows {
		if row.settled {
			continue
		}
		res.Refused[row.table]++
		if len(res.Reasons) < maxReasons {
			res.Reasons = append(res.Reasons, row.why)
		}
	}

	// What landed here naming a project this node has no row for. The merge
	// does not refuse those rows - a page can carry an artifact whose registry
	// row the puller was never handed, because a grant reads into a project
	// without being of it - so the name is recorded as observed instead, in the
	// same transaction as the rows that named it. Dropping the rows would be
	// losing replicated work to an ordering accident; saying nothing would be
	// an enumeration that does not list a project this node is holding.
	if err := ObserveProjects(ctx, tx, named); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: sync apply: %w", err)
	}

	// The clock moves once, here, and only for rows that are now in the
	// database. It used to move inside the merge, a row at a time, before the
	// commit that decides whether any of those rows exist - so a page that
	// failed halfway rolled its writes back and left the clock standing past
	// readings this node never held. Nothing undoes that: the node then stamps
	// everything it writes afterwards above rows it does not have, and the
	// peer that does have them loses every merge against a node that never
	// applied them.
	//
	// One observation of the highest reading is enough. UpdatePacked only ever
	// moves the clock forward, so the maximum subsumes the rest.
	d.observe(highest, highestNode)
	return res, nil
}

// loses reports whether the row already here wins the merge against the reading
// and node offered, so that applying the incoming row would change nothing.
//
// It is the same total order the upsert's WHERE clause uses, asked before the
// row is judged rather than after: replaying a delta is not a write and must
// not be reported as a refusal, or a peer whose rows come back at it holds its
// cursor forever on rows it already has.
func loses(ctx context.Context, tx *sql.Tx, table, id string, hlc int64, node string) (bool, error) {
	var (
		here sql.NullInt64
		who  sql.NullString
	)
	err := tx.QueryRowContext(ctx,
		`SELECT coalesce(hlc, 0), coalesce(node, '') FROM `+table+` WHERE id = $1`, id).
		Scan(&here, &who)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: sync merge %s %s: %w", table, id, err)
	}
	if here.Int64 > hlc {
		return true, nil
	}
	return here.Int64 == hlc && who.String >= node, nil
}

// eventIsHere reports whether the log already holds this event. The log is
// append-only, so an id that is here is a row that is already right.
func eventIsHere(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var here bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM events WHERE id = $1)`, id).Scan(&here); err != nil {
		return false, fmt.Errorf("store: sync merge event %s: %w", id, err)
	}
	return here, nil
}

// checkReadings refuses a delta carrying a reading no clock could have made:
// negative, or further ahead of this machine than hlc.MaxSkew.
//
// It is the whole delta rather than the row, and it runs before the merge
// rather than during it, because the damage is not the row. Applying a row
// advances this node's clock past the reading it carries, so one reading of
// MaxInt64 leaves the node unable to write anything that orders after what it
// already holds - permanently, on every node the reading reaches.
func checkReadings(in *SyncSet) error {
	nowMS := time.Now().UnixMilli()
	bad := func(kind, id string, packed int64) error {
		return fmt.Errorf("%w: %s %s carries %d", ErrBadReading, kind, id, packed)
	}
	// SIGNING BINDS A DATE TO ITS AUTHOR WITHOUT BOUNDING IT, and a peer
	// authoring a forgery signs its own. Measured: dates of 2024 and 2027 were
	// both accepted and displayed as when the subject said it, on a row the
	// receiver's own user never wrote. The hlc has had this treatment since it
	// existed - a reading no working clock could have produced is refused, and
	// the whole delta with it - and `created` is the value a person actually
	// reads off the screen, so it is the one a forged date is read from.
	//
	// The bound is one-sided on purpose. A row genuinely older than this node
	// is ordinary - that is what replicating a log means - so only the future
	// is refused, at the same MaxSkew the hlc uses, because a date further
	// ahead than a wrong clock could explain is not a clock.
	future := time.Now().UTC().Add(hlc.MaxSkew)
	late := func(kind, id string, at time.Time) error {
		return fmt.Errorf("%w: %s %s says it was created at %s, which is further ahead than a clock can be wrong",
			ErrBadReading, kind, id, at.UTC().Format(time.RFC3339))
	}
	for _, a := range in.Artifacts {
		if !hlc.BelievableAt(a.HLC, nowMS) {
			return bad("artifact", a.ID, a.HLC)
		}
		if a.Created.After(future) {
			return late("artifact", a.ID, a.Created)
		}
	}
	for _, e := range in.Events {
		if !hlc.BelievableAt(e.SeqHLC, nowMS) {
			return bad("event", e.ID, e.SeqHLC)
		}
		if e.Created.After(future) {
			return late("event", e.ID, e.Created)
		}
	}
	for _, t := range in.Tasks {
		if !hlc.BelievableAt(t.HLC, nowMS) {
			return bad("task", t.ID, t.HLC)
		}
	}
	for i := range in.Grants {
		if !hlc.BelievableAt(in.Grants[i].HLC, nowMS) {
			return bad("grant", in.Grants[i].ID, in.Grants[i].HLC)
		}
	}
	for _, project := range in.Projects {
		if !hlc.BelievableAt(project.HLC, nowMS) {
			return bad("project", project.ID, project.HLC)
		}
	}
	return nil
}

// authentic answers why a replicated row is not the row it says it is, or ""
// when it is. It is the first question the merge asks of every row, in both
// directions, and it is a different question from every other check in this
// file.
//
// The rest of the merge asks what the principal handing a row over is allowed
// to write. That is worth asking and it is not enough: a peer serves rows it
// did not write - that is what federation is - so the principal being entitled
// to carry a row says nothing about who wrote it. Without this check, a peer
// answering a pull could take any artifact, task, grant or event the puller can
// read, rewrite every column, put the original node's name back on it, raise
// the reading and hand it over. Last-writer-wins would then make the rewrite
// the truth, on the pulling node and on every node it replicates to after that.
// Nothing in the old checks could see it: the row lands where it always landed,
// owned by whoever always owned it, and only its contents are somebody else's.
//
// So: the node named on the row has a key here, the row carries a signature,
// and that signature is that node's over the canonical encoding of the row. A
// row that fails any of the three is refused with the reason, exactly like a
// row that fails an authorisation check - the peer is told, the cursor holds,
// and nothing is written.
//
// Under FLOWY_REQUIRE_PINNED_PEERS a key that only arrived over the wire is not
// enough either: the operator has to have pinned it.
func (d *DB) authentic(
	ctx context.Context, tx *sql.Tx, node string, msg, sig []byte, what string,
) (string, error) {
	if node == "" {
		return what + " names no node, so there is nobody whose signature it could carry", nil
	}
	if len(sig) == 0 {
		return what + " carries no signature from node " + node, nil
	}
	public, pinned, ok, err := identityOf(ctx, tx, node)
	if err != nil {
		return "", err
	}
	if !ok {
		return what + " is from node " + node + ", whose key this node does not hold: " +
			"pin it with `flowy identity pin` or let it arrive on a page", nil
	}
	if d.requirePinned && !pinned {
		return what + " is from node " + node + ", whose key was taken on trust rather than " +
			"pinned by the operator, and " + requirePinnedEnv + " is set", nil
	}
	if !verifyBytes(public, msg, sig) {
		return what + ": signature from node " + node + " does not verify", nil
	}
	return "", nil
}

// mintedEventTypes are the event types a handler of this node writes and
// nobody hands over: a lifecycle move, a handoff, something the forge bridge
// did. Each one is a claim the node itself makes, and a trail is only worth
// reading if the only way to get an entry in it is to have done the thing.
//
// It is the same list POST /api/events refuses by hand - see mintedTypes in
// api.go, and the test in the server package that holds the two together. A
// push that could carry them would be the way round that endpoint.
//
// The quiesce log is on it for a second reason as well as that one. A hold is a
// claim that a process on *this* node depends on a resource, and an ack is that
// process answering; neither is a fact about the fabric, and neither is a thing
// a peer is in a position to assert on somebody else's behalf. So an
// announcement travels - it is an artifact, and the whole point of federation
// scope is that it reaches every node - and the answers to it are settled on
// the node they were given on, by the node that is waiting for them.
var mintedEventTypes = map[string]bool{
	"status":            true,
	"task":              true,
	"forge":             true,
	EventAnnouncement:   true,
	EventQuiesceHold:    true,
	EventQuiesceRelease: true,
	EventQuiesceAck:     true,
	// A vote and a closure, for the first reason: both are claims made by
	// doing the thing, and the refusals that make them mean anything - a vote
	// from somebody who can read the proposal, and no vote after it closed -
	// are on the verb. A vote written by hand is a vote cast an hour after the
	// decision was recorded, counted, with those refusals walked past.
	EventProposalVote:  true,
	EventProposalClose: true,
	// An edge in the queue, and the entry that takes one back. Both are minted
	// for the first reason and for a second one that is sharper: the refusals
	// that make the graph safe to automate against - both ends readable, both
	// ends queue items, no self-edge, no cycle, and a dependent with a project so
	// the edge reaches the todo's readers - are ALL on the verb. An event a
	// client could hand over would be an edge with none of them asked, and the
	// thing reading that edge is a machine deciding whether to start work.
	EventDepAdd:    true,
	EventDepRemove: true,
	// A pin and the entry that takes one down, for the first reason: the
	// refusals that make a strip trustworthy - the message exists, this reader
	// can see it, and it was said IN THIS ROOM - are all on the verb. A pin
	// written by hand is a line in a room's strip pointing at a message that
	// room's readers may not be able to open.
	EventPinAdd:    true,
	EventPinRemove: true,
	// A bookmark and the entry that drops one. Minted for the pin's first
	// reason - the refusal that makes one honest, that the writer can READ the
	// message, is on the verb - and for a second one that is about the shape:
	// a bookmark is private because it carries no project and no room, and a
	// hand-written one could carry both and put a reader's private list into
	// somebody else's room.
	EventBookmarkAdd:    true,
	EventBookmarkRemove: true,
	// An assignment. It is minted for the first reason - the claim is made by
	// going through the verb - and because the refusal that makes it safe is on
	// the verb: the writer has to be able to READ the todo. An entry a client
	// could hand over would be a handover asserted about work the writer cannot
	// see, and the value that entry is the record of would have been written by
	// nobody.
	EventTodoAssign: true,
	// A step in a negotiation over who carries the work. Minted for the first
	// reason and for a sharper one: the whole protocol is refusals on the verb -
	// only the seat that asked may take, only after the deadline the NODE
	// stamped, and only while the same party still holds it. A step a client
	// could hand over would be a take with the deadline written by the taker,
	// which is the one thing this file exists to prevent.
	EventTodoSteal: true,
	// A queue move, for the same two reasons. The refusal that makes it safe is
	// on the verb - the mover has to be able to READ the todo - and the status it
	// records is written in the same transaction, so an entry handed over here
	// would be a closure nobody made about work that never moved.
	EventTodoStatus: true,
	// And an openspec lifecycle move, for the same two reasons. Every refusal
	// that makes the state trustworthy is on the verb - the edge must be on the
	// line, and a move into complete must pass both arms - and the state is
	// written in the same transaction as the entry, so a transition carried in
	// here would be a lifecycle move nobody made about a state that never
	// moved. A vouched peer still carries its own: the refusal is for a relay
	// that is not a speaker, same as every other minted type.
	EventOpenspecTransition: true,
	// And a classification, for the same two reasons plus the one that is this
	// field's own: the CLOSED SET is enforced by the verb. An entry a client
	// could hand over would be a category outside the vocabulary with an entry
	// behind it saying somebody chose it, which is precisely the row that makes a
	// count wrong and unauditable at the same time.
	EventTodoCategory: true,
	// A repro run's verdict. Minted for the first reason - the record is only
	// worth reading if the claim was made by actually running the tree - and
	// because the refusal that keeps a run readable by the finding's own
	// readers is on the verb: see findingruns.go on why a projectless finding
	// refuses one rather than silently writing a verdict only its reporter
	// could read back.
	EventFindingRun: true,
	// And where a finding stands on somebody else's tracker. Minted for the
	// first reason - a filing is a claim that an issue exists over there, and it
	// is only worth reading if somebody made it by going through the verb - and
	// because every refusal that keeps the fact a FACT is on the verb: a state
	// inside the vocabulary, a tracker and a number behind any state but
	// unfiled, and no second filing over one that still stands. An entry handed
	// over here would be a filing with none of those asked, sitting beside a row
	// that never moved.
	EventFindingUpstream: true,
	// And how strong a finding's evidence is. Minted for the first reason - the
	// claim is only worth reading if somebody made it through the verb - and
	// because the refusal the whole axis exists for is on the verb: `verified`
	// names the commit its reproduction ran against, and a report whose repro was
	// never run against current main is closed upstream as already-fixed. An
	// entry handed over here would be a verified claim with no commit, sitting
	// beside a row that never moved.
	EventFindingEvidence: true,
	// And an edit of a todo's words. Minted for the first reason and for the
	// one this verb is entirely made of: the entry says the edit was written
	// against a state the row was actually in, and the only thing that makes
	// that true is the compare-and-set on the verb. An entry handed over here
	// would be a lost update with a record behind it claiming it was not one.
	EventTodoEdit: true,
	// And where a row came from. Minted for the first reason - the relation is
	// only worth reading if somebody made it through the verb, which is where
	// both ends are read and the leak rules live - and because the entry IS the
	// edge: an entry a client could hand over would be provenance nobody
	// asserted, on a row whose readers cannot check either end. See origin.go,
	// which explains why this is a relation rather than an ordering.
	EventOriginAdd:    true,
	EventOriginRemove: true,
	// And a note on a row. Minted for the first reason, and for one that bites
	// harder here than anywhere else on this list: the entry IS its content, so
	// an entry a client could hand over would be words attributed to a seat that
	// never wrote them, sitting under the author's own body as what somebody
	// learned about the work. The refusals that keep a note readable by the
	// row's own readers are on the verb as well - see todonote.go on why a
	// projectless todo refuses one rather than writing a note only its writer
	// could ever read back.
	EventTodoNote: true,
}

// MintedEventType reports whether an event type is one this node's own handlers
// write rather than one a client may hand over.
func MintedEventType(kind string) bool { return mintedEventTypes[kind] }

// ActorMetaPrefix is the key prefix the handlers that mint an event write the
// speaker under: actor_kind says whether a person or their agent said it,
// actor_user says which person, actor_name says what they were called when
// they said it. The console renders them, so meta is a second
// place an event says who is talking, and every door that decides the actor
// column has to decide these as well - see speakerStripped in the server
// package, which is what the API does with them, and metaSpeaker below, which
// is what the merge does.
const ActorMetaPrefix = "actor_"

// actorMetaUser is the key inside that prefix that names a person.
const actorMetaUser = ActorMetaPrefix + "user"

// metaSpeaker reads the speaker an event's meta claims: the person named under
// actor_user, and whether the meta made any speaker claim at all.
//
// A meta that is not a JSON object claims nothing - there are no keys in it to
// claim with - and neither does one with no actor_ key. A meta that has an
// actor_ key and no actor_user claims a speaker it does not name, which is
// still a claim: it says the actor column is an agent, or is external, and the
// console renders that.
func metaSpeaker(meta json.RawMessage) (string, bool) {
	if len(meta) == 0 {
		return "", false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(meta, &fields); err != nil {
		return "", false
	}
	claimed, user := false, ""
	for k, v := range fields {
		if !strings.HasPrefix(k, ActorMetaPrefix) {
			continue
		}
		claimed = true
		if k != actorMetaUser {
			continue
		}
		var who string
		if err := json.Unmarshal(v, &who); err == nil {
			user = who
		}
	}
	return user, claimed
}

// metaClaimsAnother answers why p may not hand over an event whose meta says
// who is speaking, or "" when the meta claims nobody but p.
//
// The actor column has been decided by the token on the way in since the
// forgery in it was fixed, and by the door on the way over since checkEvent -
// but meta rode in beside it unread, and every reader that cares who is
// speaking reads meta. So `{"actor_kind":"agent","actor_user":"somebody"}` is
// the same forgery through a second door: correctly signed, correctly actored,
// and rendered everywhere as somebody else.
func metaClaimsAnother(p *Principal, e *Event) string {
	speaker, claimed := metaSpeaker(e.Meta)
	if !claimed {
		return ""
	}
	if speaker != "" && speaker == p.UserID {
		return ""
	}
	return "event " + e.ID + " says in its meta that " + named(speaker) +
		" is speaking, which is not you"
}

// metaOutrunsTheActor answers why a relayed event's meta claims more about who
// is speaking than the node relaying it has vouched for, or "" when it does
// not.
//
// A pinned node is believed about its own users, and that is the whole of what
// pinning buys: the actor column of a row it authored is taken as written. The
// meta beside it is a second place an event says who is talking - the console
// renders actor_kind and actor_user, not the actor column - so a row whose
// actor is one person and whose meta names another is a claim the node has not
// made and this one cannot check.
//
// It is deliberately narrow. An actor this node has never seen is one the
// pinned node is entitled to describe: agents are not a replicated table, so a
// message written by alice's agent arrives with an actor id nothing here can
// resolve and a meta that says which person it acts for, and refusing that
// would refuse ordinary agent chat. What is refused is a meta that renames a
// row whose actor this node can resolve to a person of its own.
func metaOutrunsTheActor(ctx context.Context, tx *sql.Tx, e *Event) (string, error) {
	speaker, claimed := metaSpeaker(e.Meta)
	if !claimed || speaker == "" || speaker == e.Actor {
		return "", nil
	}
	acts, err := agentActsFor(ctx, tx, e.Actor, speaker)
	if err != nil {
		return "", err
	}
	if acts {
		return "", nil
	}
	known, err := isLocalUser(ctx, tx, e.Actor)
	if err != nil {
		return "", err
	}
	if !known {
		return "", nil
	}
	return "event " + e.ID + " is " + named(e.Actor) + "'s and says in its meta that " +
		named(speaker) + " is speaking", nil
}

// isLocalUser reports whether a name is somebody this node holds a user row
// for, which is what makes a meta claim about them checkable here.
func isLocalUser(ctx context.Context, tx *sql.Tx, user string) (bool, error) {
	if user == "" {
		return false, nil
	}
	var here bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, user).Scan(&here); err != nil {
		return false, fmt.Errorf("store: read user %s: %w", user, err)
	}
	return here, nil
}

// speaksFor reports whether who is this principal or the agent that acts for
// it - the one party a pulled row may name without anybody having to be trusted
// about it, because the principal is the one carrying the page.
func speaksFor(p *Principal, who string) bool {
	if p == nil || who == "" {
		return false
	}
	return p.UserID != "" && who == p.UserID || p.AgentID != "" && who == p.AgentID
}

// provenance is what the one rule answers about one row: on whose word is it
// being applied?
//
// own is the carrying principal's own word. vouched is a pinned node's. A row
// needs one of the two, and which one it has decides the rules that follow -
// see checkEventRow, where a row this principal is claiming is held to what the
// API would have let them write, and a row a pinned node is vouching for is
// held to where it may land.
//
// What it is NOT, and was read as being: an answer to who wrote the row. A
// pinned node's word is enough to carry somebody else's row here and is not
// enough to make the actor column true - that is authorshipOf, which has
// already run in the verify step by the time anything below is asked. This
// rule decides where a row may land; that one decides whose word it is.
type provenance struct {
	own     bool
	vouched bool
}

// ok reports whether anybody's word stands behind the row.
func (pr provenance) ok() bool { return pr.own || pr.vouched }

// mayAssert is the provenance rule, and it is one rule: may a row asserting
// who - the owner of an artifact, the actor of an event, the grantor of a
// grant, the side handing a task over - be applied here at all?
//
// There are exactly two answers a node can give, and both doors give the same
// two:
//
//   - the row is this principal's own, or their agent's. Nobody has to be
//     trusted about that: the principal is the one carrying the page, and the
//     row says no more than they could say themselves.
//   - the row is a third party's, and then it is taken only from an authoring
//     node the operator pinned. A pinned node is one somebody decided to
//     believe, and believing a node includes believing what it says about who
//     wrote what - which is what makes ordinary federation work: alice's
//     artifact, owned by alice, written on alice's node, is exactly what a sync
//     is for, and a rule of owner-is-carrier would refuse it. From a node whose
//     key merely turned up on a page - trust on first use, which is nobody's
//     decision - a third party's row is refused.
//
// It cannot be answered with owner-is-sender, which is what the push door used
// to answer it with, because relaying other people's rows is what federation
// is; and it cannot be left unanswered, which is what the pull door used to do
// for everything except events, because then a peer serves a page of rows
// belonging to people who have never heard of it, signed perfectly well with
// its own key, and they land.
//
// node is the node the row says authored it, which the signature check has
// already tied to the bytes - so what is left, and all that is left, is whether
// that node's word is worth anything here.
//
// A nil principal is this node's own administration and asserts what it likes.
func mayAssert(
	ctx context.Context, tx *sql.Tx, p *Principal, node, who string,
) (provenance, error) {
	if p == nil {
		return provenance{own: true, vouched: true}, nil
	}
	pr := provenance{own: speaksFor(p, who)}
	_, pinned, ok, err := identityOf(ctx, tx, node)
	if err != nil {
		return provenance{}, err
	}
	pr.vouched = ok && pinned
	return pr, nil
}

// relayedBy is the refusal a pulled row gets when the party it names is a third
// party and the node that authored it is not one the operator pinned.
func relayedBy(what, who, node string) string {
	return what + " is " + named(who) + "'s and arrives from node " + node +
		", whose key nobody here pinned: a relay is not an author, so pin " + node +
		" with `flowy identity pin` or this is somebody else's row in your store"
}

// checkEvent answers why p may not push e, or "" when it may.
//
// The log is append-only and nothing ever rewrites a row of it, so the only
// question is whether this event is one p could have appended over the API -
// and the API decides two things about an event that the pusher does not: who
// wrote it, and where it landed. So both are checked here:
//
//   - the actor is the pusher. An event carrying somebody else's name is the
//     signature forgery POST /api/events was fixed to make impossible, arriving
//     by another door - and it is worse over this one, because a replicated
//     event is one every peer then holds.
//   - the project is the pusher's own, or the event has none and belongs to the
//     pusher. Writing into another project would produce a row its own author
//     cannot read back, which is what handleCreateArtifact refuses for the same
//     reason.
//   - a minted type is nobody's to write by hand.
//
// It needs no database: an event is decided entirely by what it says, because
// an id that is already here is a row that is already right.
func checkEvent(p *Principal, e *Event) string {
	if p == nil {
		return ""
	}
	if mintedEventTypes[e.Type] {
		return "a " + e.Type + " event is written by the endpoint that does the thing, " +
			"not pushed (" + e.ID + ")"
	}
	actor := p.AgentID
	if actor == "" {
		actor = p.UserID
	}
	if actor == "" || e.Actor != actor {
		return "event " + e.ID + " is signed " + e.Actor + ", which is not you"
	}
	// And the meta does not say somebody else is speaking. The actor column
	// above is the pusher by then, so a meta naming anybody else is the same
	// claim through the column beside it - the one POST /api/events strips.
	if why := metaClaimsAnother(p, e); why != "" {
		return why
	}
	if e.Project == nil {
		if (e.Actor != p.UserID || p.UserID == "") && (e.Actor != p.AgentID || p.AgentID == "") {
			return "event " + e.ID + " has no project and is not yours"
		}
		return ""
	}
	if p.Project == "" || *e.Project != p.Project {
		return "event " + e.ID + " is in project " + *e.Project + ", and you write in " + p.Project
	}
	return ""
}

// incoherentDate answers why a row's date cannot belong to its own clock
// reading, or "" when it can.
//
// created is signed, so a relay cannot rewrite somebody else's date in flight -
// that is what TestTheCreatedDateIsInsideTheSignature holds. But signing binds
// the value to whoever authored the row and does nothing to BOUND it, and an
// author writing its own forgery picks its own date: dates two years back and
// sixteen months forward were both accepted here and both displayed as the
// moment the named person spoke. Every surface that shows a time reads this
// column - the console, the TUI timeline, the inbox, the activity and worklog
// feeds - so it is a lever on every reader even while merge order stays honest,
// because ordering is seq_hlc and never this.
//
// It is checked against the row's own reading rather than against a window,
// because a window cannot tell the two directions apart. A row created long ago
// and replicated today is ordinary - a node coming back after a month away
// carries a month of them - so refusing old dates would refuse history. What is
// not ordinary is a row whose date and whose clock reading disagree: the
// reading is minted when the row is written and is already bounded forward, so
// tying one to the other polices both directions without inventing a second
// clock.
//
// It is a per-row refusal and deliberately NOT in checkReadings beside the hlc
// bound, which rejects the WHOLE delta. One incoherent row must not cost a peer
// its entire legitimate batch - that would be a denial of service anybody could
// trigger by attaching one bad row to an honest page.
func incoherentDate(created time.Time, packed int64) string {
	if created.IsZero() || packed < 0 {
		return ""
	}
	wall, _ := hlc.Unpack(packed)
	drift := created.UnixMilli() - wall
	if drift < 0 {
		drift = -drift
	}
	if drift <= hlc.MaxSkew.Milliseconds() {
		return ""
	}
	return fmt.Sprintf("says it was created %s while its clock reading says %s, %s apart",
		created.UTC().Format(time.RFC3339), time.UnixMilli(wall).UTC().Format(time.RFC3339),
		(time.Duration(drift) * time.Millisecond).Round(time.Second))
}

// checkEventRow answers why p may not hand this node e, or "" when it may. It
// is one rule, and both doors run it.
//
// A minted type is refused first and whichever way the row came. It is a claim
// about this node's own handlers that this node's own handlers did not make:
// the trail is only worth reading if the only way into it is to have done the
// thing, and neither a pusher nor a peer serving a page is a handler.
//
// Then provenance - mayAssert - and it decides which rule the row is held to,
// because who is asserting an event is not a detail of it, it is the whole of
// what an event is:
//
//   - the row is this principal's own claim, on nobody's word but theirs. Then
//     it has to be an event they could have appended over the API: the actor is
//     them exactly, the meta beside it does not name somebody else, the project
//     is one they write in, and the type is not minted. That is checkEvent, and
//     it is what POST /api/events enforces.
//
//   - the row is a third party's, and a node the operator pinned authored it.
//     Then the actor column is taken as written - that is what pinning is for,
//     and it is how alice's message reaches bob's node under alice's name - and
//     what is checked instead is where the row lands: an event in a project
//     this principal has no reach into is a peer writing into a corner of this
//     node it has nothing to do with. The meta may not outrun the actor.
//
//     "Taken as written" is exactly as far as pinning goes, and it used to be
//     the end of the story: a pinned peer could therefore write under anybody's
//     name. It is no longer, because authorshipOf has already run on this row -
//     if this node holds a key for the actor, a row at or after that key's
//     epoch without their signature never reaches here at all, and one that
//     does is marked attributed rather than as their own word.
//
//   - neither, and the event is refused. A signature says the node wrote the
//     bytes and nothing about whether the actor column is honest, so an
//     unpinned relay's event under somebody else's name would otherwise land
//     rendered everywhere as that person - permanently, because the log is
//     append-only, and onward, because the next peer pulls it too.
//
// Both ways, a thread is checked as well. Writing into a thread is not a way
// round reading it: the tasks clause in EventFilterSQL shows a thread's events
// to the parties, so a message dropped into somebody else's conversation is
// read by exactly the people whose conversation it is not.
func checkEventRow(ctx context.Context, tx *sql.Tx, p *Principal, e *Event) (string, error) {
	if p == nil {
		return "", nil
	}
	if mintedEventTypes[e.Type] {
		return "a " + e.Type + " event is written by the node that did the thing, " +
			"not carried in (" + e.ID + ")", nil
	}
	if why := incoherentDate(e.Created, e.SeqHLC); why != "" {
		return "event " + e.ID + " " + why, nil
	}
	// The third door a reaction arrives at, and the only one on the far side of
	// somebody else's node. A peer that wrote a megabyte into the body of a
	// reaction is a peer putting a paragraph where every reader of that room
	// expects a glyph, and the ceiling is not theirs to raise.
	if e.Type == EventReactionAdd || e.Type == EventReactionRemove {
		if why := ReactionBodyRefusal(e.Body); why != "" {
			return "event " + e.ID + ": " + why, nil
		}
	}
	pr, err := mayAssert(ctx, tx, p, e.Node, e.Actor)
	if err != nil {
		return "", err
	}
	switch {
	case !pr.ok():
		return "event " + e.ID + " says " + named(e.Actor) + " wrote it and arrives from node " +
			e.Node + ", whose key nobody here pinned: a relay is not a speaker, so pin " +
			e.Node + " with `flowy identity pin` or this is somebody else's name in your log", nil
	case !pr.vouched:
		if why := checkEvent(p, e); why != "" {
			return why, nil
		}
	default:
		if why, err := metaOutrunsTheActor(ctx, tx, e); why != "" || err != nil {
			return why, err
		}
		ok, err := eventReadable(ctx, tx, p, e)
		if err != nil {
			return "", err
		}
		if !ok {
			return "event " + e.ID + " lands where you read nothing", nil
		}
	}
	if why, err := threadClosed(ctx, tx, p, e.Thread, "event "+e.ID); why != "" || err != nil {
		return why, err
	}
	if why, err := publicEventInAPrivateThread(ctx, tx, e); why != "" || err != nil {
		return why, err
	}
	return artifactClosed(ctx, tx, p, e.Artifact, "event "+e.ID)
}

// eventReadable reports whether p could read e as it would land, by running the
// read filter over the incoming values rather than over a stored row.
func eventReadable(ctx context.Context, tx *sql.Tx, p *Principal, e *Event) (bool, error) {
	a := &args{}
	project := a.next(nullText(e.Project))
	actor := a.next(e.Actor)
	thread := a.next(e.Thread)
	// artifact is one of the filter's columns too - the share of one artifact
	// reaches the events about it - so the synthetic row carries it, or the
	// filter would be asked about a column that is not there.
	artifact := a.next(e.Artifact)
	// And so are the three a direct message is made of. A DM crosses a node
	// boundary like anything else - the whole point of addressing a person is
	// that they may be somewhere else - and a synthetic row missing type, room
	// or addressee would either fail on a column that is not there or answer
	// "lands where you read nothing" and refuse every replicated DM. The values
	// are the incoming row's own, so this asks of the row arriving exactly what
	// a read will ask of it once it is stored.
	eventType := a.next(e.Type)
	room := a.next(e.Room)
	addressee := a.next(e.Addressee)
	filter := EventFilterSQL(p, "e", a, false)
	var ok sql.NullBool
	err := tx.QueryRowContext(ctx,
		`SELECT `+filter+`
		   FROM (SELECT `+project+`::text AS project, `+actor+`::text AS actor,
		                `+thread+`::text AS thread, `+artifact+`::text AS artifact,
		                `+eventType+`::text AS type, `+room+`::text AS room,
		                `+addressee+`::text AS addressee) e`,
		a.vals...).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: sync check event %s: %w", e.ID, err)
	}
	return ok.Valid && ok.Bool, nil
}

// threadClosed answers why p may not write into thread, or "" when it may.
//
// The rule is the one checkTask has always used for a task's thread: a thread
// holding an event p cannot read is a conversation p is not in. A thread with
// nothing in it is nobody's yet and is allowed - there is nothing there to
// learn, and every conversation starts as one.
func threadClosed(
	ctx context.Context, tx *sql.Tx, p *Principal, thread, what string,
) (string, error) {
	if p == nil || thread == "" {
		return "", nil
	}
	hidden, err := threadHidden(ctx, tx, p, thread)
	if err != nil {
		return "", err
	}
	if hidden {
		return what + " is in thread " + thread + ", which you cannot read", nil
	}
	return "", nil
}

// publicEventInAPrivateThread answers why an event may not join the thread it
// names, or "" when it may.
//
// A private conversation holds nothing but direct messages, and every rule this
// node has about one leans on that: whether a reply may join it, whether a task
// may name it, what a client is told the thread is. An event that is not a
// direct message and lands in one breaks it - the row carries a project, so
// everybody in that project reads it, in a thread whose every other line is
// private.
//
// It is refused at both merge doors as well as at the three local ones, because
// the local refusal is about a person being misled by a message box and this one
// is about the invariant itself. A node that took the row would hold a thread
// that is half a conversation and half a room, and the next private reply into
// it would then be refused on that node and taken on the one it came from.
//
// It is not about who may READ anything: threadClosed above has already refused
// a thread the carrier cannot read, and this asks only what the row is doing.
func publicEventInAPrivateThread(ctx context.Context, tx *sql.Tx, e *Event) (string, error) {
	if e.Thread == "" || IsDirectMessage(e) {
		return "", nil
	}
	private, err := threadIsPrivate(ctx, tx, e.Thread)
	if err != nil {
		return "", err
	}
	if private {
		return "event " + e.ID + " is in thread " + e.Thread +
			", which is a private conversation: a message carrying a project would be " +
			"read by everybody in it", nil
	}
	return "", nil
}

// artifactClosed answers why p may not name artifact on an event, or "" when it
// may.
//
// The artifact column is the fourth thing an event says about somebody else's
// work - actor, thread, parents, artifact - and it was the one nothing looked
// at. The per-artifact share clause in the event filter carries the events
// about an artifact to everybody it is shared with, so an event naming an
// artifact its writer cannot read is an entry pushed into what that artifact's
// readers see, over an id that is a guess. POST /api/events refuses it through
// mayNameArtifact; both merge doors refuse it here.
//
// It is the shape threadClosed has, for the shape's reason: an artifact that is
// not on this node is not hidden from anybody, and a replicated event
// legitimately arrives before - or without - the artifact it names, so refusing
// that would refuse federation rather than injection. What is refused is an
// artifact that is here and out of the principal's reach.
func artifactClosed(
	ctx context.Context, q querier, p *Principal, artifact, what string,
) (string, error) {
	if p == nil || artifact == "" {
		return "", nil
	}
	hidden, err := artifactHidden(ctx, q, p, artifact)
	if err != nil {
		return "", err
	}
	if hidden {
		return what + " is about artifact " + artifact + ", which you cannot read", nil
	}
	return "", nil
}

// artifactHidden reports whether artifact is on this node and out of p's reach.
func artifactHidden(ctx context.Context, q querier, p *Principal, artifact string) (bool, error) {
	if p == nil {
		return true, nil
	}
	if artifact == "" {
		return false, nil
	}
	a := &args{}
	idArg := a.next(artifact)
	filter := ArtifactFilterSQL(p, "ar", a, false)
	var hidden bool
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM artifacts ar
		                 WHERE ar.id = `+idArg+`
		                   AND NOT coalesce((`+filter+`), false))`, a.vals...).Scan(&hidden)
	if err != nil {
		return false, fmt.Errorf("store: read artifact %s: %w", artifact, err)
	}
	return hidden, nil
}

// querier is what both *sql.DB and *sql.Tx satisfy, so one thread test serves
// the merge and the handlers that write into a thread over the API.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ThreadHidden reports whether thread holds an event p may not read. It is the
// read half of "you cannot write into a conversation you cannot see", and the
// endpoints that append to a thread ask it before they write.
func (d *DB) ThreadHidden(ctx context.Context, p *Principal, thread string) (bool, error) {
	return threadHidden(ctx, d.sql, p, thread)
}

// threadHidden is the same question against whatever is in hand: the merge asks
// it inside its transaction, the handlers ask it of the pool.
func threadHidden(ctx context.Context, q querier, p *Principal, thread string) (bool, error) {
	if p == nil {
		return true, nil
	}
	if thread == "" {
		return false, nil
	}
	a := &args{}
	threadArg := a.next(thread)
	events := EventFilterSQL(p, "e", a, false)
	var hidden bool
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM events e
		                 WHERE e.thread = `+threadArg+`
		                   AND NOT coalesce((`+events+`), false))`, a.vals...).Scan(&hidden)
	if err != nil {
		return false, fmt.Errorf("store: read thread %s: %w", thread, err)
	}
	return hidden, nil
}

// nullText is a project column that may not be there: NULL rather than the
// empty string, because the read filter asks `project IS NULL` to mean personal
// and an empty string is not that.
func nullText(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// checkArtifact answers why p may not hand this node a, or "" when it may.
//
// Three rules, and they are the three ways a replicated artifact escalates:
//
//   - a personal row belongs to its owner. Nobody else carries one, because
//     nobody else can read one, and a copy of somebody else's would be a write
//     into a place no read of theirs can reach.
//   - a row lands where the principal could have reached it: its own project, a
//     project a live grant opens to it, or an artifact shared to it. Without
//     this, a peer invents a row in any project it likes - including one it has
//     never been let into - and the row is then real for every node downstream.
//     shared is the one relaxation: a share riding the same page, which has not
//     been applied yet because it is waiting for this very row - see
//     sharedInDelta.
//   - a row that is already here does not change hands and does not move
//     project. Applying a row replaces every column of the one it lands on, so
//     without this a share is a way to take a row over: re-owned, re-projected
//     and then handed on. Being able to read it is not enough.
//
// And, before any of them, the fourth: the row is the owner's to assert - see
// mayAssert. An artifact owned by somebody other than the principal carrying it
// is taken only from an authoring node the operator pinned, whichever door it
// arrives at. The push door used to answer this with owner-is-sender and the
// pull door not to answer it at all, so an artifact owned by somebody who has
// never heard of the peer serving it was refused one way and applied the other,
// forged owner and all - and the name it forged then held the update and
// tombstone rights the owner column carries.
//
// It covers the row that is already here as well as the new one, because
// rewriting somebody else's artifact under a name a hostile relay signs for is
// the same forgery as inventing it.
func checkArtifact(
	ctx context.Context, tx *sql.Tx, p *Principal, a *Artifact, shared bool,
) (string, error) {
	if p == nil {
		return "", nil
	}
	// The other half of ReplicableArtifactSQL, on the door rows come in at. A
	// node-scope announcement is by definition about the node that wrote it, so
	// arriving over the wire is the one thing it cannot have honestly done -
	// and a peer that pushes one is telling this node's readers that this node
	// is going down. Refused whatever else is right about it.
	if IsLocalAnnouncement(a) {
		return "announcement " + a.ID + " is node-scope and does not cross a node boundary", nil
	}
	if why := incoherentDate(a.Created, a.HLC); why != "" {
		return "artifact " + a.ID + " " + why, nil
	}
	pr, err := mayAssert(ctx, tx, p, a.Node, a.OwnerUser)
	if err != nil {
		return "", err
	}
	if !pr.ok() {
		return relayedBy("artifact "+a.ID, a.OwnerUser, a.Node), nil
	}
	if a.Visibility == "personal" || a.Project == nil {
		if a.OwnerUser == "" || a.OwnerUser != p.UserID {
			return "artifact " + a.ID + " is personal and not yours", nil
		}
	} else {
		ok, err := artifactReadable(ctx, tx, p, a)
		if err != nil {
			return "", err
		}
		if !ok && !shared {
			return "artifact " + a.ID + " would land in project " + *a.Project +
				", which you cannot reach", nil
		}
	}

	var (
		owner   sql.NullString
		project sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT coalesce(owner_user, ''), project FROM artifacts WHERE id = $1`, a.ID).
		Scan(&owner, &project)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing here to take over, so the change-hands rule has nothing to
		// compare against. What stops a row landing here as somebody else's is
		// mayAssert above, which asks it of the row rather than of what it
		// lands on - the old push-side version of this test was written against
		// the stored row, so for a new id it never fired at all.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: sync check artifact %s: %w", a.ID, err)
	}
	if owner.String != a.OwnerUser {
		return "artifact " + a.ID + " belongs to " + owner.String +
			", and a merge does not change hands", nil
	}
	was, now := "", ""
	if project.Valid {
		was = project.String
	}
	if a.Project != nil {
		now = *a.Project
	}
	if was != now {
		return "artifact " + a.ID + " is in project " + was +
			", and a merge does not move it to " + now, nil
	}
	return "", nil
}

// sharedInDelta reports whether the page a arrived on also carries the share
// that opens it to p: a grant naming this principal as the subject and naming,
// as the grantor, the very owner the artifact says it has.
//
// The two rows need each other. The artifact is readable here because of the
// share, and the share is the owner's to give because of the artifact - so a
// merge that insists on one before the other refuses both forever, which is a
// cross-project handoff that never arrives and a cursor that never moves. That
// is what this is for, and it is not a hole because neither row is believed on
// its own: the grantor has to be the owner named on the artifact that came with
// it, and both rows are signed by the node that wrote them. What it does not do
// is let a share reach an artifact a share cannot reach - personal, project-less
// or 'project-only' - or let it stand in for the owner test on a row that is
// already here, which checkGrant asks of the stored row and not of this.
func sharedInDelta(p *Principal, a *Artifact, grants []Grant) bool {
	if p == nil || p.UserID == "" || a == nil || a.OwnerUser == "" || a.Project == nil ||
		a.Visibility == VisibilityPersonal || a.Visibility == VisibilityProjectOnly {
		return false
	}
	for i := range grants {
		g := &grants[i]
		if g.Tombstone || g.Artifact == "" || g.Artifact != a.ID {
			continue
		}
		if g.Subject == p.UserID && g.GrantedBy == a.OwnerUser {
			return true
		}
	}
	return false
}

// artifactReadable reports whether p could read a as it would land, by running
// the read filter over the incoming values rather than over a stored row. That
// is the "land where the API would put it" rule, which for a peer is wider than
// one project: what it may reach is what a grant says it may reach.
func artifactReadable(ctx context.Context, tx *sql.Tx, p *Principal, a *Artifact) (bool, error) {
	q := &args{}
	id := q.next(a.ID)
	project := q.next(nullText(a.Project))
	owner := q.next(a.OwnerUser)
	vis := q.next(a.Visibility)
	filter := ArtifactFilterSQL(p, "ar", q, false)
	var ok sql.NullBool
	err := tx.QueryRowContext(ctx,
		`SELECT `+filter+`
		   FROM (SELECT `+id+`::text AS id, `+project+`::text AS project,
		                `+owner+`::text AS owner_user, `+vis+`::text AS visibility) ar`, q.vals...).
		Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: sync check artifact %s: %w", a.ID, err)
	}
	return ok.Valid && ok.Bool, nil
}

// checkTask answers why p may not push t.
//
// A task is not only a record of a handoff: it is a read capability. The tasks
// clause in EventFilterSQL says that the parties to a task may read the thread
// it names, whichever project they write from - so a task row invented by a
// stranger, naming themselves on both sides and somebody else's thread, is a
// way to read that conversation. It is checked the way POST /api/assign is:
//
//   - a task that is already here is moved only by a party to it, and only in
//     the ways a party can move it over the API: the state and the agent it was
//     delegated to. The thread, the artifact and the two people are the shape
//     of the handoff, and a party that could re-point them would be handing
//     itself - or anybody - a read on any conversation on this node, which is
//     the same escalation as inventing the row in the first place. The two that
//     do move are checked rather than taken: an agent has to be one that acts
//     for the assignee, because naming an agent is handing it the thread, and a
//     state has to be one of the three the lifecycle has;
//
//   - a new one is a handoff its from_user is asserting, and mayAssert is what
//     says whether that assertion may be taken: the principal carrying it is
//     that person, or a node the operator pinned authored the row. The push
//     door used to ask the first and the pull door a weaker thing - that the
//     carrier was a party to it, either side - so a handoff between other
//     people arrived on an unpinned node's say-so through one door and not the
//     other, and a task is a read capability: the tasks clause in
//     EventFilterSQL shows the whole thread to from_user, to_user and the agent
//     it was delegated to.
//
//     Either way the artifact is one the carrier may read and is one that can
//     be shared at all - not personal, and not 'project-only', which no grant
//     reaches through, so a task naming one is a task whose artifact the
//     assignee gets a 404 on. The thread is one they can already read - a
//     thread nobody has said anything in yet is a thread there is nothing to
//     learn from.
//
//     And from_user is the artifact's owner and the thread is one nothing has
//     been said in yet. Both are POST /api/assign's rule: an assignment is the
//     owner's to make and it opens a fresh thread. Without them a reader of an
//     artifact could mint a task naming any thread they can read and any local
//     user in to_user, and hand that user the conversation - a task is a read
//     capability, not a note.
func checkTask(ctx context.Context, tx *sql.Tx, p *Principal, t *Task) (string, error) {
	if p == nil {
		return "", nil
	}
	var (
		exists                             bool
		thread, artifact, fromUser, toUser sql.NullString
		assignee                           sql.NullString
	)
	err := tx.QueryRowContext(ctx,
		`SELECT coalesce(thread, ''), coalesce(artifact, ''), coalesce(from_user, ''),
		        coalesce(to_user, ''), coalesce(assignee_agent, '')
		   FROM tasks WHERE id = $1`, t.ID).
		Scan(&thread, &artifact, &fromUser, &toUser, &assignee)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", fmt.Errorf("store: sync check task %s: %w", t.ID, err)
	default:
		exists = true
	}

	if exists {
		here := &Task{
			Thread: thread.String, Artifact: artifact.String,
			FromUser: fromUser.String, ToUser: toUser.String, AssigneeAgent: assignee.String,
		}
		if !taskParty(p, here) {
			return "task " + t.ID + " is already here and you are not a party to it", nil
		}
		if t.Thread != here.Thread || t.Artifact != here.Artifact ||
			t.FromUser != here.FromUser || t.ToUser != here.ToUser {
			return "task " + t.ID + " is a party's to move, not to re-point: " +
				"the thread, the artifact and the two people are how it was handed over", nil
		}
		// The two columns a party may move, and neither of them was checked.
		//
		// assignee_agent is the third read capability on the row: the tasks
		// clause in EventFilterSQL shows the thread to the agent named here, so
		// a party pushing its own task with somebody else's agent on it hands
		// that agent the conversation. Over the API only the assignee delegates,
		// and only to an agent that acts for them - see handleDelegateTask - so
		// that is the rule here too. Clearing it takes a capability away and is
		// allowed.
		if t.AssigneeAgent != "" && t.AssigneeAgent != here.AssigneeAgent {
			acts, err := agentActsFor(ctx, tx, t.AssigneeAgent, here.ToUser)
			if err != nil {
				return "", err
			}
			if !acts {
				return "task " + t.ID + " is delegated to " + t.AssigneeAgent +
					", which is not an agent of " + here.ToUser, nil
			}
		}
		// And the state is the lifecycle, not a free text column: a task parked
		// in a state nothing moves out of is a handoff that can never be closed.
		if !ValidTaskState(t.State) {
			return "task " + t.ID + " moves to state " + t.State +
				", and a task is open, delegated or done", nil
		}
		return "", nil
	}

	if p.UserID == "" {
		return "task " + t.ID + " is a handoff and this token names no user", nil
	}
	pr, err := mayAssert(ctx, tx, p, t.Node, t.FromUser)
	if err != nil {
		return "", err
	}
	if !pr.ok() {
		return relayedBy("task "+t.ID, t.FromUser, t.Node), nil
	}

	shareable := &args{}
	artArg := shareable.next(t.Artifact)
	filter := ArtifactFilterSQL(p, "ar", shareable, false)
	var canHandOver bool
	err = tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM artifacts ar
		                 WHERE ar.id = `+artArg+`
		                   AND coalesce(ar.visibility, '') <> 'personal'
		                   AND coalesce(ar.visibility, '') <> 'project-only'
		                   AND ar.project IS NOT NULL
		                   AND `+filter+`)`, shareable.vals...).Scan(&canHandOver)
	if err != nil {
		return "", fmt.Errorf("store: sync check task %s: %w", t.ID, err)
	}
	if !canHandOver {
		return "task " + t.ID + " is about " + t.Artifact + ", which is not yours to hand over", nil
	}

	// And the side handing it over is the artifact's owner, which is what POST
	// /api/assign requires. Being able to read an artifact is not being able to
	// hand it on: a task is a read capability - the tasks clause in
	// EventFilterSQL shows the whole thread to from_user, to_user and the agent
	// it was delegated to - so a reader who could mint one would be handing a
	// thread to anybody they cared to name in to_user. assignee_agent is
	// checked against the assignee above; to_user was checked against nothing.
	var owner sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT owner_user FROM artifacts WHERE id = $1`, t.Artifact).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "task " + t.ID + " is about " + t.Artifact + ", which is not here", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: sync check task %s: %w", t.ID, err)
	}
	if owner.String == "" || owner.String != t.FromUser {
		return "task " + t.ID + " hands " + t.Artifact + " over as " + named(t.FromUser) +
			", and it is " + named(owner.String) + "'s to hand over", nil
	}

	// A handoff opens a conversation; it does not join one. The entry that
	// opens a real assignment is a `task` event, and a minted type is refused
	// at every wire path - see mintedEventTypes - so a task that was genuinely
	// replicated arrives with its thread empty on this node. A new task naming
	// a thread that already holds messages is therefore not a handoff arriving:
	// it is a read on an existing conversation being written for whoever the
	// row names.
	if t.Thread != "" {
		var talking bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM events WHERE thread = $1)`, t.Thread).Scan(&talking); err != nil {
			return "", fmt.Errorf("store: sync check task %s: %w", t.ID, err)
		}
		if talking {
			return "task " + t.ID + " is a new handoff into thread " + t.Thread +
				", which is a conversation that is already here", nil
		}
	}

	return threadClosed(ctx, tx, p, t.Thread, "task "+t.ID)
}

// agentActsFor reports whether agent is an agent of user, which is the lookup
// handleDelegateTask makes before it moves a task: an agent acts for exactly
// one person, and delegating to somebody else's agent puts the work - and the
// thread the task names - outside the person whose work it is.
func agentActsFor(ctx context.Context, tx *sql.Tx, agent, user string) (bool, error) {
	if agent == "" || user == "" {
		return false, nil
	}
	var acts bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM agents WHERE id = $1 AND user_id = $2)`, agent, user).
		Scan(&acts)
	if err != nil {
		return false, fmt.Errorf("store: sync check agent %s: %w", agent, err)
	}
	return acts, nil
}

// taskParty reports whether p is one of the three people a task is between: the
// side handing the work over, the side it went to, or the agent it was
// delegated to. It is taskPartySQL's rule, in Go, for a row that is in hand.
func taskParty(p *Principal, t *Task) bool {
	if p == nil || t == nil {
		return false
	}
	return p.UserID != "" && (t.FromUser == p.UserID || t.ToUser == p.UserID) ||
		p.AgentID != "" && t.AssigneeAgent == p.AgentID
}

// checkGrant answers why p may not hand this node g, or "" when it may. It is
// the rule POST /api/grants enforces, applied to a row that arrived over the
// wire instead of in a request body, and both doors run all of it:
//
//   - the cap is one this node implements. It is a property of the row rather
//     than of who is carrying it, so it is asked first and of everybody: a
//     grant claiming a capability no reader here would honour is refused
//     whichever direction it came from. insertGrant holds the same line
//     locally.
//   - the grant reaches this principal at all - the reach GrantFilterSQL
//     replicates by. A capability between other people and other projects is
//     nobody's business to be carrying, and the push door used to ask nothing
//     of the sort.
//   - it is the grantor's to give - see mayAssert. A grant somebody other than
//     the carrier is said to have given is taken only from an authoring node
//     the operator pinned. Federation needs that: the share a handoff wrote on
//     alice's node is how the other side reads the artifact here, and it was
//     written by alice, who is not the principal carrying it. What it is not is
//     a licence for an unpinned relay to invent grantors.
//   - a project-wide grant opens a project up, so its grantor has to be
//     somebody who holds a principal in the project being opened - which is
//     POST /api/grants' own rule, where to_project has to be the caller's.
//     Without it, a peer writes a grant from its own project into one here and
//     reads it from then on, and because merging is last-writer-wins a big
//     enough reading makes the forgery permanent. It is asked of every
//     project-wide grant whose to_project is a project somebody here is in,
//     rather than only of the ones that name the carrier's own project: a grant
//     opening a project this node hosts reaches this node's work whether or not
//     the principal carrying it is in that project, and the push door's version
//     of this rule - to_project has to be the carrier's own - refused the
//     wrong things and let the rest through. A grant opening a project nobody
//     here is in opens nothing here, and refusing it would be refusing
//     federation.
//   - a share is the owner's to give: the artifact has to be here, it has to be
//     one a share can reach at all, and its owner has to be whoever the grant
//     says granted it. The claim on the row is not what is believed - the
//     artifact this node holds is - because the share clause in
//     ArtifactFilterSQL asks only for the artifact and the subject and never
//     for the grantor, so a share taken on the claim opens a read that is
//     permanent and travels onward from here.
//
// The two doors used to disagree about most of that, and not by one rule being
// a subset of the other: push refused a share the artifact's real owner wrote
// on another node, which pull applied and federation needs; pull refused a
// grant into this principal's project without a local opener, which push
// applied; push refused a grant out of this principal's project, which pull
// applied. Each hole was covered by the other door, and a peer chooses its door.
func checkGrant(ctx context.Context, tx *sql.Tx, p *Principal, g *Grant) (string, error) {
	if !GrantCapOK(g.Cap) {
		return "grant " + g.ID + " carries cap " + capSaid(g.Cap) +
			", which is not a capability this node implements", nil
	}
	if p == nil {
		return "", nil
	}
	if !grantReaches(p, g) {
		return "grant " + g.ID + " is between other people and other projects", nil
	}
	pr, err := mayAssert(ctx, tx, p, g.Node, g.GrantedBy)
	if err != nil {
		return "", err
	}
	if !pr.ok() {
		return relayedBy("grant "+g.ID, g.GrantedBy, g.Node), nil
	}

	if g.Artifact == "" {
		if g.ToProject == "" {
			return "grant " + g.ID + " opens no project up", nil
		}
		hosted, err := projectIsHosted(ctx, tx, g.ToProject)
		if err != nil {
			return "", err
		}
		if !hosted {
			// Nobody here is in the project this grant opens, so there is
			// nothing of this node's behind it: it is a capability between
			// other people's projects, riding a page this principal may read,
			// and refusing it would be refusing federation.
			return "", nil
		}
		opener, err := principalOfProject(ctx, tx, g.GrantedBy, g.ToProject)
		if err != nil {
			return "", err
		}
		if !opener {
			return "grant " + g.ID + " opens " + g.ToProject + " up, and " +
				named(g.GrantedBy) + " is nobody here who could", nil
		}
		return "", nil
	}

	var (
		owner sql.NullString
		vis   sql.NullString
		proj  sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT owner_user, visibility, project FROM artifacts WHERE id = $1`, g.Artifact).
		Scan(&owner, &vis, &proj)
	if errors.Is(err, sql.ErrNoRows) {
		return "no artifact " + g.Artifact + " here to share", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: sync check grant %s: %w", g.ID, err)
	}
	if vis.String == "personal" || !proj.Valid {
		return "artifact " + g.Artifact + " is personal and cannot be shared", nil
	}
	if owner.String == "" || owner.String != g.GrantedBy {
		return "grant " + g.ID + " is not the owner's to give", nil
	}
	return "", nil
}

// projectHere reads the registry row a merge is about to land on, or nil.
func projectHere(ctx context.Context, tx *sql.Tx, id string) (*Project, error) {
	p, err := scanProject(tx.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM projects p WHERE p.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: sync merge project %s: %w", id, err)
	}
	return p, nil
}

// projectLoses is `loses` for the registry, with one difference that decides
// whether a name collision can go unnoticed.
//
// A row that loses its merge is settled without being checked, because a delta
// being replayed is not a write. That is right for every other table, where the
// two rows are the same row. Here they may not be: a peer's `flowy` and this
// node's `flowy` can be two different projects, and if the peer's row happens
// to carry the older reading, settling it quietly would be the one thing this
// column exists to prevent - two projects meeting under one name with nobody
// told. So a collision is never settled here. It goes to checkProject, which
// refuses it and says so, whichever way the readings fall.
func projectLoses(ctx context.Context, tx *sql.Tx, in *Project) (bool, error) {
	here, err := projectHere(ctx, tx, in.ID)
	if err != nil || here == nil {
		return false, err
	}
	if same, _ := SameProject(here, in); !same {
		return false, nil
	}
	if here.HLC > in.HLC {
		return true, nil
	}
	return here.HLC == in.HLC && here.Node >= in.Node, nil
}

// checkProject is the merge's question about a registry row: may this principal
// hand this name over at all, and is the project it names the project this node
// already knows by that name.
//
// The reach test is ProjectFilterSQL, which is the same filter the pull sends
// project rows under - one rule at both doors, the way the grant path was made
// to ask one question. It is a filter over names and it is not a permission: a
// project row carries no capability, and nothing about what anybody may read
// changes when one lands.
//
// The collision test is the one that matters, and it has three branches with no
// silent merge in any of them:
//
//   - the origins meet, or one side has none to compare. One project, ordinary
//     last-writer-wins.
//   - the origins never meet. Two projects with one name, definitively, so the
//     row is refused and the operator is told to pin the one this node means.
//     Merging them would fold two teams' work under one name on the strength of
//     both having typed the same word.
//   - the row here was pinned by the operator. A pin is out-of-band and
//     authoritative - the standing a pinned peer key has - so nothing off the
//     wire overwrites it, including a row that would otherwise be judged the
//     same project.
func checkProject(ctx context.Context, tx *sql.Tx, p *Principal, in *Project) (string, error) {
	if in.ID == "" {
		return "a project row with no name", nil
	}
	if p != nil {
		reachable, err := projectReachable(ctx, tx, p, in.ID)
		if err != nil {
			return "", err
		}
		if !reachable {
			return "project " + in.ID + " is one you are not in and have no grant with", nil
		}
	}
	here, err := projectHere(ctx, tx, in.ID)
	if err != nil || here == nil {
		// Nothing here by that name: trusted on first sight, which is what this
		// node does with a peer key it has never seen. The origin it arrives
		// with is what a later collision is measured against.
		return "", err
	}
	if same, why := SameProject(here, in); !same {
		return why + " - refused rather than merged; if this node means the incoming " +
			"one, pin it: flowy projects pin --project " + in.ID + " --origin <remote>", nil
	}
	if here.Provenance == ProvenancePinned && here.Origin != in.Origin {
		return "project " + in.ID + " is pinned here to " + here.Origin +
			" and the incoming row says " + in.Origin, nil
	}
	return "", nil
}

// projectReachable reports whether p may be shown this project name at all -
// their own project, or one on the other end of a live grant edge.
//
// The filter is evaluated against the name rather than against a row, because
// the row may not be here yet: VALUES is what gives ProjectFilterSQL a `p` to
// be applied to without a second copy of the rule in Go.
func projectReachable(ctx context.Context, tx *sql.Tx, p *Principal, id string) (bool, error) {
	a := &args{}
	idArg := a.next(id)
	filter := ProjectFilterSQL(p, "p", a, false)
	var ok bool
	if err := tx.QueryRowContext(ctx,
		`SELECT coalesce((SELECT `+filter+` FROM (VALUES (`+idArg+`)) AS p (id)), false)`,
		a.vals...).Scan(&ok); err != nil {
		return false, fmt.Errorf("store: sync merge project %s: %w", id, err)
	}
	return ok, nil
}

// grantReaches is GrantFilterSQL's rule in Go, asked of a row in hand: does
// this capability touch the principal carrying it - their project on either
// side of the edge, themselves as the subject, or themselves as the grantor?
func grantReaches(p *Principal, g *Grant) bool {
	if p == nil {
		return false
	}
	return g.ToProject != "" && g.ToProject == p.Project ||
		g.FromProject != "" && g.FromProject == p.Project ||
		g.Subject != "" && g.Subject == p.UserID ||
		g.GrantedBy != "" && g.GrantedBy == p.UserID
}

// named renders a grantor for a refusal message, so an unsigned row reads as
// one rather than as an empty gap in the sentence.
func named(user string) string {
	if user == "" {
		return "nobody"
	}
	return user
}

// principalOfProject reports whether user is somebody on this node who holds a
// principal in project: a token of their own that sits there, or an agent of
// theirs that does. It is "could this person have written that grant here",
// asked of a name that arrived over the wire.
//
// The user has to exist locally as well. A peer that invents both a grantor and
// the token behind it cannot, but a name that resolves to nothing here is a
// grant this node has no way to account for, and the point of the check is that
// the grantor is somebody this node already knows.
// projectIsHosted reports whether anybody at all holds a principal in project
// on this node: a token that sits there, or an agent that does.
//
// It is what decides whether a project-wide grant is this node's business. A
// grant opening a project somebody here is in reaches work this node holds, and
// its grantor is asked to be one of those principals; a grant opening a project
// nobody here is in opens nothing here, and is a capability between other
// people's projects passing through on a page.
func projectIsHosted(ctx context.Context, tx *sql.Tx, project string) (bool, error) {
	if project == "" {
		return false, nil
	}
	var hosted bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM tokens t LEFT JOIN agents a ON a.id = t.agent_id
		                 WHERE coalesce(nullif(t.project, ''), a.project, '') = $1)
		     OR EXISTS (SELECT 1 FROM agents ag WHERE coalesce(ag.project, '') = $1)`,
		project).Scan(&hosted)
	if err != nil {
		return false, fmt.Errorf("store: sync check project %s: %w", project, err)
	}
	return hosted, nil
}

func principalOfProject(ctx context.Context, tx *sql.Tx, user, project string) (bool, error) {
	if user == "" || project == "" {
		return false, nil
	}
	var ok bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM users u WHERE u.id = $1)
		    AND (EXISTS (SELECT 1 FROM tokens t LEFT JOIN agents a ON a.id = t.agent_id
		                  WHERE coalesce(nullif(t.user_id, ''), a.user_id, '') = $1
		                    AND coalesce(nullif(t.project, ''), a.project, '') = $2)
		      OR EXISTS (SELECT 1 FROM agents ag
		                  WHERE ag.user_id = $1 AND coalesce(ag.project, '') = $2))`,
		user, project).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: sync check grantor %s: %w", user, err)
	}
	return ok, nil
}

// observe advances this node's clock past a reading seen on another node, so
// the next local write is newer than everything replication has brought in.
//
// A reading that is not believable never gets here - checkReadings refuses the
// delta first - and the belt to that brace is in Pack, which clamps rather than
// letting a wall reading shift into the sign bit.
func (d *DB) observe(packed int64, node string) {
	if packed <= 0 || !hlc.Believable(packed) {
		return
	}
	if node == "" {
		node = "peer"
	}
	d.clock.UpdatePacked(packed, node)
}

// nullTime is what a zero timestamp is sent as, so the receiving row keeps the
// server default rather than the year zero.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// applyArtifact is last-writer-wins by hlc. The search vector is rebuilt here
// rather than shipped, because it is an artefact of this node's text search
// configuration and not a fact about the artifact.
//
// The forge link travels with the row: a bug filed as an issue on one node
// arrives on its peers already carrying the issue it was filed as, cursors and
// all, so neither node files it twice and neither pushes the same reply out
// twice.
func applyArtifact(ctx context.Context, tx *sql.Tx, a *Artifact) (int, error) {
	if a.ID == "" {
		return 0, errors.New("store: sync apply: artifact with no id")
	}
	var fields any
	if len(a.Fields) > 0 {
		fields = []byte(a.Fields)
	}
	var external any
	if a.External != nil {
		encoded, err := json.Marshal(a.External)
		if err != nil {
			return 0, fmt.Errorf("store: sync apply artifact %s: %w", a.ID, err)
		}
		external = encoded
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO artifacts (id, type, kind, project, owner_user, title, body, discovery,
		                        status, severity, tags, user_tags, related, visibility,
		                        file_path, fields, hlc, node, tombstone, search, created, updated,
		                        reported, external, sig, author_sig, authorship, sig_form)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, coalesce($21::timestamptz, now()), now(),
		         $22, $23, $24, $25, $26, $27)
		 ON CONFLICT (id) DO UPDATE SET
		     type = excluded.type, kind = excluded.kind, project = excluded.project,
		     owner_user = excluded.owner_user, title = excluded.title, body = excluded.body,
		     discovery = excluded.discovery, status = excluded.status, severity = excluded.severity,
		     tags = excluded.tags, user_tags = excluded.user_tags, related = excluded.related,
		     visibility = excluded.visibility, file_path = excluded.file_path,
		     fields = excluded.fields, hlc = excluded.hlc, node = excluded.node,
		     tombstone = excluded.tombstone, search = excluded.search, updated = now(),
		     reported = excluded.reported, external = excluded.external, sig = excluded.sig,
		     -- The author's signature travels with the row and this node's own
		     -- finding about it lands beside it: what is written here is what the
		     -- verify step decided, never what the payload claimed.
		     author_sig = excluded.author_sig, authorship = excluded.authorship,
		     -- And which form that signature was made over, kept with it: the
		     -- two are one fact, and a row stored under this node's idea of the
		     -- form while carrying a peer's signature is a row nothing
		     -- downstream could verify.
		     sig_form = excluded.sig_form,
		     -- created is inside the signature now, so the stored date has to be
		     -- the one the sig column beside it was made over: a row kept at this
		     -- node's own date, under the author's signature over theirs, is a row
		     -- no peer downstream of here could verify. A row that arrives without
		     -- one keeps whatever is here.
		     created = coalesce(excluded.created, artifacts.created)
		  WHERE coalesce(artifacts.hlc, 0) < excluded.hlc
		     OR (coalesce(artifacts.hlc, 0) = excluded.hlc
		         AND coalesce(artifacts.node, '') < excluded.node)`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a),
		nullTime(a.Created), a.Reported, external, a.Sig, a.AuthorSig, authorshipOr(a.Authorship),
		a.SigForm)
	if err != nil {
		return 0, fmt.Errorf("store: sync apply artifact %s: %w", a.ID, err)
	}
	return rowsAffected(res), nil
}

// applyEvent inserts an event the node has not seen. The log is append-only, so
// there is no update branch at all: a duplicate id is a row that is already
// correct, and the DAG in parents is carried as it was written.
func applyEvent(ctx context.Context, tx *sql.Tx, e *Event) (int, error) {
	if e.ID == "" {
		return 0, errors.New("store: sync apply: event with no id")
	}
	var meta any
	if len(e.Meta) > 0 {
		meta = []byte(e.Meta)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO events (id, type, project, room, thread, parents, actor, artifact,
		                     seq_hlc, node, body, meta, addressee, sig, author_sig, authorship,
		                     created)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, nullif($13, ''), $14, $15,
		         $16, coalesce($17::timestamptz, now()))
		 ON CONFLICT (id) DO NOTHING`,
		e.ID, e.Type, e.Project, e.Room, e.Thread, pq.Array(e.Parents), e.Actor,
		e.Artifact, e.SeqHLC, e.Node, e.Body, meta, e.Addressee, e.Sig, e.AuthorSig,
		// This node's own finding about the row's authorship, put there by the
		// verify step above - never the value that arrived in the payload. See
		// syncApply, where it is set, and authorshipOf, which decides it.
		authorshipOr(e.Authorship), nullTime(e.Created))
	if err != nil {
		return 0, fmt.Errorf("store: sync apply event %s: %w", e.ID, err)
	}
	return rowsAffected(res), nil
}

// applyTask is last-writer-wins by hlc, like an artifact: a task is a row that
// moves between three states and the newest move is the one that stands.
func applyTask(ctx context.Context, tx *sql.Tx, t *Task) (int, error) {
	if t.ID == "" {
		return 0, errors.New("store: sync apply: task with no id")
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO tasks (id, artifact, from_user, to_user, project, state,
		                    assignee_agent, thread, hlc, node, sig)
		 VALUES ($1, $2, $3, $4, nullif($5, ''), $6, nullif($7, ''), $8, $9, $10, $11)
		 ON CONFLICT (id) DO UPDATE SET
		     artifact = excluded.artifact, from_user = excluded.from_user,
		     to_user = excluded.to_user, project = excluded.project, state = excluded.state,
		     assignee_agent = excluded.assignee_agent, thread = excluded.thread,
		     hlc = excluded.hlc, node = excluded.node, sig = excluded.sig
		  WHERE coalesce(tasks.hlc, 0) < excluded.hlc
		     OR (coalesce(tasks.hlc, 0) = excluded.hlc
		         AND coalesce(tasks.node, '') < excluded.node)`,
		t.ID, t.Artifact, t.FromUser, t.ToUser, t.Project, t.State,
		t.AssigneeAgent, t.Thread, t.HLC, t.Node, t.Sig)
	if err != nil {
		return 0, fmt.Errorf("store: sync apply task %s: %w", t.ID, err)
	}
	return rowsAffected(res), nil
}

// applyGrant is last-writer-wins by hlc. A revocation is a tombstoned grant
// with a later reading, so revoking travels the same way granting does.
func applyGrant(ctx context.Context, tx *sql.Tx, g *Grant) (int, error) {
	if g.ID == "" {
		return 0, errors.New("store: sync apply: grant with no id")
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO grants (id, from_project, to_project, subject, artifact, cap,
		                     granted_by, hlc, node, tombstone, sig)
		 VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, nullif($7, ''), $8, $9, $10, $11)
		 ON CONFLICT (id) DO UPDATE SET
		     from_project = excluded.from_project, to_project = excluded.to_project,
		     subject = excluded.subject, artifact = excluded.artifact, cap = excluded.cap,
		     granted_by = excluded.granted_by, hlc = excluded.hlc, node = excluded.node,
		     tombstone = excluded.tombstone, sig = excluded.sig
		  WHERE coalesce(grants.hlc, 0) < excluded.hlc
		     OR (coalesce(grants.hlc, 0) = excluded.hlc
		         AND coalesce(grants.node, '') < excluded.node)`,
		g.ID, g.FromProject, g.ToProject, g.Subject, g.Artifact, g.Cap,
		g.GrantedBy, g.HLC, g.Node, g.Tombstone, g.Sig)
	if err != nil {
		return 0, fmt.Errorf("store: sync apply grant %s: %w", g.ID, err)
	}
	return rowsAffected(res), nil
}

// rowsAffected reads a result's row count, treating a driver that will not say
// as one row: the count is a report, not a decision, and guessing low would
// make an applied row look ignored.
func rowsAffected(res sql.Result) int {
	n, err := res.RowsAffected()
	if err != nil {
		return 1
	}
	return int(n)
}

// affectedRows reads a result's row count where the count is a decision rather
// than a report - "did that update find the row" - and hands the driver's
// refusal to say back to the caller. Swallowing it there is how an update that
// touched nothing gets reported as an update that worked.
func affectedRows(res sql.Result) (int64, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("the driver would not count the rows it changed: %w", err)
	}
	return n, nil
}

// SeedClock lifts this node's clock above the highest reading its store already
// holds. It is called at startup, so a node that applied a peer's rows and was
// then restarted cannot mint a reading that loses to a row it is already
// holding - the clock lives in memory, the rows do not.
func (d *DB) SeedClock(ctx context.Context) (int64, error) {
	var highest sql.NullInt64
	err := d.sql.QueryRowContext(ctx,
		`SELECT max(h) FROM (
		    SELECT max(hlc) AS h FROM artifacts
		    UNION ALL SELECT max(seq_hlc) FROM events
		    UNION ALL SELECT max(hlc) FROM tasks
		    UNION ALL SELECT max(hlc) FROM grants
		    UNION ALL SELECT max(hlc) FROM users
		 ) readings`).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("store: seed clock: %w", err)
	}
	if !highest.Valid || highest.Int64 <= 0 {
		return 0, nil
	}
	return d.clock.UpdatePacked(highest.Int64, d.node), nil
}

// Peer is a replication bookmark: one row per peer this node syncs with.
//
// PullCursor is the greatest reading this node has pulled from the peer and
// applied; PushedCursor is the greatest reading it has handed the peer. Both
// only ever move forward, so a sync that dies halfway resumes rather than
// starts again, and a peer that was offline for a week is simply a peer with an
// old cursor.
type Peer struct {
	Peer         string    `json:"peer"`
	PullCursor   int64     `json:"pull_cursor"`
	PushedCursor int64     `json:"pushed_cursor"`
	LastSeen     time.Time `json:"last_seen,omitempty"`
}

// RegisterPeer records a peer without disturbing the cursors it already has.
func (d *DB) RegisterPeer(ctx context.Context, peer string) error {
	if peer == "" {
		return errors.New("store: register peer: empty peer")
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO peers (peer, pull_cursor, pushed_cursor) VALUES ($1, 0, 0)
		 ON CONFLICT (peer) DO NOTHING`, peer)
	if err != nil {
		return fmt.Errorf("store: register peer %s: %w", peer, err)
	}
	return nil
}

// GetPeer reads a peer's bookmarks. A peer that has never been registered comes
// back as ErrNotFound.
func (d *DB) GetPeer(ctx context.Context, peer string) (*Peer, error) {
	var (
		p        Peer
		pull     sql.NullInt64
		pushed   sql.NullInt64
		lastSeen sql.NullTime
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT peer, pull_cursor, pushed_cursor, last_seen FROM peers WHERE peer = $1`, peer).
		Scan(&p.Peer, &pull, &pushed, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get peer %s: %w", peer, err)
	}
	p.PullCursor, p.PushedCursor, p.LastSeen = pull.Int64, pushed.Int64, lastSeen.Time
	return &p, nil
}

// AdvancePullCursor moves the pull bookmark forward. It never moves back: a
// cursor that went backwards would replay rows the node has already merged, and
// a cursor that went backwards on a losing merge would replay them forever.
func (d *DB) AdvancePullCursor(ctx context.Context, peer string, to int64) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE peers SET pull_cursor = $2, last_seen = now()
		  WHERE peer = $1 AND coalesce(pull_cursor, 0) < $2`, peer, to)
	if err != nil {
		return fmt.Errorf("store: advance pull cursor for %s: %w", peer, err)
	}
	return nil
}

// AdvancePushedCursor moves the push bookmark forward, and never back.
func (d *DB) AdvancePushedCursor(ctx context.Context, peer string, to int64) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE peers SET pushed_cursor = $2, last_seen = now()
		  WHERE peer = $1 AND coalesce(pushed_cursor, 0) < $2`, peer, to)
	if err != nil {
		return fmt.Errorf("store: advance pushed cursor for %s: %w", peer, err)
	}
	return nil
}

// TouchPeer records that the node reached the peer just now, whether or not
// anything moved.
func (d *DB) TouchPeer(ctx context.Context, peer string) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE peers SET last_seen = now() WHERE peer = $1`, peer)
	if err != nil {
		return fmt.Errorf("store: touch peer %s: %w", peer, err)
	}
	return nil
}

// ListPeers reads every peer this node knows about, oldest bookmark first.
func (d *DB) ListPeers(ctx context.Context) ([]*Peer, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT peer, pull_cursor, pushed_cursor, last_seen FROM peers ORDER BY peer`)
	if err != nil {
		return nil, fmt.Errorf("store: list peers: %w", err)
	}
	defer rows.Close()

	out := []*Peer{}
	for rows.Next() {
		var (
			p            Peer
			pull, pushed sql.NullInt64
			lastSeen     sql.NullTime
		)
		if err := rows.Scan(&p.Peer, &pull, &pushed, &lastSeen); err != nil {
			return nil, fmt.Errorf("store: list peers: %w", err)
		}
		p.PullCursor, p.PushedCursor, p.LastSeen = pull.Int64, pushed.Int64, lastSeen.Time
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list peers: %w", err)
	}
	return out, nil
}
