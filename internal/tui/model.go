package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// The root model: the tab bar, the banner, the status line, and the keys.
//
// It is one model rather than one per view because the things that are shared -
// the connection, who the token is, the announcements banner, the size of the
// terminal - are shared by all of them, and a per-view model would have meant
// six copies of each and six chances for them to disagree.

// view is which pane is on screen.
type view int

const (
	viewRooms view = iota
	viewInbox
	viewArtifact
	viewMemory
	viewTimeline
	viewMetrics
	viewAnnounce
	viewCount
)

// tabNames are what the tab bar says, in order, and the digit that selects each
// is its position: 1 is rooms, 7 is announcements.
var tabNames = [viewCount]string{
	"ROOMS", "INBOX", "ARTIFACT", "MEMORY", "TIMELINE", "METRICS", "ANNOUNCE",
}

// defaultRooms are the rooms that exist by convention. Any other is opened by
// name with o - a room is not a row in a table, it is a column on the event
// log, so there is nothing to list and everything to open.
var defaultRooms = []string{"general", "handoffs", "incidents"}

// inputKind says what the one text box on screen is for, and therefore what
// enter does with it. One box rather than one per field: at 80x24 there is room
// for a line, and a form with four boxes on it would be a form nobody can see.
type inputKind int

const (
	inputNone inputKind = iota
	inputSay
	inputOpenRoom
	inputMemQuery
	inputMemTitle
	inputMemBody
	inputTimelineQuery
	inputTimelinePost
	inputAnnTitle
	inputAnnBody
)

// draft is what a two-step compose is holding on to between the steps.
type draft struct {
	id       string
	title    string
	body     string
	kind     string
	scope    string
	severity string
	personal bool
}

// Model is the whole client.
type Model struct {
	client *Client
	theme  Theme

	width, height int

	view     view
	backView view

	who     *Whoami
	node    string
	connOK  bool
	connErr string
	flash   string
	flashOK bool

	help bool

	// announcements, and the banner they draw.
	announcements []*Announcement
	annSel        int
	annScope      string
	annSeverity   string

	// rooms
	gen        int
	rooms      []string
	roomSel    int
	msgs       []*Event
	cursor     int64
	msgSel     int
	threadOpen bool
	thread     []*Event
	watching   bool

	// inbox
	tasks   []*Task
	taskSel int

	// artifact
	artifact  *Artifact
	history   *History
	statusPin bool

	// memory
	memQuery string
	memory   []*Artifact
	memSel   int

	// timeline
	tlQuery string
	tlKind  string
	tl      []*ActivityItem
	tlSel   int

	// metrics
	metrics  *Metrics
	scopeAll bool

	// how far down a pane that is taller than the terminal has been scrolled,
	// and how far it may go. scrollMax is worked out while the pane renders,
	// because it is a property of what came back rather than of the model.
	scroll    int
	scrollMax int

	// the one input box
	input  textinput.Model
	inKind inputKind
	draft  draft
}

// New builds a model against a client. It is exported so a test can drive the
// same model the binary runs, with no server-side special case and no second
// code path: what teatest sends is what a keyboard sends.
func New(client *Client, theme Theme) *Model {
	box := textinput.New()
	box.Prompt = "> "
	box.CharLimit = 4000
	box.KeyMap = tmuxSafeKeyMap()

	return &Model{
		client:      client,
		theme:       theme,
		width:       80,
		height:      24,
		rooms:       append([]string{}, defaultRooms...),
		input:       box,
		annScope:    "project",
		annSeverity: "info",
		tlKind:      "",
	}
}

// tmuxSafeKeyMap is the text box's bindings with every control key tmux and
// screen take out of it.
//
// This is not a detail. The default bindings put "go to the start of the line"
// on ctrl+a and "one character back" on ctrl+b, which are the two prefix keys
// in use on every machine this is meant to run on. Inside tmux the client never
// sees them, so those two bindings are dead weight at best; outside tmux, on a
// terminal where the user's fingers still expect a prefix, they are a surprise.
// home/end and the arrows do the same work and collide with nothing.
func tmuxSafeKeyMap() textinput.KeyMap {
	km := textinput.DefaultKeyMap
	km.CharacterForward = key.NewBinding(key.WithKeys("right"))
	km.CharacterBackward = key.NewBinding(key.WithKeys("left"))
	km.WordForward = key.NewBinding(key.WithKeys("alt+right", "ctrl+right"))
	km.WordBackward = key.NewBinding(key.WithKeys("alt+left", "ctrl+left"))
	km.LineStart = key.NewBinding(key.WithKeys("home"))
	km.LineEnd = key.NewBinding(key.WithKeys("end"))
	km.DeleteWordBackward = key.NewBinding(key.WithKeys("alt+backspace"))
	km.DeleteWordForward = key.NewBinding(key.WithKeys("alt+delete"))
	km.DeleteBeforeCursor = key.NewBinding(key.WithKeys())
	km.DeleteAfterCursor = key.NewBinding(key.WithKeys())
	km.DeleteCharacterBackward = key.NewBinding(key.WithKeys("backspace"))
	km.DeleteCharacterForward = key.NewBinding(key.WithKeys("delete"))
	km.Paste = key.NewBinding(key.WithKeys())
	km.AcceptSuggestion = key.NewBinding(key.WithKeys())
	km.NextSuggestion = key.NewBinding(key.WithKeys())
	km.PrevSuggestion = key.NewBinding(key.WithKeys())
	return km
}

// ReservedKeys are the control sequences this client refuses to bind, whatever
// else changes. ctrl+b is tmux's prefix and ctrl+a is screen's (and plenty of
// tmux configurations'); a TUI that binds either is a TUI that cannot be driven
// from inside the multiplexer it was written for. A test asserts this list is
// absent from every binding.
var ReservedKeys = []string{"ctrl+a", "ctrl+b"}

// SetRoom picks the room the client opens on, before anything is loaded. It is
// the one piece of starting state worth setting from outside: a person who
// lives in #incidents should not have to press o every morning.
func (m *Model) SetRoom(room string) {
	room = strings.TrimSpace(room)
	if room == "" || strings.Contains(room, "/") {
		return
	}
	for i, name := range m.rooms {
		if name == room {
			m.roomSel = i
			return
		}
	}
	m.rooms = append(m.rooms, room)
	sort.Strings(m.rooms)
	for i, name := range m.rooms {
		if name == room {
			m.roomSel = i
		}
	}
}

// Room is the room currently being read.
func (m *Model) Room() string {
	if len(m.rooms) == 0 {
		return ""
	}
	if m.roomSel < 0 || m.roomSel >= len(m.rooms) {
		return m.rooms[0]
	}
	return m.rooms[m.roomSel]
}

// Init loads what every view needs before anything is drawn: who the token is,
// and what is being announced.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.whoamiCmd(),
		m.announcementsCmd(),
		m.roomCmd(m.gen, m.Room(), 0),
		tick(),
	)
}

// ------------------------------------------------------------------- update

// Update is the loop. Nothing in it talks to the network - see cmds.go.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A resize reflows and nothing more: every view computes its own
		// geometry from these two numbers at render time, so there is no cached
		// layout to go stale and nothing to recompute here. A width of zero
		// arrives from some terminals during a tmux pane split, and clamping is
		// what keeps the renderers from being handed a negative column count.
		m.width, m.height = msg.Width, msg.Height
		if m.width < 20 {
			m.width = 20
		}
		if m.height < 6 {
			m.height = 6
		}
		m.input.Width = m.width - 6
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)

	case tickMsg:
		return m, tea.Batch(m.announcementsCmd(), m.refreshCmd(), tick())
	}
	return m, m.onData(msg)
}

// onData folds a reply from the node into the model.
func (m *Model) onData(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case whoamiMsg:
		if m.note(msg.err) {
			return nil
		}
		m.who = msg.who
		return nil

	case roomMsg:
		if msg.gen != m.gen {
			return nil
		}
		if m.note(msg.err) {
			return nil
		}
		m.msgs = msg.page.Events
		m.cursor = msg.page.Cursor
		m.msgSel = len(m.msgs) - 1
		m.rememberRooms(m.msgs)
		if m.watching {
			return nil
		}
		m.watching = true
		return m.waitCmd(m.gen, m.Room(), m.cursor)

	case waitMsg:
		if msg.gen != m.gen {
			// A poll for a room that is no longer on screen. Dropping it is
			// what keeps a room switch from merging one room into another.
			return nil
		}
		if msg.err != nil {
			// The watcher is the one call allowed to fail quietly and keep
			// going: a node that went away comes back, and a TUI that stopped
			// polling the first time a laptop slept would need restarting.
			m.note(msg.err)
			return m.waitCmd(m.gen, m.Room(), m.cursor)
		}
		m.connOK, m.connErr = true, ""
		atEnd := m.msgSel >= len(m.msgs)-1
		if len(msg.page.Events) > 0 {
			m.msgs = append(m.msgs, msg.page.Events...)
			m.rememberRooms(msg.page.Events)
			if atEnd {
				m.msgSel = len(m.msgs) - 1
			}
		}
		m.cursor = msg.page.Cursor
		return m.waitCmd(m.gen, m.Room(), m.cursor)

	case sentMsg:
		if m.note(msg.err) {
			return nil
		}
		m.say("posted to " + m.Room())
		// The watcher brings the message back like anybody else's, so there is
		// nothing to append here: one path into the list, and no chance of a
		// message appearing twice because it was both echoed and polled.
		return nil

	case threadMsg:
		if m.note(msg.err) {
			return nil
		}
		m.thread = msg.events
		return nil

	case tasksMsg:
		if m.note(msg.err) {
			return nil
		}
		m.tasks = msg.tasks
		if m.taskSel >= len(m.tasks) {
			m.taskSel = max(0, len(m.tasks)-1)
		}
		return nil

	case taskMoveMsg:
		if m.note(msg.err) {
			return nil
		}
		m.say("task " + shortID(msg.move.Task.ID) + " is " + msg.move.Task.State)
		return m.tasksCmd()

	case artifactMsg:
		if m.note(msg.err) {
			return nil
		}
		m.artifact, m.history = msg.artifact, msg.history
		m.statusPin = false
		m.scroll = 0
		return nil

	case statusMoveMsg:
		if m.note(msg.err) {
			return nil
		}
		m.say(msg.move.Artifact.ID + " -> " + msg.move.Artifact.Status)
		return m.artifactCmd(msg.move.Artifact.ID)

	case memoryMsg:
		if m.note(msg.err) {
			return nil
		}
		m.memQuery = msg.query
		m.memory = msg.artifacts
		if m.memSel >= len(m.memory) {
			m.memSel = max(0, len(m.memory)-1)
		}
		return nil

	case wroteMsg:
		if m.note(msg.err) {
			return nil
		}
		m.say("wrote " + msg.artifact.ID + " (" + msg.artifact.Visibility + ")")
		return m.memoryCmd(m.memQuery)

	case activityMsg:
		if m.note(msg.err) {
			return nil
		}
		m.tl = msg.page.Items
		m.tlQuery = msg.page.Query
		if m.tlSel >= len(m.tl) {
			m.tlSel = max(0, len(m.tl)-1)
		}
		return nil

	case postedMsg:
		if m.note(msg.err) {
			return nil
		}
		m.say("posted into the timeline")
		return m.activityCmd(m.tlQuery, m.tlKind)

	case metricsMsg:
		if m.note(msg.err) {
			return nil
		}
		m.metrics = msg.metrics
		m.node = msg.metrics.Node
		return nil

	case announcementsMsg:
		if msg.err != nil {
			m.note(msg.err)
			return nil
		}
		m.connOK, m.connErr = true, ""
		m.announcements = msg.list
		if m.annSel >= len(m.announcements) {
			m.annSel = max(0, len(m.announcements)-1)
		}
		return nil

	case announcedMsg:
		if m.note(msg.err) {
			return nil
		}
		m.say("announced: " + msg.announcement.Title)
		return m.announcementsCmd()

	case ackedMsg:
		if m.note(msg.err) {
			return nil
		}
		m.say("acknowledged")
		return m.announcementsCmd()
	}
	return nil
}

// note records an error for the status line and reports whether there was one.
// Nothing here ever panics on a nil reply: a failed call leaves the view as it
// was and says why at the bottom of the screen.
func (m *Model) note(err error) bool {
	if err == nil {
		m.connOK, m.connErr = true, ""
		return false
	}
	m.connOK = false
	switch {
	case Unauthorized(err):
		m.connErr = "token refused: " + err.Error()
	default:
		m.connErr = err.Error()
	}
	return true
}

// say puts a line under the view until the next thing happens.
func (m *Model) say(text string) {
	m.flash, m.flashOK = text, true
}

// refreshCmd reloads whatever is on screen.
func (m *Model) refreshCmd() tea.Cmd {
	switch m.view {
	case viewRooms:
		m.gen++
		m.watching = false
		return m.roomCmd(m.gen, m.Room(), 0)
	case viewInbox:
		return m.tasksCmd()
	case viewArtifact:
		if m.artifact != nil {
			return m.artifactCmd(m.artifact.ID)
		}
		return nil
	case viewMemory:
		return m.memoryCmd(m.memQuery)
	case viewTimeline:
		return m.activityCmd(m.tlQuery, m.tlKind)
	case viewMetrics:
		return m.metricsCmd(m.scopeAll)
	case viewAnnounce:
		return m.announcementsCmd()
	}
	return nil
}

// enter is what a view loads the first time it is opened.
func (m *Model) enter(v view) tea.Cmd {
	if v != m.view {
		m.scroll = 0
	}
	m.view = v
	switch v {
	case viewInbox:
		if m.tasks == nil {
			return m.tasksCmd()
		}
	case viewMemory:
		if m.memory == nil {
			return m.memoryCmd("")
		}
	case viewTimeline:
		if m.tl == nil {
			return m.activityCmd(m.tlQuery, m.tlKind)
		}
	case viewMetrics:
		if m.metrics == nil {
			return m.metricsCmd(m.scopeAll)
		}
	case viewAnnounce:
		if m.announcements == nil {
			return m.announcementsCmd()
		}
	}
	return nil
}

// rememberRooms adds any room seen in the log to the list, so a room somebody
// else opened is one keystroke away rather than something to type out.
func (m *Model) rememberRooms(events []*Event) {
	known := map[string]bool{}
	for _, room := range m.rooms {
		known[room] = true
	}
	added := false
	for _, e := range events {
		if e.Room != "" && !known[e.Room] {
			known[e.Room] = true
			m.rooms = append(m.rooms, e.Room)
			added = true
		}
	}
	if added {
		room := m.Room()
		sort.Strings(m.rooms)
		for i, name := range m.rooms {
			if name == room {
				m.roomSel = i
			}
		}
	}
}

// ---------------------------------------------------------------------- keys

// onKey is every keystroke. Two modes: a text box has the keyboard, or the view
// does. Nothing here is bound to a control key a multiplexer takes - see
// ReservedKeys.
func (m *Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.inKind != inputNone {
		return m.onInputKey(msg)
	}

	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "?":
		m.help = !m.help
		return m, nil
	case "esc":
		if m.help {
			m.help = false
			return m, nil
		}
		if m.statusPin {
			m.statusPin = false
			return m, nil
		}
		if m.view == viewArtifact {
			return m, m.enter(m.backView)
		}
		if m.threadOpen {
			m.threadOpen = false
			return m, nil
		}
		return m, nil
	case "tab":
		return m, m.enter((m.view + 1) % viewCount)
	case "shift+tab":
		return m, m.enter((m.view + viewCount - 1) % viewCount)
	case "1", "2", "3", "4", "5", "6", "7":
		if m.statusPin {
			break // the digits are the status choices while the picker is up
		}
		return m, m.enter(view(msg.String()[0] - '1'))
	case "r":
		m.say("refreshing")
		return m, m.refreshCmd()
	}

	switch m.view {
	case viewRooms:
		return m.roomsKey(msg)
	case viewInbox:
		return m.inboxKey(msg)
	case viewArtifact:
		return m.artifactKey(msg)
	case viewMemory:
		return m.memoryKey(msg)
	case viewTimeline:
		return m.timelineKey(msg)
	case viewMetrics:
		return m.metricsKey(msg)
	case viewAnnounce:
		return m.announceKey(msg)
	}
	return m, nil
}

// onInputKey is the keyboard while a box is open. esc always leaves it, which
// is the one rule a text box in a full-screen client has to keep: there is no
// mouse to click somewhere else with.
func (m *Model) onInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeInput()
		return m, nil
	case "enter":
		return m.submitInput()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// openInput puts the box on screen for a purpose, with a prompt and whatever it
// should start out holding.
func (m *Model) openInput(kind inputKind, prompt, value string) tea.Cmd {
	m.inKind = kind
	m.input.Prompt = prompt
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.input.Width = max(4, m.width-len([]rune(prompt))-2)
	return m.input.Focus()
}

func (m *Model) closeInput() {
	m.inKind = inputNone
	m.input.Blur()
	m.input.SetValue("")
}

// submitInput is what enter does, per box.
func (m *Model) submitInput() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	kind := m.inKind

	switch kind {
	case inputSay:
		if value == "" {
			m.closeInput()
			return m, nil
		}
		thread := ""
		if m.threadOpen {
			if selected := m.selectedMessage(); selected != nil {
				thread = selected.Thread
			}
		}
		m.closeInput()
		return m, m.sayCmd(m.Room(), value, thread, nil)

	case inputOpenRoom:
		m.closeInput()
		if value == "" || strings.Contains(value, "/") {
			m.flash, m.flashOK = "a room is one path segment", false
			return m, nil
		}
		return m, m.openRoom(value)

	case inputMemQuery:
		m.closeInput()
		return m, m.memoryCmd(value)

	case inputMemTitle:
		if value == "" {
			m.closeInput()
			return m, nil
		}
		m.draft.title = value
		return m, m.openInput(inputMemBody, "body> ", m.draft.body)

	case inputMemBody:
		m.closeInput()
		art := &Artifact{
			ID: m.draft.id, Type: "memory", Kind: m.draft.kind,
			Title: m.draft.title, Body: value,
		}
		// Personal unless the artifact being edited already was not. Memory is
		// personal by default in this node and this client does not promote it
		// for you: making something project-visible is a thing you do on
		// purpose, in the console or over the API, not a side effect of
		// pressing e in a terminal.
		if m.draft.personal {
			art.Visibility = "personal"
		}
		return m, m.writeMemoryCmd(art)

	case inputTimelineQuery:
		m.closeInput()
		return m, m.activityCmd(value, m.tlKind)

	case inputTimelinePost:
		m.closeInput()
		if value == "" {
			return m, nil
		}
		room, thread := m.Room(), ""
		if item := m.selectedActivity(); item != nil && item.Thread != "" {
			room, thread = item.Room, item.Thread
		}
		if room == "" && thread == "" {
			room = "general"
		}
		return m, m.postActivityCmd("chat", value, room, thread)

	case inputAnnTitle:
		if value == "" {
			m.closeInput()
			return m, nil
		}
		m.draft.title = value
		return m, m.openInput(inputAnnBody, "body> ", "")

	case inputAnnBody:
		m.closeInput()
		return m, m.announceCmd(m.annScope, m.annSeverity, m.draft.title, value)
	}

	m.closeInput()
	return m, nil
}

// openRoom switches the room being watched. The generation counter goes up, so
// the long poll still out for the old room is dropped when it lands.
func (m *Model) openRoom(room string) tea.Cmd {
	found := -1
	for i, name := range m.rooms {
		if name == room {
			found = i
		}
	}
	if found < 0 {
		m.rooms = append(m.rooms, room)
		sort.Strings(m.rooms)
		for i, name := range m.rooms {
			if name == room {
				found = i
			}
		}
	}
	m.roomSel = found
	m.gen++
	m.watching = false
	m.msgs, m.cursor, m.msgSel = nil, 0, 0
	m.thread, m.threadOpen = nil, false
	m.view = viewRooms
	return m.roomCmd(m.gen, room, 0)
}

// ---------------------------------------------------------------- rendering

// View draws the screen: a banner if anything is being announced, the tab bar,
// the pane, the status line, and either the text box or the help footer.
//
// Everything is clipped to the width and the pane is clipped to the height, so
// an 80x24 terminal renders the same layout a 200x60 one does with less in it,
// and nothing ever wraps into the line below and pushes the status line off the
// bottom.
func (m *Model) View() string {
	if m.help {
		return m.helpView()
	}

	var head []string
	head = append(head, m.bannerLines()...)
	head = append(head, m.tabBarLine())

	foot := []string{m.statusLine(), m.footerLine()}

	body := max(1, m.height-len(head)-len(foot))
	lines := m.pane(body)
	for len(lines) < body {
		lines = append(lines, "")
	}
	if len(lines) > body {
		lines = lines[:body]
	}

	out := append(head, lines...)
	out = append(out, foot...)
	return strings.Join(out, "\n")
}

// pane draws the current view into height lines.
func (m *Model) pane(height int) []string {
	switch m.view {
	case viewRooms:
		return m.roomsView(height)
	case viewInbox:
		return m.inboxView(height)
	case viewArtifact:
		return m.artifactView(height)
	case viewMemory:
		return m.memoryView(height)
	case viewTimeline:
		return m.timelineView(height)
	case viewMetrics:
		return m.metricsView(height)
	case viewAnnounce:
		return m.announceView(height)
	}
	return nil
}

// bannerLines is the announcements banner: one line per active announcement,
// coloured by severity, at the top of every view. Two at most, because a banner
// that can grow without limit is a banner that can take the screen.
func (m *Model) bannerLines() []string {
	if len(m.announcements) == 0 {
		return nil
	}
	var lines []string
	for i, a := range m.announcements {
		if i == 2 {
			lines = append(lines, m.theme.Dim.Render(m.theme.clip(
				fmt.Sprintf("  ... and %d more - press 7 for announcements",
					len(m.announcements)-2), m.width)))
			break
		}
		scope := a.Decoded.Scope
		if scope == "" {
			scope = "project"
		}
		text := fmt.Sprintf(" %s [%s] %s", strings.ToUpper(a.Severity), scope, a.Title)
		if a.Decoded.Mode != "" {
			text += " (" + a.Decoded.Mode + ")"
		}
		lines = append(lines, m.theme.severityStyle(a.Severity).
			Render(pad(m.theme.clip(text, m.width), m.width)))
	}
	return lines
}

// tabBarLine is the row of views, with the active one highlighted and its digit
// beside it. It is one line and it is clipped, so a narrow pane loses the tabs
// on the right rather than wrapping them onto the row the pane needs.
func (m *Model) tabBarLine() string {
	var parts []string
	used := 0
	for i, name := range tabNames {
		label := fmt.Sprintf(" %d %s ", i+1, name)
		// The budget is counted on the plain label, never on the styled one: a
		// styled string is mostly escape bytes, and clipping one by length is
		// how a tab bar ends up emitting half an escape sequence.
		if used+len([]rune(label)) > m.width {
			break
		}
		used += len([]rune(label))
		if view(i) == m.view {
			parts = append(parts, m.theme.TabActive.Render(label))
		} else {
			parts = append(parts, m.theme.Tab.Render(label))
		}
	}
	return strings.Join(parts, "")
}

// statusLine is who this is, where it is pointed, and whether it is talking to
// the node. A lost connection shows here and nowhere else: no dialog, no
// modal, nothing that has to be dismissed before the client works again.
func (m *Model) statusLine() string {
	who := "no token"
	if m.who != nil {
		// The tail of the ulid, not the whole of it: two of them and the node's
		// URL do not fit on one line, and what a person needs from an id here
		// is enough to tell it from the other one on screen.
		who = shortID(m.who.User)
		if m.who.Agent != "" {
			who += "/" + shortID(m.who.Agent)
		}
		if m.who.Project != "" {
			who += " @" + m.who.Project
		}
		if m.who.Operator {
			who += " (operator)"
		}
	}
	conn := "connected"
	if !m.connOK {
		conn = "offline"
	}
	if m.connErr != "" {
		conn = m.theme.clip(m.connErr, max(10, m.width/2))
	}
	left := fmt.Sprintf(" %s | %s", who, m.client.Base())
	right := conn + " "
	gap := m.width - len([]rune(left)) - len([]rune(right))
	if gap < 1 {
		return m.theme.Status.Render(pad(m.theme.clip(left+" "+right, m.width), m.width))
	}
	line := left + strings.Repeat(" ", gap) + right
	if m.connErr != "" {
		return m.theme.Err.Render(m.theme.clip(line, m.width))
	}
	return m.theme.Status.Render(m.theme.clip(line, m.width))
}

// footerLine is the text box when one is open, the flash when something just
// happened, and the keys for this view otherwise.
func (m *Model) footerLine() string {
	if m.inKind != inputNone {
		// Rendered as it is: the box is already sized to the terminal, and
		// clipping a styled string by rune count is how a cursor's escape
		// sequence gets cut in half.
		return m.input.View()
	}
	if m.flash != "" {
		style := m.theme.OK
		if !m.flashOK {
			style = m.theme.Err
		}
		return style.Render(m.theme.clip(m.flash, m.width))
	}
	return m.theme.Help.Render(m.theme.clip(m.viewHelp(), m.width))
}

// viewHelp is the one-line key hint for whatever is on screen.
func (m *Model) viewHelp() string {
	common := "tab/1-7 view  ?help  q quit"
	switch m.view {
	case viewRooms:
		return "j/k move  i post  t thread  o open room  n/p room  " + common
	case viewInbox:
		return "j/k move  enter artifact  d delegate  x done  " + common
	case viewArtifact:
		return "s status  esc back  " + common
	case viewMemory:
		return "j/k move  / search  enter open  i new  e edit  " + common
	case viewTimeline:
		return "j/k move  / search  i post  enter artifact  " + common
	case viewMetrics:
		return "a scope all  r refresh  " + common
	case viewAnnounce:
		return "j/k move  a ack  v severity  c scope  i post  " + common
	}
	return common
}

// helpView is the full key list, on a screen of its own. It is a screen rather
// than an overlay because an overlay over a pane on an 80x24 terminal covers
// the thing the reader was looking at.
func (m *Model) helpView() string {
	lines := []string{
		m.theme.Title.Render("flowy tui - keys"),
		"",
		"  everywhere",
		"    tab / shift-tab   next / previous view",
		"    1 2 3 4 5 6 7     rooms, inbox, artifact, memory, timeline, metrics, announce",
		"    j / k, up / down  move        g / G   first / last",
		"    enter             open        r       refresh",
		"    /                 search      i       insert (post / compose)",
		"    esc               leave a box, close this, go back",
		"    ?                 this        q / ctrl-c   quit",
		"",
		"  rooms       o open a room by name, n / p next / previous room,",
		"              t thread pane, i the message box",
		"  inbox       d delegate to your agent, x mark done, enter the artifact",
		"  artifact    s move the lifecycle status, then a digit to pick",
		"  memory      / search, i new, e edit the selected one",
		"  timeline    / filter, i post into the selected run or the current room",
		"  metrics     a ask for the node's own scope (operator only)",
		"  announce    a acknowledge, v severity, c scope, i post one",
		"",
		m.theme.Dim.Render("  ctrl-a and ctrl-b are never bound: they belong to tmux and screen."),
		m.theme.Dim.Render("  no mouse is needed, and none is captured, so selection still works."),
	}
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	if len(lines) > m.height-1 {
		lines = lines[:m.height-1]
	}
	lines = append(lines, m.theme.Help.Render(m.theme.clip("esc or ? to go back", m.width)))
	for i, line := range lines {
		lines[i] = m.theme.clip(line, m.width)
	}
	return strings.Join(lines, "\n")
}

// ------------------------------------------------------------------- helpers

// scrolled cuts a pane that is taller than the terminal down to what fits,
// starting from wherever it has been scrolled to, and records how far it may be
// scrolled so the keys have something to clamp against.
//
// It is worked out here, during the render, because it depends on how many
// lines the node's answer turned into - which nothing outside the pane knows.
func (m *Model) scrolled(lines []string, height int) []string {
	m.scrollMax = max(0, len(lines)-height)
	if m.scroll > m.scrollMax {
		m.scroll = m.scrollMax
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	lines = lines[m.scroll:]
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// window is the slice of a list that fits, kept around the selected row.
func window(length, selected, height int) (start, end int) {
	if height <= 0 || length == 0 {
		return 0, 0
	}
	if length <= height {
		return 0, length
	}
	start = selected - height/2
	if start < 0 {
		start = 0
	}
	if start+height > length {
		start = length - height
	}
	return start, start + height
}

// moveSel applies j/k/g/G/pgup/pgdn to a selection, and says whether it did.
func moveSel(key string, sel, length, page int) (int, bool) {
	if length == 0 {
		return sel, false
	}
	switch key {
	case "j", "down":
		sel++
	case "k", "up":
		sel--
	case "g", "home":
		sel = 0
	case "G", "end":
		sel = length - 1
	case "pgdown":
		sel += max(1, page)
	case "pgup":
		sel -= max(1, page)
	default:
		return sel, false
	}
	if sel < 0 {
		sel = 0
	}
	if sel >= length {
		sel = length - 1
	}
	return sel, true
}

// shortID is an id as it goes in a column: the tail, which is the part that
// differs between two ulids minted a second apart.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}
