package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestEvidenceVocabularyAndRendering is the pure half: what the vocabulary
// accepts, what a row saying nothing reads as, and what an entry looks like on a
// surface that knows nothing about this event type. No DATABASE_URL needed.
func TestEvidenceVocabularyAndRendering(t *testing.T) {
	for _, tc := range []struct{ asked, want string }{
		{"source", EvidenceSource},
		{"  REPRODUCED ", EvidenceReproduced},
		{"Verified", EvidenceVerified},
		// The way the axis is NAMED in prose, which is what somebody who has
		// just read that sentence types. One stored word, several accepted.
		{"verified-on-a-commit", EvidenceVerified},
	} {
		got, err := NormalizeEvidenceState(tc.asked)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeEvidenceState(%q) = %q, %v; want %q", tc.asked, got, err, tc.want)
		}
	}
	// EMPTY IS REFUSED, which is where this axis differs from the filing one:
	// "nobody has said" is what a row carrying no keys already reads as, and a
	// verb that accepted it would be a way of writing an assertion that asserts
	// nothing.
	if _, err := NormalizeEvidenceState("  "); err == nil {
		t.Error("an empty evidence state was accepted; unstated is the absence of a claim, " +
			"not a claim somebody makes")
	} else if _, ok := err.(DepRefusal); !ok {
		t.Errorf("an empty state is the caller's mistake, so it must be a DepRefusal: %T", err)
	}
	if _, err := NormalizeEvidenceState("confirmed"); err == nil {
		t.Error("a word outside the vocabulary was accepted; the whole point is that a " +
			"reader can count what has actually been run")
	}

	// A finding carrying no fields at all - 40 of 40 in the corpus the day this
	// landed - has NOT been judged, and that is different from source.
	if got := FindingEvidenceOf(&Artifact{ID: "f1"}); got.Stated() || got.Ran() {
		t.Errorf("a row with no fields read as %+v; want an unstated claim", got)
	}
	if got := FindingEvidenceOf(nil); got.Stated() {
		t.Errorf("a nil artifact read as %+v; want an unstated claim", got)
	}
	read := FindingEvidenceOf(&Artifact{ID: "f1", Fields: []byte(
		`{"evidence_state":"verified","verified_on":"67adbe04","verified_at":"2026-08-07T00:00:00Z"}`)})
	if !read.Stated() || !read.Ran() || read.VerifiedOn != "67adbe04" {
		t.Errorf("a verified row read as %+v", read)
	}
	// source is a claim about the code and nobody ran anything for it.
	if got := FindingEvidenceOf(&Artifact{ID: "f1", Fields: []byte(
		`{"evidence_state":"source"}`)}); !got.Stated() || got.Ran() {
		t.Errorf("a source row read as %+v; want stated and not run", got)
	}

	// verified_at is normalised, never stored as free text: a console sorts it
	// and an importer is checked against it.
	if at, err := normalizeVerifiedAt("2026-08-07"); err != nil || at != "2026-08-07T00:00:00Z" {
		t.Errorf("normalizeVerifiedAt(date) = %q, %v", at, err)
	}
	if _, err := normalizeVerifiedAt("last tuesday"); err == nil {
		t.Error("free text was accepted as verified_at")
	}

	// The body names the commit at both ends, so a re-run reads as two commits
	// and one story, and a row nobody had judged says so rather than reading as
	// an empty half.
	if got := evidenceBody(
		Evidence{}, Evidence{State: EvidenceSource}); got != "not stated->source" {
		t.Errorf("body = %q", got)
	}
	if got := evidenceBody(
		Evidence{State: EvidenceVerified, VerifiedOn: "67adbe04"},
		Evidence{State: EvidenceVerified, VerifiedOn: "1fa4374"},
	); got != "verified on 67adbe04->verified on 1fa4374" {
		t.Errorf("body = %q", got)
	}
}

// TestFindingEvidenceRoundTrip is the store half, and it walks the transitions
// the corpus and the filing rule actually contain: nobody has said, read the
// code, ran it, ran it against a named commit, and the walk back when a repro
// stops reproducing.
func TestFindingEvidenceRoundTrip(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "evidence")
	owner := &User{Handle: "prover-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}

	finding := &Artifact{
		Type: "finding", Project: &project, OwnerUser: owner.ID,
		Title: "tiktoken endoftext raises",
	}
	if err := db.UpsertArtifact(ctx, finding); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}

	// NOBODY HAS SAID is the common case and costs nothing: no keys, no call,
	// and it is not source.
	if got := FindingEvidenceOf(finding); got.Stated() {
		t.Fatalf("a fresh finding read as %+v; want an unstated claim", got)
	}

	// THE REFUSAL THE WHOLE AXIS EXISTS FOR: verified is a word plus a commit,
	// and without one it is reproduced spelled more confidently.
	if _, _, err := db.SetFindingEvidence(ctx, p, finding.ID, Evidence{
		State: EvidenceVerified,
	}); err == nil {
		t.Fatal("verified with no commit was accepted; the sha is the content of the word")
	} else if _, ok := err.(DepRefusal); !ok {
		t.Errorf("a verified claim with no commit is the caller's mistake: %T", err)
	}
	// And a commit under source, which is nobody having run it, so there is no
	// run for the commit to be the commit OF.
	if _, _, err := db.SetFindingEvidence(ctx, p, finding.ID, Evidence{
		State: EvidenceSource, VerifiedOn: "67adbe04",
	}); err == nil {
		t.Fatal("source was allowed to name the commit a run happened on")
	}
	// And a time with no commit to be the time OF, which is refused rather than
	// dropped: a value quietly discarded is a writer believing a fact is
	// recorded when it is not.
	if _, _, err := db.SetFindingEvidence(ctx, p, finding.ID, Evidence{
		State: EvidenceReproduced, VerifiedAt: "2026-08-07",
	}); err == nil {
		t.Fatal("verified_at was accepted on a claim that names no commit")
	}
	if got := FindingEvidenceOf(mustRead(t, ctx, db, p, finding.ID)); got.Stated() {
		t.Fatalf("a refused write moved the row: %+v", got)
	}

	// SOURCE: somebody read the code. 36 of the 40 imported findings are this.
	art, entry, err := db.SetFindingEvidence(ctx, p, finding.ID, Evidence{State: EvidenceSource})
	if err != nil {
		t.Fatalf("record source: %v", err)
	}
	if got := FindingEvidenceOf(art); got.State != EvidenceSource || got.Ran() ||
		got.VerifiedOn != "" || got.VerifiedAt != "" {
		t.Fatalf("after reading the code the row reads %+v", got)
	}
	if entry.Type != EventFindingEvidence || entry.Artifact != finding.ID {
		t.Errorf("entry = %+v", entry)
	}

	// REPRODUCED with no commit recorded, which is most of what got run before
	// anybody wrote down what it was run against.
	art, _, err = db.SetFindingEvidence(ctx, p, finding.ID, Evidence{State: EvidenceReproduced})
	if err != nil {
		t.Fatalf("record reproduced: %v", err)
	}
	if got := FindingEvidenceOf(art); !got.Ran() || got.VerifiedOn != "" || got.VerifiedAt != "" {
		t.Fatalf("after running it the row reads %+v", got)
	}

	// VERIFIED, with the commit and the day the run was made - stated, because a
	// claim imported from a file somebody wrote in August keeps its own date.
	art, _, err = db.SetFindingEvidence(ctx, p, finding.ID, Evidence{
		State: EvidenceVerified, VerifiedOn: "67adbe04", VerifiedAt: "2026-08-07",
		LastRun: "run-3",
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	verified := FindingEvidenceOf(art)
	if verified.State != EvidenceVerified || verified.VerifiedOn != "67adbe04" ||
		verified.VerifiedAt != "2026-08-07T00:00:00Z" || verified.LastRun != "run-3" {
		t.Fatalf("after verifying the row reads %+v", verified)
	}
	// OUR lifecycle is untouched by how sure we are, which is the whole point of
	// three axes.
	if art.Status != finding.Status {
		t.Errorf("recording evidence moved our status to %q", art.Status)
	}

	// A restatement over the same commit is somebody confirming it, and it must
	// not look like a fresh run: the day the run was made stands.
	art, _, err = db.SetFindingEvidence(ctx, p, finding.ID, Evidence{State: EvidenceVerified})
	if err != nil {
		t.Fatalf("restate: %v", err)
	}
	if got := FindingEvidenceOf(art); got.VerifiedOn != "67adbe04" ||
		got.VerifiedAt != "2026-08-07T00:00:00Z" || got.LastRun != "run-3" {
		t.Fatalf("an update that stated only the word lost what it rested on: %+v", got)
	}

	// WALKING IT BACK. A repro that stops reproducing is a real thing to record,
	// the row stops carrying a commit that no longer backs anything, and the log
	// still names the one the old claim rested on.
	art, _, err = db.SetFindingEvidence(ctx, p, finding.ID, Evidence{State: EvidenceSource})
	if err != nil {
		t.Fatalf("walk back: %v", err)
	}
	if got := FindingEvidenceOf(art); got.State != EvidenceSource || got.VerifiedOn != "" ||
		got.VerifiedAt != "" || got.LastRun != "" {
		t.Fatalf("after walking back the row reads %+v; the commit must not stand under source", got)
	}

	log, err := db.FindingEvidenceLog(ctx, p, finding.ID)
	if err != nil {
		t.Fatalf("evidence log: %v", err)
	}
	if len(log) != 5 {
		t.Fatalf("the log has %d entries; want the five claims that were recorded", len(log))
	}
	if log[0].From != "" || log[0].State != EvidenceSource {
		t.Errorf("the first entry reads %+v; want a move out of an unstated claim", log[0])
	}
	last := log[len(log)-1]
	if last.State != EvidenceSource || last.FromOn != "67adbe04" {
		t.Errorf("the walk-back entry reads %+v; want it to name the commit the old claim "+
			"rested on, which is the only place that survives", last)
	}
}
