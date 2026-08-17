package store

import (
	"errors"
	"strings"
	"testing"
)

// A small, real mxGraph XML fixture: draw.io's own two default layer cells
// ("0" and "1"), two vertices and the edge between them - the shape a
// two-minute diagram in the actual editor saves as, uncompressed.
const fixtureDiagramXML = `<mxGraphModel dx="800" dy="600" grid="1" gridSize="10" guides="1" tooltips="1" connect="1" arrows="1" fold="1" page="1" pageScale="1" pageWidth="850" pageHeight="1100" math="0" shadow="0">
  <root>
    <mxCell id="0" />
    <mxCell id="1" parent="0" />
    <mxCell id="start-node" value="Start" style="rounded=1;whiteSpace=wrap;html=1;" vertex="1" parent="1">
      <mxGeometry x="40" y="40" width="120" height="60" as="geometry" />
    </mxCell>
    <mxCell id="end-node" value="End" style="rounded=1;whiteSpace=wrap;html=1;" vertex="1" parent="1">
      <mxGeometry x="280" y="40" width="120" height="60" as="geometry" />
    </mxCell>
    <mxCell id="edge-1" style="edgeStyle=orthogonalEdgeStyle;html=1;" edge="1" parent="1" source="start-node" target="end-node">
      <mxGeometry relative="1" as="geometry" />
    </mxCell>
  </root>
</mxGraphModel>`

// The ids ParseDiagramCells finds are exactly the mxCell ids the XML carries -
// draw.io's own two layer cells included, because a citation could as
// legitimately name the layer as it could name a shape on it, and this parser
// does not get to decide which ids are "real" content.
func TestDiagramCellsAreTheMxCellIdsTheXMLContains(t *testing.T) {
	cells, err := ParseDiagramCells(fixtureDiagramXML)
	if err != nil {
		t.Fatalf("a real diagram did not parse: %v", err)
	}
	want := []string{"0", "1", "start-node", "end-node", "edge-1"}
	if len(cells) != len(want) {
		t.Fatalf("got %d cells (%v), want %d (%v)", len(cells), cells, len(want), want)
	}
	for _, id := range want {
		if !cells[id] {
			t.Fatalf("cell %q was in the XML and missing from the parse: %v", id, cells)
		}
	}
}

// An id is what makes a cell a stable citation target, so an mxCell that has
// none is refused rather than silently skipped - skipping it would still let
// the diagram "parse", with a shape on it nothing could ever name.
func TestAnMxCellWithNoIdIsRefused(t *testing.T) {
	xmlBody := `<mxGraphModel><root><mxCell id="0" /><mxCell parent="0" vertex="1" /></root></mxGraphModel>`
	_, err := ParseDiagramCells(xmlBody)
	if err == nil {
		t.Fatal("an mxCell with no id was accepted")
	}
	if !strings.Contains(err.Error(), "no id") {
		t.Fatalf("the refusal %q does not say what was wrong", err.Error())
	}
}

// Two cells sharing an id is the case a stored, validated reference exists to
// rule out: a citation of that id would not say which of the two it means,
// so the diagram is refused rather than accepted with an id that is
// ambiguous.
func TestTwoCellsSharingAnIdIsRefused(t *testing.T) {
	xmlBody := `<mxGraphModel><root>
		<mxCell id="0" />
		<mxCell id="dup" vertex="1" parent="0" />
		<mxCell id="dup" vertex="1" parent="0" />
	</root></mxGraphModel>`
	_, err := ParseDiagramCells(xmlBody)
	if err == nil {
		t.Fatal("two cells named the same id were accepted")
	}
	if !strings.Contains(err.Error(), `"dup"`) {
		t.Fatalf("the refusal %q does not name the duplicated id", err.Error())
	}
}

// XML that does not parse at all is refused with an error and not with an
// empty, silently-wrong set of cells - a diagram artifact's body is the
// source of truth, and a body that is not well-formed XML has no truth to be
// the source of.
func TestMalformedXMLIsRefused(t *testing.T) {
	if _, err := ParseDiagramCells(`<mxGraphModel><root><mxCell id="0"></root>`); err == nil {
		t.Fatal("malformed xml was accepted as a diagram")
	}
}

// A body with no mxCell elements at all is the shape draw.io's default,
// compressed save produces - the XML this parser is handed is not the
// mxGraphModel this store treats as the source of truth, it is a
// deflate+base64 blob sitting inside a <diagram> element. The refusal says
// so, so the mistake is legible rather than reading as an empty diagram.
func TestEmptyDiagramIsRefused(t *testing.T) {
	_, err := ParseDiagramCells(`<mxfile><diagram id="x" name="Page-1">deflate-base64-blob</diagram></mxfile>`)
	if err == nil {
		t.Fatal("a diagram with no mxCell elements was accepted")
	}
	if !strings.Contains(err.Error(), "no mxCell") {
		t.Fatalf("the refusal %q does not say what is missing", err.Error())
	}
}

// The validator accepts a cell the diagram actually contains and refuses one
// it does not, naming the cell that was refused - the whole point being that
// a citation is checked against the XML rather than trusted because a caller
// typed a plausible-looking id.
func TestValidateDiagramCellAcceptsARealCellAndRefusesAMadeUpOne(t *testing.T) {
	if err := ValidateDiagramCell("01DIAGRAM", fixtureDiagramXML, "start-node"); err != nil {
		t.Fatalf("a real cell was refused: %v", err)
	}
	err := ValidateDiagramCell("01DIAGRAM", fixtureDiagramXML, "no-such-cell")
	if err == nil {
		t.Fatal("a cell the diagram does not contain was accepted")
	}
	var cellErr DiagramCellError
	if !errors.As(err, &cellErr) {
		t.Fatalf("the refusal %v is not a DiagramCellError", err)
	}
	if cellErr.Diagram != "01DIAGRAM" || cellErr.Cell != "no-such-cell" {
		t.Fatalf("the refusal named diagram %q cell %q, want 01DIAGRAM/no-such-cell",
			cellErr.Diagram, cellErr.Cell)
	}
	if !strings.Contains(cellErr.Error(), `"no-such-cell"`) {
		t.Fatalf("the message %q does not name the cell that was refused", cellErr.Error())
	}
}

// A reference that names no cell at all is not a narrower cell reference, it
// is a different kind of mistake, and it is refused before the XML is even
// parsed.
func TestValidateDiagramCellRefusesAnEmptyCell(t *testing.T) {
	if err := ValidateDiagramCell("01DIAGRAM", fixtureDiagramXML, "  "); err == nil {
		t.Fatal("a blank cell reference was accepted")
	}
}

// ValidateArtifactCell refuses a row that is not a diagram before it looks at
// the cell id at all: a cell reference into a report is not "no such cell",
// it is a reference into a row with no cells, and the two refusals must not
// read the same.
func TestValidateArtifactCellRefusesANonDiagramArtifact(t *testing.T) {
	report := &Artifact{ID: "01REPORT", Type: "report", Body: fixtureDiagramXML}
	err := ValidateArtifactCell(report, "start-node")
	if err == nil {
		t.Fatal("a cell reference into a non-diagram artifact was accepted")
	}
	if !strings.Contains(err.Error(), "not a diagram") {
		t.Fatalf("the refusal %q does not say the artifact is the wrong type", err.Error())
	}
}

// The same check against a real diagram artifact, which is what a caller
// holding a permission-filtered read actually has in hand.
func TestValidateArtifactCellOnARealDiagram(t *testing.T) {
	diagram := &Artifact{ID: "01DIAGRAM", Type: DiagramType, Kind: DiagramKindDrawio, Body: fixtureDiagramXML}
	if err := ValidateArtifactCell(diagram, "end-node"); err != nil {
		t.Fatalf("a real cell on a real diagram artifact was refused: %v", err)
	}
	if err := ValidateArtifactCell(diagram, "ghost"); err == nil {
		t.Fatal("a cell the diagram does not contain was accepted")
	}
}
