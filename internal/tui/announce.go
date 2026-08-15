package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The announcements view, and the banner every other view carries.
//
// The banner is in model.go because it is on every screen; this file is the
// list, the acknowledgement, and posting one.
//
// Posting is where the capability lives, and it lives on the node: a
// federation-scope announcement is a system or monitor agent's to post, and a
// worker agent or a person holding their own token is refused with a 403 that
// says which they are. This client offers the key either way and shows the
// refusal, rather than hiding the key and pretending the rule is a UI decision.

// scopes and severities are the values the node accepts, in the order c and v
// cycle them.
var (
	scopes     = []string{"node", "project", "federation"}
	severities = []string{"info", "warning", "maintenance", "breaking"}
)

func (m *Model) announceKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "a":
		if selected := m.selectedAnnouncement(); selected != nil {
			return m, m.ackCmd(selected.ID)
		}
		return m, nil
	case "v":
		m.annSeverity = cycle(severities, m.annSeverity)
		return m, nil
	case "c":
		m.annScope = cycle(scopes, m.annScope)
		return m, nil
	case "i":
		m.draft = draft{scope: m.annScope, severity: m.annSeverity}
		return m, m.openInput(inputAnnTitle,
			fmt.Sprintf("%s/%s title> ", m.annScope, m.annSeverity), "")
	case "enter":
		if selected := m.selectedAnnouncement(); selected != nil {
			m.backView = viewAnnounce
			m.view = viewArtifact
			return m, m.artifactCmd(selected.ID)
		}
		return m, nil
	}
	if sel, moved := moveSel(msg.String(), m.annSel, len(m.announcements), m.height/2); moved {
		m.annSel = sel
	}
	return m, nil
}

func (m *Model) selectedAnnouncement() *Announcement {
	if m.annSel < 0 || m.annSel >= len(m.announcements) {
		return nil
	}
	return m.announcements[m.annSel]
}

func (m *Model) announceView(height int) []string {
	lines := []string{m.theme.Title.Render(m.theme.clip(
		fmt.Sprintf("active announcements (%d)", len(m.announcements)), m.width))}
	lines = append(lines, m.theme.Dim.Render(m.theme.clip(fmt.Sprintf(
		"  i posts one as %s / %s   (v severity, c scope)", m.annScope, m.annSeverity), m.width)))

	if len(m.announcements) == 0 {
		lines = append(lines, "", m.theme.Dim.Render(m.theme.clip(
			"nothing is being announced to this token", m.width)))
		return lines
	}

	start, end := window(len(m.announcements), m.annSel, max(1, height-3))
	for i := start; i < end; i++ {
		a := m.announcements[i]
		scope := a.Decoded.Scope
		if scope == "" {
			scope = "project"
		}
		extra := ""
		if a.Decoded.Resource != "" {
			extra = fmt.Sprintf(" holds %s (%s)", a.Decoded.Resource, a.Decoded.Mode)
		}
		text := fmt.Sprintf("%-12s %-11s %s%s", a.Severity, scope, a.Title, extra)
		text = m.theme.clip(text, m.width)
		if i == m.annSel {
			lines = append(lines, m.theme.Selected.Render(pad(text, m.width)))
			continue
		}
		lines = append(lines, m.theme.severityStyle(a.Severity).Render(text))
	}

	if selected := m.selectedAnnouncement(); selected != nil && len(lines) < height {
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(m.rule(), m.width)))
		for _, line := range strings.Split(selected.Body, "\n") {
			if len(lines) >= height {
				break
			}
			lines = append(lines, m.theme.clip(line, m.width))
		}
	}
	return lines
}

// cycle is the next value in a ring, starting over from the front.
func cycle(values []string, current string) string {
	for i, value := range values {
		if value == current {
			return values[(i+1)%len(values)]
		}
	}
	return values[0]
}
