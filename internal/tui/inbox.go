package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// The inbox: the handoffs and tasks assigned to this principal, from
// GET /api/inbox/tasks.
//
// Three things can be done to one from here, and all three are the endpoints
// the console uses: delegate it to your agent, mark it done, and jump to the
// artifact it is about. Nothing is decided locally - a delegate the node
// refuses because you are not the assignee shows the node's refusal.

func (m *Model) inboxKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	task := m.selectedTask()
	switch msg.String() {
	case "enter":
		if task != nil {
			m.backView = viewInbox
			m.view = viewArtifact
			return m, m.artifactCmd(task.Artifact)
		}
		return m, nil
	case "d":
		if task != nil {
			return m, m.delegateCmd(task.ID)
		}
		return m, nil
	case "x":
		if task != nil {
			return m, m.taskStateCmd(task.ID, "done")
		}
		return m, nil
	case "O":
		if task != nil {
			return m, m.taskStateCmd(task.ID, "open")
		}
		return m, nil
	case "t":
		// The conversation the handoff happens in. It is a thread of the log
		// like any other, so the rooms view can show it.
		if task != nil {
			m.view = viewRooms
			m.threadOpen = true
			return m, m.threadCmd(task.Thread)
		}
		return m, nil
	}
	if sel, moved := moveSel(msg.String(), m.taskSel, len(m.tasks), m.height/2); moved {
		m.taskSel = sel
	}
	return m, nil
}

func (m *Model) selectedTask() *Task {
	if m.taskSel < 0 || m.taskSel >= len(m.tasks) {
		return nil
	}
	return m.tasks[m.taskSel]
}

func (m *Model) inboxView(height int) []string {
	lines := []string{m.theme.Title.Render(m.theme.clip(
		fmt.Sprintf("tasks assigned to you (%d)", len(m.tasks)), m.width))}
	if len(m.tasks) == 0 {
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(
			"nothing assigned - a handoff arrives here as a task and a thread", m.width)))
		return lines
	}

	start, end := window(len(m.tasks), m.taskSel, max(1, height-1))
	for i := start; i < end; i++ {
		t := m.tasks[i]
		title := t.ArtifactTitle
		if title == "" {
			// No title means the share is not live: the task row is readable
			// and the artifact behind it is not. Saying so beats an empty cell.
			title = "(the artifact is not shared with you)"
		}
		agent := ""
		if t.AssigneeAgent != "" {
			agent = " -> " + shortID(t.AssigneeAgent)
		}
		text := fmt.Sprintf("%-9s %-8s from %-8s %s%s",
			t.State, shortID(t.ID), shortID(t.FromUser), title, agent)
		text = m.theme.clip(text, m.width)
		if i == m.taskSel {
			lines = append(lines, m.theme.Selected.Render(pad(text, m.width)))
			continue
		}
		lines = append(lines, text)
	}
	return lines
}
