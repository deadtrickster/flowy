package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// THE BOOTSTRAP BECOMES A ROW, ONCE, AND NEVER OVERRIDES ONE.
//
// $FLOWY_OPERATOR answered the auth question and nothing else, so on a node
// with a working operator the store held no operator at all - and a mention of
// @operator, which resolves through users.role, found nobody. Two halves of one
// fact in two files, which is the shape this fleet keeps finding.
//
// Both arms matter and the second more than the first: a stale environment
// variable that could put a REMOVED operator back at the next restart would be
// a privilege grant by config file, which is exactly what SetRole exists to
// prevent.
func TestTheBootstrapOperatorIsAdoptedOnceAndNeverOverrides(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; run ./run-tests.sh for the live checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	db, err := store.Open(ctx, dsn, "test-node")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// The role is global to the node, so this measures nothing if somebody else
	// already holds it - it says so rather than passing on the wrong arm.
	held, err := db.UsersWithRole(ctx, store.RoleOperator)
	if err != nil {
		t.Fatalf("who holds the role: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("this database already holds %d operator(s), so adoption cannot be measured", len(held))
	}

	first := &store.User{ID: "u-" + ulid.NewString(), Handle: "bootop-" + ulid.NewString()}
	second := &store.User{ID: "u-" + ulid.NewString(), Handle: "laterop-" + ulid.NewString()}
	for _, u := range []*store.User{first, second} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, u := range []*store.User{first, second} {
			if err := db.SetRole(ctx, nil, u.ID, store.RoleUser); err != nil {
				t.Errorf("putting %s back to %s: %v", u.Handle, store.RoleUser, err)
			}
		}
	})

	adoptBootstrapOperator(ctx, db, first.ID)
	got, err := db.UsersWithRole(ctx, store.RoleOperator)
	if err != nil {
		t.Fatalf("who holds the role after adoption: %v", err)
	}
	if len(got) != 1 || got[0] != first.ID {
		t.Fatalf("after adoption the role is held by %v, want exactly [%s]", got, first.ID)
	}

	// A SECOND BOOT NAMING SOMEBODY ELSE CHANGES NOTHING. This is the arm that
	// would fail on the obvious implementation - write the flag's user every
	// time - and it is the one that matters, because that version hands the
	// role back to whoever the environment last named.
	adoptBootstrapOperator(ctx, db, second.ID)
	got, err = db.UsersWithRole(ctx, store.RoleOperator)
	if err != nil {
		t.Fatalf("who holds the role after a second boot: %v", err)
	}
	if len(got) != 1 || got[0] != first.ID {
		t.Fatalf("a second boot moved the role to %v, want it left at [%s]", got, first.ID)
	}

	// AND AN EMPTY FLAG IS NOT A USER. A node with no FLOWY_OPERATOR must not
	// write a role for the empty id, which would be a row nobody can hold.
	adoptBootstrapOperator(ctx, db, "")
	got, err = db.UsersWithRole(ctx, store.RoleOperator)
	if err != nil {
		t.Fatalf("who holds the role after an empty flag: %v", err)
	}
	if len(got) != 1 || got[0] != first.ID {
		t.Fatalf("an empty operator flag changed the holders to %v", got)
	}
}
