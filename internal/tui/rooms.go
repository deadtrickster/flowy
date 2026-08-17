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
		if m.Room() == dmRoom {
			// There is no room to fall back to here, so i asks who it is for
			// rather than opening a box whose message has nowhere to go.
			return m, m.openInput(inputSayTo, "dm to> ", "")
		}
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
	case "T":
		// The room's plan, hidden and brought back. It is open by default and
		// this is how somebody gets the width back for the conversation;
		// reopening it re-reads rather than redrawing what was true when it was
		// hidden. A terminal too narrow to hold the pane says so rather than
		// leaving the key looking broken.
		m.roomTodosOpen = !m.roomTodosOpen
		if !m.roomTodosOpen {
			return m, nil
		}
		if _, _, todosWidth, threadWidth := m.roomPaneWidths(); todosWidth == 0 {
			// Which of the two it is, because they are different things to do
			// about it: one is a key away and the other is not.
			if threadWidth > 0 {
				m.say("the plan and the thread do not both fit here - t closes the thread")
			} else {
				m.say("the todos pane needs a wider terminal than this one")
			}
		}
		return m, m.roomTodosCmd(m.gen, m.Room())
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

// The name column, in the stream and in the thread pane. A row is one line and
// is clipped to the pane it is in, so the two columns share the width: whatever
// the name takes, the body does not get. A handle is longer than the tail of a
// ulid and has no bound at all - the column is what stops one from pushing what
// somebody said off the right edge of an 80-column terminal, and a name too
// long for it is cut rather than let through.
//
// 16 in the stream is what the row already budgeted for a speaker, so a name
// costs the body nothing it was not costing already. The thread pane is a third
// of the terminal, so its column is narrower and the mark and the body still
// have somewhere to go.
//
// One more than that since authorship: the column holds a ~ in front of a name
// this node cannot check a signature for - see Event.Named - and taking that
// character out of the name would have shortened every name in the fabric to
// pay for a mark that is on most of them.
const (
	streamNameWidth = 17
	threadNameWidth = 13
)

// selectedMessage is the message the cursor is on, or nil when the room is
// empty. Every caller checks for nil: an empty room is the ordinary case on a
// fresh node, not an error.
func (m *Model) selectedMessage() *Event {
	if m.msgSel < 0 || m.msgSel >= len(m.msgs) {
		return nil
	}
	return m.msgs[m.msgSel]
}

// roomPaneWidths divides the terminal between the four columns of this view.
//
// The side panes appear at a width each: the room list at 60 columns, the
// thread and the todos panes at 80. Past that the terminal is a budget, and
// when the conversation would be squeezed under minStreamWidth the panes give
// their columns back in a fixed order - THE ROOM LIST FIRST, then the todos
// pane, and the thread last. Below all of it the stream takes everything.
//
// The room list goes first because it is the one pane whose content is already
// somewhere else on the screen: the stream header names the room being read,
// and n, p and o move between rooms without it. Losing it costs a reader a
// glance and nothing else. The todos pane goes before the thread because the
// thread is a pane somebody opened on purpose to read one message's answers,
// while the plan is open by default - and T closes it, which is also how
// somebody on a narrow terminal gets the room list back.
//
// That order is the whole reason the plan is on an 80-column terminal at all,
// which is the width this client is written for and the width the pane used to
// vanish at. Three panes and a readable conversation do not fit in 80 columns:
// 14 for the list plus minStreamWidth for the stream plus the rules between
// them leaves 16 for a todo, which is a status word and half a name. A pane
// that draws and is clipped to nothing is not a pane - present-but-clipped and
// absent look identical to the person in front of it - so the columns go where
// something can be read rather than to whatever fits.
//
// It is one function rather than arithmetic inside the render because the key
// that opens the pane has to know whether opening it will show anything: a key
// that silently does nothing on an 80-column terminal is worse than a key that
// says why.
func (m *Model) roomPaneWidths() (listWidth, streamWidth, todosWidth, threadWidth int) {
	if m.width >= 60 {
		listWidth = roomListWidth
	}
	if m.threadOpen && m.width >= 80 {
		threadWidth = m.width / 3
	}
	if m.roomTodosOpen && m.width >= 80 {
		todosWidth = max(m.width/4, minTodosWidth)
	}
	rule := func(width int) int {
		if width > 0 {
			return 1
		}
		return 0
	}
	streamOf := func() int {
		return m.width - listWidth - threadWidth - todosWidth -
			rule(listWidth) - rule(threadWidth) - rule(todosWidth)
	}
	streamWidth = streamOf()
	// The list gives way to another pane and never to the stream on its own: at
	// 60 columns with nothing else open it is exactly what the 60-column
	// threshold above put there, and taking it away again to widen a stream
	// nobody asked to widen would leave the threshold meaning nothing.
	if streamWidth < minStreamWidth && listWidth > 0 && (todosWidth > 0 || threadWidth > 0) {
		listWidth = 0
		streamWidth = streamOf()
	}
	if streamWidth < minStreamWidth && todosWidth > 0 {
		todosWidth = 0
		streamWidth = streamOf()
	}
	if streamWidth < 10 {
		streamWidth, listWidth, threadWidth, todosWidth = m.width, 0, 0, 0
	}
	return listWidth, streamWidth, todosWidth, threadWidth
}

func (m *Model) roomsView(height int) []string {
	listWidth, streamWidth, todosWidth, threadWidth := m.roomPaneWidths()

	stream := m.streamLines(streamWidth, height)
	list := m.roomListLines(listWidth, height)
	thread := m.threadLines(threadWidth, height)
	todos := m.roomTodoLines(todosWidth, height)

	lines := make([]string, height)
	for i := 0; i < height; i++ {
		var b strings.Builder
		if listWidth > 0 {
			b.WriteString(pad(at(list, i), listWidth))
			b.WriteString(m.vrule())
		}
		b.WriteString(pad(at(stream, i), streamWidth))
		if todosWidth > 0 {
			b.WriteString(m.vrule())
			b.WriteString(pad(at(todos, i), todosWidth))
		}
		if threadWidth > 0 {
			b.WriteString(m.vrule())
			b.WriteString(at(thread, i))
		}
		lines[i] = strings.TrimRight(b.String(), " ")
	}
	return lines
}

// The widths the panes are worth, and the floor under the conversation.
//
// minStreamWidth is how narrow the conversation may get before a pane gives its
// column back: a timestamp, the whole of the name column, and two dozen columns
// of what somebody said. The room is what this view is, and a plan beside
// messages clipped to three words is two panes and no conversation.
//
// minTodosWidth is the floor under the plan, and it is the width of a row that
// says something: the status word (todoStatusWidth), a space, and the same
// 14-column budget the todos view gives a handle - 21 columns for "active" and
// a name, with a little over so a title line is more than two words. A quarter
// of the terminal is more than that from 96 columns up and less than it below,
// so the floor is what puts a readable pane on an 80-column terminal.
const (
	minStreamWidth = 48
	minTodosWidth  = 24
	roomListWidth  = 14
)

// todoStatusWidth is the status column in the plan pane: "active" is the
// longest of the three words, and fixing the column is what makes the names
// under each other line up instead of starting wherever the status ended.
const todoStatusWidth = 6

// roomTodoLines is the plan pane: the todos raised in this room, in the same
// reading order the todos view puts a queue in - active, then open, then done -
// and with whoever is carrying each one on the row, because "who has this" is
// the question asked of a plan beside a conversation. The name is the assignee
// the panel set if there is one and the body's OWNER line if there is not - see
// todoOwner, which is the order the node and the console read it in too.
//
// It is narrow, so it is two lines per item rather than a truncated one: the
// status and the name on the first, the title on the second. A title cut at
// twenty columns is a row that says nothing.
//
// The status is a WORD in a colour and never a colour alone. That is the half
// of this that survives a monochrome terminal, a NO_COLOR environment, a pipe,
// and a reader who cannot separate amber from green - and all four of those are
// ordinary here, since the client is written for whatever terminal is at the
// other end of an ssh session.
func (m *Model) roomTodoLines(width, height int) []string {
	if width <= 0 {
		return nil
	}
	active, open, done := todoCounts(m.roomTodos)
	head := fmt.Sprintf("todos %d/%d/%d", active, open, done)
	lines := []string{m.theme.Title.Render(m.theme.clip(head, width))}
	if len(m.roomTodos) == 0 {
		// Named, so an empty pane reads as a room that has not written anything
		// down rather than as a pane that failed to load.
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(
			"nothing raised in "+m.Room(), width)))
		return lines
	}
	for _, a := range m.roomTodos {
		if len(lines) >= height {
			break
		}
		status := a.Status
		if status == "" {
			status = todoTodo
		}
		owner := todoOwner(a)
		if owner == "" {
			// One dash for every way the queue says nobody - see nobodyWords.
			owner = "-"
		}
		// pad rather than %-6s, for the reason the stream's name column is
		// padded: a clipped word ends in an ellipsis that is one column and
		// three bytes, and a column padded by byte count stops lining up the
		// moment something is too long for it.
		row := pad(m.theme.clip(status, todoStatusWidth), todoStatusWidth) + " " + owner
		lines = append(lines, m.theme.todoStyle(status).Render(m.theme.clip(row, width)))
		if len(lines) >= height {
			break
		}
		lines = append(lines, m.theme.clip(a.Title, width))
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
	if m.Room() == dmRoom {
		head = fmt.Sprintf("direct messages (%d) - private", len(m.msgs))
	}
	if m.watching {
		head += " - watching"
	}
	lines := []string{m.theme.Title.Render(m.theme.clip(head, width))}
	if len(m.msgs) == 0 {
		empty := "nothing here yet - press i to say something"
		if m.Room() == dmRoom {
			empty = "no direct messages - press i to send one, and only the two of you read it"
		}
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(empty, width)))
		return lines
	}

	start, end := window(len(m.msgs), m.msgSel, max(1, height-1))
	for i := start; i < end; i++ {
		e := m.msgs[i]
		// The name the node recorded, and the tail of a ulid when the message
		// has none - see Event.Speaker. A room of four agents where every row
		// said CM5BYZ3W is what this column is for: the ids differ, and no
		// reader can tell which of them is which.
		// ...with a ~ in front of it when this node holds no signature of that
		// speaker's own for the row - see Event.Named. A message relayed by a
		// pinned peer under somebody else's name is drawn exactly like their own
		// message without it, which is the whole of what that peer was buying.
		who := e.Named()
		// An addressed message is drawn as one and is otherwise an ordinary
		// message in the room: the room still holds it and the same people
		// still read it, which is why the marker goes in front of the body
		// rather than the message going somewhere else.
		body := m.marked(e) + strings.ReplaceAll(e.Body, "\n", " ")
		// pad rather than %-16s: a clipped name ends in an ellipsis, which is
		// one column and three bytes, and a column padded by byte count is a
		// column that stops lining up the moment a name is too long for it.
		text := fmt.Sprintf("%s %s %s", e.Created.Local().Format("15:04"),
			pad(m.theme.clip(who, streamNameWidth), streamNameWidth), body)
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

// marked is what goes in front of a message's body: who it is for, and whether
// the two of them are the only ones who can read it.
//
// The word is spelled out on every private row rather than shown once at the top
// of the pane, and that is the point of it. Private messages and room messages
// are drawn by the same code in the same shapes, they arrive in the same
// timeline, and a person deciding what to type next is looking at a line and not
// at a header. A private message that looked identical to a public one would be
// a trap for whoever writes the next one - which is the direction the mistake
// goes, because nobody is harmed by being reminded a room is a room.
//
// It reads the node's own answer - see Event.Private - and never the pane it is
// being drawn in.
func (m *Model) marked(e *Event) string {
	switch {
	case e.Private:
		return "*private " + m.addressMark(e.Addressee) + " "
	case e.Addressee != "":
		return m.addressMark(e.Addressee) + " "
	}
	return ""
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
		body := m.marked(e) + e.Body
		// The same speaker the stream draws, in a narrower column: this pane is
		// a third of the terminal and the mark in front of it is what the pane
		// is for, so the name gives way first.
		lines = append(lines, m.theme.clip(fmt.Sprintf("%s %s %s",
			mark, m.theme.clip(e.Named(), threadNameWidth), body), width))
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
