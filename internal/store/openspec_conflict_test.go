package store

// Conflict edges: the pure extraction of capabilities from a files map, and
// the wire side - that a write of a change recomputes its edges both ways.

import (
	"encoding/json"
	"testing"
)

func TestConflictCapabilities(t *testing.T) {
	got := conflictCapabilities(map[string]string{
		"proposal.md":           "why",
		"tasks.md":              "- [ ] x\n",
		"design.md":             "how",
		"specs/foo/spec.md":     "foo delta",
		"specs/bar/nested/s.md": "bar delta",
		"specs/":                "not a delta",
		"specs/baz":             "no capability to name",
	})
	want := map[string]bool{"foo": true, "bar": true}
	if len(got) != len(want) {
		t.Fatalf("capabilities are %v, want %v", got, want)
	}
	for cap := range want {
		if !got[cap] {
			t.Fatalf("capability %q missing from %v", cap, got)
		}
	}
}

func TestChangeWriteBuildsConflictEdges(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-conflict")
	p := &Principal{UserID: "u-ospec-conflict", Project: project}

	a := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md":       "change a\n",
		"specs/foo/spec.md": "a touches foo\n",
	})
	b := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md":       "change b\n",
		"specs/foo/spec.md": "b touches foo\n",
		"specs/bar/spec.md": "and bar\n",
	})
	c := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md":       "change c\n",
		"specs/bar/spec.md": "c touches bar\n",
	})

	// A and B clash over foo; B and C over bar; A and C share nothing.
	aEdges, err := db.ConflictsOf(ctx, a.ID)
	if err != nil {
		t.Fatalf("edges of a: %v", err)
	}
	if len(aEdges) != 1 || aEdges[0].Change != b.ID || aEdges[0].Spec != "foo" {
		t.Fatalf("a's edges are %+v, want one over foo with b", aEdges)
	}
	bEdges, err := db.ConflictsOf(ctx, b.ID)
	if err != nil {
		t.Fatalf("edges of b: %v", err)
	}
	if len(bEdges) != 2 {
		t.Fatalf("b clashes with both, got %+v", bEdges)
	}
	cEdges, err := db.ConflictsOf(ctx, c.ID)
	if err != nil {
		t.Fatalf("edges of c: %v", err)
	}
	if len(cEdges) != 1 || cEdges[0].Change != b.ID || cEdges[0].Spec != "bar" {
		t.Fatalf("c's edges are %+v, want one over bar with b", cEdges)
	}
}

func TestChangeRewriteDropsConflictEdges(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "ospec-conflict-rewrite")
	p := &Principal{UserID: "u-ospec-conflict-rewrite", Project: project}

	a := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md":       "change a\n",
		"specs/foo/spec.md": "a touches foo\n",
	})
	b := openspecChangeIn(t, ctx, db, p, project, map[string]string{
		"proposal.md":       "change b\n",
		"specs/foo/spec.md": "b touches foo\n",
	})
	if edges, err := db.ConflictsOf(ctx, b.ID); err != nil || len(edges) != 1 {
		t.Fatalf("the clash is recorded: %+v, %v", edges, err)
	}

	// A drops its spec delta: the edge is a function of the row, so the
	// rewrite removes both halves.
	files, err := OpenspecFilesOf(a)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	delete(files, "specs/foo/spec.md")
	raw, err := json.Marshal(map[string]any{"openspec": map[string]any{"files": files}})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	a.Fields = raw
	if err := db.UpsertArtifact(ctx, a); err != nil {
		t.Fatalf("rewrite a: %v", err)
	}

	if edges, err := db.ConflictsOf(ctx, a.ID); err != nil || len(edges) != 0 {
		t.Fatalf("a holds no edges after the rewrite: %+v, %v", edges, err)
	}
	if edges, err := db.ConflictsOf(ctx, b.ID); err != nil || len(edges) != 0 {
		t.Fatalf("b's half of the pair went with it: %+v, %v", edges, err)
	}
}
