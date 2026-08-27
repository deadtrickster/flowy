package store

import (
	"encoding/json"
	"testing"
)

// The shape checks themselves, asked directly: every case asserts the refusal
// is THERE, so a check that stopped checking reads as a red, not as
// agreement. The wire - that create, upsert and set-fields actually ask them
// - is proven by the console check the way every store invariant is.

func dashboardRow(tiles any) *Artifact {
	a := &Artifact{ID: "01TEST00000000000000000011", Type: MemoryType, Kind: DashboardKind}
	if tiles != nil {
		raw, err := json.Marshal(map[string]any{"tiles": tiles})
		if err != nil {
			panic(err)
		}
		a.Fields = raw
	}
	return a
}

func metricRow(name string, value any) *Artifact {
	a := &Artifact{ID: "01TEST00000000000000000012", Type: MemoryType, Kind: MetricKind}
	if name != "" || value != nil {
		raw, err := json.Marshal(map[string]any{"name": name, "value": value})
		if err != nil {
			panic(err)
		}
		a.Fields = raw
	}
	return a
}

func TestCheckDashboardRowLeavesOtherKindsAlone(t *testing.T) {
	for _, a := range []*Artifact{
		{Type: MemoryType, Kind: "todo"},
		{Type: MemoryType, Kind: MergeKind},
		nil,
	} {
		if err := checkDashboardRow(a); err != nil {
			t.Fatalf("a non-dashboard row must not be refused: %v", err)
		}
		if err := checkMetricRow(a); err != nil {
			t.Fatalf("a non-metric row must not be refused: %v", err)
		}
	}
}

func TestCheckDashboardRowShape(t *testing.T) {
	ok := dashboardRow([]map[string]any{
		{"kind": "number", "label": "cells done", "metric": "cells", "stale_after_seconds": 5},
		{"kind": "table", "label": "cells, latest rows", "metric": "cells"},
		{"kind": "grid", "label": "coverage", "metric": "cells", "stale_after_seconds": 5},
		{"kind": "series", "label": "cells over time", "metric": "cells", "points": 60},
		{"kind": "log", "label": "the build tail", "metric": "build"},
		{"kind": "trace", "label": "the broken ask", "trace": "AABBCCDDEEFF00112233445566778899"},
	})
	if err := checkDashboardRow(ok); err != nil {
		t.Fatalf("a dashboard declaring number, table, grid, frame, series, log and trace tiles is a dashboard: %v", err)
	}
	// THE METRIC-LESS TILE: its query is the id, and both a missing one and a
	// stray series name beside one are refused by name rather than drawn wrong.
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "trace", "label": "the broken ask"},
	})); err == nil {
		t.Fatal("a trace tile naming no id is a query over nothing - must be refused")
	}
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "trace", "label": "the broken ask", "trace": "not-hex"},
	})); err == nil {
		t.Fatal("a trace tile naming a malformed id must be refused with the door's own rule")
	}
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "trace", "label": "the broken ask", "trace": "AABBCCDDEEFF00112233445566778899", "metric": "cells"},
	})); err == nil {
		t.Fatal("a trace tile carrying a metric must be refused - a stray series name would be silently ignored")
	}
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "number", "label": "cells", "metric": "cells", "trace": "AABBCCDDEEFF00112233445566778899"},
	})); err == nil {
		t.Fatal("a non-trace tile carrying a trace id must be refused - a stray id would be silently ignored")
	}
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "series", "label": "cells over time", "metric": "cells", "points": -1},
	})); err == nil {
		t.Fatal("a series tile declaring a negative window must be refused")
	}
	if err := checkDashboardRow(dashboardRow(nil)); err == nil {
		t.Fatal("a dashboard with no tiles declares nothing - must be refused")
	}
	if err := checkDashboardRow(dashboardRow([]any{})); err == nil {
		t.Fatal("a dashboard with an empty tiles array declares nothing - must be refused")
	}
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "bar", "label": "trend", "metric": "cells"},
	})); err == nil {
		t.Fatal("a tile kind outside the vocabulary must be refused, naming it")
	}
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "number", "label": "", "metric": "cells"},
	})); err == nil {
		t.Fatal("a tile with no label names nothing a person reads - must be refused")
	}
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "number", "label": "cells", "metric": "  "},
	})); err == nil {
		t.Fatal("a tile naming no metric is a query over nothing - must be refused")
	}
}

func TestCheckMetricRowShape(t *testing.T) {
	if err := checkMetricRow(metricRow("cells", 1200)); err != nil {
		t.Fatalf("a named reading is a metric: %v", err)
	}
	if err := checkMetricRow(metricRow("rate", 4.2)); err != nil {
		t.Fatalf("a fractional reading is a metric: %v", err)
	}
	if err := checkMetricRow(&Artifact{ID: "01TEST00000000000000000013",
		Type: MemoryType, Kind: MetricKind}); err == nil {
		t.Fatal("a metric with no fields names no series and carries no reading - must be refused")
	}
	if err := checkMetricRow(metricRow("   ", 1200)); err == nil {
		t.Fatal("a metric with a blank name names no series - must be refused")
	}
	if err := checkMetricRow(metricRow("cells", nil)); err == nil {
		t.Fatal("a metric whose value key is absent carries no reading - must be refused")
	}
	stated := func(state string) *Artifact {
		return &Artifact{ID: "01TEST00000000000000000014",
			Type: MemoryType, Kind: MetricKind,
			Fields: json.RawMessage(`{"name":"cells","value":1200,"state":"` + state + `"}`)}
	}
	for _, ok := range []string{"measured", "inferred", "unknown"} {
		if err := checkMetricRow(stated(ok)); err != nil {
			t.Fatalf("a reading that claims to be %s is a metric: %v", ok, err)
		}
	}
	if err := checkMetricRow(stated("guessed")); err == nil {
		t.Fatal("a reading claiming a state outside the three must be refused by name - measured, inferred, unknown are the states, nothing else")
	}
	if err := checkMetricRow(metricRow("cells", 1200)); err != nil {
		t.Fatalf("a reading that claims no state is a metric - absent is unknown, not an error: %v", err)
	}
}

func TestMetricStateOf(t *testing.T) {
	if got := MetricStateOf(&Artifact{Type: MemoryType, Kind: MetricKind,
		Fields: json.RawMessage(`{"name":"cells","value":1200}`)}); got != "unknown" {
		t.Fatalf("a reading that claims no state reads as unknown, got %q", got)
	}
	if got := MetricStateOf(&Artifact{Type: MemoryType, Kind: MetricKind,
		Fields: json.RawMessage(`{"name":"cells","value":1200,"state":"inferred"}`)}); got != "inferred" {
		t.Fatalf("a claimed state reads back, got %q", got)
	}
	if got := MetricStateOf(&Artifact{Type: MemoryType, Kind: MetricKind,
		Fields: json.RawMessage(`{"name":"cells","value":1200,"state":"GUESSED"}`)}); got != "unknown" {
		t.Fatalf("an unrecognised state reads as unknown, got %q", got)
	}
	if got := MetricStateOf(nil); got != "unknown" {
		t.Fatalf("no row reads as unknown, got %q", got)
	}
}

func TestDashboardTilesOfIsLenientForReaders(t *testing.T) {
	tiles, err := DashboardTilesOf(&Artifact{Type: MemoryType, Kind: DashboardKind})
	if err != nil || len(tiles) != 0 {
		t.Fatalf("absent fields reads as no tiles, not an error: %v, %v", tiles, err)
	}
	tiles, err = DashboardTilesOf(&Artifact{ID: "01TEST00000000000000000014",
		Type: MemoryType, Kind: DashboardKind,
		Fields: json.RawMessage(`{"tiles":[{"kind":"number","label":"n","metric":"m"}]}`)})
	if err != nil || len(tiles) != 1 || tiles[0].Metric != "m" {
		t.Fatalf("a declared tile parses: %v, %v", tiles, err)
	}
	if _, err := DashboardTilesOf(&Artifact{Type: MemoryType, Kind: DashboardKind,
		Fields: json.RawMessage(`not json`)}); err == nil {
		t.Fatal("unparsable fields is an error - a row this code cannot read")
	}
}

// TestRetentionOf asserts each rule on its own row - default, honoured, clamped,
// and the two shapes that must NOT be treated as a request.
func TestRetentionOf(t *testing.T) {
	row := func(fields string) *Artifact {
		return &Artifact{ID: "01M0", Type: "memory", Kind: MetricKind, Fields: json.RawMessage(fields)}
	}

	if got := RetentionOf(row(`{"name":"a","value":1}`)); got.Points != RetainDefaultPoints || got.Seconds != 0 {
		t.Fatalf("a reading that says nothing gets the default, got %+v", got)
	}
	if got := RetentionOf(nil); got.Points != RetainDefaultPoints {
		t.Fatalf("no row is the default too, got %+v", got)
	}
	if got := RetentionOf(row(`{"name":"a","value":1,"retain":{"points":10,"seconds":60}}`)); got.Points != 10 || got.Seconds != 60 {
		t.Fatalf("a producer's own retention must be honoured, got %+v", got)
	}
	if got := RetentionOf(row(`{"name":"a","value":1,"retain":{"seconds":60}}`)); got.Points != RetainDefaultPoints || got.Seconds != 60 {
		t.Fatalf("an age bound alone keeps the default count, got %+v", got)
	}
	// A CEILING THE PRODUCER CANNOT RAISE. "keep ten million" is a denial of
	// service written as a preference.
	if got := RetentionOf(row(`{"name":"a","value":1,"retain":{"points":10000000}}`)); got.Points != RetainMaxPoints {
		t.Fatalf("a retention hint above the ceiling must be clamped to it, got %+v", got)
	}
	// UNPARSABLE IS THE DEFAULT, NOT AN ERROR. This is read on the write path,
	// and losing a measurement to protect the housekeeping is the wrong trade.
	if got := RetentionOf(row(`{"name":"a","value":1,"retain":"soon"}`)); got.Points != RetainDefaultPoints {
		t.Fatalf("a malformed retention hint must not change the default, got %+v", got)
	}
	if got := RetentionOf(row(`{"name":"a","value":1,"retain":{"points":-5}}`)); got.Points != RetainDefaultPoints {
		t.Fatalf("a negative count is not a request to keep nothing, got %+v", got)
	}
}

// TestAGaugeCarriesItsScaleOnTheReading asserts each rule on its own row. The
// scale is the PRODUCER's to declare - spec 01M0XG29064XBAQCX8J16QK1E9 point 2 -
// so these are metric rows, and the tile arm at the bottom proves the other
// home is closed.
func TestAGaugeCarriesItsScaleOnTheReading(t *testing.T) {
	reading := func(fields string) *Artifact {
		return &Artifact{ID: "01M0", Type: MemoryType, Kind: MetricKind, Fields: json.RawMessage(fields)}
	}

	for _, ok := range []string{
		`{"name":"m","value":3}`,
		`{"name":"m","value":3,"min":0,"max":64}`,
		`{"name":"m","value":3,"min":0,"max":64,"thresholds":{"warn":48,"crit":60}}`,
		// LOW IS BAD, and it must be expressible: free disk, remaining quota.
		// crit below warn is the direction, not an error.
		`{"name":"m","value":3,"min":0,"max":100,"thresholds":{"warn":20,"crit":5}}`,
		// One threshold alone is a legitimate half-declaration - plenty of
		// gauges warn and never have a hard limit.
		`{"name":"m","value":3,"min":0,"max":64,"thresholds":{"warn":48}}`,
		// A scale that spans zero, for a temperature or a delta.
		`{"name":"m","value":3,"min":-40,"max":40}`,
	} {
		if err := checkMetricRow(reading(ok)); err != nil {
			t.Fatalf("%s is a legal reading: %v", ok, err)
		}
	}

	for _, bad := range []struct{ fields, why string }{
		{`{"name":"m","value":3,"min":0}`, "min without max is half a scale"},
		{`{"name":"m","value":3,"max":64}`, "max without min is half a scale"},
		{`{"name":"m","value":3,"min":64,"max":64}`, "a scale that does not ascend cannot place a reading"},
		{`{"name":"m","value":3,"min":64,"max":0}`, "an inverted scale is not a downward gauge"},
		{`{"name":"m","value":3,"thresholds":{"warn":48}}`, "a threshold with no scale marks nothing"},
		{`{"name":"m","value":3,"min":0,"max":64,"thresholds":{"warn":90}}`, "a mark off the bar can never be reached"},
		{`{"name":"m","value":3,"min":0,"max":64,"thresholds":{"crit":-1}}`, "a mark below the bar can never be reached"},
	} {
		if err := checkMetricRow(reading(bad.fields)); err == nil {
			t.Fatalf("%s - %s, must be refused", bad.fields, bad.why)
		}
	}

	// ZERO IS A DECLARED FLOOR. The arm a plain float64 fails silently.
	if err := checkMetricRow(reading(`{"name":"m","value":3,"min":0,"max":64}`)); err != nil {
		t.Fatalf("min 0 is declared, not missing: %v", err)
	}

	// THE OTHER HOME IS CLOSED. A tile carrying a scale is refused by name
	// rather than ignored - an ignored bound draws an unscaled bar and reads
	// as a rendering bug.
	for _, tile := range []map[string]any{
		{"kind": "gauge", "label": "used", "metric": "m", "min": 0, "max": 64},
		{"kind": "gauge", "label": "used", "metric": "m", "thresholds": map[string]any{"warn": 48}},
		{"kind": "number", "label": "used", "metric": "m", "max": 64},
	} {
		if err := checkDashboardRow(dashboardRow([]map[string]any{tile})); err == nil {
			t.Fatalf("tile %v carries a scale - must be refused, naming where it belongs", tile)
		}
	}

	// AND gauge IS IN THE VOCABULARY, carrying nothing but its metric and kind.
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "gauge", "label": "memory used", "metric": "m"},
	})); err != nil {
		t.Fatalf("a gauge tile names a metric and a kind and nothing else: %v", err)
	}
}
