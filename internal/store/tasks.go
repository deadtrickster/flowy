package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Task is a handoff: one artifact, from one person to another, with a chat
// thread attached to it and a state that says where it got to.
//
// It is the whole of the assignment. The share that lets the assignee read the
// artifact is a grants row written in the same operation, and the conversation
// about it is the events whose thread is Thread - a task owns neither of them,
// it names them, so both keep the permission filter they already had.
//
// State is open|delegated|done:
//
//	open      - handed over, waiting for the person
//	delegated - handed on to that person's agent, which is what auto_delegate
//	            does at assignment time and what /delegate does later
//	done      - finished, by either side
type Task struct {
	ID            string `json:"id"`
	Artifact      string `json:"artifact"`
	FromUser      string `json:"from_user"`
	ToUser        string `json:"to_user"`
	Project       string `json:"project,omitempty"`
	State         string `json:"state"`
	AssigneeAgent string `json:"assignee_agent,omitempty"`
	Thread        string `json:"thread"`
	HLC           int64  `json:"hlc"`
	Node          string `json:"node"`

	// ArtifactTitle and ArtifactType are joined in by ListTasks so an inbox can
	// be drawn in one request. They are filled only when the reader may see the
	// artifact - the join carries the same permission filter a direct read
	// would - so a task whose share was revoked lists without leaking a title.
	ArtifactTitle string `json:"artifact_title,omitempty"`
	ArtifactType  string `json:"artifact_type,omitempty"`
}

// Task states.
const (
	TaskOpen      = "open"
	TaskDelegated = "delegated"
	TaskDone      = "done"
)

// ValidTaskState reports whether state is one a task may be moved to.
func ValidTaskState(state string) bool {
	switch state {
	case TaskOpen, TaskDelegated, TaskDone:
		return true
	}
	return false
}

// taskColumns is the read list, in the order scanTask expects.
const taskColumns = `t.id, t.artifact, t.from_user, t.to_user, t.project, t.state,
	t.assignee_agent, t.thread, t.hlc, t.node`

// scanTask reads one row of taskColumns, optionally followed by the joined
// artifact title and type.
func scanTask(sc scanner, withArtifact bool) (*Task, error) {
	var (
		t                           Task
		artifact, from, to, project sql.NullString
		state, agent, thread, node  sql.NullString
		clockVal                    sql.NullInt64
		title, artType              sql.NullString
	)
	dest := []any{&t.ID, &artifact, &from, &to, &project, &state, &agent, &thread, &clockVal, &node}
	if withArtifact {
		dest = append(dest, &title, &artType)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	t.Artifact, t.FromUser, t.ToUser = artifact.String, from.String, to.String
	t.Project, t.State, t.AssigneeAgent = project.String, state.String, agent.String
	t.Thread, t.Node, t.HLC = thread.String, node.String, clockVal.Int64
	t.ArtifactTitle, t.ArtifactType = title.String, artType.String
	return &t, nil
}

// InsertTask writes a task, stamping id/hlc/node when unset.
func (d *DB) InsertTask(ctx context.Context, t *Task) error {
	return d.insertTask(ctx, d.sql, t)
}

// insertTask is InsertTask against whatever is in hand - the pool, or the
// transaction WriteAssignment holds.
func (d *DB) insertTask(ctx context.Context, q execer, t *Task) error {
	d.stamp(&t.ID, &t.HLC, &t.Node)
	if t.State == "" {
		t.State = TaskOpen
	}
	// project and assignee_agent are NULL rather than '' when absent, so a
	// personal task and an undelegated one read back the same way they were
	// written whichever client wrote them.
	_, err := q.ExecContext(ctx,
		`INSERT INTO tasks (id, artifact, from_user, to_user, project, state,
		                    assignee_agent, thread, hlc, node)
		 VALUES ($1, $2, $3, $4, nullif($5, ''), $6, nullif($7, ''), $8, $9, $10)`,
		t.ID, t.Artifact, t.FromUser, t.ToUser, t.Project, t.State,
		t.AssigneeAgent, t.Thread, t.HLC, t.Node)
	if err != nil {
		return fmt.Errorf("store: insert task: %w", err)
	}
	return nil
}

// WriteAssignment writes the three rows a handoff is - the share that lets the
// assignee read the artifact, the task, and the message that opens the thread
// they will talk in - in one transaction and under one clock reading.
//
// One reading because they are one operation as far as ordering goes: a peer
// merging them cannot interleave anything between the share and the task it
// exists for. One transaction because they are one operation to the people
// using it, and until they were, a failure between two of the writes left the
// half behind for good - a task whose artifact the assignee gets a 404 on, or a
// share nobody can see the reason for. Nothing comes back to finish it, and the
// half replicates on its own, because each row carries its own reading and a
// peer merges what is there.
//
// The rows go in in that order, so a rollback that somehow does not happen
// leaves the harmless one rather than the confusing one.
func (d *DB) WriteAssignment(ctx context.Context, g *Grant, t *Task, opening *Event) error {
	at := d.clock.Pack()
	g.HLC, t.HLC, opening.SeqHLC = at, at, at

	return d.inTx(ctx, "write assignment", func(tx *sql.Tx) error {
		if err := d.insertGrant(ctx, tx, g); err != nil {
			return err
		}
		if err := d.insertTask(ctx, tx, t); err != nil {
			return err
		}
		return d.appendEvent(ctx, tx, opening)
	})
}

// GetTask reads a task by id, without asking who wants it.
func (d *DB) GetTask(ctx context.Context, id string) (*Task, error) {
	t, err := scanTask(d.sql.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks t WHERE t.id = $1`, id), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get task %s: %w", id, err)
	}
	return t, nil
}

// taskPartySQL is true for the tasks p is a party to: the person who handed the
// work over, the person it was handed to, or the agent it was delegated to.
//
// Nobody else reads a task at all - not a project mate of either side, not the
// operator. A handoff is between two people, and a third party learning that it
// exists is the leak, so the filter is on the row rather than on the project.
func taskPartySQL(p *Principal, alias string, a *args) string {
	if p == nil {
		return "FALSE"
	}
	user := a.next(p.UserID)
	agent := a.next(p.AgentID)
	return `(` + alias + `.from_user = ` + user + ` AND ` + user + ` <> ''
	      OR ` + alias + `.to_user = ` + user + ` AND ` + user + ` <> ''
	      OR ` + alias + `.assignee_agent = ` + agent + ` AND ` + agent + ` <> '')`
}

// ReadTask returns the task only if p is a party to it. Anything else is
// ErrNotFound, the same as a task that was never there.
func (d *DB) ReadTask(ctx context.Context, p *Principal, id string) (*Task, error) {
	a := &args{}
	idArg := a.next(id)
	where := taskPartySQL(p, "t", a)
	t, err := scanTask(d.sql.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks t WHERE t.id = `+idArg+` AND `+where, a.vals...), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read task %s: %w", id, err)
	}
	return t, nil
}

// TaskQuery narrows an inbox read.
type TaskQuery struct {
	State string // one state, or empty for all of them
	Limit int
}

func (q TaskQuery) limit() int {
	if q.Limit > 0 && q.Limit <= 1000 {
		return q.Limit
	}
	return defaultLimit
}

// ListTasks is the principal's inbox: the tasks handed to them, or to their
// agent, newest first.
//
// The artifact is joined in through the same permission filter a direct read
// would use, so the title comes back when the share is live and is simply
// absent when it is not.
func (d *DB) ListTasks(ctx context.Context, p *Principal, q TaskQuery) ([]*Task, error) {
	a := &args{}
	user := a.next(p.UserID)
	agent := a.next(p.AgentID)
	where := `(t.to_user = ` + user + ` AND ` + user + ` <> ''
	        OR t.assignee_agent = ` + agent + ` AND ` + agent + ` <> '')`
	if q.State != "" {
		where += " AND t.state = " + a.next(q.State)
	}
	filter := ArtifactFilterSQL(p, "ar", a, false)

	query := `SELECT ` + taskColumns + `, ar.title, ar.type
	            FROM tasks t
	            LEFT JOIN artifacts ar
	              ON ar.id = t.artifact AND coalesce(ar.tombstone, false) = false AND ` + filter + `
	           WHERE ` + where + `
	           ORDER BY t.hlc DESC, t.id DESC
	           LIMIT ` + a.next(q.limit())

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: list tasks: %w", err)
	}
	defer rows.Close()

	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows, true)
		if err != nil {
			return nil, fmt.Errorf("store: list tasks: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tasks: %w", err)
	}
	return out, nil
}

// UpdateTask writes back a task's state and assignee, with a fresh clock
// reading so the move orders after whatever it moved from.
func (d *DB) UpdateTask(ctx context.Context, t *Task) error {
	t.HLC = d.clock.Pack()
	t.Node = d.node
	_, err := d.sql.ExecContext(ctx,
		`UPDATE tasks SET state = $2, assignee_agent = nullif($3, ''), hlc = $4, node = $5
		  WHERE id = $1`,
		t.ID, t.State, t.AssigneeAgent, t.HLC, t.Node)
	if err != nil {
		return fmt.Errorf("store: update task %s: %w", t.ID, err)
	}
	return nil
}

// AgentForUser picks the agent a user's work is delegated to: the one they
// registered first, which is stable for as long as it exists. ErrNotFound when
// the user has no agent - auto_delegate cannot hand work to nobody.
func (d *DB) AgentForUser(ctx context.Context, userID string) (*Agent, error) {
	var id string
	err := d.sql.QueryRowContext(ctx,
		`SELECT id FROM agents WHERE user_id = $1 ORDER BY hlc, id LIMIT 1`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: agent for user %s: %w", userID, err)
	}
	return d.GetAgent(ctx, id)
}

// SetAutoDelegate flips a user's standing answer to inbound work. It bumps the
// clock, because it is a fact about the user that replicates like any other.
func (d *DB) SetAutoDelegate(ctx context.Context, userID string, on bool) (*User, error) {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE users SET auto_delegate = $2, hlc = $3, node = $4 WHERE id = $1`,
		userID, on, d.clock.Pack(), d.node)
	if err != nil {
		return nil, fmt.Errorf("store: set auto_delegate for %s: %w", userID, err)
	}
	n, err := affectedRows(res)
	if err != nil {
		return nil, fmt.Errorf("store: set auto_delegate for %s: %w", userID, err)
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return d.GetUser(ctx, userID)
}
