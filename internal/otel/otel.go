// Package otel is the node's tracing: OpenTelemetry spans over the operations
// the fabric performs, and an OTLP exporter that hands them to a collector.
//
// It is written here rather than taken from the OpenTelemetry Go SDK for one
// reason: this binary vendors its dependencies and the SDK is a large tree of
// them, for a use that is one span type, one context key and one exporter. What
// is implemented is the wire format and the propagation format, which are the
// parts anything else has to agree with:
//
//   - a trace id is 16 random bytes, a span id is 8, both rendered as lower-case
//     hex, which is what the OTLP JSON encoding expects;
//   - the propagation header is W3C traceparent, 00-<trace>-<span>-<flags>;
//   - the export is OTLP/HTTP with a JSON body, POSTed to <endpoint>/v1/traces.
//
// A span goes to two places and they answer different questions. The recorder
// is this node's own store, which is what the console's waterfall and the
// cross-node collector read. The exporter is somebody else's collector, which
// is where a deployment that already has tracing wants it. Either can be absent:
// a node with neither still runs, and the spans are simply not kept.
package otel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// maxPending caps how many finished children one span holds while it waits to
// end. Past it a child is written out as it finishes: an unbounded buffer on a
// long-running root span is a memory leak wearing a trace's clothes.
const maxPending = 256

// Kind names what a span stands for. They are the operations the architecture
// asks to be followable: an MCP call, a permission check, a query, an ingest, a
// leg of replication - plus the HTTP request that most of them hang under.
const (
	KindServer     = "server"
	KindMCP        = "mcp"
	KindPermission = "permission"
	KindQuery      = "query"
	KindIngest     = "ingest"
	KindSync       = "sync"
	KindInternal   = "internal"
)

// Status is what a span says about how it ended. Unset is the default and is
// what a span that was not told otherwise reports: "ok" is a claim, and a span
// nobody looked at has not earned it.
const (
	StatusUnset = ""
	StatusOK    = "ok"
	StatusError = "error"
)

// Span is one operation. The identity fields are what the collector joins on;
// the principal fields are what the scope filter reads, so a span is filtered
// by the same three facts every other read here is filtered by.
type Span struct {
	TraceID  string
	SpanID   string
	ParentID string
	Name     string
	Kind     string
	Node     string
	Actor    string
	User     string
	Project  string
	Artifact string
	Status   string
	Started  time.Time
	Ended    time.Time

	mu      sync.Mutex
	attrs   map[string]string
	tracer  *Tracer
	parent  *Span
	pending []*Span
	ended   bool
	noop    bool
	dropped bool
}

// Recorder is where ended spans are kept. The store implements it; a node with
// no store attached simply does not keep them.
type Recorder interface {
	RecordSpan(ctx context.Context, s *Span) error
}

// Tracer mints spans and hands the ended ones to the recorder and the exporter.
type Tracer struct {
	node string

	mu       sync.RWMutex
	recorder Recorder
	exporter *Exporter

	// dropped counts spans that could not be kept - a recorder that errored, an
	// export queue that was full. It is reported by the metrics endpoint,
	// because a trace view that is quietly missing spans reads as an operation
	// that did not happen.
	dropped int64
	kept    int64
}

// New makes a tracer that stamps node onto every span it mints.
func New(node string) *Tracer { return &Tracer{node: node} }

// SetRecorder attaches the store the spans are kept in.
func (t *Tracer) SetRecorder(r Recorder) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.recorder = r
	t.mu.Unlock()
}

// SetExporter attaches an OTLP exporter, or clears it with nil.
func (t *Tracer) SetExporter(e *Exporter) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.exporter = e
	t.mu.Unlock()
}

// Node is the node name this tracer stamps.
func (t *Tracer) Node() string {
	if t == nil {
		return ""
	}
	return t.node
}

// Exporter is the OTLP exporter this tracer holds, or nil when none is
// configured. The metrics endpoint asks it what it has managed to send.
func (t *Tracer) Exporter() *Exporter {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.exporter
}

// Stats reports how many spans this tracer kept and how many it could not.
// Dropped is not a rounding error: it is the number of times the account of
// what this node did is incomplete.
func (t *Tracer) Stats() (kept, dropped int64) {
	if t == nil {
		return 0, 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.kept, t.dropped
}

// Close flushes the exporter, if there is one.
func (t *Tracer) Close() {
	if t == nil {
		return
	}
	t.mu.RLock()
	exp := t.exporter
	t.mu.RUnlock()
	exp.Close()
}

// ---------------------------------------------------------------- context

type tracerKey struct{}
type spanKey struct{}

// WithTracer puts a tracer in the context. Everything below it - a handler, a
// store call, a merge - reaches it from there rather than from a global, so two
// tracers in one process (a serve and a sync in the same test) do not collide.
func WithTracer(ctx context.Context, t *Tracer) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, tracerKey{}, t)
}

// TracerFrom returns the tracer in ctx, or nil.
func TracerFrom(ctx context.Context) *Tracer {
	t, _ := ctx.Value(tracerKey{}).(*Tracer)
	return t
}

// SpanFrom returns the innermost span in ctx, or nil.
func SpanFrom(ctx context.Context) *Span {
	s, _ := ctx.Value(spanKey{}).(*Span)
	return s
}

// TraceID is the trace the context is in, or "".
func TraceID(ctx context.Context) string {
	if s := SpanFrom(ctx); s != nil {
		return s.TraceID
	}
	return ""
}

// ------------------------------------------------------------------ spans

// Start opens a span under whatever is already in ctx, and returns the context
// its children hang under.
//
// With no tracer in ctx it returns a span that records nothing and costs
// nothing but the allocation - so a store method can be instrumented once and
// still be called from a test, a CLI or a drainer that never set one up.
func Start(ctx context.Context, kind, name string) (context.Context, *Span) {
	t := TracerFrom(ctx)
	if t == nil {
		return ctx, &Span{noop: true, Name: name, Kind: kind}
	}
	parent := SpanFrom(ctx)

	s := &Span{
		SpanID:  newSpanID(),
		Name:    name,
		Kind:    kind,
		Node:    t.node,
		Started: time.Now().UTC(),
		tracer:  t,
	}
	if parent != nil {
		s.parent = parent
		s.TraceID, s.ParentID = parent.TraceID, parent.SpanID
		s.User, s.Actor, s.Project = parent.User, parent.Actor, parent.Project
		s.Artifact = parent.Artifact
	} else {
		s.TraceID = newTraceID()
	}
	return context.WithValue(ctx, spanKey{}, s), s
}

// StartTrace opens a root span in an existing trace: the id is one that arrived
// from somewhere else - a traceparent header, or the meta of a replicated event
// - so the work here joins the trace it continues rather than starting a
// second one that nothing can be joined to.
//
// An id that is not a trace id is refused rather than used: a trace id off the
// wire is somebody else's string, and a malformed one would make a trace nobody
// can query. The span still starts, under an id of its own.
func StartTrace(ctx context.Context, kind, name, traceID, parentSpan string) (context.Context, *Span) {
	ctx = context.WithValue(ctx, spanKey{}, (*Span)(nil))
	ctx, s := Start(ctx, kind, name)
	if s.noop {
		return ctx, s
	}
	if ValidTraceID(traceID) {
		s.TraceID = strings.ToLower(traceID)
	}
	if ValidSpanID(parentSpan) {
		s.ParentID = strings.ToLower(parentSpan)
	}
	return ctx, s
}

// Adopt moves the span in ctx - and everything opened under it afterwards -
// into an existing trace.
//
// It is what makes a handoff one trace across two nodes. The request that works
// a delivered task has already started a trace of its own by the time the
// handler knows which thread it is about, and the thread is where the trace id
// arrived: so the span is moved rather than a second trace being left beside
// the first. Children opened after this inherit the adopted id, because they
// read it off this span when they start.
func Adopt(ctx context.Context, traceID string) bool {
	s := SpanFrom(ctx)
	if s == nil || s.noop || !ValidTraceID(traceID) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return false
	}
	s.TraceID = strings.ToLower(traceID)
	return true
}

// Root is the outermost span this one hangs under, which is the request or the
// operation it is part of. A span with no parent is its own root.
func (s *Span) Root() *Span {
	if s == nil {
		return nil
	}
	root := s
	for root.parent != nil {
		root = root.parent
	}
	return root
}

// SetPrincipal stamps who the work is for onto the span and its children.
func (s *Span) SetPrincipal(user, actor, project string) {
	if s == nil || s.noop {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.User, s.Actor, s.Project = user, actor, project
}

// Trace is the trace this span is in, read under the lock: a request that joins
// a handoff's trace part-way through changes it while the request is running.
func (s *Span) Trace() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TraceID
}

// Principal is who the span's work was for: the user, the actor that ran it,
// and the project it ran in.
func (s *Span) Principal() (user, actor, project string) {
	if s == nil {
		return "", "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.User, s.Actor, s.Project
}

// SetArtifact names the artifact this span is about, which is the link the
// architecture asks for: a span that produced a transcript names it, so the
// trace view and the artifact view reach each other.
func (s *Span) SetArtifact(id string) {
	if s == nil || s.noop {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Artifact = id
}

// SetAttr records one attribute. Values are strings: an attribute is read by a
// person looking at a waterfall, and a typed attribute system would be a
// schema this node does not need.
func (s *Span) SetAttr(k, v string) {
	if s == nil || s.noop || k == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = map[string]string{}
	}
	s.attrs[k] = v
}

// Attrs is a copy of the span's attributes.
func (s *Span) Attrs() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attrs) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.attrs))
	for k, v := range s.attrs {
		out[k] = v
	}
	return out
}

// Fail marks the span as having ended badly. The message is the node's own
// wording, never an error from the wire verbatim - a span body is rendered in a
// console.
func (s *Span) Fail(why string) {
	if s == nil || s.noop {
		return
	}
	s.mu.Lock()
	s.Status = StatusError
	s.mu.Unlock()
	if why != "" {
		s.SetAttr("error", why)
	}
}

// OK marks the span as having ended well.
func (s *Span) OK() {
	if s == nil || s.noop {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status == StatusUnset {
		s.Status = StatusOK
	}
}

// Drop abandons the span: it is not recorded and not exported. It is what a
// span opened before the decision to trace at all does when the answer is no.
func (s *Span) Drop() {
	if s == nil || s.noop {
		return
	}
	s.mu.Lock()
	s.dropped = true
	s.mu.Unlock()
}

// End closes the span and hands it on. Ending twice is a no-op: End is usually
// deferred, and a path that also ends it by hand should not record two spans.
func (s *Span) End() {
	if s == nil || s.noop {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.Ended = time.Now().UTC()
	dropped := s.dropped
	t := s.tracer
	s.mu.Unlock()

	if dropped || t == nil {
		return
	}

	// A child waits for its root. The trace a request belongs to is not always
	// known when the request starts - a handoff worked on the far node joins
	// the trace it reads off the thread, part-way through the handler - and a
	// child that had already been written out would be sitting on its own in a
	// trace of one. So the root holds them and stamps its own trace id on them
	// when it ends, which is the point at which the answer is final.
	if root := s.Root(); root != nil && root != s && root.hold(s) {
		return
	}
	t.finish(s)
	s.flush()
}

// hold takes a finished child, or refuses it when this span has ended already
// or is holding as many as it will. It reports whether it took it.
func (s *Span) hold(child *Span) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || len(s.pending) >= maxPending {
		return false
	}
	s.pending = append(s.pending, child)
	return true
}

// flush writes out the children this span was holding, under its own trace id.
func (s *Span) flush() {
	s.mu.Lock()
	held := s.pending
	s.pending = nil
	trace := s.TraceID
	t := s.tracer
	s.mu.Unlock()

	for _, child := range held {
		child.mu.Lock()
		child.TraceID = trace
		child.mu.Unlock()
		if t != nil {
			t.finish(child)
		}
		child.flush()
	}
}

// Duration is how long the span took. A span that has not ended reports the
// time so far, which is what a waterfall of an in-flight request wants.
func (s *Span) Duration() time.Duration {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Ended.IsZero() {
		return time.Since(s.Started)
	}
	return s.Ended.Sub(s.Started)
}

// finish records and exports one ended span.
func (t *Tracer) finish(s *Span) {
	t.mu.RLock()
	rec, exp := t.recorder, t.exporter
	t.mu.RUnlock()

	kept := false
	if rec != nil {
		// A span is an account of something that already happened, so the
		// context it is written under is not the request's: a request that was
		// cancelled is exactly the one whose span is worth having.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := rec.RecordSpan(ctx, s)
		cancel()
		if err == nil {
			kept = true
		}
	}
	if exp != nil {
		exp.Enqueue(s)
	}

	t.mu.Lock()
	if rec != nil {
		if kept {
			t.kept++
		} else {
			t.dropped++
		}
	}
	t.mu.Unlock()
}

// ------------------------------------------------------------------- ids

func newTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A trace id that is not random is still a trace id, and refusing to
		// trace because the entropy source hiccupped would take the whole
		// request down with it. It is stamped with the time so that two of
		// them are not the same trace.
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func newSpanID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ValidTraceID reports whether s is 32 hex digits and not all zero, which is
// what the W3C format calls an invalid trace id.
func ValidTraceID(s string) bool { return validID(s, 32) }

// ValidSpanID reports whether s is 16 hex digits and not all zero.
func ValidSpanID(s string) bool { return validID(s, 16) }

func validID(s string, want int) bool {
	if len(s) != want {
		return false
	}
	zero := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			if c != '0' {
				zero = false
			}
		case c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
			zero = false
		default:
			return false
		}
	}
	return !zero
}

// TraceParent renders the W3C header for a span, so an outbound request joins
// the trace it was made from.
func TraceParent(s *Span) string {
	if s == nil || s.noop || !ValidTraceID(s.TraceID) || !ValidSpanID(s.SpanID) {
		return ""
	}
	return "00-" + s.TraceID + "-" + s.SpanID + "-01"
}

// ParseTraceParent reads a W3C traceparent header. A header that is not one is
// not an error to report to anybody: it means this request starts a trace.
func ParseTraceParent(header string) (traceID, spanID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) != 4 || parts[0] != "00" {
		return "", "", false
	}
	if !ValidTraceID(parts[1]) || !ValidSpanID(parts[2]) {
		return "", "", false
	}
	return strings.ToLower(parts[1]), strings.ToLower(parts[2]), true
}
