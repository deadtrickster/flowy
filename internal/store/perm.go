package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is what a permission-filtered read returns when the row is
// missing. It is also what it returns when the row exists but the principal may
// not see it: the two cases are deliberately indistinguishable, because telling
// them apart leaks the existence of an artifact across a project boundary.
var ErrNotFound = errors.New("store: not found")

// ErrTaken is what a create returns when the id it was given is already a row
// here. It is deliberately not ErrNotFound: the caller is told nothing about
// the row - not whose it is, not what project it is in, not whether it is
// deleted - and the handler turns it into the same 404 a read would give. What
// it does say to the code above it is that nothing was written.
var ErrTaken = errors.New("store: id is taken")

// The visibilities an artifact can carry.
//
//	personal     - the floor. Its owner and their agents, and no grant reaches
//	               through it. A row with no project is one of these whatever
//	               the column says.
//	project-only - the project it is in, and nothing else. A grant does not
//	               reach it: that is the whole of what makes it narrower than
//	               the two below, and it is what the mem_write `project` scope
//	               promises an agent.
//	project      - the project it is in, plus whoever the project's grants and
//	               its own shares reach. This is the default for an artifact
//	               written over the API, and it is what "a bug in pa, readable
//	               by pb because pa opened up" has always meant.
//	shared       - the same reach, said on purpose rather than by default.
const (
	VisibilityPersonal    = "personal"
	VisibilityProjectOnly = "project-only"
	VisibilityProject     = "project"
	VisibilityShared      = "shared"
)

// Principal is the identity a request acts as: a (user, agent, project) triple
// resolved from a bearer token. Project is the principal's home project, the
// one it reads without needing a grant.
//
// Operator marks the token as belonging to whoever runs this node. It is the
// only principal that ?scope=all obeys, it is decided by local configuration
// rather than by a row, and it is never consulted by anything that replicates.
type Principal struct {
	Token    string `json:"-"`
	UserID   string `json:"user"`
	AgentID  string `json:"agent,omitempty"`
	Project  string `json:"project,omitempty"`
	Operator bool   `json:"operator,omitempty"`
}

// Grant is a capability row: a project-wide edge when Artifact is empty, a
// single-artifact share when it is not.
type Grant struct {
	ID          string `json:"id"`
	FromProject string `json:"from_project"`
	ToProject   string `json:"to_project"`
	Subject     string `json:"subject,omitempty"`
	Artifact    string `json:"artifact,omitempty"`
	Cap         string `json:"cap"`
	GrantedBy   string `json:"granted_by,omitempty"`
	HLC         int64  `json:"hlc"`
	Node        string `json:"node"`
	Tombstone   bool   `json:"tombstone"`
}

// CanRead is the read predicate, in Go. ArtifactFilterSQL is the same rule
// expressed as a WHERE fragment; TestCanReadMatchesSQL holds the two together.
//
// The rules, in the order they are applied:
//
//  1. A personal artifact - visibility 'personal', or no project at all - is
//     readable only by its owner. This is a floor: no grant reaches through it,
//     which is why it is tested before anything else looks at grants.
//  2. An artifact in the principal's home project is readable.
//  3. A 'project-only' artifact stops there. It is the second floor, and it is
//     the one the memory tools' `project` scope has always claimed to be: a
//     grant into the project does not reach it, and neither does a share.
//  4. Anything else is cross-project, and needs a live grant: either a
//     project-wide one along the edge (principal's project -> artifact's
//     project) or a share of this one artifact to this one user.
//
// grants may be any superset of the rows that mention art; tombstoned rows are
// ignored here rather than by the caller.
func CanRead(p *Principal, art *Artifact, grants []Grant) bool {
	if p == nil || art == nil {
		return false
	}
	if art.Visibility == VisibilityPersonal || art.Project == nil {
		return p.UserID != "" && art.OwnerUser == p.UserID
	}
	if p.Project != "" && *art.Project == p.Project {
		return true
	}
	if art.Visibility == VisibilityProjectOnly {
		return false
	}
	for _, g := range grants {
		if g.Tombstone {
			continue
		}
		projectWide := g.Artifact == "" &&
			p.Project != "" && g.FromProject == p.Project && g.ToProject == *art.Project
		perArtifact := g.Artifact != "" &&
			g.Artifact == art.ID && p.UserID != "" && g.Subject == p.UserID
		if projectWide || perArtifact {
			return true
		}
	}
	return false
}

// args accumulates positional query parameters so a filter fragment and the
// query it is spliced into can agree on numbering without counting by hand.
type args struct{ vals []any }

// next records v and returns the placeholder that reads it back.
func (a *args) next(v any) string {
	a.vals = append(a.vals, v)
	return fmt.Sprintf("$%d", len(a.vals))
}

// ArtifactFilterSQL returns a boolean SQL fragment that is true for exactly the
// artifacts CanRead would allow, for the table aliased as alias. Its parameters
// are appended to a.
//
// This is the only place reads are narrowed. Every list and every search puts
// it in the WHERE clause, so the database never hands the node a row the
// principal may not see - filtering after the fact would still have read it,
// and would still get the count, the rank and the paging wrong.
//
// scopeAll is the ?scope=all escape hatch and does nothing unless the principal
// is this node's operator.
func ArtifactFilterSQL(p *Principal, alias string, a *args, scopeAll bool) string {
	if p == nil {
		return "FALSE"
	}
	if scopeAll && p.Operator {
		return "TRUE"
	}
	user := a.next(p.UserID)
	project := a.next(p.Project)

	// CASE, not OR, so the personal branch is a floor: when it is taken the
	// grant tests are not merely false, they are not reachable. 'project-only'
	// is the same shape one step down - the project and nothing else - because
	// a scope that is documented as narrower than 'shared' and reaches exactly
	// as far is a scope that lies to whoever chose it.
	return strings.NewReplacer("{a}", alias, "{user}", user, "{project}", project).Replace(
		`(CASE WHEN {a}.visibility = 'personal' OR {a}.project IS NULL
		       THEN {a}.owner_user = {user} AND {user} <> ''
		       WHEN {a}.visibility = 'project-only'
		       THEN {a}.project = {project} AND {project} <> ''
		       ELSE {a}.project = {project} AND {project} <> ''
		         OR EXISTS (SELECT 1 FROM grants g
		                     WHERE coalesce(g.tombstone, false) = false
		                       AND g.artifact IS NULL
		                       AND g.from_project = {project} AND {project} <> ''
		                       AND g.to_project = {a}.project)
		         OR EXISTS (SELECT 1 FROM grants g
		                     WHERE coalesce(g.tombstone, false) = false
		                       AND g.artifact = {a}.id
		                       AND g.subject = {user} AND {user} <> '')
		  END)`)
}

// EventFilterSQL narrows the event log the same way, on the event's project.
// Events carry no visibility column, so the floor is the project-less event: it
// belongs to whoever wrote it, and only they read it back.
//
// The share of one artifact reaches the events about it, which is the clause
// ArtifactFilterSQL has and this one did not. A cross-project share let the
// subject read the artifact and its history - /api/artifact/{id}/history is
// gated on the artifact read - and not one event about it anywhere else, so the
// two read surfaces disagreed about the same rows. It is the artifact's own
// branch, joined: a share only reaches what a share can reach, so an artifact
// behind the personal or project-only floor is no more readable event by event
// than it is row by row.
//
// The last half is the assignment thread. A handoff crosses a project
// boundary by definition - the whole point of it is that somebody in another
// project now has the work - so a thread that a task names is readable by the
// two people the task is between and by the agent it was delegated to,
// whichever project each of them writes from. Without it an assignment would
// open a conversation only one side could read, which is not a conversation.
//
// It is a widening and it is deliberately narrow: it reaches only events whose
// thread is named by a tasks row, and only for the parties named on that row.
func EventFilterSQL(p *Principal, alias string, a *args, scopeAll bool) string {
	if p == nil {
		return "FALSE"
	}
	if scopeAll && p.Operator {
		return "TRUE"
	}
	user := a.next(p.UserID)
	agent := a.next(p.AgentID)
	project := a.next(p.Project)

	return strings.NewReplacer("{a}", alias, "{user}", user, "{agent}", agent, "{project}", project).Replace(
		`((CASE WHEN {a}.project IS NULL
		        THEN ({a}.actor = {user} AND {user} <> '')
		          OR ({a}.actor = {agent} AND {agent} <> '')
		        ELSE {a}.project = {project} AND {project} <> ''
		          OR EXISTS (SELECT 1 FROM grants g
		                      WHERE coalesce(g.tombstone, false) = false
		                        AND g.artifact IS NULL
		                        AND g.from_project = {project} AND {project} <> ''
		                        AND g.to_project = {a}.project)
		          OR EXISTS (SELECT 1 FROM grants g JOIN artifacts sar ON sar.id = g.artifact
		                      WHERE coalesce(g.tombstone, false) = false
		                        AND g.artifact = {a}.artifact
		                        AND coalesce({a}.artifact, '') <> ''
		                        AND g.subject = {user} AND {user} <> ''
		                        AND sar.project IS NOT NULL
		                        AND coalesce(sar.visibility, '') <> 'personal'
		                        AND coalesce(sar.visibility, '') <> 'project-only')
		   END)
		  OR EXISTS (SELECT 1 FROM tasks t
		              WHERE t.thread = {a}.thread AND coalesce({a}.thread, '') <> ''
		                AND (t.from_user = {user} AND {user} <> ''
		                  OR t.to_user = {user} AND {user} <> ''
		                  OR t.assignee_agent = {agent} AND {agent} <> '')))`)
}

// PrincipalForToken resolves a bearer token. A token that names only an agent
// inherits that agent's user and, when it names no project of its own, that
// agent's project - so an agent acting for a user reaches exactly what the user
// reaches, which is what the personal floor means by "an agent whose user is
// the owner".
//
// It returns ErrNotFound for a token that is not in the table, which the
// middleware turns into 401.
func (d *DB) PrincipalForToken(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	var (
		p                        Principal
		userID, agentID, project sql.NullString
		agentUser, agentProject  sql.NullString
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT t.token, t.user_id, t.agent_id, t.project, a.user_id, a.project
		   FROM tokens t LEFT JOIN agents a ON a.id = t.agent_id
		  WHERE t.token = $1`, token).
		Scan(&p.Token, &userID, &agentID, &project, &agentUser, &agentProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve token: %w", err)
	}

	p.UserID, p.AgentID, p.Project = userID.String, agentID.String, project.String
	if p.UserID == "" {
		p.UserID = agentUser.String
	}
	if p.Project == "" {
		p.Project = agentProject.String
	}
	return &p, nil
}

// InsertToken writes a bearer token for a principal.
func (d *DB) InsertToken(ctx context.Context, p *Principal) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO tokens (token, user_id, agent_id, project) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (token) DO UPDATE
		    SET user_id = excluded.user_id, agent_id = excluded.agent_id,
		        project = excluded.project`,
		p.Token, p.UserID, p.AgentID, p.Project)
	if err != nil {
		return fmt.Errorf("store: insert token: %w", err)
	}
	return nil
}

// InsertGrant writes a grant, stamping id/hlc/node when unset.
func (d *DB) InsertGrant(ctx context.Context, g *Grant) error {
	return d.insertGrant(ctx, d.sql, g)
}

// insertGrant is InsertGrant against whatever is in hand - the pool, or the
// transaction an assignment writes its three rows in.
func (d *DB) insertGrant(ctx context.Context, q execer, g *Grant) error {
	d.stamp(&g.ID, &g.HLC, &g.Node)
	if g.Cap == "" {
		g.Cap = "read"
	}
	// artifact and subject are NULL rather than '' when absent: the filter asks
	// `artifact IS NULL` to mean "project-wide", and an empty string is not that.
	_, err := q.ExecContext(ctx,
		`INSERT INTO grants (id, from_project, to_project, subject, artifact, cap,
		                     granted_by, hlc, node, tombstone)
		 VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, nullif($7, ''), $8, $9, $10)`,
		g.ID, g.FromProject, g.ToProject, g.Subject, g.Artifact, g.Cap,
		g.GrantedBy, g.HLC, g.Node, g.Tombstone)
	if err != nil {
		return fmt.Errorf("store: insert grant: %w", err)
	}
	return nil
}

// GrantsFor reads every grant that could bear on art: the project-wide edges
// into its project and the shares of the artifact itself. It is what feeds
// CanRead when the caller wants the predicate rather than the filter.
func (d *DB) GrantsFor(ctx context.Context, art *Artifact) ([]Grant, error) {
	project := ""
	if art.Project != nil {
		project = *art.Project
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT id, from_project, to_project, coalesce(subject, ''), coalesce(artifact, ''),
		        coalesce(cap, 'read'), coalesce(granted_by, ''), coalesce(hlc, 0),
		        coalesce(node, ''), coalesce(tombstone, false)
		   FROM grants
		  WHERE (artifact IS NULL AND to_project = $1) OR artifact = $2`, project, art.ID)
	if err != nil {
		return nil, fmt.Errorf("store: grants for %s: %w", art.ID, err)
	}
	defer rows.Close()

	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.ID, &g.FromProject, &g.ToProject, &g.Subject, &g.Artifact,
			&g.Cap, &g.GrantedBy, &g.HLC, &g.Node, &g.Tombstone); err != nil {
			return nil, fmt.Errorf("store: grants for %s: %w", art.ID, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: grants for %s: %w", art.ID, err)
	}
	return out, nil
}
