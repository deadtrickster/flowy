package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// The activity timeline: everything that happened, in one order, searchable,
// and postable-into.
//
// A turn, a run log line, a chat message and a steer are four things people
// look for in four different places, and looking in four places is how you miss
// the one that mattered. They are all events in the same log here, so the
// timeline is one read of that log with the same permission filter every other
// read has - and the message box is on it, because the moment you find the run
// where something went wrong is the moment you want to say something into it.
//
// Where you post is which of those it is: a room, a thread (which is a run, or
// a subagent's branch of one), or both. One interface, three destinations.

// The event types the timeline indexes, and the kind each is shown as.
//
// The kinds are the vocabulary the harness already uses - a turn, a log line, a
// message, a steer - and the types are what the log calls them. Anything else
// in the log is shown as "activity": a status move, a task move, something the
// forge bridge did. They are not hidden, because a timeline that quietly omits
// rows is a timeline that lies about what happened.
const (
	activityTurn    = "turn"
	activityLog     = "log"
	activityChat    = "chat"
	activitySteer   = "steer"
	activityWorklog = "worklog"
	activityOther   = "activity"
)

// turnEventType, logEventType and steerEventType are what the three harness
// kinds are written as in the log. chat is store.ChatEventType, which was here
// already, and worklogEventType is what worklog_append writes.
const (
	turnEventType  = "turn"
	logEventType   = "run.log"
	steerEventType = "steer"
)

// activityKinds maps an event type onto the kind the timeline shows it as.
var activityKinds = map[string]string{
	store.ChatEventType: activityChat,
	turnEventType:       activityTurn,
	logEventType:        activityLog,
	steerEventType:      activitySteer,
	worklogEventType:    activityWorklog,
}

// postableKinds are what a client may post into the timeline, and the event
// type each is written as.
//
// It is a closed set for the same reason mintedTypes is: a status move and a
// task move are claims this node makes by doing the thing, and a timeline that
// let a client write one would be a timeline whose entries mean nothing. A
// steer is not one of those - it is somebody saying something to a run - so it
// is here.
var postableKinds = map[string]string{
	activityChat:  store.ChatEventType,
	activityTurn:  turnEventType,
	activityLog:   logEventType,
	activitySteer: steerEventType,
}

// readableKinds are what a read of the timeline may be narrowed to: everything
// a client may post, plus the kinds this node mints for itself that a reader
// still wants to single out.
//
// The two lists are not the same list, and the worklog is why. worklog_append
// checks every artifact id an entry references against the writer's own read
// filter, so an entry cannot point at work its author could not see; POST
// /api/activity has no such check and could not have one, because the refs are
// the worklog verb's argument and not a column on an event. Letting a client
// post the kind here would be a second door onto the same stream that skips the
// check on the first, which is exactly the shape this fabric refuses elsewhere.
// Reading is the other direction and opens nothing: the filter narrows what
// comes back, it does not widen it.
var readableKinds = func() map[string]string {
	out := map[string]string{activityWorklog: worklogEventType}
	for kind, eventType := range postableKinds {
		out[kind] = eventType
	}
	return out
}()

// kindOfEvent is what the timeline shows an event as.
func kindOfEvent(e *store.Event) string {
	if kind, ok := activityKinds[e.Type]; ok {
		return kind
	}
	return activityOther
}

// activityItem is one line of the timeline: what happened, who did it, where,
// and which trace it was part of.
type activityItem struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Type      string          `json:"type"`
	Actor     string          `json:"actor"`
	ActorKind string          `json:"actor_kind,omitempty"`
	ActorUser string          `json:"actor_user,omitempty"`
	Project   *string         `json:"project"`
	Room      string          `json:"room,omitempty"`
	Thread    string          `json:"thread,omitempty"`
	Artifact  string          `json:"artifact,omitempty"`
	Parents   []string        `json:"parents"`
	Body      string          `json:"body"`
	Trace     string          `json:"trace,omitempty"`
	SeqHLC    int64           `json:"seq_hlc"`
	Node      string          `json:"node"`
	Created   string          `json:"created"`
	Meta      json.RawMessage `json:"meta,omitempty"`
}

// handleActivity reads the timeline.
//
// Everything about it is the event log's: the order is seq_hlc, the cursor is
// the last reading the caller saw, and the filter is EventFilterSQL - so a run
// in another project is not on this timeline, a personal item is on nobody's
// but its owner's, and the thread of a handoff is on the timeline of the two
// people it is between.
//
// GET /api/activity?q=&kind=&room=&thread=&since=&limit=
func (s *server) handleActivity(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	since, err := cursorParam(q.Get("since"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("since must be a packed hlc integer"))
		return
	}

	var types []string
	for _, kind := range strings.Split(q.Get("kind"), ",") {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		// A kind that is not one of them narrows to nothing rather than to
		// everything: a filter nobody implements must not quietly widen a read.
		eventType, ok := readableKinds[kind]
		if !ok {
			writeJSON(w, http.StatusBadRequest,
				errorBody("kind must be one of "+strings.Join(sortedReadKinds(), ", ")))
			return
		}
		types = append(types, eventType)
	}

	list, err := s.db.ListEvents(r.Context(), p, store.EventQuery{
		Thread:   q.Get("thread"),
		Room:     q.Get("room"),
		Types:    types,
		Contains: q.Get("q"),
		Since:    since,
		ScopeAll: scopeAll(r, p),
		Limit:    intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	items := make([]activityItem, 0, len(list))
	for _, e := range list {
		items = append(items, itemOf(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"since":  since,
		"cursor": cursorOf(since, list),
		"query":  q.Get("q"),
	})
}

// itemOf renders one event as a timeline line.
func itemOf(e *store.Event) activityItem {
	item := activityItem{
		ID: e.ID, Kind: kindOfEvent(e), Type: e.Type, Actor: e.Actor,
		Project: e.Project, Room: e.Room, Thread: e.Thread, Artifact: e.Artifact,
		Parents: e.Parents, Body: e.Body, SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format("2006-01-02T15:04:05.999999Z07:00"),
		Meta:    e.Meta,
		Trace:   store.TraceOfMeta(e.Meta),
	}
	if item.Parents == nil {
		item.Parents = []string{}
	}
	// The speaker, as the node stamped it. It is read off meta rather than
	// guessed from the actor id, because that is where every other reader of an
	// event gets it and two ideas about who is talking is one too many.
	//
	// The decode is into raw messages and not into map[string]string, because
	// meta is not all strings: a worklog entry carries its refs there as a list,
	// and one non-string value used to fail the whole unmarshal and drop the
	// speaker off every event that had one - silently, since the error was
	// ignored on the reasonable-looking grounds that meta is optional.
	var fields map[string]json.RawMessage
	if len(e.Meta) > 0 {
		if err := json.Unmarshal(e.Meta, &fields); err == nil {
			item.ActorKind, item.ActorUser = metaString(fields, "actor_kind"), metaString(fields, "actor_user")
		}
	}
	return item
}

// metaString reads one string out of a decoded meta object, and answers "" for
// a key that is absent or is not a string.
func metaString(fields map[string]json.RawMessage, key string) string {
	raw, ok := fields[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// activityPost is what posting into the timeline takes: what to say, and where.
type activityPost struct {
	Kind     string   `json:"kind"`
	Body     string   `json:"body"`
	Room     string   `json:"room"`
	Thread   string   `json:"thread"`
	Parents  []string `json:"parents"`
	Artifact string   `json:"artifact"`
}

// handlePostActivity posts into the timeline: into a room, into a run's thread,
// or into a subagent's branch of one.
//
// It is the same write POST /api/chat/{room}/say is, with the same three gates -
// the thread has to be one the speaker may write to, the parents have to name
// events they may read, and the artifact has to be one they may read - because
// it is the same log. What it adds is the kind: a steer and a turn and a log
// line are different things to read back, and a timeline that called all of
// them "chat" would be one nobody could filter.
//
// POST /api/activity  {kind?, body, room?, thread?, parents?, artifact?}
func (s *server) handlePostActivity(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req activityPost
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("body is required"))
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = activityChat
	}
	eventType, ok := postableKinds[kind]
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("kind must be one of "+strings.Join(sortedKinds(), ", ")))
		return
	}
	if req.Room == "" && req.Thread == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody("say where: a room, a thread, or both"))
		return
	}
	if req.Room != "" && strings.Contains(req.Room, "/") {
		writeJSON(w, http.StatusBadRequest, errorBody("a room is one path segment"))
		return
	}
	if req.Parents == nil {
		req.Parents = []string{}
	}
	if !s.mayNameParents(w, r, req.Parents) {
		return
	}
	if !s.mayNameArtifact(w, r, req.Artifact) {
		return
	}
	if req.Thread != "" {
		if !s.mayWriteThread(w, r, req.Thread) {
			return
		}
		// Posting into a run that is already part of a trace joins that trace,
		// which is what makes "somebody steered this handoff" a span in the
		// story of the handoff rather than an unrelated request.
		s.adoptThreadTrace(r, req.Thread)
	} else {
		req.Thread = ulid.NewString()
	}

	actor, actorKind := chatActor(p)
	meta, err := json.Marshal(map[string]string{"actor_kind": actorKind, "actor_user": p.UserID})
	if err != nil {
		serverError(w, r, err)
		return
	}

	var project *string
	if p.Project != "" {
		home := p.Project
		project = &home
	}
	e := &store.Event{
		Type:     eventType,
		Project:  project,
		Room:     req.Room,
		Thread:   req.Thread,
		Parents:  req.Parents,
		Actor:    actor,
		Artifact: req.Artifact,
		Body:     req.Body,
		Meta:     withTrace(meta, traceIDOf(r)),
	}
	if err := s.db.AppendEvent(r.Context(), e); err != nil {
		serverError(w, r, err)
		return
	}
	if span := otel.SpanFrom(r.Context()); span != nil {
		span.SetAttr("activity.kind", kind)
	}
	writeJSON(w, http.StatusOK, itemOf(e))
}

// withTrace stamps the trace this write happened in onto an event's meta.
//
// This is how a trace crosses the node boundary. There is no request from the
// assigning node to the working node - what crosses is a delta - so the id
// rides the row, in a field that is inside the event's signature: a relay that
// holds neither node's key cannot change it, and the far node reads it back off
// the thread and continues the same trace rather than starting a second one.
//
// A client cannot write this key: speakerStripped drops it off anything that
// arrives from outside, for the same reason it drops the speaker keys. A trace
// somebody else can join is a trace somebody else can pollute.
func withTrace(meta json.RawMessage, traceID string) json.RawMessage {
	if !otel.ValidTraceID(traceID) {
		return meta
	}
	fields := map[string]json.RawMessage{}
	if len(meta) > 0 {
		if err := json.Unmarshal(meta, &fields); err != nil {
			fields = map[string]json.RawMessage{}
		}
	}
	id, err := json.Marshal(traceID)
	if err != nil {
		return meta
	}
	fields[store.TraceMetaKey] = id
	out, err := json.Marshal(fields)
	if err != nil {
		return meta
	}
	return out
}

func sortedKinds() []string { return sortedNames(postableKinds) }

func sortedReadKinds() []string { return sortedNames(readableKinds) }

func sortedNames(kinds map[string]string) []string {
	out := make([]string, 0, len(kinds))
	for kind := range kinds {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}
