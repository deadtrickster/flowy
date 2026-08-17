package main

// Per-room todos: the plan a room is making, filed where the room can see it.
//
// A todo has been an artifact of type memory with kind todo since the memory
// tools were written, and the queue was readable from anywhere except the one
// place the work is actually being agreed - the room. Two agents and a person
// talking in #build would settle what has to happen, and the settling lived in
// the messages: to find out what the room had decided you read the room back.
//
// The room is a field on the item, and it is a filter and nothing else. It goes
// in fields, the way as_of and supersedes ride a report, because that is where
// this fabric puts what a row is *about* as opposed to who may see it - see
// store.RoomField. A todo carrying room=build is the same project-scoped row it
// would be with no room on it: the same owner, the same visibility, the same
// permission filter in the same WHERE clause, and every todo written before
// this field existed has no room and is on every page it was on yesterday.
//
// Raising one from the room is the point. A conversation becomes a plan without
// leaving the conversation: the write is the todo and one message in the room
// under a single clock reading, the message names the artifact in the column
// the log already has for that, and the item keeps the id of the message that
// raised it. That link is what filing the same thing in another system loses -
// the ticket says what somebody decided and never why, and the why is four
// messages up.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// todoKind is what a todo is filed as: the memory kind, not a type of its own.
const todoKind = "todo"

// maxRoomName is the longest room a write may name. A room is one path segment
// on the chat routes, so a name that cannot go back into a URL is not a room
// this node has - refusing it here is the same door roomOf keeps, at the other
// surface.
const maxRoomName = 64

// roomArg validates the room a write names, and returns it trimmed. Empty is
// the ordinary case and means "no room": a global item, which is what every
// item written before this field is.
func roomArg(room string) (string, error) {
	room = strings.TrimSpace(room)
	if room == "" {
		return "", nil
	}
	if strings.ContainsAny(room, "/ \t\n") || len(room) > maxRoomName {
		return "", fmt.Errorf("room %q is not one: a room is a single path segment "+
			"of at most %d characters, with no spaces", room, maxRoomName)
	}
	return room, nil
}

// withRoom folds the room and the raising message into an item's fields.
//
// A value that is not restated is left alone, which is what makes an update
// that says only {id, status: done} keep the room it was raised in. Clearing
// one is not something this expresses, deliberately: a todo that moved rooms is
// a todo raised in the other room, and nothing here has asked to unsay where a
// piece of work came from.
func withRoom(fields map[string]any, room, message string) map[string]any {
	if room == "" && message == "" {
		return fields
	}
	if fields == nil {
		fields = map[string]any{}
	}
	if room != "" {
		fields[store.RoomField] = room
	}
	if message != "" {
		fields[store.MessageField] = message
	}
	return fields
}

// unreadableMessage is the refusal a raising message the writer cannot read
// gets. It is a type rather than a string so the HTTP surface can tell it from
// a store that could not answer - one is the caller's mistake and 404, the
// other is this node's and 500 - and both surfaces say the same sentence.
type unreadableMessage struct{ id string }

func (e unreadableMessage) Error() string {
	return "message " + e.id + " is not one you can read; a todo is raised out of " +
		"a conversation in front of you, or out of none"
}

// readableMessage refuses a message id the writer cannot read, and says so.
//
// It is the rule worklogRefs keeps for an artifact reference and mayNameParents
// keeps for an edge in the DAG, for the same reason: an id is a guess anybody
// can make, so a todo pointing at a conversation its author could not see is
// either a dangling pointer or an assertion about somebody else's room. A
// message that is not here and one that is out of reach get the same answer,
// which is the answer a read of it would give.
func readableMessage(ctx context.Context, db *store.DB, p *store.Principal, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	unreadable, err := db.UnreadableParents(ctx, p, []string{id})
	if err != nil {
		return err
	}
	if len(unreadable) > 0 {
		return unreadableMessage{id: id}
	}
	return nil
}

// roomTodoRequest is what raising a todo from a room takes. Everything but the
// title is optional: the point of the surface is that it costs one line in the
// middle of a conversation.
type roomTodoRequest struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// handleRoomTodoRaise files a todo out of a room, and says so in the room.
//
// POST /api/chat/{room}/todo  {title, body?, status?, message?}
//
// The two rows go in together, under one clock reading, through the same
// WriteMemory the memory tools write with: an item with nothing in the log
// behind it replicates on its own and nothing here ever comes back to finish a
// half-written operation. The message is an ordinary chat message in the room -
// same log, same permission filter, same readers - carrying the todo in the
// artifact column, which is the column an event already has for what it is
// about. So the room shows that the plan grew a line, and every surface that
// opens an artifact from a message opens this one with nothing added.
func (s *server) handleRoomTodoRaise(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	room, ok := roomOf(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("room must be one non-empty path segment"))
		return
	}
	// And the same bar every other surface names a room at. roomOf keeps the
	// URL's shape and nothing else, so a name longer than roomArg allows would
	// be written onto the item here and refused on the way back out - a todo in
	// a room its own panel could never ask for.
	room, err := roomArg(room)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	if p.UserID == "" {
		writeJSON(w, http.StatusForbidden,
			errorBody("this token resolves to no user, so it cannot own a todo"))
		return
	}

	var req roomTodoRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("title is required: a todo says what is to be done"))
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if err := readableMessage(r.Context(), s.db, p, req.Message); err != nil {
		var refused unreadableMessage
		if errors.As(err, &refused) {
			writeJSON(w, http.StatusNotFound, errorBody(refused.Error()))
			return
		}
		serverError(w, r, err)
		return
	}
	// The status a queue reads: active, todo, done. An unstated one is todo -
	// raising something is saying it has to happen, not that anybody started.
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = todoKind
	}

	fields, err := json.Marshal(withRoom(nil, room, req.Message))
	if err != nil {
		serverError(w, r, err)
		return
	}

	// A todo raised in a room is read by the room, so it is written at the
	// project's scope rather than the personal default a memory item takes:
	// filing the room's plan where nobody else in the room can read it is the
	// one outcome this surface must not produce. A token with no project has no
	// project to write into and keeps its own item, which is what a principal
	// with no project has for everything else here.
	art := &store.Artifact{
		Type:       memoryType,
		Kind:       todoKind,
		Title:      title,
		Body:       req.Body,
		Status:     status,
		OwnerUser:  p.UserID,
		Visibility: store.VisibilityForScope("project"),
		Fields:     fields,
	}
	if p.Project != "" {
		home := p.Project
		art.Project = &home
	}

	actor, kind := chatActor(p)
	meta, err := json.Marshal(speakerMeta(p, kind, s.speakerName(r.Context(), p)))
	if err != nil {
		serverError(w, r, err)
		return
	}
	thread, parents, err := s.raisedFrom(r, req.Message)
	if err != nil {
		serverError(w, r, err)
		return
	}

	e := &store.Event{
		Type:    chatEventType,
		Room:    room,
		Thread:  thread,
		Parents: parents,
		Actor:   actor,
		Body:    "raised a todo: " + title,
		Meta:    withTrace(json.RawMessage(meta), traceIDOf(r)),
	}
	if err := s.db.WriteMemory(r.Context(), art, e); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": art, "event": e})
}

// maxAssigneeName is the longest name a write may hand a todo. A handle around
// here is a word, and a panel column is narrow: the bar exists so that a body
// pasted into the box lands as a refusal rather than as a row nobody can read.
const maxAssigneeName = 64

// nobodyWords are the ways the queue has said "nobody is carrying this". They
// all collapse to the empty assignee, so every surface says ONE word for one
// state.
//
// Raised as a todo through the panel itself: 'todo list has "unowned" and
// "unassigned" - looks identical'. Two words for one state read as two states,
// and a reader goes looking for a distinction that is not there. The console
// keeps the same list in web/src/lib/todos.ts, for the bodies that were written
// before the field existed.
var nobodyWords = map[string]bool{
	"?": true, "-": true, "none": true, "nobody": true,
	"tbd": true, "unassigned": true, "unowned": true, "n/a": true,
}

// assigneeArg validates a name a write hands a todo, and returns it normalised.
//
// Empty is the ordinary case and means nobody: unassigning is a thing somebody
// does on purpose, and it is the same argument with nothing in it rather than a
// second verb. So are the words the queue has always used for nobody - they
// come back as the empty name, which is what makes the panel say one word.
func assigneeArg(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || nobodyWords[strings.ToLower(name)] {
		return "", nil
	}
	if strings.ContainsAny(name, "\n\r\t") || len(name) > maxAssigneeName {
		return "", fmt.Errorf("%q is not a name: an assignee is a handle of at most %d "+
			"characters on one line", name, maxAssigneeName)
	}
	return name, nil
}

// assigneeOf is who a todo says is carrying it: the field if it has one, and
// the body's OWNER line if it does not.
//
// The order is the compatibility. Every todo in this queue was written before
// there was a field, with `OWNER: <name>` as the first line of the body, and
// those still read the way they always did. But a key that is there wins even
// when it is empty - somebody said out loud that nobody is carrying this, and a
// read that fell through to a stale OWNER line would quietly undo them.
func assigneeOf(art *store.Artifact) string {
	if art == nil {
		return ""
	}
	if len(art.Fields) > 0 {
		var fields map[string]any
		if err := json.Unmarshal(art.Fields, &fields); err == nil {
			if named, found := fields[store.AssigneeField]; found {
				name, _ := named.(string)
				return strings.TrimSpace(name)
			}
		}
	}
	return ownerLine(art.Body)
}

// ownerLine reads the convention: `OWNER: <name>` as the FIRST line of the
// body. Further down it is a sentence about somebody else's item, not a claim
// about this one, which is the same read the TUI and the console each make.
func ownerLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if rest, found := strings.CutPrefix(line, "OWNER:"); found {
			name := strings.TrimSpace(rest)
			if nobodyWords[strings.ToLower(name)] {
				return ""
			}
			return name
		}
		if line != "" {
			return ""
		}
	}
	return ""
}

// assigneeRequest is what saying who is carrying a todo takes. One field, and
// an empty one means nobody.
type assigneeRequest struct {
	Assignee string `json:"assignee"`
}

// handleRoomTodoAssign says who is carrying one of the room's todos, and says
// it in the room.
//
// POST /api/chat/{room}/todo/{id}/assignee  {assignee}
//
// It is the raise's shape one step on: the field moves and the room hears about
// it in an ordinary chat message, under one clock reading, in the thread the
// todo was raised out of - so the conversation that produced the plan is also
// the conversation that says who picked it up.
//
// Whoever can READ the todo can say who is carrying it, which is the rule a
// status move already keeps (see handleArtifactStatus) and the right one for a
// room's plan: the point of a queue beside a conversation is that somebody in
// the conversation takes a line of it. It hands nobody anything - the assignee
// is a name in fields, and the permission filter has never looked there - so
// the widest this can be is "a person who can see the plan can edit the plan",
// and a principal who cannot see it gets the 404 a read would have given.
func (s *server) handleRoomTodoAssign(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	room, ok := roomOf(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("room must be one non-empty path segment"))
		return
	}
	room, err := roomArg(room)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	id := r.PathValue("id")

	var req assigneeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	name, err := assigneeArg(req.Assignee)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	art, err := s.db.ReadArtifact(r.Context(), p, id, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such todo"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	if art.Type != memoryType || art.Kind != todoKind {
		what := art.Type
		if art.Kind != "" {
			what += "/" + art.Kind
		}
		writeJSON(w, http.StatusBadRequest, errorBody(
			"a "+what+" carries no assignee; a todo does"))
		return
	}

	// The room in the path has to be the room on the item. A panel edits its
	// own room's plan, and an id is a guess anybody can make: without this,
	// #general's panel could write into #build's queue and say so in #general,
	// which is a change nobody in #build would ever see said out loud.
	var fields map[string]any
	if len(art.Fields) > 0 {
		if err := json.Unmarshal(art.Fields, &fields); err != nil {
			serverError(w, r, fmt.Errorf("todo %s carries fields that do not parse: %w", id, err))
			return
		}
	}
	if was, _ := fields[store.RoomField].(string); was != room {
		writeJSON(w, http.StatusNotFound, errorBody("no todo "+id+" in #"+room))
		return
	}

	was := assigneeOf(art)
	if fields == nil {
		fields = map[string]any{}
	}
	fields[store.AssigneeField] = name
	raw, err := json.Marshal(fields)
	if err != nil {
		serverError(w, r, err)
		return
	}

	actor, kind := chatActor(p)
	meta, err := json.Marshal(speakerMeta(p, kind, s.speakerName(r.Context(), p)))
	if err != nil {
		serverError(w, r, err)
		return
	}
	// Under the message the todo was raised out of, so the assignment lands in
	// the conversation that produced it rather than at the bottom of the room
	// on its own. A todo raised out of nothing starts a thread here.
	message, _ := fields[store.MessageField].(string)
	thread, parents, err := s.raisedFrom(r, message)
	if err != nil {
		serverError(w, r, err)
		return
	}

	e := &store.Event{
		Type:    chatEventType,
		Room:    room,
		Thread:  thread,
		Parents: parents,
		Actor:   actor,
		Body:    assignmentSaid(art.Title, was, name),
		Meta:    withTrace(json.RawMessage(meta), traceIDOf(r)),
	}
	if err := s.db.SetArtifactFields(r.Context(), art, raw, e); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": art, "event": e})
}

// assignmentSaid is the sentence the room reads. It names the previous holder
// when there was one, because "who has this now" and "who had it" are the two
// halves of a handover and the log is where the second one lives.
func assignmentSaid(title, was, now string) string {
	switch {
	case now == "" && was == "":
		return "left " + title + " unassigned"
	case now == "":
		return "took " + title + " off " + was
	case was == "":
		return "gave " + title + " to " + now
	default:
		return "moved " + title + " from " + was + " to " + now
	}
}

// raisedFrom decides where in the log the "raised a todo" message goes: under
// the message it came out of when there is one, and on its own otherwise.
//
// It is handleChatSay's rule, and the reasoning there holds here unchanged. A
// readable parent is not a readable thread - the tasks clause in the event
// filter shows one message of a thread to a party to the task and the rest of
// it to nobody - so a reply that inherited a hidden thread would land in a
// conversation the speaker cannot read. A hidden one starts a fresh thread
// rather than refusing: nobody named a thread here.
func (s *server) raisedFrom(r *http.Request, message string) (thread string, parents []string, err error) {
	if message == "" {
		return ulid.NewString(), []string{}, nil
	}
	p := principalOf(r)
	parents = []string{message}
	parent, err := s.db.ReadEvent(r.Context(), p, message)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// readableMessage has already refused this, so it means the row went
		// away between the two reads. A fresh thread, not a failure.
		return ulid.NewString(), parents, nil
	case err != nil:
		return "", nil, err
	}
	if parent.Thread != "" {
		hidden, err := s.db.ThreadHidden(r.Context(), p, parent.Thread)
		if err != nil {
			return "", nil, err
		}
		if !hidden {
			return parent.Thread, parents, nil
		}
	}
	return ulid.NewString(), parents, nil
}
