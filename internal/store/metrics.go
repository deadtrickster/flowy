package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// The measurements behind GET /api/metrics.
//
// Two rules run through all of it, and they are the reason the shapes here have
// a `Reason` beside every number rather than only the number:
//
//   - every count is the caller's count. The corpus is the artifacts that
//     caller may read, the collaboration numbers are the events and tasks that
//     caller is party to, and the operator asking for the node gets the node.
//     That is not a nicety - a metric is an aggregate, and an aggregate over
//     rows somebody may not read tells them how many there are, which is the
//     one thing the permission filter is careful never to say. So each of these
//     puts the same filter every list uses into the same WHERE clause.
//   - a number that could not be measured is not zero. Each group carries
//     whether it is available and, when it is not, why - so a console shows
//     "could not read" where it would otherwise show a confident 0, and an
//     empty corpus and an unreadable one do not look the same.

// MetricWindow is the period the rate-shaped numbers are measured over, and it
// is named on the wire beside them: "messages" is not a number, "messages in
// the last 24 hours" is.
const MetricWindow = 24 * time.Hour

// metricDays is how many days of history the per-day series carries.
const metricDays = 7

// ChatEventType is what a chat message is written as. It is the store's
// constant rather than the API's, because the collaboration metrics count
// messages and the API writes them, and two spellings of "chat" would be a
// dashboard that quietly reports zero conversations.
const ChatEventType = "chat"

// CorpusMetrics is what this principal's half of the store holds.
type CorpusMetrics struct {
	Artifacts     int64            `json:"artifacts"`
	Events        int64            `json:"events"`
	ByType        map[string]int64 `json:"by_type"`
	ByScope       map[string]int64 `json:"by_scope"`
	ByProject     map[string]int64 `json:"by_project"`
	ByUser        map[string]int64 `json:"by_user"`
	NewArtifacts  int64            `json:"new_artifacts_24h"`
	NewEvents     int64            `json:"new_events_24h"`
	WeekArtifacts int64            `json:"new_artifacts_7d"`
}

// CorpusMetricsFor counts the artifacts and events this principal may read.
func (d *DB) CorpusMetricsFor(ctx context.Context, p *Principal, scopeAll bool) (*CorpusMetrics, error) {
	out := &CorpusMetrics{
		ByType: map[string]int64{}, ByScope: map[string]int64{},
		ByProject: map[string]int64{}, ByUser: map[string]int64{},
	}
	day := time.Now().Add(-MetricWindow)
	week := time.Now().Add(-metricDays * 24 * time.Hour)

	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	dayArg, weekArg := a.next(day), a.next(week)
	err := d.sql.QueryRowContext(ctx,
		`SELECT count(*),
		        sum(CASE WHEN ar.created >= `+dayArg+` THEN 1 ELSE 0 END),
		        sum(CASE WHEN ar.created >= `+weekArg+` THEN 1 ELSE 0 END)
		   FROM artifacts ar
		  WHERE coalesce(ar.tombstone, false) = false AND `+filter, a.vals...).
		Scan(&out.Artifacts, nullInt(&out.NewArtifacts), nullInt(&out.WeekArtifacts))
	if err != nil {
		return nil, fmt.Errorf("store: corpus metrics: %w", err)
	}

	for _, group := range []struct {
		column string
		into   map[string]int64
	}{
		{"coalesce(ar.type, '')", out.ByType},
		{"coalesce(ar.visibility, '')", out.ByScope},
		// A row with no project is personal, and it is counted under a label
		// that says so rather than under the empty string, which reads as a
		// missing value in every console that renders it.
		{"coalesce(ar.project, '(personal)')", out.ByProject},
		{"coalesce(ar.owner_user, '')", out.ByUser},
	} {
		if err := d.groupCount(ctx, p, scopeAll, group.column, group.into); err != nil {
			return nil, err
		}
	}

	ea := &args{}
	eventFilter := EventFilterSQL(p, "e", ea, scopeAll)
	eventDay := ea.next(day)
	err = d.sql.QueryRowContext(ctx,
		`SELECT count(*), sum(CASE WHEN e.created >= `+eventDay+` THEN 1 ELSE 0 END)
		   FROM events e WHERE `+eventFilter, ea.vals...).
		Scan(&out.Events, nullInt(&out.NewEvents))
	if err != nil {
		return nil, fmt.Errorf("store: corpus metrics: %w", err)
	}
	return out, nil
}

// groupCount fills into with "how many readable artifacts per value of column".
func (d *DB) groupCount(ctx context.Context, p *Principal, scopeAll bool, column string, into map[string]int64) error {
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+column+` AS bucket, count(*)
		   FROM artifacts ar
		  WHERE coalesce(ar.tombstone, false) = false AND `+filter+`
		  GROUP BY 1 ORDER BY 2 DESC`, a.vals...)
	if err != nil {
		return fmt.Errorf("store: corpus metrics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			bucket sql.NullString
			n      int64
		)
		if err := rows.Scan(&bucket, &n); err != nil {
			return fmt.Errorf("store: corpus metrics: %w", err)
		}
		key := bucket.String
		if key == "" {
			key = "(unset)"
		}
		into[key] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: corpus metrics: %w", err)
	}
	return nil
}

// IndexCoverage is how much of the readable corpus is findable, and by what.
//
// Embedded is zero on this node and says so rather than being left out: search
// here is Postgres full text, and there is no vector index until the store moves
// to SereneDB. Reporting "0 embedded of N" beside "N indexed for text" is the
// honest version of a coverage number - the alternative was a coverage
// percentage over an index that does not exist.
type IndexCoverage struct {
	Artifacts int64  `json:"artifacts"`
	TextIndex int64  `json:"text_indexed"`
	Embedded  int64  `json:"embedded"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// IndexCoverageFor counts how many readable artifacts carry a text index.
//
// artifacts.search is the one Postgres-specific column in the schema, and it is
// expected to go away when search becomes vector search. So this asks for it in
// a query of its own and treats a failure as "could not read" rather than as a
// zero: on a store without the column the number is unknown, and unknown is
// what it says.
func (d *DB) IndexCoverageFor(ctx context.Context, p *Principal, scopeAll bool) *IndexCoverage {
	out := &IndexCoverage{}
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, scopeAll)
	err := d.sql.QueryRowContext(ctx,
		`SELECT count(*), sum(CASE WHEN ar.search IS NOT NULL THEN 1 ELSE 0 END)
		   FROM artifacts ar
		  WHERE coalesce(ar.tombstone, false) = false AND `+filter, a.vals...).
		Scan(&out.Artifacts, nullInt(&out.TextIndex))
	if err != nil {
		out.Reason = "the text index column could not be read on this store"
		return out
	}
	out.Available = true
	return out
}

// StorageSize is what the store occupies on this node, per table.
//
// It is the operator's, and it is measured with a function that is Postgres the
// storage engine rather than Postgres the wire - so a store that does not
// implement it comes back unavailable with the reason, which is the correct
// answer for a SereneDB node rather than a zero that reads as an empty disk.
type StorageSize struct {
	Tables    map[string]int64 `json:"tables_bytes"`
	Total     int64            `json:"total_bytes"`
	Available bool             `json:"available"`
	Reason    string           `json:"reason,omitempty"`
}

// StorageSizes measures the spine tables and their indexes.
func (d *DB) StorageSizes(ctx context.Context) *StorageSize {
	out := &StorageSize{Tables: map[string]int64{}}
	tables := []string{"artifacts", "events", "tasks", "grants", "spans"}
	for _, t := range tables {
		var n int64
		// The table name comes from the fixed list above, never from a caller.
		err := d.sql.QueryRowContext(ctx, `SELECT pg_total_relation_size($1)`, t).Scan(&n)
		if err != nil {
			out.Reason = "this store does not report relation sizes over the wire"
			return out
		}
		out.Tables[t] = n
		out.Total += n
	}
	out.Available = true
	return out
}

// CollabMetrics is the working half: who is talking, what is assigned, what is
// still open.
type CollabMetrics struct {
	Messages      int64            `json:"messages_24h"`
	MessagesByDay []DayCount       `json:"messages_by_day"`
	Tasks         map[string]int64 `json:"tasks_by_state"`
	OpenTodos     int64            `json:"open_todos"`
	ActiveRooms   int64            `json:"active_rooms_24h"`
	ActiveUsers   int64            `json:"active_users_24h"`
	ActiveAgents  int64            `json:"active_agents_24h"`
	HandoffsOpen  int64            `json:"handoffs_in_flight"`
}

// DayCount is one day of a per-day series. Day is the UTC date the count is
// for, so a series read in one timezone and rendered in another is still the
// same series.
type DayCount struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

// CollabMetricsFor counts the conversation and the work this principal is in.
func (d *DB) CollabMetricsFor(ctx context.Context, p *Principal, scopeAll bool) (*CollabMetrics, error) {
	out := &CollabMetrics{Tasks: map[string]int64{}, MessagesByDay: []DayCount{}}
	since := time.Now().Add(-MetricWindow)

	a := &args{}
	filter := EventFilterSQL(p, "e", a, scopeAll)
	sinceArg := a.next(since)
	chatArg := a.next(ChatEventType)
	err := d.sql.QueryRowContext(ctx,
		`SELECT sum(CASE WHEN e.type = `+chatArg+` THEN 1 ELSE 0 END),
		        count(DISTINCT CASE WHEN coalesce(e.room, '') <> '' THEN e.room END),
		        count(DISTINCT CASE WHEN coalesce(e.actor, '') <> ''
		                             AND EXISTS (SELECT 1 FROM agents ag WHERE ag.id = e.actor)
		                            THEN e.actor END),
		        count(DISTINCT CASE WHEN coalesce(e.actor, '') <> ''
		                             AND NOT EXISTS (SELECT 1 FROM agents ag WHERE ag.id = e.actor)
		                            THEN e.actor END)
		   FROM events e
		  WHERE `+filter+` AND e.created >= `+sinceArg, a.vals...).
		Scan(nullInt(&out.Messages), &out.ActiveRooms, &out.ActiveAgents, &out.ActiveUsers)
	if err != nil {
		return nil, fmt.Errorf("store: collaboration metrics: %w", err)
	}

	byDay, err := d.messagesByDay(ctx, p, scopeAll)
	if err != nil {
		return nil, err
	}
	out.MessagesByDay = byDay

	// The operator's whole-node view is TRUE and takes no parameters, so the
	// filter is built only when it is the one that runs: building it and then
	// throwing the SQL away leaves its placeholders behind in the argument
	// list, and a statement handed two parameters it never mentions is an error
	// rather than a wider read.
	ta := &args{}
	taskFilter := "TRUE"
	if !(scopeAll && p != nil && p.Operator) {
		taskFilter = taskPartySQL(p, "t", ta)
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT coalesce(t.state, ''), count(*) FROM tasks t WHERE `+taskFilter+` GROUP BY 1`,
		ta.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: collaboration metrics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			state string
			n     int64
		)
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("store: collaboration metrics: %w", err)
		}
		if state == "" {
			state = "(unset)"
		}
		out.Tasks[state] = n
		if state == TaskOpen || state == TaskDelegated {
			out.HandoffsOpen += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: collaboration metrics: %w", err)
	}

	// Open work, in the memory tools' own words: a todo, a feature or a handoff
	// whose status is not done. It is the same query the todos tool runs, so the
	// number on the dashboard and the list an agent gets are the same thing.
	todos, err := d.ListArtifacts(ctx, p, ArtifactQuery{
		Type: "memory", Kinds: []string{"todo", "feature", "handoff"},
		NotStatus: "done", ScopeAll: scopeAll, Limit: maxLimit,
	})
	if err != nil {
		return nil, err
	}
	out.OpenTodos = int64(len(todos))
	return out, nil
}

// messagesByDay is the last week of chat, one count per UTC day.
//
// The buckets are built here and passed as parameters rather than asked of the
// database with date_trunc: truncating a timestamp to a day is a function of
// the engine and of its timezone setting, and this has to mean the same thing
// on a SereneDB node as on a Postgres one.
func (d *DB) messagesByDay(ctx context.Context, p *Principal, scopeAll bool) ([]DayCount, error) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -(metricDays - 1))

	a := &args{}
	filter := EventFilterSQL(p, "e", a, scopeAll)
	chatArg := a.next(ChatEventType)
	selects := ""
	days := make([]string, 0, metricDays)
	for i := 0; i < metricDays; i++ {
		from := start.AddDate(0, 0, i)
		to := from.AddDate(0, 0, 1)
		if i > 0 {
			selects += ", "
		}
		selects += "sum(CASE WHEN e.created >= " + a.next(from) + " AND e.created < " + a.next(to) +
			" THEN 1 ELSE 0 END)"
		days = append(days, from.Format("2006-01-02"))
	}

	counts := make([]sql.NullInt64, metricDays)
	dest := make([]any, metricDays)
	for i := range counts {
		dest[i] = &counts[i]
	}
	err := d.sql.QueryRowContext(ctx,
		`SELECT `+selects+` FROM events e WHERE `+filter+` AND e.type = `+chatArg, a.vals...).
		Scan(dest...)
	if err != nil {
		return nil, fmt.Errorf("store: messages by day: %w", err)
	}
	out := make([]DayCount, 0, metricDays)
	for i, day := range days {
		out = append(out, DayCount{Day: day, Count: counts[i].Int64})
	}
	return out, nil
}

// PermissionMetrics is the capability surface this principal is inside: the
// grants that touch them, the shares that cross a project boundary, and what
// this node refused.
type PermissionMetrics struct {
	Grants        int64            `json:"grants"`
	Shares        int64            `json:"artifact_shares"`
	CrossProject  int64            `json:"cross_project_grants"`
	Tombstoned    int64            `json:"tombstoned_grants"`
	Denials       int64            `json:"denied_24h"`
	DenialsByCode map[string]int64 `json:"denied_by_status"`
}

// PermissionMetricsFor counts the grants p is party to and the refusals p was
// given. The operator asking for the node gets the node's.
func (d *DB) PermissionMetricsFor(ctx context.Context, p *Principal, scopeAll bool) (*PermissionMetrics, error) {
	out := &PermissionMetrics{DenialsByCode: map[string]int64{}}

	a := &args{}
	filter := "TRUE"
	if !(scopeAll && p != nil && p.Operator) {
		filter = GrantFilterSQL(p, "g", a)
	}
	err := d.sql.QueryRowContext(ctx,
		`SELECT count(*),
		        sum(CASE WHEN g.artifact IS NOT NULL THEN 1 ELSE 0 END),
		        sum(CASE WHEN g.from_project <> g.to_project THEN 1 ELSE 0 END),
		        sum(CASE WHEN coalesce(g.tombstone, false) THEN 1 ELSE 0 END)
		   FROM grants g WHERE `+filter, a.vals...).
		Scan(&out.Grants, nullInt(&out.Shares), nullInt(&out.CrossProject), nullInt(&out.Tombstoned))
	if err != nil {
		return nil, fmt.Errorf("store: permission metrics: %w", err)
	}

	da := &args{}
	since := da.next(time.Now().Add(-MetricWindow))
	who := "TRUE"
	if !(scopeAll && p != nil && p.Operator) {
		// Your refusals are yours. A refusal names a route and a status and
		// nothing about the row that was refused, but how often somebody else
		// is being turned away is still their business and not yours.
		user := da.next(principalUser(p))
		agent := da.next(principalAgent(p))
		who = `(coalesce(dn.user_id, '') = ` + user + ` AND ` + user + ` <> ''
		     OR coalesce(dn.agent, '') = ` + agent + ` AND ` + agent + ` <> '')`
	}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT dn.status, count(*) FROM access_denials dn
		  WHERE dn.at >= `+since+` AND `+who+` GROUP BY 1`, da.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: permission metrics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			status sql.NullInt64
			n      int64
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("store: permission metrics: %w", err)
		}
		out.DenialsByCode[fmt.Sprintf("%d", status.Int64)] = n
		out.Denials += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: permission metrics: %w", err)
	}
	return out, nil
}

func principalUser(p *Principal) string {
	if p == nil {
		return ""
	}
	return p.UserID
}

func principalAgent(p *Principal) string {
	if p == nil {
		return ""
	}
	return p.AgentID
}

// PeerMetrics is one peer's replication state as this node knows it.
//
// PendingPush is measured: it is the rows here above the cursor this node last
// pushed past, which is what it still owes that peer. PendingPull is not, and
// says so - what a peer holds above our pull cursor is the peer's high water
// mark and cannot be read from this side without asking it. Reporting a zero
// there would say "we are up to date" on the strength of never having looked.
type PeerMetrics struct {
	Peer         string     `json:"peer"`
	PullCursor   int64      `json:"pull_cursor"`
	PushedCursor int64      `json:"pushed_cursor"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	LastSeenAgeS *int64     `json:"last_seen_age_s,omitempty"`
	PendingPush  int64      `json:"pending_push"`
	Conflicts    int64      `json:"conflicts"`
	Refused      int64      `json:"refused"`
	Applied      int64      `json:"applied"`
}

// SyncMetrics is the federation group: the peers, what is queued, and what the
// merges did.
type SyncMetrics struct {
	Peers        []PeerMetrics `json:"peers"`
	LocalHWM     int64         `json:"local_hwm"`
	OfflineQueue int64         `json:"offline_queue"`
	Conflicts    int64         `json:"conflicts_total"`
}

// SyncMetricsNode reads the node's replication state. It is the operator's
// view: a peer's cursor is not something one principal's token should reveal
// to another, which is the rule GET /api/peers already keeps.
func (d *DB) SyncMetricsNode(ctx context.Context) (*SyncMetrics, error) {
	out := &SyncMetrics{Peers: []PeerMetrics{}}

	// The highest reading this node holds anywhere: what a peer would have to
	// catch up to.
	err := d.sql.QueryRowContext(ctx,
		`SELECT coalesce(max(hwm), 0) FROM (
		     SELECT max(hlc) AS hwm FROM artifacts
		     UNION ALL SELECT max(seq_hlc) FROM events
		     UNION ALL SELECT max(hlc) FROM tasks
		     UNION ALL SELECT max(hlc) FROM grants) readings`).Scan(&out.LocalHWM)
	if err != nil {
		return nil, fmt.Errorf("store: sync metrics: %w", err)
	}
	if err := d.sql.QueryRowContext(ctx, `SELECT count(*) FROM sync_pending`).
		Scan(&out.OfflineQueue); err != nil {
		return nil, fmt.Errorf("store: sync metrics: %w", err)
	}

	stats := map[string][3]int64{}
	rows, err := d.sql.QueryContext(ctx,
		`SELECT peer, coalesce(conflicts, 0), coalesce(refused, 0), coalesce(applied, 0)
		   FROM sync_stats`)
	if err != nil {
		return nil, fmt.Errorf("store: sync metrics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			peer                        string
			conflicts, refused, applied int64
		)
		if err := rows.Scan(&peer, &conflicts, &refused, &applied); err != nil {
			return nil, fmt.Errorf("store: sync metrics: %w", err)
		}
		stats[peer] = [3]int64{conflicts, refused, applied}
		out.Conflicts += conflicts
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: sync metrics: %w", err)
	}

	peers, err := d.ListPeers(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, peer := range peers {
		m := PeerMetrics{
			Peer: peer.Peer, PullCursor: peer.PullCursor, PushedCursor: peer.PushedCursor,
		}
		if !peer.LastSeen.IsZero() {
			seen := peer.LastSeen
			age := int64(now.Sub(seen).Seconds())
			m.LastSeen, m.LastSeenAgeS = &seen, &age
		}
		if s, ok := stats[peer.Peer]; ok {
			m.Conflicts, m.Refused, m.Applied = s[0], s[1], s[2]
		}
		if err := d.sql.QueryRowContext(ctx,
			`SELECT (SELECT count(*) FROM artifacts WHERE hlc > $1)
			      + (SELECT count(*) FROM events WHERE seq_hlc > $1)
			      + (SELECT count(*) FROM tasks WHERE hlc > $1)
			      + (SELECT count(*) FROM grants WHERE hlc > $1)`,
			peer.PushedCursor).Scan(&m.PendingPush); err != nil {
			return nil, fmt.Errorf("store: sync metrics: %w", err)
		}
		out.Peers = append(out.Peers, m)
	}
	return out, nil
}

// SpanMetrics is what the trace layer itself holds, which is the one number a
// trace view cannot get from the traces: how many spans there are to look at.
type SpanMetrics struct {
	Spans  int64 `json:"spans_24h"`
	Traces int64 `json:"traces_24h"`
	Errors int64 `json:"error_spans_24h"`
}

// SpanMetricsFor counts the spans this principal may read.
func (d *DB) SpanMetricsFor(ctx context.Context, p *Principal, scopeAll bool) (*SpanMetrics, error) {
	out := &SpanMetrics{}
	a := &args{}
	filter := SpanFilterSQL(p, "s", a, scopeAll)
	since := a.next(time.Now().Add(-MetricWindow))
	err := d.sql.QueryRowContext(ctx,
		`SELECT count(*), count(DISTINCT s.trace_id),
		        sum(CASE WHEN s.status = 'error' THEN 1 ELSE 0 END)
		   FROM spans s WHERE `+filter+` AND s.started >= `+since, a.vals...).
		Scan(&out.Spans, &out.Traces, nullInt(&out.Errors))
	if err != nil {
		return nil, fmt.Errorf("store: span metrics: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------- history

// MetricMinSamples is how many readings a series needs before this node will
// call anything about it unusual.
//
// Below it there is no answer to give. Two readings have a mean and a standard
// deviation and both are noise; a verdict drawn from them is a coin toss with a
// number printed next to it, and the number is what people act on. So the
// anomaly pass refuses - it says how many samples it has and how many it needs
// - rather than reporting a deviation it cannot support. This is the whole of
// the "below N samples" rule, and it is a constant here so that the refusal and
// the check that it refuses read the same value.
const MetricMinSamples = 8

// MetricSampleEvery is how often a scope's history takes a reading. Metrics are
// read far more often than they change - a console polls, an agent asks - and a
// history that recorded every read would be a history of how often somebody
// looked.
const MetricSampleEvery = 60 * time.Second

// metricHistory is how many readings back an anomaly pass looks.
const metricHistory = 200

// RecordSample writes one reading of one series for one scope, but only if the
// last reading for it is older than MetricSampleEvery. It reports whether it
// wrote.
func (d *DB) RecordSample(ctx context.Context, scope, series string, value float64) (bool, error) {
	var last sql.NullTime
	err := d.sql.QueryRowContext(ctx,
		`SELECT max(at) FROM metric_samples WHERE scope = $1 AND series = $2`, scope, series).
		Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("store: record sample: %w", err)
	}
	if last.Valid && time.Since(last.Time) < MetricSampleEvery {
		return false, nil
	}
	if _, err := d.sql.ExecContext(ctx,
		`INSERT INTO metric_samples (id, scope, series, value, at) VALUES ($1, $2, $3, $4, now())`,
		ulid.NewString(), scope, series, value); err != nil {
		return false, fmt.Errorf("store: record sample: %w", err)
	}
	return true, nil
}

// Anomaly is one series' verdict.
//
// Verdict is deliberately a string and not a boolean: "unusual", "normal" and
// "insufficient samples" are three answers, and the third is not a quiet
// version of the second. Baseline, Sigma and Samples are carried with it so
// that whoever reads it can see what the claim rests on - a z of 4 against a
// baseline of two readings is not a finding.
const (
	VerdictNormal       = "normal"
	VerdictUnusual      = "unusual"
	VerdictInsufficient = "insufficient samples"
)

// AnomalyZ is how many standard deviations from its own history a reading has
// to be before this node calls it unusual. It is a distance from a measured
// baseline, not a fixed threshold on the value: nothing here says "more than
// 100 messages is a lot", because that is a number somebody made up about a
// deployment they have not seen.
const AnomalyZ = 3.0

// Anomaly is the verdict for one series.
type Anomaly struct {
	Series   string  `json:"series"`
	Verdict  string  `json:"verdict"`
	Latest   float64 `json:"latest"`
	Baseline float64 `json:"baseline,omitempty"`
	Sigma    float64 `json:"sigma,omitempty"`
	Z        float64 `json:"z,omitempty"`
	Samples  int     `json:"samples"`
	Required int     `json:"required"`
	Reason   string  `json:"reason,omitempty"`
}

// AnomalyFor judges one series against this scope's own recorded history.
func (d *DB) AnomalyFor(ctx context.Context, scope, series string, latest float64) (*Anomaly, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT value FROM metric_samples WHERE scope = $1 AND series = $2
		  ORDER BY at DESC LIMIT $3`, scope, series, metricHistory)
	if err != nil {
		return nil, fmt.Errorf("store: anomaly %s: %w", series, err)
	}
	defer rows.Close()

	var history []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: anomaly %s: %w", series, err)
		}
		history = append(history, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: anomaly %s: %w", series, err)
	}
	return Judge(series, latest, history), nil
}

// Judge is the verdict itself, over a series' history. It is separate from the
// query so that the rule - refuse below MetricMinSamples, otherwise compare
// against the measured mean and spread - can be tested without a database.
func Judge(series string, latest float64, history []float64) *Anomaly {
	out := &Anomaly{
		Series: series, Verdict: VerdictInsufficient, Latest: latest,
		Samples: len(history), Required: MetricMinSamples,
	}
	if len(history) < MetricMinSamples {
		out.Reason = fmt.Sprintf("%d of %d readings recorded; no verdict is drawn below %d",
			len(history), MetricMinSamples, MetricMinSamples)
		return out
	}
	mean := 0.0
	for _, v := range history {
		mean += v
	}
	mean /= float64(len(history))
	variance := 0.0
	for _, v := range history {
		variance += (v - mean) * (v - mean)
	}
	sigma := math.Sqrt(variance / float64(len(history)))

	out.Baseline, out.Sigma, out.Verdict = mean, sigma, VerdictNormal
	if sigma == 0 {
		// A series that has never moved has no spread to be unusual against.
		// Anything other than the value it has always had is the finding; the
		// same value is not.
		if latest != mean {
			out.Verdict = VerdictUnusual
			out.Reason = fmt.Sprintf("every one of %d readings was %g, and this one is %g",
				len(history), mean, latest)
		}
		return out
	}
	out.Z = (latest - mean) / sigma
	if math.Abs(out.Z) >= AnomalyZ {
		out.Verdict = VerdictUnusual
		out.Reason = fmt.Sprintf("%.1f sigma from a baseline of %g over %d readings",
			out.Z, round2(mean), len(history))
	}
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// SeriesSeen lists the series this scope has any history for, which is what a
// console needs to show what it could ask about.
func (d *DB) SeriesSeen(ctx context.Context, scope string) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT DISTINCT series FROM metric_samples WHERE scope = $1`, scope)
	if err != nil {
		return nil, fmt.Errorf("store: series: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("store: series: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: series: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// PoolStats is what the connection pool is doing, which is the first thing to
// look at when the node is slow and the database is fine.
type PoolStats struct {
	InUse   int `json:"in_use"`
	Idle    int `json:"idle"`
	Open    int `json:"open"`
	MaxOpen int `json:"max_open"`
	Waited  int `json:"wait_count"`
}

// Pool reports the pool's state.
func (d *DB) Pool() PoolStats {
	s := d.sql.Stats()
	return PoolStats{
		InUse: s.InUse, Idle: s.Idle, Open: s.OpenConnections,
		MaxOpen: s.MaxOpenConnections, Waited: int(s.WaitCount),
	}
}

// nullInt scans a possibly-NULL count into an int64. A sum over no rows is NULL
// and not zero, and scanning it straight into an int64 fails the whole read.
func nullInt(into *int64) any { return &nullIntScanner{into: into} }

type nullIntScanner struct{ into *int64 }

func (n *nullIntScanner) Scan(src any) error {
	var v sql.NullInt64
	if err := v.Scan(src); err != nil {
		return err
	}
	*n.into = v.Int64
	return nil
}
