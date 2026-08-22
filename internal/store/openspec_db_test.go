package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The wire side of the openspec shape rule: the three statements every surface
// writes through - create, upsert and set-fields - each ask checkOpenspecRow,
// proven against the database rather than by calling the check directly (which
// openspec_test.go does). A husk is refused at every one of them, and the
// refusal is the check's own, so an API surface cannot write a shape the
// doors could not.

// openspecChangeIn writes a change carrying exactly the files named, the way a
// door would have, and hands back the stored row.
func openspecChangeIn(t *testing.T, ctx context.Context, db *DB, p *Principal,
	project string, files map[string]string,
) *Artifact {
	t.Helper()

	raw, err := json.Marshal(map[string]any{
		"openspec": map[string]any{"files": files},
	})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	art := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: ChangeKind,
		Project: &project, OwnerUser: p.UserID, Title: "the change", Fields: raw,
	}
	if err := db.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("write change: %v", err)
	}
	return art
}

func TestCreateArtifactRefusesAHuskChange(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-create")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	for _, files := range []map[string]string{
		nil,
		{"tasks.md": "- [ ] only tasks\n"},
		{"proposal.md": "   \n"},
	} {
		raw := []byte("")
		if files != nil {
			var err error
			if raw, err = json.Marshal(map[string]any{
				"openspec": map[string]any{"files": files},
			}); err != nil {
				t.Fatalf("fields: %v", err)
			}
		}
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: ChangeKind,
			Project: &project, OwnerUser: p.UserID, Title: "husk", Fields: raw,
		}
		err := db.CreateArtifact(ctx, art)
		if err == nil {
			t.Fatalf("a change with files %v was written - a change that proposes nothing must not exist", files)
		}
		var refusal DepRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal is %T, not a DepRefusal the doors map to 400: %v", err, err)
		}
	}
}

func TestUpsertArtifactRefusesToStripTheProposal(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-upsert")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "- [ ] do it\n",
	})

	// The same row, restated with files that dropped the proposal. An upsert
	// must refuse it, not husk the row.
	tasksOnly, err := json.Marshal(map[string]any{
		"openspec": map[string]any{"files": map[string]string{"tasks.md": "- [ ] do it\n"}},
	})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	husked := *art
	husked.Fields = tasksOnly
	if err := db.UpsertArtifact(ctx, &husked); err == nil {
		t.Fatal("an upsert that drops the proposal was accepted")
	} else {
		var refusal DepRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal is %T, not a DepRefusal: %v", err, err)
		}
	}

	// And the stored row still carries what it did - a refusal writes nothing.
	stored, err := db.ReadArtifact(ctx, p, art.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	files, err := OpenspecFilesOf(stored)
	if err != nil {
		t.Fatalf("files of the stored row: %v", err)
	}
	if files["proposal.md"] == "" || files["tasks.md"] == "" {
		t.Fatalf("the refused upsert moved the row: files %v", files)
	}
}

func TestSetArtifactFieldsRefusesToStripTheProposal(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-setfields")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	art := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
	})

	tasksOnly, err := json.Marshal(map[string]any{
		"openspec": map[string]any{"files": map[string]string{"tasks.md": "- [ ] x\n"}},
	})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	if err := db.SetArtifactFields(ctx, art, tasksOnly); err == nil {
		t.Fatal("a fields write that drops the proposal was accepted")
	} else {
		var refusal DepRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal is %T, not a DepRefusal: %v", err, err)
		}
	}

	stored, err := db.ReadArtifact(ctx, p, art.ID, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	files, err := OpenspecFilesOf(stored)
	if err != nil {
		t.Fatalf("files of the stored row: %v", err)
	}
	if files["proposal.md"] == "" {
		t.Fatal("the refused fields write moved the row")
	}
}

// The positive side, once at the wire level: a spec and a change write through
// the statements and read back whole - files included.
func TestOpenspecRowsWriteAndReadBack(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-roundtrip")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}

	spec := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: SpecKind,
		Project: &project, OwnerUser: p.UserID,
		Title: "the-capability", Body: "# the-capability\n",
	}
	if err := db.CreateArtifact(ctx, spec); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	change := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "- [ ] do it\n",
		"design.md":   "## shape\n",
	})

	stored, err := db.ReadArtifact(ctx, p, change.ID, false)
	if err != nil {
		t.Fatalf("read change: %v", err)
	}
	if !IsOpenspec(stored) {
		t.Fatalf("the stored change does not answer as openspec: %+v", stored)
	}
	files, err := OpenspecFilesOf(stored)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(files) != 3 || files["design.md"] == "" {
		t.Fatalf("the stored change lost its files: %v", files)
	}
}
