package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The reports view: the published documents - research, designs, reviews - that
// a project reads on purpose, listed and opened from the terminal.
//
// It exists because the TUI reached artifacts only through the activity feed,
// which carries what report_write emits and nothing else. A report filed
// straight over POST /api/artifacts was listed in the console and invisible
// here, and one client seeing what the other cannot is worse than either
// missing it: the reader has no way to tell an empty list from a blind one. So
// this pane asks the store for reports the way the console does, and shows what
// the permission filter allows.
//
// It reads and does not write, which is the one deliberate asymmetry with the
// memory pane next door. A report carries what it was true of (as_of) and what
// it replaces (supersedes); without those it is a claim with no expiry, which
// is exactly what the type was invented to avoid. report_write asks for both. A
// title-then-body compose in a terminal would ask for neither and publish
// anyway, so the composing stays where the provenance is.

func (m *Model) reportsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "/":
		return m, m.openInput(inputRepQuery, "search> ", m.repQuery)
	case "enter":
		if selected := m.selectedReport(); selected != nil {
			m.backView = viewReports
			m.view = viewArtifact
			return m, m.artifactCmd(selected.ID)
		}
		return m, nil
	case "c":
		m.repQuery = ""
		return m, m.reportsCmd("")
	}
	if sel, moved := moveSel(msg.String(), m.repSel, len(m.reports), m.height/2); moved {
		m.repSel = sel
	}
	return m, nil
}

func (m *Model) selectedReport() *Artifact {
	if m.repSel < 0 || m.repSel >= len(m.reports) {
		return nil
	}
	return m.reports[m.repSel]
}

func (m *Model) reportsView(height int) []string {
	head := fmt.Sprintf("reports (%d)", len(m.reports))
	if m.repQuery != "" {
		head = fmt.Sprintf("reports matching %q (%d)", m.repQuery, len(m.reports))
	}
	lines := []string{m.theme.Title.Render(m.theme.clip(head, m.width))}
	if len(m.reports) == 0 {
		// An empty pane says where reports come from rather than only that there
		// are none: this client cannot publish one, so "press i" would be a lie
		// and silence would read as a broken view.
		what := "no reports yet - published with report_write over MCP"
		if m.repQuery != "" {
			what = "nothing matched - c clears the search"
		}
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(what, m.width)))
		return lines
	}

	// Same split as the memory pane: the list on top and the selected document
	// below it, so reading one does not mean losing your place in the list. A
	// report is longer than a memory, so what is under the rule is a preview -
	// enter opens it in the artifact view, which scrolls.
	listHeight := max(1, (height-1)/2)
	start, end := window(len(m.reports), m.repSel, listHeight)
	for i := start; i < end; i++ {
		a := m.reports[i]
		// as_of is in the row and not only in the preview: which commit a report
		// was true of is how a reader tells a current one from a stale one, and
		// that decision is made while looking at the list.
		asOf := reportProvenance(a).AsOf
		if asOf == "" {
			asOf = "-"
		}
		text := fmt.Sprintf("%s  %-12s %s", a.Updated.Local().Format("01-02 15:04"),
			m.theme.clip(asOf, 12), a.Title)
		text = m.theme.clip(text, m.width)
		if i == m.repSel {
			lines = append(lines, m.theme.Selected.Render(pad(text, m.width)))
			continue
		}
		lines = append(lines, text)
	}

	if selected := m.selectedReport(); selected != nil && len(lines) < height {
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(m.rule(), m.width)))
		lines = append(lines, m.theme.Badge.Render(m.theme.clip(m.badges(selected), m.width)))
		if line := provenanceLine(selected); line != "" {
			lines = append(lines, m.theme.Label.Render(m.theme.clip(line, m.width)))
		}
		for _, line := range splitLines(selected.Body) {
			if len(lines) >= height {
				break
			}
			lines = append(lines, m.theme.clip(line, m.width))
		}
	}
	return lines
}

// reportFields is the provenance a report carries: what it was true of, and
// which report it replaces.
//
// It rides fields rather than columns, so it is read the way the console reads
// it and never assumed. Anything that does not parse leaves it empty: a report
// whose provenance is missing or malformed is a report with no provenance on
// screen, which is the truth, and its body is still readable - which is what
// somebody opened the pane for.
type reportFields struct {
	AsOf       string `json:"as_of"`
	Supersedes string `json:"supersedes"`
}

func reportProvenance(a *Artifact) reportFields {
	var fields reportFields
	if a == nil || len(a.Fields) == 0 {
		return fields
	}
	_ = json.Unmarshal(a.Fields, &fields)
	return fields
}

// provenanceLine is the as_of/supersedes row under the badges, and empty when a
// report carries none of it.
//
// "replaced by" is the half that is not on the row: supersedes points at what
// this document replaced, which is history, and replaced_by says this document
// has been overtaken, which is the reader's problem right now. The node derives
// it through the read filter, so it is here exactly when there is a newer
// report this token may go and read.
func provenanceLine(a *Artifact) string {
	fields := reportProvenance(a)
	var parts []string
	if fields.AsOf != "" {
		parts = append(parts, "as of "+fields.AsOf)
	}
	if fields.Supersedes != "" {
		parts = append(parts, "supersedes "+shortID(fields.Supersedes))
	}
	if a != nil && a.ReplacedBy != "" {
		parts = append(parts, "REPLACED BY "+shortID(a.ReplacedBy))
	}
	return strings.Join(parts, "  ")
}
