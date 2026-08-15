package otel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The OTLP/HTTP exporter.
//
// One goroutine, one queue and a POST per batch. It is deliberately small: the
// endpoint is configuration, the body is the JSON encoding of the protocol, and
// a collector that is down must never be able to slow a request down or fail
// one. So the queue is bounded and a full queue drops - and says how many it
// dropped, because an exporter that silently loses spans is worse than one that
// is off, which is the whole of the serenedash rule about empty not being
// all-clear.

// exportQueue is how many ended spans may be waiting to go out. A hundred is
// several seconds of a busy node; past that the collector is not keeping up and
// the node's own recorder is the copy that matters.
const exportQueue = 512

// exportBatch is the most spans one POST carries.
const exportBatch = 128

// exportEvery is how long a partial batch waits for company.
const exportEvery = 200 * time.Millisecond

// Exporter posts spans to an OTLP/HTTP collector.
type Exporter struct {
	endpoint string
	service  string
	node     string
	client   *http.Client

	in   chan *Span
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	sent    int64
	dropped int64
	failed  int64
	lastErr string
}

// NewExporter starts an exporter posting to endpoint, which is the collector's
// base URL: the spans go to <endpoint>/v1/traces, which is where OTLP/HTTP
// says they go. An endpoint that already names that path is taken as it is, so
// a collector configured either way works.
func NewExporter(endpoint, service, node string) *Exporter {
	e := &Exporter{
		endpoint: tracesURL(endpoint),
		service:  service,
		node:     node,
		client:   &http.Client{Timeout: 10 * time.Second},
		in:       make(chan *Span, exportQueue),
		done:     make(chan struct{}),
	}
	go e.run()
	return e
}

// tracesURL is the endpoint a batch is POSTed to.
func tracesURL(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return ""
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	if strings.HasSuffix(endpoint, "/v1/traces") {
		return endpoint
	}
	return endpoint + "/v1/traces"
}

// Endpoint is where this exporter posts.
func (e *Exporter) Endpoint() string {
	if e == nil {
		return ""
	}
	return e.endpoint
}

// Enqueue hands a span to the exporter, or drops it when the queue is full.
// It never blocks: a request is not held up by a collector.
func (e *Exporter) Enqueue(s *Span) {
	if e == nil || s == nil {
		return
	}
	select {
	case e.in <- s:
	default:
		e.mu.Lock()
		e.dropped++
		e.mu.Unlock()
	}
}

// Stats is what the metrics endpoint reports about the exporter.
func (e *Exporter) Stats() (sent, dropped, failed int64, lastErr string) {
	if e == nil {
		return 0, 0, 0, ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sent, e.dropped, e.failed, e.lastErr
}

// Close stops the exporter after flushing what is queued.
func (e *Exporter) Close() {
	if e == nil {
		return
	}
	e.once.Do(func() {
		close(e.in)
		<-e.done
	})
}

func (e *Exporter) run() {
	defer close(e.done)
	ticker := time.NewTicker(exportEvery)
	defer ticker.Stop()

	batch := make([]*Span, 0, exportBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		e.post(batch)
		batch = batch[:0]
	}
	for {
		select {
		case s, ok := <-e.in:
			if !ok {
				flush()
				return
			}
			batch = append(batch, s)
			if len(batch) >= exportBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// post sends one batch and records what happened to it. A collector that is
// down is logged once per batch and the spans are gone: they are already in the
// node's own store, and retrying forever is how an exporter becomes the outage.
func (e *Exporter) post(batch []*Span) {
	body, err := json.Marshal(Payload(e.service, e.node, batch))
	if err != nil {
		e.record(0, 0, int64(len(batch)), err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		e.record(0, 0, int64(len(batch)), err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		e.record(0, 0, int64(len(batch)), err.Error())
		log.Printf("otlp: %d span(s) not exported: %v", len(batch), err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		why := fmt.Sprintf("collector answered %d", resp.StatusCode)
		e.record(0, 0, int64(len(batch)), why)
		log.Printf("otlp: %d span(s) not exported: %s", len(batch), why)
		return
	}
	e.record(int64(len(batch)), 0, 0, "")
}

func (e *Exporter) record(sent, dropped, failed int64, why string) {
	e.mu.Lock()
	e.sent += sent
	e.dropped += dropped
	e.failed += failed
	if why != "" {
		e.lastErr = why
	}
	e.mu.Unlock()
}

// ------------------------------------------------------------ the encoding

// Payload is a batch of spans as an OTLP/HTTP JSON ExportTraceServiceRequest.
//
// It is exported so a test can assert the shape without a collector: the field
// names are the protocol's, not this package's, and getting one of them wrong
// is a payload every collector rejects for a reason none of them explain well.
func Payload(service, node string, spans []*Span) map[string]any {
	out := make([]map[string]any, 0, len(spans))
	for _, s := range spans {
		out = append(out, otlpSpan(s))
	}
	return map[string]any{
		"resourceSpans": []any{map[string]any{
			"resource": map[string]any{
				"attributes": []any{
					attr("service.name", service),
					attr("service.node", node),
				},
			},
			"scopeSpans": []any{map[string]any{
				"scope": map[string]any{"name": "flowy"},
				"spans": out,
			}},
		}},
	}
}

// otlpKind maps this node's span kinds onto the protocol's small enum. A server
// span is 2, a client span 3, everything else internal (1) - the fabric's own
// vocabulary lives in the span's name and in the flowy.kind attribute, where a
// collector can read it without this having to lie about the enum.
func otlpKind(kind string) int {
	switch kind {
	case KindServer, KindMCP:
		return 2
	case KindSync:
		return 3
	default:
		return 1
	}
}

// otlpStatus maps a status onto the protocol's: 0 unset, 1 ok, 2 error.
func otlpStatus(status string) int {
	switch status {
	case StatusOK:
		return 1
	case StatusError:
		return 2
	default:
		return 0
	}
}

func otlpSpan(s *Span) map[string]any {
	attrs := []any{attr("flowy.kind", s.Kind), attr("flowy.node", s.Node)}
	if s.User != "" {
		attrs = append(attrs, attr("flowy.user", s.User))
	}
	if s.Actor != "" {
		attrs = append(attrs, attr("flowy.actor", s.Actor))
	}
	if s.Project != "" {
		attrs = append(attrs, attr("flowy.project", s.Project))
	}
	if s.Artifact != "" {
		// The link the architecture asks for: a span that produced a transcript
		// names it, so a trace and an artifact reach each other.
		attrs = append(attrs, attr("flowy.artifact", s.Artifact))
	}
	for k, v := range s.Attrs() {
		attrs = append(attrs, attr(k, v))
	}
	span := map[string]any{
		"traceId":           s.TraceID,
		"spanId":            s.SpanID,
		"name":              s.Name,
		"kind":              otlpKind(s.Kind),
		"startTimeUnixNano": nanos(s.Started),
		"endTimeUnixNano":   nanos(s.Ended),
		"attributes":        attrs,
		"status":            map[string]any{"code": otlpStatus(s.Status)},
	}
	if s.ParentID != "" {
		span["parentSpanId"] = s.ParentID
	}
	return span
}

// nanos is a timestamp as OTLP wants it: nanoseconds since the epoch, as a
// string, because the value does not fit a JSON number every reader parses the
// same way.
func nanos(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

func attr(k, v string) map[string]any {
	return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
}
