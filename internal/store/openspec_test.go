package store

import (
	"encoding/json"
	"errors"
	"testing"
)

// The shape check itself, asked directly: every case asserts the refusal is
// THERE, so a check that stopped checking reads as a red, not as agreement.
// The wire - that create, upsert and set-fields actually ask it - is proven
// by the DB-backed tests, the way every store invariant is.

func openspecChange(files map[string]string) *Artifact {
	a := &Artifact{ID: "01TEST00000000000000000001", Type: MemoryType, Kind: ChangeKind}
	if files != nil {
		raw, err := json.Marshal(map[string]any{
			"openspec": map[string]any{"files": files},
		})
		if err != nil {
			panic(err)
		}
		a.Fields = raw
	}
	return a
}

func TestCheckOpenspecRowLeavesOtherKindsAlone(t *testing.T) {
	if err := checkOpenspecRow(&Artifact{Type: MemoryType, Kind: "todo"}); err != nil {
		t.Fatalf("a todo row must not be refused: %v", err)
	}
	if err := checkOpenspecRow(&Artifact{Type: MemoryType, Kind: MergeKind}); err != nil {
		t.Fatalf("a merge row must not be refused: %v", err)
	}
	if err := checkOpenspecRow(nil); err != nil {
		t.Fatalf("a nil row must not be refused: %v", err)
	}
}

func TestCheckOpenspecRowSpec(t *testing.T) {
	ok := &Artifact{ID: "01TEST00000000000000000002", Type: MemoryType,
		Kind: SpecKind, Title: "the-capability", Body: "# the-capability\n"}
	if err := checkOpenspecRow(ok); err != nil {
		t.Fatalf("a spec with words and a name is a spec: %v", err)
	}
	noBody := &Artifact{ID: "01TEST00000000000000000003", Type: MemoryType,
		Kind: SpecKind, Title: "the-capability"}
	if err := checkOpenspecRow(noBody); err == nil {
		t.Fatal("a spec with no body is a spec that specifies nothing - must be refused")
	}
	noTitle := &Artifact{ID: "01TEST00000000000000000004", Type: MemoryType,
		Kind: SpecKind, Body: "# words\n"}
	if err := checkOpenspecRow(noTitle); err == nil {
		t.Fatal("a spec with no title names no capability - must be refused")
	}
}

func TestCheckOpenspecRowChange(t *testing.T) {
	ok := openspecChange(map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "- [ ] do the thing\n",
	})
	if err := checkOpenspecRow(ok); err != nil {
		t.Fatalf("a change with a proposal is a change: %v", err)
	}
	if err := checkOpenspecRow(&Artifact{ID: "01TEST00000000000000000005",
		Type: MemoryType, Kind: ChangeKind}); err == nil {
		t.Fatal("a change with no files map has no proposal - must be refused")
	}
	if err := checkOpenspecRow(openspecChange(map[string]string{
		"proposal.md": "   \n",
	})); err == nil {
		t.Fatal("a blank proposal.md is no proposal - must be refused")
	}
	if err := checkOpenspecRow(openspecChange(map[string]string{
		"tasks.md": "- [ ] only tasks\n",
	})); err == nil {
		t.Fatal("a change without proposal.md proposes nothing - must be refused")
	}
	bad := &Artifact{ID: "01TEST00000000000000000006", Type: MemoryType,
		Kind: ChangeKind, Fields: json.RawMessage(`{not json`)}
	if err := checkOpenspecRow(bad); err == nil {
		t.Fatal("unparsable fields must be refused, not read as no files")
	}
}

func TestCheckOpenspecFilePaths(t *testing.T) {
	for _, path := range []string{"../escape.md", "/root.md", `..\escape.md`, `a\..\b.md`, ""} {
		if err := checkOpenspecFilePaths(map[string]string{path: "x"}); err == nil {
			t.Fatalf("path %q is a route out of the change - must be refused", path)
		}
	}
	for _, path := range []string{"proposal.md", "specs/cap/spec.md", "notes/one two.md"} {
		if err := checkOpenspecFilePaths(map[string]string{path: "x"}); err != nil {
			t.Fatalf("path %q is a name inside the change: %v", path, err)
		}
	}
}

// The refusal must satisfy the contract the doors map to 400. It is the same
// contract MergeRowWithoutBranchError implements - if this assertion breaks,
// every openspec refusal starts answering 500 at the doors.
func TestOpenspecRowErrorIsADepRefusal(t *testing.T) {
	err := OpenspecRowError{Row: "r", Why: "w"}
	var refusal DepRefusal
	if !errors.As(err, &refusal) {
		t.Fatal("OpenspecRowError must implement DepRefusal")
	}
}
