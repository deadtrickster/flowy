package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ReportKind is the second dashboard style: a document rather than a terminal.
//
// The operator, 2026-08-26: "I want flowy to support two dashboard styles
// serenedash style and this. And I want you all to stop publishing them as
// claude artifacts, and publish to flowy instead." The serenedash style is the
// frame tile - a producer renders ANSI and the console draws it exactly. This is
// the other one, and it exists because the pages that were being published
// elsewhere were leaving the fabric: a dashboard nobody can find from flowy is
// not this node's dashboard.
//
// THE PRODUCER PUSHES STRUCTURE, NOT MARKUP, and that is the whole design
// decision. A frame pushes LINES because a terminal has one glyph per cell and
// the alignment is the meaning; a report pushes SECTIONS because a document
// reflows and the meaning is the hierarchy. Neither pushes HTML: the console
// renders and escapes, so a producer cannot inject - and a reading stays
// something a second renderer could draw differently without the producer
// changing.
const ReportKind = "report"

// ReportSectionKinds is the closed vocabulary, refused by name like every other
// vocabulary on this node.
//
// `columns` rather than `table`: the TILE vocabulary already has a word `table`
// meaning something else - a tile that draws the rows of one metric - and one
// word for two things in one declaration is how a reader ends up drawing the
// wrong component. dead-claude caught it before the renderer was written.
var ReportSectionKinds = []string{"progress", "cards", "columns", "squares"}

// ReportTones is the closed set a producer may ask for. It pushes a WORD and the
// console maps it to a palette - the same rule the frame keeps, and for the same
// reason: a producer that pushed colours would fix them for every theme, and the
// page renders in the reader's.
var ReportTones = []string{"", "good", "warn", "bad", "dim", "accent"}

// ReportRowError is the report twin of MetricRowError.
type ReportRowError struct {
	Row string
	Why string
}

func (e ReportRowError) Error() string { return fmt.Sprintf("report row %s: %s", e.Row, e.Why) }

func (e ReportRowError) depRefusal() {}

// checkReportReading is the shape of a report reading, asked of a metric row
// whose value looks like one.
//
//   - a report with no sections is a page that shows nothing, which is what
//     checkDashboardRow already refuses for a dashboard with no tiles
//   - a section kind outside the vocabulary is a declaration the renderer cannot
//     honour, refused by name rather than drawn as a gap
//   - a progress section with no total cannot place its segments: a bar whose
//     parts do not know what they are parts OF is a row of coloured boxes
//   - a table row with more cells than the section has columns draws off the end
//     of its own header, which is the one way a table lies quietly
func checkReportReading(row string, value json.RawMessage) error {
	var doc struct {
		Sections []struct {
			Kind    string            `json:"kind"`
			Tone    *string           `json:"tone"`
			Total   *float64          `json:"total"`
			Columns []json.RawMessage `json:"columns"`
			Rows    []struct {
				Cells []json.RawMessage `json:"cells"`
			} `json:"rows"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(value, &doc); err != nil {
		return ReportRowError{Row: row, Why: "the reading is not a report document: " + err.Error()}
	}
	if len(doc.Sections) == 0 {
		return ReportRowError{Row: row,
			Why: "a report is its sections - value.sections must carry at least one"}
	}
	for i, s := range doc.Sections {
		if s.Tone != nil && !knownTone(*s.Tone) {
			return ReportRowError{Row: row, Why: fmt.Sprintf(
				"section %d asks for tone %q - the tones are %s, and a producer pushes the WORD "+
					"so the console can map it to the reader's theme",
				i, *s.Tone, strings.Join(ReportTones[1:], ", "))}
		}
		known := false
		for _, k := range ReportSectionKinds {
			if s.Kind == k {
				known = true
				break
			}
		}
		if !known {
			return ReportRowError{Row: row, Why: fmt.Sprintf(
				"section %d declares kind %q - the vocabulary is %s",
				i, s.Kind, strings.Join(ReportSectionKinds, ", "))}
		}
		if s.Kind == "progress" && s.Total == nil {
			return ReportRowError{Row: row, Why: fmt.Sprintf(
				"section %d is a progress bar with no total - segments that do not know what they are "+
					"parts of are a row of coloured boxes", i)}
		}
		if s.Kind == "columns" {
			for r, tr := range s.Rows {
				if len(tr.Cells) > len(s.Columns) {
					return ReportRowError{Row: row, Why: fmt.Sprintf(
						"section %d row %d has %d cells against %d columns - a row that runs past its "+
							"own header is the one way a table lies quietly",
						i, r, len(tr.Cells), len(s.Columns))}
				}
			}
		}
	}
	return nil
}

// IsReportReading reports whether a metric row's value carries a report
// document. Lenient: a reading that is not one is not an error here, it simply
// is not a report - the same contract frameOf keeps on the console side.
func IsReportReading(fields json.RawMessage) bool {
	if len(fields) == 0 {
		return false
	}
	var outer struct {
		Value struct {
			// A POINTER, so PRESENT-BUT-EMPTY is distinguishable from ABSENT.
			// Testing for a non-empty array made `{"sections":[]}` read as "not
			// a report" and skip the guard entirely - which is exactly the
			// document the guard exists to refuse. A report that declares no
			// sections is a page that shows nothing; a reading with no sections
			// KEY is a number, a grid, a frame, and none of this one's business.
			Sections *json.RawMessage `json:"sections"`
		} `json:"value"`
	}
	if err := json.Unmarshal(fields, &outer); err != nil {
		return false
	}
	return outer.Value.Sections != nil
}

func knownTone(t string) bool {
	for _, k := range ReportTones {
		if t == k {
			return true
		}
	}
	return false
}
