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
