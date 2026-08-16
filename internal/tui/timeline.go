package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The timeline: GET /api/activity, which is the everything-view.
//
// It is the everything-view because the log it reads is one log. A turn an
// agent took, a line an fc run wrote, a message somebody said in a room and a
// steer somebody typed into a running job are four kinds on the same table, in
// one order, behind one permission filter. So this pane scrolls them, searches
// them with the node's own contains filter, and posts back into whichever run
// or room the cursor is on.

// kinds are the filters the node accepts, and the order k cycles them in. The
// empty string is everything.
var kinds = []string{"", "turn", "log", "chat", "steer", "worklog"}

// timelineNameWidth is the speaker column: two more than the tail of a ulid
// took before a line could carry a name, which is what an ordinary handle
// needs, and a hard stop for one that is longer. The body runs the rest of the
// line, so every column taken here is one taken from what happened.
const timelineNameWidth = 12

func (m *Model) timelineKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "/":
		return m, m.openInput(inputTimelineQuery, "filter> ", m.tlQuery)
	case "i":
		return m, m.openInput(inputTimelinePost, m.postTarget()+"> ", "")
	case "c":
		m.tlQuery = ""
		return m, m.activityCmd("", m.tlKind)
	case "f":
		// f cycles the kind filter. j/k are taken by movement and the node's
		// own word for this is kind, so f is for filter.
		next := 0
		for i, kind := range kinds {
			if kind == m.tlKind {
				next = (i + 1) % len(kinds)
			}
		}
		m.tlKind = kinds[next]
		return m, m.activityCmd(m.tlQuery, m.tlKind)
	case "enter":
		item := m.selectedActivity()
		if item == nil {
			return m, nil
		}
		if item.Artifact != "" {
			m.backView = viewTimeline
			m.view = viewArtifact
			return m, m.artifactCmd(item.Artifact)
		}
		if item.Room != "" {
			return m, m.openRoom(item.Room)
		}
		return m, nil
	}
	if sel, moved := moveSel(msg.String(), m.tlSel, len(m.tl), m.height/2); moved {
		m.tlSel = sel
	}
	return m, nil
}

func (m *Model) selectedActivity() *ActivityItem {
	if m.tlSel < 0 || m.tlSel >= len(m.tl) {
		return nil
	}
	return m.tl[m.tlSel]
}

// postTarget is where i would post from here, said the way the prompt says it.
func (m *Model) postTarget() string {
	if item := m.selectedActivity(); item != nil && item.Thread != "" {
		if item.Room != "" {
			return item.Room + "/" + shortID(item.Thread)
		}
		return "thread " + shortID(item.Thread)
	}
	if room := m.Room(); room != "" {
		return room
	}
	return "general"
}

func (m *Model) timelineView(height int) []string {
	head := fmt.Sprintf("activity (%d)", len(m.tl))
	if m.tlKind != "" {
		head += " kind=" + m.tlKind
	}
	if m.tlQuery != "" {
		head += fmt.Sprintf(" matching %q", m.tlQuery)
	}
	lines := []string{m.theme.Title.Render(m.theme.clip(head, m.width))}
	if len(m.tl) == 0 {
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(
			"nothing on the timeline for this token - / filters, f changes the kind", m.width)))
		return lines
	}

	start, end := window(len(m.tl), m.tlSel, max(1, height-1))
	for i := start; i < end; i++ {
		item := m.tl[i]
		where := item.Room
		if where == "" && item.Thread != "" {
			where = shortID(item.Thread)
		}
		// The speaker column is fixed at what the tail of an id took, and a
		// name longer than that is cut to it: this pane is full width and the
		// body is what a person is reading along it, so a handle is not allowed
		// to walk the body off the right edge.
		text := fmt.Sprintf("%s %-5s %s %-10s %s",
			clock(item.Created), item.Kind,
			pad(m.theme.clip(item.Speaker(), timelineNameWidth), timelineNameWidth),
			m.theme.clip(where, 10), strings.ReplaceAll(item.Body, "\n", " "))
		text = m.theme.clip(text, m.width)
		switch {
		case i == m.tlSel:
			lines = append(lines, m.theme.Selected.Render(pad(text, m.width)))
		case item.ActorKind == "agent":
			lines = append(lines, m.theme.Agent.Render(text))
		default:
			lines = append(lines, text)
		}
	}
	return lines
}

// clock is the time part of the timestamp the node writes, without parsing it:
// the format is fixed by the handler, and a parse that failed would be a blank
// column for no reason a reader could act on.
func clock(created string) string {
	if len(created) >= 16 {
		return created[11:16]
	}
	return "     "
}
