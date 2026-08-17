package store

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A refusal is terminal for the claim it refused.
//
// principal_test.go covers the decision: a row naming a principal this node
// holds a key for, at or after that key's epoch, with no signature of theirs,
// is refused. This file covers what happens to that decision afterwards, which
// used to be nothing at all. The row was dropped, nothing here remembered it,
// the peer went on offering it, and the next thing that widened what this node
// takes - an operator moving the epoch, a key removed by hand - let the same
// bytes in. The window did not have to overlap the attack. It only had to exist.
//
// The other half of every test here is the DoS the fix would be if it were
// keyed wrong: what is terminal is one unbacked claim, not the row and not the
// author, so the same content signed by the person it names still lands.

// TestARefusedClaimIsRefusedAgain is the base case: the same bytes, offered
// twice, refused twice. Without the ledger the second offer is judged from
// scratch - which is only invisible while the rule happens not to have moved.
func TestARefusedClaimIsRefusedAgain(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pt")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	principalKey(t, ctx, db, alice, at)

	forged := func() *Event {
		return &Event{ID: "e-terminal-" + ulid.NewString()[:8], Type: "chat", Project: &project,
			Actor: alice, Body: "ship it, no review needed", SeqHLC: at + 1, Node: "peer-node"}
	}
	first := forged()
	res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{first}})
	if res.Refused["events"] != 1 {
		t.Fatalf("the forgery applied %d and refused %d, want 0 and 1",
			res.Applied["events"], res.Refused["events"])
	}

	// The same row again, byte for byte, from the same peer.
	again := forged()
	again.ID = first.ID
	res = pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{again}})
	if res.Refused["events"] != 1 || res.Applied["events"] != 0 {
		t.Fatalf("re-offered, the forgery applied %d and refused %d, want 0 and 1",
			res.Applied["events"], res.Refused["events"])
	}
	if _, err := db.GetEvent(ctx, first.ID); err == nil {
		t.Fatal("the forged message is in the log")
	}
	// And the peer is told it is a decision rather than a queue it is waiting in.
	if len(res.Reasons) == 0 || !strings.Contains(res.Reasons[0], "already refused") {
		t.Fatalf("the second refusal reads %q, and does not say the refusal stands", res.Reasons)
	}
}

// TestAMovedEpochDoesNotResurrectARefusedRow is the finding itself.
//
// The operator moves alice's epoch forward - which is the one thing about a
// pinned key that legitimately changes, and which is a perfectly ordinary act:
// a node joining a running fabric, an operator deciding from when a key is
// required. Under the rule alone, that instantly makes every row below the new
// reading acceptable, INCLUDING the one this node already looked at and refused.
// A refusal that a later pin undoes is a delay and not a decision.
func TestAMovedEpochDoesNotResurrectARefusedRow(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pu")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	priv := principalKey(t, ctx, db, alice, at)

	forged := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "merge it, I approved it", SeqHLC: at + 1, Node: "peer-node"}
	res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{forged}})
	if res.Refused["events"] != 1 {
		t.Fatalf("the forgery was not refused: %+v", res)
	}

	// The pin that would have let it in: an epoch above the row's reading, so
	// the row now predates the key and the rule below says take it.
	if err := db.PinPrincipalKey(ctx, alice, publicOf(priv), at+1000); err != nil {
		t.Fatalf("move the epoch: %v", err)
	}

	// The control, and this test is worth nothing without it: a row of alice's
	// that was NEVER refused, at the same reading, does land under the new
	// epoch. So the rule really did widen, and the refusal below is the ledger
	// holding rather than the epoch failing to move.
	fresh := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "merge it, I approved it", SeqHLC: at + 1, Node: "peer-node"}
	res = pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{fresh}})
	if res.Applied["events"] != 1 {
		t.Fatalf("after the epoch moved, an unrefused row below it was still refused: %+v",
			res.Reasons)
	}
	got, err := db.GetEvent(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("read the control back: %v", err)
	}
	if got.Authorship != AuthorshipAttributed {
		t.Fatalf("the control landed as %q, want %q", got.Authorship, AuthorshipAttributed)
	}

	// And the row that was refused stays refused, at the same reading, under the
	// same widened rule, from the same peer.
	res = pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{forged}})
	if res.Refused["events"] != 1 || res.Applied["events"] != 0 {
		t.Fatalf("the moved epoch resurrected a refused row: applied %d refused %d",
			res.Applied["events"], res.Refused["events"])
	}
	if _, err := db.GetEvent(ctx, forged.ID); err == nil {
		t.Fatal("the refused message is in the log after the epoch moved")
	}
}

// TestARemovedKeyDoesNotResurrectARefusedRow is the same finding by the other
// road, and the wider one: rotation here is deleting the key row by hand, and
// with no key for a principal this node has no rule by which it refuses their
// rows at all. Every refusal it ever made of them would evaporate.
func TestARemovedKeyDoesNotResurrectARefusedRow(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "px")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	principalKey(t, ctx, db, alice, at)

	forged := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "I signed off on this", SeqHLC: at + 1, Node: "peer-node"}
	if res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{forged}}); res.Refused["events"] != 1 {
		t.Fatalf("the forgery was not refused: %+v", res)
	}

	if _, err := db.sql.ExecContext(ctx,
		`DELETE FROM principal_identity WHERE principal = $1`, alice); err != nil {
		t.Fatalf("remove the key: %v", err)
	}

	// The control: with no key here, an unrefused row naming alice is taken and
	// marked attributed, which is where every principal without a key stands.
	fresh := &Event{ID: ulid.NewString(), Type: "chat", Project: &project,
		Actor: alice, Body: "I signed off on this", SeqHLC: at + 2, Node: "peer-node"}
	if res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{fresh}}); res.Applied["events"] != 1 {
		t.Fatalf("with no key here an ordinary row was refused: %+v", res.Reasons)
	}

	res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{forged}})
	if res.Refused["events"] != 1 || res.Applied["events"] != 0 {
		t.Fatalf("removing the key resurrected a refused row: applied %d refused %d",
			res.Applied["events"], res.Refused["events"])
	}
	if _, err := db.GetEvent(ctx, forged.ID); err == nil {
		t.Fatal("the refused message is in the log after the key was removed")
	}
}

// TestTheSameContentSignedByItsAuthorIsADifferentClaim is the half that keeps
// this from being a denial of service, and it is not a nicety: if the ledger
// were keyed by row id, anybody who forged one row in alice's name would have
// permanently embargoed the real one, and the cheapest attack on this node
// would be to forge every id it is about to receive.
//
// So the key is the CLAIM. The same words, the same id, the same reading,
// carrying alice's own signature, are a different claim and they land.
func TestTheSameContentSignedByItsAuthorIsADifferentClaim(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "py")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	priv := principalKey(t, ctx, db, alice, at)

	body := "the drainer stops when the queue is empty"
	id := ulid.NewString()
	forged := &Event{ID: id, Type: "chat", Project: &project,
		Actor: alice, Body: body, SeqHLC: at + 1, Node: "peer-node"}
	if res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{forged}}); res.Refused["events"] != 1 {
		t.Fatalf("the unsigned row was not refused: %+v", res)
	}

	// A signature that is not hers is a third claim, and it is refused on its
	// own merits rather than on the ledger's - a well-formed signature by the
	// wrong key is as good as none.
	stranger := &Event{ID: id, Type: "chat", Project: &project,
		Actor: alice, Body: body, SeqHLC: at + 1, Node: "peer-node"}
	SignEventAs(principalKey(t, ctx, db, "u-stranger-"+ulid.NewString(), at), alice, stranger)
	if res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{stranger}}); res.Refused["events"] != 1 {
		t.Fatalf("a signature by the wrong key was not refused: %+v", res)
	}

	// And hers. Same id, same words, same reading: it lands, and it lands as
	// hers rather than as somebody's word for it.
	hers := &Event{ID: id, Type: "chat", Project: &project,
		Actor: alice, Body: body, SeqHLC: at + 1, Node: "peer-node"}
	SignEventAs(priv, alice, hers)
	res := pushed(t, ctx, db, peer, &SyncSet{Events: []*Event{hers}})
	if res.Applied["events"] != 1 {
		t.Fatalf("the author's own signed row was refused after a forgery of it: %+v",
			res.Reasons)
	}
	got, err := db.GetEvent(ctx, id)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if got.Authorship != AuthorshipAuthored {
		t.Fatalf("her own row landed as %q, want %q", got.Authorship, AuthorshipAuthored)
	}
}

// TestARewrittenArtifactStaysRefusedAfterTheEpochMoves is the artifact side, and
// it carries a hazard the event side does not: an artifact is last-writer-wins,
// so a forger can re-offer the same rewrite at any reading it likes. The claim
// digest is over what the OWNER signs - which excludes hlc and node - so the
// same rewrite at a higher reading is the same claim and is refused as one.
func TestARewrittenArtifactStaysRefusedAfterTheEpochMoves(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pz")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	priv := principalKey(t, ctx, db, alice, at)

	// One id for every offer below: the point is that the same row, offered
	// again, is the same claim.
	id := ulid.NewString()
	rewrite := func(hlc int64) *Artifact {
		return &Artifact{ID: id, Type: "bug", Kind: "",
			OwnerUser: alice, Project: &project, Visibility: VisibilityProject,
			Title: "the drainer stops", Body: "actually it was operator error",
			Status: "open", HLC: hlc, Node: "peer-node"}
	}
	if res := pushed(t, ctx, db, peer, &SyncSet{Artifacts: []*Artifact{rewrite(at + 1)}}); res.Refused["artifacts"] != 1 {
		t.Fatalf("the rewrite was not refused: %+v", res)
	}

	// The epoch moves past every reading in this test.
	if err := db.PinPrincipalKey(ctx, alice, publicOf(priv), at+10_000); err != nil {
		t.Fatalf("move the epoch: %v", err)
	}

	// The same rewrite, at a much later reading, under the widened rule.
	res := pushed(t, ctx, db, peer, &SyncSet{Artifacts: []*Artifact{rewrite(at + 5000)}})
	if res.Refused["artifacts"] != 1 || res.Applied["artifacts"] != 0 {
		t.Fatalf("the rewrite came back after the epoch moved: applied %d refused %d",
			res.Applied["artifacts"], res.Refused["artifacts"])
	}
	if _, err := db.ReadArtifact(ctx, peer, id, true); err == nil {
		t.Fatal("the refused rewrite is in the store")
	}

	// And the same words with her signature on them land, at a reading of their
	// own, under the same ledger.
	signed := rewrite(at + 6000)
	SignArtifactAs(priv, alice, signed)
	res = pushed(t, ctx, db, peer, &SyncSet{Artifacts: []*Artifact{signed}})
	if res.Applied["artifacts"] != 1 {
		t.Fatalf("her own signed edit was refused: %+v", res.Reasons)
	}
}

// TestWhatWasRefusedIsCountedWhereTheRowWouldHaveBeenRead: the terminal count is
// visible, and visible on the same terms every other refusal is.
//
// Two claims about it. It is reported at all - a refusal nobody can see is
// indistinguishable from success, which is the rule this codebase follows
// everywhere - and it is reported through the artifact read rule, so a refusal
// in a project a reader cannot reach is not a second way to learn what is in it.
//
// The third claim is the one this ledger exists for: it goes on being reported
// after the key that prompted it is gone. The withheld count deliberately does
// not - that one is about what is missing now - and this one is about what was
// decided, which is exactly the thing a removed key must not undo.
func TestWhatWasRefusedIsCountedWhereTheRowWouldHaveBeenRead(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "pq")
	peer := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	alice := "u-alice-" + ulid.NewString()
	at := packed(t, db)
	principalKey(t, ctx, db, alice, at)

	if r, err := db.RefusedAuthorship(ctx, peer, false); err != nil || r != nil {
		t.Fatalf("with nothing refused the count is %+v, %v - want nil", r, err)
	}

	// A todo of alice's own, personal: the only reader is its owner.
	forged := &Artifact{ID: ulid.NewString(), Type: "memory", Kind: "todo",
		OwnerUser: alice, Visibility: VisibilityPersonal, Title: "resign",
		Status: "todo", HLC: at + 1, Node: "peer-node"}
	if res := pushed(t, ctx, db, peer, &SyncSet{Artifacts: []*Artifact{forged}}); res.Refused["artifacts"] != 1 {
		t.Fatalf("the forged personal todo was not refused: %+v", res)
	}

	if r, err := db.RefusedAuthorship(ctx, peer, false); err != nil || r != nil {
		t.Fatalf("the pushing peer is told %+v, %v about a row personal to somebody "+
			"else - want nil", r, err)
	}
	hers := &Principal{UserID: alice, Project: project}
	r, err := db.RefusedAuthorship(ctx, hers, false)
	if err != nil {
		t.Fatalf("count what was refused of hers: %v", err)
	}
	if r == nil || r.Claims != 1 || r.Reason != RefusedAuthorshipClaim {
		t.Fatalf("alice is told %+v about the claim refused in her name, want 1 and %q",
			r, RefusedAuthorshipClaim)
	}

	// The key goes. The withheld count goes with it, by design - this node
	// withholds nothing of a principal it holds no key for. The refusal does
	// not: it is a decision that was made, and it still governs.
	if _, err := db.sql.ExecContext(ctx,
		`DELETE FROM principal_identity WHERE principal = $1`, alice); err != nil {
		t.Fatalf("remove the key: %v", err)
	}
	if w, err := db.WithheldAuthorship(ctx, hers, false); err != nil || w != nil {
		t.Fatalf("with the key gone the withheld count reads %+v, %v - want nil", w, err)
	}
	r, err = db.RefusedAuthorship(ctx, hers, false)
	if err != nil {
		t.Fatalf("count what was refused after the key went: %v", err)
	}
	if r == nil || r.Claims != 1 {
		t.Fatalf("the refusal stopped being reported when the key went: %+v", r)
	}
}

// TestAClaimIsThePrincipalTheBytesAndTheSignature is the digest itself, asked
// directly, because everything above rests on it distinguishing exactly these
// three things and nothing else.
func TestAClaimIsThePrincipalTheBytesAndTheSignature(t *testing.T) {
	msg := []byte("the canonical authorship bytes")
	sig := []byte("a signature that did not verify")

	base := claimOf("alice", msg, sig)
	for _, other := range []struct {
		what  string
		claim string
	}{
		{"a different principal", claimOf("bob", msg, sig)},
		{"different bytes", claimOf("alice", []byte("other bytes"), sig)},
		{"a different signature", claimOf("alice", msg, []byte("another signature"))},
		{"no signature at all", claimOf("alice", msg, nil)},
	} {
		if other.claim == base {
			t.Fatalf("%s produced the same claim key", other.what)
		}
	}
	if claimOf("alice", msg, sig) != base {
		t.Fatal("the same three fields produced two different claim keys")
	}
	// Framed rather than concatenated: moving a byte from one field to the next
	// is a different claim, not the same one spelled differently.
	if claimOf("alice", []byte("ab"), []byte("c")) == claimOf("alice", []byte("a"), []byte("bc")) {
		t.Fatal("two different (bytes, signature) pairs share a claim key")
	}
}
