package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The todos view: the shared queue, who owns each item, and what it is waiting
// on.
//
// It exists because the queue was real and invisible. Four agents and a person
// working in one room had the state of the work in chat messages and summaries,
// which is to say nowhere: what is in flight, who has it, and what is blocked
// behind what were things you found out by asking. The items themselves have
// been artifacts for a while - type memory, kind todo, project scope - and
// nothing rendered them, so the fabric knew the answer and no surface said it.
//
// The ordering is the feature and not a preference. Active first, then the
// open ones, then done: a list that buries what is in flight under what is
// finished answers none of the three questions somebody opens this to ask. Done
// is kept rather than filtered because "is that finished?" is one of them.
//
// It reads and does not write. Closing a todo is a lifecycle question - what
// done means here, who may say it, and what the trail records - and inventing
// an answer to that on a key in a terminal is how a queue ends up with two
// disagreeing ideas of its own state. mem_write already has one.

// todoStatuses is what an item's status may say, in the order the list puts
// them in. Anything else the queue grows later sorts with the open ones rather
// than with the finished ones: an unrecognised status is work nobody has said
// is done.
const (
	todoActive = "active"
	todoDone   = "done"
	todoTodo   = "todo"
)

// todoRank is where a status sorts. The gap at 2 is deliberate - it is where
// every status this client has never heard of lands, above done and below
// nothing.
func todoRank(status string) int {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case todoActive:
		return 0
	case todoTodo, "":
		return 1
	case todoDone:
		return 3
	default:
		return 2
	}
}

// sortTodos puts the queue in reading order: active, open, done.
//
// It is a stable sort, so within a status the node's own order survives - which
// is what it came back in, and the only ordering the node has an opinion about.
func sortTodos(list []*Artifact) []*Artifact {
	sort.SliceStable(list, func(i, j int) bool {
		return todoRank(list[i].Status) < todoRank(list[j].Status)
	})
	return list
}

// todoOwner is who is doing it: the name on the OWNER line the queue's items
// start with.
//
// It is read out of the body rather than off owner_user, which is the ulid of
// whoever wrote the row - usually the one agent that filed the whole queue, and
// never the answer to "who is doing this". Two items in the queue carry no
// OWNER line at all, and those render with a dash: an unowned todo is a fact
// about the queue worth seeing, not a row to hide.
func todoOwner(a *Artifact) string {
	if a == nil {
		return ""
	}
	for _, line := range splitLines(a.Body) {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "OWNER:"); ok {
			return strings.TrimSpace(rest)
		}
		if line != "" {
			// The convention is the first line of the body. Scanning the whole
			// body would find the word in a sentence about somebody else's item.
			return ""
		}
	}
	return ""
}

// todoCounts is the header: how many are in flight, how many are waiting, and
// how many are finished.
func todoCounts(list []*Artifact) (active, open, done int) {
	for _, a := range list {
		switch todoRank(a.Status) {
		case 0:
			active++
		case 3:
			done++
		default:
			open++
		}
	}
	return active, open, done
}

func (m *Model) todosKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "/":
		return m, m.openInput(inputTodoQuery, "search> ", m.todoQuery)
	case "enter":
		if selected := m.selectedTodo(); selected != nil {
			m.backView = viewTodos
			m.view = viewArtifact
			return m, m.artifactCmd(selected.ID)
		}
		return m, nil
	case "c":
		m.todoQuery = ""
		return m, m.todosCmd("")
	}
	if sel, moved := moveSel(msg.String(), m.todoSel, len(m.todos), m.height/2); moved {
		m.todoSel = sel
	}
	return m, nil
}

func (m *Model) selectedTodo() *Artifact {
	if m.todoSel < 0 || m.todoSel >= len(m.todos) {
		return nil
	}
	return m.todos[m.todoSel]
}

// todoOwnerWidth is what the owner column gets. A handle has no length limit
// and a terminal does: at 80 columns the status and the owner take 23 of them
// and the title keeps the rest, which is the point of fixing it rather than
// letting one long name push what the item is off the right of the row.
const todoOwnerWidth = 14

func (m *Model) todosView(height int) []string {
	active, open, done := todoCounts(m.todos)
	head := fmt.Sprintf("todos (%d active, %d open, %d done)", active, open, done)
	if m.todoQuery != "" {
		head = fmt.Sprintf("todos matching %q (%d active, %d open, %d done)",
			m.todoQuery, active, open, done)
	}
	lines := []string{m.theme.Title.Render(m.theme.clip(head, m.width))}
	if len(m.todos) == 0 {
		// Where they come from, because this pane cannot make one and an empty
		// list otherwise reads as a broken view rather than an empty queue.
		what := "no todos yet - written with mem_write, kind todo"
		if m.todoQuery != "" {
			what = "nothing matched - c clears the search"
		}
		lines = append(lines, m.theme.Dim.Render(m.theme.clip(what, m.width)))
		return lines
	}

	// The same split the memory and reports panes use: the list on top, the
	// selected item under the rule. What is under the rule here is the body,
	// which is where the DEPENDS ON line lives - the half of the question this
	// view was asked for that a row has no room for.
	listHeight := max(1, (height-1)/2)
	start, end := window(len(m.todos), m.todoSel, listHeight)
	for i := start; i < end; i++ {
		a := m.todos[i]
		owner := todoOwner(a)
		if owner == "" {
			owner = "-"
		}
		status := a.Status
		if status == "" {
			status = todoTodo
		}
		text := fmt.Sprintf("%-8s %-*s %s", m.theme.clip(status, 8),
			todoOwnerWidth, m.theme.clip(owner, todoOwnerWidth), a.Title)
		text = m.theme.clip(text, m.width)
		if i == m.todoSel {
			lines = append(lines, m.theme.Selected.Render(pad(text, m.width)))
			continue
		}
		lines = append(lines, text)
	}

	if selected := m.selectedTodo(); selected != nil && len(lines) < height {
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
