package store

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A test that hands the merge a row is standing in for another node, and since
// Phase 6.5 another node is a keypair. These two helpers are what every such
// test goes through: a key that is a function of the node's name, so the same
// node has the same key in every test and in every run, and a signing pass over
// a delta that pins those keys first.
//
// Nothing here weakens the merge. The rows are signed the way a real peer signs
// them - by the node named in their own node column - so a test that wants a
// forgery makes one by signing with the wrong key, which is what
// TestAForgedRowIsRefused does.

// testKey is the key node signs with in the tests. It is derived from the name
// rather than random so that two calls for one node agree.
func testKey(node string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("flowy test node:" + node))
	return ed25519.NewKeyFromSeed(seed[:])
}

// pinTestNode makes sure this node holds node's key, the way an operator would
// have pinned it.
func pinTestNode(t *testing.T, ctx context.Context, db *DB, node string) ed25519.PrivateKey {
	t.Helper()
	priv := testKey(node)
	err := db.PinIdentity(ctx, node, publicOf(priv))
	if err != nil && !errors.Is(err, ErrKeyRotation) {
		t.Fatalf("pin %s: %v", node, err)
	}
	return priv
}

// fromPeer signs every row of a delta as the node that row names, pinning each
// of those nodes' keys here first, and hands the delta back. It is what a
// correct peer would have sent.
func fromPeer(t *testing.T, ctx context.Context, db *DB, set *SyncSet) *SyncSet {
	t.Helper()
	keys := map[string]ed25519.PrivateKey{}
	key := func(node string) ed25519.PrivateKey {
		if node == "" {
			t.Fatalf("a row in the delta names no node, so nothing could have signed it")
		}
		if k, ok := keys[node]; ok {
			return k
		}
		k := pinTestNode(t, ctx, db, node)
		keys[node] = k
		return k
	}
	for _, a := range set.Artifacts {
		SignArtifact(key(a.Node), a)
	}
	for i := range set.Grants {
		SignGrant(key(set.Grants[i].Node), &set.Grants[i])
	}
	for _, task := range set.Tasks {
		SignTask(key(task.Node), task)
	}
	for _, e := range set.Events {
		SignEvent(key(e.Node), e)
	}
	return set
}

// TestThisNodeSignsAndKeepsItsKey is the local half: a node mints one identity,
// keeps it across handles on the same database, and signs its own writes with
// it.
func TestThisNodeSignsAndKeepsItsKey(t *testing.T) {
	ctx, db := open(t)

	id, err := db.Identity(ctx)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if id.NodeID != db.Node() {
		t.Fatalf("this node's identity names %q, want %q", id.NodeID, db.Node())
	}
	if len(id.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("the public key is %d bytes", len(id.PublicKey))
	}
	if !verifyBytes(id.PublicKey, canonicalIdentity(id.NodeID, id.PublicKey), id.Sig) {
		t.Fatal("this node's identity is not self-signed")
	}

	// A second handle on the same store is the same node: it reads the key
	// rather than minting a second one.
	again, err := db.Identity(ctx)
	if err != nil {
		t.Fatalf("identity again: %v", err)
	}
	if !equalKeys(again.PublicKey, id.PublicKey) {
		t.Fatal("asking twice produced two keys")
	}

	// And a row written here verifies under it, which is the whole point.
	project := "ps-" + ulid.NewString()
	art := &Artifact{Type: "note", Project: &project, OwnerUser: "u-" + ulid.NewString(),
		Title: "signed on the way in", Body: "whistlebrick"}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(art.Sig) == 0 {
		t.Fatal("a local write left the row unsigned")
	}
	read, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !verifyBytes(id.PublicKey, canonicalArtifact(read), read.Sig) {
		t.Fatal("the row this node wrote does not verify under this node's key")
	}

	// The private half is this node's alone: what a peer is handed is the
	// public list, and there is no private key in any of it.
	held, err := db.ListIdentities(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, one := range held {
		if one.NodeID == db.Node() {
			found = true
			if len(one.PublicKey) != ed25519.PublicKeySize {
				t.Errorf("the listed key is %d bytes", len(one.PublicKey))
			}
		}
	}
	if !found {
		t.Error("this node's own identity is not in the list it hands over")
	}
}

// TestPinnedKeyDoesNotRotate is the no-silent-rotation rule at the operator's
// door: a second key for a node already here is refused, and the key that is
// here stays.
func TestPinnedKeyDoesNotRotate(t *testing.T) {
	ctx, db := open(t)

	node := "rot-" + ulid.NewString()
	first := testKey(node)
	if err := db.PinIdentity(ctx, node, publicOf(first)); err != nil {
		t.Fatalf("pin: %v", err)
	}
	second := testKey(node + "-impostor")
	err := db.PinIdentity(ctx, node, publicOf(second))
	if !errors.Is(err, ErrKeyRotation) {
		t.Fatalf("pinning a second key for %s returned %v, want ErrKeyRotation", node, err)
	}
	held, err := db.GetIdentity(ctx, node)
	if err != nil {
		t.Fatalf("get identity: %v", err)
	}
	if !equalKeys(held.PublicKey, publicOf(first)) {
		t.Fatal("the key was replaced")
	}
	if !held.Pinned {
		t.Error("a pinned key came back unpinned")
	}

	// Pinning the same key again is not a rotation: it is how an identity that
	// arrived on a page is promoted to one the operator vouches for.
	if err := db.PinIdentity(ctx, node, publicOf(first)); err != nil {
		t.Fatalf("re-pinning the same key: %v", err)
	}
}
