package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The artifact view: the body, the badges, the status trail, and the moves the
// workflow allows from here.
//
// The moves come from the node - GET /api/artifact/{id}/history answers with
// next - rather than from a copy of the lifecycle rules kept here. That is the
// same reason the console reads them: two ideas about which transitions are
// legal is one too many, and the one that is wrong is always the client's.

func (m *Model) artifactKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.statusPin {
		// The picker has the digits while it is up, which is why the digit
		// view-switch defers to it in onKey.
		if key := msg.String(); len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			index := int(key[0] - '1')
			if m.history != nil && index < len(m.history.Next) {
				status := m.history.Next[index]
				m.statusPin = false
				return m, m.statusCmd(m.artifact.ID, status)
			}
			return m, nil
		}
		if msg.String() == "s" {
			m.statusPin = false
		}
		return m, nil
	}
	switch msg.String() {
	case "s":
		if m.artifact == nil {
			return m, nil
		}
		if m.history == nil || len(m.history.Next) == 0 {
			m.flash, m.flashOK = "nothing this artifact may move to from "+m.artifact.Status, false
			return m, nil
		}
		m.statusPin = true
		return m, nil
	case "t":
		if m.artifact != nil {
			m.view = viewRooms
			m.threadOpen = true
			return m, m.threadCmd(m.artifact.ID)
		}
		return m, nil
	}
	if scroll, moved := moveSel(msg.String(), m.scroll, m.scrollMax+1, m.height/2); moved {
		m.scroll = scroll
	}
	return m, nil
}

func (m *Model) artifactView(height int) []string {
	if m.artifact == nil {
		return []string{m.theme.Dim.Render(m.theme.clip(
			"no artifact open - open one from the inbox, memory or the timeline", m.width))}
	}
	a := m.artifact

	lines := []string{
		m.theme.Title.Render(m.theme.clip(a.Title, m.width)),
		m.theme.Badge.Render(m.theme.clip(m.badges(a), m.width)),
		m.theme.Dim.Render(m.theme.clip(
			fmt.Sprintf("%s  owner %s  node %s  updated %s",
				shortID(a.ID), shortID(a.OwnerUser), a.Node, a.Updated.Local().Format("2006-01-02 15:04")),
			m.width)),
		"",
	}

	for _, line := range strings.Split(a.Body, "\n") {
		lines = append(lines, m.theme.clip(line, m.width))
	}
	if a.Discovery != "" {
		lines = append(lines, "", m.theme.Label.Render("discovery"))
		for _, line := range strings.Split(a.Discovery, "\n") {
			lines = append(lines, m.theme.clip(line, m.width))
		}
	}

	if m.history != nil {
		lines = append(lines, "", m.theme.Label.Render(m.theme.clip("history", m.width)))
		for _, e := range m.history.Events {
			lines = append(lines, m.theme.clip(fmt.Sprintf("  %s %-10s %s",
				e.Created.Local().Format("01-02 15:04"), shortID(e.Actor), e.Body), m.width))
		}
		if m.statusPin {
			var choices []string
			for i, next := range m.history.Next {
				choices = append(choices, fmt.Sprintf("%d) %s", i+1, next))
			}
			lines = append(lines, "", m.theme.Title.Render(m.theme.clip(
				"move to: "+strings.Join(choices, "  ")+"   (esc cancels)", m.width)))
		} else if len(m.history.Next) > 0 {
			lines = append(lines, "", m.theme.Dim.Render(m.theme.clip(
				"s to move: "+strings.Join(m.history.Next, ", "), m.width)))
		}
	}

	// The scroll is reset whenever another artifact is opened - see enter and
	// the artifact message - so a long one is readable and the next one still
	// opens at its title rather than halfway down.
	return m.scrolled(lines, height)
}

// badges is the type/scope/status/tags row.
func (m *Model) badges(a *Artifact) string {
	parts := []string{"[" + a.Type + "]", "[" + a.Scope() + "]"}
	if a.Status != "" {
		parts = append(parts, "["+a.Status+"]")
	}
	if a.Severity != "" {
		parts = append(parts, "["+a.Severity+"]")
	}
	if a.Kind != "" {
		parts = append(parts, "["+a.Kind+"]")
	}
	for _, tag := range a.UserTags {
		parts = append(parts, "#"+tag)
	}
	for _, tag := range a.Tags {
		parts = append(parts, "+"+tag)
	}
	if a.Tombstone {
		parts = append(parts, "[deleted]")
	}
	return strings.Join(parts, " ")
}
