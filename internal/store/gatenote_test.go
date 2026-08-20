package store

import (
	"os"
	"regexp"
	"testing"
	"time"
)

// A NOTE DOES NOT OUTLIVE THE VERDICT IT DESCRIBES.
//
// A declaration already clears gated_tip, the red, and the block, each for the
// same reason: it says the old evidence is being replaced. gated_note was added
// later and was not added to that list, so a green count survived the verdict it
// came from.
//
// It cost a wrong reading within a day. A row carrying
// `gated_note: passed: 686 failed: 0` with no gated_tip was read as a green
// verdict by a seat looking for a stalled landing - correctly, because a count
// is the only thing that field can mean. The verdict had been cleared by the
// re-declaration that was in flight.
//
// gated_tip is what says a verdict EXISTS. A note that outlives it is a
// measurement with nothing behind it, which is worse than no note at all: it is
// a number somebody will quote.
func TestADeclarationClearsTheNoteWithTheVerdict(t *testing.T) {
	now := time.Now()

	// A recorded green: tip and note together.
	// Every field the set names, as a recorded run would leave them - so the
	// walk below is about the SET and not about the two this bug happened to
	// involve.
	fields := map[string]any{}
	applyGate(fields, "run-1", "abc1234", now)
	for _, field := range supersededByADeclaration {
		if _, ok := fields[field]; !ok {
			fields[field] = "left by the run being replaced"
		}
	}
	fields[GateNoteField] = "passed: 686 failed: 0"
	if fields[GatedTipField] != "abc1234" {
		t.Fatalf("the verdict did not record a tip: %v", fields[GatedTipField])
	}

	// The re-declaration. It replaces the evidence, so everything describing the
	// old run goes with it.
	applyGate(fields, "run-2", "", now)

	// EVERY FIELD IN THE SET, walked rather than listed again. A test that
	// repeated the list would pass the day somebody adds a field to one copy -
	// which is exactly how gated_note came to survive a declaration.
	for _, field := range supersededByADeclaration {
		if got, ok := fields[field]; ok {
			t.Errorf("a declaration left %s behind: %v - it describes the run this "+
				"declaration is replacing", field, got)
		}
	}
	// AND THE RUN IS THE NEW ONE, which is what makes the row say "measuring"
	// rather than "measured".
	if fields[GateRunField] != "run-2" {
		t.Errorf("gate_run is %v, want the declaring run", fields[GateRunField])
	}
}

// EVERY FIELD THIS FILE DEFINES IS CLASSIFIED, so a new one cannot be added
// without somebody deciding whether a declaration replaces it.
//
// This is the mechanism the one-line fix did not provide, and the difference is
// measurable: removing GateNoteField from supersededByADeclaration leaves the
// walk above GREEN, because a walk over a set cannot see a field that never
// joined it. That is precisely how the defect happened - the field arrived, the
// set did not grow, and nothing anywhere failed.
//
// So this reads the source, like the route-completeness check does, and asks of
// every `<name>Field = "..."` constant here: is it superseded by a declaration,
// or is it deliberately not? A new constant belongs in one of the two lists and
// the answer has to be typed.
func TestEveryGateFieldIsClassified(t *testing.T) {
	src, err := os.ReadFile("mergegate.go")
	if err != nil {
		t.Fatalf("read mergegate.go: %v", err)
	}

	// The fields a declaration deliberately does NOT clear, with the reason.
	kept := map[string]string{
		"GateRunField":   "the declaration REPLACES it - the row must say which run is measuring now",
		"GateAtField":    "applyGate stamps it, for the same reason",
		"GateRefField":   "where the evidence lives, restated by each declaration that has one",
		"GateActorField": "who declared, which the declaring path rewrites",
	}
	inSet := map[string]bool{}
	for _, f := range supersededByADeclaration {
		inSet[f] = true
	}
	// The set holds VALUES ("gated_tip"); the source holds NAMES. Map one to the
	// other by reading the constant's own value.
	decl := regexp.MustCompile(`(?m)^const ([A-Za-z]+Field) = "([^"]+)"`)
	for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
		name, value := m[1], m[2]
		if inSet[value] {
			continue
		}
		if _, ok := kept[name]; ok {
			continue
		}
		t.Errorf("%s (%q) is neither in supersededByADeclaration nor in this test's kept "+
			"list. A declaration replaces the evidence of the run before it: decide which "+
			"this field is. Leaving it unclassified is how gated_note came to outlive the "+
			"verdict it described.", name, value)
	}
}
