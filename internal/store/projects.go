package store

// The project registry. A project used to be a free string on a token, which
// meant a project came into existence the moment somebody wrote it: nothing
// declared one, nothing checked one, and enumerating what existed was a UNION
// of DISTINCT project over the tables that carry it - which cannot see a
// project with no rows yet and cannot tell a typo from a place. A day of real
// shared memory was filed into `pa` that way, which is the smoke seeder's
// fixture project.
//
// So a projects row is a referent, and this file is the four things that makes
// it: declaring one, refusing a local write into one that was never declared,
// enumerating the ones a principal may see, and adapting the registry to data
// that was written before it existed.
//
// Three decisions, in the order they matter:
//
//   - identity IS the name. The primary key is exactly the string the other
//     tables carry, and there is no ULID beside it. `project` is already inside
//     the signed payload of every artifact, event, task and grant that names one
//     (internal/sign), so the name is already the identity on this node: minting
//     a second one would add an identity axis that no existing signed row points
//     at. It is also what makes declaration idempotent - two nodes declaring
//     `flowy` with no contact converge as an ordinary last-writer-wins merge on
//     one key rather than colliding as two ids for one name.
//   - it is identity and referential integrity, and NOT a permission system.
//     Nothing here narrows or widens what a principal may read: permissions are
//     grants plus the token's scope, exactly as they were, and the filter below
//     decides which project NAMES a principal is shown, not which rows. If
//     membership ever moved into this table there would be two places membership
//     is decided, and the one-permission-filter claim would be gone.
//   - the registry adapts to the data, never the other way round. Rows written
//     before this table existed name pa, pb, pc and flowy, and `project` is
//     inside their signatures: rewriting a project column to fit the registry
//     would produce rows whose signatures no longer verify. So the back-fill
//     reads the names the data already carries and declares those.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// The provenances: how a projects row came to exist. It is a fact about the
// row, and it is a different question from Fixture, which is what the project
// is for, and from Origin, which is where the project came from.
//
//	declared - somebody asked for it, through the declaration path.
//	seed     - the smoke seeder wrote it, along with the principals that use it.
//	backfill - it was already named by rows here before the registry existed.
//	observed - it arrived on a replicated row from a peer that had declared it,
//	           and this node had never heard the name. See ObserveProjects.
//	pinned   - the operator said, by hand, which origin this name means here.
//	           It is the answer to a collision, and it is the only provenance a
//	           peer's row cannot overwrite.
const (
	ProvenanceDeclared = "declared"
	ProvenanceSeed     = "seed"
	ProvenanceBackfill = "backfill"
	ProvenanceObserved = "observed"
	ProvenancePinned   = "pinned"
)

// The two origin schemes.
//
// A project's origin is where it came from, and it is the only thing a registry
// row says that two nodes can check against the world rather than against each
// other. `git:` is a canonicalised remote and is the strong form: two nodes
// working on one repository arrive at the same string without exchanging
// anything. `derived:` is what a project with no repository gets - the node
// that first declared it, and the name - and it is a first-class case rather
// than a placeholder: plenty of projects are not repositories, and one that is
// not must work exactly as well.
const (
	OriginGit     = "git:"
	OriginDerived = "derived:"
)

// FixtureProjects are the smoke seeder's fixture projects: demo seed data that
// the gate and the seeded node use to prove the permission rules, and not
// anybody's real work. cmd/smoke puts alice and the operator in pa, bob in pb,
// and a second token for alice in pc.
//
// The list is here rather than in the seeder because two things read it - the
// seeder that writes those rows, and the back-fill that meets them on a
// database seeded before this table existed - and a second copy is a copy to
// forget. It is deliberately not in schema.sql for the same reason.
//
// What the flag buys, precisely: a write into a fixture is refused by nothing.
// pa is a legitimate, writable project and the write that started all this was
// a valid write. The flag is what makes it VISIBLE at the moment it is made -
// on the status line, in the tool result, in the whoami - instead of six hours
// later.
var FixtureProjects = []string{"pa", "pb", "pc"}

// maxProjectName caps a declared name. It is a referent that appears in a FUSE
// path, on a status line and in every row that names it, so it is short by
// design rather than by accident.
const maxProjectName = 64

// ErrUndeclaredProject is a local write into a project that has no registry
// row. It is what makes the registry a referent rather than a list: a typo is
// not silently a valid target.
var ErrUndeclaredProject = errors.New("store: no such project")

// ErrBadProjectName is a declaration this node will not make.
var ErrBadProjectName = errors.New("store: not a usable project name")

// CanonicalOrigin is a git remote as this node records it: one repository, one
// string, whatever spelling it arrived in.
//
// git@github.com:owner/name.git, https://github.com/owner/name and
// ssh://git@github.com/owner/name.git are one repository and three strings, and
// an identity that is three strings is not an identity. So the scheme, the
// credentials, the port and the .git suffix all go, and what is left is
// host/path under the `git:` scheme.
//
// It lower-cases the whole thing rather than the host alone. Owner and
// repository names are case-insensitive on the forges anybody is using, so
// keeping their case would make `github.com/Acme/Flowy` and
// `github.com/acme/flowy` two projects on the strength of how somebody typed a
// remote - which is exactly the silent split this column exists to prevent. The
// cost is a host that really does serve two repositories differing only in
// case, which nothing in this fabric has ever seen.
//
// The port goes for the same reason: two remotes reaching one repository over
// two ports are one repository. An origin already in canonical form is passed
// through, which is what lets a pin, a declaration and a peer's row all be
// written the same way.
func CanonicalOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: an origin needs a value", ErrBadProjectName)
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, OriginDerived) {
		return lower, nil
	}

	// The scheme, whichever kind it is. `://` is checked before the `git:`
	// prefix is stripped, because `git://host/path` is a URL scheme and
	// `git:host/path` is this node's own canonical form - taking the prefix off
	// first would leave `//host/path` and put it back as `git://...`, which is
	// the same repository under a second name.
	//
	// scp-style is the third shape: `git@host:owner/name.git` has no scheme at
	// all, and its colon separates the host from a path rather than from a
	// port, which is why no URL parser reads it.
	switch scheme := strings.Index(lower, "://"); {
	case scheme >= 0:
		lower = lower[scheme+3:]
	case strings.HasPrefix(lower, OriginGit):
		lower = strings.TrimPrefix(lower, OriginGit)
	default:
		if at := strings.Index(lower, "@"); at >= 0 && strings.Contains(lower[at:], ":") {
			lower = strings.Replace(lower[at+1:], ":", "/", 1)
		}
	}
	if at := strings.Index(lower, "@"); at >= 0 {
		// Credentials in a remote are a credential, not an identity.
		lower = lower[at+1:]
	}
	// The port, on the host segment only: a path may hold a colon and that is
	// the path's business.
	if slash := strings.Index(lower, "/"); slash > 0 {
		if colon := strings.Index(lower[:slash], ":"); colon >= 0 {
			lower = lower[:colon] + lower[slash:]
		}
	}
	lower = strings.TrimSuffix(strings.TrimRight(lower, "/"), ".git")
	lower = strings.TrimRight(lower, "/")
	if lower == "" {
		return "", fmt.Errorf("%w: %q is not a remote", ErrBadProjectName, short(raw))
	}
	return OriginGit + lower, nil
}

// DerivedOrigin is the identity a project with no repository gets: the node
// that declared it and the name it was declared under. That pair is what makes
// first-origin trust decidable later - a peer claiming the same name with a
// different derived origin declared it somewhere else, and this node can say so
// rather than merge the two.
func DerivedOrigin(node, name string) string {
	return OriginDerived + strings.ToLower(node) + "/" + strings.ToLower(name)
}

// originChain is every origin a project row has ever claimed, current one last.
// It is the set a collision is decided against: two rows are the same project
// when their chains meet anywhere, because a substitution only ever appends.
func originChain(p *Project) []string {
	if p == nil {
		return nil
	}
	chain := append([]string(nil), p.Superseded...)
	if p.Origin != "" {
		chain = append(chain, p.Origin)
	}
	return chain
}

// SameProject reports whether two registry rows for one name are the same
// project, and says why when they are not.
//
// The three branches, and none of them is a silent merge:
//
//   - the chains meet - the same remote on both sides, or one side holding an
//     origin the other has since superseded. Definitively one project.
//   - either side has no origin at all. A row written before this node recorded
//     origins says nothing about where it came from, and refusing on the
//     strength of a column that was never filled in would be inventing a
//     collision. Accepted, and the adoption in BackfillProjects is what stops
//     it staying that way.
//   - two origins that never meet. Definitively two projects with one name, so
//     it is refused and the operator is told to pin the one this node means.
func SameProject(here, incoming *Project) (bool, string) {
	mine, theirs := originChain(here), originChain(incoming)
	if len(mine) == 0 || len(theirs) == 0 {
		return true, ""
	}
	seen := make(map[string]bool, len(mine))
	for _, origin := range mine {
		seen[origin] = true
	}
	for _, origin := range theirs {
		if seen[origin] {
			return true, ""
		}
	}
	return false, fmt.Sprintf("project %q here comes from %s and the incoming row comes "+
		"from %s; they are two projects with one name", here.ID, here.Origin, incoming.Origin)
}

// Project is one registry row: the name every other row's project column
// points at, and the little that is known about it.
//
// There is no Tombstone, and that is deliberate - see the note on the table in
// schema.sql. A project cannot be deleted because deleting it would orphan
// every row that names it, and because a revocable referent is one a peer can
// revoke: a tombstone arriving over sync would stop this node writing into its
// own project.
type Project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedBy  string `json:"created_by,omitempty"`
	Provenance string `json:"provenance"`
	Fixture    bool   `json:"fixture"`
	// Origin is where the project came from: a canonicalised git remote, or the
	// derived form when it has no repository. Superseded is the origins it
	// replaced, oldest first, and OriginAt is when the current one took over.
	//
	// A substitution appends to Superseded and never touches a row anywhere
	// else. `project` is inside the signed payload of every artifact, event,
	// task and grant that names one, so moving a project's identity by
	// rewriting those columns would leave a database of rows whose signatures
	// no longer verify - forged rows, by this node's own definition. The name
	// is what rows point at and the name does not move; the chain is how a peer
	// still holding the old origin is recognised as the same project.
	Origin     string    `json:"origin,omitempty"`
	Superseded []string  `json:"superseded,omitempty"`
	OriginAt   time.Time `json:"origin_at,omitempty"`
	HLC        int64     `json:"hlc"`
	Node       string    `json:"node"`
	// Sig is the declaring node's signature over the row - see Artifact.Sig.
	Sig     []byte    `json:"sig,omitempty"`
	Created time.Time `json:"created"`
}

// projectColumns is the select list every read of the table uses, in the order
// scanProject reads them.
const projectColumns = `p.id, coalesce(p.name, ''), coalesce(p.created_by, ''),
	coalesce(p.provenance, 'declared'), coalesce(p.fixture, false),
	coalesce(p.origin, ''), p.superseded, p.origin_at,
	coalesce(p.hlc, 0), coalesce(p.node, ''), p.sig, p.created`

// scanProject reads one row of projectColumns.
func scanProject(sc scanner) (*Project, error) {
	var (
		p                 Project
		originAt, created sql.NullTime
		superseded        pq.StringArray
	)
	if err := sc.Scan(&p.ID, &p.Name, &p.CreatedBy, &p.Provenance, &p.Fixture,
		&p.Origin, &superseded, &originAt,
		&p.HLC, &p.Node, &p.Sig, &created); err != nil {
		return nil, err
	}
	p.Superseded = superseded
	p.OriginAt, p.Created = originAt.Time, created.Time
	return &p, nil
}

// cleanProjectName is the name as it would be stored, or an error saying why it
// is not a name. Declaration is the one door it is asked at: the back-fill and
// the observed path take the names the data already carries, because refusing
// one of those would be the registry telling the data it is wrong.
//
// A slash is refused because a project is a directory in the FUSE mount, and a
// name with a separator in it is a path that means something else there.
func cleanProjectName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", fmt.Errorf("%w: a project needs a name", ErrBadProjectName)
	case len(name) > maxProjectName:
		return "", fmt.Errorf("%w: %d bytes, over the %d ceiling",
			ErrBadProjectName, len(name), maxProjectName)
	case strings.ContainsAny(name, "/\\"):
		return "", fmt.Errorf("%w: %q has a path separator in it, and a project is a "+
			"directory in the FUSE mount", ErrBadProjectName, short(name))
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: %q has a control character in it",
				ErrBadProjectName, short(name))
		}
	}
	return name, nil
}

// DeclareProject writes a registry row, stamping id/hlc/node and signing it.
//
// It is idempotent by construction rather than by a check: declaring a project
// that is already here is the same last-writer-wins upsert a peer's copy of it
// goes through, so declaring twice on one node and declaring once on each of
// two nodes end in the same place. What comes back is the row as it now stands,
// which is not always the row that was passed in - a declaration that loses to
// a later one changes nothing, and saying otherwise would be this node
// reporting a write it did not make.
//
// Origin is where the substitution happens. Declaring a project that is already
// here under a different origin is a project that has moved - it had no
// repository and now has one, or its remote was renamed or transferred - and
// the answer is to append the old origin to the chain rather than to forget it.
// Nothing outside this row is touched by that, which is the whole rule: an
// alias, never a rewrite.
func (d *DB) DeclareProject(ctx context.Context, p *Project) error {
	name, err := cleanProjectName(p.ID)
	if err != nil {
		return err
	}
	p.ID = name
	if p.Name == "" {
		p.Name = name
	}
	if p.Provenance == "" {
		p.Provenance = ProvenanceDeclared
	}
	if p.Origin != "" {
		origin, err := CanonicalOrigin(p.Origin)
		if err != nil {
			return err
		}
		p.Origin = origin
	}

	here, err := d.Project(ctx, name)
	switch {
	case errors.Is(err, ErrNotFound):
		// A project declared with no remote is not a project with no identity:
		// it gets the derived form, which is this node and this name.
		if p.Origin == "" {
			p.Origin = DerivedOrigin(d.node, name)
		}
		p.OriginAt = createdNow()
	case err != nil:
		return err
	default:
		p.Superseded, p.OriginAt = here.Superseded, here.OriginAt
		if p.CreatedBy == "" {
			p.CreatedBy = here.CreatedBy
		}
		if p.Created.IsZero() {
			p.Created = here.Created
		}
		if p.Origin == "" || p.Origin == here.Origin {
			p.Origin = here.Origin
			break
		}
		if here.Origin != "" {
			p.Superseded = append(append([]string(nil), here.Superseded...), here.Origin)
		}
		p.OriginAt = createdNow()
	}
	return d.writeProject(ctx, p)
}

// writeProject is DeclareProject without the name rules or the origin
// bookkeeping, for the paths that take a name the data already carries: the
// back-fill and the observed record.
func (d *DB) writeProject(ctx context.Context, p *Project) error {
	id := p.ID
	if err := d.stamp(&id, &p.HLC, &p.Node); err != nil {
		return err
	}
	if p.Created.IsZero() {
		// Minted here rather than left to the column default, because it is
		// inside the signature - see createdNow.
		p.Created = createdNow()
	}
	if err := d.signProject(ctx, d.sql, p); err != nil {
		return err
	}
	if _, err := upsertProject(ctx, d.sql, p); err != nil {
		return err
	}
	stored, err := d.Project(ctx, p.ID)
	if err != nil {
		return err
	}
	*p = *stored
	return nil
}

// upsertProject is the merge, and it is the only way a row lands in this table:
// last-writer-wins by hlc, tie broken by node name, exactly as an artifact or a
// grant is. A local declaration and a peer's copy of one go through it, so
// there is one rule for what a project row is rather than two.
func upsertProject(ctx context.Context, q execer, p *Project) (int, error) {
	if p.ID == "" {
		return 0, errors.New("store: project with no id")
	}
	res, err := q.ExecContext(ctx,
		`INSERT INTO projects (id, name, created_by, provenance, fixture, origin, superseded,
		                       origin_at, hlc, node, sig, created)
		 VALUES ($1, $2, nullif($3, ''), $4, $5, nullif($6, ''), $7, $8, $9, $10, $11,
		         coalesce($12::timestamptz, now()))
		 ON CONFLICT (id) DO UPDATE SET
		     name = excluded.name, created_by = excluded.created_by,
		     -- A pin is the operator's, and it outlives the row that carries
		     -- it. Everything else about the row still merges last-writer-wins,
		     -- so a peer's later edit lands - but it lands on a row that is
		     -- still pinned here, and the next collision is still refused
		     -- against the origin the operator named rather than against
		     -- whatever the last writer happened to say.
		     provenance = CASE WHEN projects.provenance = 'pinned'
		                       THEN 'pinned' ELSE excluded.provenance END,
		     fixture = excluded.fixture,
		     origin = excluded.origin, superseded = excluded.superseded,
		     origin_at = excluded.origin_at,
		     hlc = excluded.hlc, node = excluded.node, sig = excluded.sig,
		     created = coalesce(excluded.created, projects.created)
		  WHERE coalesce(projects.hlc, 0) < excluded.hlc
		     OR (coalesce(projects.hlc, 0) = excluded.hlc
		         AND coalesce(projects.node, '') < excluded.node)`,
		p.ID, p.Name, p.CreatedBy, p.Provenance, p.Fixture, p.Origin,
		pq.Array(p.Superseded), nullTime(p.OriginAt), p.HLC, p.Node, p.Sig,
		nullTime(p.Created))
	if err != nil {
		return 0, fmt.Errorf("store: declare project %s: %w", p.ID, err)
	}
	return rowsAffected(res), nil
}

// Project reads one registry row by name, without asking who wants it. A
// project's existence is not a secret - every row that names it already says
// the name - and the read that is filtered is the enumeration below.
func (d *DB) Project(ctx context.Context, id string) (*Project, error) {
	p, err := scanProject(d.sql.QueryRowContext(ctx,
		`SELECT `+projectColumns+` FROM projects p WHERE p.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: project %s: %w", id, err)
	}
	return p, nil
}

// ProjectFilterSQL narrows the registry to the projects a principal has any
// business being shown: their own, and the ones on the other end of a live
// grant edge either way.
//
// It is a filter over NAMES and nothing else. It does not decide what anybody
// may read - the artifact and event filters do that, unchanged - and widening
// it would leak nothing but a list of names, which is why scope=all is simply
// the whole table.
//
// scope=all is the whole table for EVERY principal, and not only for this
// node's operator. That is the one place this filter differs from
// ArtifactFilterSQL, where the same word is an operator-only escape hatch,
// because the two are asking different questions. An artifact is a row somebody
// may not read; a project name is not a secret and never was. Project() reads
// one by name for anybody who asks, requireProject refuses a write by name, and
// every artifact, event, task and grant carries the name inside its signature.
// So an operator-only scope=all bought no secrecy - a caller could already probe
// the registry one name at a time - and it cost the thing the enumeration exists
// for: a project that was just declared was invisible to the principal that
// declared it, so a declaration that SUCCEEDED read back exactly like one that
// did nothing, and no console could offer a project it could not list.
//
// Which project a principal may WRITE into is untouched by this: a write lands
// in the token's own project and nowhere else. Which rows they may read is
// untouched too, and the projects response says so in a second list - see
// ReadableProjects, which asks the read rule rather than this one.
//
// The narrow form below is still what sync sends, and it must stay evaluable
// against a bare name: projectReachable applies it to a VALUES row that has an
// id column and nothing more, so a clause here that reaches for another column
// of the table would break the merge rather than fail a test.
//
// The edge is read in both directions on purpose: a project that opened itself
// up to you and a project you opened yourself up to are both projects you are
// already working across, and both already appear on grants you can read.
func ProjectFilterSQL(p *Principal, alias string, a *args, scopeAll bool) string {
	if p == nil {
		return "FALSE"
	}
	if scopeAll {
		return "TRUE"
	}
	project := a.next(p.Project)
	return `(` + alias + `.id = ` + project + ` AND ` + project + ` <> ''
	      OR EXISTS (SELECT 1 FROM grants g
	                  WHERE coalesce(g.tombstone, false) = false
	                    AND (g.from_project = ` + project + ` AND g.to_project = ` + alias + `.id
	                      OR g.to_project = ` + project + ` AND g.from_project = ` + alias + `.id)
	                    AND ` + project + ` <> ''))`
}

// ListProjects is the enumeration: every project this principal may be shown,
// by name. It is what the console dropdown, `flowy projects` and the MCP
// projects tool all read.
func (d *DB) ListProjects(ctx context.Context, p *Principal, scopeAll bool) ([]*Project, error) {
	a := &args{}
	where := ProjectFilterSQL(p, "p", a, scopeAll)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+projectColumns+` FROM projects p WHERE `+where+` ORDER BY p.id`, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	out := []*Project{}
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list projects: %w", err)
		}
		out = append(out, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	return out, nil
}

// ReadableProjects is the other question about the registry, and ListProjects
// does not answer it: which of the projects a principal is SHOWN can they read
// anything IN.
//
// The two differ, and not in a corner. ProjectFilterSQL reads a grant edge in
// both directions on purpose - a project that opened itself to you is one you
// are working across, and its name is worth showing. Reading is one-directional:
// artifactReachSQL leaves the principal's own project along `from_project =
// mine` and never comes back the other way. So a reader in pa, with pb holding a
// grant on pa, is shown pb and can read nothing in it.
//
// A list that spans projects has to say whose union it is, and a scope line
// built on the enumeration would tell that reader they read two projects while
// handing them one project's rows - which is the failure the cross-project list
// exists to prevent rather than one to ship. So this asks the READ rule, and it
// asks the one copy of it: ArtifactFilterSQL, over a hypothetical artifact in
// each project. There is no second wording of the floor here to drift from the
// first.
//
// The probe is a shared artifact because that is the widest visibility: the
// question is whether ANY row in that project could be reachable, and a narrower
// probe would answer about what the project happens to hold instead. The empty
// id is deliberate in the same way - an artifact-level grant reaches one row and
// does not make its project readable, so it must not show up here.
func (d *DB) ReadableProjects(ctx context.Context, p *Principal, scopeAll bool) ([]string, error) {
	a := &args{}
	where := ArtifactFilterSQL(p, "ar", a, scopeAll)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT ar.project
		   FROM (SELECT p.id AS project, '`+VisibilityShared+`'::text AS visibility,
		                ''::text AS owner_user, ''::text AS id
		           FROM projects p) ar
		  WHERE `+where+`
		  ORDER BY ar.project`, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: readable projects: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: readable projects: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: readable projects: %w", err)
	}
	return out, nil
}

// requireProject refuses a local write into a project that was never declared.
//
// It is asked of every local write that carries a project - an artifact, an
// event, a grant's two endpoints, a token's scope, an agent's home - which is
// what makes the registry a referent rather than a list. An empty name is not a
// project and is not checked: a row with no project is the personal floor, and
// it has always been allowed.
//
// It reads the table rather than a cache. The registry replicates, so a cached
// answer would be a node refusing a project a peer declared a second ago, and
// the read is one lookup by primary key.
func requireProject(ctx context.Context, q execer, name string) error {
	if name == "" {
		return nil
	}
	var here bool
	if err := q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1)`, name).Scan(&here); err != nil {
		return fmt.Errorf("store: project %s: %w", name, err)
	}
	if !here {
		return fmt.Errorf("%w: %q was never declared here - declare it first, "+
			"or check the spelling against the projects registry", ErrUndeclaredProject, short(name))
	}
	return nil
}

// requireProjectPtr is requireProject for a nullable column, where nil is the
// personal floor.
func requireProjectPtr(ctx context.Context, q execer, name *string) error {
	if name == nil {
		return nil
	}
	return requireProject(ctx, q, *name)
}

// ObserveProjects records the projects named by rows that arrived from a peer
// and have no registry row here.
//
// The merge does not refuse those rows, and this is the other half of that
// decision. A page can legitimately carry an artifact whose project row the
// puller was never handed - a grant lets a principal read into a project
// without being of it - so refusing would drop replicated data on an ordering
// accident. The row arrived signed, and it was authorised, which means the node
// that wrote it had declared that project: recording the name as `observed`
// keeps the enumeration honest about what this node is now holding, and says in
// the row itself that nobody here declared it.
//
// It never overwrites a row that is already here, whatever its provenance: an
// observation is the weakest thing this table records and must not be able to
// relabel somebody's declaration.
func ObserveProjects(ctx context.Context, tx *sql.Tx, names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, provenance) VALUES ($1, $1, $2)
			 ON CONFLICT (id) DO NOTHING`, name, ProvenanceObserved); err != nil {
			return fmt.Errorf("store: observe project %s: %w", name, err)
		}
	}
	return nil
}

// BackfillProjects makes the registry describe the database it was added to,
// and is run at startup by every command that writes: serve, mcp and fuse.
//
// Three things happen here, and all of them are about a database that predates
// this table:
//
//  1. schema.sql declares the names the data already carries, because the
//     foreign key on tokens cannot be added while a token points at a project
//     with no row. Those rows are written by SQL, so they carry no reading, no
//     node and no signature - they cannot replicate. This adopts them: stamped,
//     dated and signed under this node's key, which is this node saying it
//     stands behind a name it found rather than one it was told.
//  2. anything the SQL missed, or that arrived since, is declared the same way.
//  3. the smoke seeder's fixture names are marked as fixtures, and a row with
//     no origin gets the derived one, so a database seeded before either column
//     existed still says which of its projects are demo data and where each of
//     them came from.
//
// A name that is already stamped, signed, flagged and origined is left alone.
// Adopting it again would raise its reading on every start and hand every peer
// a new winner for a row that did not change - and, worse, an origin this node
// derived would start winning against the remote a peer had recorded.
//
// It returns how many rows it wrote, which is what the startup line reports.
func (d *DB) BackfillProjects(ctx context.Context) (int, error) {
	names, err := d.projectNamesInUse(ctx)
	if err != nil {
		return 0, err
	}
	fixture := map[string]bool{}
	for _, name := range FixtureProjects {
		fixture[name] = true
	}

	written := 0
	for _, name := range names {
		here, err := d.Project(ctx, name)
		switch {
		case errors.Is(err, ErrNotFound):
			here = nil
		case err != nil:
			return written, err
		}
		if here != nil && here.Sig != nil && here.HLC != 0 && here.Origin != "" &&
			here.Fixture == fixture[name] {
			continue
		}
		// An observation is not a declaration, and adopting one would turn it
		// into this node's: it would be signed here, given an origin derived
		// from this node's name, and replicated onward - where it would collide
		// with the real origin from the node that actually declared it. So an
		// observed row stays as it is, local and unsigned, until somebody
		// declares the project or the declaring node's row arrives.
		if here != nil && here.Provenance == ProvenanceObserved {
			continue
		}
		adopted := &Project{ID: name, Name: name, Provenance: ProvenanceBackfill,
			Fixture: fixture[name], Origin: DerivedOrigin(d.node, name),
			OriginAt: createdNow()}
		if here != nil {
			adopted.Name, adopted.CreatedBy = here.Name, here.CreatedBy
			adopted.Provenance, adopted.Created = here.Provenance, here.Created
			adopted.Superseded = here.Superseded
			if here.Origin != "" {
				adopted.Origin, adopted.OriginAt = here.Origin, here.OriginAt
			}
		}
		if err := d.writeProject(ctx, adopted); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// projectNamesInUse is every project name the database already carries, from
// the tables that carry one. It is the same union schema.sql back-fills from,
// and it is here as well because a name can arrive after the schema was loaded
// - a peer's artifact, a token minted by hand - and the registry is meant to
// describe what is here rather than what was here at load time.
//
// The registry itself is in the union, which is not circular: schema.sql writes
// rows there with no reading, no node and no signature, and those are exactly
// the rows the adoption exists to finish. A project that has been declared and
// not yet written into is in the same position.
func (d *DB) projectNamesInUse(ctx context.Context) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT DISTINCT name FROM (
		     SELECT DISTINCT project      AS name FROM artifacts  WHERE project      IS NOT NULL
		     UNION SELECT DISTINCT project        FROM events     WHERE project      IS NOT NULL
		     UNION SELECT DISTINCT project        FROM tasks      WHERE project      IS NOT NULL
		     UNION SELECT DISTINCT project        FROM tokens     WHERE project      IS NOT NULL
		     UNION SELECT DISTINCT project        FROM agents     WHERE project      IS NOT NULL
		     UNION SELECT DISTINCT project        FROM fs_intents WHERE project      IS NOT NULL
		     UNION SELECT DISTINCT from_project   FROM grants     WHERE from_project IS NOT NULL
		     UNION SELECT DISTINCT to_project     FROM grants     WHERE to_project   IS NOT NULL
		     UNION SELECT DISTINCT id             FROM projects
		 ) AS named
		  WHERE name <> ''
		  ORDER BY name`)
	if err != nil {
		// The one failure worth naming rather than passing through, because the
		// raw form of it says nothing an operator can act on: a database that
		// predates this binary has no projects table at all, and `relation
		// "projects" does not exist` reads as a bug in the node rather than as
		// a schema that was never applied. It cost a live node three restart
		// loops before anyone read the message closely. The gate cannot catch
		// it either - the gate builds its database from schema.sql every run,
		// so it has never seen an older one.
		if isUndefinedTable(err) {
			return nil, fmt.Errorf("this database has no projects table, so it is "+
				"older than this binary: apply schema.sql to it and start again "+
				"(underlying error: %w)", err)
		}
		return nil, fmt.Errorf("store: project names in use: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("store: project names in use: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: project names in use: %w", err)
	}
	return out, nil
}

// isUndefinedTable reports whether an error is Postgres saying the relation is
// not there (SQLSTATE 42P01), rather than any of the other reasons a query can
// fail. It is asked in one place - see ProjectNamesInUse - so that a database
// older than the binary reading it gets an answer an operator can act on.
func isUndefinedTable(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "42P01"
}
