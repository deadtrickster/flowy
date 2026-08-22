package store

// The validate arm's wire side (p4): the checks run pure where a refusal is a
// sentence, and the cached verdict travels the row - written through the
// ordinary path, read back whole, read by the complete arm, and compared by
// the files hash so it can never outlive the files it covered.

import (
	"context"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// cleanChangeFiles is a change that validates: one delta, one task citing it,
// a requirement with a scenario, and a spec row to back the delta.
func cleanChangeFiles() map[string]string {
	return map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "- [ ] 1.1 Add a delta to specs/session/spec.md\n",
		"specs/session/spec.md": "## ADDED Requirements\n\n" +
			"### Requirement: Sessions remember\n" +
			"A session SHALL remember its reader.\n\n" +
			"#### Scenario: remembered\n" +
			"- **WHEN** a reader returns\n- **THEN** the session is found\n",
	}
}

// specIn files a spec row the way the doors' callers do, for the
// delta-names-a-spec check.
func specIn(t *testing.T, ctx context.Context, db *DB, p *Principal, project, capability string) {
	t.Helper()
	spec := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: SpecKind,
		Project: &project, OwnerUser: p.UserID,
		Title: capability, Body: "# " + capability + "\n",
	}
	if err := db.CreateArtifact(ctx, spec); err != nil {
		t.Fatalf("write spec %s: %v", capability, err)
	}
}

// mustFiles is OpenspecFilesOf with the error in the test's voice.
func mustFiles(t *testing.T, a *Artifact) map[string]string {
	t.Helper()
	files, err := OpenspecFilesOf(a)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	return files
}

// closeDerivedTodos closes every todo a change derived, the way a caller
// finishes the tasks before asking for complete.
func closeDerivedTodos(t *testing.T, ctx context.Context, db *DB, p *Principal, change string) {
	t.Helper()
	byLine := derivedRowsByLine(t, ctx, db, change)
	if len(byLine) == 0 {
		t.Fatal("the change derived no todos to close")
	}
	for _, row := range byLine {
		if _, _, err := db.SetTodoStatus(ctx, p, row.ID, DoneStatus, "done"); err != nil {
			t.Fatalf("close %s: %v", row.ID, err)
		}
	}
}

// cacheVerdict validates a stored change and caches the verdict through the
// ordinary write path, the way the validate door does.
func cacheVerdict(t *testing.T, ctx context.Context, db *DB, p *Principal, id string) {
	t.Helper()
	stored := storedChange(t, ctx, db, p, id)
	verdict, err := db.ValidateOpenspecChange(ctx, stored)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := SetOpenspecValidation(stored, verdict); err != nil {
		t.Fatalf("set verdict: %v", err)
	}
	if err := db.UpsertArtifact(ctx, stored); err != nil {
		t.Fatalf("cache the verdict: %v", err)
	}
}

func TestOpenspecValidatePassesACleanChange(t *testing.T) {
	problems := checkOpenspecValidate(cleanChangeFiles(), map[string]bool{"session": true})
	if len(problems) != 0 {
		t.Fatalf("a clean change refuses: %v", problems)
	}
}

// Each check's refusal in its own words, pure over the files map - the
// sentences the complete arm refuses with verbatim.
func TestOpenspecValidateRefusalsInTheirOwnWords(t *testing.T) {
	clean := cleanChangeFiles()
	cases := []struct {
		name  string
		edit  func(map[string]string)
		specs map[string]bool
		want  string
	}{
		{
			name:  "no tasks",
			edit:  func(f map[string]string) { delete(f, "tasks.md") },
			specs: map[string]bool{"session": true},
			want:  "tasks.md is absent or empty",
		},
		{
			name:  "task names no delta",
			edit:  func(f map[string]string) { f["tasks.md"] = "- [ ] 1.1 write a poem\n" },
			specs: map[string]bool{"session": true},
			want:  "names no delta",
		},
		{
			name:  "delta names no spec",
			edit:  func(f map[string]string) {},
			specs: map[string]bool{},
			want:  `names no spec - the capability "session"`,
		},
		{
			name: "requirement without scenario",
			edit: func(f map[string]string) {
				f["specs/session/spec.md"] = "## ADDED Requirements\n\n" +
					"### Requirement: Lonely\nnothing follows\n"
			},
			specs: map[string]bool{"session": true},
			want:  `requirement "Lonely" in specs/session/spec.md has no scenario`,
		},
		{
			name: "delta with no requirements",
			edit: func(f map[string]string) {
				f["specs/session/spec.md"] = "## ADDED Requirements\n\nprose only\n"
			},
			specs: map[string]bool{"session": true},
			want:  "holds no requirements",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := make(map[string]string, len(clean))
			for k, v := range clean {
				files[k] = v
			}
			tc.edit(files)
			problems := checkOpenspecValidate(files, tc.specs)
			if len(problems) == 0 {
				t.Fatal("the change validated - nothing refused")
			}
			joined := strings.Join(problems, "; ")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("the refusal is %q, not %q", joined, tc.want)
			}
		})
	}
}

// The hash is the cache's clock: an edit moves it, an insertion-order shuffle
// does not, so the compare reads the map and never the marshal.
func TestOpenspecFilesHashFollowsTheFiles(t *testing.T) {
	files := cleanChangeFiles()
	h1 := openspecFilesHash(files)

	shuffled := map[string]string{}
	for _, k := range []string{"specs/session/spec.md", "proposal.md", "tasks.md"} {
		shuffled[k] = files[k]
	}
	if openspecFilesHash(shuffled) != h1 {
		t.Fatal("the hash follows insertion order, not the map")
	}

	edited := cleanChangeFiles()
	edited["proposal.md"] = "# a different why\n"
	if openspecFilesHash(edited) == h1 {
		t.Fatal("an edit left the hash alone")
	}
}

// The cached verdict travels the row: absent refuses with the door sentence,
// cached survives the ordinary write (and the funnel keeps the state), and
// complete goes through once the tasks are closed.
func TestOpenspecValidateVerdictTravelsToTheCompleteArm(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-validate-travel")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	specIn(t, ctx, db, p, project, "session")
	art := openspecChangeIn(t, ctx, db, p, project, cleanChangeFiles())
	openspecMove(t, ctx, db, art, OpenspecInProgress)

	// The tasks arm runs before the validate arm, so the absent verdict only
	// gets its refusal once the work is closed.
	closeDerivedTodos(t, ctx, db, p, art.ID)
	stored := storedChange(t, ctx, db, p, art.ID)
	err := db.CheckOpenspecTransition(ctx, stored, OpenspecComplete)
	if err == nil || !strings.Contains(err.Error(), "has not been validated - run POST /api/openspec/{id}/validate") {
		t.Fatalf("unvalidated complete: %v", err)
	}

	cacheVerdict(t, ctx, db, p, art.ID)

	stored = storedChange(t, ctx, db, p, art.ID)
	if stateOf(t, ctx, db, p, art.ID) != OpenspecInProgress {
		t.Fatal("the validate write moved the lifecycle state - the funnel did not carry it")
	}
	cached, err := OpenspecValidationOf(stored)
	if err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	if cached == nil || !cached.Ok || cached.FilesHash != openspecFilesHash(mustFiles(t, stored)) {
		t.Fatalf("the cached verdict did not survive the write: %+v", cached)
	}

	closeDerivedTodos(t, ctx, db, p, art.ID)
	stored = storedChange(t, ctx, db, p, art.ID)
	if err := db.CheckOpenspecTransition(ctx, stored, OpenspecComplete); err != nil {
		t.Fatalf("a validated change with tasks done refused complete: %v", err)
	}
}

// A verdict that outlives the files it covered is a lie wearing the old
// hash: an edit after validation reads stale, and complete refuses.
func TestOpenspecCompleteRefusesAStaleVerdict(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-validate-stale")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	specIn(t, ctx, db, p, project, "session")
	art := openspecChangeIn(t, ctx, db, p, project, cleanChangeFiles())
	openspecMove(t, ctx, db, art, OpenspecInProgress)
	cacheVerdict(t, ctx, db, p, art.ID)

	stored := storedChange(t, ctx, db, p, art.ID)
	files := mustFiles(t, stored)
	files["proposal.md"] = "# edited after validation\n"
	if err := SetOpenspecFiles(stored, files); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := db.UpsertArtifact(ctx, stored); err != nil {
		t.Fatalf("write edit: %v", err)
	}

	closeDerivedTodos(t, ctx, db, p, art.ID)
	stored = storedChange(t, ctx, db, p, art.ID)
	err := db.CheckOpenspecTransition(ctx, stored, OpenspecComplete)
	if err == nil || !strings.Contains(err.Error(), "has been edited since it was validated - run POST /api/openspec/{id}/validate") {
		t.Fatalf("stale complete: %v", err)
	}
}

// A red verdict is refused with the checks' own words, joined - the caller
// holds the problems and the lifecycle says them back.
func TestOpenspecCompleteRefusesARedVerdict(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-validate-red")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	// A change with no tasks.md - the clean files minus one - caches red.
	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
	})
	openspecMove(t, ctx, db, art, OpenspecInProgress)

	stored := storedChange(t, ctx, db, p, art.ID)
	verdict, err := db.ValidateOpenspecChange(ctx, stored)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if verdict.Ok {
		t.Fatal("a change with no tasks.md validated green")
	}
	if err := SetOpenspecValidation(stored, verdict); err != nil {
		t.Fatalf("set verdict: %v", err)
	}
	if err := db.UpsertArtifact(ctx, stored); err != nil {
		t.Fatalf("cache the verdict: %v", err)
	}

	stored = storedChange(t, ctx, db, p, art.ID)
	err = db.CheckOpenspecTransition(ctx, stored, OpenspecComplete)
	if err == nil ||
		!strings.Contains(err.Error(), "the change does not validate - tasks.md is absent or empty") {
		t.Fatalf("red complete: %v", err)
	}
}
