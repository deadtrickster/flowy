package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestCanonicalOriginIsOneStringPerRepository is the identity claim, in the one
// place it can be tested without a database: three spellings of one remote are
// one origin, and two remotes are two.
func TestCanonicalOriginIsOneStringPerRepository(t *testing.T) {
	same := []string{
		"git@github.com:deadtrickster/flowy.git",
		"https://github.com/deadtrickster/flowy",
		"https://github.com/deadtrickster/flowy.git",
		"https://github.com/deadtrickster/flowy/",
		"ssh://git@github.com/deadtrickster/flowy.git",
		"ssh://git@github.com:2222/deadtrickster/flowy.git",
		"https://someone:token@github.com/deadtrickster/flowy.git",
		"git://github.com/DeadTrickster/Flowy.git",
		"git:github.com/deadtrickster/flowy",
	}
	want := "git:github.com/deadtrickster/flowy"
	for _, spelling := range same {
		got, err := CanonicalOrigin(spelling)
		if err != nil {
			t.Fatalf("canonicalise %q: %v", spelling, err)
		}
		if got != want {
			t.Fatalf("%q canonicalised to %q, want %q", spelling, got, want)
		}
	}

	other, err := CanonicalOrigin("git@gitlab.com:deadtrickster/flowy.git")
	if err != nil {
		t.Fatalf("canonicalise the other host: %v", err)
	}
	if other == want {
		t.Fatalf("two hosts canonicalised to one origin: %q", other)
	}
	if _, err := CanonicalOrigin("   "); !errors.Is(err, ErrBadProjectName) {
		t.Fatalf("an empty origin was accepted: %v", err)
	}
}

// TestAProjectWithNoRepositoryIsNotSecondClass: the derived case has to work as
// well as the git one, because plenty of projects are not repositories.
func TestAProjectWithNoRepositoryIsNotSecondClass(t *testing.T) {
	ctx, db := open(t)

	name := "pnorepo-" + ulid.NewString()
	p := &Project{ID: name}
	if err := db.DeclareProject(ctx, p); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if !strings.HasPrefix(p.Origin, OriginDerived) {
		t.Fatalf("origin is %q, want a derived one", p.Origin)
	}
	if p.Sig == nil || p.HLC == 0 || p.Node != "test-node" {
		t.Fatalf("the row was not stamped and signed: %+v", p)
	}
	// And it is writable, which is the whole of "not second class".
	art := &Artifact{Type: "note", Project: &name, OwnerUser: "u-" + ulid.NewString(), Title: "t"}
	if err := db.InsertArtifact(ctx, art); err != nil {
		t.Fatalf("write into a project with no repository: %v", err)
	}
}

// TestAWriteIntoAnUndeclaredProjectIsRefused is the referent claim: a project
// that was never declared is not a valid target, so a typo cannot become one.
func TestAWriteIntoAnUndeclaredProjectIsRefused(t *testing.T) {
	ctx, db := open(t)

	declared := declaredProject(t, ctx, db, "pdecl")
	typo := declared + "x"

	art := &Artifact{Type: "note", Project: &typo, OwnerUser: "u-" + ulid.NewString(), Title: "t"}
	if err := db.InsertArtifact(ctx, art); !errors.Is(err, ErrUndeclaredProject) {
		t.Fatalf("a write into %s was answered with %v, want ErrUndeclaredProject", typo, err)
	}
	if err := db.AppendEvent(ctx, &Event{Type: "chat", Project: &typo, Room: "general"}); !errors.Is(
		err, ErrUndeclaredProject) {
		t.Fatalf("an event in %s was answered with %v, want ErrUndeclaredProject", typo, err)
	}
	if err := db.InsertGrant(ctx, &Grant{FromProject: typo, ToProject: declared}); !errors.Is(
		err, ErrUndeclaredProject) {
		t.Fatalf("a grant out of %s was answered with %v, want ErrUndeclaredProject", typo, err)
	}
	if err := db.InsertToken(ctx, &Principal{
		Token: "tt-" + ulid.NewString(), UserID: "u", Project: typo,
	}); !errors.Is(err, ErrUndeclaredProject) {
		t.Fatalf("a token scoped to %s was answered with %v, want ErrUndeclaredProject", typo, err)
	}

	// The declared one still works, or the check is refusing everything.
	art.Project = &declared
	if err := db.InsertArtifact(ctx, art); err != nil {
		t.Fatalf("a write into the declared project: %v", err)
	}
}

// TestDeclaringIsIdempotentAndSubstitutionIsAnAlias covers the two halves of
// identity: declaring the same name twice is one project, and giving a project
// a repository it did not have keeps the old identity in the chain rather than
// rewriting anything.
func TestDeclaringIsIdempotentAndSubstitutionIsAnAlias(t *testing.T) {
	ctx, db := open(t)

	name := "palias-" + ulid.NewString()
	first := &Project{ID: name}
	if err := db.DeclareProject(ctx, first); err != nil {
		t.Fatalf("declare: %v", err)
	}
	derived := first.Origin

	// An artifact written before the substitution: it names the project, and
	// what it names must not move.
	art := &Artifact{Type: "note", Project: &name, OwnerUser: "u-" + ulid.NewString(), Title: "before"}
	if err := db.InsertArtifact(ctx, art); err != nil {
		t.Fatalf("insert: %v", err)
	}
	sig := append([]byte(nil), art.Sig...)

	moved := &Project{ID: name, Origin: "git@github.com:acme/" + name + ".git"}
	if err := db.DeclareProject(ctx, moved); err != nil {
		t.Fatalf("substitute the origin: %v", err)
	}
	if moved.Origin == derived {
		t.Fatal("the substitution did not take")
	}
	if len(moved.Superseded) != 1 || moved.Superseded[0] != derived {
		t.Fatalf("the chain is %v, want it to end at %s", moved.Superseded, derived)
	}
	if moved.OriginAt.IsZero() {
		t.Fatal("a substitution with no date")
	}

	// The row that named the project is untouched, signature and all. This is
	// the rule the pa migration is named after: substitution is an alias, never
	// a rewrite, because project is inside the signed payload.
	after, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.Project == nil || *after.Project != name {
		t.Fatalf("the artifact's project moved to %v", after.Project)
	}
	if string(after.Sig) != string(sig) {
		t.Fatal("the artifact's signature changed under a project substitution")
	}

	// One row, not two: there is one project called this.
	only, err := db.ListProjects(ctx, &Principal{UserID: "u", Project: name}, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(only) != 1 || only[0].ID != name {
		t.Fatalf("the registry holds %d rows for %s", len(only), name)
	}
}

// TestTwoProjectsWithOneNameAreNotMerged is the collision rule. Two nodes each
// declare `flowy` with no contact; whether they are one project is decided by
// the origin, and neither answer is a silent merge.
func TestTwoProjectsWithOneNameAreNotMerged(t *testing.T) {
	name := "pcollide-" + ulid.NewString()
	remote := &Project{ID: name, Origin: "git:github.com/acme/thing"}

	sameRepo := &Project{ID: name, Origin: "git:github.com/acme/thing"}
	if same, why := SameProject(remote, sameRepo); !same {
		t.Fatalf("one repository on both sides was called a collision: %s", why)
	}

	otherRepo := &Project{ID: name, Origin: "git:github.com/other/thing"}
	same, why := SameProject(remote, otherRepo)
	if same {
		t.Fatal("two different remotes under one name were judged the same project")
	}
	if !strings.Contains(why, name) || !strings.Contains(why, "two projects with one name") {
		t.Fatalf("the refusal does not say what happened: %q", why)
	}

	// A moved remote is a substitution, not a new project: the far side is
	// still holding the origin this one superseded, and the chains meet there.
	movedOn := &Project{
		ID: name, Origin: "git:gitlab.com/acme/thing",
		Superseded: []string{"git:github.com/acme/thing"},
	}
	if same, why := SameProject(remote, movedOn); !same {
		t.Fatalf("a moved remote was called a collision: %s", why)
	}

	// A row from before origins were recorded says nothing about where it came
	// from, and inventing a collision out of an empty column would refuse
	// federation with every node running an older build.
	if same, _ := SameProject(&Project{ID: name}, otherRepo); !same {
		t.Fatal("a row with no origin was treated as a collision")
	}
}

// TestBackfillAdoptsWhatTheDataAlreadyNames is the migration claim: rows written
// before the registry existed stay valid, and the registry describes them.
// Nothing rewrites a project column to make that true.
func TestBackfillAdoptsWhatTheDataAlreadyNames(t *testing.T) {
	ctx, db := open(t)

	// A project named by a row and nothing else, put there the way schema.sql's
	// back-fill puts one: no reading, no node, no signature.
	name := "porphan-" + ulid.NewString()
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO projects (id, name, provenance) VALUES ($1, $1, $2)`,
		name, ProvenanceBackfill); err != nil {
		t.Fatalf("seed the unsigned row: %v", err)
	}

	written, err := db.BackfillProjects(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if written == 0 {
		t.Fatal("the back-fill adopted nothing at all")
	}
	adopted, err := db.Project(ctx, name)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if adopted.Sig == nil || adopted.HLC == 0 || adopted.Node != "test-node" {
		t.Fatalf("the row was not adopted: %+v", adopted)
	}
	if adopted.Origin == "" {
		t.Fatal("the adopted row has no origin, so no collision could ever be decided")
	}

	// And it is stable: a second run must not raise the reading on a row that
	// did not change, or every start would hand every peer a new winner.
	before := adopted.HLC
	if _, err := db.BackfillProjects(ctx); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	again, err := db.Project(ctx, name)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if again.HLC != before {
		t.Fatalf("the second back-fill re-stamped the row: %d then %d", before, again.HLC)
	}
}

// TestTheFixtureFlagIsVisibleAndRefusesNothing states precisely what the flag
// buys: it does not stop a write, it makes one sayable.
func TestTheFixtureFlagIsVisibleAndRefusesNothing(t *testing.T) {
	ctx, db := open(t)

	name := "pfix-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &Project{ID: name, Fixture: true}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	art := &Artifact{Type: "note", Project: &name, OwnerUser: "u-" + ulid.NewString(), Title: "real"}
	if err := db.InsertArtifact(ctx, art); err != nil {
		t.Fatalf("a write into a fixture project was refused: %v", err)
	}
	held, err := db.Project(ctx, name)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !held.Fixture {
		t.Fatal("the fixture flag did not survive the write")
	}
}

// TestTheRegistryIsNotAPermissionSystem is the hard line: what a principal may
// read does not change when a project is declared, and the enumeration is a
// list of names rather than a way into anybody's rows.
func TestTheRegistryIsNotAPermissionSystem(t *testing.T) {
	ctx, db := open(t)

	mine := declaredProject(t, ctx, db, "pmine")
	theirs := declaredProject(t, ctx, db, "ptheirs")

	owner := &User{Handle: "owner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	art := &Artifact{Type: "note", Project: &theirs, OwnerUser: owner.ID, Title: "not yours",
		Visibility: VisibilityProject}
	if err := db.InsertArtifact(ctx, art); err != nil {
		t.Fatalf("insert: %v", err)
	}

	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: mine}
	if _, err := db.ReadArtifact(ctx, stranger, art.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("declaring a project changed what a stranger may read: %v", err)
	}

	// And the enumeration shows the stranger their own project and not the
	// other one, because there is no grant between them.
	list, err := db.ListProjects(ctx, stranger, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, project := range list {
		if project.ID == theirs {
			t.Fatal("the enumeration listed a project with no edge to the caller")
		}
	}

	// A grant is what puts it in the list - the same rule as everywhere else,
	// and no new one.
	if err := db.InsertGrant(ctx, &Grant{
		FromProject: mine, ToProject: theirs, GrantedBy: owner.ID,
	}); err != nil {
		t.Fatalf("insert grant: %v", err)
	}
	list, err = db.ListProjects(ctx, stranger, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := false
	for _, project := range list {
		seen = seen || project.ID == theirs
	}
	if !seen {
		t.Fatal("a live grant edge did not put the project in the enumeration")
	}
	// The grant put the NAME in the list. It did not put the row in reach of
	// anything it was not already in reach of: reading the artifact still goes
	// through the artifact filter, which the grant also opened - that is the
	// existing rule and it is the only one that ran.
	if _, err := db.ReadArtifact(ctx, stranger, art.ID, false); err != nil {
		t.Fatalf("the grant did not open the artifact: %v", err)
	}
}

// TestAProjectNameIsCheckedBeforeItIsDeclared keeps the names usable: a project
// is a directory in the FUSE mount and a word on a status line.
func TestAProjectNameIsCheckedBeforeItIsDeclared(t *testing.T) {
	ctx, db := open(t)

	for _, bad := range []string{"", "   ", "a/b", "a\\b", "a\tb", strings.Repeat("p", 65)} {
		err := db.DeclareProject(ctx, &Project{ID: bad})
		if !errors.Is(err, ErrBadProjectName) {
			t.Fatalf("declaring %q was answered with %v, want ErrBadProjectName", bad, err)
		}
	}
	// A name the data already carries is not put through those rules: the
	// registry adapts to the data rather than telling it it is wrong.
	odd := "odd name-" + ulid.NewString()
	if err := ObserveProjectsForTest(ctx, db, odd); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := db.Project(ctx, odd); err != nil {
		t.Fatalf("an observed name was not recorded: %v", err)
	}
}

// ObserveProjectsForTest runs the merge's observed-name path against the pool,
// so a test can assert it without assembling a delta.
func ObserveProjectsForTest(ctx context.Context, db *DB, names ...string) error {
	tx, err := db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back only when Commit did not happen
	if err := ObserveProjects(ctx, tx, names); err != nil {
		return err
	}
	return tx.Commit()
}
