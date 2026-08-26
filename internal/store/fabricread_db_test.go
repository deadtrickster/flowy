package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestAFabricSkillIsReadableFromAnotherProject is the feature itself, against a
// real database: the read branch is one line of SQL inside the predicate every
// list goes through, and nothing but a cross-project read proves it.
func TestAFabricSkillIsReadableFromAnotherProject(t *testing.T) {
	ctx, db := open(t)
	home := declaredProject(t, ctx, db, "skills-home")
	away := declaredProject(t, ctx, db, "skills-away")
	author := &Principal{UserID: "u-" + ulid.NewString(), Project: home}
	stranger := &Principal{UserID: "u-" + ulid.NewString(), Project: away}

	write := func(vis string) *Artifact {
		t.Helper()
		a := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: SkillKind, Project: &home,
			OwnerUser: author.UserID, Title: "how something is done here",
			Body: "the procedure", Visibility: vis,
		}
		if err := db.CreateArtifact(ctx, a); err != nil {
			t.Fatalf("write %s skill: %v", vis, err)
		}
		return a
	}
	fabric := write(FabricVisibility)
	scoped := write("project")

	canRead := func(p *Principal, id string) bool {
		t.Helper()
		got, err := db.ReadArtifact(ctx, p, id, false)
		if err != nil {
			return false
		}
		return got != nil
	}

	// THE POINT: another project reads the fabric skill.
	if !canRead(stranger, fabric.ID) {
		t.Fatal("a fabric skill is not readable from another project - that is the whole feature")
	}
	// AND STILL CANNOT READ THE PROJECT-SCOPED ONE. Without this arm the test
	// would pass just as well against a filter that had stopped narrowing at all.
	if canRead(stranger, scoped.ID) {
		t.Fatal("a project-scoped skill leaked to another project - the filter stopped narrowing")
	}
	// The author reads both, which is the floor this must not have moved.
	if !canRead(author, fabric.ID) || !canRead(author, scoped.ID) {
		t.Fatal("the author lost sight of their own skills")
	}
}
