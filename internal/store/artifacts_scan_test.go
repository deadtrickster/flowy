package store

import (
	"bytes"
	"encoding/json"
	"testing"
)

// emptyRow is a row where every column is NULL or empty - the state pq.Array
// leaves a destination nil in. Scan writes nothing and reports success, which
// is exactly what a real row with no tags produces.
type emptyRow struct{}

func (emptyRow) Scan(dest ...any) error { return nil }

// A ROW WITH NO TAGS MARSHALS AS [], NOT null.
//
// pq.Array leaves its destination nil for both a NULL column and an empty one,
// so every artifact nobody has tagged came back with three nil slices and Go
// marshalled each as null. Measured on the live node: 200 rows, all three
// fields null on every one of them.
//
// THIS IS THE HALF A FRESH-NODE WALK CANNOT SEE. That walk asks GETs on a node
// with nothing in it; a POST answer describing a row created a moment ago
// carries the same nil on a node full of data. A fresh ROW is an empty state
// too, and @flowy-claude found it on the attachment door answering a create.
//
// ASKED OF scanArtifact, not of an Artifact literal. The first cut of this test
// marshalled &Artifact{} and failed against a working fix, because a struct
// built by hand never goes through the path that defaults them - it was
// measuring the zero value rather than the read.
//
// And on the BYTES, because a nil slice and an empty one have the same length
// and differ only in what a reader receives.
func TestAnArtifactsListsMarshalAsArraysWhenEmpty(t *testing.T) {
	a, err := scanArtifact(emptyRow{}, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	out, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"tags":[]`, `"user_tags":[]`, `"related":[]`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("missing %s - a reader gets null and indexes it: %s", want, out)
		}
	}
}
