package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
)

// monoTheme is the theme every test renders with: no colour and no unicode, so
// what the assertions look at is the text and not the escape sequences around
// it. It is also the theme a TERM=dumb terminal gets, which makes these tests
// the check that the client is usable on one.
func monoTheme() Theme { return NewTheme(termenv.Ascii, false) }

func testModel(t *testing.T, client *Client) *Model {
	t.Helper()
	m := New(client, monoTheme())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

// ------------------------------------------------------- the multiplexer rule

// The one binding rule this client has: ctrl-a and ctrl-b belong to screen and
// tmux, and a client that takes either cannot be driven from inside the
// multiplexer it was written for.
//
// The text box is where this goes wrong by default - upstream binds ctrl+a to
// "start of line" and ctrl+b to "one character back" - so the box's key map is
// walked field by field rather than spot-checked.
func TestNoReservedKeyIsBound(t *testing.T) {
	km := tmuxSafeKeyMap()
	value := reflect.ValueOf(km)
	for i := 0; i < value.NumField(); i++ {
		binding, ok := value.Field(i).Interface().(key.Binding)
		if !ok {
			continue
		}
		for _, bound := range binding.Keys() {
			for _, reserved := range ReservedKeys {
				if bound == reserved {
					t.Fatalf("%s is bound to %s, which belongs to tmux/screen",
						reserved, value.Type().Field(i).Name)
				}
			}
		}
	}
}

// And in the view itself: a reserved key does nothing at all, rather than
// quitting or switching a view behind the multiplexer's back.
func TestAReservedKeyDoesNothing(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	for _, reserved := range ReservedKeys {
		before := m.view
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
		if reserved == "ctrl+b" {
			_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
		}
		if cmd != nil {
			t.Fatalf("%s produced a command; it should do nothing", reserved)
		}
		if m.view != before {
			t.Fatalf("%s changed the view", reserved)
		}
	}
}

// ----------------------------------------------------------------- the layout

// seed puts something in every view, so the layout checks are run against panes
// with content in them rather than against six empty boxes.
func seed(m *Model) {
	project := "pa"
	now := time.Now()
	m.who = &Whoami{User: "01HUSERAAAAAAAAAAAAAAAAAAA", Agent: "01HAGENTAAAAAAAAAAAAAAAAAA", Project: "pa"}
	m.msgs = []*Event{
		{ID: "01HEVENT1", Room: "general", Thread: "01HTHREAD1", Actor: "01HUSERAAAAAAAAAAAAAAAAAAA",
			Body: "a message long enough to need clipping on a narrow pane, several times over", Created: now},
		{ID: "01HEVENT2", Room: "general", Thread: "01HTHREAD1", Parents: []string{"01HEVENT1"},
			Actor: "01HAGENTAAAAAAAAAAAAAAAAAA", Body: "an answer", Created: now},
	}
	m.msgs[1].Meta.ActorKind = "agent"
	m.msgs[1].Meta.ActorUser = "01HUSERAAAAAAAAAAAAAAAAAAA"
	m.msgSel = 1
	m.thread = m.msgs
	m.threadOpen = true
	m.tasks = []*Task{{
		ID: "01HTASKAAAAAAAAAAAAAAAAAAA", Artifact: "01HARTAAAAAAAAAAAAAAAAAAAA",
		FromUser: "01HUSERBBBBBBBBBBBBBBBBBBB", ToUser: "01HUSERAAAAAAAAAAAAAAAAAAA",
		State: "open", Thread: "01HTHREAD2", ArtifactTitle: "the seeded handoff",
	}}
	m.artifact = &Artifact{
		ID: "01HARTAAAAAAAAAAAAAAAAAAAA", Type: "bug", Project: &project, Status: "open",
		Title: "a bug", Body: "line one\nline two", OwnerUser: "01HUSERAAAAAAAAAAAAAAAAAAA",
		UserTags: []string{"tag"}, Updated: now,
	}
	m.history = &History{Artifact: m.artifact.ID, Status: "open", Next: []string{"triaged", "wontfix"},
		Events: []*Event{{Actor: "01HUSERAAAAAAAAAAAAAAAAAAA", Body: "opened", Created: now}}}
	m.memory = []*Artifact{{
		ID: "01HMEMAAAAAAAAAAAAAAAAAAAA", Type: "memory", Title: "how the gate is run",
		Body: "run-tests.sh, from clean", Visibility: "personal", Status: "active", Updated: now,
	}}
	m.reports = []*Artifact{{
		ID: "01HREPAAAAAAAAAAAAAAAAAAAA", Type: "report", Project: &project,
		Title: "the harness architecture", Body: "# the spine\nan event fabric",
		Visibility: "project", UserTags: []string{"harness-research"},
		Fields:  json.RawMessage(`{"as_of":"f343027","supersedes":"01HREPBBBBBBBBBBBBBBBBBBBB"}`),
		Updated: now,
	}}
	// The queue, in the shape it is really written in: type memory, kind todo,
	// an OWNER line the row is drawn from and a DEPENDS ON line under the rule.
	// It is seeded out of order on purpose, so a layout check is looking at the
	// same ordering a reader is.
	m.todos = sortTodos([]*Artifact{
		{
			ID: "01HTODODONEAAAAAAAAAAAAAAA", Type: "memory", Kind: "todo", Project: &project,
			Title: "the names on every chat surface", Body: "OWNER: flowy-claude\nDEPENDS ON: nothing",
			Visibility: "project", Status: "done", Updated: now,
		},
		{
			ID: "01HTODOTODOAAAAAAAAAAAAAAA", Type: "memory", Kind: "todo", Project: &project,
			Title:      "the console's own todos panel",
			Body:       "OWNER: orchestrator\nDEPENDS ON: the tui todos view landing first",
			Visibility: "project", Status: "todo", Updated: now,
		},
		{
			// The queue has two of these: filed by whoever noticed the work, with
			// nobody named on them yet.
			ID: "01HTODOUNOWNEDAAAAAAAAAAAA", Type: "memory", Kind: "todo", Project: &project,
			Title: "decide what closing a todo means", Body: "DEPENDS ON: nothing - it is a decision",
			Visibility: "project", Status: "todo", Updated: now,
		},
		{
			ID: "01HTODOACTIVEAAAAAAAAAAAAA", Type: "memory", Kind: "todo", Project: &project,
			Title:      "a todos view in the terminal client",
			Body:       "OWNER: todo-view\nDEPENDS ON: the kind filter on /api/artifacts",
			Visibility: "project", Status: "active", Updated: now,
		},
	})
	m.tl = []*ActivityItem{{
		ID: "01HACTAAAAAAAAAAAAAAAAAAAA", Kind: "chat", Actor: "01HUSERAAAAAAAAAAAAAAAAAAA",
		Room: "general", Thread: "01HTHREAD1", Body: "something happened",
		Created: "2026-08-15T10:11:12Z",
	}}
	m.metrics = &Metrics{Node: "gate", Version: "0.8.0", Scope: MetricScope{Key: "self"}}
	m.metrics.Groups.Node = &NodeGroup{Availability: Availability{Available: true}, UptimeS: 90}
	m.metrics.Groups.Corpus = &CorpusGroup{
		Availability: Availability{Available: true}, Artifacts: 12, Events: 40,
		ByType: map[string]int{"memory": 6, "bug": 3},
	}
	m.metrics.Groups.Collab = &CollabGroup{
		Availability: Availability{Available: true}, Messages24h: 7,
		MessagesByDay: []DayCount{{Day: "2026-08-14", Count: 3}, {Day: "2026-08-15", Count: 7}},
	}
	m.metrics.Groups.Anomalies = &AnomaliesGroup{
		Availability: Availability{Available: true}, MinSamples: 7, Insufficient: 1,
		Series: []Anomaly{{Series: "messages", Verdict: "insufficient samples", Samples: 2, Required: 7}},
	}
	m.announcements = []*Announcement{{
		Artifact: Artifact{ID: "01HANNAAAAAAAAAAAAAAAAAAAA", Type: "announcement",
			Title: "the store is going down at six", Severity: "maintenance", Body: "drain first"},
		Decoded: AnnouncementFields{Scope: "federation", Resource: "store", Mode: "drain"},
	}}
}

// Every view, at every size a terminal in a tmux pane actually is, renders
// inside the box it was given: no line wider than the terminal, no more lines
// than it is tall. A line that is one too wide wraps, and a wrapped line pushes
// the status bar off the bottom of the screen for good.
func TestEveryViewFitsTheTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {132, 43}, {40, 12}, {20, 6}, {200, 60}}
	for _, size := range sizes {
		for v := view(0); v < viewCount; v++ {
			m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
			seed(m)
			m.view = v
			m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})

			lines := strings.Split(m.View(), "\n")
			if len(lines) > max(6, size.h) {
				t.Fatalf("%s at %dx%d: %d lines, want at most %d",
					tabNames[v], size.w, size.h, len(lines), size.h)
			}
			for i, line := range lines {
				if width := lipgloss.Width(line); width > max(20, size.w) {
					t.Fatalf("%s at %dx%d: line %d is %d wide:\n%q",
						tabNames[v], size.w, size.h, i, width, line)
				}
			}
		}
	}
}

// The help screen is a screen, and it obeys the same rule.
func TestHelpFitsTheTerminal(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.help = true
	for _, size := range []struct{ w, h int }{{80, 24}, {40, 10}} {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		lines := strings.Split(m.View(), "\n")
		if len(lines) > max(6, size.h) {
			t.Fatalf("help at %dx%d: %d lines", size.w, size.h, len(lines))
		}
		for _, line := range lines {
			if width := lipgloss.Width(line); width > max(20, size.w) {
				t.Fatalf("help at %dx%d: a line is %d wide: %q", size.w, size.h, width, line)
			}
		}
	}
}

// A resize is a reflow and never a crash - including the degenerate sizes a
// terminal reports while a tmux pane is being split, which are zero and one.
func TestAResizeNeverCrashes(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	seed(m)
	for _, size := range []struct{ w, h int }{
		{0, 0}, {1, 1}, {5, 2}, {80, 24}, {300, 100}, {17, 3}, {80, 1}, {80, 24},
	} {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		for v := view(0); v < viewCount; v++ {
			m.view = v
			if m.View() == "" && size.h > 0 {
				t.Fatalf("%s rendered nothing at %dx%d", tabNames[v], size.w, size.h)
			}
		}
	}
}

// ------------------------------------------------------- talking to the node

// stub is a node that answers the handful of endpoints a view reads, and
// records what was written to it.
type stub struct {
	*httptest.Server
	status int
	// fixture is what the stub node says about the project the token writes
	// into, so a test can drive the indicator without a database.
	fixture bool
	written []map[string]any
	asked   []string
}

func newStub(t *testing.T) *stub {
	t.Helper()
	s := &stub{status: 0}
	mux := http.NewServeMux()
	deny := func(w http.ResponseWriter) bool {
		if s.status != 0 {
			w.WriteHeader(s.status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no such token"})
			return true
		}
		return false
	}
	mux.HandleFunc("GET /api/whoami", func(w http.ResponseWriter, _ *http.Request) {
		if deny(w) {
			return
		}
		_ = json.NewEncoder(w).Encode(Whoami{
			User: "u1", Agent: "a1", Project: "pa",
			ProjectDeclared: true, ProjectFixture: s.fixture,
		})
	})
	mux.HandleFunc("GET /api/announcements", func(w http.ResponseWriter, _ *http.Request) {
		if deny(w) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"announcements": []any{}})
	})
	// The two reads a list pane makes, answering with one report so what a pane
	// asked for can be checked against what it was given.
	answerWithAReport := func(w http.ResponseWriter, r *http.Request) {
		if deny(w) {
			return
		}
		s.asked = append(s.asked, r.URL.Path+"?"+r.URL.RawQuery)
		_ = json.NewEncoder(w).Encode(map[string]any{"artifacts": []Artifact{{
			ID: "01HREPSTUBAAAAAAAAAAAAAAAA", Type: "report", Title: "a stubbed report",
			Body: "a body", Visibility: "project",
			Fields: json.RawMessage(`{"as_of":"deadbee"}`),
		}}})
	}
	mux.HandleFunc("GET /api/artifacts", answerWithAReport)
	mux.HandleFunc("GET /api/search", answerWithAReport)
	mux.HandleFunc("POST /api/artifacts", func(w http.ResponseWriter, r *http.Request) {
		if deny(w) {
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.written = append(s.written, body)
		_ = json.NewEncoder(w).Encode(Artifact{
			ID: "01HNEWAAAAAAAAAAAAAAAAAAAA", Type: "memory", Visibility: "personal",
		})
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// A token the node refuses is a line on the status bar saying so, and a client
// that carries on running. It is the failure people actually hit - a stale
// token in the environment - and a panic on it would take the terminal down
// with the process.
func TestABadTokenIsSaidAndNotPanicked(t *testing.T) {
	s := newStub(t)
	s.status = http.StatusUnauthorized

	m := testModel(t, NewClient(s.URL, "not-a-token"))
	msg := m.whoamiCmd()()
	m.Update(msg)

	if m.connOK {
		t.Fatal("the model thinks it is connected after a 401")
	}
	if !strings.Contains(m.statusLine(), "token refused") {
		t.Fatalf("the status line does not say the token was refused: %q", m.statusLine())
	}
	if !strings.Contains(m.View(), "token refused") {
		t.Fatal("the refusal is not on screen")
	}
}

// The current-project indicator. The status line is the one surface a person
// looking at this client sees on every screen, and the project a token writes
// into was on it as a word with nothing to distinguish a fixture from real
// work - which is how a day of real memory went into the smoke seeder's demo
// project without anybody noticing. A fixture takes the whole line now, the way
// a connection error does.
func TestTheStatusLineSaysWhenTheProjectIsAFixture(t *testing.T) {
	s := newStub(t)
	s.fixture = true

	m := testModel(t, NewClient(s.URL, "t"))
	m.Update(m.whoamiCmd()())

	line := m.statusLine()
	if !strings.Contains(line, "@pa") {
		t.Fatalf("the status line does not say which project this token writes into: %q", line)
	}
	if !strings.Contains(line, "FIXTURE") {
		t.Fatalf("the status line does not say the project is a fixture: %q", line)
	}

	// And it says nothing of the sort about an ordinary project, or the marker
	// would be noise on every screen of every real node.
	s.fixture = false
	plain := testModel(t, NewClient(s.URL, "t"))
	plain.Update(plain.whoamiCmd()())
	if strings.Contains(plain.statusLine(), "FIXTURE") {
		t.Fatalf("an ordinary project is being called a fixture: %q", plain.statusLine())
	}
	if !strings.Contains(plain.statusLine(), "@pa") {
		t.Fatalf("the project is missing from the status line: %q", plain.statusLine())
	}
}

// A node that is not there is the same: a status, not a stack trace.
func TestAnUnreachableNodeIsAStatus(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.Update(m.whoamiCmd()())
	if m.connOK {
		t.Fatal("connected to a port nothing is listening on")
	}
	if m.connErr == "" {
		t.Fatal("nothing was said about the connection")
	}
	_ = m.View()
}

// Memory is personal by default in this node, and this client does not promote
// it. A new memory is written personal; an edit of one that is not carries no
// visibility at all, so the node keeps whatever the row already had rather than
// being told something by a client that was only ever guessing.
func TestWritingMemoryDoesNotPromoteIt(t *testing.T) {
	s := newStub(t)
	m := testModel(t, NewClient(s.URL, "t"))
	m.view = viewMemory
	m.memory = []*Artifact{
		{ID: "01HMEMPERSONAL", Type: "memory", Title: "mine", Body: "b", Visibility: "personal"},
		{ID: "01HMEMSHARED", Type: "memory", Title: "ours", Body: "b", Visibility: "project"},
	}

	write := func(key string, sel int, title, body string) map[string]any {
		t.Helper()
		m.memSel = sel
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m.input.SetValue(title)
		m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.input.SetValue(body)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("no write was issued")
		}
		if msg, ok := cmd().(wroteMsg); ok && msg.err != nil {
			t.Fatalf("the write failed: %v", msg.err)
		}
		return s.written[len(s.written)-1]
	}

	fresh := write("i", 0, "a new one", "body")
	if fresh["visibility"] != "personal" {
		t.Fatalf("a new memory was written as %v, want personal", fresh["visibility"])
	}
	if _, ok := fresh["id"]; ok {
		t.Fatal("a new memory was written against an id it did not have")
	}

	editPersonal := write("e", 0, "mine", "changed")
	if editPersonal["visibility"] != "personal" {
		t.Fatalf("editing a personal memory sent visibility %v", editPersonal["visibility"])
	}
	if editPersonal["id"] != "01HMEMPERSONAL" {
		t.Fatalf("the edit went to %v", editPersonal["id"])
	}

	editShared := write("e", 1, "ours", "changed")
	if _, ok := editShared["visibility"]; ok {
		t.Fatalf("editing a project memory sent a visibility of its own: %v", editShared["visibility"])
	}
}

// ------------------------------------------------------ who said what, in it

// saidBy is a message the node recorded a name for. An empty name is a message
// from before it recorded any, which is most of what is in a live room.
func saidBy(name, body string) *Event {
	e := &Event{
		ID: "01HEVENT" + name, Room: "general", Thread: "01HTHREAD1",
		Actor: "01HUSERAAAAAAAAAACM5BYZ3W", Body: body, Created: time.Now(),
	}
	e.Meta.ActorKind, e.Meta.ActorUser = "user", "01HUSERAAAAAAAAAACM5BYZ3W"
	e.Meta.ActorName = name
	return e
}

// rowWith is the rendered line a body landed on, and the assertion that it
// landed on one at all.
func rowWith(t *testing.T, screen, body string) string {
	t.Helper()
	for _, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, body) {
			return line
		}
	}
	t.Fatalf("no row on screen carries %q:\n%s", body, screen)
	return ""
}

// The complaint this column exists for: four agents and a person in one room,
// and every line drawn as the tail of a ulid. A message that carries a name is
// drawn under it - in the stream and in the thread pane beside it, because
// those are two places the same person reads the same conversation.
func TestARoomDrawsTheNameOfWhoeverSpoke(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.msgs = []*Event{saidBy("orchestrator", "one-body"),
		saidBy("flowy-claude", "two-body")}
	m.thread, m.threadOpen, m.view = m.msgs, true, viewRooms

	screen := m.View()
	for _, e := range m.msgs {
		if !strings.Contains(screen, e.Meta.ActorName) {
			t.Fatalf("the room does not say %q said anything:\n%s", e.Meta.ActorName, screen)
		}
		if !strings.Contains(rowWith(t, screen, e.Body), e.Meta.ActorName) {
			t.Fatalf("%q is on screen but not on its own row:\n%s", e.Meta.ActorName, screen)
		}
	}
	// And the id it replaced is not what a reader is left matching against.
	if strings.Contains(rowWith(t, screen, "one-body"), "CM5BYZ3W") {
		t.Fatalf("a named message still draws the tail of its actor id:\n%s", screen)
	}
}

// The discriminating half, and the one that covers the whole existing log:
// every message said before the node stamped a name has none, and those rows
// have to go on rendering exactly as they did - the tail of the actor's id, and
// the person an agent acts for where the message says so. A build that drew a
// blank column for them would have made the room less readable, not more, and
// would pass a test that only looked at the new field.
func TestAMessageWithNoNameFallsBackToTheId(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	person := saidBy("", "old-body")
	agent := saidBy("", "old-agent-body")
	agent.Actor = "01HAGENTBBBBBBBBBQ7WXYZ12"
	agent.Meta.ActorKind = "agent"
	m.msgs = []*Event{person, agent}
	m.thread, m.threadOpen, m.view = m.msgs, true, viewRooms

	screen := m.View()
	if row := rowWith(t, screen, person.Body); !strings.Contains(row, "CM5BYZ3W") {
		t.Fatalf("a message with no name lost its speaker altogether: %q", row)
	}
	if row := rowWith(t, screen, agent.Body); !strings.Contains(row, "'s agent") {
		t.Fatalf("an agent message with no name stopped saying whose agent it is: %q", row)
	}
	if got := person.Speaker(); got != "CM5BYZ3W" {
		t.Fatalf("Speaker() on a nameless message is %q, not the tail of the actor id", got)
	}
}

// A handle has no length limit and a terminal does. The name column is a
// budget: a long one is cut to it, so the body starts in the same place on
// every row and what somebody said is still on screen at 80 columns. The rule
// the layout test asserts - no line wider than the terminal - is met by a row
// that clipped the body away entirely, so this checks the body is there.
func TestALongNameDoesNotPushTheBodyOffTheRow(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.msgs = []*Event{
		saidBy("ada", "the short name's message"),
		saidBy(strings.Repeat("a-very-long-handle-", 5), "the long name's message"),
	}
	m.view = viewRooms
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	screen := m.View()
	short := rowWith(t, screen, "the short name's message")
	long := rowWith(t, screen, "the long name's message")
	if strings.Index(short, "the short name's message") != strings.Index(long, "the long name's message") {
		t.Fatalf("the long name moved the body along the row:\n%s\n%s", short, long)
	}
	if lipgloss.Width(long) > 80 {
		t.Fatalf("the row is %d wide at 80 columns: %q", lipgloss.Width(long), long)
	}
	// The same budget on the timeline, which is the other pane a message can
	// be read on and is full width rather than a column of three.
	m.view = viewTimeline
	m.tl = []*ActivityItem{
		{ID: "01HACT1", Kind: "chat", Actor: "01HUSERAAAAAAAAAACM5BYZ3W",
			ActorName: "ada", Room: "general", Body: "the short name's line",
			Created: "2026-08-15T10:11:12Z"},
		{ID: "01HACT2", Kind: "chat", Actor: "01HUSERAAAAAAAAAACM5BYZ3W",
			ActorName: strings.Repeat("a-very-long-handle-", 5), Room: "general",
			Body: "the long name's line", Created: "2026-08-15T10:11:13Z"},
	}
	screen = m.View()
	short = rowWith(t, screen, "the short name's line")
	long = rowWith(t, screen, "the long name's line")
	if strings.Index(short, "the short name's line") != strings.Index(long, "the long name's line") {
		t.Fatalf("the long name moved the timeline body along the row:\n%s\n%s", short, long)
	}
}

// --------------------------------------------------------------- the reports

// A report says what it is true of, and the list is where that has to be
// legible: as_of is how a reader tells a current report from a stale one, and
// that decision is made while looking at the list rather than after opening
// something. supersedes and the tags belong to the same judgement.
func TestTheReportsListSaysWhatEachReportIsTrueOf(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	seed(m)
	m.view = viewReports

	screen := m.View()
	for _, want := range []string{
		"reports (1)", "the harness architecture", "f343027",
		"supersedes", "harness-research", "the spine",
	} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the reports pane does not show %q:\n%s", want, screen)
		}
	}
}

// A report whose provenance is missing or malformed is a report with no
// provenance on screen. The body is still what somebody opened the pane for, so
// the pane renders rather than failing on a field it could not read.
func TestAReportWithNoUsableProvenanceStillRenders(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.view = viewReports
	m.reports = []*Artifact{
		{ID: "01HREPNOFIELDS", Type: "report", Title: "no fields at all", Body: "still readable"},
		{ID: "01HREPBADFIELDS", Type: "report", Title: "fields that do not parse",
			Body: "also readable", Fields: json.RawMessage(`{"as_of": 17}`)},
	}
	for sel := range m.reports {
		m.repSel = sel
		screen := m.View()
		if !strings.Contains(screen, m.reports[sel].Title) {
			t.Fatalf("report %d is not on screen:\n%s", sel, screen)
		}
		if !strings.Contains(screen, "readable") {
			t.Fatalf("report %d rendered without its body:\n%s", sel, screen)
		}
	}
	if line := provenanceLine(m.reports[1]); line != "" {
		t.Fatalf("fields that do not parse produced a provenance line: %q", line)
	}
}

// Both the list and the search ask for reports and nothing else. A search with
// no type answers with memories, bugs and notes beside the reports, and a pane
// that listed those under the word "reports" would be saying something untrue
// about what a report is.
func TestTheReportsPaneAsksForReportsOnly(t *testing.T) {
	s := newStub(t)
	m := testModel(t, NewClient(s.URL, "t"))

	m.Update(m.reportsCmd("")())
	if len(m.reports) != 1 || m.repQuery != "" {
		t.Fatalf("the list did not land: %d reports, query %q", len(m.reports), m.repQuery)
	}
	m.Update(m.reportsCmd("caching spine")())
	if m.repQuery != "caching spine" {
		t.Fatalf("the pane is showing %q, not the search that was run", m.repQuery)
	}

	if len(s.asked) != 2 {
		t.Fatalf("the node was asked %d times, want 2: %v", len(s.asked), s.asked)
	}
	for _, asked := range s.asked {
		if !strings.Contains(asked, "type=report") {
			t.Fatalf("a read went out without the report type: %q", asked)
		}
	}
	if !strings.Contains(s.asked[0], "/api/artifacts") {
		t.Fatalf("an empty query did not list: %q", s.asked[0])
	}
	if !strings.Contains(s.asked[1], "/api/search") {
		t.Fatalf("a query did not search: %q", s.asked[1])
	}
}

// Opening a report goes to the artifact view, which scrolls, and esc comes back
// to the list rather than to whichever pane was open before it.
func TestOpeningAReportComesBackToTheReportsList(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	seed(m)
	m.view = viewReports

	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("enter on a report issued no read")
	}
	if m.view != viewArtifact {
		t.Fatalf("enter left the view at %s", tabNames[m.view])
	}
	if m.backView != viewReports {
		t.Fatalf("esc would go back to %s, not the reports list", tabNames[m.backView])
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.view != viewReports {
		t.Fatalf("esc landed on %s", tabNames[m.view])
	}
}

// Nine tabs of full labels are wider than the terminal this client is written
// for, so the bar drops to digits rather than dropping the last tab off the
// right: a view nobody can see the key for is a view nobody finds. Eight
// already did not fit at 95 columns; todos took the row to 95.
func TestTheTabBarKeepsEveryDigitOnANarrowTerminal(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	for _, size := range []struct{ w, h int }{{80, 24}, {132, 43}} {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		bar := m.tabBarLine()
		if width := lipgloss.Width(bar); width > size.w {
			t.Fatalf("the tab bar is %d wide at %d columns: %q", width, size.w, bar)
		}
		for i := range tabNames {
			if !strings.Contains(bar, fmt.Sprint(i+1)) {
				t.Fatalf("the digit for %s is missing at %d columns: %q",
					tabNames[i], size.w, bar)
			}
		}
		// The one being looked at keeps its name whatever the width.
		if !strings.Contains(bar, tabNames[m.view]) {
			t.Fatalf("the active tab lost its name at %d columns: %q", size.w, bar)
		}
	}

	// And the half that makes the digits above mean something. At 80 the names
	// have to be gone - all nine of them do not fit, and a bar that kept them
	// and let the clip take the right-hand tabs is the failure this drop-to-
	// digits behaviour exists to prevent. At 132 they all fit and are all drawn,
	// so a build that gave up its names unconditionally fails here.
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	narrow := m.tabBarLine()
	for i, name := range tabNames {
		if view(i) == m.view {
			continue
		}
		if strings.Contains(narrow, name) {
			t.Fatalf("%s kept its name at 80 columns, where nine names do not fit: %q",
				name, narrow)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 132, Height: 43})
	wide := m.tabBarLine()
	for _, name := range tabNames {
		if !strings.Contains(wide, name) {
			t.Fatalf("%s is missing from a bar with room for every name: %q", name, wide)
		}
	}
}

// ----------------------------------------------------------------- the todos
//
// The complaint this view exists for, in the words that produced it: "I'm
// confused as hell now where we are and what we are doing and what depends on
// what". Four agents and a person were working one queue that existed as chat
// messages and summaries, and the items had been artifacts for a while with
// nothing rendering them.

// The ordering is the feature. Active first, then what is open, then what is
// done - a list that buries what is in flight under what is finished answers
// none of the questions somebody opens this pane to ask - and the header counts
// each, because "how much is left" is the other one.
func TestTheTodosListPutsWhatIsInFlightFirst(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.view = viewTodos
	// Deliberately in the worst order the node could hand them over in, and
	// through the message the command really sends, so the ordering is the
	// client's own and not the fixture's.
	m.Update(todosMsg{artifacts: []*Artifact{
		{ID: "01HT1", Type: "memory", Kind: "todo", Status: "done",
			Title: "finished-one", Body: "OWNER: ada"},
		{ID: "01HT2", Type: "memory", Kind: "todo", Status: "todo",
			Title: "waiting-one", Body: "OWNER: bob"},
		{ID: "01HT3", Type: "memory", Kind: "todo", Status: "blocked",
			Title: "unheard-of-status-one", Body: "OWNER: cass"},
		{ID: "01HT4", Type: "memory", Kind: "todo", Status: "active",
			Title: "in-flight-one", Body: "OWNER: dee"},
	}})

	var order []string
	for _, a := range m.todos {
		order = append(order, a.Status)
	}
	// A status this client has never heard of is work nobody has said is done,
	// so it sorts with the open ones rather than out of the way at the bottom.
	if want := []string{"active", "todo", "blocked", "done"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("the queue came out %v, want %v", order, want)
	}

	screen := m.View()
	at := func(title string) int {
		t.Helper()
		i := strings.Index(screen, title)
		if i < 0 {
			t.Fatalf("%q is not on screen:\n%s", title, screen)
		}
		return i
	}
	if !(at("in-flight-one") < at("waiting-one") && at("waiting-one") < at("finished-one")) {
		t.Fatalf("the rows are not drawn active, open, done:\n%s", screen)
	}
	if !strings.Contains(screen, "todos (1 active, 2 open, 1 done)") {
		t.Fatalf("the header does not count the queue by status:\n%s", screen)
	}
}

// Who is doing it is the point of the row: the complaint was that nobody could
// tell, which is the same complaint that put names on the chat surfaces an hour
// earlier. It is read off the OWNER line the items are written with, and never
// off owner_user - that is the ulid of whoever filed the row, which for this
// queue is one agent for all fourteen of them.
func TestATodoRowSaysWhoOwnsIt(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	seed(m)
	m.view = viewTodos

	screen := m.View()
	for _, want := range []string{"active", "todo-view", "a todos view in the terminal client"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("the todos pane does not show %q:\n%s", want, screen)
		}
	}
	row := rowWith(t, screen, "a todos view in the terminal client")
	if !strings.Contains(row, "todo-view") {
		t.Fatalf("the owner is on screen but not on the item's own row: %q", row)
	}
	// What depends on what, which is the other half of the question and lives in
	// the body rather than on the row.
	if !strings.Contains(screen, "DEPENDS ON: the kind filter") {
		t.Fatalf("the selected todo's body is not under the rule:\n%s", screen)
	}
	if got := todoOwner(m.todos[0]); got != "todo-view" {
		t.Fatalf("the owner parsed as %q", got)
	}
}

// Two of the fourteen carry no OWNER line, so a row that only knew how to draw
// an owner would have dropped or broken those two - and they are exactly the
// ones somebody needs to see, because an unowned todo is work nobody has picked
// up. They render with a dash, body and all.
func TestATodoWithNoOwnerStillRenders(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.view = viewTodos
	m.todos = []*Artifact{
		{ID: "01HTNONE", Type: "memory", Kind: "todo", Status: "todo",
			Title: "nobody has this one", Body: "DEPENDS ON: somebody picking it up"},
		{ID: "01HTEMPTY", Type: "memory", Kind: "todo", Status: "todo",
			Title: "and this one has no body at all"},
	}
	for sel := range m.todos {
		m.todoSel = sel
		screen := m.View()
		if !strings.Contains(screen, m.todos[sel].Title) {
			t.Fatalf("todo %d is not on screen:\n%s", sel, screen)
		}
		if !strings.Contains(rowWith(t, screen, m.todos[sel].Title), "-") {
			t.Fatalf("todo %d draws no owner column at all:\n%s", sel, screen)
		}
	}
	m.todoSel = 0
	if !strings.Contains(m.View(), "DEPENDS ON: somebody picking it up") {
		t.Fatal("an unowned todo lost its body")
	}

	// And the parse, which reads the first line and not the whole body: OWNER in
	// the middle of a sentence about somebody else's item is not an owner.
	if got := todoOwner(&Artifact{Body: "DEPENDS ON: x\nOWNER: not-really"}); got != "" {
		t.Fatalf("an OWNER further down the body was taken as the owner: %q", got)
	}
	if got := todoOwner(&Artifact{Body: "OWNER:   ada  \nrest"}); got != "ada" {
		t.Fatalf("the owner parsed as %q", got)
	}
}

// The discriminating one: an empty queue renders its empty state and not a
// blank pane. A view that drew nothing at all would pass every layout check
// here - no line is too wide and no pane too tall when there are no lines - and
// would tell a reader who came to find out where the work is that the client is
// broken, or that there is no queue, with no way to tell which.
func TestAnEmptyTodoListSaysSoRatherThanRenderingNothing(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.view = viewTodos
	m.todos = []*Artifact{}

	screen := m.View()
	if !strings.Contains(screen, "todos (0 active, 0 open, 0 done)") {
		t.Fatalf("an empty queue has no header:\n%s", screen)
	}
	// Where they come from, because this pane cannot write one.
	if !strings.Contains(screen, "mem_write") {
		t.Fatalf("the empty pane does not say where a todo comes from:\n%s", screen)
	}

	// And a search that matched nothing says that instead, because "there are
	// none" and "none matched what you typed" are different things to do next
	// about.
	m.todoQuery = "quinceberry"
	searched := m.View()
	if !strings.Contains(searched, "nothing matched") || !strings.Contains(searched, "c clears") {
		t.Fatalf("an empty search result does not say how to get back:\n%s", searched)
	}
	if strings.Contains(searched, "mem_write") {
		t.Fatalf("an empty search result is being explained as an empty queue:\n%s", searched)
	}
}

// A todo is an artifact of type memory with kind todo, so both narrowings go
// out on both reads. The type alone answers with every note, handoff and
// feature anybody has written, and a pane that listed those as todos would be
// showing a queue nobody filed.
func TestTheTodosPaneAsksForTodosOnly(t *testing.T) {
	s := newStub(t)
	m := testModel(t, NewClient(s.URL, "t"))

	m.Update(m.todosCmd("")())
	if len(m.todos) != 1 || m.todoQuery != "" {
		t.Fatalf("the list did not land: %d todos, query %q", len(m.todos), m.todoQuery)
	}
	m.Update(m.todosCmd("the tab bar")())
	if m.todoQuery != "the tab bar" {
		t.Fatalf("the pane is showing %q, not the search that was run", m.todoQuery)
	}

	if len(s.asked) != 2 {
		t.Fatalf("the node was asked %d times, want 2: %v", len(s.asked), s.asked)
	}
	for _, asked := range s.asked {
		if !strings.Contains(asked, "type=memory") || !strings.Contains(asked, "kind=todo") {
			t.Fatalf("a read went out without both narrowings: %q", asked)
		}
	}
	if !strings.Contains(s.asked[0], "/api/artifacts") {
		t.Fatalf("an empty query did not list: %q", s.asked[0])
	}
	if !strings.Contains(s.asked[1], "/api/search") {
		t.Fatalf("a query did not search: %q", s.asked[1])
	}
}

// Opening a todo goes to the artifact view, which scrolls, and esc comes back
// to the queue rather than to whichever pane was open before it.
func TestOpeningATodoComesBackToTheTodosList(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	seed(m)
	m.view = viewTodos

	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("enter on a todo issued no read")
	}
	if m.view != viewArtifact {
		t.Fatalf("enter left the view at %s", tabNames[m.view])
	}
	if m.backView != viewTodos {
		t.Fatalf("esc would go back to %s, not the todos list", tabNames[m.backView])
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.view != viewTodos {
		t.Fatalf("esc landed on %s", tabNames[m.view])
	}
}

// A handle has no length limit and a terminal has eighty columns. The owner is
// a budgeted column: a long name is cut to it rather than pushing what the item
// is off the right of the row, so the titles start in the same place and the
// pane is still readable at the size it is written for. The layout check next
// door is met by a row that clipped the title away entirely, so this one looks
// for the title.
func TestALongOwnerDoesNotPushTheTitleOffTheRow(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.view = viewTodos
	m.todos = []*Artifact{
		{ID: "01HTSHORT", Type: "memory", Kind: "todo", Status: "active",
			Title: "the short owner's item", Body: "OWNER: ada"},
		{ID: "01HTLONG", Type: "memory", Kind: "todo", Status: "active",
			Title: "the long owner's item",
			Body:  "OWNER: " + strings.Repeat("a-very-long-handle-", 5)},
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	screen := m.View()
	short := rowWith(t, screen, "the short owner's item")
	long := rowWith(t, screen, "the long owner's item")
	if strings.Index(short, "the short owner's item") != strings.Index(long, "the long owner's item") {
		t.Fatalf("the long owner moved the title along the row:\n%s\n%s", short, long)
	}
	if width := lipgloss.Width(long); width > 80 {
		t.Fatalf("the row is %d wide at 80 columns: %q", width, long)
	}
}

// --------------------------------------------------- the room's plan, in the room
//
// The console grew a todo panel beside the transcript and then dropped its
// global /todos page, so in a browser the room's panel is the only queue view
// there is. This is the same thing for the terminal: what the room has agreed
// to do and who is carrying it, beside the conversation it was agreed in.

// roomWithAPlan is a room with something said in it and a plan of three todos
// beside it, one in each state and one of them assigned through the field
// rather than through the body's OWNER line.
//
// Every title is inside minTodosWidth on purpose. The pane clips, and a check
// that seeded a 40-character title would be asserting about the clip rather
// than about the panel.
func roomWithAPlan(m *Model) {
	m.view = viewRooms
	m.msgs = []*Event{saidBy("ada", "said something here")}
	m.msgSel = 0
	m.thread = m.msgs
	m.roomTodos = sortTodos([]*Artifact{
		{ID: "01HRTDONE", Type: "memory", Kind: "todo", Status: "done",
			Title: "the panel itself", Body: "OWNER: ada"},
		{ID: "01HRTTODO", Type: "memory", Kind: "todo", Status: "todo",
			Title: "the eighty column fit", Body: "OWNER: bob"},
		{ID: "01HRTACTIVE", Type: "memory", Kind: "todo", Status: "active",
			Title: "the three states", Body: "OWNER: ignore-me",
			Fields: json.RawMessage(`{"room":"general","assignee":"cass"}`)},
	})
}

// panelColumn is what was drawn to the right of the rightmost column rule,
// which in this view is the todos panel: roomsView writes it last of the panes
// that are open, and the thread pane is closed in every caller below.
//
// It exists because "the title is on the screen" is not the claim being made.
// Raising a todo from a room posts "raised a todo: <title>" into that room, so
// the words are in the stream whether or not the panel drew anything at all,
// and a check that looked for them anywhere would pass against a client with no
// panel in it.
func panelColumn(screen string) []string {
	var out []string
	for _, line := range strings.Split(screen, "\n") {
		if i := strings.LastIndex(line, "|"); i >= 0 {
			out = append(out, strings.TrimSpace(line[i+1:]))
		}
	}
	return out
}

// panelSays reports whether some line of the panel column is exactly this, and
// panelHead whether the panel drew its counted header at all.
func panelSays(panel []string, want string) bool {
	for _, line := range panel {
		if line == want {
			return true
		}
	}
	return false
}

func panelHead(panel []string) string {
	for _, line := range panel {
		if strings.HasPrefix(line, "todos ") && strings.Contains(line, "/") {
			return line
		}
	}
	return ""
}

// The width the failure lives at. 80 columns is what this client targets - it
// is why the tab bar has a compact fallback - and the plan pane was drawn at a
// quarter of the terminal, which at 80 columns left the conversation under
// minStreamWidth and took the whole pane away. So somebody on the commonest
// terminal there is saw no plan at all and no reason why, which is the same
// outcome as not having built it.
func TestTheRoomsPlanIsThereAtEightyColumns(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	roomWithAPlan(m)

	screen := m.View()
	panel := panelColumn(screen)
	if got := panelHead(panel); got != "todos 1/1/1" {
		t.Fatalf("the plan pane has no counted header at 80 columns, got %q:\n%s", got, screen)
	}
	for _, a := range m.roomTodos {
		if !panelSays(panel, a.Title) {
			t.Fatalf("%q is not in the plan pane at 80 columns:\n%s", a.Title, screen)
		}
	}
	// Who is carrying each one, which is the other half of the row - and cass is
	// read off the assignee field while ada and bob are read off their bodies,
	// so a panel that only knew one of the two orders fails here.
	// The spacing is part of it: the status column is fixed, so the names line
	// up under each other instead of starting wherever the status word ended.
	for _, row := range []string{"active cass", "todo   bob", "done   ada"} {
		if !panelSays(panel, row) {
			t.Fatalf("the plan pane has no row %q:\n%s", row, screen)
		}
	}
	// And the conversation is still a conversation: the pane is beside the room
	// and not instead of it, so what somebody said is drawn whole.
	if !strings.Contains(screen, "said something here") {
		t.Fatalf("the plan pane squeezed the stream out at 80 columns:\n%s", screen)
	}
	for i, line := range strings.Split(screen, "\n") {
		if width := lipgloss.Width(line); width > 80 {
			t.Fatalf("line %d is %d wide at 80 columns: %q", i, width, line)
		}
	}

	// What paid for it, since something had to: at 80 the room list gives up its
	// column, which is the documented order and the reason the pane fits. The
	// list is back the moment the plan is closed, and T is how that is done.
	if listWidth, _, todosWidth, _ := m.roomPaneWidths(); listWidth != 0 || todosWidth == 0 {
		t.Fatalf("at 80 columns the list is %d wide and the plan is %d", listWidth, todosWidth)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")})
	if listWidth, _, todosWidth, _ := m.roomPaneWidths(); listWidth == 0 || todosWidth != 0 {
		t.Fatalf("closing the plan did not give the room list its column back: "+
			"list %d, plan %d", listWidth, todosWidth)
	}
}

// Three states, told apart, and told apart in a word as well as in a colour.
//
// The colours are the console's three meanings: amber for what is in flight,
// grey for what is waiting, green for what is finished. Two of the three used
// to share a style here, which is a queue with two states in it - a finished
// todo and an untouched one looked the same.
//
// The second half is the one that matters more. This client runs over ssh onto
// whatever terminal is at the other end, and lipgloss renders every one of
// these as plain text on a TERM=dumb, a NO_COLOR, or a pipe. So the status WORD
// is on the row, and it is on the row in mono too.
func TestTheThreeTodoStatesAreToldApartAndSaidInWords(t *testing.T) {
	coloured := New(NewClient("http://127.0.0.1:1", "t"), NewTheme(termenv.ANSI256, false))
	coloured.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	roomWithAPlan(coloured)

	// The panel is a header and then two lines per item, in reading order, so
	// the status rows are the odd ones and they are active, todo, done.
	lines := coloured.roomTodoLines(30, 20)
	if len(lines) < 7 {
		t.Fatalf("the plan pane drew %d lines for three todos: %q", len(lines), lines)
	}
	rows := []struct{ status, line string }{
		{"active", lines[1]}, {"todo", lines[3]}, {"done", lines[5]},
	}
	drawn := map[string]string{}
	for _, row := range rows {
		if !strings.Contains(row.line, row.status) {
			t.Fatalf("the %s row does not say which state it is in: %q", row.status, row.line)
		}
		colour := ansiPrefix(row.line)
		if colour == "" {
			t.Fatalf("the %s row is drawn in no colour at all on a 256-colour terminal: %q",
				row.status, row.line)
		}
		if other, taken := drawn[colour]; taken {
			t.Fatalf("%s and %s are drawn in the same colour, so the panel has "+
				"two states in it and not three: %q", other, row.status, row.line)
		}
		drawn[colour] = row.status
	}

	// And the terminal that has none of that: no escape sequence anywhere, and
	// all three states still readable.
	mono := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	roomWithAPlan(mono)
	monoLines := mono.roomTodoLines(30, 20)
	for _, want := range []string{"active", "todo", "done"} {
		found := false
		for i, line := range monoLines {
			if i%2 == 1 && strings.Contains(line, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q is not a word on any row of a monochrome plan pane: %q", want, monoLines)
		}
	}
	for _, line := range monoLines {
		if strings.Contains(line, "\x1b") {
			t.Fatalf("a monochrome terminal was sent an escape sequence: %q", line)
		}
	}
}

// ansiPrefix is the escape sequence a line opens with, or "" for a line drawn
// in nothing.
func ansiPrefix(line string) string {
	if !strings.HasPrefix(line, "\x1b[") {
		return ""
	}
	if i := strings.Index(line, "m"); i >= 0 {
		return line[:i+1]
	}
	return ""
}

// The regression this change is most likely to have caused: the thread pane was
// here first, the gate drives it by keyboard, and the plan pane is beside it
// rather than instead of it.
//
// At 80 columns they do not both fit - see roomPaneWidths - so the plan yields
// and the key that opens the thread says which of the two it is, because
// "wider terminal" and "close the other pane" are different things to do. At
// 120 they are both on screen at once.
func TestTheThreadPaneStillWorksBesideThePlan(t *testing.T) {
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	roomWithAPlan(m)
	if panelHead(panelColumn(m.View())) == "" {
		t.Fatalf("the plan is not drawn at 80 columns to begin with:\n%s", m.View())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if !m.threadOpen {
		t.Fatal("t did not open the thread pane")
	}
	screen := m.View()
	if !strings.Contains(screen, "thread") {
		t.Fatalf("the thread pane is not on screen after t:\n%s", screen)
	}
	if !strings.Contains(screen, "said something here") {
		t.Fatalf("the thread pane drew no thread:\n%s", screen)
	}

	// t again, and the plan is back where it was.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if m.threadOpen {
		t.Fatal("t did not close the thread pane again")
	}
	if panelHead(panelColumn(m.View())) == "" {
		t.Fatalf("the plan did not come back when the thread closed:\n%s", m.View())
	}

	// T with the thread open at 80 says which pane is in the way rather than
	// leaving the key looking broken.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")})
	if !strings.Contains(m.flash, "t closes the thread") {
		t.Fatalf("T on a terminal where the thread is what is in the way said %q", m.flash)
	}

	// And at 120 there is room for both, which is the pane count this view was
	// built to hold.
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	screen = m.View()
	if !strings.Contains(screen, "thread") || panelHead(panelColumn(screen)) != "" {
		t.Fatalf("the thread pane and the plan are not both drawn at 120 columns:\n%s", screen)
	}
	_, _, todosWidth, threadWidth := m.roomPaneWidths()
	if todosWidth == 0 || threadWidth == 0 {
		t.Fatalf("at 120 columns the plan is %d wide and the thread %d", todosWidth, threadWidth)
	}
}

// One word for one state, which is a todo the panel raised about itself: 'todo
// list has "unowned" and "unassigned" - looks identical. triage and fix'. Two
// words for one state read as two states and send a reader looking for a
// distinction that is not there.
//
// The node normalises what it is handed and the console collapses the same set
// on the way out; the bodies written before either existed still say whatever
// somebody typed, and this is the terminal's copy of that read.
func TestTheQueueSaysOneWordForNobody(t *testing.T) {
	for _, said := range []string{"unassigned", "unowned", "none", "nobody", "TBD", "?", "-", "n/a"} {
		if got := todoOwner(&Artifact{Body: "OWNER: " + said}); got != "" {
			t.Fatalf("OWNER: %s came back as %q, which is a second word for nobody", said, got)
		}
		if got := todoOwner(&Artifact{
			Fields: json.RawMessage(`{"assignee":"` + said + `"}`)}); got != "" {
			t.Fatalf("an assignee of %q came back as %q", said, got)
		}
	}
	// And a name that merely starts with one of them is a name.
	if got := todoOwner(&Artifact{Body: "OWNER: nobody-in-particular"}); got != "nobody-in-particular" {
		t.Fatalf("a real handle was collapsed to %q", got)
	}

	// On the row, where it is seen: two items written with two different words
	// draw the same one column.
	m := testModel(t, NewClient("http://127.0.0.1:1", "t"))
	m.view = viewTodos
	m.todos = []*Artifact{
		{ID: "01HTW1", Type: "memory", Kind: "todo", Status: "todo",
			Title: "the first one", Body: "OWNER: unowned"},
		{ID: "01HTW2", Type: "memory", Kind: "todo", Status: "todo",
			Title: "the second one", Body: "OWNER: unassigned"},
	}
	screen := m.View()
	for _, title := range []string{"the first one", "the second one"} {
		row := rowWith(t, screen, title)
		if strings.Contains(row, "unowned") || strings.Contains(row, "unassigned") {
			t.Fatalf("the row for %q spells nobody out: %q", title, row)
		}
		if !strings.Contains(row, "-") {
			t.Fatalf("the row for %q draws no owner column at all: %q", title, row)
		}
	}
}

// ------------------------------------------------------------ the live gate
//
// These run against the gate's own `flowy serve`, driven headless through
// teatest: the same model the binary runs, fed the keystrokes a keyboard sends.
// They skip when there is no node to talk to, which is what keeps `go test
// ./...` runnable on its own.

type liveEnv struct {
	url, token, room, message, memory, task, report, asOf string
	todo, todoOwner                                       string
	// roomTodo is a todo raised in the room the drive opens, which the room
	// view has to draw beside the messages rather than only on the todos tab.
	roomTodo string
}

func live(t *testing.T) liveEnv {
	t.Helper()
	env := liveEnv{
		url:     os.Getenv("FLOWY_TUI_URL"),
		token:   os.Getenv("FLOWY_TUI_TOKEN"),
		room:    os.Getenv("FLOWY_TUI_ROOM"),
		message: os.Getenv("FLOWY_TUI_MESSAGE"),
		memory:  os.Getenv("FLOWY_TUI_MEMORY"),
		task:    os.Getenv("FLOWY_TUI_TASK"),
		report:  os.Getenv("FLOWY_TUI_REPORT"),
		asOf:    os.Getenv("FLOWY_TUI_REPORT_AS_OF"),

		todo:      os.Getenv("FLOWY_TUI_TODO"),
		todoOwner: os.Getenv("FLOWY_TUI_TODO_OWNER"),
		roomTodo:  os.Getenv("FLOWY_TUI_ROOM_TODO"),
	}
	if env.url == "" || env.token == "" {
		t.Skip("no live node: set FLOWY_TUI_URL and FLOWY_TUI_TOKEN")
	}
	if env.room == "" {
		env.room = "general"
	}
	return env
}

// screen accumulates everything the program has drawn, so that one wait does
// not consume the frames the next one is looking for. teatest.WaitFor reads the
// stream itself and would; and a wait that fails because an earlier wait ate
// the evidence is the worst kind of flake to read.
type screen struct {
	out  io.Reader
	seen []byte
}

func watch(tm *teatest.TestModel) *screen { return &screen{out: tm.Output()} }

// waitFor blocks until text has been drawn, and says what it was waiting for
// and what was on the screen instead when it never was.
func (s *screen) waitFor(t *testing.T, what, text string) {
	t.Helper()
	s.waitUntil(t, what, func(seen []byte) bool { return bytes.Contains(seen, []byte(text)) })
}

// waitUntil is waitFor for a condition that is not a plain substring - the one
// that matters being "this text was drawn somewhere other than the box it was
// typed into".
func (s *screen) waitUntil(t *testing.T, what string, ready func(seen []byte) bool) {
	t.Helper()
	buf := make([]byte, 8192)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if ready(s.seen) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s timed out; the screen ended up as:\n%s",
				what, lastFrame(s.seen))
		}
		n, err := s.out.Read(buf)
		if n > 0 {
			s.seen = append(s.seen, buf[:n]...)
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("reading the screen while waiting for %s: %v", what, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// outsideTheBox reports whether text has been drawn somewhere that is not the
// message box.
//
// It matters because the box echoes what is typed into it: a wait for the text
// of a message would be satisfied the moment somebody typed it, on a client
// that then posted nothing at all. The box's prompt ends in "> ", and the
// stream pads its columns with spaces, so what is looked for is an occurrence
// that is not immediately after a prompt.
func outsideTheBox(seen []byte, text string) bool {
	needle := []byte(text)
	for i := 0; i+len(needle) <= len(seen); i++ {
		if !bytes.HasPrefix(seen[i:], needle) {
			continue
		}
		if i >= 2 && seen[i-1] == ' ' && seen[i-2] == '>' {
			continue
		}
		return true
	}
	return false
}

// lastFrame is as much of the tail of the output as belongs in a failure.
func lastFrame(seen []byte) string {
	const keep = 3000
	if len(seen) > keep {
		seen = seen[len(seen)-keep:]
	}
	return string(seen)
}

// The whole client, driven by the keyboard against a live node: the room
// renders, a message typed into the box comes back through the watcher, the
// inbox has the seeded task in it, memory finds the seeded item, and the
// timeline and the metrics render. Then it is resized, and then it quits.
func TestLiveTUIDrivenByTheKeyboard(t *testing.T) {
	env := live(t)

	m := New(NewClient(env.url, env.token), monoTheme())
	m.SetRoom(env.room)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	s := watch(tm)

	s.waitFor(t, "the room header", env.room)
	if env.message != "" {
		s.waitFor(t, "the seeded message", env.message)
	}
	// The room's own plan, beside the room's messages. It is drawn without
	// anybody pressing anything: the panel is open by default and it is filled
	// off the back of the room read, so what this waits for is the pane having
	// been read and drawn rather than a key having worked.
	if env.roomTodo != "" {
		s.waitFor(t, "the todo raised in this room", env.roomTodo)
	}

	// The message box: i opens it, the text goes in, enter posts it.
	//
	// What is waited for here is the node's acknowledgement and not the text
	// itself. The text is on screen the moment it is typed - it is in the box -
	// so a wait for it would pass on a client that posted nothing at all. That
	// it arrived in the room is checked at the end, against the model's own
	// message list, which is filled by the watcher and by nothing else.
	mine := "tui-said-" + fmt.Sprint(time.Now().UnixNano())
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	tm.Type(mine)
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	s.waitFor(t, "the node's acknowledgement of the posted message", "posted to "+env.room)
	s.waitUntil(t, "the posted message coming back into the stream through the watcher",
		func(seen []byte) bool { return outsideTheBox(seen, mine) })

	// The inbox.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	s.waitFor(t, "the tasks header", "tasks assigned to you")
	if env.task != "" {
		s.waitFor(t, "the seeded task", env.task)
	}

	// Memory, and a search that finds the seeded item.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	s.waitFor(t, "the memory header", "memory (")
	if env.memory != "" {
		// The header only says "matching" once the node has answered: the query
		// is set from the response and never from what was typed, so this is
		// the search having happened rather than the search box having been
		// filled in.
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		tm.Type(env.memory)
		tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
		s.waitFor(t, "the memory search", `matching "`+env.memory+`"`)
	}

	// The timeline and the metrics.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	s.waitFor(t, "the timeline", "activity (")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("6")})
	s.waitFor(t, "the metrics", "scope ")

	// The reports, which the seed filed over the API rather than through
	// report_write - the case this view exists for, since that path emits no
	// activity event and the timeline therefore cannot show it.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("8")})
	s.waitFor(t, "the reports header", "reports (")
	if env.report != "" {
		s.waitFor(t, "the seeded report", env.report)
		if env.asOf != "" {
			// What the report is true of, rendered from fields rather than from
			// a column. A report listed without it is the claim-with-no-expiry
			// the type was invented to avoid.
			s.waitFor(t, "the report's as_of", env.asOf)
		}
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		tm.Type(env.report)
		tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
		s.waitFor(t, "the report search", `matching "`+env.report+`"`)
	}

	// The todos: the queue, with who owns each item on the row. Nothing is typed
	// into this one - it reads and does not write, and what it has to get right
	// is the list itself, which is checked against the final model below.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9")})
	s.waitFor(t, "the todos header", "todos (")
	if env.todo != "" {
		s.waitFor(t, "the seeded todo", env.todo)
		if env.todoOwner != "" {
			s.waitFor(t, "the todo's owner", env.todoOwner)
		}
	}

	// A resize, twice, including down to the smallest terminal anybody uses.
	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	tm.Send(tea.WindowSizeMsg{Width: 40, Height: 10})
	tm.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	s.waitFor(t, "the room again after the resize", env.room)

	// q quits, and the program comes back rather than hanging.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(15*time.Second))

	// And what the screen alone cannot say. The message list is filled by the
	// room read and the watcher and by nothing else, so finding the posted
	// message in it is the message having gone to the node and come back.
	final, ok := tm.FinalModel(t).(*Model)
	if !ok {
		t.Fatal("the final model is not this model")
	}
	said := false
	for _, e := range final.msgs {
		if strings.Contains(e.Body, mine) {
			said = true
		}
	}
	if !said {
		t.Fatalf("the message posted through the box never came back into %s", env.room)
	}
	if !strings.Contains(final.View(), mine) {
		t.Fatal("the posted message is not on the last screen the client drew")
	}
	if env.memory != "" {
		if len(final.memory) == 0 {
			t.Fatalf("searching memory for %q found nothing", env.memory)
		}
		if final.memQuery != env.memory {
			t.Fatalf("the memory view is showing %q, not the search that was run", final.memQuery)
		}
	}
	if env.report != "" {
		if len(final.reports) == 0 {
			t.Fatalf("searching the reports for %q found nothing", env.report)
		}
		if final.repQuery != env.report {
			t.Fatalf("the reports pane is showing %q, not the search that was run", final.repQuery)
		}
		if env.asOf != "" && reportProvenance(final.reports[0]).AsOf != env.asOf {
			t.Fatalf("the report came back without its as_of: %q",
				string(final.reports[0].Fields))
		}
	}
	if env.todo != "" {
		if len(final.todos) == 0 {
			t.Fatal("the todos view never got a queue")
		}
		// The narrowing, against a store that has notes and handoffs of the same
		// type in it: everything the pane listed was filed as a todo. A client
		// that asked for the type alone passes every screen check above and
		// fails here.
		for _, a := range final.todos {
			if a.Kind != "todo" {
				t.Fatalf("the queue lists a %s/%s, which nobody filed as a todo: %q",
					a.Type, a.Kind, a.Title)
			}
		}
		// And the ordering, live: the seeded one is the only active item in this
		// project and the earlier todos check left a done one behind it.
		done := false
		for _, a := range final.todos {
			if a.Status == "done" {
				done = true
				continue
			}
			if done {
				t.Fatalf("%q (%s) is listed below a finished todo", a.Title, a.Status)
			}
		}
		if final.todos[0].Title != env.todo {
			t.Fatalf("the queue opens on %q, not the one item in flight", final.todos[0].Title)
		}
		if env.todoOwner != "" && todoOwner(final.todos[0]) != env.todoOwner {
			t.Fatalf("the todo came back without its owner: %q", final.todos[0].Body)
		}
	}
	if env.roomTodo != "" {
		// What the screen cannot say: the panel is the room's own list and not
		// the whole queue drawn beside a room. Every item in it carries this
		// room, which is the narrowing the node did - a client that asked for
		// the type and the kind and forgot the room passes the wait above,
		// because the seeded item is in the whole queue too.
		if len(final.roomTodos) == 0 {
			t.Fatal("the room view never got the room's todos")
		}
		for _, a := range final.roomTodos {
			if todoRoomOf(a) != env.room {
				t.Fatalf("%q is in the panel for %s and was raised in %q",
					a.Title, env.room, todoRoomOf(a))
			}
		}
		// And at eighty columns, which is the width the failure lived at: the
		// last thing the drive did was resize to 80x24 and go back to the room,
		// so this is the screen somebody on the commonest terminal there is would
		// be looking at. The panel was drawn at a quarter of the terminal, which
		// left the conversation too narrow at 80 and took the whole pane away -
		// so there was a plan, and nobody in a terminal could see it.
		//
		// It is read out of the panel's own column and not off the screen. Raising
		// a todo from a room posts "raised a todo: <title>" into that room, so the
		// title is in the stream whether or not the panel drew anything, and a
		// check that looked for the words anywhere would pass against a client
		// with no panel in it at all.
		screen := final.View()
		panel := panelColumn(screen)
		if panelHead(panel) == "" {
			t.Fatalf("the room's plan pane is not on an 80-column screen:\n%s", screen)
		}
		if !panelSays(panel, env.roomTodo) {
			t.Fatalf("the plan pane at 80 columns does not carry %q, it carries %q:\n%s",
				env.roomTodo, panel, screen)
		}
		if env.todoOwner != "" {
			// Who is carrying it, on the item's own row: the status word and then
			// the name, which for this one is read off the body's OWNER line.
			carried := false
			for _, line := range panel {
				if strings.HasSuffix(line, " "+env.todoOwner) {
					carried = true
				}
			}
			if !carried {
				t.Fatalf("no row of the plan pane says %s is carrying anything: %q",
					env.todoOwner, panel)
			}
		}
	}
	if env.task != "" {
		found := false
		for _, task := range final.tasks {
			if strings.Contains(task.ArtifactTitle, env.task) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the seeded task %q is not in the inbox the client read", env.task)
		}
	}
	if final.metrics == nil {
		t.Fatal("the metrics view never got any metrics")
	}
	if len(final.tl) == 0 {
		t.Fatal("the timeline never got any activity")
	}
}

// The guard itself, since the live drive rests on it: text that was only ever
// typed into the box does not count as text the client drew.
func TestOutsideTheBoxIgnoresWhatWasOnlyTyped(t *testing.T) {
	const said = "tui-said-42"
	typed := []byte("\x1b[0mgeneral> " + said)
	if outsideTheBox(typed, said) {
		t.Fatal("the box's own echo was counted as the message having been drawn")
	}
	streamed := append(typed, []byte("\r\n12:00 ABCDEFGH         "+said)...)
	if !outsideTheBox(streamed, said) {
		t.Fatal("the message in the stream was not recognised")
	}
}

// A token the node refuses, against the live node: the error is on the status
// line and the client is still running.
func TestLiveABadTokenIsRefusedClearly(t *testing.T) {
	env := live(t)

	m := New(NewClient(env.url, "definitely-not-a-token"), monoTheme())
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	watch(tm).waitFor(t, "the refusal", "token refused")
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(15*time.Second))
}
