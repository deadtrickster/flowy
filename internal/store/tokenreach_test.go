package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A TOKEN NAMES ITS PROJECTS, PLURAL, and reach takes a set.
//
// The store has been multi-project since the beginning - every row carries a
// project, the registry declares them, grants cross them, and five projects
// hold rows on the live node. The credential was the single-valued half:
// Principal.Project, one string, so a seat could act in exactly one.
//
// This is the read half. Two projects, one credential, and the rows of both
// come back through the same filter that refused them before.
func TestATokenReachesEveryProjectItNames(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "reachhere")
	there := declaredProject(t, ctx, db, "reachthere")
	elsewhere := declaredProject(t, ctx, db, "reachelse")

	author := &Principal{UserID: "u-" + ulid.NewString(), Project: here}
	otherAuthor := &Principal{UserID: "u-" + ulid.NewString(), Project: there}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: elsewhere}

	mine := todoIn(t, ctx, db, author, "the row in the project I act in", VisibilityProjectOnly, "")
	yours := todoIn(t, ctx, db, otherAuthor, "the row in the project I only read",
		VisibilityProjectOnly, "")
	theirs := todoIn(t, ctx, db, stranger, "the row in neither", VisibilityProjectOnly, "")

	// The credential as every token on every node has it today: one project,
	// and no set. It reaches what it always reached and nothing else.
	single := &Principal{UserID: author.UserID, Project: here}
	if got := single.Reach(); len(got) != 1 || got[0] != here {
		t.Fatalf("a one-project credential reaches %v", got)
	}
	if _, err := db.ReadArtifact(ctx, single, yours.ID, false); err == nil {
		t.Fatal("a credential naming one project read a row in another")
	}

	// The same seat, with the second project on its ceiling.
	both := &Principal{UserID: author.UserID, Project: here, Projects: []string{there}}
	for _, want := range []*Artifact{mine, yours} {
		if _, err := db.ReadArtifact(ctx, both, want.ID, false); err != nil {
			t.Fatalf("a credential naming both projects could not read %s: %v", want.ID, err)
		}
	}
	// And the ceiling is a ceiling. A project it does not name is still refused,
	// which is the half that makes the rest of it worth anything.
	if _, err := db.ReadArtifact(ctx, both, theirs.ID, false); err == nil {
		t.Fatal("a credential read a row in a project it does not name")
	}

	// THE LIST DOOR AGREES WITH THE SINGLE-ROW READ. These are two filters -
	// artifactReachSQL through ArtifactFilterSQL, and the same rule again in Go
	// through CanRead - and the head of artifactReachSQL is about what happens
	// when two read surfaces drift.
	seen := map[string]bool{}
	rows, err := db.ListArtifacts(ctx, both, ArtifactQuery{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range rows {
		seen[row.ID] = true
	}
	if !seen[mine.ID] || !seen[yours.ID] {
		t.Fatalf("the list door dropped a row the read door allowed: %v", seen)
	}
	if seen[theirs.ID] {
		t.Fatal("the list door handed over a row the read door refused")
	}

	// CanRead, the Go half, on the same three rows.
	for _, c := range []struct {
		art  *Artifact
		want bool
	}{{mine, true}, {yours, true}, {theirs, false}} {
		if got := CanRead(both, c.art, nil); got != c.want {
			t.Fatalf("CanRead(%s) = %v, want %v - the Go rule and the SQL rule disagree",
				c.art.ID, got, c.want)
		}
	}
}

// REACH FOLDS THE ACTING PROJECT IN, always, and says each project once.
//
// A credential that could write into a project it cannot read would file work
// it can never see again, so the acting project is not optional in the set. The
// deduplication is not tidiness either: the set becomes a text[] in every read
// query, and a repeated element is a repeated comparison on every row.
func TestReachIsTheActingProjectAndTheCeiling(t *testing.T) {
	for _, c := range []struct {
		name string
		p    *Principal
		want []string
	}{
		{"one project, no set", &Principal{Project: "pa"}, []string{"pa"}},
		{"a set that repeats the acting project",
			&Principal{Project: "pa", Projects: []string{"pa", "pb"}}, []string{"pa", "pb"}},
		{"a set with an empty in it",
			&Principal{Project: "pa", Projects: []string{"", "pb"}}, []string{"pa", "pb"}},
		{"no project at all", &Principal{}, []string{}},
		{"a ceiling with no acting project - still readable, still not writable anywhere",
			&Principal{Projects: []string{"pb"}}, []string{"pb"}},
	} {
		got := c.p.Reach()
		if len(got) != len(c.want) {
			t.Fatalf("%s: reach %v, want %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: reach %v, want %v", c.name, got, c.want)
			}
		}
	}
	// The ceiling test the acting project is chosen against.
	p := &Principal{Project: "pa", Projects: []string{"pb"}}
	for _, c := range []struct {
		project string
		want    bool
	}{{"pa", true}, {"pb", true}, {"pc", false}, {"", false}} {
		if got := p.CanReachProject(c.project); got != c.want {
			t.Fatalf("CanReachProject(%q) = %v, want %v", c.project, got, c.want)
		}
	}
}

// A MINT THAT SAYS NOTHING ABOUT REACH TOUCHES NOTHING.
//
// nil is unstated and an empty-but-present set is "reaches nothing extra",
// which is memWriteArgs' rule for its pointer fields one type along: a caller
// that says nothing about a field must not move it.
//
// MEASURED, and it is why this test exists rather than the rule being obvious:
// the first version of InsertToken cleared token_projects unconditionally, and
// the gate's upgrade section - which seeds principals into a database one commit
// old, on purpose - failed with `pq: relation "token_projects" does not exist`.
// A write path that reaches for a table the database may not have yet is the
// outage that whole section was written after.
func TestAMintThatSaysNothingAboutReachLeavesItAlone(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "minthere")
	there := declaredProject(t, ctx, db, "mintthere")
	token := "t-" + ulid.NewString()
	user := "u-" + ulid.NewString()

	// Minted with a set.
	if err := db.InsertToken(ctx, &Principal{
		Token: token, UserID: user, Project: here, Projects: []string{there},
	}); err != nil {
		t.Fatalf("mint with a set: %v", err)
	}
	back, err := db.PrincipalForToken(ctx, token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(back.Reach()) != 2 {
		t.Fatalf("a token minted with two projects reaches %v", back.Reach())
	}

	// Re-minted saying NOTHING about reach. The set stands.
	if err := db.InsertToken(ctx, &Principal{Token: token, UserID: user, Project: here}); err != nil {
		t.Fatalf("re-mint with no set: %v", err)
	}
	if back, err = db.PrincipalForToken(ctx, token); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(back.Reach()) != 2 {
		t.Fatalf("a mint that said nothing about reach changed it to %v", back.Reach())
	}

	// Re-minted STATING an empty set. That is a statement, and it narrows.
	if err := db.InsertToken(ctx, &Principal{
		Token: token, UserID: user, Project: here, Projects: []string{},
	}); err != nil {
		t.Fatalf("re-mint with an empty set: %v", err)
	}
	if back, err = db.PrincipalForToken(ctx, token); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := back.Reach(); len(got) != 1 || got[0] != here {
		t.Fatalf("stating an empty set left the token reaching %v", got)
	}
}

// A MINT NAMING A PROJECT NOBODY DECLARED IS REFUSED, on the reach half as well
// as on the acting half.
//
// The reach half is the one that decides what a credential can SEE, so a name
// nobody declared there is access granted to a project that does not exist. It
// is checked before anything is written, so a refusal leaves no token behind.
func TestAMintIsRefusedForAnUndeclaredProjectInItsReach(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "mintguard")
	token := "t-" + ulid.NewString()

	err := db.InsertToken(ctx, &Principal{
		Token: token, UserID: "u-" + ulid.NewString(), Project: here,
		Projects: []string{"no-such-project-" + ulid.NewString()},
	})
	if err == nil {
		t.Fatal("a token was minted reaching a project nobody declared")
	}
	if _, err := db.PrincipalForToken(ctx, token); err == nil {
		t.Fatal("the refused mint left a token behind")
	}
}
