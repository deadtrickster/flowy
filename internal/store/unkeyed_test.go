package store

import (
	"context"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestAPrincipalWithNoKeyIsNamed is the standing half of the pin finding
// (01M0AG9HVG), which no accept rule can close: a principal this node holds no
// key for is a name a pinned peer may author rows under, and until now this
// node knew every such name and said nothing.
//
// The measurement is a DELTA, both arms, because a list is only worth anything
// if it also stops naming somebody once they are provisioned:
//
//	with rows here and no key -> named, with the command that closes it
//	with a key                -> gone, because there is nothing left to fix
func TestAPrincipalWithNoKeyIsNamed(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "unkeyed")
	alice := "u-alice-" + ulid.NewString()
	if err := db.InsertUser(ctx, &User{ID: alice, Handle: "alice-" + alice[8:16]}); err != nil {
		t.Fatalf("seed the user: %v", err)
	}
	at := packed(t, db)
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	row := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "the row a peer relayed", SeqHLC: at + 1, Node: "peer-node"}
	if res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{row}}); res.Applied["events"] != 1 {
		t.Fatalf("the relayed row was refused: %+v", res.Reasons)
	}

	found, ok := exposedEntry(t, ctx, db, alice)
	if !ok {
		t.Fatal("a principal with rows here and no key is not named, so an operator " +
			"cannot provision what this node already knows is open")
	}
	if found.Rows < 1 {
		t.Errorf("named with %d rows, want at least the one that was just pushed", found.Rows)
	}
	if found.Credentialed {
		t.Errorf("%s reads as credentialed here, and holds no token on this node", alice)
	}
	if want := "flowy principal pin --as " + alice; len(found.Fix()) == 0 ||
		found.Fix()[:len(want)] != want {
		t.Errorf("the fix offered is %q - a principal who does not write here needs "+
			"the key from where they do, pinned", found.Fix())
	}

	principalKey(t, ctx, db, alice, at)
	if _, ok := exposedEntry(t, ctx, db, alice); ok {
		t.Error("a principal who now has a key is still named as exposed - a list " +
			"that never shrinks is one nobody works through")
	}
}

func exposedEntry(t *testing.T, ctx context.Context, db *DB, principal string) (UnkeyedPrincipal, bool) {
	t.Helper()
	open, err := db.UnkeyedPrincipals(ctx)
	if err != nil {
		t.Fatalf("UnkeyedPrincipals: %v", err)
	}
	for _, u := range open {
		if u.Principal == principal {
			return u, true
		}
	}
	return UnkeyedPrincipal{}, false
}
