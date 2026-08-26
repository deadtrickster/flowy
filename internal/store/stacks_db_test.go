package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestStacksThroughFiltersAndCountsTopFrames runs the read against a real
// database - the EXISTS over jsonb_array_elements and the top-frame group-by are
// the parts no unit test reaches.
func TestStacksThroughFiltersAndCountsTopFrames(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "stacks-read")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	stream := "serened." + ulid.NewString()

	push := func(title string, frames []map[string]any) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"stream": stream, "frames": frames})
		if err != nil {
			t.Fatalf("fields: %v", err)
		}
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: StackKind,
			Project: &project, OwnerUser: p.UserID, Title: title, Fields: raw,
		}
		if err := db.CreateArtifact(ctx, art); err != nil {
			t.Fatalf("push %q: %v", title, err)
		}
	}
	titles := func(ss []*Stack) string {
		out := make([]string, 0, len(ss))
		for _, s := range ss {
			out = append(out, s.Title)
		}
		return strings.Join(out, ",")
	}

	push("first", []map[string]any{
		{"symbol": "decodeBody", "file": "http.go", "line": 12},
		{"symbol": "serveHTTP", "file": "http.go", "line": 90},
	})
	push("second", []map[string]any{
		{"symbol": "decodeBody", "file": "http.go", "line": 12},
		{"symbol": "worker", "file": "pool.go", "line": 7},
	})
	push("third", []map[string]any{
		{"symbol": "flush", "file": "store.go", "line": 44},
		{"symbol": "decodeBody", "file": "http.go", "line": 12},
	})
	// A frame located only by file, to prove the top-frame count falls back to
	// it rather than dropping the trace out of every total.
	push("fourth", []map[string]any{{"file": "vendor/lib.c", "line": 3}})

	// NEWEST FIRST - a pile of crashes is read as "what is happening now".
	got, counts, err := db.StacksThrough(ctx, p, stream, "", "", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if titles(got) != "fourth,third,second,first" {
		t.Fatalf("newest first, got %q", titles(got))
	}
	if len(got[3].Frames) != 2 || got[3].Frames[0].Symbol != "decodeBody" || got[3].Frames[0].Line != 12 {
		t.Fatalf("frames must round-trip in order with their lines, got %+v", got[3].Frames)
	}

	// TOP FRAME COUNTS, with the file used when the top frame has no symbol.
	if counts.TopFrames["decodeBody"] != 2 || counts.TopFrames["flush"] != 1 ||
		counts.TopFrames["vendor/lib.c"] != 1 || len(counts.TopFrames) != 3 {
		t.Fatalf("top frames, got %v", counts.TopFrames)
	}

	// THE SYMBOL FILTER LOOKS AT EVERY FRAME, not just the top one. "third" has
	// decodeBody SECOND, and it is the arm a top-frame-only filter fails.
	got, _, err = db.StacksThrough(ctx, p, stream, "decodebody", "", 0)
	if err != nil {
		t.Fatalf("symbol filter: %v", err)
	}
	if titles(got) != "third,second,first" {
		t.Fatalf("a symbol filter matches any frame, case-insensitively - got %q", titles(got))
	}

	got, _, err = db.StacksThrough(ctx, p, stream, "", "pool.go", 0)
	if err != nil {
		t.Fatalf("file filter: %v", err)
	}
	if titles(got) != "second" {
		t.Fatalf("a file filter matches any frame, got %q", titles(got))
	}

	// COUNTS DESCRIBE THE FILTER, NOT THE PAGE.
	got, counts, err = db.StacksThrough(ctx, p, stream, "decodebody", "", 1)
	if err != nil {
		t.Fatalf("counts over the filter: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the page is one trace, got %d", len(got))
	}
	if counts.TopFrames["decodeBody"] != 2 || counts.TopFrames["flush"] != 1 {
		t.Fatalf("the counts describe the filtered set, not the page - got %v", counts.TopFrames)
	}

	// AN UNWRITTEN STREAM IS EMPTY, NOT AN ERROR - and a stream must be named.
	got, counts, err = db.StacksThrough(ctx, p, "nobody."+ulid.NewString(), "", "", 0)
	if err != nil {
		t.Fatalf("an unwritten stream is empty, not broken: %v", err)
	}
	if len(got) != 0 || len(counts.TopFrames) != 0 {
		t.Fatalf("an unwritten stream has nothing in it, got %d", len(got))
	}
	if _, _, err := db.StacksThrough(ctx, p, " ", "", "", 0); err == nil {
		t.Fatal("a read with no stream must be refused")
	}
}
