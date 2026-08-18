package store

import "testing"

// WHAT A ROW IS, ONCE. The measurement this function exists for: on the live
// node todo, note, report and diagram each exist as a TYPE and as a KIND of
// memory, and neither side is empty - so every reader that compared .Type was
// answering about half the rows.
//
// The diagram case is the one that had a defect in it already: every diagram
// the console writes is type=memory kind=diagram, one row is type=diagram, and
// ValidateArtifactCell compared .Type - so it refused the real diagrams and
// accepted the odd one. It had no callers yet, which is the only reason nobody
// had hit it.
func TestEntityTypeIsIdentityWhereverItIsWritten(t *testing.T) {
	both := []*Artifact{
		{ID: "01A", Type: MemoryType, Kind: "diagram"},
		{ID: "01B", Type: DiagramType},
	}
	for _, a := range both {
		if got := EntityType(a); got != "diagram" {
			t.Errorf("%s reads as %q, want diagram - the two ways of writing one identity", a.ID, got)
		}
		if !IsEntityType(a, DiagramType) {
			t.Errorf("%s is not recognised as a diagram", a.ID)
		}
	}

	// A kind that is NOT identity keeps its type. finding.kind is a defect
	// class and attachment.kind is a media type; reading either as identity is
	// what this function is separating out.
	for _, c := range []struct {
		a    *Artifact
		want string
	}{
		{&Artifact{Type: "finding", Kind: "crash"}, "finding"},
		{&Artifact{Type: "attachment", Kind: "text"}, "attachment"},
		{&Artifact{Type: MemoryType, Kind: "todo"}, "todo"},
		{&Artifact{Type: MemoryType}, MemoryType},
		{nil, ""},
	} {
		if got := EntityType(c.a); got != c.want {
			t.Errorf("EntityType(%+v) = %q, want %q", c.a, got, c.want)
		}
	}
}

// And the caller it was written for: a cell reference is refused against a row
// that is not a diagram, whichever way that row spells what it is.
func TestACellReferenceIsJudgedAgainstEitherSpelling(t *testing.T) {
	const xml = `<mxfile><diagram><mxGraphModel><root><mxCell id="0"/><mxCell id="n1"/></root></mxGraphModel></diagram></mxfile>`

	for _, a := range []*Artifact{
		{ID: "01A", Type: MemoryType, Kind: "diagram", Body: xml},
		{ID: "01B", Type: DiagramType, Body: xml},
	} {
		if err := ValidateArtifactCell(a, "n1"); err != nil {
			t.Errorf("a cell that is in %s was refused: %v", a.ID, err)
		}
		if err := ValidateArtifactCell(a, "nope"); err == nil {
			t.Errorf("a cell that is not in %s was accepted", a.ID)
		}
	}

	if err := ValidateArtifactCell(&Artifact{ID: "01C", Type: MemoryType, Kind: "todo"}, "n1"); err == nil {
		t.Error("a todo answered a cell reference")
	}
}
