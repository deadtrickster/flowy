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
	status  int
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
		_ = json.NewEncoder(w).Encode(Whoami{User: "u1", Agent: "a1", Project: "pa"})
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

// Eight tabs of full labels are wider than the terminal this client is written
// for, so the bar drops to digits rather than dropping the last tab off the
// right: a view nobody can see the key for is a view nobody finds.
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
}

// ------------------------------------------------------------ the live gate
//
// These run against the gate's own `flowy serve`, driven headless through
// teatest: the same model the binary runs, fed the keystrokes a keyboard sends.
// They skip when there is no node to talk to, which is what keeps `go test
// ./...` runnable on its own.

type liveEnv struct {
	url, token, room, message, memory, task, report, asOf string
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
