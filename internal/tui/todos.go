package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// todoStyle is how a status is drawn, which is the same three meanings the
// console draws them with - see statusStyle in web/src/lib/todos.ts.
//
// The colour is the second half of the row and never the whole of it. A
// terminal may be monochrome, piped into less, or read by somebody who cannot
// separate amber from green, and lipgloss quietly renders all three of these as
// plain text on a TERM=dumb - so the status WORD goes on the row as well, every
// time. A queue whose states are only a colour has no states at all for the
// reader who cannot see them.
func (t Theme) todoStyle(status string) lipgloss.Style {
	switch todoRank(status) {
	case 0:
		return t.TodoActive
	case 3:
		return t.TodoDone
	default:
		// Including the statuses this client has never heard of, which sort with
		// the open ones and are drawn as them: unrecognised is not finished.
		return t.TodoOpen
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

// todoOwner is who is doing it: the assignee field if the item carries one, and
// the OWNER line the queue's older items start with if it does not.
//
// Neither is owner_user, which is the ulid of whoever wrote the row - usually
// the one agent that filed the whole queue, and never the answer to "who is
// doing this". Items that say neither render with a dash: an unowned todo is a
// fact about the queue worth seeing, not a row to hide.
//
// The field wins even when it is empty, which is the same order the console and
// the node read these in. Somebody who unassigned a todo through a panel said
// so; falling back to the OWNER line still in its body would undo them here and
// leave the two clients disagreeing about who has the work.
func todoOwner(a *Artifact) string {
	if a == nil {
		return ""
	}
	if named := todoProvenance(a).Assignee; named != nil {
		return somebody(*named)
	}
	for _, line := range splitLines(a.Body) {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "OWNER:"); ok {
			return somebody(rest)
		}
		if line != "" {
			// The convention is the first line of the body. Scanning the whole
			// body would find the word in a sentence about somebody else's item.
			return ""
		}
	}
	return ""
}

// nobodyWords are the ways this queue has said that nobody is carrying an item.
// They all collapse to the empty owner, so every surface says ONE word for one
// state.
//
// It was raised as a todo through the panel itself: 'todo list has "unowned"
// and "unassigned" - looks identical. triage and fix'. Two words for one state
// read as two states and send a reader looking for a distinction that is not
// there. The node normalises what it is handed (nobodyWords in todos.go) and
// the console collapses the same set on the way out (web/src/lib/todos.ts);
// this is the third copy, for the bodies written before either existed.
var nobodyWords = map[string]bool{
	"?": true, "-": true, "none": true, "nobody": true,
	"tbd": true, "unassigned": true, "unowned": true, "n/a": true,
}

// somebody is a name that means somebody, or "" for one of the words above.
func somebody(name string) string {
	name = strings.TrimSpace(name)
	if nobodyWords[strings.ToLower(name)] {
		return ""
	}
	return name
}

// todoFields is what a todo carries in fields: where it was raised, the message
// it came out of, and who is carrying it. It rides fields rather than columns
// for the reason a report's as_of does - see reportProvenance, which is the same
// read of the same column - and anything that does not parse leaves them empty,
// which is a todo with no room on screen and is the truth.
//
// Assignee is a pointer because an empty one is a value: "nobody is carrying
// this" is a thing somebody said, and it has to be told apart from an item that
// predates the field and whose owner is still a line in its body.
type todoFields struct {
	Room     string  `json:"room"`
	Message  string  `json:"message"`
	Assignee *string `json:"assignee"`
	// Raiser is who the work came from, and it is a plain string where the
	// assignee is a pointer, because the two absences are not the same absence.
	// An empty assignee is somebody saying nobody is carrying this; a raiser is
	// settled when the row is raised and never restated, so an absent one is a
	// row that does not say where its work came from - which is every queue item
	// written before the field, and is not a claim anybody made.
	Raiser string `json:"raiser"`
}

func todoProvenance(a *Artifact) todoFields {
	var fields todoFields
	if a == nil || len(a.Fields) == 0 {
		return fields
	}
	_ = json.Unmarshal(a.Fields, &fields)
	return fields
}

// todoRoomOf is the room a todo was raised in, or "" for one raised in none.
func todoRoomOf(a *Artifact) string { return todoProvenance(a).Room }

// todoRaiser is who the work came from: the party that asked for it, which is
// not the party carrying it and is not owner_user either.
//
// owner_user is the seat whose token wrote the row - one agent for a whole
// board it filed on somebody else's behalf - so this client has never drawn it
// and does not start here. A row that says nothing gets nothing: there is no
// older convention to fall back on the way todoOwner falls back on an OWNER
// line, and guessing the author would put a name on the screen that nobody
// claimed.
func todoRaiser(a *Artifact) string { return somebody(todoProvenance(a).Raiser) }

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

// todoOwnerWidth is what each of the two name columns gets. A handle has no
// length limit and a terminal does: at 80 columns the status and the two names
// take 37 of them and the title keeps the rest, which is the point of fixing
// them rather than letting one long name push what the item is off the right of
// the row.
const todoOwnerWidth = 14

// todoStatusColumn is what the status word gets, and it is the width the header
// below lines its own labels up on.
const todoStatusColumn = 8

// todoHeader names the columns, and it is here because the row grew a SECOND
// name.
//
// One name needed no label - a queue row was a state and a person, and which
// person was obvious. Two are not: "raised by" and "carried by" are different
// claims and are frequently different parties, and a reader who has to work out
// which column is which from the values in it will get it wrong on the rows
// where it matters, which are the rows where they differ. The console says the
// words on every row because it has the width for it; a terminal says them once
// at the top.
func todoHeader() string {
	return fmt.Sprintf("%-*s %-*s %-*s %s",
		todoStatusColumn, "status", todoOwnerWidth, "raised by",
		todoOwnerWidth, "carried by", "what")
}

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
	//
	// Two lines are spent before the list now rather than one - the count and
	// the column labels - so the list gets one fewer.
	lines = append(lines, m.theme.Dim.Render(m.theme.clip(todoHeader(), m.width)))
	listHeight := max(1, (height-2)/2)
	start, end := window(len(m.todos), m.todoSel, listHeight)
	for i := start; i < end; i++ {
		a := m.todos[i]
		owner := todoOwner(a)
		if owner == "" {
			owner = "-"
		}
		// One dash for a row that does not say where the work came from, which
		// is the same dash an unowned row gets and says the same thing: nobody
		// stated this. It is not owner_user standing in - see todoRaiser.
		raiser := todoRaiser(a)
		if raiser == "" {
			raiser = "-"
		}
		status := a.Status
		if status == "" {
			status = todoTodo
		}
		text := fmt.Sprintf("%-*s %-*s %-*s %s",
			todoStatusColumn, m.theme.clip(status, todoStatusColumn),
			todoOwnerWidth, m.theme.clip(raiser, todoOwnerWidth),
			todoOwnerWidth, m.theme.clip(owner, todoOwnerWidth), a.Title)
		text = m.theme.clip(text, m.width)
		if i == m.todoSel {
			lines = append(lines, m.theme.Selected.Render(pad(text, m.width)))
			continue
		}
		// The same three states the room's panel draws, because it is the same
		// queue: a reader who learned the colours in one of the two views has
		// learned them in the other.
		lines = append(lines, m.theme.todoStyle(status).Render(text))
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
