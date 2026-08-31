package store

// Diagrams: a draw.io diagram stored as an artifact, and the cells inside it
// named well enough to cite.
//
// A diagram is an artifact of type 'diagram', a peer of 'report' and
// 'proposal' rather than a kind of 'memory'. Three things follow from that.
//
// THE BODY IS THE mxGraph XML, VERBATIM, AND NOTHING ELSE DERIVES IT. Body is
// already a free-form string column on every artifact, so a diagram needs no
// new column - draw.io's own save format goes in Body unchanged, the way a
// report's prose goes in Body unchanged. Any rendering - an SVG, a PNG, a
// console's canvas - is downstream of this column and never the other way:
// see the head of this comment's rule 3 in the ruling this file builds to.
// This file does not render anything; it reads the XML that is already there.
//
// A CELL REFERENCE IS THE ARTIFACT TRIPLE PLUS ONE SEGMENT. An artifact is
// already addressed by (project, type, id) everywhere else in this store; a
// cell inside a diagram adds the mxCell id on the end, because that id is what
// survives a re-layout and an id derived from position or index is not -
// draw.io hands out the same ids across ordinary edits, which is exactly the
// property a citation needs and a screen coordinate does not have. This file
// owns the id half of that: ParseDiagramCells reads the ids the XML actually
// contains, and ValidateDiagramCell (or ValidateArtifactCell, against a row
// already in hand) is the door that turns "a cell reference" from a string a
// caller typed into a string this diagram is known to contain. Composing the
// full four-segment reference for a citation surface is a console concern and
// is not built here.
//
// DIAGRAM IS DELIBERATELY NOT IN WorkKinds. WorkKinds is the queue: a work
// kind has a status, an assignee, and a place in Ready()'s per-reader
// computation, and everything in deps.go about ordering work is keyed off
// isWorkKind and off Type == MemoryType (see readWorkItem). A diagram has none
// of that - it is not carried by anybody, it does not become "done", and nothing
// drains it - so putting it in WorkKinds would put every diagram artifact on
// every reader's queue for no reason the queue exists to serve. It stays out.
//
// One consequence of staying out, said plainly rather than left to be found
// later: AddDep/RemoveDep in deps.go route both ends of an edge through
// readWorkItem, which refuses anything that is not Type == MemoryType and a
// WorkKind - so a diagram cannot yet stand on either end of a dep.add today,
// even though "a task can start from a diagram, or a diagram can be a result
// of a task" is exactly the shape of an edge deps.go already carries (rule 4
// of the ruling this file builds to: no second edge type). Widening
// readWorkItem's gate to accept a diagram as an edge endpoint - without
// widening it into the queue itself, which is the mistake putting diagram in
// WorkKinds would make - is the follow-up this file deliberately leaves
// undone. This task is the artifact kind, the parser and the validator; the
// wiring is store surface this task's scope does not reach.

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// DiagramType is the artifact type a draw.io diagram is stored as. It is
// ProposalType's shape, for ProposalType's reason: one table, one permission
// filter, one signature, one merge, serving a fourth kind of thing this store
// holds without multiplying any of them.
const DiagramType = "diagram"

// DiagramKindDrawio is the diagram format this file parses: draw.io's
// mxGraphModel XML. Kind is left as a real field rather than assumed, because
// a diagram artifact naming its format is what lets a second diagram format
// (mermaid, excalidraw, whatever comes next) share the type without this
// parser being asked to guess which one it was handed.
const DiagramKindDrawio = "drawio"

// DiagramCellError is a cell reference naming a cell the diagram does not
// contain: the mistake ValidateDiagramCell and ValidateArtifactCell refuse,
// with the cell that was named on the message rather than left for the
// caller to log separately.
type DiagramCellError struct {
	Diagram string
	Cell    string
}

func (e DiagramCellError) Error() string {
	if e.Diagram == "" {
		return fmt.Sprintf("diagram has no cell %q", e.Cell)
	}
	return fmt.Sprintf("diagram %s has no cell %q", e.Diagram, e.Cell)
}

// ParseDiagramCells reads every mxCell id out of xmlBody and returns them as a
// set, so membership is a map lookup and not a second parse per check.
//
// It walks tokens rather than unmarshalling into a struct shaped like
// mxGraphModel, because the id is the only thing this file needs and an
// element it does not recognise - a future draw.io addition, a shape plugin's
// own tag - must not stop the walk. Restricting to <mxCell> and reading only
// its id attribute is the whole of what makes this forward-compatible with
// XML this build has never seen.
//
// Two things about the ids it finds are refused rather than silently taken:
// an mxCell with no id, and two cells sharing one. Both break the property
// this file exists to protect - a cell reference names exactly one shape,
// stably - and a diagram that cannot promise that is refused at the door
// rather than accepted with a reference nobody can trust.
//
// It does not inflate draw.io's compressed export format (the <diagram> node
// whose text is deflate+base64, produced by a plain "Save"). That is a
// transform an upload surface would do before the XML reaches this store, and
// this store's ruling is that the XML - the source of truth - is what gets
// validated, not a compressed blob nothing here can citation into. A body
// that parses to zero cells says so, so the caller finds out at the door
// rather than from a diagram that silently has nothing referenceable in it.
func ParseDiagramCells(xmlBody string) (map[string]bool, error) {
	cells := map[string]bool{}
	dec := xml.NewDecoder(strings.NewReader(xmlBody))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("store: diagram xml did not parse: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "mxCell" {
			continue
		}
		id := ""
		for _, attr := range se.Attr {
			if attr.Name.Local == "id" {
				id = attr.Value
				break
			}
		}
		if id == "" {
			return nil, fmt.Errorf("store: diagram xml has an mxCell with no id, " +
				"so it could never be a stable citation target")
		}
		if cells[id] {
			return nil, fmt.Errorf("store: diagram xml has two cells named %q, "+
				"so a citation of that id would not say which one it means", id)
		}
		cells[id] = true
	}
	if len(cells) == 0 {
		return nil, fmt.Errorf("store: diagram xml has no mxCell elements - if this came " +
			"from draw.io's default save, it is the compressed export; this parser reads the " +
			"uncompressed mxGraphModel XML that is this store's source of truth")
	}
	return cells, nil
}

// ValidateDiagramCell checks that cellID names a cell diagramXML actually
// contains, so that a reference into a diagram is VALIDATED against the XML
// rather than trusted the way a hand-typed id otherwise would be.
//
// diagramID is used only to name the diagram in a refusal - pass "" when the
// caller has no id in hand yet (a body being validated before it is stored,
// say) and the message names the cell alone.
func ValidateDiagramCell(diagramID, diagramXML, cellID string) error {
	cellID = strings.TrimSpace(cellID)
	if cellID == "" {
		return fmt.Errorf("store: a cell reference names a cell; this one names none")
	}
	cells, err := ParseDiagramCells(diagramXML)
	if err != nil {
		return err
	}
	if !cells[cellID] {
		return DiagramCellError{Diagram: diagramID, Cell: cellID}
	}
	return nil
}

// ValidateArtifactCell is ValidateDiagramCell against a diagram artifact
// already in hand, so a caller holding the row from a permission-filtered
// read does not have to pull Type, ID and Body back out of it by hand.
//
// It refuses an artifact that is not a diagram before it looks at cellID at
// all: a cell reference into a report or a todo is not a narrower version of
// a valid reference, it is a different mistake, and conflating the two would
// have this function's refusal say "no such cell" about a row that has no
// cells of any kind.
func ValidateArtifactCell(a *Artifact, cellID string) error {
	if a == nil {
		return fmt.Errorf("store: no diagram to validate a cell reference against")
	}
	// THROUGH EntityType, because a diagram is written both ways on this node
	// and this check saw only one of them. Measured: every diagram the console
	// has ever written is type=memory kind=diagram, and one row is type=diagram
	// - so a validator comparing a.Type refused the two real diagrams and
	// accepted the one that came in through the other door. It has no callers
	// yet, which is the only reason nobody has hit it: a latent defect is not
	// dormant, it is waiting for its first caller.
	if !IsEntityType(a, DiagramType) {
		return fmt.Errorf("store: %s is a %s, not a diagram, so it has no cells to reference",
			a.ID, EntityType(a))
	}
	return ValidateDiagramCell(a.ID, a.Body, cellID)
}

// checkDiagramRow refuses a drawio diagram whose body is not a draw.io file.
//
// WHY A WRITE CHECK AND NOT A READ ONE. The console's editor reads
// <mxfile><diagram>...</mxfile> - see web/src/lib/diagrams.ts, whose empty
// template is exactly that. A body without the wrapper stores perfectly well,
// reads back byte for byte, parses as XML, and passes ParseDiagramCells, which
// only ever looks for mxCell ids. It then renders as a wall of raw text, and
// nothing between the author and the reader says otherwise.
//
// Measured 2026-08-31, by doing it: a diagram written as a bare <mxGraphModel>
// was accepted, stored, read back, and shown to the operator as XML. Every
// check I ran said it was fine, because every check I ran was about whether the
// bytes survived rather than whether the thing could be drawn.
//
// HERE FOR THE REASON THE OTHER SHAPE CHECKS ARE HERE. Every surface writes
// through upsertArtifact - the API, the memory tools, the FUSE drainer, the
// editor's own save - and a rule kept per surface is a rule the next surface
// forgets. The editor would never produce a body this refuses; everything else
// might, and did.
//
// AN EMPTY BODY IS ALLOWED. A diagram is created before it is drawn - the
// console's own "new" makes the row and opens the editor - and refusing that
// would refuse the ordinary first step. Empty renders as an empty canvas, which
// is honest. It is a body with CONTENT that is not an mxfile that lies.
func checkDiagramRow(a *Artifact) error {
	if a == nil || a.Type != DiagramType || a.Kind != DiagramKindDrawio {
		return nil
	}
	body := strings.TrimSpace(a.Body)
	if body == "" {
		return nil
	}

	// Walked rather than unmarshalled, for ParseDiagramCells's reason: the only
	// question here is what the FIRST element is, and an XML body this build has
	// never seen must not stop the walk before it answers that.
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("store: a %s diagram must be draw.io xml, and this body does not parse: %w",
				DiagramKindDrawio, err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "mxfile" {
			return nil
		}
		return fmt.Errorf(
			"store: a %s diagram body must be an <mxfile>, and this one starts with <%s>. "+
				"draw.io wraps the model as <mxfile><diagram><mxGraphModel>, and the console's "+
				"editor reads that wrapper - a bare <%s> is stored intact and drawn as raw text",
			DiagramKindDrawio, start.Name.Local, start.Name.Local)
	}
}
