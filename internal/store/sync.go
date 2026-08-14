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
)

// Replication, the store half.
//
// A node syncs with a peer by reading the peer's rows since a cursor and
// applying them locally, and by handing the peer its own rows since a second
// cursor. Both halves go through the same two pieces of code: SyncPull, which
// is a permission-filtered read of every replicated table, and SyncApply, which
// is the merge.
//
// The merge rules, which are the whole of the consistency story:
//
//   - events are append-only, so an event that is already here is already
//     right: insert when the id is new, ignore it otherwise. Nothing about an
//     event is ever updated, including by replication.
//   - artifacts, tasks and grants are last-writer-wins by hlc. An incoming row
//     replaces the local one only when its hlc is strictly greater, so the two
//     nodes pick the same winner whichever order the rows arrive in and however
//     many times they arrive. A tombstone is a column on the row rather than an
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
	HWM       int64       `json:"hwm"`
}

// Len is the number of rows in the set.
func (s *SyncSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Artifacts) + len(s.Events) + len(s.Tasks) + len(s.Grants)
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
	project := a.next(p.Project)
	return `(` + alias + `.to_project = ` + project + ` AND ` + project + ` <> ''
	      OR ` + alias + `.from_project = ` + project + ` AND ` + project + ` <> ''
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
	if p == nil {
		return nil, errors.New("store: sync pull without a principal")
	}
	limit := q.limit()
	set := &SyncSet{Artifacts: []*Artifact{}, Events: []*Event{}, Tasks: []*Task{}, Grants: []Grant{}}

	// hwm tracks the greatest reading in the set; capped tracks the smallest
	// greatest reading among the tables that filled their page.
	var hwm int64
	capped := int64(0)
	note := func(last int64, n int) {
		if last > hwm {
			hwm = last
		}
		if n == limit && last > 0 && (capped == 0 || last < capped) {
			capped = last
		}
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
	newly, err := d.syncNewlyVisible(ctx, p, q.Since, limit, grants)
	if err != nil {
		return nil, err
	}
	set.Artifacts = append(set.Artifacts, newly...)

	set.HWM = hwm
	if capped > 0 && capped < hwm {
		set.HWM = capped
	}
	return set, nil
}

func (d *DB) syncArtifacts(ctx context.Context, p *Principal, since int64, limit int) ([]*Artifact, error) {
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	query := `SELECT ` + artifactColumns + `
	            FROM artifacts ar
	           WHERE ar.hlc > ` + a.next(since) + `
	             AND ` + filter + `
	           ORDER BY ar.hlc, ar.id
	           LIMIT ` + a.next(limit)

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: sync artifacts: %w", err)
	}
	defer rows.Close()

	out := []*Artifact{}
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, fmt.Errorf("store: sync artifacts: %w", err)
		}
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sync artifacts: %w", err)
	}
	return out, nil
}

// syncNewlyVisible reads the artifacts below the cursor that the grants in this
// page have just opened up to p: the ones a fresh share names, and the ones in
// a project a fresh project-wide grant reaches into.
//
// Only grants that widen p's own view count. A share of somebody else's to
// somebody else changes nothing about what p may read, and re-scanning for it
// would be a page of rows p is about to be refused anyway.
func (d *DB) syncNewlyVisible(
	ctx context.Context, p *Principal, since int64, limit int, grants []Grant,
) ([]*Artifact, error) {
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
		return nil, nil
	}

	a := &args{}
	sinceArg := a.next(since)
	reach := []string{}
	if len(shared) > 0 {
		reach = append(reach, "ar.id IN ("+placeholders(a, shared)+")")
	}
	if len(opened) > 0 {
		reach = append(reach, "ar.project IN ("+placeholders(a, opened)+")")
	}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	query := `SELECT ` + artifactColumns + `
	            FROM artifacts ar
	           WHERE ar.hlc <= ` + sinceArg + `
	             AND (` + strings.Join(reach, " OR ") + `)
	             AND ` + filter + `
	           ORDER BY ar.hlc, ar.id
	           LIMIT ` + a.next(limit)

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: sync newly visible artifacts: %w", err)
	}
	defer rows.Close()

	out := []*Artifact{}
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, fmt.Errorf("store: sync newly visible artifacts: %w", err)
		}
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sync newly visible artifacts: %w", err)
	}
	return out, nil
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
	a := &args{}
	filter := EventFilterSQL(p, "e", a, false)
	query := `SELECT ` + eventColumns + `
	            FROM events e
	           WHERE e.seq_hlc > ` + a.next(since) + `
	             AND ` + filter + `
	           ORDER BY e.seq_hlc, e.id
	           LIMIT ` + a.next(limit)

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: sync events: %w", err)
	}
	defer rows.Close()

	out := []*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: sync events: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sync events: %w", err)
	}
	return out, nil
}

func (d *DB) syncTasks(ctx context.Context, p *Principal, since int64, limit int) ([]*Task, error) {
	a := &args{}
	where := taskPartySQL(p, "t", a)
	query := `SELECT ` + taskColumns + `
	            FROM tasks t
	           WHERE t.hlc > ` + a.next(since) + `
	             AND ` + where + `
	           ORDER BY t.hlc, t.id
	           LIMIT ` + a.next(limit)

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: sync tasks: %w", err)
	}
	defer rows.Close()

	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows, false)
		if err != nil {
			return nil, fmt.Errorf("store: sync tasks: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sync tasks: %w", err)
	}
	return out, nil
}

func (d *DB) syncGrants(ctx context.Context, p *Principal, since int64, limit int) ([]Grant, error) {
	a := &args{}
	filter := GrantFilterSQL(p, "g", a)
	query := `SELECT g.id, g.from_project, g.to_project, coalesce(g.subject, ''),
	                 coalesce(g.artifact, ''), coalesce(g.cap, 'read'), coalesce(g.granted_by, ''),
	                 coalesce(g.hlc, 0), coalesce(g.node, ''), coalesce(g.tombstone, false)
	            FROM grants g
	           WHERE coalesce(g.hlc, 0) > ` + a.next(since) + `
	             AND ` + filter + `
	           ORDER BY g.hlc, g.id
	           LIMIT ` + a.next(limit)

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: sync grants: %w", err)
	}
	defer rows.Close()

	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.ID, &g.FromProject, &g.ToProject, &g.Subject, &g.Artifact,
			&g.Cap, &g.GrantedBy, &g.HLC, &g.Node, &g.Tombstone); err != nil {
			return nil, fmt.Errorf("store: sync grants: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sync grants: %w", err)
	}
	return out, nil
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

// maxReasons caps what a refusal reports, so a page of bad rows answers with a
// readable message rather than a page of its own.
const maxReasons = 8

// ErrBadReading is what a delta carrying a clock reading no clock could have
// made comes back as. It is refused whole: a single poisoned reading, merged,
// lifts this node's clock past everything it will ever write again.
var ErrBadReading = errors.New("store: clock reading is not believable")

// SyncApply merges a delta this node went and fetched into it, and reports how
// many rows of each table it actually changed. A row that lost its merge - an
// older artifact, an event that is already here - is received and not applied,
// which is what makes a second push of the same set report zeros.
//
// The whole set lands in one transaction: a peer's page is either applied or
// not, so a driver that dies mid-page resumes from a cursor that still
// describes the database.
//
// It does not filter by principal, because there is none: this is the pull
// side, run by this node's own operator against a peer they named and hold a
// token for. What arrives unasked - POST /api/sync/push - goes through
// SyncApplyAs instead.
func (d *DB) SyncApply(ctx context.Context, in *SyncSet) (map[string]int, error) {
	res, err := d.syncApply(ctx, nil, in)
	if err != nil {
		return nil, err
	}
	return res.Applied, nil
}

// SyncApplyAs merges a delta somebody pushed at this node, as p, and refuses
// the rows p could not have written one at a time over the API.
//
// A peer authenticates as a principal, and the rows it hands over are rows that
// principal is claiming. Without this, a push was a way around every rule the
// rest of the node keeps: a grant into a project the pusher has no say over, a
// personal artifact belonging to somebody else, a rewrite of a row the pusher
// cannot even read. Merging is last-writer-wins, so a forged row with a high
// enough reading wins and stays won.
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

func (d *DB) syncApply(ctx context.Context, p *Principal, in *SyncSet) (*SyncResult, error) {
	res := &SyncResult{
		Applied: map[string]int{"artifacts": 0, "events": 0, "tasks": 0, "grants": 0},
		Refused: map[string]int{"artifacts": 0, "events": 0, "tasks": 0, "grants": 0},
	}
	if in == nil || in.Len() == 0 {
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

	refuse := func(table, why string) {
		res.Refused[table]++
		if len(res.Reasons) < maxReasons {
			res.Reasons = append(res.Reasons, why)
		}
	}

	for _, art := range in.Artifacts {
		if why, err := checkArtifact(ctx, tx, p, art); err != nil {
			return nil, err
		} else if why != "" {
			refuse("artifacts", why)
			continue
		}
		n, err := applyArtifact(ctx, tx, art)
		if err != nil {
			return nil, err
		}
		res.Applied["artifacts"] += n
		d.observe(art.HLC, art.Node)
	}
	for _, e := range in.Events {
		n, err := applyEvent(ctx, tx, e)
		if err != nil {
			return nil, err
		}
		res.Applied["events"] += n
		d.observe(e.SeqHLC, e.Node)
	}
	for _, t := range in.Tasks {
		if why, err := checkTask(ctx, tx, p, t); err != nil {
			return nil, err
		} else if why != "" {
			refuse("tasks", why)
			continue
		}
		n, err := applyTask(ctx, tx, t)
		if err != nil {
			return nil, err
		}
		res.Applied["tasks"] += n
		d.observe(t.HLC, t.Node)
	}
	for i := range in.Grants {
		if why, err := checkGrant(ctx, tx, p, &in.Grants[i]); err != nil {
			return nil, err
		} else if why != "" {
			refuse("grants", why)
			continue
		}
		n, err := applyGrant(ctx, tx, &in.Grants[i])
		if err != nil {
			return nil, err
		}
		res.Applied["grants"] += n
		d.observe(in.Grants[i].HLC, in.Grants[i].Node)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: sync apply: %w", err)
	}
	return res, nil
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
	for _, a := range in.Artifacts {
		if !hlc.BelievableAt(a.HLC, nowMS) {
			return bad("artifact", a.ID, a.HLC)
		}
	}
	for _, e := range in.Events {
		if !hlc.BelievableAt(e.SeqHLC, nowMS) {
			return bad("event", e.ID, e.SeqHLC)
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
	return nil
}

// checkArtifact answers why p may not push art, or "" when it may.
//
// Two rules, and they are the two ways a pushed artifact escalates:
//
//   - a personal row belongs to its owner. Nobody else pushes one, because
//     nobody else can read one, and a push of somebody else's would be a write
//     into a place no read of theirs can reach.
//   - a row that is already here is only overwritten by somebody who can read
//     the row that is here. An id is a guess anybody can make; without this,
//     guessing one is enough to take the row over, which is the same hole the
//     store's own upsert had.
//
// A row that is not here yet is allowed: that is ordinary replication, and a
// row invented in a project the pusher cannot read is a row the pusher still
// cannot read.
func checkArtifact(ctx context.Context, tx *sql.Tx, p *Principal, a *Artifact) (string, error) {
	if p == nil {
		return "", nil
	}
	if a.Visibility == "personal" || a.Project == nil {
		if a.OwnerUser == "" || a.OwnerUser != p.UserID {
			return "artifact " + a.ID + " is personal and not yours", nil
		}
	}
	args := &args{}
	idArg := args.next(a.ID)
	filter := ArtifactFilterSQL(p, "ar", args, false)
	var readable bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM artifacts ar WHERE ar.id = `+idArg+` AND `+filter+`)`,
		args.vals...).Scan(&readable)
	if err != nil {
		return "", fmt.Errorf("store: sync check artifact %s: %w", a.ID, err)
	}
	if readable {
		return "", nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM artifacts WHERE id = $1)`, a.ID).Scan(&exists); err != nil {
		return "", fmt.Errorf("store: sync check artifact %s: %w", a.ID, err)
	}
	if exists {
		return "artifact " + a.ID + " is already here and you cannot read it", nil
	}
	return "", nil
}

// checkTask answers why p may not push t. The rule is the one every read of a
// task uses: a task is between the two people named on it and the agent it was
// delegated to. A task that is already here is only rewritten by one of them,
// so a handoff cannot be reassigned by a stranger who guessed its id.
func checkTask(ctx context.Context, tx *sql.Tx, p *Principal, t *Task) (string, error) {
	if p == nil {
		return "", nil
	}
	args := &args{}
	idArg := args.next(t.ID)
	party := taskPartySQL(p, "t", args)
	var mine bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM tasks t WHERE t.id = `+idArg+` AND `+party+`)`,
		args.vals...).Scan(&mine)
	if err != nil {
		return "", fmt.Errorf("store: sync check task %s: %w", t.ID, err)
	}
	if mine {
		return "", nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM tasks WHERE id = $1)`, t.ID).Scan(&exists); err != nil {
		return "", fmt.Errorf("store: sync check task %s: %w", t.ID, err)
	}
	if exists {
		return "task " + t.ID + " is already here and you are not a party to it", nil
	}
	return "", nil
}

// checkGrant answers why p may not push g. It is the rule POST /api/grants
// enforces, applied to a row that arrived over the wire instead of in a
// request body:
//
//   - a project-wide grant opens a project up, so it has to come from a
//     principal of the project being opened. Without this, a peer writes
//     itself a grant from its own project into yours and reads it from then
//     on - and because merging is last-writer-wins, a big enough reading makes
//     the forgery permanent.
//   - a share is the owner's to give. The artifact has to be here, and its
//     owner has to be whoever the grant says granted it.
func checkGrant(ctx context.Context, tx *sql.Tx, p *Principal, g *Grant) (string, error) {
	if p == nil {
		return "", nil
	}
	if g.Artifact == "" {
		if g.ToProject == "" || g.ToProject != p.Project {
			return "only a principal of " + g.ToProject + " can open it up", nil
		}
		return "", nil
	}
	var (
		owner sql.NullString
		vis   sql.NullString
		proj  sql.NullString
	)
	err := tx.QueryRowContext(ctx,
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
		                        reported, external)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		         $19, `+fmt.Sprintf(artifactSearchSQL, 20)+`, coalesce($21::timestamptz, now()), now(),
		         $22, $23)
		 ON CONFLICT (id) DO UPDATE SET
		     type = excluded.type, kind = excluded.kind, project = excluded.project,
		     owner_user = excluded.owner_user, title = excluded.title, body = excluded.body,
		     discovery = excluded.discovery, status = excluded.status, severity = excluded.severity,
		     tags = excluded.tags, user_tags = excluded.user_tags, related = excluded.related,
		     visibility = excluded.visibility, file_path = excluded.file_path,
		     fields = excluded.fields, hlc = excluded.hlc, node = excluded.node,
		     tombstone = excluded.tombstone, search = excluded.search, updated = now(),
		     reported = excluded.reported, external = excluded.external
		  WHERE coalesce(artifacts.hlc, 0) < excluded.hlc`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a),
		nullTime(a.Created), a.Reported, external)
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
		                     seq_hlc, node, body, meta, created)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		         coalesce($13::timestamptz, now()))
		 ON CONFLICT (id) DO NOTHING`,
		e.ID, e.Type, e.Project, e.Room, e.Thread, pq.Array(e.Parents), e.Actor,
		e.Artifact, e.SeqHLC, e.Node, e.Body, meta, nullTime(e.Created))
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
		                    assignee_agent, thread, hlc, node)
		 VALUES ($1, $2, $3, $4, nullif($5, ''), $6, nullif($7, ''), $8, $9, $10)
		 ON CONFLICT (id) DO UPDATE SET
		     artifact = excluded.artifact, from_user = excluded.from_user,
		     to_user = excluded.to_user, project = excluded.project, state = excluded.state,
		     assignee_agent = excluded.assignee_agent, thread = excluded.thread,
		     hlc = excluded.hlc, node = excluded.node
		  WHERE coalesce(tasks.hlc, 0) < excluded.hlc`,
		t.ID, t.Artifact, t.FromUser, t.ToUser, t.Project, t.State,
		t.AssigneeAgent, t.Thread, t.HLC, t.Node)
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
		                     granted_by, hlc, node, tombstone)
		 VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, nullif($7, ''), $8, $9, $10)
		 ON CONFLICT (id) DO UPDATE SET
		     from_project = excluded.from_project, to_project = excluded.to_project,
		     subject = excluded.subject, artifact = excluded.artifact, cap = excluded.cap,
		     granted_by = excluded.granted_by, hlc = excluded.hlc, node = excluded.node,
		     tombstone = excluded.tombstone
		  WHERE coalesce(grants.hlc, 0) < excluded.hlc`,
		g.ID, g.FromProject, g.ToProject, g.Subject, g.Artifact, g.Cap,
		g.GrantedBy, g.HLC, g.Node, g.Tombstone)
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
