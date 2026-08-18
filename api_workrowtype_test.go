package main

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A merge request sent as a TYPE is a row on no queue, and it used to be a 200.
//
// No database: the whole content of the check is the reading of two strings,
// and a version that needed a cluster to exercise is one nobody would run.
func TestAMergeRowSentAsATypeIsRefused(t *testing.T) {
	why := mergeRowSentAsAType(store.MergeKind, "")
	if why == "" {
		t.Fatal("a merge sent as a type with no kind was accepted - it would be on no queue")
	}
	// The refusal has to say what to send instead. A 400 that only says no
	// leaves the caller guessing at the thing they already guessed wrong.
	if !strings.Contains(why, store.MemoryType) || !strings.Contains(why, store.MergeKind) {
		t.Fatalf("the refusal does not name the shape to send: %s", why)
	}
	// Explicit type AND kind is somebody being deliberate, not somebody making
	// the mistake - the mistake is defined by the missing kind.
	if why := mergeRowSentAsAType(store.MergeKind, store.MergeKind); why != "" {
		t.Fatalf("an explicit type+kind pair was refused: %s", why)
	}
	if why := mergeRowSentAsAType(store.MemoryType, store.MergeKind); why != "" {
		t.Fatalf("a properly shaped merge request was refused: %s", why)
	}
}

// THE ARM THAT MATTERS, and the one the gate had to teach me.
//
// The first cut of this refused every work kind sent as a type. The suite
// creates a top-level `feature` and moves it open -> triaged -> wont-fix, so
// that cut turned a legitimate artifact into a 400 and went red - 662/1, on a
// check about the status line and nothing to do with types.
//
// todo, note, report, diagram and feature all live on both sides of the
// type/kind fence, with rows on both, so a refusal wide enough to catch the
// mistake also refuses shapes this fabric supports. Merge is the one with no
// second reading.
func TestTheShapesThisFabricAlreadyWritesAreNotRefused(t *testing.T) {
	for _, typ := range []string{
		// Every top-level type measured on the live node, 636 rows.
		"memory", "finding", "announcement", "todo", "attachment", "diagram", "note", "report", "bug",
		// And the one the gate refused: a feature with the artifact lifecycle.
		"feature", "handoff", "work", "join",
	} {
		if why := mergeRowSentAsAType(typ, ""); why != "" {
			t.Fatalf("a top-level %s was refused, and rows like it are written today: %s", typ, why)
		}
	}
}
