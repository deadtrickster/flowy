package store

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The merge's authenticity gate, one property per test. Every one of them fails
// on the code as it was, because on the code as it was nothing on a row said
// who wrote it.

// identityOfNode is a node's identity as it travels: public half, self-signed.
func identityOfNode(node string) NodeIdentity {
	priv := testKey(node)
	id := NodeIdentity{NodeID: node, PublicKey: publicOf(priv)}
	id.Sig = signIdentity(priv, &id)
	return id
}

// TestAHostilePeerCannotRewriteAnothersRow is the finding this phase closes.
//
// Node A writes an artifact and signs it. A peer that can read it - which on a
// pull is every peer the artifact reaches - serves the same id back with the
// title, the body and the status rewritten and a higher reading, still claiming
// node A. Before signatures there was nothing to catch it: the row lands where
// it always landed, owned by whoever always owned it, and last-writer-wins made
// the rewrite the truth on this node and on every node downstream of it.
//
// It is refused three ways over: signed by the wrong key, signed by nobody, and
// signed by the right key over different bytes.
func TestAHostilePeerCannotRewriteAnothersRow(t *testing.T) {
	ctx, db := open(t)

	nodeA := "nodeA-" + ulid.NewString()
	nodeB := "nodeB-" + ulid.NewString()
	keyA := pinTestNode(t, ctx, db, nodeA)
	keyB := pinTestNode(t, ctx, db, nodeB)

	project := "pv-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	at := packed(t, db)
	id := ulid.NewString()

	original := &Artifact{
		ID: id, Type: "bug", Project: &project, OwnerUser: owner, Visibility: "project",
		Title: "the wipers judder", Body: "on the return stroke", Status: "open",
		HLC: at + 1, Node: nodeA,
	}
	SignArtifact(keyA, original)
	applied, err := db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{original}})
	if err != nil {
		t.Fatalf("apply the real row: %v", err)
	}
	if applied["artifacts"] != 1 {
		t.Fatalf("A's own signed row applied %d rows, want 1", applied["artifacts"])
	}

	// What a hostile peer serves: same id, same owner, same project, everything
	// else rewritten, and a reading that beats what is here.
	rewrite := func() *Artifact {
		return &Artifact{
			ID: id, Type: "bug", Project: &project, OwnerUser: owner, Visibility: "project",
			Title: "the wipers are fine", Body: "closed as invalid", Status: "done",
			HLC: at + 100, Node: nodeA,
		}
	}

	// 1. Signed with the hostile peer's own key, claiming to be A.
	forged := rewrite()
	SignArtifact(keyB, forged)
	// 2. Not signed at all.
	unsigned := rewrite()
	unsigned.ID = id
	// 3. Signed by A over other bytes: the signature of the row A really wrote,
	// carried on a row that is not it.
	replayed := rewrite()
	replayed.Sig = original.Sig

	for what, row := range map[string]*Artifact{
		"signed by another node":                      forged,
		"not signed at all":                           unsigned,
		"carrying A's signature over a different row": replayed,
	} {
		res, err := db.syncApply(ctx, nil, modePull, &SyncSet{Artifacts: []*Artifact{row}})
		if err != nil {
			t.Fatalf("%s: apply: %v", what, err)
		}
		if res.Applied["artifacts"] != 0 || res.Refused["artifacts"] != 1 {
			t.Errorf("a rewrite %s applied %d and was refused %d times, want 0 and 1: %+v",
				what, res.Applied["artifacts"], res.Refused["artifacts"], res.Reasons)
		}
		held, err := db.GetArtifact(ctx, id)
		if err != nil {
			t.Fatalf("%s: get: %v", what, err)
		}
		if held.Title != "the wipers judder" || held.Body != "on the return stroke" ||
			held.Status != "open" || held.HLC != at+1 {
			t.Fatalf("a rewrite %s landed: %+v", what, held)
		}
	}

	// And the node that really did write it can still move its own row, which is
	// what says the refusals above are about the signature and not about the id.
	moved := rewrite()
	moved.Title, moved.Status = "the wipers judder in the wet", "triaged"
	SignArtifact(keyA, moved)
	if applied, err = db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{moved}}); err != nil {
		t.Fatalf("apply A's own move: %v", err)
	}
	if applied["artifacts"] != 1 {
		t.Fatalf("A's own move applied %d rows, want 1", applied["artifacts"])
	}
}

// TestOneFlippedByteIsRefused is the tamper case reduced to its smallest: a
// row that verified, with one byte of one signed field changed in transit.
func TestOneFlippedByteIsRefused(t *testing.T) {
	ctx, db := open(t)

	node := "nodet-" + ulid.NewString()
	key := pinTestNode(t, ctx, db, node)
	project := "pt-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	at := packed(t, db)

	build := func() *Artifact {
		a := &Artifact{
			ID: ulid.NewString(), Type: "note", Project: &project, OwnerUser: owner,
			Visibility: "project", Title: "untouched", Body: "quibberflax",
			HLC: at + 1, Node: node,
		}
		SignArtifact(key, a)
		return a
	}

	// Every field the row carries is inside the signature, so pick a few that a
	// relay would most want to change and change one byte of each.
	for what, tamper := range map[string]func(*Artifact){
		"the title":     func(a *Artifact) { a.Title = "untouchee" },
		"the body":      func(a *Artifact) { a.Body = "quibberflay" },
		"the owner":     func(a *Artifact) { a.OwnerUser = owner[:len(owner)-1] + "x" },
		"the reading":   func(a *Artifact) { a.HLC++ },
		"the signature": func(a *Artifact) { a.Sig[0] ^= 0x01 },
	} {
		row := build()
		tamper(row)
		res, err := db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{row}})
		if err != nil {
			t.Fatalf("%s: apply: %v", what, err)
		}
		if res["artifacts"] != 0 {
			t.Errorf("a row with %s changed after signing applied %d rows, want 0",
				what, res["artifacts"])
		}
		if n := rows(t, db, "artifacts", row.ID); n != 0 {
			t.Errorf("a row with %s changed after signing is in the table (%d rows)", what, n)
		}
	}

	// The untampered one lands, so the refusals are about the tampering.
	good := build()
	if res, err := db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{good}}); err != nil {
		t.Fatalf("apply: %v", err)
	} else if res["artifacts"] != 1 {
		t.Fatalf("the untampered row applied %d rows, want 1", res["artifacts"])
	}
}

// TestAuthenticityAndAuthorisationAreTwoLayers is the composition.
//
// A peer really does write a grant - its own key, its own node, the signature
// verifies - and the grant is still not one it may write: it opens a project up
// in the name of somebody who is nobody here. Authenticity says who wrote it;
// authorisation says whether writing it was theirs to do. A row has to pass
// both, and the refusal here comes from the second, which is what says the
// first did not swallow it.
func TestAuthenticityAndAuthorisationAreTwoLayers(t *testing.T) {
	ctx, db := open(t)

	node := "nodeg-" + ulid.NewString()
	key := pinTestNode(t, ctx, db, node)

	home := "ph-" + ulid.NewString()
	theirs := "pi-" + ulid.NewString()
	me := &User{Handle: "opener-" + ulid.NewString()}
	if err := db.InsertUser(ctx, me); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db.InsertToken(ctx, &Principal{
		Token: "t-" + ulid.NewString(), UserID: me.ID, Project: home,
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	puller := &Principal{UserID: me.ID, Project: home}
	at := packed(t, db)

	// Genuinely written by the peer, and genuinely not the peer's to write.
	forged := Grant{
		ID: ulid.NewString(), FromProject: theirs, ToProject: home, Cap: "read",
		GrantedBy: "u-stranger-" + ulid.NewString(), HLC: at + 1, Node: node,
	}
	SignGrant(key, &forged)
	if !verifyBytes(publicOf(key), canonicalGrant(&forged), forged.Sig) {
		t.Fatal("the test's own grant does not verify; the rest of it means nothing")
	}

	res, err := db.SyncApplyFrom(ctx, puller, &SyncSet{Grants: []Grant{forged}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Refused["grants"] != 1 || res.Applied["grants"] != 0 {
		t.Fatalf("a validly signed forgery applied %d and was refused %d times, want 0 and 1: %+v",
			res.Applied["grants"], res.Refused["grants"], res.Reasons)
	}
	if len(res.Reasons) == 0 || strings.Contains(res.Reasons[0], "does not verify") {
		t.Fatalf("the refusal is the signature check rather than the authorisation check: %+v",
			res.Reasons)
	}
	if !strings.Contains(res.Reasons[0], "opens "+home+" up") {
		t.Fatalf("the refusal does not say what was wrong with it: %+v", res.Reasons)
	}
	if grantRows(t, db, forged.ID) != 0 {
		t.Errorf("the grant opened %s up on the strength of a valid signature", home)
	}
}

// TestAnIdentityArrivesWithTheRowsItVerifies is the relay: a page carrying a
// third node's rows carries that node's key, and the rows verify under it.
//
// This is what makes A <- B <- C work. A holds no key for C and never talks to
// it; C's identity is self-signed, so B cannot alter it on the way through, and
// A takes it on first use and verifies C's rows with it.
func TestAnIdentityArrivesWithTheRowsItVerifies(t *testing.T) {
	ctx, db := open(t)

	nodeC := "nodeC-" + ulid.NewString()
	keyC := testKey(nodeC) // and this node has never heard of it
	if _, err := db.GetIdentity(ctx, nodeC); err == nil {
		t.Fatalf("%s is already known here; the test proves nothing", nodeC)
	}

	project := "pr-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	at := packed(t, db)
	art := &Artifact{
		ID: ulid.NewString(), Type: "note", Project: &project, OwnerUser: owner,
		Visibility: "project", Title: "written on C", Body: "relayed by B",
		HLC: at + 1, Node: nodeC,
	}
	SignArtifact(keyC, art)

	// The row on its own is refused: no key, nothing to check it with.
	res, err := db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{art}})
	if err != nil {
		t.Fatalf("apply without the key: %v", err)
	}
	if res["artifacts"] != 0 {
		t.Fatalf("a row from an unknown node applied %d rows, want 0", res["artifacts"])
	}

	// The same row on a page that carries C's identity: taken on first use, and
	// the row verifies.
	res, err = db.SyncApply(ctx, &SyncSet{
		Identities: []NodeIdentity{identityOfNode(nodeC)},
		Artifacts:  []*Artifact{art},
	})
	if err != nil {
		t.Fatalf("apply with the key: %v", err)
	}
	if res["artifacts"] != 1 {
		t.Fatalf("a relayed row applied %d rows, want 1: the key came with it", res["artifacts"])
	}
	held, err := db.GetIdentity(ctx, nodeC)
	if err != nil {
		t.Fatalf("the relayed identity was not kept: %v", err)
	}
	if held.Pinned {
		t.Error("an identity that arrived over the wire is marked as the operator's pin")
	}

	// And an identity a relay altered on the way is not an identity: the
	// self-signature is over the name and the key together.
	swapped := identityOfNode(nodeC)
	swapped.PublicKey = publicOf(testKey(nodeC + "-impostor"))
	res2, err := db.syncApply(ctx, nil, modePull, &SyncSet{Identities: []NodeIdentity{swapped}})
	if err != nil {
		t.Fatalf("apply the swapped identity: %v", err)
	}
	if res2.Refused[tableIdentities] != 1 {
		t.Errorf("an identity with the key swapped out was refused %d times, want 1: %+v",
			res2.Refused[tableIdentities], res2.Reasons)
	}
}

// TestAKeyDoesNotRotateOverTheWire is the other half of key distribution: once
// a node is known, no page changes what it is.
//
// Without this, TOFU would be worth nothing - a peer would simply serve a new
// identity for the node it wanted to impersonate, and then sign that node's
// rows itself.
func TestAKeyDoesNotRotateOverTheWire(t *testing.T) {
	ctx, db := open(t)

	node := "noder-" + ulid.NewString()
	real := pinTestNode(t, ctx, db, node)
	impostor := testKey(node + "-impostor")

	// The impostor's identity, correctly self-signed under its own key, for a
	// node name this node already holds.
	swap := NodeIdentity{NodeID: node, PublicKey: publicOf(impostor)}
	swap.Sig = signIdentity(impostor, &swap)

	project := "pu-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	row := &Artifact{
		ID: ulid.NewString(), Type: "note", Project: &project, OwnerUser: owner,
		Visibility: "project", Title: "signed by the impostor",
		HLC: packed(t, db) + 1, Node: node,
	}
	SignArtifact(impostor, row)

	res, err := db.syncApply(ctx, nil, modePull, &SyncSet{
		Identities: []NodeIdentity{swap},
		Artifacts:  []*Artifact{row},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Refused[tableIdentities] != 1 || res.Applied[tableIdentities] != 0 {
		t.Errorf("a second key for %s applied %d and was refused %d times, want 0 and 1: %+v",
			node, res.Applied[tableIdentities], res.Refused[tableIdentities], res.Reasons)
	}
	if res.Applied["artifacts"] != 0 || res.Refused["artifacts"] != 1 {
		t.Errorf("the impostor's row applied %d and was refused %d times, want 0 and 1: %+v",
			res.Applied["artifacts"], res.Refused["artifacts"], res.Reasons)
	}
	held, err := db.GetIdentity(ctx, node)
	if err != nil {
		t.Fatalf("get identity: %v", err)
	}
	if !equalKeys(held.PublicKey, publicOf(real)) {
		t.Fatal("the key held here was replaced by one that arrived on a page")
	}
	if !held.Pinned {
		t.Error("the pin was lost")
	}
}

// TestRequirePinnedPeersRefusesATrustedOnFirstUseNode is the high-security
// deployment: transitive relay is exactly what it will not have.
func TestRequirePinnedPeersRefusesATrustedOnFirstUseNode(t *testing.T) {
	ctx, db := open(t)

	tofu := "nodef-" + ulid.NewString()
	pinned := "nodep-" + ulid.NewString()
	pinnedKey := pinTestNode(t, ctx, db, pinned)
	tofuKey := testKey(tofu)

	project := "pw-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	at := packed(t, db)
	row := func(node string, key ed25519.PrivateKey, n int64) *Artifact {
		a := &Artifact{
			ID: ulid.NewString(), Type: "note", Project: &project, OwnerUser: owner,
			Visibility: "project", Title: "from " + node, HLC: at + n, Node: node,
		}
		SignArtifact(key, a)
		return a
	}

	// First contact, with the flag off: the identity is taken on trust and the
	// row lands.
	open1 := row(tofu, tofuKey, 1)
	res, err := db.SyncApply(ctx, &SyncSet{
		Identities: []NodeIdentity{identityOfNode(tofu)}, Artifacts: []*Artifact{open1},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res["artifacts"] != 1 {
		t.Fatalf("a TOFU node's row applied %d rows with the flag off, want 1", res["artifacts"])
	}

	// With the flag on, the same node is not good enough - and a node the
	// operator pinned still is.
	db.SetRequirePinnedPeers(true)
	t.Cleanup(func() { db.SetRequirePinnedPeers(false) })

	refused := row(tofu, tofuKey, 2)
	allowed := row(pinned, pinnedKey, 3)
	out, err := db.syncApply(ctx, nil, modePull, &SyncSet{
		Artifacts: []*Artifact{refused, allowed},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Refused["artifacts"] != 1 || out.Applied["artifacts"] != 1 {
		t.Fatalf("applied %d and refused %d, want one of each: %+v",
			out.Applied["artifacts"], out.Refused["artifacts"], out.Reasons)
	}
	if n := rows(t, db, "artifacts", refused.ID); n != 0 {
		t.Errorf("a row from an unpinned node landed under %s (%d rows)", requirePinnedEnv, n)
	}
	if n := rows(t, db, "artifacts", allowed.ID); n != 1 {
		t.Errorf("a row from a pinned node was refused (%d rows)", n)
	}

	// And the pin is what lifts the refusal: pinning the TOFU node's key lets
	// its rows in again, without anything about the rows changing.
	if err := db.PinIdentity(ctx, tofu, publicOf(tofuKey)); err != nil {
		t.Fatalf("pin: %v", err)
	}
	after := row(tofu, tofuKey, 4)
	if res, err = db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{after}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res["artifacts"] != 1 {
		t.Fatalf("a pinned node's row applied %d rows, want 1", res["artifacts"])
	}
}

// TestALocalWriteIsSignedForEveryTable walks the four replicated tables through
// the writes the API makes and asserts each row comes out with a signature that
// verifies under this node's key. A table that is written unsigned is a table
// that cannot replicate at all.
func TestALocalWriteIsSignedForEveryTable(t *testing.T) {
	ctx, db := open(t)

	id, err := db.Identity(ctx)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	project := "px-" + ulid.NewString()
	from := &User{Handle: "from-" + ulid.NewString()}
	to := &User{Handle: "to-" + ulid.NewString()}
	for _, u := range []*User{from, to} {
		if err := db.InsertUser(ctx, u); err != nil {
			t.Fatalf("insert user: %v", err)
		}
	}

	art := &Artifact{Type: "bug", Project: &project, OwnerUser: from.ID, Title: "the work"}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	thread := ulid.NewString()
	grant := &Grant{
		ID: ulid.NewString(), FromProject: project, ToProject: project, Subject: to.ID,
		Artifact: art.ID, Cap: "read", GrantedBy: from.ID,
	}
	task := &Task{
		ID: ulid.NewString(), Artifact: art.ID, FromUser: from.ID, ToUser: to.ID,
		Project: project, State: TaskOpen, Thread: thread,
	}
	opening := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "handoffs",
		Thread: thread, Parents: []string{}, Actor: from.ID, Artifact: art.ID, Body: "yours",
	}
	if err := db.WriteAssignment(ctx, grant, task, opening); err != nil {
		t.Fatalf("write assignment: %v", err)
	}

	// Read them back rather than trusting the structs: what replicates is what
	// is in the columns.
	storedArt, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	storedTask, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	storedEvent, err := db.GetEvent(ctx, opening.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	grants, err := db.GrantsFor(ctx, storedArt)
	if err != nil {
		t.Fatalf("grants for: %v", err)
	}
	var storedGrant *Grant
	for i := range grants {
		if grants[i].ID == grant.ID {
			storedGrant = &grants[i]
		}
	}
	if storedGrant == nil {
		t.Fatal("the share the assignment wrote is not there")
	}

	if !verifyBytes(id.PublicKey, canonicalArtifact(storedArt), storedArt.Sig) {
		t.Error("the artifact this node wrote does not verify under its own key")
	}
	if !verifyBytes(id.PublicKey, canonicalGrant(storedGrant), storedGrant.Sig) {
		t.Error("the grant this node wrote does not verify under its own key")
	}
	if !verifyBytes(id.PublicKey, canonicalTask(storedTask), storedTask.Sig) {
		t.Error("the task this node wrote does not verify under its own key")
	}
	if !verifyBytes(id.PublicKey, canonicalEvent(storedEvent), storedEvent.Sig) {
		t.Error("the event this node wrote does not verify under its own key")
	}

	// A move is a write: the state change re-signs, so the row a peer merges is
	// the row that is here rather than the one it used to be.
	storedTask.State = TaskDone
	if err := db.UpdateTask(ctx, storedTask); err != nil {
		t.Fatalf("update task: %v", err)
	}
	moved, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !verifyBytes(id.PublicKey, canonicalTask(moved), moved.Sig) {
		t.Error("a task that moved state does not verify under this node's key")
	}

	// So is a status move, and so is a delete.
	if err := db.SetArtifactStatus(ctx, storedArt, "triaged"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	afterStatus, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	if !verifyBytes(id.PublicKey, canonicalArtifact(afterStatus), afterStatus.Sig) {
		t.Error("an artifact that moved status does not verify under this node's key")
	}
	if _, err := db.TombstoneArtifact(ctx, &Principal{UserID: from.ID, Project: project}, art.ID); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	deleted, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	if !deleted.Tombstone {
		t.Fatal("the delete did not land")
	}
	if !verifyBytes(id.PublicKey, canonicalArtifact(deleted), deleted.Sig) {
		t.Error("a tombstone does not verify under this node's key: the delete cannot replicate")
	}
}

// TestASignatureSurvivesTheDatabase is the other half of "sign what is
// written": a jsonb column is not a string.
//
// Postgres parses jsonb, drops the whitespace, orders the keys its own way and
// normalises the numbers, so the bytes handed to the insert are not the bytes
// that come back out. A signature over the request body verifies on the node
// that made it and on nothing else - and the row it breaks on is any artifact
// with fields or any message with meta, which is every message the chat
// endpoints write.
func TestASignatureSurvivesTheDatabase(t *testing.T) {
	ctx, db := open(t)

	id, err := db.Identity(ctx)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	project := "pj-" + ulid.NewString()
	owner := "u-" + ulid.NewString()

	// Whitespace, key order and a number Postgres will write back its own way.
	art := &Artifact{
		Type: "note", Project: &project, OwnerUser: owner, Title: "with fields",
		Body:   "thrumblewick",
		Fields: json.RawMessage(`{ "zeta": 1.0,  "alpha": {"b":2, "a":[3, 4]}, "omega": "x" }`),
	}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	stored, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(stored.Fields) == string(art.Fields) {
		t.Log("note: this database returned the fields column byte for byte")
	}
	if !verifyBytes(id.PublicKey, canonicalArtifact(stored), stored.Sig) {
		t.Error("an artifact with a fields object does not verify after a round trip: " +
			"what was signed is not what a peer will be handed")
	}

	e := &Event{
		Type: "chat", Project: &project, Room: "general", Actor: owner,
		Parents: []string{}, Body: "said with meta",
		Meta: json.RawMessage(`{ "topic": "kept", "actor_kind":"agent", "n": 1e2 }`),
	}
	if err := db.AppendEvent(ctx, e); err != nil {
		t.Fatalf("append: %v", err)
	}
	storedEvent, err := db.GetEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if !verifyBytes(id.PublicKey, canonicalEvent(storedEvent), storedEvent.Sig) {
		t.Error("an event with a meta object does not verify after a round trip")
	}

	// And the round trip is the one replication makes: read it out of the store
	// the way a pull does and merge it somewhere that has never seen it.
	set, err := db.SyncPull(ctx, &Principal{UserID: owner, Project: project}, SyncQuery{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	seen := 0
	for _, one := range set.Artifacts {
		if one.ID == art.ID {
			seen++
			if !verifyBytes(id.PublicKey, canonicalArtifact(one), one.Sig) {
				t.Error("the artifact as a peer is handed it does not verify")
			}
		}
	}
	for _, one := range set.Events {
		if one.ID == e.ID {
			seen++
			if !verifyBytes(id.PublicKey, canonicalEvent(one), one.Sig) {
				t.Error("the event as a peer is handed it does not verify")
			}
		}
	}
	if seen != 2 {
		t.Fatalf("the pull carried %d of the two rows this test wrote", seen)
	}
}

// TestSyncPullHandsOverTheKeysAndNoPrivateOnes is the other end of key
// distribution: a page carries the public halves, and nothing carries a private
// one.
func TestSyncPullHandsOverTheKeysAndNoPrivateOnes(t *testing.T) {
	ctx, db := open(t)

	if _, err := db.Identity(ctx); err != nil {
		t.Fatalf("identity: %v", err)
	}
	set, err := db.SyncPull(ctx, &Principal{UserID: "u-" + ulid.NewString()}, SyncQuery{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	found := false
	for _, id := range set.Identities {
		if id.NodeID == db.Node() {
			found = true
		}
		if len(id.PublicKey) != ed25519.PublicKeySize {
			t.Errorf("the key handed over for %s is %d bytes", id.NodeID, len(id.PublicKey))
		}
	}
	if !found {
		t.Fatal("a pull handed over no identity for the node answering it")
	}

	// The private key is not a field of what travels, so the only way to be
	// sure is the encoding: what a peer receives has no seed in it anywhere.
	seed, err := db.SigningKey(ctx)
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("encode the page: %v", err)
	}
	for what, spelling := range map[string]string{
		"as base64":          base64.StdEncoding.EncodeToString(seed.Seed()),
		"as base64, in full": base64.StdEncoding.EncodeToString(seed),
		"as hex":             EncodeKey(seed.Seed()),
	} {
		if strings.Contains(string(raw), spelling) {
			t.Fatalf("a pull answer carries this node's private key %s", what)
		}
	}
	if strings.Contains(string(raw), "private") {
		t.Fatal("a pull answer has a private key field in it")
	}
}

// TestTheCreatedDateIsInsideTheSignature is the column the signature left out.
//
// canonicalArtifact named twenty-one fields and canonicalEvent its own set, and
// neither of them named created. The column replicates - it is what every list,
// every digest and every reader ages and orders by - so a relay could hand on
// somebody else's row with its date moved and nothing anywhere would notice:
// the signature still verifies, because the bytes it covers are unchanged, and
// the row lands looking more authentic than an unsigned one ever could. An
// adversary planted rows three months old on a receiver that way.
//
// So created is signed on both row types, and the local writes mint it rather
// than leaving it to the column's DEFAULT now(), because a value the database
// invents after the signing is a value nothing signed - see createdNow.
func TestTheCreatedDateIsInsideTheSignature(t *testing.T) {
	ctx, db := open(t)

	node := "dated-" + ulid.NewString()
	key := pinTestNode(t, ctx, db, node)
	project := "pd-" + ulid.NewString()
	owner := "u-" + ulid.NewString()
	at := packed(t, db)
	when := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	planted := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	art := &Artifact{
		ID: ulid.NewString(), Type: "note", Project: &project, OwnerUser: owner,
		Visibility: "project", Title: "dated", Body: "wrackenspoke",
		HLC: at + 1, Node: node, Created: when,
	}
	SignArtifact(key, art)
	if !verifyBytes(publicOf(key), canonicalArtifact(art), art.Sig) {
		t.Fatal("an artifact signed with its date does not verify")
	}

	// One field moved, nothing else touched.
	moved := *art
	moved.Created = planted
	if verifyBytes(publicOf(key), canonicalArtifact(&moved), moved.Sig) {
		t.Error("an artifact whose created was rewritten still verifies: " +
			"the date is outside the signature")
	}

	res, err := db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{&moved}})
	if err != nil {
		t.Fatalf("apply the back-dated artifact: %v", err)
	}
	if res["artifacts"] != 0 {
		t.Errorf("a back-dated artifact applied %d rows, want 0", res["artifacts"])
	}
	if n := rows(t, db, "artifacts", art.ID); n != 0 {
		t.Fatalf("the back-dated artifact landed (%d rows)", n)
	}

	// The row as its author signed it lands, with the date its author gave it.
	if res, err = db.SyncApply(ctx, &SyncSet{Artifacts: []*Artifact{art}}); err != nil {
		t.Fatalf("apply the artifact: %v", err)
	}
	if res["artifacts"] != 1 {
		t.Fatalf("the signed artifact applied %d rows, want 1", res["artifacts"])
	}
	held, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !held.Created.Equal(when) {
		t.Errorf("the artifact is dated %s here, want %s", held.Created.UTC(), when)
	}

	// And the same for an event, which is the row a date matters most on: the
	// log is read in date order.
	e := &Event{
		ID: ulid.NewString(), Type: "chat", Project: &project, Room: "general",
		Actor: owner, Body: "said at a time", SeqHLC: at + 2, Node: node, Created: when,
	}
	SignEvent(key, e)
	if !verifyBytes(publicOf(key), canonicalEvent(e), e.Sig) {
		t.Fatal("an event signed with its date does not verify")
	}
	movedEvent := *e
	movedEvent.Created = time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	if verifyBytes(publicOf(key), canonicalEvent(&movedEvent), movedEvent.Sig) {
		t.Error("an event whose created was rewritten still verifies")
	}
	if res, err = db.SyncApply(ctx, &SyncSet{Events: []*Event{&movedEvent}}); err != nil {
		t.Fatalf("apply the back-dated event: %v", err)
	}
	if res["events"] != 0 {
		t.Errorf("a back-dated event applied %d rows, want 0", res["events"])
	}
	if res, err = db.SyncApply(ctx, &SyncSet{Events: []*Event{e}}); err != nil {
		t.Fatalf("apply the event: %v", err)
	}
	if res["events"] != 1 {
		t.Fatalf("the signed event applied %d rows, want 1", res["events"])
	}
	heldEvent, err := db.GetEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if !heldEvent.Created.Equal(when) {
		t.Errorf("the event is dated %s here, want %s", heldEvent.Created.UTC(), when)
	}
}

// TestALocalWritesDateIsSignedWithIt is the other half: the date a local write
// puts in the column is the date it signed.
//
// It used to be the column's own DEFAULT now(), filled in after the signature
// was made, so every row this node wrote carried a date outside its own
// signature - and a peer relaying it onward could hand over any date it liked
// with the author's signature still verifying beside it.
func TestALocalWritesDateIsSignedWithIt(t *testing.T) {
	ctx, db := open(t)

	id, err := db.Identity(ctx)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	project := "pl-" + ulid.NewString()
	owner := "u-" + ulid.NewString()

	art := &Artifact{
		Type: "note", Project: &project, OwnerUser: owner, Visibility: "project",
		Title: "written here", Body: "flintermoss",
	}
	if err := db.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("create: %v", err)
	}
	stored, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Created.IsZero() {
		t.Fatal("the stored artifact has no date at all")
	}
	if !verifyBytes(id.PublicKey, canonicalArtifact(stored), stored.Sig) {
		t.Error("a locally created artifact does not verify with the date the column holds")
	}
	backdated := *stored
	backdated.Created = stored.Created.Add(-90 * 24 * time.Hour)
	if verifyBytes(id.PublicKey, canonicalArtifact(&backdated), backdated.Sig) {
		t.Error("moving a locally written artifact's date leaves it verifying")
	}

	// An edit keeps the date the row was created with, and signs that one: a
	// row signed over a date it does not have is a row no peer can verify.
	stored.Title = "edited here"
	if err := db.UpsertArtifact(ctx, stored); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	edited, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("get after edit: %v", err)
	}
	if !edited.Created.Equal(art.Created) {
		t.Errorf("an edit moved the date from %s to %s", art.Created, edited.Created)
	}
	if !verifyBytes(id.PublicKey, canonicalArtifact(edited), edited.Sig) {
		t.Error("an edited artifact does not verify with the date the column holds")
	}

	e := &Event{
		Type: "chat", Project: &project, Room: "general", Actor: owner, Body: "said here",
	}
	if err := db.AppendEvent(ctx, e); err != nil {
		t.Fatalf("append: %v", err)
	}
	storedEvent, err := db.GetEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if storedEvent.Created.IsZero() {
		t.Fatal("the stored event has no date at all")
	}
	if !verifyBytes(id.PublicKey, canonicalEvent(storedEvent), storedEvent.Sig) {
		t.Error("a locally appended event does not verify with the date the column holds")
	}
}
