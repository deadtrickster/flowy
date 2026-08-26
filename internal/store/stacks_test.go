package store

import (
	"encoding/json"
	"testing"
)

func stackRow(fields string) *Artifact {
	return &Artifact{ID: "01M0", Type: MemoryType, Kind: StackKind, Fields: json.RawMessage(fields)}
}

// TestCheckStackRowShape asserts each rule on its own row.
func TestCheckStackRowShape(t *testing.T) {
	for _, ok := range []string{
		`{"stream":"serened","frames":[{"symbol":"main.run","file":"main.go","line":42}]}`,
		// SYMBOL ALONE - a stripped C frame resolved from an address.
		`{"stream":"s","frames":[{"symbol":"0x7f2a1c"}]}`,
		// FILE ALONE, with a line - a script traceback that never names a function.
		`{"stream":"s","frames":[{"file":"app.py","line":9}]}`,
		// LINE ZERO IS UNKNOWN, not the first line, and is legal.
		`{"stream":"s","frames":[{"symbol":"f","file":"a.c"}]}`,
		// Depth is not bounded here - a real traceback is long.
		`{"stream":"s","frames":[{"symbol":"a"},{"symbol":"b"},{"symbol":"c"},{"symbol":"d"}]}`,
	} {
		if err := checkStackRow(stackRow(ok)); err != nil {
			t.Fatalf("%s is a legal stacktrace: %v", ok, err)
		}
	}

	for _, bad := range []struct{ fields, why string }{
		{`{"frames":[{"symbol":"f"}]}`, "a stacktrace with no stream cannot be found again"},
		{`{"stream":"   ","frames":[{"symbol":"f"}]}`, "a stream of blanks names nothing"},
		{`{"stream":"s"}`, "a stacktrace is its frames"},
		{`{"stream":"s","frames":[]}`, "an empty frames array accounts for nothing"},
		{`{"stream":"s","frames":[{"line":9}]}`, "a frame with neither symbol nor file names nowhere"},
		{`{"stream":"s","frames":[{"symbol":"  ","file":"  "}]}`, "blanks are not names"},
		{`{"stream":"s","frames":[{"symbol":"a"},{"file":""}]}`, "a blank frame anywhere in the list, not just first"},
		{`{"stream":"s","frames":[{"symbol":"f","line":-1}]}`, "zero already means unknown, so a negative is a bug"},
	} {
		if err := checkStackRow(stackRow(bad.fields)); err == nil {
			t.Fatalf("%s - %s, must be refused", bad.fields, bad.why)
		}
	}

	if err := checkStackRow(&Artifact{ID: "01M0", Type: MemoryType, Kind: MetricKind,
		Fields: json.RawMessage(`{"name":"m","value":1}`)}); err != nil {
		t.Fatalf("checkStackRow must leave other kinds alone: %v", err)
	}
	if err := checkStackRow(nil); err != nil {
		t.Fatalf("no row is not a bad row: %v", err)
	}
}
