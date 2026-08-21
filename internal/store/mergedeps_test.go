package store

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A MERGE THAT DEPENDS ON ANOTHER MERGE WAITS FOR IT - 01M0G9GDS5, from the
// operator's question "what if a merge depends on the other merge?".
//
// The dependency model is deliberate and unchanged: order between merges IS a
// dep edge, and there is no second graph. What was missing is that the edge was
// advisory - B could be gated and landed while A sat in the queue, so a stack
// landed bottom-up only if whoever drove it remembered the order.
//
// TWO ARMS, and the second is the one that makes the first mean anything: a
// test that only proved the refusal could be passed by a door that refuses
// everything.
func TestADependentMergeWaitsForTheOneItDependsOn(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	holder := &Principal{UserID: "u-holder", Project: project}

	mergeRow := func(title, branch string) *Artifact {
		t.Helper()
		a := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
			OwnerUser: holder.UserID, Title: title, Visibility: "project",
			Fields: marshalFields(t, map[string]any{BranchField: branch, TargetField: target}),
		}
		if err := db.UpsertArtifact(ctx, a); err != nil {
			t.Fatalf("file %s: %v", title, err)
		}
		return a
	}
	// A is the lower half of the stack, B waits for it.
	first := mergeRow("the one underneath", "feat-a")
	second := mergeRow("the one stacked on it", "feat-b")
	if _, err := db.AddDep(ctx, holder, second.ID, first.ID); err != nil {
		t.Fatalf("record that B waits for A: %v", err)
	}

	// B is measured and green. Gating a dependent branch early is not wrong -
	// the verdict is true of the tree it measured - so nothing refuses here.
	if _, _, err := db.SetMergeGate(ctx, holder, second.ID, "run-b", "", ""); err != nil {
		t.Fatalf("declare B: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, second.ID, "run-b", "bbbb111222333", ""); err != nil {
		t.Fatalf("B's verdict: %v", err)
	}

	// ARM ONE: the order is refused, and the refusal names what to do.
	_, _, err := db.LandMerge(ctx, holder, second.ID, "bbbb111222333")
	if err == nil {
		t.Fatal("B landed while A was still open")
	}
	if !strings.Contains(err.Error(), first.ID) {
		t.Errorf("the refusal does not name the row being waited for: %v", err)
	}

	// ARM TWO: finish A, and the identical call now succeeds. Nothing else
	// changed - same principal, same sha, same row - so the refusal was about
	// the edge and not about anything incidental.
	if _, _, err := db.SetTodoStatus(ctx, holder, first.ID, DoneStatus, "landed"); err != nil {
		t.Fatalf("finish A: %v", err)
	}
	if _, _, err := db.LandMerge(ctx, holder, second.ID, "bbbb111222333"); err != nil {
		t.Fatalf("B still refused after A finished: %v", err)
	}
}

// A ROW WITH NO EDGES IS UNAFFECTED, which is most of the queue. Without this
// the change above could refuse everything and the test above would still pass.
func TestAMergeWithNoDependenciesLandsAsBefore(t *testing.T) {
	ctx, db, project := lockCtx(t)
	target := ownTarget(t)
	holder := &Principal{UserID: "u-holder", Project: project}

	row := &Artifact{
		ID: ulid.NewString(), Type: MemoryType, Kind: MergeKind, Project: &project,
		OwnerUser: holder.UserID, Title: "on its own", Visibility: "project",
		Fields: marshalFields(t, map[string]any{BranchField: "feat-solo", TargetField: target}),
	}
	if err := db.UpsertArtifact(ctx, row); err != nil {
		t.Fatalf("file: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run-s", "", ""); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if _, _, err := db.SetMergeGate(ctx, holder, row.ID, "run-s", "cccc111222333", ""); err != nil {
		t.Fatalf("verdict: %v", err)
	}
	if _, _, err := db.LandMerge(ctx, holder, row.ID, "cccc111222333"); err != nil {
		t.Fatalf("a row with no dependencies was refused: %v", err)
	}
}
