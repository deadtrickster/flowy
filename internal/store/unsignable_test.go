package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestAWriteThisNodeCannotSignIsRefusedAtTheDoor is @orchestrator's caveat on
// the twelve keygens, made into a rule.
//
// A node may hold only the PUBLIC half of a principal's key - that is what
// `flowy principal pin` is for, and it is how a second machine learns whose
// word a row is. Before this, a local write by that principal found no private
// key, signed nothing, and landed marked attributed, looking exactly like every
// other row here. Every peer holding that key then refuses it, at a relay the
// writer never watches, minutes or days later.
//
// Three arms, because the refusal must not be wider than the failure:
//
//	public half only  -> refused here, naming both ways out
//	no key at all     -> written, attributed, exactly as before
//	private half here -> written and AUTHORED, which is the whole point of a key
func TestAWriteThisNodeCannotSignIsRefusedAtTheDoor(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "unsign")

	// A principal this node knows only by their public key: keygen elsewhere,
	// pin here, which is exactly the two-machine shape.
	elsewhere := "u-elsewhere-" + ulid.NewString()
	priv := principalKey(t, ctx, db, elsewhere+"-source", 0)
	if err := db.PinPrincipalKey(ctx, elsewhere, PrincipalPublicKey(priv), 0); err != nil {
		t.Fatalf("pin their public key: %v", err)
	}

	err := db.CreateArtifact(ctx, &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "note", Project: &project,
		OwnerUser: elsewhere, Title: "written where the key is not", Visibility: "project",
	})
	if err == nil {
		t.Fatal("a row this node cannot sign was written - it lands here and every peer " +
			"holding that key refuses it, which the writer never sees")
	}
	var refusal DepRefusal
	if !errors.As(err, &refusal) {
		t.Errorf("refused as %v, not as something the caller can act on", err)
	}
	for _, want := range []string{"private", "keygen"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q, so it does not name a way out: %v", want, err)
		}
	}

	// NOT WIDER THAN THE FAILURE. A principal with no key anywhere is nearly
	// every principal on this fabric, and their rows are taken as before.
	nobody := "u-nokey-" + ulid.NewString()
	if err := db.CreateArtifact(ctx, &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: "note", Project: &project,
		OwnerUser: nobody, Title: "no key anywhere", Visibility: "project",
	}); err != nil {
		t.Fatalf("a principal with no key here can no longer write: %v", err)
	}

	// And the positive control: with the private half here the row is signed
	// and authored, which is what the refusal above is protecting.
	mine := "u-mine-" + ulid.NewString()
	principalKey(t, ctx, db, mine, 0)
	id := ulid.NewString()
	if err := db.CreateArtifact(ctx, &Artifact{
		ID: id, Type: MemoryType, Kind: "note", Project: &project,
		OwnerUser: mine, Title: "written where the key is", Visibility: "project",
	}); err != nil {
		t.Fatalf("a principal whose key is here was refused: %v", err)
	}
	got, err := db.GetArtifact(ctx, id)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if got.Authorship != AuthorshipAuthored {
		t.Errorf("a row signed with the principal's own key reads %q, want %q",
			got.Authorship, AuthorshipAuthored)
	}
}
