// Package store is the node's Postgres-wire persistence layer.
//
// It speaks stock Postgres over the wire and nothing more: the deployment
// target is a SereneDB node, so the SQL here avoids anything that is a property
// of Postgres the storage engine rather than Postgres the protocol.
//
// Rows are stamped on the way in - a ULID for the primary key when the caller
// left it empty, and a packed hybrid logical clock plus the node name for the
// hlc/seq_hlc columns - so a later phase can merge two nodes' writes.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/hlc"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// DB is a handle on the node's database plus the clocks that stamp its rows.
type DB struct {
	sql   *sql.DB
	clock *hlc.Clock
	node  string
}

// Open dials dsn and verifies the connection. node names this node in every row
// it writes.
func Open(ctx context.Context, dsn, node string) (*DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("store: empty DSN (set DATABASE_URL)")
	}
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &DB{sql: sqlDB, clock: hlc.New(node), node: node}, nil
}

// Close releases the pool.
func (d *DB) Close() error { return d.sql.Close() }

// SQL exposes the underlying pool for callers that need raw access.
func (d *DB) SQL() *sql.DB { return d.sql }

// Node returns the node name stamped onto rows.
func (d *DB) Node() string { return d.node }

// Clock returns the node's hybrid logical clock.
func (d *DB) Clock() *hlc.Clock { return d.clock }

// Ping reports whether the database is reachable.
func (d *DB) Ping(ctx context.Context) error { return d.sql.PingContext(ctx) }

// execer is what both the pool and a transaction satisfy, so one statement
// serves a write made on its own and the same write made as part of a sequence
// that has to land whole.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn in one transaction and commits it, rolling back on any error.
//
// It is what the multi-row operations are written against. An assignment is
// three rows - the share, the task and the message that opens its thread - and
// a status move is two, and until they were one transaction a node that failed
// halfway left the half behind: a share nothing points at, a task about an
// artifact the assignee cannot read, a status the trail has no entry for.
// Nothing backfills that. Worse, the half replicates on its own, because the
// rows carry their own readings and a peer merges whatever is there.
func (d *DB) inTx(ctx context.Context, what string, fn func(tx *sql.Tx) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only when Commit did not happen
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	return nil
}

// stamp fills in an id, an hlc reading and the node name where the caller left
// them empty.
func (d *DB) stamp(id *string, clockVal *int64, node *string) {
	if *id == "" {
		*id = ulid.NewString()
	}
	if *clockVal == 0 {
		*clockVal = d.clock.Pack()
	}
	if *node == "" {
		*node = d.node
	}
}

// User is a person on this node.
type User struct {
	ID           string `json:"id"`
	Handle       string `json:"handle"`
	Display      string `json:"display"`
	AutoDelegate bool   `json:"auto_delegate"`
	HLC          int64  `json:"hlc"`
	Node         string `json:"node"`
}

// InsertUser writes a user, stamping id/hlc/node when unset.
func (d *DB) InsertUser(ctx context.Context, u *User) error {
	d.stamp(&u.ID, &u.HLC, &u.Node)
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO users (id, handle, display, auto_delegate, hlc, node)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		u.ID, u.Handle, u.Display, u.AutoDelegate, u.HLC, u.Node)
	if err != nil {
		return fmt.Errorf("store: insert user: %w", err)
	}
	return nil
}

// GetUser reads a user by id.
func (d *DB) GetUser(ctx context.Context, id string) (*User, error) {
	var (
		u                        User
		handle, display, nodeCol sql.NullString
		auto                     sql.NullBool
		clockVal                 sql.NullInt64
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT id, handle, display, auto_delegate, hlc, node FROM users WHERE id = $1`, id).
		Scan(&u.ID, &handle, &display, &auto, &clockVal, &nodeCol)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user %s: %w", id, err)
	}
	u.Handle, u.Display, u.Node = handle.String, display.String, nodeCol.String
	u.AutoDelegate, u.HLC = auto.Bool, clockVal.Int64
	return &u, nil
}

// Agent is a coding agent acting for a user. Kind is claude|glm|opencode.
type Agent struct {
	ID      string `json:"id"`
	UserID  string `json:"user_id"`
	Kind    string `json:"kind"`
	Project string `json:"project"`
	HLC     int64  `json:"hlc"`
	Node    string `json:"node"`
}

// InsertAgent writes an agent, stamping id/hlc/node when unset.
func (d *DB) InsertAgent(ctx context.Context, a *Agent) error {
	d.stamp(&a.ID, &a.HLC, &a.Node)
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO agents (id, user_id, kind, project, hlc, node)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		a.ID, a.UserID, a.Kind, a.Project, a.HLC, a.Node)
	if err != nil {
		return fmt.Errorf("store: insert agent: %w", err)
	}
	return nil
}

// GetAgent reads an agent by id.
func (d *DB) GetAgent(ctx context.Context, id string) (*Agent, error) {
	var (
		a                              Agent
		userID, kind, project, nodeCol sql.NullString
		clockVal                       sql.NullInt64
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT id, user_id, kind, project, hlc, node FROM agents WHERE id = $1`, id).
		Scan(&a.ID, &userID, &kind, &project, &clockVal, &nodeCol)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get agent %s: %w", id, err)
	}
	a.UserID, a.Kind, a.Project, a.Node = userID.String, kind.String, project.String, nodeCol.String
	a.HLC = clockVal.Int64
	return &a, nil
}

// Artifact is anything the node holds that is worth naming: a transcript, a
// memory, a bug, a note. A nil Project means the artifact is personal to
// OwnerUser.
//
// Kind narrows Type without multiplying it: a memory item is an artifact of
// type 'memory' whose kind is note|todo|feature|handoff, so one table, one
// permission filter and one search index serve all of them.
type Artifact struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Kind       string          `json:"kind,omitempty"`
	Project    *string         `json:"project"`
	OwnerUser  string          `json:"owner_user"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Discovery  string          `json:"discovery"`
	Status     string          `json:"status"`
	Severity   string          `json:"severity"`
	Tags       []string        `json:"tags"`
	UserTags   []string        `json:"user_tags"`
	Related    []string        `json:"related"`
	Visibility string          `json:"visibility"`
	FilePath   string          `json:"file_path"`
	Fields     json.RawMessage `json:"fields,omitempty"`
	// Reported and External are the forge link: whether this artifact has been
	// filed as an issue somewhere, and where. Both are written only by the
	// forge endpoints - see SetArtifactExternal - so an ordinary update of the
	// artifact carries them forward untouched.
	Reported  bool         `json:"reported"`
	External  *ExternalRef `json:"external,omitempty"`
	HLC       int64        `json:"hlc"`
	Node      string       `json:"node"`
	Tombstone bool         `json:"tombstone"`
	Created   time.Time    `json:"created"`
	Updated   time.Time    `json:"updated"`
}

// InsertArtifact writes an artifact, stamping id/hlc/node when unset.
func (d *DB) InsertArtifact(ctx context.Context, a *Artifact) error {
	d.stamp(&a.ID, &a.HLC, &a.Node)
	if a.Visibility == "" {
		a.Visibility = "project"
	}
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
		 RETURNING created, updated`,
		a.ID, a.Type, a.Kind, a.Project, a.OwnerUser, a.Title, a.Body, a.Discovery,
		a.Status, a.Severity, pq.Array(a.Tags), pq.Array(a.UserTags), pq.Array(a.Related),
		a.Visibility, a.FilePath, fields, a.HLC, a.Node, a.Tombstone, searchText(a)).
		Scan(&a.Created, &a.Updated)
	if err != nil {
		return fmt.Errorf("store: insert artifact: %w", err)
	}
	return nil
}

// GetArtifact reads an artifact by id, without asking who wants it. Handlers
// use ReadArtifact instead; this is for the paths that have already decided,
// and for the tests.
func (d *DB) GetArtifact(ctx context.Context, id string) (*Artifact, error) {
	a, err := scanArtifact(d.sql.QueryRowContext(ctx,
		`SELECT `+artifactColumns+` FROM artifacts ar WHERE ar.id = $1`, id), nil)
	if err != nil {
		return nil, fmt.Errorf("store: get artifact %s: %w", id, err)
	}
	return a, nil
}

// Event is one entry in the append-only log. Parents carries the thread DAG:
// empty starts a thread, one continues it, several merge branches.
type Event struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Project  *string         `json:"project"`
	Room     string          `json:"room"`
	Thread   string          `json:"thread"`
	Parents  []string        `json:"parents"`
	Actor    string          `json:"actor"`
	Artifact string          `json:"artifact"`
	SeqHLC   int64           `json:"seq_hlc"`
	Node     string          `json:"node"`
	Body     string          `json:"body"`
	Meta     json.RawMessage `json:"meta,omitempty"`
	Created  time.Time       `json:"created"`
}

// AppendEvent writes an event, stamping id/seq_hlc/node when unset.
func (d *DB) AppendEvent(ctx context.Context, e *Event) error {
	return d.appendEvent(ctx, d.sql, e)
}

// appendEvent is AppendEvent against whatever is in hand: the pool for a
// message on its own, a transaction for one that is part of an operation.
func (d *DB) appendEvent(ctx context.Context, q execer, e *Event) error {
	d.stamp(&e.ID, &e.SeqHLC, &e.Node)
	if e.Thread == "" {
		// A thread with no explicit head is named after its first event.
		e.Thread = e.ID
	}
	var meta any
	if len(e.Meta) > 0 {
		meta = []byte(e.Meta)
	}
	err := q.QueryRowContext(ctx,
		`INSERT INTO events (id, type, project, room, thread, parents, actor, artifact,
		                     seq_hlc, node, body, meta)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 RETURNING created`,
		e.ID, e.Type, e.Project, e.Room, e.Thread, pq.Array(e.Parents), e.Actor,
		e.Artifact, e.SeqHLC, e.Node, e.Body, meta).
		Scan(&e.Created)
	if err != nil {
		return fmt.Errorf("store: append event: %w", err)
	}
	return nil
}

// GetEvent reads an event by id, without asking who wants it.
func (d *DB) GetEvent(ctx context.Context, id string) (*Event, error) {
	e, err := scanEvent(d.sql.QueryRowContext(ctx,
		`SELECT `+eventColumns+` FROM events e WHERE e.id = $1`, id))
	if err != nil {
		return nil, fmt.Errorf("store: get event %s: %w", id, err)
	}
	return e, nil
}

// ThreadEvents reads one thread in log order.
//
// One statement, like ListEvents: it used to read the thread's ids and then
// fetch each event by id, which is a query per message. The console's thread
// pane and the forge's reviewer loop both walk whole threads, so that was the
// length of the conversation in round trips every time either of them ran, and
// the rows in between could move under it.
//
// It asks who wants it no more than GetEvent does - the callers that need the
// filter use ListEvents with a thread.
func (d *DB) ThreadEvents(ctx context.Context, thread string) ([]*Event, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events e WHERE e.thread = $1 ORDER BY e.seq_hlc, e.id`,
		thread)
	if err != nil {
		return nil, fmt.Errorf("store: thread %s: %w", thread, err)
	}
	defer rows.Close()

	out := []*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: thread %s: %w", thread, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: thread %s: %w", thread, err)
	}
	return out, nil
}

// Counts returns the row count of each spine table, which is what /healthz
// reports when it is asked for detail.
func (d *DB) Counts(ctx context.Context) (map[string]int64, error) {
	tables := []string{"users", "agents", "tokens", "grants", "artifacts", "events", "tasks", "peers"}
	out := make(map[string]int64, len(tables))
	for _, t := range tables {
		var n int64
		// t comes from the fixed list above, never from a caller.
		if err := d.sql.QueryRowContext(ctx, `SELECT count(*) FROM `+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("store: count %s: %w", t, err)
		}
		out[t] = n
	}
	return out, nil
}
