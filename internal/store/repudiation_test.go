package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The reading half is pure: which rows a repudiation covers, and which claim it
// is. It must be checkable without a database, because it is what every reader
// will call per row.

func repudiation(t *testing.T, subject, speaker string, from, to int64) *Artifact {
	t.Helper()
	return &Artifact{
		ID: ulid.NewString(), Type: RepudiationType, Visibility: "project",
		Fields: fieldsJSON(t, map[string]any{
			SubjectField: subject, SpeakerField: speaker,
			FromField: from, ToField: to,
		}),
	}
}

func TestARepudiationCoversItsWindowAndNothingElse(t *testing.T) {
	r := repudiation(t, "u-alice", SpeakerSubject, 100, 200)
	reps := []*Artifact{r}

	for _, at := range []int64{100, 150, 200} {
		if Repudiated(reps, "u-alice", at) == nil {
			t.Fatalf("a row at %d is inside the window 100-200 and is not covered", at)
		}
	}
	// CLOSED AT BOTH ENDS, checked at the ends rather than in the middle: an
	// off-by-one at the edge of a compromise is the one row nobody looks at
	// twice.
	for _, at := range []int64{99, 201} {
		if Repudiated(reps, "u-alice", at) != nil {
			t.Fatalf("a row at %d is outside the window 100-200 and is covered", at)
		}
	}
	// SOMEBODY ELSE'S ROWS ARE NOT DISOWNED BY THIS. The window is a range of
	// readings, which every principal writes into at once, so a repudiation
	// that ignored its subject would disown the whole fabric for that period.
	if Repudiated(reps, "u-bob", 150) != nil {
		t.Fatal("alice's repudiation covered a row bob wrote")
	}
}

// A row that cannot be read back unambiguously disowns nothing, whoever sent it.
func TestARepudiationThatCannotBeReadDisownsNothing(t *testing.T) {
	cases := map[string]*Artifact{
		"no subject":       repudiation(t, "", SpeakerSubject, 100, 200),
		"unknown speaker":  repudiation(t, "u-alice", "the auditor", 100, 200),
		"open ended":       repudiation(t, "u-alice", SpeakerSubject, 100, 0),
		"ends before it b": repudiation(t, "u-alice", SpeakerSubject, 200, 100),
	}
	for name, a := range cases {
		if err := CheckRepudiation(a); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	good := repudiation(t, "u-alice", SpeakerOperator, 100, 200)
	if err := CheckRepudiation(good); err != nil {
		t.Fatalf("a well-formed operator repudiation was refused: %v", err)
	}
}

// The two speakers are two different claims and the door decides which one the
// caller may make. Reading the field and believing it would make the whole
// distinction a value anybody can type.
func TestTheSpeakerIsNotTakenOnTheRowsWord(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "repud")
	alice := "u-" + ulid.NewString()
	mallory := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	subject := &Principal{UserID: alice, Project: project}
	operator := &Principal{UserID: "u-" + ulid.NewString(), Project: project, Operator: true}

	// Mallory claims to be speaking as alice.
	a := repudiation(t, alice, SpeakerSubject, 100, 200)
	a.Project, a.OwnerUser = &project, mallory.UserID
	if err := db.WriteRepudiation(ctx, mallory, a, nil); err == nil {
		t.Fatal("somebody else wrote a first-hand repudiation of alice's rows")
	}
	// Mallory is not the operator either, so the weak form is refused too.
	b := repudiation(t, alice, SpeakerOperator, 100, 200)
	b.Project, b.OwnerUser = &project, mallory.UserID
	if err := db.WriteRepudiation(ctx, mallory, b, nil); err == nil {
		t.Fatal("a non-operator spoke for a principal who cannot speak")
	}
	// Alice may disown her own.
	c := repudiation(t, alice, SpeakerSubject, 100, 200)
	c.Project, c.OwnerUser = &project, alice
	if err := db.WriteRepudiation(ctx, subject, c, nil); err != nil {
		t.Fatalf("alice cannot repudiate her own rows: %v", err)
	}
	// And the operator may speak for her, as the weaker claim.
	e := repudiation(t, alice, SpeakerOperator, 300, 400)
	e.Project, e.OwnerUser = &project, operator.UserID
	if err := db.WriteRepudiation(ctx, operator, e, nil); err != nil {
		t.Fatalf("the operator cannot speak for alice: %v", err)
	}

	got, err := db.Repudiations(ctx, subject)
	if err != nil {
		t.Fatalf("read the repudiations: %v", err)
	}
	seen := map[string]string{}
	for _, r := range got {
		seen[r.ID] = RepudiationSpeakerOf(r)
	}
	if seen[c.ID] != SpeakerSubject || seen[e.ID] != SpeakerOperator {
		t.Fatalf("the two claims did not come back as two claims: %v", seen)
	}
	if _, ok := seen[a.ID]; ok {
		t.Fatal("a refused repudiation is in the store")
	}
	// AND THE ROWS THAT LANDED REALLY COVER SOMETHING, so a pass above is not
	// two writes that quietly wrote nothing.
	if Repudiated(got, alice, 150) == nil || Repudiated(got, alice, 350) == nil {
		t.Fatal("the stored repudiations cover neither window they name")
	}
	if Repudiated(got, alice, 250) != nil {
		t.Fatal("a reading between the two windows is covered by neither and reads as covered")
	}
}

// Rotation is the deliberate replacement MintPrincipalKey refuses to do by
// accident, and it is what `flowy principal repudiate` needs for the only
// principals it exists for - the ones that already have a key.
//
// The defect this pins: repudiate shipped calling MintPrincipalKey alone, so it
// worked for a principal with no key and refused for one with a key, with "a
// principal's signing key is not replaced in place". A smoke test using fresh
// names never met it; the browser check for the drawing half met it at once.
func TestRotationReplacesTheKeyAndSaysWhatItReplaced(t *testing.T) {
	ctx, db := open(t)
	who := "u-" + ulid.NewString()

	first, err := db.MintPrincipalKey(ctx, who, nil, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// The refusal that made the command wrong is still there, because a keygen
	// run twice must not silently invalidate what was signed under the first.
	if _, err := db.MintPrincipalKey(ctx, who, nil, 0); err == nil {
		t.Fatal("minting over an existing key was accepted - a rotation by accident")
	}

	second, was, err := db.RotatePrincipalKey(ctx, who, nil)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if equalKeys(first.PublicKey, second.PublicKey) {
		t.Fatal("the key did not change")
	}
	// The old epoch comes back, because the caller has to know where the key it
	// replaced began to say where the disowned window should end.
	if was != first.Epoch {
		t.Fatalf("the rotation reported the old epoch as %d, want %d", was, first.Epoch)
	}
	if second.Epoch <= first.Epoch {
		t.Fatalf("the new epoch %d is not after the old %d", second.Epoch, first.Epoch)
	}
	if !second.Local {
		t.Fatal("the rotated key has no private half here, so this node cannot sign with it")
	}

	// A principal with nothing to rotate is told so rather than quietly given a
	// first key: the caller asked to replace something.
	if _, _, err := db.RotatePrincipalKey(ctx, "u-"+ulid.NewString(), nil); err == nil {
		t.Fatal("rotating a principal with no key here was accepted")
	}
}
