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

// raisedBy is who a queue item's work came from when it came out of a message:
// the speaker of that message, resolved the way every other surface resolves a
// speaker's name.
//
// THIS IS THE POINT OF THE FIELD. A row filed out of a conversation should say
// whose request it was without anybody typing it - the operator asks for
// something in #general, an agent files the line, and the trail from the row
// back to the ask is one field rather than four messages of scrollback. An
// explicit raiser overrides it, which is the case of an agent filing on
// somebody's behalf out of no message at all.
//
// A message this writer cannot read has already been refused by
// readableMessage, so ErrNotFound here means the row went away between two
// reads: the todo is written with no raiser rather than the write failing. The
// work still came from somewhere and this node can no longer say where, which
// is what an empty raiser says.
//
// A resolved name that will not normalise is dropped for the same reason. The
// default is this node's courtesy on somebody else's write, and a handle that
// happens to be too long for the column must not turn their todo into a
// refusal - what they asked for was a todo.
func raisedBy(ctx context.Context, db *store.DB, p *store.Principal, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return "", nil
	}
	said, err := db.ReadEvent(ctx, p, message)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "", nil
	case err != nil:
		return "", err
	}
	name, err := store.NormalizeRaiser(speakerNameOfEvent(ctx, db, said))
	if err != nil {
		return "", nil
	}
	return name, nil
}

// roomTodoRequest is what raising a todo from a room takes. Everything but the
// title is optional: the point of the surface is that it costs one line in the
// middle of a conversation.
type roomTodoRequest struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Category string `json:"category"`
	// Who the work came from, when it did not come from whoever is posting
	// this. Left out, a todo raised out of a message takes the speaker of that
	// message - see raisedBy - and one raised out of no message says nobody,
	// because owner_user already records the seat that typed it and inventing a
	// second copy of that answer would be inventing the fact this field exists
	// to keep honest.
	Raiser string `json:"raiser"`
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
	//
	// A stated one goes through the same door every other write of a queue status
	// goes through, which is what keeps the vocabulary one vocabulary: a row
	// raised as "finished" here would be a todo that nothing downstream counts as
	// done, and the refusal it gets from every other surface would arrive a day
	// later. See store.NormalizeTodoStatus.
	status := store.TodoStatus
	if strings.TrimSpace(req.Status) != "" {
		status, err = store.NormalizeTodoStatus(req.Status)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
	}

	// What kind of work it is, out of the closed set, and refused here rather
	// than stored as whatever was typed: the vocabulary is one vocabulary at
	// every door or it is not closed at all. Unstated is unclassified, which is
	// what every todo raised before this field is - a room raising a line in the
	// middle of a conversation is not made to classify it first.
	category, err := store.NormalizeTodoCategory(req.Category)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	// And who it came from. Stated, it is whoever the writer says - an agent
	// filing a line on the operator's behalf says so here. Unstated and raised
	// out of a message, it is that message's speaker, which is the case this
	// whole field is for. Unstated and raised out of nothing, it stays absent:
	// see store.RaiserField, where a row that says nothing is not a row to
	// guess owner_user onto.
	raiser, err := store.NormalizeRaiser(req.Raiser)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	if raiser == "" {
		if raiser, err = raisedBy(r.Context(), s.db, p, req.Message); err != nil {
			serverError(w, r, err)
			return
		}
	}
	raised := withRoom(nil, room, req.Message)
	if category != "" {
		if raised == nil {
			raised = map[string]any{}
		}
		raised[store.CategoryField] = category
	}
	if raiser != "" {
		if raised == nil {
			raised = map[string]any{}
		}
		raised[store.RaiserField] = raiser
	}
	fields, err := json.Marshal(raised)
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
	events := []*store.Event{e}
	// A stated category leaves its entry in the same transaction, for the reason
	// mem_write's does: the author filing this as a bug and somebody else
	// reclassifying it later are the same claim, and a value that sometimes has an
	// entry behind it is a log that cannot answer who called it one.
	if category != "" {
		entry, err := store.TodoCategoryEntryEvent(art, principalOf(r), "", category)
		if err != nil {
			serverError(w, r, err)
			return
		}
		events = append(events, entry)
	}
	// Through the queue's error mapper rather than straight to a 500: this door
	// takes a status and has never taken an assignee, so raising a row as
	// `active` is a thing a caller can ask for and the store will not hold - and
	// a refusal the caller can act on must not arrive as a broken node. See
	// store.ActiveUnownedError.
	if err := s.db.WriteMemory(r.Context(), art, events...); err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	// The row this door just wrote, answered in the shape a READ of it would
	// have. Without this the raiser is in fields and absent from the row, which
	// is how the gate found it: `fields.raiser` correct, `item.raiser` null.
	store.FillDerived(art)
	writeJSON(w, http.StatusOK, map[string]any{"item": art, "event": e})
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
