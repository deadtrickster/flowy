package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// Spans: what this node saw itself do, kept locally.
//
// The store is the tracer's recorder - see otel.Recorder - so a span ends in
// exactly one place and lands here whatever opened it: an HTTP handler, an MCP
// tool, a merge. Reading them back goes through a permission filter like every
// other read, because a span names a principal, a project and sometimes an
// artifact, and a waterfall of somebody else's work is somebody else's work.

// Span is one recorded operation, as the API and the console read it.
type Span struct {
	SpanID     string            `json:"span_id"`
	TraceID    string            `json:"trace_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Node       string            `json:"node"`
	Actor      string            `json:"actor,omitempty"`
	User       string            `json:"user,omitempty"`
	Project    string            `json:"project,omitempty"`
	Artifact   string            `json:"artifact,omitempty"`
	Status     string            `json:"status,omitempty"`
	Started    time.Time         `json:"started"`
	Ended      time.Time         `json:"ended"`
	DurationUS int64             `json:"duration_us"`
	Attrs      map[string]string `json:"attrs,omitempty"`
}

// spanColumns is the read list, in the order scanSpan expects.
const spanColumns = `s.span_id, s.trace_id, s.parent_id, s.name, s.kind, s.node, s.actor,
	s.user_id, s.project, s.artifact, s.status, s.started, s.ended, s.duration_us, s.attrs`

func scanSpan(sc scanner) (*Span, error) {
	var (
		s                              Span
		parent, name, kind, nodeCol    sql.NullString
		actor, user, project, artifact sql.NullString
		status                         sql.NullString
		started, ended                 sql.NullTime
		duration                       sql.NullInt64
		attrs                          []byte
	)
	if err := sc.Scan(&s.SpanID, &s.TraceID, &parent, &name, &kind, &nodeCol, &actor,
		&user, &project, &artifact, &status, &started, &ended, &duration, &attrs); err != nil {
		return nil, err
	}
	s.ParentID, s.Name, s.Kind, s.Node = parent.String, name.String, kind.String, nodeCol.String
	s.Actor, s.User, s.Project = actor.String, user.String, project.String
	s.Artifact, s.Status = artifact.String, status.String
	s.Started, s.Ended, s.DurationUS = started.Time, ended.Time, duration.Int64
	if len(attrs) > 0 {
		var into map[string]string
		if err := json.Unmarshal(attrs, &into); err == nil {
			s.Attrs = into
		}
	}
	return &s, nil
}

// RecordSpan writes an ended span. It is what makes *DB an otel.Recorder.
//
// ON CONFLICT DO NOTHING, because one span id is one span: the spans that stand
// for a replicated row derive their id from that row's id (see DeliverSpanID),
// so applying the same delta twice records the delivery once rather than twice.
func (d *DB) RecordSpan(ctx context.Context, s *otel.Span) error {
	if s == nil || s.SpanID == "" || s.TraceID == "" {
		return nil
	}
	var attrs any
	if a := s.Attrs(); len(a) > 0 {
		raw, err := json.Marshal(a)
		if err == nil {
			attrs = raw
		}
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO spans (span_id, trace_id, parent_id, name, kind, node, actor, user_id,
		                    project, artifact, status, started, ended, duration_us, attrs)
		 VALUES ($1, $2, nullif($3, ''), $4, $5, $6, nullif($7, ''), nullif($8, ''),
		         nullif($9, ''), nullif($10, ''), nullif($11, ''), $12, $13, $14, $15)
		 ON CONFLICT (span_id) DO NOTHING`,
		s.SpanID, s.TraceID, s.ParentID, s.Name, s.Kind, s.Node, s.Actor, s.User,
		s.Project, s.Artifact, s.Status, s.Started, s.Ended,
		s.Duration().Microseconds(), attrs)
	if err != nil {
		return fmt.Errorf("store: record span: %w", err)
	}
	return nil
}

// DeliverSpanID is the span id that stands for this node having taken delivery
// of one replicated row.
//
// It is derived from the row's id rather than minted, so a sync that is run
// twice - or a page that is offered again because something after it was
// refused - records one delivery and not one per attempt. The suffix keeps it
// from colliding with any other derived id over the same row.
func DeliverSpanID(rowID string) string {
	sum := sha256.Sum256([]byte("flowy.deliver|" + rowID))
	return hex.EncodeToString(sum[:8])
}

// SpanFilterSQL is the read rule for spans, as a boolean fragment over the
// table aliased as alias.
//
// A span is its principal's first: the user it was run for, or the agent that
// ran it. After that it is the project's, and then only as far as the artifact
// it names - a span that names an artifact you may not read is a span about
// work you may not see, so it inherits that artifact's floor exactly as an
// event naming one does. A span that names none stops at the project, which is
// a name and a duration and nothing that was said or stored.
//
// The operator's ?scope=all is the same escape hatch it is everywhere else, and
// like everywhere else it is a local view of a local table.
func SpanFilterSQL(p *Principal, alias string, a *args, scopeAll bool) string {
	if p == nil {
		return "FALSE"
	}
	if scopeAll && p.Operator {
		return "TRUE"
	}
	user := a.next(p.UserID)
	agent := a.next(p.AgentID)
	project := a.next(p.Project)

	return strings.NewReplacer("{a}", alias, "{user}", user, "{agent}", agent, "{project}", project).Replace(
		`(CASE WHEN coalesce({a}.user_id, '') <> '' AND {a}.user_id = {user} AND {user} <> ''
		       THEN TRUE
		       WHEN coalesce({a}.actor, '') <> '' AND {a}.actor = {agent} AND {agent} <> ''
		       THEN TRUE
		       WHEN coalesce({a}.project, '') <> '' AND {a}.project = {project} AND {project} <> ''
		       THEN coalesce({a}.artifact, '') = ''
		         OR EXISTS (SELECT 1 FROM artifacts par
		                     WHERE par.id = {a}.artifact
		                       AND ` + artifactReachSQL("par", user, project) + `)
		       ELSE FALSE
		  END)`)
}

// SpanQuery narrows a read of the spans. Trace reads one trace - which is what
// a waterfall is - and the rest page the recent ones.
type SpanQuery struct {
	Trace    string
	Name     string
	Since    time.Time
	ScopeAll bool
	Limit    int
}

func (q SpanQuery) limit() int { return clampLimit(q.Limit) }

// ListSpans returns the spans p may read, oldest first inside a trace and
// newest first across traces - which is the order each of the two views wants:
// a waterfall reads down, a list of recent traces reads from the top.
func (d *DB) ListSpans(ctx context.Context, p *Principal, q SpanQuery) ([]*Span, error) {
	a := &args{}
	filter := SpanFilterSQL(p, "s", a, q.ScopeAll)
	where := ""
	order := "s.started DESC, s.span_id DESC"
	if q.Trace != "" {
		where += " AND s.trace_id = " + a.next(q.Trace)
		order = "s.started ASC, s.span_id ASC"
	}
	if q.Name != "" {
		where += " AND s.name = " + a.next(q.Name)
	}
	if !q.Since.IsZero() {
		where += " AND s.started >= " + a.next(q.Since)
	}
	query := `SELECT ` + spanColumns + `
	            FROM spans s
	           WHERE ` + filter + where + `
	           ORDER BY ` + order + `
	           LIMIT ` + a.next(q.limit())

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: list spans: %w", err)
	}
	defer rows.Close()

	out := []*Span{}
	for rows.Next() {
		s, err := scanSpan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list spans: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list spans: %w", err)
	}
	return out, nil
}

// Trace is one trace as the console and the collector read it: its spans, the
// nodes that recorded them, and how long the whole thing took.
//
// Nodes is the point of it. A handoff assigned on one node and worked on
// another is one trace with spans recorded in two databases, and the only way
// to see that from one of them is to say which nodes are in the list - so a
// caller can tell "this is the whole trace" from "this is my half of it".
type Trace struct {
	TraceID    string    `json:"trace_id"`
	Spans      []*Span   `json:"spans"`
	Nodes      []string  `json:"nodes"`
	Root       string    `json:"root,omitempty"`
	Started    time.Time `json:"started"`
	Ended      time.Time `json:"ended"`
	DurationUS int64     `json:"duration_us"`
	Errors     int       `json:"errors"`
}

// ReadTrace assembles one trace out of the spans p may read.
func (d *DB) ReadTrace(ctx context.Context, p *Principal, traceID string, scopeAll bool) (*Trace, error) {
	spans, err := d.ListSpans(ctx, p, SpanQuery{Trace: traceID, ScopeAll: scopeAll, Limit: maxLimit})
	if err != nil {
		return nil, err
	}
	return AssembleTrace(traceID, spans), nil
}

// AssembleTrace puts a set of spans into one trace, in start order.
//
// It is a function rather than a method because the collector calls it with
// spans from two nodes: this node's, read out of the table, and a peer's, read
// over that peer's API. Reassembly is the same operation either way - sort by
// start, notice which nodes are represented, take the earliest span with no
// parent as the root - and doing it in one place is what makes a collected
// trace and a local one the same shape on the wire.
func AssembleTrace(traceID string, spans []*Span) *Trace {
	t := &Trace{TraceID: traceID, Spans: []*Span{}, Nodes: []string{}}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for _, s := range spans {
		if s == nil {
			continue
		}
		t.Spans = append(t.Spans, s)
		ids[s.SpanID] = true
		if s.Node != "" && !seen[s.Node] {
			seen[s.Node] = true
			t.Nodes = append(t.Nodes, s.Node)
		}
	}
	sort.SliceStable(t.Spans, func(i, j int) bool {
		if t.Spans[i].Started.Equal(t.Spans[j].Started) {
			return t.Spans[i].SpanID < t.Spans[j].SpanID
		}
		return t.Spans[i].Started.Before(t.Spans[j].Started)
	})
	sort.Strings(t.Nodes)

	for _, s := range t.Spans {
		if s.Status == otel.StatusError {
			t.Errors++
		}
		if t.Started.IsZero() || s.Started.Before(t.Started) {
			t.Started = s.Started
		}
		if s.Ended.After(t.Ended) {
			t.Ended = s.Ended
		}
		// The root is the first span whose parent is not in this set: on a
		// collected trace that is the span the work started with, and on a half
		// of one it is the earliest span that node holds.
		if t.Root == "" && (s.ParentID == "" || !ids[s.ParentID]) {
			t.Root = s.Name
		}
	}
	if !t.Started.IsZero() && t.Ended.After(t.Started) {
		t.DurationUS = t.Ended.Sub(t.Started).Microseconds()
	}
	return t
}

// TraceSummary is one line of the trace list: enough to choose a trace to open
// without reading every span in it.
type TraceSummary struct {
	TraceID    string    `json:"trace_id"`
	Root       string    `json:"root"`
	Spans      int       `json:"spans"`
	Nodes      []string  `json:"nodes"`
	Started    time.Time `json:"started"`
	DurationUS int64     `json:"duration_us"`
	Errors     int       `json:"errors"`
}

// ListTraces summarises the recent traces p may read.
//
// It reads spans and groups them here rather than grouping in SQL, because the
// filter is a per-span rule: a trace is not a row and has no principal of its
// own, so "which traces may I see" is exactly "which spans may I see, gathered
// up". A trace whose spans are half readable comes back as the half - which is
// the truth about what this principal may see, and is why the count of nodes is
// on the summary.
func (d *DB) ListTraces(ctx context.Context, p *Principal, q SpanQuery) ([]*TraceSummary, error) {
	// Enough spans to make a page of traces out of, capped like every other read.
	spans, err := d.ListSpans(ctx, p, SpanQuery{Since: q.Since, ScopeAll: q.ScopeAll, Limit: maxLimit})
	if err != nil {
		return nil, err
	}
	byTrace := map[string][]*Span{}
	order := []string{}
	for _, s := range spans {
		if _, ok := byTrace[s.TraceID]; !ok {
			order = append(order, s.TraceID)
		}
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}
	limit := q.limit()
	out := make([]*TraceSummary, 0, len(order))
	for _, id := range order {
		if len(out) >= limit {
			break
		}
		t := AssembleTrace(id, byTrace[id])
		out = append(out, &TraceSummary{
			TraceID: t.TraceID, Root: t.Root, Spans: len(t.Spans), Nodes: t.Nodes,
			Started: t.Started, DurationUS: t.DurationUS, Errors: t.Errors,
		})
	}
	return out, nil
}

// TraceOfThread is the trace a conversation belongs to, read off the meta of
// the events in it.
//
// This is the half of cross-node tracing that does not need a header. A handoff
// is assigned on one node and worked on another, and there is no request from
// the first to the second to carry a traceparent: what crosses is the rows. So
// the trace id rides the assignment's opening event in meta - inside the
// event's signature, so a relay cannot rewrite it - and the node that takes
// delivery reads it back out of the thread and continues the same trace.
//
// The events are read through the permission filter, so a thread the principal
// cannot see yields no trace rather than one they could then query.
func (d *DB) TraceOfThread(ctx context.Context, p *Principal, thread string) (string, error) {
	if thread == "" {
		return "", nil
	}
	events, err := d.ListEvents(ctx, p, EventQuery{Thread: thread, Limit: 50})
	if err != nil {
		return "", err
	}
	for _, e := range events {
		if id := TraceOfMeta(e.Meta); id != "" {
			return id, nil
		}
	}
	return "", nil
}

// TraceMetaKey is the meta key a trace id rides in. It is stripped off anything
// a client writes - see speakerStripped in the API - for the same reason the
// speaker keys are: correlation the node did not stamp is correlation somebody
// else chose, and a trace somebody can join is a trace they can pollute.
const TraceMetaKey = "trace"

// TraceOfMeta reads the trace id out of an event's meta, or "" when there is
// none and when what is there is not a trace id.
func TraceOfMeta(meta json.RawMessage) string {
	if len(meta) == 0 {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(meta, &fields); err != nil {
		return ""
	}
	raw, ok := fields[TraceMetaKey]
	if !ok {
		return ""
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return ""
	}
	if !otel.ValidTraceID(id) {
		return ""
	}
	return strings.ToLower(id)
}

// DeliveredTraces is what a merge just took delivery of: the trace id on each
// event in the delta that carries one, with the event and its thread, so the
// node can record one span per delivery.
type Delivered struct {
	TraceID  string
	EventID  string
	Thread   string
	Artifact string
	Project  string
	Actor    string
	Node     string
}

// DeliveredIn reads the deliveries out of a delta this node has just applied.
//
// It is deliberately read off the SyncSet rather than out of the database: what
// arrived is what was delivered, and a row that lost its merge was still
// delivered - the trace of the handoff should say the peer handed it over, not
// only that it was new here.
func DeliveredIn(in *SyncSet) []Delivered {
	if in == nil {
		return nil
	}
	var out []Delivered
	seen := map[string]bool{}
	for _, e := range in.Events {
		if e == nil {
			continue
		}
		id := TraceOfMeta(e.Meta)
		if id == "" || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		project := ""
		if e.Project != nil {
			project = *e.Project
		}
		out = append(out, Delivered{
			TraceID: id, EventID: e.ID, Thread: e.Thread, Artifact: e.Artifact,
			Project: project, Actor: e.Actor, Node: e.Node,
		})
	}
	return out
}

// ------------------------------------------------------------- merge stats

// RecordMergeStats writes what one merge did against one peer: how many rows it
// applied, how many it refused, and how many lost a merge to a row already here.
//
// The last of those is the conflict count, and it has to be written as it
// happens: last-writer-wins keeps no loser, so a conflict that is not counted
// at the merge cannot be counted afterwards by looking at the table.
func (d *DB) RecordMergeStats(ctx context.Context, peer string, applied, refused, conflicts int) error {
	if peer == "" {
		peer = "unnamed-peer"
	}
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO sync_stats (peer, conflicts, refused, applied, at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (peer) DO UPDATE
		    SET conflicts = sync_stats.conflicts + excluded.conflicts,
		        refused   = sync_stats.refused + excluded.refused,
		        applied   = sync_stats.applied + excluded.applied,
		        at        = now()`,
		peer, conflicts, refused, applied)
	if err != nil {
		return fmt.Errorf("store: record merge stats: %w", err)
	}
	return nil
}

// ------------------------------------------------------------- denials

// RecordDenial writes down one refusal: a 401 or a 403 under /api/.
//
// Nothing about the row that was refused is stored, deliberately. A denial log
// that records which artifact somebody was refused is a way to learn that the
// artifact exists, which is the one thing the permission filter is careful never
// to say. What is kept is who asked, what they asked of, and what they were
// told.
func (d *DB) RecordDenial(ctx context.Context, user, agent, method, route, reason string, status int) error {
	_, err := d.sql.ExecContext(ctx,
		`INSERT INTO access_denials (id, at, user_id, agent, status, method, route, reason)
		 VALUES ($1, now(), nullif($2, ''), nullif($3, ''), $4, $5, $6, $7)`,
		ulid.NewString(), user, agent, status, method, route, reason)
	if err != nil {
		return fmt.Errorf("store: record denial: %w", err)
	}
	return nil
}
