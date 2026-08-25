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
	})
	if err := checkDashboardRow(ok); err != nil {
		t.Fatalf("a dashboard declaring number, table, grid and frame tiles is a dashboard: %v", err)
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
