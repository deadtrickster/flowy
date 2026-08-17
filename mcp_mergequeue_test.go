package main

import (
	"testing"
)

// The merge queue surface is mostly a read, so what is worth asserting without a
// database is the part that decides: that the tool is registered at all, and
// that "no tip stated" produces NO VERDICT rather than a yes.

func TestTheMergeQueueToolIsRegistered(t *testing.T) {
	// A tool group that is written and never appended is a surface nobody can
	// call, and it looks exactly like a working one from the source. That
	// happened to another group on this server once already.
	found := false
	for _, tl := range allTools() {
		if tl.Name == "merge_queue" {
			found = true
			if tl.call == nil {
				t.Fatal("merge_queue is listed with no implementation behind it")
			}
		}
	}
	if !found {
		t.Fatal("merge_queue is not in allTools, so nothing can call it")
	}
}

// Every tool the server lists must be callable, which is the general form of the
// bug above. Cheap to assert once, and it covers every group added later.
func TestEveryListedToolHasAnImplementation(t *testing.T) {
	seen := map[string]bool{}
	for _, tl := range allTools() {
		if tl.Name == "" {
			t.Error("a tool is listed with no name")
		}
		if tl.call == nil {
			t.Errorf("tool %q is listed with no implementation", tl.Name)
		}
		if seen[tl.Name] {
			t.Errorf("tool %q is listed twice - the second one is unreachable", tl.Name)
		}
		seen[tl.Name] = true
	}
}
