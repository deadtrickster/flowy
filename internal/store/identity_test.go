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

// TestAServedIdentityIsSelfSignedAndNeverRotates is what a peer may teach this
// node about who is who.
//
// A pull applies the identities on the page before the rows, because a row
// cannot be verified before its node's key is here - so the page is also a peer
// telling this node which key belongs to which node, and every key it plants is
// a node it can then sign for. Three rules hold that door, and this is all
// three at once:
//
//   - self-signed. An identity for node C carries C's own signature over C's
//     own name and key, so a relay passing it on cannot swap the key inside it,
//     and a peer cannot mint one for a node it does not hold the key of.
//   - never rotated. A second, different key for a node already here is
//     refused, TOFU'd or pinned alike - otherwise a peer impersonates any node
//     it likes by serving a new identity for it first.
//   - and first contact, which is the one trust the rule leaves: an unknown
//     node's key is taken on trust and marked unpinned. That is the residual,
//     and FLOWY_REQUIRE_PINNED_PEERS is the deployment that will not have it -
//     a key nobody named is refused rather than recorded, so every key such a
//     node holds is one its operator chose.
func TestAServedIdentityIsSelfSignedAndNeverRotates(t *testing.T) {
	ctx, db := open(t)

	// A peer serving an identity for somebody else, signed with its own key.
	// The name is C's, the key inside is C's, and the signature is the relay's:
	// only the holder of C's key can make that signature, which is the whole
	// point of it.
	third := "third-" + ulid.NewString()
	relay := testKey("relay-" + third)
	notSelfSigned := NodeIdentity{NodeID: third, PublicKey: publicOf(testKey(third))}
	notSelfSigned.Sig = signBytes(relay, canonicalIdentity(third, notSelfSigned.PublicKey))

	res, err := db.syncApply(ctx, nil, modePull, &SyncSet{
		Identities: []NodeIdentity{notSelfSigned},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied[tableIdentities] != 0 || res.Refused[tableIdentities] != 1 {
		t.Fatalf("an identity for %s signed by somebody else applied %d and was refused %d, "+
			"want 0 and 1: %+v",
			third, res.Applied[tableIdentities], res.Refused[tableIdentities], res.Reasons)
	}
	if _, err := db.GetIdentity(ctx, third); !errors.Is(err, ErrNotFound) {
		t.Errorf("a key for %s that %s never signed for is here: %v", third, third, err)
	}

	// A second, different key for a node this one already holds. Refused on the
	// wire, and refused at the operator's own door as ErrKeyRotation.
	known := "known-" + ulid.NewString()
	first := testKey(known)
	if err := db.PinIdentity(ctx, known, publicOf(first)); err != nil {
		t.Fatalf("pin: %v", err)
	}
	impostor := testKey(known + "-impostor")
	rotated := NodeIdentity{NodeID: known, PublicKey: publicOf(impostor)}
	rotated.Sig = signIdentity(impostor, &rotated)

	res, err = db.syncApply(ctx, nil, modePull, &SyncSet{Identities: []NodeIdentity{rotated}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied[tableIdentities] != 0 || res.Refused[tableIdentities] != 1 {
		t.Fatalf("a second key for %s applied %d and was refused %d, want 0 and 1: %+v",
			known, res.Applied[tableIdentities], res.Refused[tableIdentities], res.Reasons)
	}
	if err := db.PinIdentity(ctx, known, publicOf(impostor)); !errors.Is(err, ErrKeyRotation) {
		t.Errorf("pinning a second key for %s returned %v, want ErrKeyRotation", known, err)
	}
	held, err := db.GetIdentity(ctx, known)
	if err != nil {
		t.Fatalf("get identity: %v", err)
	}
	if !equalKeys(held.PublicKey, publicOf(first)) {
		t.Fatal("the key held here was replaced by one that arrived on a page")
	}

	// First contact, which is the residual: an unknown node's self-signed
	// identity is taken on trust with the flag off.
	tofu := "tofu-" + ulid.NewString()
	res, err = db.syncApply(ctx, nil, modePull, &SyncSet{
		Identities: []NodeIdentity{identityOfNode(tofu)},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied[tableIdentities] != 1 {
		t.Fatalf("an unknown node's identity applied %d, want 1: %+v",
			res.Applied[tableIdentities], res.Reasons)
	}

	// And the deployment that will not have it. The key is refused outright
	// rather than recorded and then found wanting on every row that needs it.
	db.SetRequirePinnedPeers(true)
	t.Cleanup(func() { db.SetRequirePinnedPeers(false) })

	strict := "strict-" + ulid.NewString()
	strictKey := testKey(strict)
	project := "pi-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	row := &Artifact{
		ID: ulid.NewString(), Type: "note", Project: &project, OwnerUser: owner,
		Visibility: "project", Title: "from a node nobody named", Body: "gravelbight",
		HLC: packed(t, db) + 1, Node: strict,
	}
	SignArtifact(strictKey, row)

	res, err = db.syncApply(ctx, nil, modePull, &SyncSet{
		Identities: []NodeIdentity{identityOfNode(strict)}, Artifacts: []*Artifact{row},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Applied[tableIdentities] != 0 || res.Refused[tableIdentities] != 1 {
		t.Errorf("an unpinned identity applied %d and was refused %d under %s, want 0 and 1: %+v",
			res.Applied[tableIdentities], res.Refused[tableIdentities], requirePinnedEnv,
			res.Reasons)
	}
	if _, err := db.GetIdentity(ctx, strict); !errors.Is(err, ErrNotFound) {
		t.Errorf("a key nobody pinned was recorded under %s: %v", requirePinnedEnv, err)
	}
	if res.Applied["artifacts"] != 0 {
		t.Errorf("a row from that node landed anyway (%d rows)", res.Applied["artifacts"])
	}

	// The operator's pin is what lifts it, and nothing about the page changes.
	if err := db.PinIdentity(ctx, strict, publicOf(strictKey)); err != nil {
		t.Fatalf("pin: %v", err)
	}
	res, err = db.syncApply(ctx, nil, modePull, &SyncSet{
		Identities: []NodeIdentity{identityOfNode(strict)}, Artifacts: []*Artifact{row},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Refused[tableIdentities] != 0 || res.Applied["artifacts"] != 1 {
		t.Errorf("after the pin, the identity was refused %d times and the row applied %d, "+
			"want 0 and 1: %+v", res.Refused[tableIdentities], res.Applied["artifacts"],
			res.Reasons)
	}
}
