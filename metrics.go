package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/store"
)

// GET /api/metrics - the fabric watching itself.
//
// The store is Postgres-wire, so the engine's numbers come from the database
// and the fabric's own - corpus, sync, collaboration, permissions - are layered
// on top of the same reads the API already does. Which means they are the same
// reads: every count here goes through the permission filter, because a metric
// is an aggregate and an aggregate over rows somebody may not read tells them
// how many there are. A principal of pa gets pa's numbers, a personal artifact
// is counted for its owner and for nobody else, and the node itself is the
// operator's - ?scope=all, the same escape hatch as everywhere else.
//
// Three habits, taken from serenedash and kept deliberately:
//
//   - report only what was measured. Nothing here is estimated or extrapolated,
//     and a group that could not be read says so.
//   - name the denominator. "cpu 0.42" is not a number; "0.42 of one core,
//     averaged over the last 37 seconds" is. Every rate and every share carries
//     what it is a share of.
//   - empty is could-not-read, not all-clear. A group carries `available`, and
//     when it is false it carries the reason. A console shows the reason where
//     it would otherwise show a confident zero.

// metricsResponse is the whole answer.
type metricsResponse struct {
	Node      string       `json:"node"`
	Version   string       `json:"version"`
	Generated time.Time    `json:"generated"`
	Scope     metricScope  `json:"scope"`
	Groups    metricGroups `json:"groups"`
}

// metricScope says whose numbers these are. It is on the response because the
// same endpoint answers differently to different tokens, and a dashboard that
// does not say whose view it is showing is a dashboard people misread.
type metricScope struct {
	User     string `json:"user,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Project  string `json:"project,omitempty"`
	Operator bool   `json:"operator"`
	All      bool   `json:"all"`
	Key      string `json:"key"`
}

// metricGroups are the six groups. GET /api/metrics answers with all of them,
// every time, each one able to say it could not be read - because a group that
// is missing from a payload is indistinguishable from a group that measured
// nothing, which is the confusion this whole file is written against.
//
// They are omitted only when a caller asked for a subset, which is what the MCP
// tools do: `storage` is the corpus and the permissions, and a null `sync` in
// its answer would be a claim about replication that nothing measured.
type metricGroups struct {
	Node          *nodeGroup      `json:"node,omitempty"`
	Corpus        *corpusGroup    `json:"corpus,omitempty"`
	Sync          *syncGroup      `json:"sync,omitempty"`
	Collaboration *collabGroup    `json:"collaboration,omitempty"`
	Permissions   *permGroup      `json:"permissions,omitempty"`
	Anomalies     *anomaliesGroup `json:"anomalies,omitempty"`
}

// availability is what every group carries: whether it was measured, and when
// it was not, why.
type availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Measured  string `json:"measured,omitempty"`
}

func measured(what string) availability {
	return availability{Available: true, Measured: what}
}

func unavailable(why string) availability {
	return availability{Available: false, Reason: why}
}

// ------------------------------------------------------------------- node

// nodeGroup is the machine. Every part of it is a pointer and omitted when it
// is absent, because a zero-valued dbHealth reads as "the database is down" -
// which is the exact confusion between "not measured" and "measured, and bad"
// that the availability flag exists to prevent. An unavailable node group is
// its reason and nothing else.
type nodeGroup struct {
	availability
	Uptime  float64       `json:"uptime_s,omitempty"`
	Started *time.Time    `json:"started,omitempty"`
	Build   string        `json:"build,omitempty"`
	DB      *dbHealth     `json:"db,omitempty"`
	Pool    *poolHealth   `json:"pool,omitempty"`
	CPU     *cpuHealth    `json:"cpu,omitempty"`
	Memory  *memoryHealth `json:"memory,omitempty"`
	Traces  *traceHealth  `json:"traces,omitempty"`
}

type dbHealth struct {
	availability
	Up        bool    `json:"up"`
	Engine    string  `json:"engine"`
	LatencyMS float64 `json:"latency_ms"`
	HLC       int64   `json:"hlc"`
}

type poolHealth struct {
	availability
	store.PoolStats
	Of string `json:"of"`
}

// cpuHealth is CPU as a share of one core, and it says so in the payload.
//
// A share of one core is the only denominator that means the same thing on
// every machine this runs on: "42% CPU" is 42% of one core on a laptop and 42%
// of thirty-two on a server, and a dashboard that does not say which is a
// dashboard reporting a different quantity depending on where it is pointed.
// Cores is carried beside it so a reader can work out the share of the machine
// if that is what they wanted.
type cpuHealth struct {
	availability
	CoreShare float64 `json:"core_share"`
	Of        string  `json:"of"`
	WindowS   float64 `json:"window_s"`
	Cores     int     `json:"cores"`
}

type memoryHealth struct {
	availability
	RSSBytes int64  `json:"rss_bytes"`
	Source   string `json:"source"`
	PageSize int    `json:"page_size"`
}

// traceHealth is the trace layer reporting on itself: how many spans it kept,
// how many it could not, and what the exporter did with them.
type traceHealth struct {
	availability
	Kept     int64  `json:"kept"`
	Dropped  int64  `json:"dropped"`
	Exporter string `json:"exporter,omitempty"`
	Sent     int64  `json:"exported"`
	NotSent  int64  `json:"export_failed"`
	LastErr  string `json:"export_error,omitempty"`
}

// ------------------------------------------------------------------ corpus

type corpusGroup struct {
	availability
	*store.CorpusMetrics
	Index     *store.IndexCoverage `json:"index"`
	Storage   *store.StorageSize   `json:"storage"`
	Growth    growth               `json:"growth"`
	Embedding embeddingCoverage    `json:"embedding"`
}

type growth struct {
	Day    int64  `json:"artifacts_24h"`
	Week   int64  `json:"artifacts_7d"`
	Events int64  `json:"events_24h"`
	Of     string `json:"of"`
}

// embeddingCoverage is how much of the readable corpus is reachable by which
// kind of search.
//
// Embedded is zero here and is reported rather than hidden. Search on this node
// is Postgres full text; the vector index arrives with SereneDB. "0 of 214
// embedded, 214 of 214 text-indexed" is a measurement. A coverage percentage
// over an index that does not exist would not be.
type embeddingCoverage struct {
	availability
	Embedded    int64   `json:"embedded"`
	TextOnly    int64   `json:"bm25_only"`
	Denominator int64   `json:"denominator"`
	Of          string  `json:"of"`
	Share       float64 `json:"embedded_share"`
}

// ------------------------------------------------------------------- sync

type syncGroup struct {
	availability
	*store.SyncMetrics
	PendingPull pendingPull `json:"pending_pull"`
}

// pendingPull is the one number in this group that is not measured, and it says
// so instead of reporting a zero.
//
// What a peer holds above our pull cursor is that peer's high water mark. It
// cannot be read from this side without asking the peer, and a sync that has
// not run has not asked. Reporting 0 would say "we are up to date" on the
// strength of never having looked, which is the failure mode this whole file is
// written against.
type pendingPull struct {
	availability
}

// ----------------------------------------------------------- collaboration

type collabGroup struct {
	availability
	*store.CollabMetrics
	Window string `json:"window"`
}

// ------------------------------------------------------------- permissions

type permGroup struct {
	availability
	*store.PermissionMetrics
	Window string `json:"window"`
	Of     string `json:"of"`
}

// --------------------------------------------------------------- anomalies

// anomaliesGroup is the only group that draws a conclusion, and it is the one
// that most often refuses to.
//
// The comparison is against this scope's own recorded history - readings this
// node took of itself, at most one a minute - and never against a number
// somebody chose. Below store.MetricMinSamples readings the verdict is
// "insufficient samples" with the count and the requirement beside it, because
// a deviation drawn from three points is a coin toss with a decimal place.
type anomaliesGroup struct {
	availability
	MinSamples int              `json:"min_samples"`
	Series     []*store.Anomaly `json:"series"`
	Unusual    int              `json:"unusual"`
	Refused    int              `json:"insufficient"`
	Basis      string           `json:"basis"`
}

// ------------------------------------------------------------ the handler

// metricsSource is what the node half of the metrics needs, and it is a struct
// rather than a *server so that the MCP surface can answer the same questions
// with the same code - it has a store and a start time and no HTTP.
type metricsSource struct {
	node    string
	started time.Time
	tracer  *otel.Tracer
	cpu     *cpuMeter
}

func (s *server) metricsSource() metricsSource {
	return metricsSource{node: s.node, started: s.started, tracer: s.tracer, cpu: &s.cpuMeter}
}

// handleMetrics answers the whole set.
//
// GET /api/metrics?scope=all
func (s *server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	all := scopeAll(r, p)

	resp, err := buildMetrics(r.Context(), s.db, p, all, s.metricsSource(), allGroups)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// The group names, so a caller - and the MCP tools, which each want a slice of
// this - can ask for part of it.
const (
	groupNode      = "node"
	groupCorpus    = "corpus"
	groupSync      = "sync"
	groupCollab    = "collaboration"
	groupPerms     = "permissions"
	groupAnomalies = "anomalies"
)

var allGroups = []string{groupNode, groupCorpus, groupSync, groupCollab, groupPerms, groupAnomalies}

// buildMetrics measures what was asked for, as this principal.
func buildMetrics(ctx context.Context, db *store.DB, p *store.Principal, all bool,
	src metricsSource, want []string,
) (*metricsResponse, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "metrics.collect")
	defer span.End()

	operator := p != nil && p.Operator
	resp := &metricsResponse{
		Node: src.node, Version: version, Generated: time.Now().UTC(),
		Scope: metricScope{Operator: operator, All: all, Key: scopeKey(p, all)},
	}
	if p != nil {
		resp.Scope.User, resp.Scope.Agent, resp.Scope.Project = p.UserID, p.AgentID, p.Project
	}

	asked := map[string]bool{}
	for _, g := range want {
		asked[g] = true
	}

	if asked[groupNode] {
		resp.Groups.Node = nodeMetrics(ctx, db, src, operator)
	}
	if asked[groupCorpus] {
		group, err := corpusMetrics(ctx, db, p, all, operator)
		if err != nil {
			return nil, err
		}
		resp.Groups.Corpus = group
	}
	if asked[groupSync] {
		resp.Groups.Sync = syncMetrics(ctx, db, operator)
	}
	if asked[groupCollab] {
		group, err := collabMetrics(ctx, db, p, all)
		if err != nil {
			return nil, err
		}
		resp.Groups.Collaboration = group
	}
	if asked[groupPerms] {
		group, err := permMetrics(ctx, db, p, all)
		if err != nil {
			return nil, err
		}
		resp.Groups.Permissions = group
	}
	if asked[groupAnomalies] {
		group, err := anomalies(ctx, db, p, all)
		if err != nil {
			return nil, err
		}
		resp.Groups.Anomalies = group
	}
	span.OK()
	return resp, nil
}

// scopeKey names the view a set of numbers belongs to, and is what a series of
// them is recorded under.
//
// Two principals see two different corpora, so their histories are two
// different series and averaging them together would make both baselines wrong.
// The operator's node-wide view is a third.
func scopeKey(p *store.Principal, all bool) string {
	if p == nil {
		return "anonymous"
	}
	if all && p.Operator {
		return "node:all"
	}
	return "user:" + p.UserID + "|project:" + p.Project
}

// nodeMetrics is the machine, and it is the operator's.
//
// Not because uptime is a secret - /healthz has always answered that to
// anybody - but because the pool, the CPU share, the resident size and how many
// spans were dropped are the shape and the state of somebody's machine, and
// ?scope=all is where this node draws that line already. A non-operator gets
// available:false and the reason, which is the truthful answer: it was not
// measured for you.
func nodeMetrics(ctx context.Context, db *store.DB, src metricsSource, operator bool) *nodeGroup {
	if !operator {
		g := &nodeGroup{availability: unavailable(
			"node health is the operator's view of this machine; /healthz answers " +
				"whether the node is up")}
		return g
	}
	startedAt := src.started.UTC()
	g := &nodeGroup{
		availability: measured("this node, since it started"),
		Uptime:       time.Since(src.started).Seconds(),
		Started:      &startedAt,
		Build:        buildStamp,
	}

	start := time.Now()
	err := db.Ping(ctx)
	g.DB = &dbHealth{
		availability: measured("one ping, now"),
		Up:           err == nil,
		Engine:       "postgres-wire",
		LatencyMS:    float64(time.Since(start).Microseconds()) / 1000,
		HLC:          db.Clock().Reading().Pack(),
	}
	if err != nil {
		// Down is a measurement, and it carries why. It is not the same as the
		// group being unavailable: the group was read, and what it found was a
		// database that did not answer.
		g.DB.Reason = "the store did not answer this ping"
	}

	g.Pool = &poolHealth{
		availability: measured("connections held by this process"),
		PoolStats:    db.Pool(),
		Of:           "of max_open connections",
	}
	cpu := src.cpu.read(src.started)
	memory := readMemory()
	g.CPU, g.Memory = &cpu, &memory

	kept, dropped := src.tracer.Stats()
	g.Traces = &traceHealth{
		availability: measured("spans this process recorded"),
		Kept:         kept, Dropped: dropped,
	}
	if exp := src.tracer.Exporter(); exp != nil {
		sent, expDropped, failed, lastErr := exp.Stats()
		g.Traces.Exporter = exp.Endpoint()
		g.Traces.Sent, g.Traces.NotSent = sent, failed+expDropped
		g.Traces.LastErr = lastErr
	}
	return g
}

// corpusMetrics is what the caller's half of the store holds.
func corpusMetrics(ctx context.Context, db *store.DB, p *store.Principal, all, operator bool) (*corpusGroup, error) {
	m, err := db.CorpusMetricsFor(ctx, p, all)
	if err != nil {
		return nil, err
	}
	g := &corpusGroup{
		availability:  measured("artifacts and events this principal may read"),
		CorpusMetrics: m,
		Index:         db.IndexCoverageFor(ctx, p, all),
		Growth: growth{
			Day: m.NewArtifacts, Week: m.WeekArtifacts, Events: m.NewEvents,
			Of: "of " + strconv.FormatInt(m.Artifacts, 10) + " readable artifacts",
		},
	}
	g.Embedding = embeddingCoverage{
		availability: unavailable("no vector index on this node: search here is text search, " +
			"and embeddings arrive with SereneDB"),
		Denominator: g.Index.Artifacts,
		Of:          "of the readable corpus",
	}
	if g.Index.Available {
		// The coverage is a real measurement even though the embedded half of
		// it is zero: how much of what this principal can read is findable at
		// all, and by which index.
		g.Embedding.Available = true
		g.Embedding.Reason = ""
		g.Embedding.Measured = "text index rows over readable artifacts"
		g.Embedding.TextOnly = g.Index.TextIndex
		g.Embedding.Embedded = 0
		g.Embedding.Share = 0
	}
	// Bytes on disk are the operator's: it is a property of the machine rather
	// than of the corpus, and it cannot be narrowed to a principal's rows.
	if operator {
		g.Storage = db.StorageSizes(ctx)
	} else {
		g.Storage = &store.StorageSize{
			Tables: map[string]int64{},
			Reason: "bytes on disk are the operator's view of this machine",
		}
	}
	return g, nil
}

// syncMetrics is federation, and it is the operator's for the reason GET
// /api/peers is: a peer's cursor is not one principal's business to learn from
// another's token.
func syncMetrics(ctx context.Context, db *store.DB, operator bool) *syncGroup {
	if !operator {
		return &syncGroup{
			availability: unavailable("replication cursors are the operator's view of this node"),
			SyncMetrics:  &store.SyncMetrics{Peers: []store.PeerMetrics{}},
			PendingPull:  pendingPull{unavailable("not measured: it is the peer's high water mark")},
		}
	}
	m, err := db.SyncMetricsNode(ctx)
	if err != nil {
		return &syncGroup{
			availability: unavailable("the replication bookmarks could not be read"),
			SyncMetrics:  &store.SyncMetrics{Peers: []store.PeerMetrics{}},
			PendingPull:  pendingPull{unavailable("not measured: it is the peer's high water mark")},
		}
	}
	return &syncGroup{
		availability: measured("this node's bookmarks and what its merges did"),
		SyncMetrics:  m,
		PendingPull: pendingPull{unavailable(
			"not measured here: what a peer holds above our pull cursor is that peer's " +
				"high water mark, and this node has not asked it")},
	}
}

func collabMetrics(ctx context.Context, db *store.DB, p *store.Principal, all bool) (*collabGroup, error) {
	m, err := db.CollabMetricsFor(ctx, p, all)
	if err != nil {
		return nil, err
	}
	return &collabGroup{
		availability:  measured("messages, tasks and rooms this principal is party to"),
		CollabMetrics: m,
		Window:        "24h for the rates, 7d for the per-day series",
	}, nil
}

func permMetrics(ctx context.Context, db *store.DB, p *store.Principal, all bool) (*permGroup, error) {
	m, err := db.PermissionMetricsFor(ctx, p, all)
	if err != nil {
		return nil, err
	}
	return &permGroup{
		availability:      measured("grants this principal is party to, and refusals it was given"),
		PermissionMetrics: m,
		Window:            "24h",
		Of:                "refusals are counted by status, and name no row",
	}, nil
}

// anomalies takes a reading of each series, records it if this scope's history
// is due one, and judges the reading against the history.
func anomalies(ctx context.Context, db *store.DB, p *store.Principal, all bool) (*anomaliesGroup, error) {
	key := scopeKey(p, all)
	readings, err := seriesReadings(ctx, db, p, all)
	if err != nil {
		return nil, err
	}

	g := &anomaliesGroup{
		availability: measured("each series against this scope's own recorded history"),
		MinSamples:   store.MetricMinSamples,
		Series:       []*store.Anomaly{},
		Basis: fmt.Sprintf("deviation from a measured baseline, not a fixed threshold: "+
			"%g sigma over at least %d readings, at most one reading a minute",
			store.AnomalyZ, store.MetricMinSamples),
	}

	names := make([]string, 0, len(readings))
	for name := range readings {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		value := readings[name]
		// The verdict is drawn against the history as it was before this
		// reading was recorded, so a reading is never compared with itself.
		verdict, err := db.AnomalyFor(ctx, key, name, value)
		if err != nil {
			return nil, err
		}
		if _, err := db.RecordSample(ctx, key, name, value); err != nil {
			return nil, err
		}
		g.Series = append(g.Series, verdict)
		switch verdict.Verdict {
		case store.VerdictUnusual:
			g.Unusual++
		case store.VerdictInsufficient:
			g.Refused++
		}
	}
	return g, nil
}

// seriesReadings is what the anomaly pass watches. They are deliberately few
// and each one is a count somebody would act on: how much is here, how much is
// being said, how often this node is refusing somebody, how much work is open.
func seriesReadings(ctx context.Context, db *store.DB, p *store.Principal, all bool) (map[string]float64, error) {
	corpus, err := db.CorpusMetricsFor(ctx, p, all)
	if err != nil {
		return nil, err
	}
	collab, err := db.CollabMetricsFor(ctx, p, all)
	if err != nil {
		return nil, err
	}
	perms, err := db.PermissionMetricsFor(ctx, p, all)
	if err != nil {
		return nil, err
	}
	spans, err := db.SpanMetricsFor(ctx, p, all)
	if err != nil {
		return nil, err
	}
	return map[string]float64{
		"corpus.artifacts":    float64(corpus.Artifacts),
		"corpus.new_24h":      float64(corpus.NewArtifacts),
		"collab.messages_24h": float64(collab.Messages),
		"collab.open_todos":   float64(collab.OpenTodos),
		"perms.denied_24h":    float64(perms.Denials),
		"traces.spans_24h":    float64(spans.Spans),
	}, nil
}

// ------------------------------------------------------------- cpu and rss

// cpuMeter turns the process's CPU time into a share of one core over the
// window since it was last read.
//
// The window matters and is reported: a share since the process started is a
// different quantity from a share over the last minute, and a node that has been
// up for a week will report almost nothing for the first if it is busy now.
type cpuMeter struct {
	mu      sync.Mutex
	lastCPU float64
	lastAt  time.Time
}

func (m *cpuMeter) read(started time.Time) cpuHealth {
	cpu, err := processCPUSeconds()
	if err != nil {
		return cpuHealth{
			availability: unavailable("this platform does not report process CPU time here"),
			Of:           "one core", Cores: numCPU(),
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	prevCPU, prevAt := m.lastCPU, m.lastAt
	now := time.Now()
	m.lastCPU, m.lastAt = cpu, now

	window := now.Sub(started).Seconds()
	used := cpu
	if !prevAt.IsZero() {
		window = now.Sub(prevAt).Seconds()
		used = cpu - prevCPU
	}
	if window <= 0 {
		return cpuHealth{
			availability: unavailable("no window to measure over yet"),
			Of:           "one core", Cores: numCPU(),
		}
	}
	return cpuHealth{
		availability: measured("process CPU time over the window below"),
		CoreShare:    round3(used / window),
		Of:           "one core (1.0 is one core saturated; this machine has more than one)",
		WindowS:      round3(window),
		Cores:        numCPU(),
	}
}

// processCPUSeconds reads utime+stime out of /proc/self/stat.
//
// It is Linux and it says so when it is not there, rather than reporting zero.
// The fields are the 14th and 15th, in clock ticks; the comm field can contain
// spaces and parentheses, so the split starts after the last ')'.
func processCPUSeconds() (float64, error) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	line := string(raw)
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 >= len(line) {
		return 0, fmt.Errorf("metrics: /proc/self/stat is not the expected shape")
	}
	fields := strings.Fields(line[end+2:])
	// After the comm and state fields, utime is field 11 and stime is 12
	// (0-based 11 and 12 of what is left).
	if len(fields) < 13 {
		return 0, fmt.Errorf("metrics: /proc/self/stat is short")
	}
	utime, err := strconv.ParseFloat(fields[11], 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseFloat(fields[12], 64)
	if err != nil {
		return 0, err
	}
	return (utime + stime) / clockTicks, nil
}

// clockTicks is the kernel's USER_HZ, which is 100 on every Linux this runs on.
// It is not read from getconf because that would be a subprocess for a constant.
const clockTicks = 100.0

// readMemory is the process's resident set, from /proc/self/statm.
func readMemory() memoryHealth {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return memoryHealth{
			availability: unavailable("this platform does not report a resident set here"),
		}
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return memoryHealth{availability: unavailable("/proc/self/statm is not the expected shape")}
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return memoryHealth{availability: unavailable("the resident set did not parse")}
	}
	size := os.Getpagesize()
	return memoryHealth{
		availability: measured("resident pages of this process"),
		RSSBytes:     pages * int64(size),
		Source:       "/proc/self/statm",
		PageSize:     size,
	}
}

// numCPU is how many cores this machine has. It is reported beside the core
// share so a reader can work out the share of the machine, which is the number
// they may have thought they were reading.
func numCPU() int { return runtime.NumCPU() }

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

// --------------------------------------------------------- prometheus text

// handlePrometheus renders the same measurements in the Prometheus exposition
// format, for a deployment that already scrapes.
//
// It is the same numbers behind the same filter: a scrape is a read, and a
// scraper holds a token like every other client. The labels carry the scope, so
// two scrapers with two tokens produce two series rather than one series that
// alternates between two principals' corpora.
//
// GET /metrics
func (s *server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	all := scopeAll(r, p)

	resp, err := buildMetrics(r.Context(), s.db, p, all, s.metricsSource(), allGroups)
	if err != nil {
		serverError(w, r, err)
		return
	}

	var b strings.Builder
	labels := fmt.Sprintf(`node=%q,scope=%q`, s.node, resp.Scope.Key)
	// One HELP and one TYPE per metric family, however many label sets it has:
	// a scrape carrying them twice is a scrape Prometheus rejects, and a metric
	// with several labelled series - artifacts by type, tasks by state - is
	// exactly where that happens.
	declared := map[string]bool{}
	metric := func(name, help, kind string, value float64, extra string) {
		if !declared[name] {
			declared[name] = true
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
		}
		all := labels
		if extra != "" {
			all += "," + extra
		}
		fmt.Fprintf(&b, "%s{%s} %g\n", name, all, value)
	}

	if g := resp.Groups.Corpus; g != nil && g.Available {
		metric("flowy_artifacts", "artifacts this principal may read", "gauge",
			float64(g.Artifacts), "")
		metric("flowy_events", "events this principal may read", "gauge", float64(g.Events), "")
		metric("flowy_artifacts_new_24h", "artifacts created in the last 24 hours", "gauge",
			float64(g.NewArtifacts), "")
		for kind, n := range g.ByType {
			metric("flowy_artifacts_by_type", "artifacts by type", "gauge", float64(n),
				fmt.Sprintf("type=%q", kind))
		}
	}
	if g := resp.Groups.Collaboration; g != nil && g.Available {
		metric("flowy_messages_24h", "chat messages in the last 24 hours", "gauge",
			float64(g.Messages), "")
		metric("flowy_open_todos", "todo, feature and handoff items not done", "gauge",
			float64(g.OpenTodos), "")
		metric("flowy_handoffs_in_flight", "tasks open or delegated", "gauge",
			float64(g.HandoffsOpen), "")
		for state, n := range g.Tasks {
			metric("flowy_tasks", "tasks by state", "gauge", float64(n),
				fmt.Sprintf("state=%q", state))
		}
	}
	if g := resp.Groups.Permissions; g != nil && g.Available {
		metric("flowy_grants", "grants this principal is party to", "gauge", float64(g.Grants), "")
		metric("flowy_denied_24h", "requests refused in the last 24 hours", "gauge",
			float64(g.Denials), "")
	}
	if g := resp.Groups.Sync; g != nil && g.Available {
		metric("flowy_sync_offline_queue", "rows owed to a reader and not yet handed over",
			"gauge", float64(g.OfflineQueue), "")
		metric("flowy_sync_conflicts", "rows that arrived and lost their merge", "counter",
			float64(g.Conflicts), "")
		for _, peer := range g.Peers {
			metric("flowy_sync_pending_push", "rows above the cursor we last pushed past",
				"gauge", float64(peer.PendingPush), fmt.Sprintf("peer=%q", peer.Peer))
		}
	}
	if g := resp.Groups.Node; g != nil && g.Available && g.CPU != nil {
		metric("flowy_uptime_seconds", "seconds since this node started", "gauge", g.Uptime, "")
		metric("flowy_cpu_core_share",
			"process CPU as a share of ONE core over the window since the last read",
			"gauge", g.CPU.CoreShare, "")
		metric("flowy_rss_bytes", "resident set size of this process", "gauge",
			float64(g.Memory.RSSBytes), "")
		metric("flowy_db_pool_in_use", "connections checked out of the pool", "gauge",
			float64(g.Pool.InUse), "")
		metric("flowy_spans_dropped", "spans this process could not record", "counter",
			float64(g.Traces.Dropped), "")
	}
	// A group that could not be read is written as a zero-valued availability
	// series rather than as a missing metric: a scraper cannot tell a metric
	// that is absent because nothing happened from one that is absent because
	// the node would not answer, and this is the only place to say which.
	for name, group := range map[string]availability{
		"node":          availabilityOf(resp.Groups.Node),
		"corpus":        availabilityOf(resp.Groups.Corpus),
		"sync":          availabilityOf(resp.Groups.Sync),
		"collaboration": availabilityOf(resp.Groups.Collaboration),
		"permissions":   availabilityOf(resp.Groups.Permissions),
		"anomalies":     availabilityOf(resp.Groups.Anomalies),
	} {
		value := 0.0
		if group.Available {
			value = 1
		}
		metric("flowy_group_available",
			"1 when the group was measured, 0 when it could not be - not a zero count",
			"gauge", value, fmt.Sprintf("group=%q", name))
	}
	if g := resp.Groups.Anomalies; g != nil {
		metric("flowy_anomalies_unusual",
			"series whose latest reading is far from their own recorded baseline",
			"gauge", float64(g.Unusual), "")
		metric("flowy_anomalies_insufficient",
			"series with too little history for a verdict to be drawn",
			"gauge", float64(g.Refused), "")
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(b.String())); err != nil {
		return
	}
}

// availabilityOf reads the availability off any of the groups, treating a group
// that is not there at all as one that could not be read.
func availabilityOf(group any) availability {
	switch g := group.(type) {
	case *nodeGroup:
		if g != nil {
			return g.availability
		}
	case *corpusGroup:
		if g != nil {
			return g.availability
		}
	case *syncGroup:
		if g != nil {
			return g.availability
		}
	case *collabGroup:
		if g != nil {
			return g.availability
		}
	case *permGroup:
		if g != nil {
			return g.availability
		}
	case *anomaliesGroup:
		if g != nil {
			return g.availability
		}
	}
	return unavailable("this group was not asked for")
}
