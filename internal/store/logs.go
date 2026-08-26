package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/lib/pq"
)

// LogKind is one line of one log stream. fields.stream names the stream,
// fields.message carries the line, and fields.level and fields.type are what
// make it filterable.
//
// IT IS A TYPE RATHER THAN A METRIC WITH AN OPAQUE VALUE. A reading's value is
// any JSON, so a log line pushed as a metric is storable today - and nothing can
// filter or count it, which is the whole reason spec 01M0XG29064XBAQCX8J16QK1E9
// point 3 asks for the type: "Both are artifacts today only as prose in a body,
// which is why nothing can filter or count them."
const LogKind = "log"

// LogLevels is the closed vocabulary, taken from serenedash's logs.py rather
// than invented here, so a filter can offer the levels before a line of that
// level has been seen.
//
// WARNING and WARN are both here because both are emitted in the wild and
// collapsing them at the door would rewrite what a producer said.
var LogLevels = []string{"TRACE", "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "FATAL"}

// LogRowError is the log twin of MetricRowError.
type LogRowError struct {
	Row string
	Why string
}

func (e LogRowError) Error() string { return fmt.Sprintf("log row %s: %s", e.Row, e.Why) }

func (e LogRowError) depRefusal() {}

// IsLog reports whether the row is one line of a log stream.
func IsLog(a *Artifact) bool { return IsEntityType(a, LogKind) }

// checkLogRow is checkMetricRow's twin, asked at the same three statements.
//
//   - a line with no stream cannot be tailed - there is nothing to tail
//   - a line with no message is not a line
//   - a level outside the vocabulary cannot be counted with the others, and a
//     count that silently omits a level is worse than a refused write
//
// AN EMPTY LEVEL IS LEGAL and deliberately so. logs.py returns an unparseable
// line as a message with no timestamp and no level rather than dropping it,
// because "a crash dump or a library writing straight to stderr is exactly the
// thing you are tailing the log for, and it will not be in the server's format".
// Refusing those would lose the lines most worth having.
func checkLogRow(a *Artifact) error {
	if a == nil || !IsLog(a) {
		return nil
	}
	if len(a.Fields) == 0 {
		return LogRowError{Row: a.ID,
			Why: "a log line names its stream and carries its text - fields.stream and fields.message must be there"}
	}
	var outer struct {
		Stream  string `json:"stream"`
		Message string `json:"message"`
		Level   string `json:"level"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil {
		return LogRowError{Row: a.ID, Why: "fields is not JSON: " + err.Error()}
	}
	if strings.TrimSpace(outer.Stream) == "" {
		return LogRowError{Row: a.ID, Why: "a log line belongs to a stream - fields.stream must name one"}
	}
	if outer.Message == "" {
		return LogRowError{Row: a.ID, Why: "a log line is its text - fields.message must carry it"}
	}
	if outer.Level == "" {
		return nil
	}
	for _, l := range LogLevels {
		if strings.EqualFold(outer.Level, l) {
			return nil
		}
	}
	return LogRowError{Row: a.ID, Why: fmt.Sprintf(
		"level %q is outside the vocabulary %s - a level nothing can count is a line that vanishes from every total",
		outer.Level, strings.Join(LogLevels, ", "))}
}

// LogLine is one line as a reader gets it back.
type LogLine struct {
	ID      string `json:"id"`
	At      int64  `json:"at"`
	Stream  string `json:"stream"`
	Level   string `json:"level"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// LogCounts is what is in the window, so a header can say so without the caller
// counting the page it happened to be given.
type LogCounts struct {
	Levels map[string]int `json:"levels"`
	Types  map[string]int `json:"types"`
}

// TailLogs answers "the last N lines of this stream, filtered", oldest first.
//
// OLDEST FIRST, like SeriesOf and for the same reason: a log read top to bottom
// runs forwards, and every other list on this node is newest-first, which makes
// the order the thing most likely to be wrong and least likely to look wrong.
//
// THE FILTER IS SUBSTRING, NOT REGEX, following logs.py: "this runs on every
// keystroke of a search box, and a half-typed regex is a syntax error rather
// than a narrower result". It matches the message OR the type, which is what
// makes typing a subsystem name behave the way a person expects.
//
// THE COUNTS ARE OVER THE FILTERED WINDOW, not the page. A header saying "12
// ERROR" that changes when you scroll is describing the scroll position.
func (d *DB) TailLogs(ctx context.Context, p *Principal, stream, needle string,
	levels, types []string, limit int,
) ([]*LogLine, *LogCounts, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "logs.tail")
	defer span.End()
	if strings.TrimSpace(stream) == "" {
		return nil, nil, fmt.Errorf("store: tail logs: a stream must be named")
	}
	if limit <= 0 {
		limit = 400
	}
	if limit > 4096 {
		limit = 4096
	}

	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	where := []string{
		"coalesce(ar.tombstone, false) = false",
		filter,
		"ar.kind = '" + LogKind + "'",
		"ar.fields->>'stream' = " + a.next(stream),
	}
	if strings.TrimSpace(needle) != "" {
		// One parameter used twice: message OR type, as logs.py does.
		n := a.next("%" + strings.ToLower(needle) + "%")
		where = append(where, "(lower(ar.fields->>'message') LIKE "+n+
			" OR lower(coalesce(ar.fields->>'type', '')) LIKE "+n+")")
	}
	if len(levels) > 0 {
		where = append(where, "upper(coalesce(ar.fields->>'level', '')) = ANY("+a.next(pq.Array(upperAll(levels)))+")")
	}
	if len(types) > 0 {
		where = append(where, "coalesce(ar.fields->>'type', '') = ANY("+a.next(pq.Array(types))+")")
	}
	cond := strings.Join(where, "\n AND ")

	// The counts run over the WHOLE filtered set, before the limit, which is why
	// this is a second statement rather than a pass over the rows returned.
	counts := &LogCounts{Levels: map[string]int{}, Types: map[string]int{}}
	crows, err := d.sql.QueryContext(ctx, `
		SELECT upper(coalesce(ar.fields->>'level', '')) AS lvl,
		       coalesce(ar.fields->>'type', '') AS typ,
		       count(*)
		  FROM artifacts ar
		 WHERE `+cond+`
		 GROUP BY 1, 2`, a.vals...)
	if err != nil {
		span.Fail("the log counts did not run")
		return nil, nil, fmt.Errorf("store: count logs: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var lvl, typ string
		var n int
		if err := crows.Scan(&lvl, &typ, &n); err != nil {
			return nil, nil, fmt.Errorf("store: count logs: %w", err)
		}
		if lvl != "" {
			counts.Levels[lvl] += n
		}
		if typ != "" {
			counts.Types[typ] += n
		}
	}
	if err := crows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: count logs: %w", err)
	}

	// Newest N by hlc, then handed back oldest first - the window is the LAST
	// lines, and reading them runs forwards.
	rows, err := d.sql.QueryContext(ctx, `
		SELECT id, hlc, stream, lvl, typ, msg FROM (
		  SELECT ar.id AS id, ar.hlc AS hlc,
		         ar.fields->>'stream' AS stream,
		         coalesce(ar.fields->>'level', '') AS lvl,
		         coalesce(ar.fields->>'type', '') AS typ,
		         ar.fields->>'message' AS msg
		    FROM artifacts ar
		   WHERE `+cond+`
		   ORDER BY ar.hlc DESC, ar.id DESC
		   LIMIT `+a.next(limit)+`
		) w ORDER BY w.hlc ASC, w.id ASC`, a.vals...)
	if err != nil {
		span.Fail("the log read did not run")
		return nil, nil, fmt.Errorf("store: tail logs: %w", err)
	}
	defer rows.Close()
	out := []*LogLine{}
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.ID, &l.At, &l.Stream, &l.Level, &l.Type, &l.Message); err != nil {
			return nil, nil, fmt.Errorf("store: tail logs: %w", err)
		}
		out = append(out, &l)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: tail logs: %w", err)
	}
	return out, counts, nil
}

func upperAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToUpper(s))
	}
	return out
}
