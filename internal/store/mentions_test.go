package store

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// Resolving a name into a principal, against the registry. These need the
// database the gate stands up; without DATABASE_URL they sit out - see open().

// A name means the one principal that answers to it, whatever case it was
// written in, and a name nobody answers to means nobody rather than an error.
func TestANameResolvesToTheOnePrincipalItNames(t *testing.T) {
	ctx, db := open(t)
	// A handle nothing else could hold: this runs against whatever rows earlier
	// runs left behind, and a name that collided with one of them would resolve
	// to two principals and read as the ambiguity case below.
	handle := "mentioned-" + ulid.NewString()
	u := &User{Handle: handle, Display: "mentioned"}
	if err := db.InsertUser(ctx, u); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	found, err := db.PrincipalsNamed(ctx, []string{handle})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if found[strings.ToLower(handle)] != u.ID {
		t.Fatalf("%q resolved to %q, want the user %q",
			handle, found[strings.ToLower(handle)], u.ID)
	}

	// The same name shouted at the start of a sentence is the same person. A
	// reader who has to remember which case they typed is a colder version of
	// no feature at all.
	found, err = db.PrincipalsNamed(ctx, []string{strings.ToUpper(handle)})
	if err != nil {
		t.Fatalf("resolve the same name in upper case: %v", err)
	}
	if found[strings.ToLower(handle)] != u.ID {
		t.Fatalf("@%s did not reach %s", strings.ToUpper(handle), handle)
	}

	found, err = db.PrincipalsNamed(ctx, []string{"nobody-" + ulid.NewString()})
	if err != nil {
		t.Fatalf("resolve a name nobody answers to: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("a name nobody answers to resolved to %v", found)
	}
}

// An agent answers to its runtime kind exactly when the room would have drawn
// it under that kind - which is when the person it acts for has no handle to
// lend it, the fallback speakerName takes, inverted.
//
// And when two of them answer to one name it resolves to NEITHER. Picking one
// of two agents called claude would address the wrong one silently, which is
// the failure the addressee field exists to prevent rather than to cause.
func TestAnAmbiguousNameResolvesToNobody(t *testing.T) {
	ctx, db := open(t)
	// Written here rather than through InsertUser because the handle has to be
	// NULL and not empty: the column is UNIQUE, so the first '' ever inserted
	// is the only one the database will hold, and a test that minted one would
	// pass once and fail on every run against that database afterwards.
	nameless := ulid.NewString()
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO users (id, display) VALUES ($1, $2)`,
		nameless, "no handle to lend"); err != nil {
		t.Fatalf("insert the handle-less user: %v", err)
	}

	kind := "runtime-" + ulid.NewString()
	first := &Agent{UserID: nameless, Kind: kind}
	if err := db.InsertAgent(ctx, first); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	found, err := db.PrincipalsNamed(ctx, []string{kind})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if found[strings.ToLower(kind)] != first.ID {
		t.Fatalf("%q resolved to %q, want the agent %q",
			kind, found[strings.ToLower(kind)], first.ID)
	}

	second := &Agent{UserID: nameless, Kind: kind}
	if err := db.InsertAgent(ctx, second); err != nil {
		t.Fatalf("insert the second agent of the same kind: %v", err)
	}
	found, err = db.PrincipalsNamed(ctx, []string{kind})
	if err != nil {
		t.Fatalf("resolve after the second: %v", err)
	}
	if id, found := found[strings.ToLower(kind)]; found {
		t.Fatalf("a name two agents answer to picked %q instead of nobody", id)
	}
}

// An agent whose person HAS a handle is not called by its kind, because that is
// not what the room calls it: it speaks under the handle, and the handle
// reaches it - a waiter wakes for either half of its own principal, so
// addressing the person addresses the agent working for them.
func TestAnAgentWhosePersonHasAHandleIsNamedByTheHandle(t *testing.T) {
	ctx, db := open(t)
	handle := "named-" + ulid.NewString()
	u := &User{Handle: handle, Display: "named"}
	if err := db.InsertUser(ctx, u); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	kind := "runtime-" + ulid.NewString()
	if err := db.InsertAgent(ctx, &Agent{UserID: u.ID, Kind: kind}); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	found, err := db.PrincipalsNamed(ctx, []string{kind, handle})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id, ok := found[strings.ToLower(kind)]; ok {
		t.Fatalf("the agent answered to its kind %q as %q, which is not what the room calls it",
			kind, id)
	}
	if found[strings.ToLower(handle)] != u.ID {
		t.Fatalf("the handle did not reach the person: %v", found)
	}
}

// A ROLE IS A NAME PEOPLE ALREADY USE, and on this node it is the one they use
// most: four seats write @operator in the room every day.
//
// MEASURED 2026-08-21, one seeded message: "@operator and @deadtrickster and
// @orchestrator" came back resolved as deadtrickster and orchestrator, and
// @operator resolved to nobody. So the operator's report - "@operator is not
// highlighted in the chat" - was literally true, and none of the three causes
// on 01M0GGSM99 were it: the ring works, the reader is known, the name simply
// was not a name here.
//
// THE TEST COUNTS FIRST. The role lives on users.role, which is global to the
// node rather than scoped to a project, so an operator another test left behind
// would make this one measure ambiguity while claiming to measure resolution.
// It refuses on that instead of passing, and it puts both of its own back -
// otherwise it is a fixture that passes once per database, which is the defect
// 01M0HJ1M25 was.
func TestAMentionOfTheRoleResolvesToTheOneWhoHoldsIt(t *testing.T) {
	ctx, db := open(t)

	var already int
	if err := db.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE role = $1`, RoleOperator).Scan(&already); err != nil {
		t.Fatalf("count operators: %v", err)
	}
	if already != 0 {
		t.Fatalf("this database already holds %d operator(s), so a mention of the role "+
			"is ambiguous before this test does anything - it would measure the wrong arm", already)
	}

	one := &User{ID: "u-" + ulid.NewString(), Handle: "roleone-" + ulid.NewString()}
	two := &User{ID: "u-" + ulid.NewString(), Handle: "roletwo-" + ulid.NewString()}
	for _, u := range []*User{one, two} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	demote := func(u *User) {
		if err := db.SetRole(ctx, nil, u.ID, RoleUser); err != nil {
			t.Errorf("putting %s back to %s: %v", u.Handle, RoleUser, err)
		}
	}
	t.Cleanup(func() { demote(one); demote(two) })

	if err := db.SetRole(ctx, nil, one.ID, RoleOperator); err != nil {
		t.Fatalf("make an operator: %v", err)
	}

	got, err := db.PrincipalsNamed(ctx, []string{RoleOperator})
	if err != nil {
		t.Fatalf("resolve the role: %v", err)
	}
	if got[RoleOperator] != one.ID {
		t.Fatalf("@%s resolved to %q, want the one operator %s", RoleOperator, got[RoleOperator], one.ID)
	}

	// AND A SECOND HOLDER MAKES IT NOBODY, which is this file's existing rule
	// rather than a new one - a name two principals answer to is not a guess
	// between them. It matters more here than for a handle: handles are unique
	// in the schema, so the role is the FIRST name that can genuinely be shared.
	if err := db.SetRole(ctx, nil, two.ID, RoleOperator); err != nil {
		t.Fatalf("make a second operator: %v", err)
	}
	got, err = db.PrincipalsNamed(ctx, []string{RoleOperator})
	if err != nil {
		t.Fatalf("resolve the role with two holders: %v", err)
	}
	if id, found := got[RoleOperator]; found {
		t.Fatalf("@%s resolved to %s while two people hold the role", RoleOperator, id)
	}

	// A ROLE THE STORE DOES NOT DEFINE IS NOT A NAME. The arm is narrowed to
	// RoleOperator on purpose: without that, writing any word into users.role
	// would mint a mention, and the naming scheme would be whatever somebody
	// last typed into a column.
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE users SET role = 'archivist' WHERE id = $1`, two.ID); err != nil {
		t.Fatalf("write an undefined role: %v", err)
	}
	got, err = db.PrincipalsNamed(ctx, []string{"archivist"})
	if err != nil {
		t.Fatalf("resolve an undefined role: %v", err)
	}
	if id, found := got["archivist"]; found {
		t.Fatalf("a word in the role column became a mention, resolving to %s", id)
	}
}
