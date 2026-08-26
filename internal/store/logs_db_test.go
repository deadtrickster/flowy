package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestTailLogsFiltersAndCounts runs the tail against a real database, because a
// SQL string that only ever compiled is not a query anybody has run.
func TestTailLogsFiltersAndCounts(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "logs-tail")
	p := &Principal{UserID: "u-" + ulid.NewString(), Project: project}
	stream := "serened." + ulid.NewString()

	push := func(level, typ, msg string) {
		t.Helper()
		f := map[string]any{"stream": stream, "message": msg}
		if level != "" {
			f["level"] = level
		}
		if typ != "" {
			f["type"] = typ
		}
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("fields: %v", err)
		}
		art := &Artifact{
			ID: ulid.NewString(), Type: MemoryType, Kind: LogKind,
			Project: &project, OwnerUser: p.UserID, Title: stream, Fields: raw,
		}
		if err := db.CreateArtifact(ctx, art); err != nil {
			t.Fatalf("push %q: %v", msg, err)
		}
	}
	msgs := func(ls []*LogLine) string {
		out := make([]string, 0, len(ls))
		for _, l := range ls {
			out = append(out, l.Message)
		}
		return strings.Join(out, ",")
	}

	push("INFO", "Startup", "one")
	push("ERROR", "Storage", "two")
	push("INFO", "Storage", "three")
	push("", "", "Traceback (most recent call last):")
	push("ERROR", "Search", "five")

	// OLDEST FIRST. A log read top to bottom runs forwards.
	got, counts, err := db.TailLogs(ctx, p, stream, "", nil, nil, 0)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if msgs(got) != "one,two,three,Traceback (most recent call last):,five" {
		t.Fatalf("oldest first, got %q", msgs(got))
	}

	// COUNTS SKIP THE EMPTY LEVEL AND THE EMPTY TYPE rather than counting them
	// under "". An unparseable line has no level; it is not a level of its own.
	if counts.Levels["INFO"] != 2 || counts.Levels["ERROR"] != 2 || len(counts.Levels) != 2 {
		t.Fatalf("levels over the window, got %v", counts.Levels)
	}
	if counts.Types["Storage"] != 2 || counts.Types["Startup"] != 1 || counts.Types["Search"] != 1 ||
		len(counts.Types) != 3 {
		t.Fatalf("types over the window, got %v", counts.Types)
	}

	// A LIMIT TAKES THE NEWEST, and still hands them back oldest first. A limit
	// that took the OLDEST would pass a length check and be useless as a tail.
	got, _, err = db.TailLogs(ctx, p, stream, "", nil, nil, 2)
	if err != nil {
		t.Fatalf("tail limited: %v", err)
	}
	if msgs(got) != "Traceback (most recent call last):,five" {
		t.Fatalf("the last two, oldest first, got %q", msgs(got))
	}

	// THE NEEDLE MATCHES THE MESSAGE, case-insensitively.
	got, _, err = db.TailLogs(ctx, p, stream, "TRACEBACK", nil, nil, 0)
	if err != nil {
		t.Fatalf("needle: %v", err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0].Message, "Traceback") {
		t.Fatalf("a needle matches the message whatever its case, got %q", msgs(got))
	}

	// AND IT MATCHES THE TYPE, which is what makes typing a subsystem name
	// behave the way a person expects.
	got, _, err = db.TailLogs(ctx, p, stream, "storage", nil, nil, 0)
	if err != nil {
		t.Fatalf("needle on type: %v", err)
	}
	if msgs(got) != "two,three" {
		t.Fatalf("a needle matches the type too, got %q", msgs(got))
	}

	// LEVELS ARE EXACT AND CASE-FOLDED.
	got, _, err = db.TailLogs(ctx, p, stream, "", []string{"error"}, nil, 0)
	if err != nil {
		t.Fatalf("level filter: %v", err)
	}
	if msgs(got) != "two,five" {
		t.Fatalf("level filter, got %q", msgs(got))
	}

	got, _, err = db.TailLogs(ctx, p, stream, "", nil, []string{"Search"}, 0)
	if err != nil {
		t.Fatalf("type filter: %v", err)
	}
	if msgs(got) != "five" {
		t.Fatalf("type filter, got %q", msgs(got))
	}

	// THE COUNTS DESCRIBE THE FILTER, NOT THE PAGE. This is the arm that fails
	// if the counts are computed over the limited rows: the filter selects two
	// ERROR lines and the page shows one.
	got, counts, err = db.TailLogs(ctx, p, stream, "", []string{"ERROR"}, nil, 1)
	if err != nil {
		t.Fatalf("counts over the filter: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the page is one line, got %d", len(got))
	}
	if counts.Levels["ERROR"] != 2 {
		t.Fatalf("the counts describe the filtered set, not the page - got %v", counts.Levels)
	}

	// A STREAM NOBODY WROTE IS EMPTY, NOT AN ERROR.
	got, counts, err = db.TailLogs(ctx, p, "nobody."+ulid.NewString(), "", nil, nil, 0)
	if err != nil {
		t.Fatalf("an unwritten stream is empty, not broken: %v", err)
	}
	if len(got) != 0 || len(counts.Levels) != 0 {
		t.Fatalf("an unwritten stream has nothing in it, got %d lines %v", len(got), counts.Levels)
	}

	// AND A STREAM MUST BE NAMED - "every line on this node" is not a tail.
	if _, _, err := db.TailLogs(ctx, p, "  ", "", nil, nil, 0); err == nil {
		t.Fatal("a tail with no stream must be refused")
	}
}
