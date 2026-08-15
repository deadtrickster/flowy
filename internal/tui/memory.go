package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// The memory view: browse, full-text search, read, and write back.
//
// Search is GET /api/search, which ranks title, body, discovery and tags
// through the same permission filter a list does - so a hit is something this
// token may read, and a miss is not evidence that nothing is there.
//
// Writing is the part with a rule attached. Memory in this node is personal by
// default, and this client never quietly changes that: a new memory is written
// with visibility personal, an edit of one carries whatever the artifact
// already had, and making something project-visible is done deliberately
// elsewhere. A terminal client that promoted a private note the first time
// somebody fixed a typo in it would be a leak with a keyboard shortcut.

func (m *Model) memoryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "/":
		return m, m.openInput(inputMemQuery, "search> ", m.memQuery)
	case "i":
		// note is what the memory surface calls an item with nothing else said
		// about it, and personal is where one lands unless somebody says
		// otherwise. Both are the node's defaults, written out rather than
		// left to whichever endpoint happens to fill them in.
		m.draft = draft{personal: true, kind: "note"}
		return m, m.openInput(inputMemTitle, "title> ", "")
	case "e":
		selected := m.selectedMemory()
		if selected == nil {
			return m, nil
		}
		m.draft = draft{
			id:    selected.ID,
			title: selected.Title,
			body:  selected.Body,
			kind:  selected.Kind,
			// Carried, not decided: an edit keeps the visibility the artifact
			// already had.
			personal: selected.Visibility == "personal",
		}
		return m, m.openInput(inputMemTitle, "title> ", selected.Title)
	case "enter":
		if selected := m.selectedMemory(); selected != nil {
			m.backView = viewMemory
			m.view = viewArtifact
			return m, m.artifactCmd(selected.ID)
		}
		return m, nil
	case "c":
		m.memQuery = ""
		return m, m.memoryCmd("")
	}
	if sel, moved := moveSel(msg.String(), m.memSel, len(m.memory), m.height/2); moved {
		m.memSel = sel
	}
	return m, nil
}

func (m *Model) selectedMemory() *Artifact {
	if m.memSel < 0 || m.memSel >= len(m.memory) {
		return nil
	}
	return m.memory[m.memSel]
}

func (m *Model) memoryView(height int) []string {
	head := fmt.Sprintf("memory (%d)", len(m.memory))
	if m.memQuery != "" {
		head = fmt.Sprintf("memory matching %q (%d)", m.memQuery, len(m.memory))
	}
	lines := []string{m.theme.Title.Render(m.theme.clip(head, m.width))}
	if len(m.memory) == 0 {
		what := "no memory yet - press i to write one"
		if m.memQuery != "" {
			what = "nothing matched - c clears the search"
		}
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(what, m.width)))
		return lines
	}

	// The list takes the top half and the selected item's body the bottom, so
	// reading one does not mean losing your place in the list.
	listHeight := max(1, (height-1)/2)
	start, end := window(len(m.memory), m.memSel, listHeight)
	for i := start; i < end; i++ {
		a := m.memory[i]
		text := fmt.Sprintf("%-10s %-8s %s", m.theme.clip(a.Scope(), 10),
			m.theme.clip(a.Status, 8), a.Title)
		text = m.theme.clip(text, m.width)
		if i == m.memSel {
			lines = append(lines, m.theme.Selected.Render(pad(text, m.width)))
			continue
		}
		lines = append(lines, text)
	}

	if selected := m.selectedMemory(); selected != nil && len(lines) < height {
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(m.rule(), m.width)))
		lines = append(lines, m.theme.Badge.Render(m.theme.clip(m.badges(selected), m.width)))
		for _, line := range splitLines(selected.Body) {
			if len(lines) >= height {
				break
			}
			lines = append(lines, m.theme.clip(line, m.width))
		}
	}
	return lines
}

// rule is a horizontal divider in whatever the terminal draws.
func (m *Model) rule() string {
	if m.theme.Unicode {
		return repeat("─", m.width)
	}
	return repeat("-", m.width)
}

func repeat(s string, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// splitLines breaks a body into lines without allocating a slice per call site.
func splitLines(body string) []string {
	if body == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(body); i++ {
		if body[i] == '\n' {
			lines = append(lines, body[start:i])
			start = i + 1
		}
	}
	return append(lines, body[start:])
}
