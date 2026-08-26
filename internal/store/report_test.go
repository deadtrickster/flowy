package store

import (
	"encoding/json"
	"testing"
)

func reading(value string) *Artifact {
	return &Artifact{ID: "01M0", Type: MemoryType, Kind: MetricKind,
		Fields: json.RawMessage(`{"name":"r","value":` + value + `}`)}
}

// TestCheckReportReading asserts each rule on its own document, because they
// fail separately and a producer should learn which.
func TestCheckReportReading(t *testing.T) {
	ok := []string{
		`{"title":"VL Sweep","sections":[{"kind":"progress","total":100,"value":44.3}]}`,
		`{"sections":[{"kind":"cards","cards":[{"title":"lab2x1"}]}]}`,
		`{"sections":[{"kind":"columns","columns":[{"label":"model"},{"label":"box"}],
		               "rows":[{"cells":["a","b"]},{"cells":["a"]}]}]}`,
		`{"sections":[{"kind":"squares","groups":[{"label":"lab2x1","rows":[{"label":"m","cells":[{"tone":"ok"}]}]}]}]}`,
		// EVERY SECTION KIND IN ONE DOCUMENT - the shape a real page has.
		`{"eyebrow":"three boxes","title":"t","lede":"l","sections":[
		   {"kind":"progress","total":5850,"value":44.3,"segments":[{"label":"a","value":1}]},
		   {"kind":"cards","cards":[]},
		   {"kind":"columns","columns":[],"rows":[]},
		   {"kind":"squares","groups":[]}]}`,
	}
	for _, v := range ok {
		if err := checkMetricRow(reading(v)); err != nil {
			t.Fatalf("%s is a legal report: %v", v[:60], err)
		}
	}

	for _, bad := range []struct{ value, why string }{
		{`{"sections":[]}`, "a report with no sections shows nothing"},
		{`{"sections":[{"kind":"chart"}]}`, "a kind outside the vocabulary cannot be drawn"},
		{`{"sections":[{"kind":"progress","value":44}]}`, "a progress bar with no total cannot place its segments"},
		{`{"sections":[{"kind":"columns","columns":[{"label":"one"}],"rows":[{"cells":["a","b"]}]}]}`,
			"a row that runs past its own header lies quietly"},
	} {
		if err := checkMetricRow(reading(bad.value)); err == nil {
			t.Fatalf("%s - %s, must be refused", bad.value, bad.why)
		}
	}

	// A READING THAT IS NOT A REPORT IS LEFT ALONE. The value is any JSON and
	// most readings are a number; this guard must not become a second opinion on
	// every metric written.
	for _, plain := range []string{`1`, `"a string"`, `{"cols":[],"rows":[]}`, `{"lines":["x"]}`, `[1,2,3]`} {
		if err := checkMetricRow(reading(plain)); err != nil {
			t.Fatalf("the plain reading %s was refused by the report guard: %v", plain, err)
		}
	}

	// AND report IS IN THE TILE VOCABULARY.
	if err := checkDashboardRow(dashboardRow([]map[string]any{
		{"kind": "report", "label": "the sweep", "metric": "r"},
	})); err != nil {
		t.Fatalf("a report tile is not in the vocabulary: %v", err)
	}
}
