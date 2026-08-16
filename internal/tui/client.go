// Package tui is the terminal client to a flowy node.
//
// It is another client of the same /api surface the console and the MCP server
// use, and it adds nothing to the server: every view here is one of the
// endpoints Phase 1 through Phase 8 already answer, read with a bearer token
// and rendered for a terminal instead of a browser. That is deliberate. A
// terminal client that needed its own endpoints would be a second permission
// filter, and the whole claim of this node is that there is one.
package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is the node's HTTP API as the TUI sees it.
//
// Two http.Clients rather than one: the long poll blocks on the server for up
// to the wait window, and a timeout short enough to keep an ordinary read
// honest would cut the watcher off mid-window every time.
type Client struct {
	base  string
	token string
	http  *http.Client
	poll  *http.Client
}

// APIError carries the status, because 401 and 404 mean different things to a
// view: the first is a token to fix, the second is a row that is not there or
// not yours, and the TUI says so rather than showing an empty pane.
type APIError struct {
	Status int
	Msg    string
}

func (e *APIError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Msg)
}

// Unauthorized reports whether an error is the node refusing the token. It is
// what the status line reads to say "bad token" rather than "cannot reach the
// node", which are two different things to go and fix.
func Unauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized ||
		apiErr.Status == http.StatusForbidden)
}

// NewClient builds a client against a node's base URL.
func NewClient(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 20 * time.Second},
		// Comfortably past the node's 25s wait window, so a poll that runs the
		// whole window comes back as "nothing said" rather than as a timeout.
		poll: &http.Client{Timeout: 45 * time.Second},
	}
}

// Base is the node this client talks to, for the status line.
func (c *Client) Base() string { return c.base }

// maxBody caps what a single response may be read into. A terminal renders a
// screen at a time and a node that answered with a gigabyte would only take the
// client down with it.
const maxBody = 8 << 20

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Msg: errorMessage(raw, resp.Status)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// Not JSON at all is a proxy's error page standing in front of the
		// node. Reporting the parse error would name a byte offset and nothing
		// about what happened, so the first of the body goes in the message.
		return &APIError{Status: resp.StatusCode, Msg: snippet(raw)}
	}
	return nil
}

// errorMessage reads the node's {"error": "..."} out of a failed response, and
// falls back to the status line when the body is not that.
func errorMessage(raw []byte, status string) string {
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.Error != "" {
		return body.Error
	}
	if text := snippet(raw); text != "" {
		return text
	}
	return status
}

// snippet is as much of a body as belongs on one status line.
func snippet(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 160 {
		return text[:160] + "..."
	}
	return text
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, c.http, http.MethodGet, path, nil, out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, c.http, http.MethodPost, path, body, out)
}

// ---------------------------------------------------------------- the types
//
// These mirror what the handlers write, field for field, so a rename on the
// server side shows up here as a field that stops being populated rather than
// as a view that quietly renders nothing.

// Whoami is the (user, agent, project) triple the token resolves to, and what
// the node knows about the project half of it.
//
// ProjectFixture is why the extra fields are here at all: the project a token
// writes into is decided by the token and shown nowhere, which is how a day of
// real work was filed into a fixture project without anybody seeing it. The
// status line is the surface that can say so on every screen, so the answer it
// reads has to carry it.
type Whoami struct {
	User            string `json:"user"`
	Agent           string `json:"agent,omitempty"`
	AgentKind       string `json:"agent_kind,omitempty"`
	Project         string `json:"project,omitempty"`
	Operator        bool   `json:"operator,omitempty"`
	ProjectFixture  bool   `json:"project_fixture,omitempty"`
	ProjectDeclared bool   `json:"project_declared,omitempty"`
	ProjectOrigin   string `json:"project_origin,omitempty"`
}

// Event is one row of the append-only log. A chat message is one of these.
type Event struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Project  *string  `json:"project"`
	Room     string   `json:"room"`
	Thread   string   `json:"thread"`
	Parents  []string `json:"parents"`
	Actor    string   `json:"actor"`
	Artifact string   `json:"artifact"`
	SeqHLC   int64    `json:"seq_hlc"`
	Node     string   `json:"node"`
	Body     string   `json:"body"`
	Meta     struct {
		ActorKind string `json:"actor_kind"`
		ActorUser string `json:"actor_user"`
	} `json:"meta"`
	Created time.Time `json:"created"`
}

// IsAgent reads the speaker's kind off what the node stamped the message with,
// rather than guessing it from the actor id.
func (e *Event) IsAgent() bool { return e.Meta.ActorKind == "agent" }

// ChatPage is what a room read or a long poll answers with.
type ChatPage struct {
	Room   string   `json:"room"`
	Events []*Event `json:"events"`
	Since  int64    `json:"since"`
	Cursor int64    `json:"cursor"`
}

// Artifact is a memory, a bug, a feature, a note or an announcement.
type Artifact struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Kind       string          `json:"kind,omitempty"`
	Project    *string         `json:"project"`
	OwnerUser  string          `json:"owner_user"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	Discovery  string          `json:"discovery"`
	Status     string          `json:"status"`
	Severity   string          `json:"severity"`
	Tags       []string        `json:"tags"`
	UserTags   []string        `json:"user_tags"`
	Related    []string        `json:"related"`
	Visibility string          `json:"visibility"`
	FilePath   string          `json:"file_path"`
	Fields     json.RawMessage `json:"fields,omitempty"`
	HLC        int64           `json:"hlc"`
	Node       string          `json:"node"`
	Tombstone  bool            `json:"tombstone"`
	Created    time.Time       `json:"created"`
	Updated    time.Time       `json:"updated"`
}

// Scope is where an artifact lives, said the way a badge says it.
func (a *Artifact) Scope() string {
	if a.Visibility == "personal" || a.Project == nil {
		return "personal"
	}
	return *a.Project
}

// Task is one handoff: an artifact, the two people it is between, the thread
// they talk in and where it got to.
type Task struct {
	ID            string `json:"id"`
	Artifact      string `json:"artifact"`
	FromUser      string `json:"from_user"`
	ToUser        string `json:"to_user"`
	Project       string `json:"project,omitempty"`
	State         string `json:"state"`
	AssigneeAgent string `json:"assignee_agent,omitempty"`
	Thread        string `json:"thread"`
	HLC           int64  `json:"hlc"`
	Node          string `json:"node"`
	ArtifactTitle string `json:"artifact_title,omitempty"`
	ArtifactType  string `json:"artifact_type,omitempty"`
}

// TaskMove is what /delegate and /state answer with.
type TaskMove struct {
	Task  *Task  `json:"task"`
	Event *Event `json:"event"`
}

// History is an artifact's status trail. Next is where the workflow allows it
// to go from here, so the TUI draws the choices from the node rather than
// keeping a second copy of the rules.
type History struct {
	Artifact string   `json:"artifact"`
	Status   string   `json:"status"`
	Next     []string `json:"next"`
	Events   []*Event `json:"events"`
}

// StatusMove is what a lifecycle transition answers with.
type StatusMove struct {
	Artifact *Artifact `json:"artifact"`
	Event    *Event    `json:"event"`
}

// ActivityItem is one line of the timeline: a turn, a log line, a message or a
// steer.
type ActivityItem struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Type      string   `json:"type"`
	Actor     string   `json:"actor"`
	ActorKind string   `json:"actor_kind,omitempty"`
	ActorUser string   `json:"actor_user,omitempty"`
	Project   *string  `json:"project"`
	Room      string   `json:"room,omitempty"`
	Thread    string   `json:"thread,omitempty"`
	Artifact  string   `json:"artifact,omitempty"`
	Parents   []string `json:"parents"`
	Body      string   `json:"body"`
	Trace     string   `json:"trace,omitempty"`
	SeqHLC    int64    `json:"seq_hlc"`
	Node      string   `json:"node"`
	Created   string   `json:"created"`
}

// ActivityPage is a page of the timeline.
type ActivityPage struct {
	Items  []*ActivityItem `json:"items"`
	Since  int64           `json:"since"`
	Cursor int64           `json:"cursor"`
	Query  string          `json:"query"`
}

// Availability is on every metric group: whether it was measured, and when it
// was not, why. A pane renders the reason wherever it would otherwise render a
// zero, because "0 artifacts" and "we could not read the artifacts" are
// different sentences.
type Availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Measured  string `json:"measured,omitempty"`
}

// MetricScope is whose numbers came back.
type MetricScope struct {
	User     string `json:"user,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Project  string `json:"project,omitempty"`
	Operator bool   `json:"operator"`
	All      bool   `json:"all"`
	Key      string `json:"key"`
}

// NodeGroup is the process: how long it has been up, and what it is sitting on.
type NodeGroup struct {
	Availability
	UptimeS float64 `json:"uptime_s"`
	Build   string  `json:"build"`
	DB      *struct {
		Availability
		Up        bool    `json:"up"`
		Engine    string  `json:"engine"`
		LatencyMS float64 `json:"latency_ms"`
		HLC       int64   `json:"hlc"`
	} `json:"db"`
	Pool *struct {
		Availability
		InUse   int `json:"in_use"`
		Idle    int `json:"idle"`
		Open    int `json:"open"`
		MaxOpen int `json:"max_open"`
	} `json:"pool"`
	CPU *struct {
		Availability
		CoreShare float64 `json:"core_share"`
		Cores     int     `json:"cores"`
	} `json:"cpu"`
	Memory *struct {
		Availability
		RSSBytes int64 `json:"rss_bytes"`
	} `json:"memory"`
	Traces *struct {
		Availability
		Kept    int `json:"kept"`
		Dropped int `json:"dropped"`
	} `json:"traces"`
}

// CorpusGroup is what is stored.
type CorpusGroup struct {
	Availability
	Artifacts int            `json:"artifacts"`
	Events    int            `json:"events"`
	ByType    map[string]int `json:"by_type"`
	ByScope   map[string]int `json:"by_scope"`
	ByProject map[string]int `json:"by_project"`
	Growth    struct {
		Artifacts24h int `json:"artifacts_24h"`
		Artifacts7d  int `json:"artifacts_7d"`
		Events24h    int `json:"events_24h"`
	} `json:"growth"`
	Storage struct {
		Availability
		TotalBytes int64 `json:"total_bytes"`
	} `json:"storage"`
}

// PeerMetrics is one federation peer's cursors and counts.
type PeerMetrics struct {
	Peer         string  `json:"peer"`
	PullCursor   int64   `json:"pull_cursor"`
	PushedCursor int64   `json:"pushed_cursor"`
	LastSeenAgeS float64 `json:"last_seen_age_s"`
	PendingPush  int     `json:"pending_push"`
	Conflicts    int     `json:"conflicts"`
	Refused      int     `json:"refused"`
	Applied      int     `json:"applied"`
}

// SyncGroup is replication.
type SyncGroup struct {
	Availability
	Peers          []PeerMetrics `json:"peers"`
	LocalHWM       int64         `json:"local_hwm"`
	OfflineQueue   int           `json:"offline_queue"`
	ConflictsTotal int           `json:"conflicts_total"`
}

// DayCount is one bar of the messages-by-day sparkline.
type DayCount struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

// CollabGroup is who is talking to whom.
type CollabGroup struct {
	Availability
	Messages24h      int            `json:"messages_24h"`
	MessagesByDay    []DayCount     `json:"messages_by_day"`
	TasksByState     map[string]int `json:"tasks_by_state"`
	OpenTodos        int            `json:"open_todos"`
	ActiveRooms24h   int            `json:"active_rooms_24h"`
	ActiveUsers24h   int            `json:"active_users_24h"`
	ActiveAgents24h  int            `json:"active_agents_24h"`
	HandoffsInFlight int            `json:"handoffs_in_flight"`
}

// PermGroup is the permission surface: what is shared, and what was refused.
type PermGroup struct {
	Availability
	Grants             int            `json:"grants"`
	ArtifactShares     int            `json:"artifact_shares"`
	CrossProjectGrants int            `json:"cross_project_grants"`
	TombstonedGrants   int            `json:"tombstoned_grants"`
	Denied24h          int            `json:"denied_24h"`
	DeniedByStatus     map[string]int `json:"denied_by_status"`
}

// Anomaly is one series' verdict, with what the verdict rests on.
type Anomaly struct {
	Series   string  `json:"series"`
	Verdict  string  `json:"verdict"`
	Latest   float64 `json:"latest"`
	Baseline float64 `json:"baseline"`
	Sigma    float64 `json:"sigma"`
	Z        float64 `json:"z"`
	Samples  int     `json:"samples"`
	Required int     `json:"required"`
	Reason   string  `json:"reason,omitempty"`
}

// AnomaliesGroup is the verdicts, and how many refused to give one.
type AnomaliesGroup struct {
	Availability
	MinSamples   int       `json:"min_samples"`
	Series       []Anomaly `json:"series"`
	Unusual      int       `json:"unusual"`
	Insufficient int       `json:"insufficient"`
	Basis        string    `json:"basis"`
}

// Metrics is GET /api/metrics, scope-filtered by the token.
type Metrics struct {
	Node      string      `json:"node"`
	Version   string      `json:"version"`
	Generated string      `json:"generated"`
	Scope     MetricScope `json:"scope"`
	Groups    struct {
		Node        *NodeGroup      `json:"node"`
		Corpus      *CorpusGroup    `json:"corpus"`
		Sync        *SyncGroup      `json:"sync"`
		Collab      *CollabGroup    `json:"collaboration"`
		Permissions *PermGroup      `json:"permissions"`
		Anomalies   *AnomaliesGroup `json:"anomalies"`
	} `json:"groups"`
}

// AnnouncementFields is the scope, the resource and the mode, read out of the
// jsonb the node signs as part of the row.
type AnnouncementFields struct {
	Scope      string `json:"scope"`
	Resource   string `json:"resource,omitempty"`
	Mode       string `json:"mode,omitempty"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

// Announcement is an artifact of type announcement, with its fields decoded.
type Announcement struct {
	Artifact
	Decoded AnnouncementFields `json:"-"`
}

// ------------------------------------------------------------- the endpoints

// Whoami asks the node who this token is.
func (c *Client) Whoami(ctx context.Context) (*Whoami, error) {
	var out Whoami
	return &out, c.get(ctx, "/api/whoami", &out)
}

// Room reads a room from a cursor, exclusive.
func (c *Client) Room(ctx context.Context, room string, since int64) (*ChatPage, error) {
	var out ChatPage
	return &out, c.get(ctx, "/api/chat/"+url.PathEscape(room)+"?since="+
		strconv.FormatInt(since, 10), &out)
}

// Wait is the watcher: it blocks on the server for up to window seconds and
// answers with whatever landed, or with nothing.
//
// It is the one call the TUI makes that is allowed to take a long time, and it
// is why the whole of this client is called from tea.Cmds on their own
// goroutines: a long poll on the update loop would be a UI that stops redrawing
// for twenty-five seconds every time a room goes quiet.
func (c *Client) Wait(ctx context.Context, room string, cursor int64, window int) (*ChatPage, error) {
	path := fmt.Sprintf("/api/chat/%s/wait?cursor=%d&window=%d",
		url.PathEscape(room), cursor, window)
	var out ChatPage
	return &out, c.do(ctx, c.poll, http.MethodGet, path, nil, &out)
}

// Say posts a message into a room as whoever the token is.
func (c *Client) Say(ctx context.Context, room, body string, parents []string, thread string) (*Event, error) {
	req := map[string]any{"body": body, "parents": parents}
	if thread != "" {
		req["thread"] = thread
	}
	var out Event
	return &out, c.post(ctx, "/api/chat/"+url.PathEscape(room)+"/say", req, &out)
}

// Thread reads one thread of the log, whichever room it was said in.
func (c *Client) Thread(ctx context.Context, thread string) ([]*Event, error) {
	var out struct {
		Events []*Event `json:"events"`
	}
	err := c.get(ctx, "/api/events?thread="+url.QueryEscape(thread), &out)
	return out.Events, err
}

// Tasks is the work waiting for this principal.
func (c *Client) Tasks(ctx context.Context, state string) ([]*Task, error) {
	path := "/api/inbox/tasks"
	if state != "" {
		path += "?state=" + url.QueryEscape(state)
	}
	var out struct {
		Tasks []*Task `json:"tasks"`
	}
	err := c.get(ctx, path, &out)
	return out.Tasks, err
}

// Inbox is every chat message this principal may see and did not write.
func (c *Client) Inbox(ctx context.Context, since int64) (*ChatPage, error) {
	var out ChatPage
	return &out, c.get(ctx, "/api/inbox?since="+strconv.FormatInt(since, 10), &out)
}

// Delegate hands a task to the assignee's agent. Only the assignee may.
func (c *Client) Delegate(ctx context.Context, id, agent string) (*TaskMove, error) {
	req := map[string]any{}
	if agent != "" {
		req["agent"] = agent
	}
	var out TaskMove
	return &out, c.post(ctx, "/api/task/"+url.PathEscape(id)+"/delegate", req, &out)
}

// TaskState moves a task to open, delegated or done.
func (c *Client) TaskState(ctx context.Context, id, state string) (*TaskMove, error) {
	var out TaskMove
	return &out, c.post(ctx, "/api/task/"+url.PathEscape(id)+"/state",
		map[string]any{"state": state}, &out)
}

// Artifact reads one artifact, or 404 when it is missing or out of reach.
func (c *Client) Artifact(ctx context.Context, id string) (*Artifact, error) {
	var out Artifact
	return &out, c.get(ctx, "/api/artifact/"+url.PathEscape(id), &out)
}

// History is an artifact's status trail and where it may go next.
func (c *Client) History(ctx context.Context, id string) (*History, error) {
	var out History
	return &out, c.get(ctx, "/api/artifact/"+url.PathEscape(id)+"/history", &out)
}

// SetStatus moves an artifact's lifecycle status.
func (c *Client) SetStatus(ctx context.Context, id, status string) (*StatusMove, error) {
	var out StatusMove
	return &out, c.post(ctx, "/api/artifact/"+url.PathEscape(id)+"/status",
		map[string]any{"status": status}, &out)
}

// Artifacts lists what the principal may read, optionally narrowed by type.
func (c *Client) Artifacts(ctx context.Context, artifactType string, limit int) ([]*Artifact, error) {
	query := url.Values{}
	if artifactType != "" {
		query.Set("type", artifactType)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Artifacts []*Artifact `json:"artifacts"`
	}
	err := c.get(ctx, "/api/artifacts?"+query.Encode(), &out)
	return out.Artifacts, err
}

// Search ranks the artifacts the principal may read against a free text query.
func (c *Client) Search(ctx context.Context, query, artifactType string) ([]*Artifact, error) {
	values := url.Values{"q": {query}}
	if artifactType != "" {
		values.Set("type", artifactType)
	}
	var out struct {
		Artifacts []*Artifact `json:"artifacts"`
	}
	err := c.get(ctx, "/api/search?"+values.Encode(), &out)
	return out.Artifacts, err
}

// WriteArtifact creates or replaces an artifact.
//
// visibility is passed through as the caller gave it and is never filled in
// here. Personal is the default the node applies to a memory, and a client that
// quietly sent "project" because the field was empty would be a client that
// promotes somebody's private note the first time they edit it.
func (c *Client) WriteArtifact(ctx context.Context, art *Artifact) (*Artifact, error) {
	req := map[string]any{
		"type":  art.Type,
		"title": art.Title,
		"body":  art.Body,
	}
	if art.ID != "" {
		req["id"] = art.ID
	}
	if art.Kind != "" {
		req["kind"] = art.Kind
	}
	if art.Visibility != "" {
		req["visibility"] = art.Visibility
	}
	if len(art.UserTags) > 0 {
		req["user_tags"] = art.UserTags
	}
	if art.Discovery != "" {
		req["discovery"] = art.Discovery
	}
	var out Artifact
	return &out, c.post(ctx, "/api/artifacts", req, &out)
}

// Activity reads the timeline: turns, run logs, chat and steers.
func (c *Client) Activity(ctx context.Context, query, kind string, limit int) (*ActivityPage, error) {
	values := url.Values{}
	if query != "" {
		values.Set("q", query)
	}
	if kind != "" {
		values.Set("kind", kind)
	}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	var out ActivityPage
	return &out, c.get(ctx, "/api/activity?"+values.Encode(), &out)
}

// PostActivity says something into the timeline: into a room, or into a run's
// thread.
func (c *Client) PostActivity(ctx context.Context, kind, body, room, thread string) (*ActivityItem, error) {
	req := map[string]any{"kind": kind, "body": body}
	if room != "" {
		req["room"] = room
	}
	if thread != "" {
		req["thread"] = thread
	}
	var out ActivityItem
	return &out, c.post(ctx, "/api/activity", req, &out)
}

// Metrics is the whole observability set, scope-filtered by the token. all asks
// for the node's own view, and is answered that way only for an operator.
func (c *Client) Metrics(ctx context.Context, all bool) (*Metrics, error) {
	path := "/api/metrics"
	if all {
		path += "?scope=all"
	}
	var out Metrics
	return &out, c.get(ctx, path, &out)
}

// Announcements is what the banner reads: the ones still active that this token
// may see. The node decides both, which is why the banner has no filter.
func (c *Client) Announcements(ctx context.Context) ([]*Announcement, error) {
	var out struct {
		Announcements []*Announcement `json:"announcements"`
	}
	if err := c.get(ctx, "/api/announcements", &out); err != nil {
		return nil, err
	}
	for _, a := range out.Announcements {
		if len(a.Fields) > 0 {
			_ = json.Unmarshal(a.Fields, &a.Decoded)
		}
	}
	return out.Announcements, nil
}

// Announce posts an announcement. Scope federation is refused to anything but a
// system or monitor agent, and the refusal comes back as a 403 the view shows.
func (c *Client) Announce(ctx context.Context, scope, severity, title, body string) (*Announcement, error) {
	var out Announcement
	err := c.post(ctx, "/api/announcements", map[string]any{
		"scope": scope, "severity": severity, "title": title, "body": body,
	}, &out)
	if err == nil && len(out.Fields) > 0 {
		_ = json.Unmarshal(out.Fields, &out.Decoded)
	}
	return &out, err
}

// Ack says this principal has seen an announcement and is out of its way.
func (c *Client) Ack(ctx context.Context, id string) error {
	return c.post(ctx, "/api/announcement/"+url.PathEscape(id)+"/ack", map[string]any{}, nil)
}
