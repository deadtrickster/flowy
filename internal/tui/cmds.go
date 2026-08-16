package tui

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Every call to the node happens here, in a tea.Cmd.
//
// That is the whole of the "the UI never freezes" rule: bubbletea runs a Cmd on
// a goroutine of its own and delivers whatever it returns back to Update as a
// message, so the update loop is never the thing waiting on a socket. The long
// poll is the case that matters - it blocks on the server for up to its window -
// and it is a Cmd like the rest, which is why a quiet room does not stop the
// timeline scrolling.
//
// None of these return an error to the caller: an error is a message like any
// other, because a view that could not load is a thing to render rather than a
// reason to stop.

// callTimeout is the ceiling on an ordinary read. It is above the client's own
// timeout so the client's message is the one that surfaces.
const callTimeout = 25 * time.Second

func callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), callTimeout)
}

// ------------------------------------------------------------------ messages

type whoamiMsg struct {
	who *Whoami
	err error
}

type roomMsg struct {
	gen  int
	page *ChatPage
	err  error
}

// waitMsg is a long poll coming back. gen says which room asked: a poll issued
// for a room the user has since left is dropped rather than merged into the one
// they are looking at now.
type waitMsg struct {
	gen  int
	page *ChatPage
	err  error
}

type sentMsg struct {
	event *Event
	err   error
}

type threadMsg struct {
	events []*Event
	err    error
}

type tasksMsg struct {
	tasks []*Task
	err   error
}

type taskMoveMsg struct {
	move *TaskMove
	err  error
}

type artifactMsg struct {
	artifact *Artifact
	history  *History
	err      error
}

type statusMoveMsg struct {
	move *StatusMove
	err  error
}

type memoryMsg struct {
	query     string
	artifacts []*Artifact
	err       error
}

type wroteMsg struct {
	artifact *Artifact
	err      error
}

type reportsMsg struct {
	query     string
	artifacts []*Artifact
	err       error
}

type todosMsg struct {
	query     string
	artifacts []*Artifact
	err       error
}

// roomTodosMsg is one room's todos coming back. gen says which room asked, the
// way waitMsg does: an answer for a room the user has left is dropped rather
// than drawn beside another room's conversation.
type roomTodosMsg struct {
	gen       int
	room      string
	artifacts []*Artifact
	err       error
}

type activityMsg struct {
	page *ActivityPage
	err  error
}

type postedMsg struct {
	item *ActivityItem
	err  error
}

type metricsMsg struct {
	metrics *Metrics
	err     error
}

type announcementsMsg struct {
	list []*Announcement
	err  error
}

type announcedMsg struct {
	announcement *Announcement
	err          error
}

type ackedMsg struct {
	err error
}

// tickMsg is the periodic refresh of everything that has no watcher of its own:
// the announcements banner, and whichever view is on screen.
type tickMsg time.Time

// refreshEvery is how often that happens. It is slow on purpose - the room has
// a long poll and does not need it, and a metrics query is not free.
const refreshEvery = 20 * time.Second

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// ------------------------------------------------------------------ commands

func (m *Model) whoamiCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		who, err := m.client.Whoami(ctx)
		return whoamiMsg{who: who, err: err}
	}
}

// roomCmd loads whatever the room list is pointing at. The private entry is not
// a room, so it reads the private log instead - the branch is here rather than
// in the client because the client's two calls are two different endpoints and
// pretending otherwise would put a room name into a URL that has none.
func (m *Model) roomCmd(gen int, room string, since int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		if room == dmRoom {
			page, err := m.client.DMs(ctx, since)
			return roomMsg{gen: gen, page: page, err: err}
		}
		page, err := m.client.Room(ctx, room, since)
		return roomMsg{gen: gen, page: page, err: err}
	}
}

// waitCmd is the watcher. The window is short relative to the node's ceiling so
// that a room the user has left stops being polled within seconds rather than
// holding a request open for the whole of the node's window.
func (m *Model) waitCmd(gen int, room string, cursor int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if room == dmRoom {
			page, err := m.client.WaitDMs(ctx, cursor, 8)
			return waitMsg{gen: gen, page: page, err: err}
		}
		page, err := m.client.Wait(ctx, room, cursor, 8)
		return waitMsg{gen: gen, page: page, err: err}
	}
}

// sayCmd posts what was typed, into a room or as a direct message.
//
// A direct message with nobody to send it to never leaves this process. The
// endpoint would refuse it anyway - the addressee is the path - but a message
// somebody typed while looking at a private pane must not fall back to a room:
// that is the one mistake here that would publish something the writer thought
// was private.
func (m *Model) sayCmd(room, body, thread, to string, parents []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		if room == dmRoom {
			if to == "" {
				return sentMsg{err: errNoAddressee}
			}
			event, err := m.client.SendDM(ctx, to, body, parents, thread)
			return sentMsg{event: event, err: err}
		}
		event, err := m.client.Say(ctx, room, body, parents, thread, to)
		return sentMsg{event: event, err: err}
	}
}

// errNoAddressee is what a private message with nobody to send it to comes back
// as. It is a message and not a refusal from the node, because nothing was sent.
var errNoAddressee = errors.New(
	"a direct message needs somebody to send it to - press a and name them")

func (m *Model) threadCmd(thread string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		events, err := m.client.Thread(ctx, thread)
		return threadMsg{events: events, err: err}
	}
}

func (m *Model) tasksCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		tasks, err := m.client.Tasks(ctx, "")
		return tasksMsg{tasks: tasks, err: err}
	}
}

func (m *Model) delegateCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		move, err := m.client.Delegate(ctx, id, "")
		return taskMoveMsg{move: move, err: err}
	}
}

func (m *Model) taskStateCmd(id, state string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		move, err := m.client.TaskState(ctx, id, state)
		return taskMoveMsg{move: move, err: err}
	}
}

// artifactCmd reads an artifact and its history together, because the view
// shows both and two round trips that can disagree about which artifact is on
// screen is one too many.
func (m *Model) artifactCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		art, err := m.client.Artifact(ctx, id)
		if err != nil {
			return artifactMsg{err: err}
		}
		history, err := m.client.History(ctx, id)
		if err != nil {
			// The artifact is readable and its trail is not - a type with no
			// lifecycle, most often. That is a view with badges and no history,
			// not a failure to open the artifact.
			return artifactMsg{artifact: art}
		}
		return artifactMsg{artifact: art, history: history}
	}
}

func (m *Model) statusCmd(id, status string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		move, err := m.client.SetStatus(ctx, id, status)
		return statusMoveMsg{move: move, err: err}
	}
}

// memoryCmd searches when there is a query and lists when there is not, so the
// view has something in it before anybody types.
func (m *Model) memoryCmd(query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		if query == "" {
			list, err := m.client.Artifacts(ctx, "memory", "", 200)
			return memoryMsg{query: query, artifacts: list, err: err}
		}
		hits, err := m.client.Search(ctx, query, "memory", "")
		return memoryMsg{query: query, artifacts: hits, err: err}
	}
}

func (m *Model) writeMemoryCmd(art *Artifact) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		written, err := m.client.WriteArtifact(ctx, art)
		return wroteMsg{artifact: written, err: err}
	}
}

// reportsCmd lists reports when there is no query and searches them when there
// is, the same two calls the memory pane makes with the type it cares about.
//
// The type is passed on both paths and never left off. A search with no type
// answers with memories, bugs and notes alongside the reports, and a pane that
// listed those under the word "reports" would be telling the reader something
// untrue about what a report is.
func (m *Model) reportsCmd(query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		if query == "" {
			list, err := m.client.Artifacts(ctx, "report", "", 200)
			return reportsMsg{query: query, artifacts: list, err: err}
		}
		hits, err := m.client.Search(ctx, query, "report", "")
		return reportsMsg{query: query, artifacts: hits, err: err}
	}
}

// todosCmd lists the todo queue when there is no query and searches it when
// there is, the same two calls the reports pane makes.
//
// Both narrowings go out on both paths. A todo is an artifact of type memory
// with kind todo, so asking for the type alone answers with every note and
// handoff anybody has written and asking for neither answers with the whole
// corpus - either way the pane would be calling something a todo that nobody
// filed as one.
func (m *Model) todosCmd(query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		if query == "" {
			list, err := m.client.Artifacts(ctx, "memory", "todo", 200)
			return todosMsg{query: query, artifacts: list, err: err}
		}
		hits, err := m.client.Search(ctx, query, "memory", "todo")
		return todosMsg{query: query, artifacts: hits, err: err}
	}
}

// roomTodosCmd reads the todos raised in one room.
//
// It is issued off the back of the room read and of every long poll that comes
// back, rather than on a timer of its own: the poll returns when somebody says
// something and when its window runs out, which is when the plan agreed in this
// room could have moved, and a second clock would be a second opinion about how
// often the room is alive.
func (m *Model) roomTodosCmd(gen int, room string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		list, err := m.client.RoomTodos(ctx, room)
		return roomTodosMsg{gen: gen, room: room, artifacts: list, err: err}
	}
}

func (m *Model) activityCmd(query, kind string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		page, err := m.client.Activity(ctx, query, kind, 300)
		return activityMsg{page: page, err: err}
	}
}

func (m *Model) postActivityCmd(kind, body, room, thread string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		item, err := m.client.PostActivity(ctx, kind, body, room, thread)
		return postedMsg{item: item, err: err}
	}
}

func (m *Model) metricsCmd(all bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		metrics, err := m.client.Metrics(ctx, all)
		return metricsMsg{metrics: metrics, err: err}
	}
}

func (m *Model) announcementsCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		list, err := m.client.Announcements(ctx)
		return announcementsMsg{list: list, err: err}
	}
}

func (m *Model) announceCmd(scope, severity, title, body string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		announcement, err := m.client.Announce(ctx, scope, severity, title, body)
		return announcedMsg{announcement: announcement, err: err}
	}
}

func (m *Model) ackCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := callCtx()
		defer cancel()
		return ackedMsg{err: m.client.Ack(ctx, id)}
	}
}
