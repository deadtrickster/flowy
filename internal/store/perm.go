package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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

// MemScopes are the scopes an agent chooses between, in the order they widen:
// personal is the floor no grant reaches through, project is everyone in the
// project and nobody else, shared is the project plus whoever holds a grant or
// a share.
var MemScopes = []string{"personal", "project", "shared"}

// memScopeVisibility is the visibility each scope is stored as.
//
// project is not stored as 'project'. That value has always meant "the project,
// and whoever the project's grants reach", because it is what an artifact
// written over the API gets by default - so an item written at scope=project
// was readable by exactly the people the scope said it was not.
// VisibilityProjectOnly is the value that means what the scope says.
//
// It is here rather than beside the tool that first needed it because it is not
// one surface's opinion any more: mem_write chooses a scope from an argument
// and the FUSE mount chooses one from a path plus a line of front matter, and
// two tables mapping the same three words to the same three columns are two
// tables that will disagree one day about which of them means "the project and
// nobody else".
var memScopeVisibility = map[string]string{
	"personal": VisibilityPersonal,
	"project":  VisibilityProjectOnly,
	"shared":   VisibilityShared,
}

// VisibilityForScope is the visibility a scope is stored as. Anything that is
// not one of the scopes is passed through: an item written before this
// distinction existed carries a visibility that is not a scope, and reading it
// back is not the moment to change what it means.
func VisibilityForScope(scope string) string {
	if v, ok := memScopeVisibility[scope]; ok {
		return v
	}
	return scope
}

// ScopeForVisibility names the scope a visibility is, for a message an agent
// reads and for the front matter of a file in the mount.
func ScopeForVisibility(visibility string) string {
	for scope, v := range memScopeVisibility {
		if v == visibility {
			return scope
		}
	}
	return visibility
}

// Principal is the identity a request acts as: a (user, agent, project) triple
// resolved from a bearer token. Project is the principal's home project, the
// one it reads without needing a grant.
//
// Operator marks the token as belonging to whoever runs this node. It is the
// only principal that ?scope=all obeys, it is decided by local configuration
// rather than by a row, and it is never consulted by anything that replicates.
type Principal struct {
	Token   string `json:"-"`
	UserID  string `json:"user"`
	AgentID string `json:"agent,omitempty"`
	// AgentKind is the kind of the agent the token names, when it names one:
	// worker|reviewer|system|monitor. It comes off the agents row at the same
	// time as the user and the project do, because it is part of the same
	// answer - what this token is - and a capability read on a second query is
	// a capability that can be read against a row that has since changed.
	//
	// A token that names no agent has no kind: a person is not an agent, and
	// the one thing the kind unlocks - a federation-scope announcement - is
	// deliberately something a machine posts and not something a person can
	// post by holding their own credential.
	AgentKind string `json:"agent_kind,omitempty"`
	Project   string `json:"project,omitempty"`
	Operator  bool   `json:"operator,omitempty"`
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
	// Sig is the writing node's signature over the capability - see
	// Artifact.Sig. A grant is the row that opens a project up, so it is the
	// row a hostile peer most wants to write in somebody else's name.
	Sig []byte `json:"sig,omitempty"`
}

// CapRead is the one capability a grant can carry. Every read rule - CanRead,
// ArtifactFilterSQL, EventFilterSQL, the share clauses in the merge - treats a
// grant that is not tombstoned as a read and asks the column nothing, so a
// grant saying anything else describes a reach this node does not implement.
const CapRead = "read"

// ErrBadCap is a grant carrying a capability that is not implemented here.
var ErrBadCap = errors.New("store: grant carries a capability this node does not implement")

// GrantCapOK reports whether cap is a capability a grant may carry. Empty is
// one: the reads coalesce a missing cap to read and the local write path fills
// it in, so a row that arrived without one is a read grant and not a lie.
//
// It is deliberately a closed set rather than a length check. A cap nothing
// consults is a column that can say anything - `write`, or ten megabytes of
// it - and be stored, signed and replicated as if this node had agreed to it.
// Nothing acts on it today, so the day something does, it acts on values that
// were never checked when they were written. Both doors ask this: the handler
// that mints a grant, and the merge that takes one from a peer. Widen it when a
// second capability is implemented, not before.
func GrantCapOK(capability string) bool {
	return capability == "" || capability == CapRead
}

// capSaid renders a cap for a refusal, cut short: it is a string off the wire,
// and a refusal is not a place to print an arbitrary amount of somebody else's
// text back out.
func capSaid(capability string) string {
	const most = 32
	if len(capability) > most {
		return strconv.Quote(capability[:most]) + "..."
	}
	return strconv.Quote(capability)
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

// artifactReachSQL is the read rule for one artifact row, as a boolean SQL
// fragment over the table aliased as alias, given the placeholders that already
// hold the principal's user id and home project.
//
// It is the definition, and it has exactly one copy on purpose. Two read
// surfaces cover an artifact - the row itself and the events that name it - and
// while each carried its own hand-written idea of the floor they drifted: the
// event filter's grant branches were given an approximation of this rule, its
// home-project branch was given none at all, and an artifact refused row by row
// was handed over event by event by whichever branch had been missed. Both
// callers now evaluate this, so there is no second rule left to forget.
//
// CASE, not OR, so the personal branch is a floor: when it is taken the grant
// tests are not merely false, they are not reachable. 'project-only' is the
// same shape one step down - the project and nothing else - because a scope
// that is documented as narrower than 'shared' and reaches exactly as far is a
// scope that lies to whoever chose it.
//
// The grants subqueries alias the table as `g`. A caller splicing this inside
// its own `grants g` scope would shadow that alias, so the event filter puts
// this in a clause of its own rather than inside one.
func artifactReachSQL(alias, user, project string) string {
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

// A direct message is the only private conversation this node has, and it is a
// SHAPE rather than a flag: a chat event with no project, no room, and an
// addressee. Every part of it is already inside the signature - see
// sign.CanonicalEvent - so a relay cannot turn a room message into a private one
// or a private one into a room message without producing bytes that do not
// verify.
//
// It is a shape and not a column for the reason the room is not a permission:
// nothing here has a per-row privacy flag, and the first one would be a value
// every future branch of the filter has to remember to consult. A projectless
// event is already readable by its author and nobody else, so a direct message
// is not a new kind of secret - it is the floor that was already there, widened
// by exactly one named principal.
//
// The three parts each rule something out rather than decorate:
//
//   - project IS NULL is what keeps it off every project read. It is not that
//     the branches below choose not to show it; they are not reachable for it.
//   - an empty room is what keeps this from re-reading rows that already exist. A
//     projectless principal saying something addressed in a room writes a
//     projectless addressed event today, and that row is its author's alone.
//     Requiring no room means no row written before this feature changes who
//     reads it.
//   - type = 'chat' because a message is the only thing that has parties. A
//     status move or a worklog entry with an addressee is not a conversation,
//     and the widening is only sound for rows whose addressee means "this is
//     for you".
//
// IsDirectMessage is the same rule in Go, for the surfaces that render one.
func privateEventSQL(alias string) string {
	return strings.NewReplacer("{a}", alias).Replace(
		`({a}.project IS NULL
		    AND {a}.type = '` + ChatEventType + `'
		    AND coalesce({a}.room, '') = ''
		    AND coalesce({a}.addressee, '') <> '')`)
}

// IsDirectMessage reports whether e is a direct message: the Go half of
// privateEventSQL, for the handlers that decide what to write and the clients
// that decide how to draw it.
//
// It decides nothing about who may read one. The SQL is what runs on every read,
// and this must never become a second answer to that question - a row is private
// because the database refused it to everybody else, not because a renderer
// believed it was.
func IsDirectMessage(e *Event) bool {
	return e != nil && e.Project == nil && e.Type == ChatEventType &&
		e.Room == "" && e.Addressee != ""
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
	return artifactReachSQL(alias, user, project)
}

// EventFilterSQL narrows the event log the same way, on the event's project.
// Events carry no visibility column, so the floor is the project-less event: it
// belongs to whoever wrote it, and only they read it back.
//
// The one exception is a direct message, and it is deliberately built into the
// floor rather than beside it. A DM is a projectless chat event that names its
// addressee - see privateEventSQL - and all this branch does is widen "the
// author" to "the author and the principal they named". It is one clause, in the
// branch that already excludes everybody, so a projectless event stays invisible
// to every project reader BY CONSTRUCTION: the branches below are not merely
// false for it, the CASE never reaches them. A privacy that lived on the room
// instead would be a per-room scope - a table, a join in the hottest predicate
// here, and a rule every future branch has to remember - and one that lived on
// the tasks clause below would be worse still, because that clause is OR-ed onto
// the END of the whole predicate and ADDS readers rather than narrowing them.
//
// The addressee is matched against the reader the same way the actor is: the
// user id and the agent id, and nothing else. That is not an oversight, it is
// the existing shape of this branch, and it has one consequence worth knowing:
// a message an AGENT sends is the agent's, so the person it works for does not
// read it back from their own token, exactly as a projectless event written by
// an agent has never been readable by its user. Address a person to reach a
// person - an agent's token inherits its user's id, so a DM to a person is read
// by that person and by the agents acting for them, which is the pair this node
// has always treated as one reader.
//
// A project-bearing event is two questions, and it is one AND because both have
// to be answered:
//
//  1. Does the reader reach the event's project at all? Their own project does;
//     a live project-wide edge into it does; and a live share of the artifact
//     the event names does, because a share of one artifact carries the events
//     about it - /api/artifact/{id}/history is gated on reading the artifact
//     rather than on reading each event, so without that clause the two read
//     surfaces disagreed about the same rows.
//  2. Does the reader reach the artifact the event names? An event that names
//     one inherits that artifact's floor, and the test is artifactReachSQL -
//     ArtifactFilterSQL's own branches, on the named row. An event that names
//     none is chatter and stops at question 1, which is what the grant and the
//     project are for.
//
// The second question is why this is written the way it is. It used to be
// asked branch by branch, in wording of each branch's own: the project-wide
// grant carried an approximation of the floor, the share carried the same
// approximation, and the home-project branch - the one every reader in the
// event's own project takes - carried nothing. So an artifact shared into a
// project by name was refused row by row to everybody else in that project and
// handed to them event by event: the sharer names it on an event, the event
// lands in their home project because that is where their writes land, and the
// whole project reads the body while /api/artifact/{id}/history 404s at them.
// It replicated from there, permanently. One rule, evaluated once, in a clause
// no branch can skip, is what stops a fourth branch being written without it.
//
// The last half is the assignment thread. A handoff crosses a project
// boundary by definition - the whole point of it is that somebody in another
// project now has the work - so a thread that a task names is readable by the
// two people the task is between and by the agent it was delegated to,
// whichever project each of them writes from. Without it an assignment would
// open a conversation only one side could read, which is not a conversation. A
// party naming their own artifact in their own handoff thread is disclosure by
// a party rather than a way round the floor, so this clause is deliberately
// left outside the AND.
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

	// The floor, in the artifact filter's own words rather than in a copy of
	// them. An event naming an artifact this node has never seen is not
	// readable across a project boundary on the strength of the name alone.
	floor := `(coalesce({a}.artifact, '') = ''
		           OR EXISTS (SELECT 1 FROM artifacts par
		                       WHERE par.id = {a}.artifact
		                         AND ` + artifactReachSQL("par", user, project) + `))`

	return strings.NewReplacer("{a}", alias, "{user}", user, "{agent}", agent, "{project}", project).Replace(
		`((CASE WHEN {a}.project IS NULL
		        THEN ({a}.actor = {user} AND {user} <> '')
		          OR ({a}.actor = {agent} AND {agent} <> '')
		          OR (` + privateEventSQL("{a}") + `
		               AND (({a}.addressee = {user} AND {user} <> '')
		                 OR ({a}.addressee = {agent} AND {agent} <> '')))
		        ELSE ({a}.project = {project} AND {project} <> ''
		               OR EXISTS (SELECT 1 FROM grants g
		                           WHERE coalesce(g.tombstone, false) = false
		                             AND g.artifact IS NULL
		                             AND g.from_project = {project} AND {project} <> ''
		                             AND g.to_project = {a}.project)
		               OR EXISTS (SELECT 1 FROM grants g
		                           WHERE coalesce(g.tombstone, false) = false
		                             AND g.artifact = {a}.artifact
		                             AND coalesce({a}.artifact, '') <> ''
		                             AND g.subject = {user} AND {user} <> ''))
		             AND ` + floor + `
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
		agentKind                sql.NullString
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT t.token, t.user_id, t.agent_id, t.project, a.user_id, a.project,
		        coalesce(a.agent_kind, 'worker')
		   FROM tokens t LEFT JOIN agents a ON a.id = t.agent_id
		  WHERE t.token = $1`, token).
		Scan(&p.Token, &userID, &agentID, &project, &agentUser, &agentProject, &agentKind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: resolve token: %w", err)
	}

	p.UserID, p.AgentID, p.Project = userID.String, agentID.String, project.String
	// Only a token that actually names an agent carries a kind. The coalesce in
	// the query fires on the LEFT JOIN's empty side too, so without this a
	// person's own token would come back as a worker - which reads as "an agent
	// of the least privileged kind" rather than as "not an agent".
	if p.AgentID != "" {
		p.AgentKind = agentKind.String
	}
	if p.UserID == "" {
		p.UserID = agentUser.String
	}
	if p.Project == "" {
		p.Project = agentProject.String
	}
	return &p, nil
}

// InsertToken writes a bearer token for a principal.
//
// The scope is checked against the registry before the row is written, and this
// is the doorway the whole project entity exists for: a token's project is
// where every write made with it lands, so a token scoped to a name nobody
// declared is a whole seat's work filed into a project that does not exist. The
// column carries a foreign key as well - tokens are local, so the database can
// hold that promise - and this is the same rule where it can say which project
// it means.
func (d *DB) InsertToken(ctx context.Context, p *Principal) error {
	if err := requireProject(ctx, d.sql, p.Project); err != nil {
		return err
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO tokens (token, user_id, agent_id, project) VALUES ($1, $2, $3, nullif($4, ''))
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
	if err := d.stamp(&g.ID, &g.HLC, &g.Node); err != nil {
		return err
	}
	if g.Cap == "" {
		g.Cap = CapRead
	}
	// Before it is signed, because signing it is this node saying the row is
	// what it says it is. A cap nothing implements is not that, and a grant is
	// replicated: a value waved through here is a value every peer holds.
	if !GrantCapOK(g.Cap) {
		return fmt.Errorf("%w: %s", ErrBadCap, capSaid(g.Cap))
	}
	// Both ends of the edge name a declared project. A grant is a capability
	// between two referents, and one that points at a project nobody declared
	// opens nothing - it is a typo that replicates.
	for _, project := range []string{g.FromProject, g.ToProject} {
		if err := requireProject(ctx, q, project); err != nil {
			return err
		}
	}
	if err := d.signGrant(ctx, g); err != nil {
		return err
	}
	// artifact and subject are NULL rather than '' when absent: the filter asks
	// `artifact IS NULL` to mean "project-wide", and an empty string is not that.
	_, err := q.ExecContext(ctx,
		`INSERT INTO grants (id, from_project, to_project, subject, artifact, cap,
		                     granted_by, hlc, node, tombstone, sig)
		 VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), $6, nullif($7, ''), $8, $9, $10, $11)`,
		g.ID, g.FromProject, g.ToProject, g.Subject, g.Artifact, g.Cap,
		g.GrantedBy, g.HLC, g.Node, g.Tombstone, g.Sig)
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
		        coalesce(node, ''), coalesce(tombstone, false), sig
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
			&g.Cap, &g.GrantedBy, &g.HLC, &g.Node, &g.Tombstone, &g.Sig); err != nil {
			return nil, fmt.Errorf("store: grants for %s: %w", art.ID, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: grants for %s: %w", art.ID, err)
	}
	return out, nil
}
