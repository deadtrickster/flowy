package store

import (
	"encoding/json"
	"testing"
)

func logRow(fields string) *Artifact {
	return &Artifact{ID: "01M0", Type: MemoryType, Kind: LogKind, Fields: json.RawMessage(fields)}
}

// TestCheckLogRowShape asserts each rule on its own row, because they fail
// separately and a reader of the failure should learn which one went.
func TestCheckLogRowShape(t *testing.T) {
	for _, ok := range []string{
		`{"stream":"serened","message":"listening on 5432","level":"INFO","type":"Startup"}`,
		// LOWERCASE IS THE SAME LEVEL. A producer that writes "error" is not
		// making a different claim from one that writes "ERROR".
		`{"stream":"serened","message":"boom","level":"error"}`,
		// NO LEVEL AND NO TYPE. logs.py returns an unparseable line exactly like
		// this rather than dropping it - a crash dump is the thing you are
		// tailing for and it is not in the server's format.
		`{"stream":"serened","message":"Traceback (most recent call last):"}`,
		// WARN and WARNING are both real and neither is rewritten at the door.
		`{"stream":"s","message":"m","level":"WARN"}`,
		`{"stream":"s","message":"m","level":"WARNING"}`,
	} {
		if err := checkLogRow(logRow(ok)); err != nil {
			t.Fatalf("%s is a legal log line: %v", ok, err)
		}
	}

	for _, bad := range []struct{ fields, why string }{
		{`{"message":"m","level":"INFO"}`, "a line with no stream cannot be tailed"},
		{`{"stream":"  ","message":"m"}`, "a stream of blanks names nothing"},
		{`{"stream":"s","level":"INFO"}`, "a line with no message is not a line"},
		{`{"stream":"s","message":""}`, "an empty message is not a line"},
		{`{"stream":"s","message":"m","level":"NOTICE"}`, "a level nothing can count vanishes from every total"},
		{`{"stream":"s","message":"m","level":"critical"}`, "an invented level is still outside the vocabulary"},
	} {
		if err := checkLogRow(logRow(bad.fields)); err == nil {
			t.Fatalf("%s - %s, must be refused", bad.fields, bad.why)
		}
	}

	// A row of another kind is not this check's business.
	if err := checkLogRow(&Artifact{ID: "01M0", Type: MemoryType, Kind: MetricKind,
		Fields: json.RawMessage(`{"name":"m","value":1}`)}); err != nil {
		t.Fatalf("checkLogRow must leave other kinds alone: %v", err)
	}
	if err := checkLogRow(nil); err != nil {
		t.Fatalf("no row is not a bad row: %v", err)
	}
}
