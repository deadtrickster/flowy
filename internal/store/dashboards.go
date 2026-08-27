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
//	            fields.value is the reading, fields.state is the producer's
//	            word for which of measured, inferred, unknown it is (absent
//	            is unknown), and the row's own clock is its timestamp: a
//	            dashboard never asks a producer for a value, so a number on
//	            a dashboard is always a number somebody already pushed, with
//	            the age of the row it came from. Scope decides who reads it,
//	            exactly as it does every other row.
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
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/lib/pq"
)

const (
	// MetricKind is one reading of one metric series. fields.name names the
	// series, fields.value is the reading, and fields.state says which of
	// measured, inferred, unknown the reading is - absent is unknown.
	MetricKind = "metric"
	// DashboardKind is one page of declared tiles. fields.tiles is the whole
	// of the declaration.
	DashboardKind = "dashboard"
)

// DashboardTile is one declared panel of a dashboard row.
type DashboardTile struct {
	// Kind is the component: number, table, grid, frame, gauge, series, log.
	// The closed set - see checkDashboardRow.
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
	// Min, Max and Thresholds are read ONLY so that a tile carrying them can be
	// refused by name. They are not part of the declaration: a gauge's scale
	// travels with the reading. See checkTileCarriesNoBounds.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// RawMessage rather than a bool or a struct: this only ever needs to answer
	// "was the key there", and a typed field would silently accept a thresholds
	// object it could not parse and report its absence.
	Thresholds json.RawMessage `json:"thresholds,omitempty"`
	// Points is the window a series tile draws - the newest N readings of its
	// metric, oldest first. Zero means the console's default. Only a series
	// tile reads it; the shape check refuses it as negative.
	Points int64 `json:"points,omitempty"`
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

// MetricStateOf reads fields.state off a metric row: which of measured,
// inferred, unknown the reading is. Absent or unrecognised is unknown - a
// number that does not say what it is must read as unknown, not as measured,
// because measured is a claim and the claim is the producer's to make.
func MetricStateOf(a *Artifact) string {
	if a == nil || len(a.Fields) == 0 {
		return "unknown"
	}
	var outer struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil {
		return "unknown"
	}
	switch outer.State {
	case "measured", "inferred", "unknown":
		return outer.State
	}
	return "unknown"
}

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
//   - a tile carrying min, max or thresholds has put them on the wrong object.
//     A gauge is a value WITH ITS BOUNDS and the bounds travel beside the
//     reading, because the producer is the party that knows them - see
//     checkMetricRow. Refused by name rather than ignored, because an ignored
//     bound draws an unscaled bar and looks like a rendering bug.
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
		if t.Kind != "number" && t.Kind != "table" && t.Kind != "grid" &&
			t.Kind != "frame" && t.Kind != "gauge" && t.Kind != "series" &&
			t.Kind != LogKind && t.Kind != ReportKind {
			return DashboardRowError{Row: a.ID, Why: fmt.Sprintf(
				"tile %q declares kind %q - the vocabulary is number, table, grid, frame, gauge, series, log, report",
				t.Label, t.Kind)}
		}
		if err := checkTileCarriesNoBounds(a.ID, t); err != nil {
			return err
		}
		if strings.TrimSpace(t.Label) == "" {
			return DashboardRowError{Row: a.ID, Why: "a tile names what it shows - label must carry it"}
		}
		if strings.TrimSpace(t.Metric) == "" {
			return DashboardRowError{Row: a.ID, Why: "a tile reads a metric - metric must name one"}
		}
		if t.Points < 0 {
			return DashboardRowError{Row: a.ID, Why: fmt.Sprintf(
				"tile %q declares %d points - the window must be positive", t.Label, t.Points)}
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
		State string          `json:"state"`
		Min   *float64        `json:"min"`
		Max   *float64        `json:"max"`
		// Thresholds is where a value stops being ordinary. A pointer, because
		// a reading with no thresholds is the common case and an empty object
		// is a different statement from an absent one.
		Thresholds *struct {
			Warn *float64 `json:"warn"`
			Crit *float64 `json:"crit"`
		} `json:"thresholds"`
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
	if outer.State != "" && outer.State != "measured" && outer.State != "inferred" && outer.State != "unknown" {
		return MetricRowError{Row: a.ID, Why: fmt.Sprintf(
			"a metric says what its reading is - fields.state is %q, and the states are measured, inferred, unknown",
			outer.State)}
	}
	// A HALF-DECLARED SCALE IS NOT A SCALE. min alone cannot place a value and
	// max alone cannot either; a renderer given one of them has to invent the
	// other, and what it invents is zero.
	if (outer.Min == nil) != (outer.Max == nil) {
		return MetricRowError{Row: a.ID,
			Why: "a scale is both ends - fields.min and fields.max come together or not at all"}
	}
	// Both checked, though the refusal above already guarantees they arrive
	// together. That guarantee is an ORDERING and an ordering is invisible from
	// here: the same shape, with only one of them checked, turned a bad
	// declaration into a SIGSEGV once already.
	if outer.Min != nil && outer.Max != nil && *outer.Max <= *outer.Min {
		return MetricRowError{Row: a.ID, Why: fmt.Sprintf(
			"max %v is at or below min %v - a scale that does not ascend cannot place a reading",
			*outer.Max, *outer.Min)}
	}
	// A READING THAT IS A REPORT IS CHECKED AS ONE. The value is any JSON, so a
	// malformed report would otherwise be stored and refused only by the
	// renderer - at which point the producer has moved on and the page is blank
	// with nobody to ask.
	if IsReportReading(a.Fields) {
		if err := checkReportReading(a.ID, reportValueOf(a.Fields)); err != nil {
			return err
		}
	}
	if outer.Thresholds == nil {
		return nil
	}
	// THRESHOLDS NEED A SCALE TO BE ON. "warn at 15" says nothing without
	// knowing what the bar runs between.
	if outer.Min == nil || outer.Max == nil {
		return MetricRowError{Row: a.ID,
			Why: "thresholds mark a place on a scale - fields.min and fields.max must declare one"}
	}
	for _, m := range []struct {
		name string
		at   *float64
	}{{"warn", outer.Thresholds.Warn}, {"crit", outer.Thresholds.Crit}} {
		// Every operand this comparison uses is checked here rather than
		// inherited from the refusal above. Disabling that refusal to prove it
		// fails turned this loop into a nil dereference, which is the second
		// time tonight an ordering stood in for a guard.
		if m.at == nil || outer.Min == nil || outer.Max == nil {
			continue
		}
		if *m.at < *outer.Min || *m.at > *outer.Max {
			return MetricRowError{Row: a.ID, Why: fmt.Sprintf(
				"threshold %s at %v is outside the scale %v..%v - a mark off the bar can never be reached",
				m.name, *m.at, *outer.Min, *outer.Max)}
		}
	}
	// DELIBERATELY NOT CHECKED: that warn comes before crit. A gauge of free
	// disk or remaining quota gets worse DOWNWARDS, and forcing warn <= crit
	// would refuse every one of them.
	//
	// The order is not a constraint here, it is the DIRECTION: crit above warn
	// means high is bad, crit below warn means low is bad. A renderer reads the
	// sense of the gauge off the two numbers it already has, so nothing needs a
	// field saying which way round it is.
	return nil
}

// checkTileCarriesNoBounds refuses a scale declared on the tile.
//
// It exists because the obvious place to put min and max is the tile - it is
// where stale_after_seconds lives, and that is a presentation decision. Bounds
// are not: the producer measuring memory is the party that knows the limit is
// 64GB, and it changes when the machine does. A tile-side scale would have to
// be re-declared on every dashboard that reads the series, and each copy would
// go stale on its own.
func checkTileCarriesNoBounds(row string, t DashboardTile) error {
	if t.Min == nil && t.Max == nil && len(t.Thresholds) == 0 {
		return nil
	}
	return DashboardRowError{Row: row, Why: fmt.Sprintf(
		"tile %q carries a scale - min, max and thresholds belong on the reading, where the producer that knows them writes them",
		t.Label)}
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

// SeriesPoint is one reading of one series: when it was written, and what it said.
//
// `At` is the hlc rather than a wall clock, for the reason every other ordering
// on this node uses it: two nodes' clocks disagree and the log does not.
type SeriesPoint struct {
	// At is the store's HLC - a LOGICAL ordering clock, not wall time. It is
	// what the points are sorted by and what makes the order total, and it is
	// meaningless as a date: subtracting it from now() gives a number with no
	// unit. A console that measured a tile's age from it got NaN, and an NaN
	// age compares false against every threshold, so the tile never went stale.
	// Fixed console-side in b3c0b18; When exists so nobody has to find that out
	// twice.
	At int64 `json:"at"`
	// When is the same point's WALL CLOCK, RFC3339, from the row's created
	// column - what a label, an axis or an age is computed from.
	//
	// Both are carried because they answer different questions and neither
	// substitutes: two rows can share a wall-clock second and still have a
	// definite order, which is the whole reason the store keeps an HLC.
	When  string          `json:"when"`
	Value json.RawMessage `json:"value"`
}

// Series is one named series and its points, OLDEST FIRST.
type Series struct {
	Name   string        `json:"name"`
	Points []SeriesPoint `json:"points"`
	// Truncated says the ring was full: there are older points than these.
	// A caller drawing a sparkline needs to know its window is not the whole
	// history, and "the series starts here" and "this is where I stopped
	// looking" are different facts.
	Truncated bool `json:"truncated"`
}

// SeriesOf reads the last `points` readings of each named series, oldest first.
//
// WHY THIS IS NOT Metrics() WITH A LIMIT. Metrics takes one limit across every
// name it is asked for and returns them interleaved, newest first. That answers
// "what is the current value" perfectly and cannot answer "the last 60 of each",
// which is what a sparkline is: three series at limit 200 can come back as 200
// points of the busiest one and none of the others.
//
// A WINDOW PER NAME, therefore, and the retention is the CALLER'S window rather
// than a policy here. The operator's rule: "we dont need to store historical
// data beyond what is on the screen, same as tui (mind reflows so up to some max
// width/height)". So this door bounds what it RETURNS and says when it truncated;
// what the store keeps is a separate question from what a panel can draw.
//
// OLDEST FIRST because that is the direction a sparkline is read. Every other
// door here answers newest-first, so this one says so in its name and its doc
// rather than leaving a caller to discover the order from the data - a series
// drawn backwards looks like a trend reversing, which is a wrong answer that
// renders beautifully.
func (d *DB) SeriesOf(ctx context.Context, p *Principal, names []string, points int) ([]*Series, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "metrics.series")
	defer span.End()
	if points <= 0 {
		points = 60
	}
	if points > 4096 {
		// serenedash's history.py keeps 4096 samples and says why: enough for an
		// overnight comparison without turning into something that needs
		// managing. A panel cannot draw more columns than that either.
		points = 4096
	}
	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	// One window per name, so a busy series cannot crowd out a quiet one. The
	// extra row per name is what tells the caller it truncated.
	query := `SELECT name, hlc, created, value FROM (
	            SELECT ar.fields->>'name' AS name, ar.hlc AS hlc, ar.created AS created,
	                   ar.fields->'value' AS value,
	                   row_number() OVER (PARTITION BY ar.fields->>'name' ORDER BY ar.hlc DESC, ar.id DESC) AS rn
	              FROM artifacts ar
	             WHERE coalesce(ar.tombstone, false) = false
	               AND ` + filter + `
	               AND ar.kind = '` + MetricKind + `'
	               AND ar.fields ? 'name'
	               AND ar.fields->>'name' = ANY(` + a.next(pq.Array(names)) + `)
	          ) w
	         WHERE w.rn <= ` + a.next(points+1) + `
	         ORDER BY w.name, w.hlc ASC`

	rows, err := d.sql.QueryContext(ctx, query, a.vals...)
	if err != nil {
		span.Fail("the series read did not run")
		return nil, fmt.Errorf("store: read series: %w", err)
	}
	defer rows.Close()

	byName := map[string]*Series{}
	order := []string{}
	for rows.Next() {
		var name string
		var hlc int64
		var created sql.NullTime
		var raw []byte
		if err := rows.Scan(&name, &hlc, &created, &raw); err != nil {
			return nil, fmt.Errorf("store: read series: %w", err)
		}
		s := byName[name]
		if s == nil {
			s = &Series{Name: name, Points: []SeriesPoint{}}
			byName[name] = s
			order = append(order, name)
		}
		// A row with no created is left as "" rather than stamped with now():
		// an absent wall clock and a wall clock of this instant are different
		// facts, and the second one silently makes an old point look fresh.
		when := ""
		if created.Valid {
			when = created.Time.UTC().Format(time.RFC3339)
		}
		s.Points = append(s.Points, SeriesPoint{At: hlc, When: when, Value: json.RawMessage(raw)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read series: %w", err)
	}
	out := make([]*Series, 0, len(order))
	for _, n := range order {
		s := byName[n]
		// The extra row was only ever a probe for "is there more"; drop it from
		// the OLDEST end, because the newest points are the ones being drawn.
		if len(s.Points) > points {
			s.Truncated = true
			s.Points = s.Points[len(s.Points)-points:]
		}
		out = append(out, s)
	}
	// A NAME WITH NO ROWS IS ABSENT, NOT EMPTY, and the caller is told which by
	// getting no entry rather than an entry with no points. "nothing has been
	// pushed" and "this series does not exist" are different, and collapsing
	// that pair has cost this fleet more than any other single mistake.
	return out, nil
}

// RetainDefaultPoints is how many readings of a series survive when the
// producer does not say. 4096 is serenedash's own history.py KEEP, chosen there
// because it bounds the file without turning a restart into a young baseline -
// about 5.7 hours at one sample every 5 seconds.
const RetainDefaultPoints = 4096

// Retention is how much of its own series a producer wants kept, carried on the
// reading as fields.retain.
//
// IT LIVES ON THE READING because the producer is the only party that knows its
// push rate. A node-wide number cannot be right for a series sampled every five
// seconds and one pushed hourly at the same time, and a per-series table
// somewhere else would be a second place to describe a series that already
// describes itself every time it is written.
type Retention struct {
	// Points is the most readings to keep. Zero means RetainDefaultPoints.
	Points int `json:"points"`
	// Seconds is how old a reading may get before it is dropped. Zero means no
	// age bound - Points alone holds the series down.
	Seconds int64 `json:"seconds"`
}

// RetentionOf reads fields.retain off a reading. A row that does not carry one
// gets the default, because a producer that says nothing about retention is
// asking for the ordinary case rather than for unbounded growth.
//
// Unparsable fields is the default too, and deliberately not an error: this is
// read on the write path, and refusing a reading because its retention hint is
// malformed would lose the measurement to protect the housekeeping. That is
// history.py's rule - "every reader treats a missing or unreadable file as no
// history" - pointed the same way.
func RetentionOf(a *Artifact) Retention {
	r := Retention{Points: RetainDefaultPoints}
	if a == nil || len(a.Fields) == 0 {
		return r
	}
	var outer struct {
		Retain *Retention `json:"retain"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil || outer.Retain == nil {
		return r
	}
	if outer.Retain.Points > 0 {
		r.Points = outer.Retain.Points
	}
	if outer.Retain.Points > RetainMaxPoints {
		r.Points = RetainMaxPoints
	}
	if outer.Retain.Seconds > 0 {
		r.Seconds = outer.Retain.Seconds
	}
	return r
}

// RetainMaxPoints is the ceiling a producer cannot raise itself above. A
// retention hint is untrusted input like any other field, and "keep ten million"
// is a denial of service written as a preference.
const RetainMaxPoints = 65536

// pruneSeries drops the readings of one series that retention no longer covers.
// Two bounds, and a reading must satisfy BOTH to survive:
//
//   - POINTS is the safety bound. It is what stops a series pushed a hundred
//     times a second from being unbounded, and it is the one that always applies.
//   - SECONDS is the policy. A dashboard window is measured in time, not in
//     samples, and forty series at different push rates cover wildly different
//     spans at the same count - which is exactly why count alone is not enough.
//
// AMORTISED, not run on every write. The delete only happens once a series is
// past twice its allowance, so a series at its limit is rewritten every N pushes
// rather than every one. history.py trims in place at twice retention for the
// same reason and says so: "a rewrite every few hundred samples rather than
// every one".
//
// A SERIES NOBODY PUSHES KEEPS ITS LAST WINDOW. Pruning happens on write, so a
// producer that stops is not emptied out by its own age bound. That is the
// useful behaviour rather than a gap in this one: a panel that says "last seen
// 3h ago" tells you the box died, and a panel that says "no data" does not.
func (d *DB) pruneSeries(ctx context.Context, q execer, name string, r Retention) error {
	if strings.TrimSpace(name) == "" || r.Points <= 0 {
		return nil
	}
	var n int
	if err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM artifacts
		  WHERE coalesce(tombstone, false) = false AND kind = $1 AND fields->>'name' = $2`,
		MetricKind, name).Scan(&n); err != nil {
		return fmt.Errorf("store: count series %q: %w", name, err)
	}
	if n <= 2*r.Points {
		return nil
	}
	args := []any{MetricKind, name, r.Points}
	age := ""
	if r.Seconds > 0 {
		// w.created, not ar.created - ar is the subquery's alias and is out of
		// scope out here. The subquery selects it precisely so this can.
		age = ` OR w.created < now() - ($4 || ' seconds')::interval`
		args = append(args, r.Seconds)
	}
	_, err := q.ExecContext(ctx, `
		DELETE FROM artifacts WHERE id IN (
		  SELECT id FROM (
		    SELECT ar.id,
		           row_number() OVER (ORDER BY ar.hlc DESC, ar.id DESC) AS rn,
		           ar.created
		      FROM artifacts ar
		     WHERE coalesce(ar.tombstone, false) = false
		       AND ar.kind = $1
		       AND ar.fields->>'name' = $2
		  ) w WHERE w.rn > $3`+age+`
		)`, args...)
	if err != nil {
		return fmt.Errorf("store: prune series %q: %w", name, err)
	}
	return nil
}

// reportValueOf pulls fields.value back out for the report check. Returns nil
// when there is not one, which checkReportReading never sees because
// IsReportReading gates it.
func reportValueOf(fields json.RawMessage) json.RawMessage {
	var outer struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(fields, &outer); err != nil {
		return nil
	}
	return outer.Value
}
