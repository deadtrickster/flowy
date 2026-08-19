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

// A CLOCK READING DOES NOT SURVIVE A JSON NUMBER, and a window that cannot be
// read must disown nothing rather than disown approximately.
//
// Measured on 2026-08-19: a repudiation covering [before+1, epoch] marked the
// message at `before`. encoding/json decodes numbers into float64, a packed
// reading is about 1.17e17, and 2^53 is 9.0e15 - thirteen times smaller - so
// before+1 and before are the same float64 and the window swallowed the one row
// its author had deliberately left out.
//
// Every positive assertion passed. Only the boundary could see it, which is
// what the negative control was for.
func TestAReadingSurvivesTheFieldsBlob(t *testing.T) {
	// A real reading from this node, past 2^53 by a factor of thirteen.
	const from = int64(117119446652354561)
	const to = int64(117119446652354570)

	// AS TEXT, which is what the writer now stores: the digits cross any
	// encoder unchanged.
	asText := &Artifact{
		ID: "01REPUD", Type: RepudiationType,
		Fields: fieldsJSON(t, map[string]any{
			SubjectField: "u-alice", SpeakerField: SpeakerSubject,
			FromField: "117119446652354561", ToField: "117119446652354570",
		}),
	}
	if err := CheckRepudiation(asText); err != nil {
		t.Fatalf("a window written as text was refused: %v", err)
	}
	gotFrom, gotTo := RepudiationWindowOf(asText)
	if gotFrom != from || gotTo != to {
		t.Fatalf("the window read back as [%d, %d], want [%d, %d]", gotFrom, gotTo, from, to)
	}
	// The row one reading BELOW the window is outside it, which is the exact
	// assertion the float64 path got wrong.
	if Repudiated([]*Artifact{asText}, "u-alice", from-1) != nil {
		t.Fatal("the reading one below the window is covered by it")
	}
	if Repudiated([]*Artifact{asText}, "u-alice", from) == nil {
		t.Fatal("the first reading of the window is not covered by it")
	}

	// AS A JSON NUMBER, which is what a writer that has not been fixed - or a
	// peer - may still send. It cannot have survived, so it reads as absent and
	// the row is refused rather than applied with a window off by one.
	asNumber := &Artifact{
		ID: "01REPUD2", Type: RepudiationType,
		Fields: fieldsJSON(t, map[string]any{
			SubjectField: "u-alice", SpeakerField: SpeakerSubject,
			FromField: float64(from), ToField: float64(to),
		}),
	}
	if err := CheckRepudiation(asNumber); err == nil {
		t.Fatal("a window that cannot have survived a float was accepted - " +
			"it would disown a window nobody wrote")
	}
	if Repudiated([]*Artifact{asNumber}, "u-alice", from+1) != nil {
		t.Fatal("an unreadable window disowned a row")
	}

	// AN UNREADABLE WINDOW IS EMPTY, NOT UNIVERSAL, and this is the assertion
	// that catches the catastrophic version of this fix: (0, MaxInt64) is one
	// typo from (0, 0) and would disown every row its subject ever wrote.
	//
	// It is safe by two independent mechanisms - Repudiated refuses a reading
	// at or below zero, and the comparison needs at >= from AND at <= to - and
	// this pins both, so removing either as redundant fails here rather than in
	// front of somebody who has just been impersonated.
	empty := &Artifact{
		ID: "01REPUD4", Type: RepudiationType,
		Fields: fieldsJSON(t, map[string]any{
			SubjectField: "u-alice", SpeakerField: SpeakerSubject,
		}),
	}
	gotFrom, gotTo = RepudiationWindowOf(empty)
	if gotFrom != 0 || gotTo != 0 {
		t.Fatalf("a missing window read as [%d, %d], want [0, 0]", gotFrom, gotTo)
	}
	for _, at := range []int64{1, 100, from, to, 1 << 62} {
		if Repudiated([]*Artifact{empty}, "u-alice", at) != nil {
			t.Fatalf("a zero window disowned the reading %d", at)
		}
	}

	// And a small number still reads, so the guard is about what cannot be
	// represented rather than about numbers in general.
	small := &Artifact{
		ID: "01REPUD3", Type: RepudiationType,
		Fields: fieldsJSON(t, map[string]any{
			SubjectField: "u-alice", SpeakerField: SpeakerSubject,
			FromField: float64(100), ToField: float64(200),
		}),
	}
	if err := CheckRepudiation(small); err != nil {
		t.Fatalf("a window well inside float64's exact range was refused: %v", err)
	}
}

// A repudiation is a fact about a PRINCIPAL, so it marks their rows in every
// project - not only the one it happens to be filed in.
//
// The defect (01M0BNAWCP): the marking path read repudiations through the
// permission filter, which is project-scoped. A subject therefore had to file
// one per project, and any project they forgot kept rendering the thief's rows
// as that person's own word. `flowy principal repudiate` required --project for
// exactly that reason.
func TestARepudiationMarksItsSubjectsRowsInEveryProject(t *testing.T) {
	ctx, db := open(t)

	here := declaredProject(t, ctx, db, "repudhere")
	there := declaredProject(t, ctx, db, "repudthere")
	alice := "u-" + ulid.NewString()
	subject := &Principal{UserID: alice, Project: here}
	// A reader who lives in the OTHER project and has never heard of the one
	// the repudiation was filed in.
	elsewhere := &Principal{UserID: "u-" + ulid.NewString(), Project: there}

	rep := repudiation(t, alice, SpeakerSubject, 100, 1<<40)
	rep.Project, rep.OwnerUser = &here, alice
	if err := db.WriteRepudiation(ctx, subject, rep, nil); err != nil {
		t.Fatalf("write the repudiation: %v", err)
	}

	// THE READER CANNOT OPEN THE ROW ITSELF, which is what makes this the real
	// case rather than a restatement: the list surface still filters.
	visible, err := db.Repudiations(ctx, elsewhere)
	if err != nil {
		t.Fatalf("list repudiations: %v", err)
	}
	for _, r := range visible {
		if r.ID == rep.ID {
			t.Fatal("a reader in another project can open the repudiation row, " +
				"so this test is not measuring the marking path")
		}
	}

	// And it is still applied to alice's row, because the mark annotates a row
	// this reader can already read rather than revealing one they cannot.
	all, err := db.LiveRepudiations(ctx)
	if err != nil {
		t.Fatalf("read every repudiation: %v", err)
	}
	hers := &Artifact{ID: ulid.NewString(), OwnerUser: alice, HLC: 500}
	his := &Artifact{ID: ulid.NewString(), OwnerUser: "u-" + ulid.NewString(), HLC: 500}
	if err := db.FillDisowned(ctx, []*Artifact{hers, his}, nil); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if hers.Disowned == nil {
		t.Fatal("alice's row in another project was not marked, which is the whole defect")
	}
	if hers.Disowned.By != rep.ID {
		t.Fatalf("marked by %q, want the repudiation %q", hers.Disowned.By, rep.ID)
	}
	// The negative control that has ridden with this feature from the start: a
	// window is a range of readings and everybody writes into it at once.
	if his.Disowned != nil {
		t.Fatal("somebody else's row in the same window was marked")
	}
	if len(all) == 0 {
		t.Fatal("the unfiltered read came back empty")
	}
}

// What this node holds and cannot apply is reported, not swallowed.
//
// A repudiation with an unreadable window is dropped by every other surface,
// which is safe and silent - and silence makes "nobody disowned this" and
// "somebody disowned this and I cannot tell whom" the same answer. The place to
// say so is the surface whose reader came to look at repudiations.
func TestWhatCannotBeReadIsCounted(t *testing.T) {
	ctx, db := open(t)

	project := declaredProject(t, ctx, db, "unreadable")
	alice := "u-" + ulid.NewString()
	subject := &Principal{UserID: alice, Project: project}

	// NOTHING HELD IS ABSENT, NOT ZERO - the shape withheld and refused use, so
	// a reader can tell "none" from "this answer does not carry the number".
	// Asked before anything is written, because after it the answer is the same
	// either way.
	before, err := db.UnreadableRepudiations(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if before != nil {
		for _, id := range before.IDs {
			t.Logf("an earlier run left an unreadable repudiation: %s", id)
		}
	}

	// A window written as a large JSON number - what a peer with a different
	// encoder sends, and what this node's own writer produced before 30b5c8b.
	bad := &Artifact{
		ID: ulid.NewString(), Type: RepudiationType, Visibility: "project",
		Project: &project, OwnerUser: alice,
		Title: "unreadable by construction",
		Fields: fieldsJSON(t, map[string]any{
			SubjectField: alice, SpeakerField: SpeakerSubject,
			FromField: float64(117119446652354561), ToField: float64(117119446652354570),
		}),
	}
	// Written around the door, because the door is what refuses it - the case
	// under test is a row that is ALREADY here, from a peer or an older writer.
	if err := db.UpsertArtifact(ctx, bad); err != nil {
		t.Fatalf("plant the unreadable row: %v", err)
	}

	got, err := db.UnreadableRepudiations(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got == nil {
		t.Fatal("an unreadable repudiation is held and the count says nothing")
	}
	found := false
	for _, id := range got.IDs {
		if id == bad.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the ids %v do not name the unreadable row %s - a count alone cannot be acted on",
			got.IDs, bad.ID)
	}

	// AND IT IS STILL APPLIED TO NOBODY. Reporting it must not be the step that
	// starts trusting it.
	live, err := db.LiveRepudiations(ctx)
	if err != nil {
		t.Fatalf("live: %v", err)
	}
	for _, r := range live {
		if r.ID == bad.ID {
			t.Fatal("the unreadable repudiation is in the list used to mark rows")
		}
	}
	row := &Artifact{ID: ulid.NewString(), OwnerUser: alice, HLC: 117119446652354565}
	if err := db.FillDisowned(ctx, []*Artifact{row}, nil); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if row.Disowned != nil {
		t.Fatal("a row inside the unreadable window was marked, on evidence nobody can read")
	}
	_ = subject
}
