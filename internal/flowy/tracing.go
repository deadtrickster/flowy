package flowy

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/store"
)

// Tracing, the node half.
//
// One span per request, children for the things worth seeing inside it - the
// permission check, the queries, the ingest, the leg of replication - and a
// trace id that survives the node boundary. That last part is the whole reason
// this exists: a handoff is assigned on one node and worked on another, and
// without a shared trace id the two halves are two unrelated piles of spans
// that nobody can put together afterwards.
//
// It travels two ways, because there are two ways work crosses:
//
//   - over a request, in the W3C traceparent header. A sync driver talking to a
//     peer sends one, so the peer's pull is a child of our sync.
//   - over the rows, in the meta of the event that opens a handoff's thread.
//     Nothing requests anything of the far node when an assignment replicates -
//     what crosses is a delta - so the id rides the delta. It is inside the
//     event's signature, so a relay cannot rewrite it, and the far node reads
//     it back off the thread and continues the same trace.

// traceHeader is the W3C propagation header.
const traceHeader = "traceparent"

// otlpEndpointEnv names the collector spans are exported to. Unset means the
// spans are kept here and exported nowhere, which is the default: a node should
// not post its telemetry to anything nobody configured.
const otlpEndpointEnv = "FLOWY_OTLP_ENDPOINT"

// newTracer builds the node's tracer: it records into the store, and exports to
// a collector when one is configured.
func newTracer(node string, db *store.DB) *otel.Tracer {
	t := otel.New(node)
	t.SetRecorder(db)
	if endpoint := strings.TrimSpace(os.Getenv(otlpEndpointEnv)); endpoint != "" {
		exp := otel.NewExporter(endpoint, "flowy", node)
		t.SetExporter(exp)
		log.Printf("traces: exporting OTLP to %s", exp.Endpoint())
	} else {
		log.Printf("traces: recorded here; no OTLP collector configured (set %s)", otlpEndpointEnv)
	}
	return t
}

// observed is the outermost middleware under /api/: it opens the request's span
// and writes down what the request was refused, if it was.
//
// It sits outside authenticate rather than inside it for two reasons. A 401 is
// a refusal like any other and has to be counted, and the span has to exist
// before the token is resolved so that resolving it can be a child span - the
// permission check is one of the things a trace is for.
func (s *server) observed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.WithTracer(r.Context(), s.tracer)

		// A caller that is already in a trace continues it. Anything that is
		// not a traceparent is ignored rather than refused: a header nobody
		// sent correctly is a request that starts a trace of its own.
		route := routeName(r)
		name := r.Method + " " + route
		if traceID, parent, ok := otel.ParseTraceParent(r.Header.Get(traceHeader)); ok {
			ctx, _ = otel.StartTrace(ctx, otel.KindServer, name, traceID, parent)
		} else {
			ctx, _ = otel.Start(ctx, otel.KindServer, name)
		}
		span := otel.SpanFrom(ctx)
		span.SetAttr("http.method", r.Method)
		span.SetAttr("http.route", route)
		defer span.End()

		// The trace id goes back on the response, so a client - the console, a
		// peer, a person with curl - can ask for the trace it just made. It is
		// stamped when the response starts rather than now, because a handler
		// that joins the trace a handoff arrived in does so before it writes,
		// and a header naming the trace the request started in would name a
		// trace that ends up holding none of its spans.
		rec := &traceWriter{ResponseWriter: w, span: span, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		span.SetAttr("http.status", strconv.Itoa(rec.status))
		if rec.status >= 500 {
			span.Fail("the node answered " + strconv.Itoa(rec.status))
		} else {
			span.OK()
		}
		if rec.status == http.StatusUnauthorized || rec.status == http.StatusForbidden {
			s.recordDenial(r, span, rec.status, route)
		}
	})
}

// traceWriter stamps the response with the trace the request is in, at the
// moment the response starts, and remembers the status for the span.
//
// The stamp is late on purpose. A handler that joins the trace a handoff
// arrived in - see adoptThreadTrace - does so before it writes anything, and a
// header written before the handler ran would name the trace the request
// started in, which by then holds none of the request's spans.
type traceWriter struct {
	http.ResponseWriter
	span    *otel.Span
	status  int
	started bool
}

func (w *traceWriter) stamp() {
	if w.started {
		return
	}
	w.started = true
	if id := w.span.Trace(); id != "" {
		w.Header().Set("Trace-Id", id)
	}
}

func (w *traceWriter) WriteHeader(code int) {
	w.status = code
	w.stamp()
	w.ResponseWriter.WriteHeader(code)
}

func (w *traceWriter) Write(b []byte) (int, error) {
	w.stamp()
	return w.ResponseWriter.Write(b)
}

// recordDenial writes down a 401 or a 403.
//
// It is written after the response, so a refusal costs the caller nothing, and
// it records no id from the request: what was refused is exactly what the
// permission filter is careful never to confirm exists. A failure to write it is
// logged and dropped - a node that could not count a refusal must still refuse.
func (s *server) recordDenial(r *http.Request, span *otel.Span, status int, route string) {
	// Who was refused is read off the span rather than out of the request's
	// context. authenticate resolves the principal into a context of its own,
	// which is the handler's and not this middleware's - so principalOf here is
	// always empty, and every refusal would have been counted as nobody's. The
	// span is the one object both halves hold.
	user, agent, _ := span.Principal()
	reason := "unauthenticated"
	if status == http.StatusForbidden {
		reason = "refused"
	}
	span.SetAttr("denied", reason)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.db.RecordDenial(ctx, user, agent, r.Method, route, reason, status); err != nil {
		log.Printf("metrics: a %d on %s was not counted: %v", status, route, err)
	}
}

// routeName is the path with the ids taken out, so that a thousand requests for
// a thousand artifacts are one route in a trace rather than a thousand.
//
// The rule is the shape of an id here: a ULID is 26 characters of Crockford
// base32, and nothing else in a path is. It is done by hand because this build
// targets Go 1.22, where the pattern a request matched is not on the request.
func routeName(r *http.Request) string {
	parts := strings.Split(r.URL.Path, "/")
	for i, part := range parts {
		if looksLikeID(part) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

// looksLikeID reports whether a path segment is a minted id rather than a name.
func looksLikeID(s string) bool {
	const ulidLen = 26
	if len(s) != ulidLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// adoptThreadTrace moves this request into the trace a conversation already
// belongs to.
//
// This is the far end of a cross-node handoff. The request that works a
// delivered task carries no traceparent - nobody made it on behalf of the node
// that assigned the work - so the trace id is read off the thread, where it
// rode over as part of the event that opened it. The request's span is moved
// into that trace rather than a second one being started beside it.
//
// A thread with no trace, or one the caller may not read, leaves the request in
// the trace it already had. It is telemetry: it does not widen anything and it
// must not fail a request.
func (s *server) adoptThreadTrace(r *http.Request, thread string) {
	adoptThreadTraceOf(r.Context(), s.db, principalOf(r), thread)
}

// adoptThreadTraceOf is that move for a caller with a context and no request.
// The say path is one - it runs behind two doors now, see sayInRoom - and a
// message said over MCP into a handoff's thread belongs in that handoff's trace
// exactly as one said over HTTP does.
//
// The tracer is taken from the context rather than from a server, which is what
// makes the guard the same on both sides: no tracer means nothing to adopt into,
// and the store is not asked.
func adoptThreadTraceOf(ctx context.Context, db *store.DB, p *store.Principal, thread string) {
	if thread == "" || p == nil || otel.TracerFrom(ctx) == nil {
		return
	}
	id, err := db.TraceOfThread(ctx, p, thread)
	if err != nil {
		log.Printf("traces: could not read the trace of thread %s: %v", thread, err)
		return
	}
	if id == "" {
		return
	}
	if otel.Adopt(ctx, id) {
		if span := otel.SpanFrom(ctx); span != nil {
			span.SetAttr("trace.adopted_from", "thread "+thread)
		}
	}
}

// traceIDOf is the trace the request is in, for the handlers that stamp it onto
// a row they are about to write.
func traceIDOf(r *http.Request) string { return otel.TraceID(r.Context()) }

// recordDeliveries writes one span per row of a delta that arrived carrying a
// trace id.
//
// This is the "delivered to B" half of a cross-node handoff, and it is the
// point of the whole exercise: the assignment's opening event carries the trace
// it was made in, and when it lands here this node records - under that same
// trace id - that it took delivery. One trace then spans two nodes' databases,
// and the collector can put them together.
//
// The span ids are derived from the event ids, so a delta applied twice records
// one delivery: see store.DeliverSpanID.
func recordDeliveries(ctx context.Context, db *store.DB, node string, in *store.SyncSet, peer string) {
	if db == nil || in == nil {
		return
	}
	for _, d := range store.DeliveredIn(in) {
		recordDelivery(ctx, db, node, d, peer)
	}
}

// recordDelivery writes the one span. It is built by hand rather than through
// otel.Start because its id has to be derived from the row's, and because it
// stands for an arrival rather than for a stretch of this node's own time.
func recordDelivery(ctx context.Context, db *store.DB, node string, d store.Delivered, peer string) {
	span := &otel.Span{
		SpanID:   store.DeliverSpanID(d.EventID),
		TraceID:  d.TraceID,
		Name:     "handoff.deliver",
		Kind:     otel.KindSync,
		Node:     node,
		Actor:    d.Actor,
		Project:  d.Project,
		Artifact: d.Artifact,
		Status:   otel.StatusOK,
		Started:  time.Now().UTC(),
		Ended:    time.Now().UTC(),
	}
	span.SetAttr("event", d.EventID)
	span.SetAttr("thread", d.Thread)
	span.SetAttr("from_node", d.Node)
	if peer != "" {
		span.SetAttr("peer", peer)
	}
	// The user the work is for is the one the thread's task names, and that is
	// a row this node may not have yet. The span is the node's own account of
	// an arrival, so it is left to the project and the artifact, which are on
	// the event and are what the scope filter reads.
	if err := db.RecordSpan(ctx, span); err != nil {
		log.Printf("traces: delivery of %s was not recorded: %v", d.EventID, err)
	}
}

// Unwrap hands back the ResponseWriter underneath, which is how
// http.ResponseController reaches the real connection through this wrapper.
//
// WITHOUT IT EVERY STREAMING RESPONSE ON THIS NODE IS BROKEN, and silently: a
// controller that cannot find the writer answers http.ErrNotSupported, so
// Flush() does nothing and SetWriteDeadline() fails. /api/stream refuses to
// start rather than serving a stream it knows will be cut - see stream.go - and
// before this method it refused every time, on a node whose handler was
// correct. A wrapper is invisible to the handler it wraps, which is exactly why
// this is the sort of thing that gets found in a browser rather than in a test.
//
// It is the contract Go added in 1.20 for precisely this: a middleware that
// wraps the writer must say what it wrapped.
func (w *traceWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
