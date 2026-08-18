package main

// One event stream per tab, instead of one poll per panel.
//
// THE MEASUREMENT THAT PRODUCED THIS. An idle console tab on /chat/general made
// about thirty requests a minute across six independent clocks - 1.2s and 2.5s
// in ReproPanel, 15s for presence, 15s for the worklog, 20s for the unread
// badges, 30s for the bundle check - plus a long poll, and each of those is a
// separate idea of how often the world changes. The queue page made NONE, which
// is the defect this door was opened for: every claim and reassignment an agent
// made was invisible until somebody pressed F5.
//
// N pollers is N clocks, N wakeups and N chances to draw a stale panel beside a
// fresh one. The cost grows with the number of panels somebody has open rather
// than with the number of things that actually changed, which is the wrong way
// round.
//
// ENVELOPES, NOT DELTAS, and this is the whole design in one choice. A message
// on this stream says THAT something changed and never WHAT it now is:
//
//	{"topic":"todos","hlc":117...,"artifact":"01M...","type":"todo.assign"}
//
// The subscriber re-reads the list it already knows how to read. Everything
// that makes a streaming protocol hard falls out of that: a duplicate delivery
// is a wasted read, an out-of-order delivery is invisible, the gap after a
// disconnect is an ordinary cursor read, and NOTHING PARTIAL IS EVER APPLIED to
// a row somebody may be editing. The alternative - shipping row deltas - buys
// bandwidth and pays for it in every one of those corner cases, on a console
// whose whole queue read is 300KB once in a while.
//
// It is server-sent events rather than a websocket because the traffic is
// one-directional. Every write in this console already goes over a plain POST,
// SSE rides the HTTP and the auth this node already has, and the browser
// reconnects and resumes by itself. A websocket would add a second protocol to
// carry nothing back.
//
// And it is net/http rather than an SSE library because the framing is three
// lines and the hard part is not framing. The hard part is the permission
// filter and the cursor, and those are store code that no SSE library has an
// opinion about.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// streamHeartbeat is how often the door writes to a stream that has nothing to
// say, AND IT IS THE POINT OF THE DOOR AS MUCH AS THE EVENTS ARE.
//
// A silent connection and a dead one are byte-identical to a client until a
// write fails, and a write only fails when somebody tries. So a panel whose
// "as of" reads the last EVENT freezes the moment the queue goes quiet, which
// is indistinguishable from the node having gone away - the exact defect this
// row was filed about, reintroduced one layer down. The heartbeat is what makes
// "quiet" and "dead" different pictures, and the console reads its clock off
// this and never off the last change.
const streamHeartbeat = 5 * time.Second

// streamTick is how often the door asks the store whether anything landed. It
// is waitTick's value and for waitTick's reason: this is the same shape as the
// room's long poll, and two ideas of how often the node notices a write is how
// two surfaces come to disagree about when something happened.
//
// It is honest about what this is. Until the store can push - Postgres
// LISTEN/NOTIFY, which lib/pq supports and nothing here uses yet - a stream is
// the node polling on the browser's behalf. That is still the win: the requests
// collapse from N panels per tab to one connection per tab, and the database
// reads stay where they were.
const streamTick = waitTick

// streamBatch is how many events one read may carry. A tab that has been closed
// over a weekend reconnects with an old cursor and must not ask for the whole
// log in one answer; it gets a page, advances, and asks again on the next tick.
const streamBatch = 200

// The topics this door serves, and the event types each one is made of.
//
// DENY BY DEFAULT, like paramguard next door and for the same reason: a topic
// this map does not know is refused by name rather than quietly subscribed to
// nothing. A client that subscribes to "todo" instead of "todos" and is
// answered 200 gets a connection that is alive, heartbeating, and will never
// tell it anything - which reads as "the queue is quiet" forever. That is the
// failure this whole row is about, so it cannot be the failure this door has.
var streamTopics = map[string][]string{
	// Everything that moves a row in the queue. It is the union of the minted
	// types the node writes for queue work - see mintedTypes in api.go, which
	// is the list of things a client may not forge - plus `status`, the generic
	// lifecycle move, because a todo taken to done through the artifact door is
	// the same fact to somebody reading the board.
	"todos": {
		store.EventTodoAssign,
		store.EventTodoStatus,
		store.EventTodoCategory,
		store.EventTodoEdit,
		store.EventTodoNote,
		store.EventTodoSteal,
		store.EventDepAdd,
		store.EventDepRemove,
		store.EventWorkClaim,
		store.EventWorkDone,
		statusEventType,
	},
	// The merge queue: declared, landed, given back. The queue's admissibility
	// is recomputed against a moving master, so a client re-reads the queue on
	// any of these rather than trying to reason about what one of them did.
	"queue": {
		store.EventMergeGate,
		store.EventMergeLand,
		store.EventMergeAbandon,
	},
}

// streamEnvelope is one message. It carries no row content on purpose - see the
// file comment. `type` is the event type rather than a shape of its own,
// because a subscriber that wants to be cleverer later needs the real name and
// one that does not can ignore it.
type streamEnvelope struct {
	Topic    string `json:"topic"`
	HLC      int64  `json:"hlc"`
	Artifact string `json:"artifact,omitempty"`
	Type     string `json:"type"`
	Project  string `json:"project,omitempty"`
}

// streamBeat is the message that says the connection is alive and nothing has
// happened. `hlc` rides along so a client that missed a `change` can still see
// the cursor move.
type streamBeat struct {
	At  string `json:"at"`
	HLC int64  `json:"hlc"`
}

// handleStream is the door.
//
// GET /api/stream?topics=todos,queue&since=<hlc>
//
// It answers text/event-stream and does not return until the client hangs up or
// the process shuts down. Everything it writes is derived from a permission-
// filtered store read, so a subscriber learns exactly what a GET would have
// told it and never that a row it cannot read exists.
func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	topics, types, err := streamSubscription(q.Get("topics"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	// THE WRITE DEADLINE, and this is not a detail.
	//
	// serve.go sets WriteTimeout on the API server, which Go stamps onto the
	// connection when the request starts. A response still being written when
	// it expires is CUT - so without this line every stream on this node dies
	// at sixty seconds, and a stream that dies looks exactly like a room where
	// nothing is happening. No SSE library removes the need for it: it is a
	// property of this server, not of the framing.
	//
	// Taken before anything is written, and a controller that cannot do it is a
	// refusal rather than a stream that will be cut later without saying so.
	rc := http.NewResponseController(w)
	if err := clearWriteDeadline(rc); err != nil {
		serverError(w, r, fmt.Errorf("stream: clearing the write deadline: %w", err))
		return
	}

	cursor, err := streamCursor(r, q.Get("since"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	// No cursor at all means "start from now". A tab opening for the first time
	// wants what happens NEXT: replaying the log from zero would hand a fresh
	// console thousands of envelopes, each of which it would answer with a
	// re-read of the same list.
	//
	// A cursor the caller DID state is honoured however old it is, because that
	// is the resume path and its whole job is to close a gap.
	if cursor < 0 {
		cursor, err = s.streamHead(r, p, types)
		if err != nil {
			serverError(w, r, err)
			return
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// For any proxy that buffers by default. There is none in front of this node
	// today; a buffered SSE stream is silent until the buffer fills, which is
	// the failure this door must never have.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// The opening message states the cursor this connection started from, so a
	// client can tell "connected and up to date" from "connected and about to
	// be told about a backlog" - and so a check can assert a connection was
	// actually established rather than that bytes arrived.
	if !writeSSE(w, rc, "hello", 0, streamBeat{At: nowRFC3339(), HLC: cursor}) {
		return
	}

	ctx := r.Context()
	spoke := time.Now()
	tick := time.NewTicker(streamTick)
	defer tick.Stop()

	for {
		list, err := s.db.ListEvents(ctx, p, store.EventQuery{
			Types:    types,
			Since:    cursor,
			ScopeAll: scopeAll(r, p),
			Limit:    streamBatch,
		})
		if err != nil {
			// The client is holding an open stream, so there is nowhere to put
			// a 500 - the status went out with the headers. Saying so and
			// hanging up is the honest answer: the client reconnects, and the
			// reconnect carries its cursor, so nothing is lost by ending here.
			logStreamEnd(r, err)
			return
		}
		for _, e := range list {
			env := streamEnvelope{
				Topic:    topics[e.Type],
				HLC:      e.SeqHLC,
				Artifact: e.Artifact,
				Type:     e.Type,
				Project:  derefOr(e.Project),
			}
			if !writeSSE(w, rc, "change", e.SeqHLC, env) {
				return
			}
			cursor = e.SeqHLC
			spoke = time.Now()
		}
		// A full page means there is more waiting, so go straight round again
		// rather than sleeping a tick between every page of a backlog.
		if len(list) == streamBatch {
			continue
		}
		if time.Since(spoke) >= streamHeartbeat {
			if !writeSSE(w, rc, "heartbeat", 0, streamBeat{At: nowRFC3339(), HLC: cursor}) {
				return
			}
			spoke = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// streamSubscription turns `?topics=a,b` into the event types to read and the
// type-to-topic map to label them with.
//
// An unknown topic is REFUSED and named, with the served ones listed. See
// streamTopics for why a silent subscription is the one answer this door must
// never give.
func streamSubscription(param string) (map[string]string, []string, error) {
	asked := strings.Split(strings.TrimSpace(param), ",")
	topics := map[string]string{}
	var types []string
	named := 0
	for _, raw := range asked {
		topic := strings.TrimSpace(raw)
		if topic == "" {
			continue
		}
		named++
		kinds, ok := streamTopics[topic]
		if !ok {
			return nil, nil, fmt.Errorf("no topic %q on this stream - it serves %s",
				topic, strings.Join(streamTopicNames(), ", "))
		}
		for _, kind := range kinds {
			// A type claimed by two topics would be labelled by whichever was
			// listed last, which is a silent wrong answer. None do today; this
			// keeps that true rather than assuming it.
			if was, taken := topics[kind]; taken {
				return nil, nil, fmt.Errorf("%q and %q both carry %q", was, topic, kind)
			}
			topics[kind] = topic
			types = append(types, kind)
		}
	}
	if named == 0 {
		return nil, nil, fmt.Errorf("say which topics to stream: %s",
			strings.Join(streamTopicNames(), ", "))
	}
	return topics, types, nil
}

// streamTopicNames is the served list, in a stable order so a refusal reads the
// same twice.
func streamTopicNames() []string {
	names := make([]string, 0, len(streamTopics))
	for name := range streamTopics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// streamCursor reads where the client got to, from Last-Event-ID first and the
// query second, and answers -1 for "said nothing".
//
// LAST-EVENT-ID WINS, because it is the one the BROWSER sends without being
// asked. A reconnecting EventSource replays the header by itself; the query
// parameter is for a client opening deliberately at a known point. When both
// are present the header is the more recent statement of where this connection
// actually got to.
func streamCursor(r *http.Request, since string) (int64, error) {
	if id := strings.TrimSpace(r.Header.Get("Last-Event-ID")); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("Last-Event-ID must be a packed hlc integer, not %q", id)
		}
		return n, nil
	}
	if strings.TrimSpace(since) == "" {
		return -1, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(since), 10, 64)
	if err != nil || n < 0 {
		return 0, errNotACursor
	}
	return n, nil
}

// streamHead is the newest reading this principal can see in the topics it
// asked for: where a connection that stated no cursor begins.
//
// Narrowed to the same types the stream carries, deliberately. The head of the
// WHOLE log would put a busy room's chat cursor on a queue subscription, and
// the first queue change after that would arrive with a reading below the
// cursor and be skipped - a stream that connects, heartbeats, and silently
// drops the first thing it was opened for.
func (s *server) streamHead(r *http.Request, p *store.Principal, types []string) (int64, error) {
	list, err := s.db.RecentEvents(r.Context(), p, store.EventQuery{
		Types:    types,
		ScopeAll: scopeAll(r, p),
		Limit:    1,
	})
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, nil
	}
	return list[0].SeqHLC, nil
}

// writeSSE writes one framed message and flushes it, and says whether the
// client is still there.
//
// The flush is the difference between a stream and a buffer. Without it Go
// holds the bytes until its writer fills, so a low-traffic stream - which is
// every one of these - arrives in a lump minutes late or not at all, and the
// heartbeat that exists to prove liveness would be the first casualty.
//
// `id:` is the hlc, which is what the browser hands back as Last-Event-ID on a
// reconnect. It is written ONLY for changes: an id on a heartbeat would move a
// reconnecting client's cursor past events it never saw.
func writeSSE(w http.ResponseWriter, rc *http.ResponseController, event string, id int64, body any) bool {
	data, err := json.Marshal(body)
	if err != nil {
		return false
	}
	var b strings.Builder
	if id > 0 {
		fmt.Fprintf(&b, "id: %d\n", id)
	}
	fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", event, data)
	if _, err := w.Write([]byte(b.String())); err != nil {
		return false
	}
	return rc.Flush() == nil
}

// nowRFC3339 is the wall clock on a heartbeat. It is what a person reads on the
// panel, not a cursor and not an ordering: the hlc beside it is the ordering.
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// logStreamEnd records a stream that ended on an error rather than on the
// client leaving. It is separate from serverError because the status is long
// gone and writing one would corrupt the framing of a stream somebody is still
// reading.
func logStreamEnd(r *http.Request, err error) {
	log.Printf("stream %s ended: %v", r.URL.RequestURI(), err)
}

// derefOr is the project on an event, which is a pointer because a projectless
// event is a real thing here - a private message belongs to its author and to
// nobody's project. An envelope says the empty string for that rather than
// carrying a null a subscriber has to test for.
func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// clearWriteDeadline takes the server's WriteTimeout off THIS response.
//
// It is a named function with one line in it so the check that proves it works
// can call the same code the door calls. A test that reproduced the line would
// prove that the technique works, not that this door uses it - and "the guard
// is not there" is the first hypothesis whenever something looks unguarded.
//
// See stream_test.go, which runs a server with a short WriteTimeout and asserts
// the difference between a stream that called this and one that did not.
func clearWriteDeadline(rc *http.ResponseController) error {
	return rc.SetWriteDeadline(time.Time{})
}
