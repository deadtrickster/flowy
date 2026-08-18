package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestAMergeRequestWithNoBranchIsRefusedByTheStore is the rule moved off one
// door and into the write path every door goes through.
//
// It was written in mcp_merge.go and asked only there, so a create through
// POST /api/artifacts wrote a merge row with no branch - a row BranchOf reads
// empty, which nothing can rebase, gate or fast-forward, sitting in the queue
// looking exactly like work.
//
// Both arms, because a store that refused every merge row would pass the first
// one on its own: the same row with a branch is written and reads back with it.
func TestAMergeRequestWithNoBranchIsRefusedByTheStore(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "mergerow")
	owner := "u-" + ulid.NewString()

	bare := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: owner, Title: "land something", Visibility: "project",
	}
	err := db.CreateArtifact(ctx, bare)
	if err == nil {
		t.Fatal("a merge request with no branch was written - nothing can rebase, " +
			"gate or fast-forward it, and it sits in the queue looking like work")
	}
	var refusal DepRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("refused as %v, and not as the caller's own mistake - every door "+
			"maps a DepRefusal to a 400 and this one would arrive as a 500", err)
	}
	if _, readErr := db.GetArtifact(ctx, bare.ID); readErr == nil {
		t.Error("the refused row is in the table")
	}

	named := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: owner, Title: "land something", Visibility: "project",
		Fields: json.RawMessage(`{"branch":"feat/somewhere","target":"master"}`),
	}
	if err := db.CreateArtifact(ctx, named); err != nil {
		t.Fatalf("a merge request that says its branch was refused: %v", err)
	}
	got, err := db.GetArtifact(ctx, named.ID)
	if err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if BranchOf(got) != "feat/somewhere" {
		t.Errorf("branch reads %q, want feat/somewhere", BranchOf(got))
	}
}
