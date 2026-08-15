package otel

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// recorder collects the spans a tracer finishes, in the order it finished them.
type recorder struct {
	mu    sync.Mutex
	spans []*Span
}

func (r *recorder) RecordSpan(_ context.Context, s *Span) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, s)
	return nil
}

func (r *recorder) traceIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.spans))
	for _, s := range r.spans {
		out = append(out, s.TraceID)
	}
	return out
}

// A span opened with no tracer in the context records nothing and does not
// panic. Every store method is instrumented, and most of them are also called
// from a CLI, a drainer and a test that never set a tracer up.
func TestSpanWithoutATracerIsInert(t *testing.T) {
	ctx, span := Start(context.Background(), KindQuery, "artifacts.list")
	span.SetAttr("k", "v")
	span.SetArtifact("01ABC")
	span.OK()
	span.End()
	if got := TraceID(ctx); got != "" {
		t.Fatalf("a context with no tracer is in trace %q, want none", got)
	}
	if span.Attrs() != nil {
		t.Fatalf("an inert span kept attributes: %v", span.Attrs())
	}
}

// The point of the buffering: a request that discovers half-way through which
// trace it belongs to takes its children with it. Without that, the permission
// check would sit in a trace of one and the handoff's trace would be missing
// the reason its rows were readable.
func TestAdoptMovesTheWholeRequestIntoTheTrace(t *testing.T) {
	rec := &recorder{}
	tracer := New("nodeA")
	tracer.SetRecorder(rec)

	ctx := WithTracer(context.Background(), tracer)
	ctx, root := Start(ctx, KindServer, "POST /api/task/{id}/state")

	childCtx, child := Start(ctx, KindPermission, "principal.resolve")
	child.End()
	_ = childCtx

	const arrived = "aabbccddeeff00112233445566778899"
	if !Adopt(ctx, arrived) {
		t.Fatal("Adopt refused a valid trace id")
	}
	after, second := Start(ctx, KindQuery, "events.list")
	second.End()
	_ = after
	root.End()

	for _, got := range rec.traceIDs() {
		if got != arrived {
			t.Fatalf("a span stayed in trace %q, want every span in %q", got, arrived)
		}
	}
	if len(rec.spans) != 3 {
		t.Fatalf("recorded %d spans, want 3", len(rec.spans))
	}
}

// A trace id off the wire is somebody else's string. One that is not 32 hex
// digits - or is all zeroes, which the W3C format calls invalid - is refused
// rather than used, because a trace nobody can query is worse than a new one.
func TestBadTraceIDsAreRefused(t *testing.T) {
	for _, id := range []string{
		"", "not-a-trace", "00000000000000000000000000000000",
		"aabbccddeeff0011223344556677889", // 31
		"aabbccddeeff001122334455667788999",
		"zzbbccddeeff00112233445566778899",
	} {
		if ValidTraceID(id) {
			t.Errorf("ValidTraceID(%q) is true, want false", id)
		}
	}
	if !ValidTraceID("AABBCCDDEEFF00112233445566778899") {
		t.Error("an upper-case trace id was refused")
	}
}

func TestTraceParentRoundTrips(t *testing.T) {
	rec := &recorder{}
	tracer := New("nodeA")
	tracer.SetRecorder(rec)
	_, span := Start(WithTracer(context.Background(), tracer), KindSync, "sync.run")

	header := TraceParent(span)
	trace, parent, ok := ParseTraceParent(header)
	if !ok {
		t.Fatalf("ParseTraceParent(%q) refused a header this package wrote", header)
	}
	if trace != span.TraceID || parent != span.SpanID {
		t.Fatalf("round trip gave %s/%s, want %s/%s", trace, parent, span.TraceID, span.SpanID)
	}
	if _, _, ok := ParseTraceParent("01-" + span.TraceID + "-" + span.SpanID + "-01"); ok {
		t.Error("a header of an unknown version was accepted")
	}
	if _, _, ok := ParseTraceParent("nonsense"); ok {
		t.Error("a header that is not a traceparent was accepted")
	}
}

// The payload is the protocol's, not this package's: a collector that rejects
// it says nothing useful about why, so the field names are asserted here.
func TestOTLPPayloadShape(t *testing.T) {
	rec := &recorder{}
	tracer := New("nodeA")
	tracer.SetRecorder(rec)
	ctx := WithTracer(context.Background(), tracer)
	_, span := Start(ctx, KindServer, "GET /api/artifacts")
	span.SetArtifact("01ARTIFACT")
	span.Fail("the node answered 500")
	span.End()

	raw, err := json.Marshal(Payload("flowy", "nodeA", []*Span{span}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					TraceID string `json:"traceId"`
					SpanID  string `json:"spanId"`
					Name    string `json:"name"`
					Kind    int    `json:"kind"`
					Start   string `json:"startTimeUnixNano"`
					End     string `json:"endTimeUnixNano"`
					Status  struct {
						Code int `json:"code"`
					} `json:"status"`
					Attributes []struct {
						Key string `json:"key"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the payload does not decode as OTLP: %v", err)
	}
	if len(got.ResourceSpans) != 1 || len(got.ResourceSpans[0].ScopeSpans) != 1 {
		t.Fatalf("payload has no single resource/scope: %s", raw)
	}
	spans := got.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 1 {
		t.Fatalf("payload carries %d spans, want 1", len(spans))
	}
	one := spans[0]
	if one.TraceID != span.TraceID || one.SpanID != span.SpanID {
		t.Errorf("ids did not survive: %s/%s", one.TraceID, one.SpanID)
	}
	if one.Kind != 2 {
		t.Errorf("a server span exported as kind %d, want 2", one.Kind)
	}
	if one.Status.Code != 2 {
		t.Errorf("a failed span exported status %d, want 2", one.Status.Code)
	}
	if one.Start == "0" || one.End == "0" {
		t.Errorf("timestamps are unset: %s..%s", one.Start, one.End)
	}
	// The artifact link is what ties a trace to the row the work produced.
	found := false
	for _, attr := range one.Attributes {
		if attr.Key == "flowy.artifact" {
			found = true
		}
	}
	if !found {
		t.Errorf("the span does not carry flowy.artifact: %s", raw)
	}
}

// The exporter never blocks a request: a queue that is full drops and counts,
// rather than making a slow collector into a slow node.
func TestExporterDropsRatherThanBlocks(t *testing.T) {
	e := &Exporter{in: make(chan *Span, 1)}
	e.Enqueue(&Span{SpanID: "a", TraceID: "b"})
	for i := 0; i < 10; i++ {
		e.Enqueue(&Span{SpanID: "c", TraceID: "d"})
	}
	if _, dropped, _, _ := e.Stats(); dropped != 10 {
		t.Fatalf("dropped %d spans, want 10", dropped)
	}
}

func TestTracesURL(t *testing.T) {
	for endpoint, want := range map[string]string{
		"http://localhost:4318":           "http://localhost:4318/v1/traces",
		"http://localhost:4318/":          "http://localhost:4318/v1/traces",
		"localhost:4318":                  "http://localhost:4318/v1/traces",
		"http://localhost:4318/v1/traces": "http://localhost:4318/v1/traces",
		"":                                "",
	} {
		if got := tracesURL(endpoint); got != want {
			t.Errorf("tracesURL(%q) = %q, want %q", endpoint, got, want)
		}
	}
}
