package store

// Dashboards live under the memory bucket, the same as todos and openspec rows:
// kind is the identity and EntityType answers it (entitytype.go). Two kinds,
// and only the first is a declaration:
//
//	dashboard - one page an agent authors for a person to read. The whole of
//	            it is fields.tiles: a fixed vocabulary of components, each one
//	            a query over a named metric series. It RUNS nothing - the
//	            numbers come from metric rows producers push through the
//	            ordinary artifact door, and the console renders the
//	            declaration. (The operator, 01M0WY7F5: "templated ui
//	            components", and the answer filed on the row: declare, do not
//	            run - a plugin system with a separate backend would be a
//	            second node to feed, and the node already stores, scopes and
//	            signs every row a dashboard reads.)
//
//	metric     - one reading of one series. fields.name names the series,
//	            fields.value is the reading, and the row's own clock is its
//	            timestamp: a dashboard never asks a producer for a value, so a
//	            number on a dashboard is always a number somebody already
//	            pushed, with the age of the row it came from. Scope decides
//	            who reads it, exactly as it does every other row.
//
// A dashboard row is no more readable than the metrics it names - the
// permission filter on the read door is the same one every list goes through -
// and it holds no state of its own: two loads of the page answer from the rows,
// so what changed between them is what the page shows.
//
// The vocabulary is closed ON PURPOSE. tile kinds land as the renderer lands:
// a tile kind the node cannot draw is refused at write naming the vocabulary,
// rather than accepted and silently drawn as nothing - the plausible zero the
// missing-metric state exists to prevent.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/lib/pq"
)

const (
	// MetricKind is one reading of one metric series. fields.name names the
	// series, fields.value is the reading.
	MetricKind = "metric"
	// DashboardKind is one page of declared tiles. fields.tiles is the whole
	// of the declaration.
	DashboardKind = "dashboard"
)

// DashboardTile is one declared panel of a dashboard row.
type DashboardTile struct {
	// Kind is the component: number, table, grid. The closed set - see
	// checkDashboardRow.
	Kind string `json:"kind"`
	// Label is what the tile is called on the page, in a person's words.
	Label string `json:"label"`
	// Metric names the series the tile reads. The newest row of that series
	// is what the tile draws.
	Metric string `json:"metric"`
	// StaleAfterSeconds is how old a reading may be before the tile draws it
	// as stale rather than live. Zero means never stale: a reading is a fact
	// about the row it came from, and "fresh enough" is a decision the
	// author makes per tile.
	StaleAfterSeconds int64 `json:"stale_after_seconds,omitempty"`
}

// IsDashboard reports whether the row is a dashboard declaration.
func IsDashboard(a *Artifact) bool { return IsEntityType(a, DashboardKind) }

// IsMetric reports whether the row is one reading of a metric series.
func IsMetric(a *Artifact) bool { return IsEntityType(a, MetricKind) }

// DashboardTilesOf reads fields.tiles off a row. Absent fields is no tiles,
// not an error - a dashboard row without its declaration is a defect that
// checkDashboardRow refuses at the next write, and a reader that cannot see
// the tiles says so by returning nothing. Unparsable fields IS an error, the
// same contract OpenspecFilesOf keeps.
func DashboardTilesOf(a *Artifact) ([]DashboardTile, error) {
	if a == nil || len(a.Fields) == 0 {
		return nil, nil
	}
	var outer struct {
		Tiles []DashboardTile `json:"tiles"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil {
		return nil, fmt.Errorf("fields is not JSON: %w", err)
	}
	return outer.Tiles, nil
}

// MetricNameOf reads fields.name off a metric row, or "" when the row does not
// carry one. The row checks make a written row carry it; a read is lenient,
// for the reason DashboardTilesOf is.
func MetricNameOf(a *Artifact) string {
	if a == nil || len(a.Fields) == 0 {
		return ""
	}
	var outer struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil {
		return ""
	}
	return outer.Name
}

// DashboardRowError is a refusal: the statement wanted to write a row that
// says it is a dashboard but is not one. The caller can fix it, so it is a
// 400 at the doors - it implements depRefusal, the same refusal contract
// checkMergeRow and checkOpenspecRow use (deps.go).
type DashboardRowError struct {
	Row string
	Why string
}

func (e DashboardRowError) Error() string {
	return fmt.Sprintf("dashboard row %s: %s", e.Row, e.Why)
}

func (e DashboardRowError) depRefusal() {}

// MetricRowError is the metric-row twin of DashboardRowError.
type MetricRowError struct {
	Row string
	Why string
}

func (e MetricRowError) Error() string {
	return fmt.Sprintf("metric row %s: %s", e.Row, e.Why)
}

func (e MetricRowError) depRefusal() {}

// checkDashboardRow is the invariant, asked of the row a statement is about
// to write at the same three statements that ask checkOpenspecRow - upsert,
// set-fields and create. It refuses nothing that is not a dashboard kind, and
// for those it is the whole shape:
//
//   - a dashboard with no tiles is a page that shows nothing
//   - a tile kind outside the vocabulary is a declaration the renderer
//     cannot honour, refused by name rather than drawn as nothing
//   - a tile with no label is a number nobody knows the meaning of
//   - a tile naming no metric is a query over nothing
func checkDashboardRow(a *Artifact) error {
	if a == nil || !IsDashboard(a) {
		return nil
	}
	tiles, err := DashboardTilesOf(a)
	if err != nil {
		return DashboardRowError{Row: a.ID, Why: err.Error()}
	}
	if len(tiles) == 0 {
		return DashboardRowError{Row: a.ID,
			Why: "a dashboard is its declaration - fields.tiles must carry at least one tile"}
	}
	for _, t := range tiles {
		if t.Kind != "number" && t.Kind != "table" && t.Kind != "grid" {
			return DashboardRowError{Row: a.ID, Why: fmt.Sprintf(
				"tile %q declares kind %q - the vocabulary is number, table, grid",
				t.Label, t.Kind)}
		}
		if strings.TrimSpace(t.Label) == "" {
			return DashboardRowError{Row: a.ID, Why: "a tile names what it shows - label must carry it"}
		}
		if strings.TrimSpace(t.Metric) == "" {
			return DashboardRowError{Row: a.ID, Why: "a tile reads a metric - metric must name one"}
		}
	}
	return nil
}

// checkMetricRow is checkDashboardRow's twin for a pushed reading: a metric
// row names its series and carries a reading. Asked at the same three
// statements, for the same reason - a rule kept per surface is a rule the
// next surface forgets.
func checkMetricRow(a *Artifact) error {
	if a == nil || !IsMetric(a) {
		return nil
	}
	if len(a.Fields) == 0 {
		return MetricRowError{Row: a.ID,
			Why: "a metric is a named reading - fields.name and fields.value must carry them"}
	}
	var outer struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil {
		return MetricRowError{Row: a.ID, Why: "fields is not JSON: " + err.Error()}
	}
	if strings.TrimSpace(outer.Name) == "" {
		return MetricRowError{Row: a.ID, Why: "a metric names its series - fields.name must carry it"}
	}
	if len(outer.Value) == 0 || string(outer.Value) == "null" {
		return MetricRowError{Row: a.ID, Why: "a metric is its reading - fields.value must carry it"}
	}
	return nil
}

// Metrics reads the rows of the named metric series, newest first, under the
// principal's own reach - the same permission filter every list goes through,
// so a dashboard is no more readable than the rows it names. The caller's
// limit is clamped the way every list door clamps it (clampLimit).
//
// Newest first means the row's own clock: hlc, then id, the same ordering
// tasks and forge rows use. The hlc is the one ordering no later write
// changes, which is the property a series read needs - a value pushed last
// is the value a dashboard must show, however its row was filed.
func (d *DB) Metrics(ctx context.Context, p *Principal, names []string, limit int) ([]*Artifact, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "metrics.read")
	defer span.End()
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	query := `SELECT ` + artifactColumns + `
	            FROM artifacts ar
	           WHERE coalesce(ar.tombstone, false) = false
	             AND ` + filter + `
	             AND ar.kind = '` + MetricKind + `'
	             AND ar.fields ? 'name'
	             AND ar.fields->>'name' = ANY(` + a.next(pq.Array(names)) + `)
	           ORDER BY ar.hlc DESC, ar.id DESC
	           LIMIT ` + a.next(clampLimit(limit))

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		span.Fail("the metrics read did not run")
		return nil, fmt.Errorf("store: read metrics: %w", err)
	}
	defer rows.Close()

	out := []*Artifact{}
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return nil, fmt.Errorf("store: read metrics: %w", err)
		}
		out = append(out, art)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read metrics: %w", err)
	}
	return out, nil
}
