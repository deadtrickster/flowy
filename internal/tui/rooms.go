package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The rooms view: the room list down the left, the live stream in the middle,
// the thread of the selected message on the right when it is asked for, and the
// message box at the bottom.
//
// The stream is live because of the long poll in cmds.go, and the long poll is
// a tea.Cmd, so the room updating and the user scrolling the timeline in
// another tab are not competing for the same goroutine.

func (m *Model) roomsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "i":
		// A plain say is to the room, so whatever an interrupted "a" left in
		// the draft does not become the addressee of the next message.
		m.draft.to = ""
		return m, m.openInput(inputSay, m.Room()+"> ", "")
	case "a":
		return m, m.openInput(inputSayTo, "to> ", "")
	case "o":
		return m, m.openInput(inputOpenRoom, "room> ", "")
	case "t":
		m.threadOpen = !m.threadOpen
		if m.threadOpen {
			if selected := m.selectedMessage(); selected != nil {
				return m, m.threadCmd(selected.Thread)
			}
		}
		return m, nil
	case "n":
		if len(m.rooms) > 0 {
			return m, m.openRoom(m.rooms[(m.roomSel+1)%len(m.rooms)])
		}
		return m, nil
	case "p":
		if len(m.rooms) > 0 {
			return m, m.openRoom(m.rooms[(m.roomSel+len(m.rooms)-1)%len(m.rooms)])
		}
		return m, nil
	case "enter":
		if selected := m.selectedMessage(); selected != nil {
			if selected.Artifact != "" {
				m.backView = viewRooms
				m.view = viewArtifact
				return m, m.artifactCmd(selected.Artifact)
			}
			m.threadOpen = true
			return m, m.threadCmd(selected.Thread)
		}
		return m, nil
	}

	if sel, moved := moveSel(msg.String(), m.msgSel, len(m.msgs), m.height/2); moved {
		m.msgSel = sel
		if m.threadOpen {
			if selected := m.selectedMessage(); selected != nil {
				return m, m.threadCmd(selected.Thread)
			}
		}
		return m, nil
	}
	return m, nil
}

// selectedMessage is the message the cursor is on, or nil when the room is
// empty. Every caller checks for nil: an empty room is the ordinary case on a
// fresh node, not an error.
func (m *Model) selectedMessage() *Event {
	if m.msgSel < 0 || m.msgSel >= len(m.msgs) {
		return nil
	}
	return m.msgs[m.msgSel]
}

func (m *Model) roomsView(height int) []string {
	// Three columns, and the two side ones give way first: on an 80-column
	// terminal the room list is 14 wide and the thread pane only appears when
	// it is asked for. Below that the stream takes everything.
	listWidth := 0
	if m.width >= 60 {
		listWidth = 14
	}
	threadWidth := 0
	if m.threadOpen && m.width >= 80 {
		threadWidth = m.width / 3
	}
	streamWidth := m.width - listWidth - threadWidth
	if listWidth > 0 {
		streamWidth -= 1
	}
	if threadWidth > 0 {
		streamWidth -= 1
	}
	if streamWidth < 10 {
		streamWidth, listWidth, threadWidth = m.width, 0, 0
	}

	stream := m.streamLines(streamWidth, height)
	list := m.roomListLines(listWidth, height)
	thread := m.threadLines(threadWidth, height)

	lines := make([]string, height)
	for i := 0; i < height; i++ {
		var b strings.Builder
		if listWidth > 0 {
			b.WriteString(pad(at(list, i), listWidth))
			b.WriteString(m.vrule())
		}
		b.WriteString(pad(at(stream, i), streamWidth))
		if threadWidth > 0 {
			b.WriteString(m.vrule())
			b.WriteString(at(thread, i))
		}
		lines[i] = strings.TrimRight(b.String(), " ")
	}
	return lines
}

// vrule is the column separator, in whatever the terminal can draw.
func (m *Model) vrule() string {
	if m.theme.Unicode {
		return "│"
	}
	return "|"
}

func (m *Model) roomListLines(width, height int) []string {
	if width <= 0 {
		return nil
	}
	lines := []string{m.theme.Title.Render(m.theme.clip("rooms", width))}
	start, end := window(len(m.rooms), m.roomSel, max(1, height-1))
	for i := start; i < end; i++ {
		label := m.theme.clip(m.rooms[i], width)
		if i == m.roomSel {
			lines = append(lines, m.theme.Selected.Render(pad(label, width)))
		} else {
			lines = append(lines, label)
		}
	}
	return lines
}

// streamLines is the room, one message per line, oldest at the top. A message
// too long for the width is clipped rather than wrapped: a wrapped stream makes
// "how far back am I" impossible to answer by counting rows.
func (m *Model) streamLines(width, height int) []string {
	head := fmt.Sprintf("%s (%d)", m.Room(), len(m.msgs))
	if m.watching {
		head += " - watching"
	}
	lines := []string{m.theme.Title.Render(m.theme.clip(head, width))}
	if len(m.msgs) == 0 {
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(
			"nothing here yet - press i to say something", width)))
		return lines
	}

	start, end := window(len(m.msgs), m.msgSel, max(1, height-1))
	for i := start; i < end; i++ {
		e := m.msgs[i]
		// Ids, not handles: what the log carries is a ulid, and the tail of one
		// is the part that differs between two minted a second apart. A room
		// with the whole 26 characters in every row is a room with no space
		// left for what anybody said.
		who := shortID(e.Actor)
		if e.Meta.ActorUser != "" && e.IsAgent() {
			who = shortID(e.Meta.ActorUser) + "'s agent"
		}
		// An addressed message is drawn as one and is otherwise an ordinary
		// message in the room: the room still holds it and the same people
		// still read it, which is why the marker goes in front of the body
		// rather than the message going somewhere else.
		body := strings.ReplaceAll(e.Body, "\n", " ")
		if e.Addressee != "" {
			body = m.addressMark(e.Addressee) + " " + body
		}
		text := fmt.Sprintf("%s %-16s %s",
			e.Created.Local().Format("15:04"), m.theme.clip(who, 16), body)
		text = m.theme.clip(text, width)
		switch {
		case i == m.msgSel:
			lines = append(lines, m.theme.Selected.Render(pad(text, width)))
		case m.addressedToMe(e):
			// For you, from somebody else: the one row in a busy room that is
			// worth finding without reading the room.
			lines = append(lines, m.theme.OK.Render(text))
		case e.IsAgent():
			lines = append(lines, m.theme.Agent.Render(text))
		default:
			lines = append(lines, m.theme.Human.Render(text))
		}
	}
	return lines
}

// addressMark is how an addressee is drawn: "to you" when it is this
// principal, and the short id otherwise. Spelling out "you" rather than making
// the reader match a ulid against their own is the whole value of the field on
// a terminal - a room of 26-character ids is a room where nobody notices.
func (m *Model) addressMark(addressee string) string {
	if m.isMe(addressee) {
		return "->you"
	}
	return "->" + shortID(addressee)
}

// addressedToMe reports whether a message is directed at this principal by
// somebody else. Own messages are excluded for the reason the inbox excludes
// them: a message cannot be news to the person who wrote it, and a token that
// addresses itself would otherwise light up its own row.
func (m *Model) addressedToMe(e *Event) bool {
	return m.isMe(e.Addressee) && !m.isMe(e.Actor)
}

// isMe reports whether an id is this principal - the person or the agent
// working for them, which is the same pair the node treats as one reader.
func (m *Model) isMe(id string) bool {
	if id == "" || m.who == nil {
		return false
	}
	return id == m.who.User || (m.who.Agent != "" && id == m.who.Agent)
}

// threadLines is the DAG pane: the selected message's thread, with each
// message's parents shown, so a branch and a merge are visible as what they
// are rather than as a flat list.
func (m *Model) threadLines(width, height int) []string {
	if width <= 0 {
		return nil
	}
	lines := []string{m.theme.Title.Render(m.theme.clip("thread", width))}
	if len(m.thread) == 0 {
		lines = append(lines, m.theme.Dim.Render(m.theme.clip("no thread selected", width)))
		return lines
	}
	// Which messages in the thread are answered by something else in it: those
	// are the forks, and marking them is the whole reason this pane is a DAG
	// pane and not a second copy of the stream.
	children := map[string]int{}
	for _, e := range m.thread {
		for _, parent := range e.Parents {
			children[parent]++
		}
	}
	for _, e := range m.thread {
		if len(lines) >= height {
			break
		}
		mark := " "
		switch {
		case len(e.Parents) > 1:
			mark = "M" // a merge: this message answers two
		case children[e.ID] > 1:
			mark = "<" // a fork: two messages answer this one
		case len(e.Parents) == 1:
			mark = "|"
		}
		who := e.Actor
		if e.IsAgent() && e.Meta.ActorUser != "" {
			who = e.Meta.ActorUser + "@"
		}
		body := e.Body
		if e.Addressee != "" {
			body = m.addressMark(e.Addressee) + " " + body
		}
		lines = append(lines, m.theme.clip(
			fmt.Sprintf("%s %s %s", mark, shortID(who), body), width))
	}
	return lines
}

// at is the i'th line of a column, or blank when that column has run out.
func at(lines []string, i int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return lines[i]
}
