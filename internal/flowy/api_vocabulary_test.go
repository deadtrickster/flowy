package flowy

import (
	"context"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// A NARROWING THAT MATCHED NOTHING SAYS WHAT IT NARROWED ON.
//
// ?kind=attachment answers 200 with an empty list. So does ?kind=nonsense-xyz.
// Both are honest and neither is useful: "attachment" is a TYPE and has never
// been a kind, and the door had no way to say so. On 2026-08-20 a seat filed a
// bug against a working filter on the strength of that answer, and two more
// endorsed it before anybody read the vocabulary - "none of those" and "that is
// not one of these" arriving as one value, in a door people use to find things.
//
// NOT A REFUSAL, and the test asserts that too. `kind` is open in the data, so
// a closed set would refuse callers asking about values that exist. What the
// door does is answer the page it was asked for AND say what it would have
// found something under.
func TestAFilterThatMatchedNothingSaysWhatExists(t *testing.T) {
	ctx, s, p, project := listDoor(t)

	// Two kinds, so the vocabulary has something to be.
	kinded := func(kind, title string) {
		t.Helper()
		art := &store.Artifact{
			ID: ulid.NewString(), Type: "memory", Kind: kind, OwnerUser: p.UserID,
			Title: title, Body: "seeded by the vocabulary test",
			Status: "todo", Visibility: "project", Project: &project,
		}
		if err := s.db.UpsertArtifact(ctx, art); err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
	}
	kinded("todo", "a todo in this project")
	kinded("todo", "another todo")
	// note rather than merge: a merge kind is refused without fields.branch, and
	// correctly so - "a merge request with no branch cannot be rebased, gated or
	// fast-forwarded". The vocabulary needs two kinds, not a queue row.
	kinded("note", "a note in this project")

	// A KIND THAT IS NOT ONE. Empty page, and the door says what is.
	code, body := ask(t, ctx, s, p, "kind=attachment&project="+project)
	if code != 200 {
		t.Fatalf("a filter outside the vocabulary answered %d - it is a narrowing, not a refusal", code)
	}
	if got := len(titles(t, body)); got != 0 {
		t.Fatalf("kind=attachment returned %d rows", got)
	}
	vocab, ok := body["vocabulary"].(map[string]any)
	if !ok {
		t.Fatalf("an empty page for a kind nobody has says nothing about kinds: %v", body)
	}
	kindHint, ok := vocab["kind"].(map[string]any)
	if !ok {
		t.Fatalf("the hint does not name the column that was asked about: %v", vocab)
	}
	if got := kindHint["asked"]; got != "attachment" {
		t.Errorf("the hint says asked=%v, want attachment - it must repeat what it narrowed on", got)
	}
	have, ok := kindHint["have"].(map[string]any)
	if !ok {
		t.Fatalf("the hint carries no vocabulary: %v", kindHint)
	}
	if _, ok := have["todo"]; !ok {
		t.Errorf("the vocabulary does not include todo, which this project has two of: %v", have)
	}
	if _, ok := have["note"]; !ok {
		t.Errorf("the vocabulary does not include note: %v", have)
	}

	// A KIND THAT IS ONE, WITH NOTHING UNDER IT HERE, IS A TRUE EMPTY. Saying
	// "these kinds exist" there would imply the caller had asked for a word
	// that is not a kind, which is the opposite of what happened.
	code, body = ask(t, ctx, s, p, "kind=note&status=done&project="+project)
	if code != 200 {
		t.Fatalf("a real kind with no rows answered %d", code)
	}
	if len(titles(t, body)) != 0 {
		t.Fatalf("the fixture has no done note rows")
	}
	if _, ok := body["vocabulary"]; ok {
		t.Errorf("a real kind with nothing under it was told its word does not exist: %v", body["vocabulary"])
	}

	// AND A PAGE THAT ANSWERED CARRIES NO HINT. The question "what else is
	// there" is one nobody asks about a page that returned rows, and computing
	// it would put a second query on every ordinary read.
	code, body = ask(t, ctx, s, p, "kind=todo&project="+project)
	if code != 200 || len(titles(t, body)) == 0 {
		t.Fatalf("kind=todo answered %d with %d rows", code, len(titles(t, body)))
	}
	if _, ok := body["vocabulary"]; ok {
		t.Errorf("a page with rows on it carries a vocabulary hint: %v", body["vocabulary"])
	}
}

// THE VOCABULARY IS WHAT THIS READER WOULD SEE, not what the table holds.
//
// A hint assembled without the permission filter sends somebody to a value they
// cannot read anything under - which is where they started, with an extra step
// and a false explanation.
func TestTheVocabularyIsPermissionFiltered(t *testing.T) {
	ctx, s, p, project := listDoor(t)

	mine := &store.Artifact{
		ID: ulid.NewString(), Type: "memory", Kind: "todo", OwnerUser: p.UserID,
		Title: "readable", Body: "seeded", Status: "todo",
		Visibility: "project", Project: &project,
	}
	if err := s.db.UpsertArtifact(ctx, mine); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Somebody else's, in a project this principal is not in.
	elsewhere := "vocab-elsewhere-" + ulid.NewString()
	if err := s.db.DeclareProject(ctx, &store.Project{ID: elsewhere}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	theirs := &store.Artifact{
		ID: ulid.NewString(), Type: "memory", Kind: "unreachable-kind",
		OwnerUser: "u-" + ulid.NewString(), Title: "not for us", Body: "seeded",
		Status: "todo", Visibility: "project", Project: &elsewhere,
	}
	if err := s.db.UpsertArtifact(ctx, theirs); err != nil {
		t.Fatalf("upsert theirs: %v", err)
	}

	have, err := s.db.ArtifactVocabulary(context.WithValue(ctx, principalKey{}, p), p, "kind", false)
	if err != nil {
		t.Fatalf("vocabulary: %v", err)
	}
	if _, ok := have["unreachable-kind"]; ok {
		t.Errorf("the vocabulary offers a kind whose only rows this reader cannot read: %v", have)
	}
	if have["todo"] < 1 {
		t.Errorf("the vocabulary lost a kind this reader can see: %v", have)
	}

	// AND IT REFUSES A COLUMN IT HAS NO VOCABULARY FOR, rather than building
	// SQL from whatever it was handed. A column name cannot be a bound
	// parameter, so the only safe version of this call is one that cannot be
	// given an arbitrary string.
	if _, err := s.db.ArtifactVocabulary(ctx, p, "body", false); err == nil {
		t.Errorf("ArtifactVocabulary built a query for an arbitrary column name")
	}
}
