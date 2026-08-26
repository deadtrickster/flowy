package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
)

// StackKind is one stacktrace: an ordered list of frames, innermost first.
//
// A TYPE RATHER THAN PROSE IN A BODY, for spec 01M0XG29064XBAQCX8J16QK1E9 point
// 3's stated reason - "nothing can filter or count them". A traceback pasted
// into a body cannot answer "which crashes go through this symbol" or "what is
// the commonest top frame", and those are the only two questions anybody asks
// of a pile of stacktraces.
const StackKind = "stacktrace"

// StackFrame is one line of a traceback.
//
// SYMBOL OR FILE, NOT BOTH REQUIRED. A Go panic has both; a stripped C frame has
// only an address-derived symbol; a script traceback sometimes has only a file
// and a line. A frame with NEITHER is not a frame - it is a blank line that
// would sit in every count as an unnamed bucket.
type StackFrame struct {
	Symbol string `json:"symbol,omitempty"`
	File   string `json:"file,omitempty"`
	// Line is 1-based where it is known. Zero means unknown rather than the
	// first line: a frame at "line 0" is a frame nobody located.
	Line int `json:"line,omitempty"`
}

// StackRowError is the stacktrace twin of LogRowError.
type StackRowError struct {
	Row string
	Why string
}

func (e StackRowError) Error() string { return fmt.Sprintf("stacktrace row %s: %s", e.Row, e.Why) }

func (e StackRowError) depRefusal() {}

// IsStack reports whether the row is a stacktrace.
func IsStack(a *Artifact) bool { return IsEntityType(a, StackKind) }

// checkStackRow is checkLogRow's twin, asked at the same three statements.
//
//   - a stacktrace with no stream cannot be found again
//   - a stacktrace with no frames is a claim that something crashed with no
//     account of where, which is what a body full of prose already was
//   - a frame with neither symbol nor file is a blank line, and it would sit in
//     every group-by as an unnamed bucket nobody can act on
//   - a negative line number is not an unknown one. Zero already means unknown,
//     so a negative is a producer bug and saying so beats storing it
func checkStackRow(a *Artifact) error {
	if a == nil || !IsStack(a) {
		return nil
	}
	if len(a.Fields) == 0 {
		return StackRowError{Row: a.ID,
			Why: "a stacktrace names its stream and carries its frames - fields.stream and fields.frames must be there"}
	}
	var outer struct {
		Stream string       `json:"stream"`
		Frames []StackFrame `json:"frames"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil {
		return StackRowError{Row: a.ID, Why: "fields is not JSON: " + err.Error()}
	}
	if strings.TrimSpace(outer.Stream) == "" {
		return StackRowError{Row: a.ID, Why: "a stacktrace belongs to a stream - fields.stream must name one"}
	}
	if len(outer.Frames) == 0 {
		return StackRowError{Row: a.ID,
			Why: "a stacktrace is its frames - fields.frames must carry at least one"}
	}
	for i, f := range outer.Frames {
		if strings.TrimSpace(f.Symbol) == "" && strings.TrimSpace(f.File) == "" {
			return StackRowError{Row: a.ID, Why: fmt.Sprintf(
				"frame %d has neither symbol nor file - a frame that names nowhere cannot be grouped or followed", i)}
		}
		if f.Line < 0 {
			return StackRowError{Row: a.ID, Why: fmt.Sprintf(
				"frame %d has line %d - zero already means unknown, so a negative is a producer bug", i, f.Line)}
		}
	}
	return nil
}

// Stack is one stacktrace as a reader gets it back.
type Stack struct {
	ID     string       `json:"id"`
	At     int64        `json:"at"`
	Stream string       `json:"stream"`
	Title  string       `json:"title"`
	Frames []StackFrame `json:"frames"`
}

// StackCounts groups the matched traces by their TOP frame, which is the one
// question a pile of stacktraces exists to answer: where is this crashing.
type StackCounts struct {
	TopFrames map[string]int `json:"top_frames"`
}

// StacksThrough answers "the stacktraces of this stream, optionally only those
// passing through a symbol or a file", newest first, with a count by top frame.
//
// NEWEST FIRST, and deliberately NOT oldest-first like the log tail and the
// series door. Those are read as a run of time - a sparkline, a log scrolled top
// to bottom. A pile of crashes is read as "what is happening now", and the
// newest is the one being investigated.
//
// THE FILTER IS SUBSTRING OVER EVERY FRAME, not just the top one. "Does this
// crash go through decodeBody" is the question, and the answer is usually three
// frames down.
//
// THE COUNTS ARE OVER THE WHOLE MATCHED SET, not the page, for the reason
// TailLogs' are: a header that changes as you scroll is describing the scroll.
func (d *DB) StacksThrough(ctx context.Context, p *Principal, stream, symbol, file string,
	limit int,
) ([]*Stack, *StackCounts, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "stacks.read")
	defer span.End()
	if strings.TrimSpace(stream) == "" {
		return nil, nil, fmt.Errorf("store: read stacktraces: a stream must be named")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	a := &args{}
	filter := ArtifactFilterSQL(p, "ar", a, false)
	where := []string{
		"coalesce(ar.tombstone, false) = false",
		filter,
		"ar.kind = '" + StackKind + "'",
		"ar.fields->>'stream' = " + a.next(stream),
	}
	// EXISTS over the frames array rather than a containment operator: @> is an
	// exact match on a whole object and cannot answer "a symbol containing
	// this", which is what a person types.
	if strings.TrimSpace(symbol) != "" {
		where = append(where, `EXISTS (SELECT 1 FROM jsonb_array_elements(ar.fields->'frames') fr
		         WHERE lower(coalesce(fr->>'symbol', '')) LIKE `+a.next("%"+strings.ToLower(symbol)+"%")+`)`)
	}
	if strings.TrimSpace(file) != "" {
		where = append(where, `EXISTS (SELECT 1 FROM jsonb_array_elements(ar.fields->'frames') fr
		         WHERE lower(coalesce(fr->>'file', '')) LIKE `+a.next("%"+strings.ToLower(file)+"%")+`)`)
	}
	cond := strings.Join(where, "\n AND ")

	counts := &StackCounts{TopFrames: map[string]int{}}
	crows, err := d.sql.QueryContext(ctx, `
		SELECT coalesce(nullif(ar.fields->'frames'->0->>'symbol', ''),
		                ar.fields->'frames'->0->>'file', '') AS top,
		       count(*)
		  FROM artifacts ar
		 WHERE `+cond+`
		 GROUP BY 1`, a.vals...)
	if err != nil {
		span.Fail("the stacktrace counts did not run")
		return nil, nil, fmt.Errorf("store: count stacktraces: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var top string
		var n int
		if err := crows.Scan(&top, &n); err != nil {
			return nil, nil, fmt.Errorf("store: count stacktraces: %w", err)
		}
		if top != "" {
			counts.TopFrames[top] += n
		}
	}
	if err := crows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: count stacktraces: %w", err)
	}

	rows, err := d.sql.QueryContext(ctx, `
		SELECT ar.id, ar.hlc, ar.fields->>'stream', coalesce(ar.title, ''), ar.fields->'frames'
		  FROM artifacts ar
		 WHERE `+cond+`
		 ORDER BY ar.hlc DESC, ar.id DESC
		 LIMIT `+a.next(limit), a.vals...)
	if err != nil {
		span.Fail("the stacktrace read did not run")
		return nil, nil, fmt.Errorf("store: read stacktraces: %w", err)
	}
	defer rows.Close()
	out := []*Stack{}
	for rows.Next() {
		var s Stack
		var raw []byte
		if err := rows.Scan(&s.ID, &s.At, &s.Stream, &s.Title, &raw); err != nil {
			return nil, nil, fmt.Errorf("store: read stacktraces: %w", err)
		}
		if err := json.Unmarshal(raw, &s.Frames); err != nil {
			return nil, nil, fmt.Errorf("store: read stacktraces: frames of %s: %w", s.ID, err)
		}
		out = append(out, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("store: read stacktraces: %w", err)
	}
	return out, counts, nil
}
