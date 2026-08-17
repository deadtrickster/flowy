package store

import (
	"bytes"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestFindingReproParsesFields is the pure half: FindingRepro reads exactly
// what was marshaled, off a row that never touched the database. No
// DATABASE_URL needed.
func TestFindingReproParsesFields(t *testing.T) {
	art := &Artifact{
		ID: "f1",
		Fields: []byte(`{"repro_files":[{"path":"repro-01-crash.sh","attachment_id":"a1"},
			{"path":"evidence/errors.log","attachment_id":"a2"}],
			"repro_entrypoint":"repro-01-crash.sh","repro_interp":"bash",
			"isolation":"vm","cmd_override":""}`),
	}
	manifest, files, err := FindingRepro(art)
	if err != nil {
		t.Fatalf("FindingRepro: %v", err)
	}
	if manifest.Entrypoint != "repro-01-crash.sh" || manifest.Interp != "bash" || manifest.Isolation != "vm" {
		t.Errorf("manifest came back as %+v", manifest)
	}
	if len(files) != 2 || files[0].Path != "repro-01-crash.sh" || files[0].AttachmentID != "a1" {
		t.Errorf("files came back as %+v", files)
	}
	if files[1].Path != "evidence/errors.log" || files[1].AttachmentID != "a2" {
		t.Errorf("files[1] came back as %+v", files[1])
	}
}

// TestFindingReproRefusesNoManifest is what a finding with nothing recorded,
// or fields that are somebody else's shape entirely, gets back: a refusal
// naming the finding, never a nil slice a caller has to know to check for.
func TestFindingReproRefusesNoManifest(t *testing.T) {
	if _, _, err := FindingRepro(&Artifact{ID: "bare"}); err == nil {
		t.Fatal("a finding with no fields at all should refuse")
	}
	if _, _, err := FindingRepro(&Artifact{ID: "other", Fields: []byte(`{"as_of":"v1"}`)}); err == nil {
		t.Fatal("fields with no repro_files should refuse")
	}
	if _, _, err := FindingRepro(&Artifact{ID: "bad", Fields: []byte(`not json`)}); err == nil {
		t.Fatal("fields that do not parse should refuse")
	}
}

// TestFindingReproRoundTrip is the store half: every file WriteFindingRepro
// attached comes back byte for byte through ReadFindingRepro, titled by its
// path, and the manifest survives the trip.
func TestFindingReproRoundTrip(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fr")
	owner := &User{Handle: "finder-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}

	finding := &Artifact{
		Type: "finding", Project: &project, OwnerUser: owner.ID, Title: "ctid UPDATE loses rows",
	}
	if err := db.UpsertArtifact(ctx, finding); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}

	script := []byte("#!/bin/bash\nset -e\necho reproducing\n")
	log := []byte("errors.log\nline one\n\x00binary-ish\xff\n")
	sources := []ReproSource{
		{Path: "repro-01-crash.sh", Content: script},
		{Path: "evidence/errors.log", Content: log},
	}
	manifest := ReproManifest{Entrypoint: "repro-01-crash.sh", Interp: "bash", Isolation: "vm"}

	files, err := db.WriteFindingRepro(ctx, p, finding.ID, sources, manifest)
	if err != nil {
		t.Fatalf("write finding repro: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("wrote %d files, want 2", len(files))
	}

	gotManifest, gotFiles, err := db.ReadFindingRepro(ctx, p, finding.ID)
	if err != nil {
		t.Fatalf("read finding repro: %v", err)
	}
	if gotManifest != manifest {
		t.Errorf("manifest came back as %+v, want %+v", gotManifest, manifest)
	}
	if len(gotFiles) != 2 {
		t.Fatalf("read back %d files, want 2", len(gotFiles))
	}
	if gotFiles[0].Path != "repro-01-crash.sh" || !bytes.Equal(gotFiles[0].Content, script) {
		t.Errorf("file 0 came back as %q, %q", gotFiles[0].Path, gotFiles[0].Content)
	}
	if gotFiles[1].Path != "evidence/errors.log" || !bytes.Equal(gotFiles[1].Content, log) {
		t.Errorf("file 1 came back as %q, %q", gotFiles[1].Path, gotFiles[1].Content)
	}

	// Each file is an ordinary attachment, titled by its path - findable and
	// readable the same way any attachment_write'd file would be.
	att, _, err := db.ReadAttachment(ctx, p, files[0].AttachmentID)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if att.Type != "attachment" || att.Title != "repro-01-crash.sh" {
		t.Errorf("attachment came back as %s %q", att.Type, att.Title)
	}
}

// TestFindingReproRefusesDuplicatePath is the refusal that keeps a manifest
// from having to guess which of two attachments a path now means.
func TestFindingReproRefusesDuplicatePath(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fr")
	owner := &User{Handle: "finder-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}
	finding := &Artifact{Type: "finding", Project: &project, OwnerUser: owner.ID, Title: "dup"}
	if err := db.UpsertArtifact(ctx, finding); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}

	_, err := db.WriteFindingRepro(ctx, p, finding.ID, []ReproSource{
		{Path: "same.sh", Content: []byte("a")},
		{Path: "same.sh", Content: []byte("b")},
	}, ReproManifest{})
	if err == nil {
		t.Fatal("a repeated path should be refused")
	}
}

// TestFindingReproRefusesWrongType is the namespace answer: an id that reads
// back fine as a bug is not a finding, and this surface says so the same way
// it would say an id is not there at all.
func TestFindingReproRefusesWrongType(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "fr")
	owner := &User{Handle: "finder-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}
	bug := &Artifact{Type: "bug", Project: &project, OwnerUser: owner.ID, Title: "not a finding"}
	if err := db.UpsertArtifact(ctx, bug); err != nil {
		t.Fatalf("upsert bug: %v", err)
	}

	_, err := db.WriteFindingRepro(ctx, p, bug.ID, []ReproSource{{Path: "x.sh", Content: []byte("a")}},
		ReproManifest{})
	var nf NotAFindingError
	if !errors.As(err, &nf) {
		t.Fatalf("write on a bug id should be NotAFindingError, got %v", err)
	}
}
