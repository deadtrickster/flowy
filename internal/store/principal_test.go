package store

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The accept side, and whose word a row is.
//
// Every check in this file is about one sentence: a node signature says which
// machine relayed a row and says nothing whatever about who wrote it. The merge
// used to conflate the two - a peer the operator had pinned could write rows
// under anybody's name, including this node's own people's - and what closes it
// is a second key, held by the principal rather than by any node, plus an epoch
// that says from when that key is required.

// principalKey mints a principal a keypair on this node, derived from the name
// so two calls agree, and returns the private half.
func principalKey(t *testing.T, ctx context.Context, db *DB, principal string, epoch int64) ed25519.PrivateKey {
	t.Helper()
	seed := sha256.Sum256([]byte("flowy test principal:" + principal))
	if _, err := db.MintPrincipalKey(ctx, principal, seed[:], epoch); err != nil {
		t.Fatalf("mint a key for %s: %v", principal, err)
	}
	return ed25519.NewKeyFromSeed(seed[:])
}

// pushed merges a delta the way a peer pushing at this node does, and reports
// what happened to it.
func pushed(t *testing.T, ctx context.Context, db *DB, p *Principal, set *SyncSet) *SyncResult {
	t.Helper()
	res, err := db.SyncApplyAs(ctx, p, fromPeer(t, ctx, db, set))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return res
}

// TestAPinnedNodeCannotSpeakForAPrincipalWithAKey is the finding, and the fix,
// in one test: the same forgery is applied before the principal has a key here
// and refused after.
//
// The row is an ordinary chat message. It is signed correctly by a node this
// node has pinned, it lands in a project the pusher reads, and every rule the
// merge had before this one says yes to it - which is the point. Pinning a node
// is agreeing to carry what it relays, and it was being read as agreeing to
// whatever it says about who wrote what.
func TestAPinnedNodeCannotSpeakForAPrincipalWithAKey(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pk")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)

	// Before the key: taken, and marked attributed rather than as alice's own
	// word. That is the state every row in the fabric is in today.
	before := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "I agree, ship it", SeqHLC: at + 1, Node: "peer-node"}
	res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{before}})
	if res.Applied["events"] != 1 {
		t.Fatalf("a relayed row for a principal with no key here was refused: %+v", res.Reasons)
	}
	got, err := db.GetEvent(ctx, before.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if got.Authorship != AuthorshipAttributed {
		t.Fatalf("a row nobody signed for is marked %q, want %q", got.Authorship, AuthorshipAttributed)
	}

	// Now alice has a key here, with an epoch below what follows.
	principalKey(t, ctx, db, alice, at)

	after := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "I agree, ship it", SeqHLC: at + 2, Node: "peer-node"}
	res = pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{after}})
	if res.Refused["events"] != 1 || res.Applied["events"] != 0 {
		t.Fatalf("the same forgery applied %d and refused %d, want 0 and 1",
			res.Applied["events"], res.Refused["events"])
	}
	if _, err := db.GetEvent(ctx, after.ID); err == nil {
		t.Fatal("the forged message is in the log")
	}
}

// TestARowBelowTheEpochIsStillTaken is the migration seam, and it is why this
// is not the home-node floor that had to be reverted.
//
// A fabric that has been running carries rows nobody could have signed, because
// the key did not exist when they were written. Refusing those would refuse
// history - every node would reject every peer's back catalogue the moment a
// key was provisioned. So the epoch is a line in the clock: below it a row is
// taken exactly as it always was, above it the author's own signature is
// required.
func TestARowBelowTheEpochIsStillTaken(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pl")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)

	// The epoch is above the row's reading: the row predates the key.
	principalKey(t, ctx, db, alice, at+1000)

	old := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "written before there was a key", SeqHLC: at + 1, Node: "peer-node"}
	res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{old}})
	if res.Applied["events"] != 1 {
		t.Fatalf("a row below the epoch was refused: %+v", res.Reasons)
	}
	got, err := db.GetEvent(ctx, old.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if got.Authorship != AuthorshipAttributed {
		t.Fatalf("a row below the epoch is marked %q, want %q", got.Authorship, AuthorshipAttributed)
	}

	// And one reading at the epoch is not below it.
	fresh := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "written after", SeqHLC: at + 1000, Node: "peer-node"}
	res = pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{fresh}})
	if res.Refused["events"] != 1 {
		t.Fatalf("a row at the epoch was applied: %+v", res)
	}
}

// TestAPrincipalSignedEventIsAuthored is the other half: the row a legitimate
// node writes for alice, carrying her signature, lands and is marked as hers.
//
// Without this the fix would be indistinguishable from a rule that refuses
// everything about a principal with a key, which is a break rather than a fix.
func TestAPrincipalSignedEventIsAuthored(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pm")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	priv := principalKey(t, ctx, db, alice, at)

	hers := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "this one really is mine", SeqHLC: at + 1, Node: "peer-node"}
	SignEventAs(priv, alice, hers)

	res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{hers}})
	if res.Applied["events"] != 1 {
		t.Fatalf("alice's own signed message was refused: %+v", res.Reasons)
	}
	got, err := db.GetEvent(ctx, hers.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if got.Authorship != AuthorshipAuthored {
		t.Fatalf("a signed row is marked %q, want %q", got.Authorship, AuthorshipAuthored)
	}

	// Somebody else's key over the same claim is not alice's signature, however
	// well formed it is.
	bob := "u-bob-" + ulid.NewString()
	other := principalKey(t, ctx, db, bob, at)
	forged := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "and this one is not", SeqHLC: at + 2, Node: "peer-node"}
	SignEventAs(other, alice, forged)
	res = pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{forged}})
	if res.Refused["events"] != 1 {
		t.Fatalf("a signature from another principal was accepted as alice's: %+v", res)
	}

	// And so is a signature of hers over a different row: the body is inside
	// what she signs, so moving it is the same forgery one step along.
	moved := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "the words she signed", SeqHLC: at + 3, Node: "peer-node"}
	SignEventAs(priv, alice, moved)
	moved.Body = "the words she did not"
	res = pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{moved}})
	if res.Refused["events"] != 1 {
		t.Fatalf("a signature over other words was accepted: %+v", res)
	}
}

// TestTheEpochHoldsWhoeverIsCarryingTheRow closes the gap the reverted attempt
// named and left open.
//
// The old provenance rule only asked who was vouching when the row named
// somebody OTHER than the principal carrying the page. A node syncing AS alice
// therefore walked past every check with rows claiming to be alice's - 35
// pulled, 35 applied, 0 refused, replication looking healthy - while a node
// syncing as anybody else was refused every one. Authorship is not a question
// about the carrier, so the carrier does not enter into it here.
func TestTheEpochHoldsWhoeverIsCarryingTheRow(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pn")
	alice := "u-alice-" + ulid.NewString()
	// The principal carrying the page IS the principal the row names.
	carrier := &Principal{UserID: alice, Project: project}
	at := packed(t, db)
	principalKey(t, ctx, db, alice, at)

	claim := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "the row claims me, which is what an attacker wants it to say",
		SeqHLC: at + 1, Node: "peer-node"}
	res := pushed(t, ctx, db, carrier, &SyncSet{Events: []*Event{claim}})
	if res.Refused["events"] != 1 {
		t.Fatalf("a row naming the carrier was taken on the carrier's own say-so: %+v", res)
	}

	// The same rule with no principal at all, which is this node's own
	// administration merging a file: authorship is not authorisation, so the
	// operator's door asks it too.
	admin := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "merged by hand", SeqHLC: at + 2, Node: "peer-node"}
	if _, err := db.SyncApply(ctx, fromPeer(t, ctx, db, &SyncSet{Events: []*Event{admin}})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := db.GetEvent(ctx, admin.ID); err == nil {
		t.Fatal("the operator's own merge took an unsigned claim about a principal with a key")
	}
}

// TestALocalWriteCarriesThePrincipalsSignature is the write side: the node that
// holds a principal's key signs what that principal writes, without anything
// upstream of the store having to know about it.
func TestALocalWriteCarriesThePrincipalsSignature(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "po")
	alice := "u-alice-" + ulid.NewString()
	priv := principalKey(t, ctx, db, alice, 0)

	e := &Event{Type: "chat", Project: &project, Actor: alice, Body: "said here"}
	if err := db.AppendEvent(ctx, e); err != nil {
		t.Fatalf("append: %v", err)
	}
	if e.Authorship != AuthorshipAuthored {
		t.Fatalf("a locally written event is marked %q, want %q", e.Authorship, AuthorshipAuthored)
	}
	if !verifyBytes(publicOf(priv), canonicalEventAuthorship(alice, e), e.AuthorSig) {
		t.Fatal("the event this node wrote does not verify under the key it signed with")
	}
	// And the mark is on the row and not only on the struct that was handed back.
	stored, err := db.GetEvent(ctx, e.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if stored.Authorship != AuthorshipAuthored || len(stored.AuthorSig) == 0 {
		t.Fatalf("the stored row: authorship %q, %d bytes of signature",
			stored.Authorship, len(stored.AuthorSig))
	}

	// An artifact the same way, and a message by somebody with no key here the
	// other way: attributed, unsigned, and not refused.
	a := &Artifact{Type: "note", Project: &project, OwnerUser: alice, Title: "hers"}
	if err := db.UpsertArtifact(ctx, a); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if a.Authorship != AuthorshipAuthored ||
		!verifyBytes(publicOf(priv), canonicalArtifactAuthorship(alice, a), a.AuthorSig) {
		t.Fatalf("the artifact this node wrote for alice: %q, %d bytes",
			a.Authorship, len(a.AuthorSig))
	}

	nobody := "u-nobody-" + ulid.NewString()
	plain := &Event{Type: "chat", Project: &project, Actor: nobody, Body: "no key here"}
	if err := db.AppendEvent(ctx, plain); err != nil {
		t.Fatalf("append: %v", err)
	}
	if plain.Authorship != AuthorshipAttributed || len(plain.AuthorSig) != 0 {
		t.Fatalf("a principal with no key here: %q, %d bytes", plain.Authorship, len(plain.AuthorSig))
	}
}

// TestAPartysWriteKeepsTheOwnersSignature is why an artifact's author signature
// covers a subset of the row rather than all of it.
//
// An artifact is mutable and other people legitimately write it: a party moves
// its status, a todo's assignee lands in fields. If the owner's signature
// covered those columns, the first ordinary status move would strip an artifact
// of its authorship and its owner's peers would then refuse it - a federation
// break dressed as a security fix. So what the owner signs is what only the
// owner writes, and a party's move carries it forward intact.
func TestAPartysWriteKeepsTheOwnersSignature(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pp")
	alice := "u-alice-" + ulid.NewString()
	priv := principalKey(t, ctx, db, alice, 0)

	art := &Artifact{Type: "bug", Project: &project, OwnerUser: alice,
		Title: "the thing is broken", Body: "and here is how", Status: "open"}
	if err := db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	signed := append([]byte(nil), art.AuthorSig...)

	e := &Event{Type: "status", Project: &project, Actor: "u-somebody-else", Body: "open->in-review"}
	if err := db.MoveArtifactStatus(ctx, art, "in-review", e); err != nil {
		t.Fatalf("move status: %v", err)
	}
	moved, err := db.GetArtifact(ctx, art.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if moved.Status != "in-review" {
		t.Fatalf("the status did not move: %q", moved.Status)
	}
	if string(moved.AuthorSig) != string(signed) {
		t.Fatal("a party's status move rewrote the owner's signature")
	}
	if !verifyBytes(publicOf(priv), canonicalArtifactAuthorship(alice, moved), moved.AuthorSig) {
		t.Fatal("the owner's signature stopped verifying when somebody else moved the status")
	}

	// And the words are still hers to sign: a rewrite of the body under her
	// name does not verify, which is what makes the refusal bite.
	forged := *moved
	forged.Body = "and here is how, allegedly"
	if verifyBytes(publicOf(priv), canonicalArtifactAuthorship(alice, &forged), forged.AuthorSig) {
		t.Fatal("a rewritten body still verifies under the owner's signature")
	}
}

// TestARewrittenArtifactIsRefusedAfterTheEpoch is the artifact half of the
// finding: a pinned peer serving somebody else's bug back with the words
// changed.
func TestARewrittenArtifactIsRefusedAfterTheEpoch(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pq")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	priv := principalKey(t, ctx, db, alice, at)

	hers := &Artifact{ID: ulid.NewString(), Type: "bug", Project: &project, OwnerUser: alice,
		Title: "a real bug", Body: "found in the drainer", Visibility: "project",
		HLC: at + 1, Node: "peer-node", Created: createdNow()}
	SignArtifactAs(priv, alice, hers)
	res := pushed(t, ctx, db, peer, &SyncSet{Artifacts: []*Artifact{hers}})
	if res.Applied["artifacts"] != 1 {
		t.Fatalf("alice's own signed artifact was refused: %+v", res.Reasons)
	}

	rewritten := *hers
	rewritten.Body = "actually it was operator error"
	rewritten.HLC = at + 2
	res = pushed(t, ctx, db, peer, &SyncSet{Artifacts: []*Artifact{&rewritten}})
	if res.Refused["artifacts"] != 1 {
		t.Fatalf("a rewrite of alice's words was applied: %+v", res)
	}
	stored, err := db.GetArtifact(ctx, hers.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if stored.Body != "found in the drainer" || stored.Authorship != AuthorshipAuthored {
		t.Fatalf("after the refused rewrite: %q, %q", stored.Body, stored.Authorship)
	}
}

// TestAPrincipalKeyIsNotReplacedInPlace: rotation is out of scope, and out of
// scope has to be a refusal rather than a silent overwrite - a key that can be
// replaced by whoever gets there second is not a key.
func TestAPrincipalKeyIsNotReplacedInPlace(t *testing.T) {
	ctx, db := open(t)

	alice := "u-alice-" + ulid.NewString()
	priv := principalKey(t, ctx, db, alice, 0)

	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	err := db.PinPrincipalKey(ctx, alice, publicOf(other), 0)
	if !errors.Is(err, ErrPrincipalKeyRotation) {
		t.Fatalf("pinning a second key returned %v, want ErrPrincipalKeyRotation", err)
	}
	held, err := db.GetPrincipalKey(ctx, alice)
	if err != nil {
		t.Fatalf("read the key back: %v", err)
	}
	if !equalKeys(held.PublicKey, publicOf(priv)) || !held.Local {
		t.Fatalf("the key here is not the one that was minted: %+v", held)
	}

	// The same key again moves the epoch, which is the one thing about a pinned
	// key that does change: an operator deciding from when it is required.
	if err := db.PinPrincipalKey(ctx, alice, publicOf(priv), 99); err != nil {
		t.Fatalf("re-pinning the same key: %v", err)
	}
	held, err = db.GetPrincipalKey(ctx, alice)
	if err != nil {
		t.Fatalf("read the key back: %v", err)
	}
	if held.Epoch != 99 {
		t.Fatalf("the epoch is %d, want 99", held.Epoch)
	}
}

// TestARefusedRowSaysHowMuchItWithheld is the other half of a refusal, and the
// half that was missing: somebody on THIS side being told it happened.
//
// The merge answers the peer that pushed a forgery with a count and a reason.
// Nobody here was answered at all - the row was refused, so it was not in the
// log, so a queue read handed back a shorter list and said nothing. A shorter
// list is indistinguishable from a shorter queue: the reader cannot tell "that is
// all the work there is" from "there is work this node would not carry", and the
// first is a false statement about the fleet made by a node that knows better.
//
// So the refusal is counted, the count carries the reason, and it stops being
// made the moment the row is no longer being withheld.
func TestARefusedRowSaysHowMuchItWithheld(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pw")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	priv := principalKey(t, ctx, db, alice, at)

	// An absence rather than a zero: a surface with nothing to report renders
	// nothing, so nothing that had nothing to say changed.
	if w, err := db.WithheldAuthorship(ctx, peer, false); err != nil || w != nil {
		t.Fatalf("with nothing refused the count is %+v, %v - want nil", w, err)
	}

	forged := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "ship it, no review needed", SeqHLC: at + 1, Node: "peer-node"}
	res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{forged}})
	if res.Refused["events"] != 1 || res.Applied["events"] != 0 {
		t.Fatalf("the forgery applied %d and refused %d, want 0 and 1",
			res.Applied["events"], res.Refused["events"])
	}

	w, err := db.WithheldAuthorship(ctx, peer, false)
	if err != nil {
		t.Fatalf("count what was withheld: %v", err)
	}
	if w == nil {
		t.Fatal("a row was refused and the queue says nothing was withheld")
	}
	if w.Rows != 1 || w.Reason != WithheldUnverifiedAuthorship {
		t.Fatalf("withheld %+v, want 1 row and %q", w, WithheldUnverifiedAuthorship)
	}

	// The count is a read like any other read: a principal who could not have
	// been handed the row is not told it was refused either. The refusal is not
	// a second way to learn what is in a project.
	elsewhere := &Principal{UserID: "u-" + ulid.NewString(),
		Project: declaredProject(t, ctx, db, "pw-other")}
	if w, err := db.WithheldAuthorship(ctx, elsewhere, false); err != nil || w != nil {
		t.Fatalf("a reader outside the project is told %+v, %v - want nil", w, err)
	}

	// The same row, from the same peer, carrying alice's own signature this
	// time. It lands - and the node stops saying it is withholding something it
	// is not, which is the same lie the other way up.
	hers := &Event{ID: forged.ID, Type: "chat", Project: &project,
		Actor: alice, Body: "ship it, no review needed", SeqHLC: at + 1, Node: "peer-node"}
	SignEventAs(priv, alice, hers)
	res = pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{hers}})
	if res.Applied["events"] != 1 {
		t.Fatalf("alice's own signed row was refused: %+v", res.Reasons)
	}
	if w, err := db.WithheldAuthorship(ctx, peer, false); err != nil || w != nil {
		t.Fatalf("the row landed and the count still reads %+v, %v - want nil", w, err)
	}
}

// TestWhatWasWithheldIsCountedWhereTheRowWouldHaveBeenRead: the count is scoped
// by the artifact read rule and not by anything of its own.
//
// A refused row that would have been personal to alice is counted for alice and
// for nobody else. That matters in both directions: a reader who would never
// have seen the row learns nothing about it from the refusal, and the one person
// whose queue is short is told that it is.
func TestWhatWasWithheldIsCountedWhereTheRowWouldHaveBeenRead(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pv")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	principalKey(t, ctx, db, alice, at)

	// A todo of alice's own, in no project at all: the personal floor, where the
	// only reader is its owner.
	forged := &Artifact{ID: ulid.NewString(), Type: "memory", Kind: "todo",
		OwnerUser: alice, Visibility: VisibilityPersonal, Title: "resign",
		Status: "todo", HLC: at + 1, Node: "peer-node"}
	res := pushed(t, ctx, db, peer, &SyncSet{Artifacts: []*Artifact{forged}})
	if res.Refused["artifacts"] != 1 {
		t.Fatalf("the forged personal todo applied %d and refused %d, want 0 and 1",
			res.Applied["artifacts"], res.Refused["artifacts"])
	}

	if w, err := db.WithheldAuthorship(ctx, peer, false); err != nil || w != nil {
		t.Fatalf("the pushing peer is told %+v, %v about a row personal to somebody "+
			"else - want nil", w, err)
	}
	hers := &Principal{UserID: alice, Project: project}
	w, err := db.WithheldAuthorship(ctx, hers, false)
	if err != nil {
		t.Fatalf("count what was withheld of hers: %v", err)
	}
	if w == nil || w.Rows != 1 || w.Reason != WithheldUnverifiedAuthorship {
		t.Fatalf("alice is told %+v about her own withheld row, want 1 row and %q",
			w, WithheldUnverifiedAuthorship)
	}
}
