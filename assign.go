package main

// WHO IS CARRYING A TODO, over HTTP.
//
// THE RULES ARE ALL IN THE STORE - see internal/store/assign.go, which is where
// they are and why they are those rules - so this file is argument checking, a
// sentence for the room, and status codes. That is deliberate and it is the shape
// the worklog and the dependency surfaces already have: an operator clicking a
// name in the console, an agent claiming a task over MCP and a drainer handing one
// on over HTTP must not be able to reach three ideas of who is carrying what, and
// the way to guarantee that is that none of them holds one. store.AssignTodo is
// the only thing in this program that moves an assignee.
//
// THERE ARE THREE DOORS AND ONE WAY IN.
//
//   - POST /api/todo/{id}/assignee - any todo you can read, from anywhere. This
//     is the one that was missing, and its absence is what this change is about:
//     an operator with a queue full of one agent's todos had no way at all to
//     hand any of them out, because the only door was the room panel's and it
//     only opens for a todo raised in a room.
//   - POST /api/chat/{room}/todo/{id}/assignee - the room panel's door, which
//     also says the handover out loud in the room. It is the same write with a
//     chat message beside it, in one transaction.
//   - todo_assign over MCP - see mcp_assign.go, which is twelve lines because it
//     is a caller of this and not a second implementation.
//
// A REFUSAL HERE IS ABOUT THE TODO AND NEVER ABOUT THE ASSIGNEE. Read permission
// is the whole bar: an id you cannot read is answered as an id that is not there,
// and there is nothing else to be refused for. In particular naming somebody who
// does not exist here is NOT refused - an assignee is a claim about the work, not
// a principal, and the node resolves it to nothing. See store.AssigneeField.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
)

// assigneeRequest is what saying who is carrying a todo takes. One field, and an
// empty one means nobody.
type assigneeRequest struct {
	Assignee string `json:"assignee"`
	// Expect turns a handover into a CLAIM. It is the holder the caller read
	// before deciding to take the row - "" for a row nobody held - and when the
	// key is PRESENT the write is refused, naming the winner, if the row moved
	// in between. Absent, this is an ordinary assignment and stays
	// last-write-wins, because handing somebody work is not a race.
	//
	// A pointer, so absent and present-but-empty are different requests. That
	// distinction is the whole feature: claiming an unowned row is exactly
	// expect:"".
	Expect *string `json:"expect"`
}

// assignView is one todo's assignment as every surface hands it back: the item,
// who has it, who said so, and the log the answer was folded out of.
//
// The log is in it rather than behind a second call for depsView's reason. The
// name is the value and the entries are the record: a reader given only "b-agent
// has it" cannot answer who handed it to them or when, and that question - WHO
// gave this away - is the whole reason an assignment is an event.
type assignView struct {
	Item       *store.Artifact     `json:"item"`
	Assignee   string              `json:"assignee"`
	Assignment *store.Assignment   `json:"assignment"`
	Log        []store.AssignEntry `json:"log"`
}

// viewAssignment assembles it. Every surface calls this, so a console and an
// agent are looking at one answer rather than at two that agree today.
func viewAssignment(
	ctx context.Context, db *store.DB, p *store.Principal, art *store.Artifact,
) (*assignView, error) {
	log, err := db.AssignLog(ctx, p, art.ID)
	if err != nil {
		return nil, err
	}
	return &assignView{
		Item: art, Assignee: assigneeOf(art),
		Assignment: store.LatestAssignment(log), Log: log,
	}, nil
}

// assigneeOf is who a todo says is carrying it. The rule is the store's - see
// store.AssigneeOf - because the ready query asks the same question of the same
// key, and being carried is half of whether a todo can be started. Two readers of
// one key are two chances to disagree about whether somebody is on it.
func assigneeOf(art *store.Artifact) string { return store.AssigneeOf(art) }

// handleTodoAssign says who is carrying a todo - any todo the caller can read,
// wherever it was raised.
//
// POST /api/todo/{id}/assignee  {assignee}
//
// Whoever can READ the todo may say who is carrying it, and may override what
// somebody else said. That is the ruling this endpoint exists to implement: a
// queue filed by one agent that nobody else can ever own is a queue with one
// worker, and the operator asking three times why every row said nobody was the
// symptom of not having this door.
//
// It hands the named party nothing - the assignee is a name in fields and the
// permission filter has never looked there - so the widest this reaches is
// "somebody who can see the work can say who is doing it", and a principal who
// cannot see it gets the 404 a read would have given.
//
// THE ROOM HEARS THIS DOOR TOO, and the argument that it should not was wrong
// in a way worth keeping visible: "a door that does not know which room it is
// in would be a message in the wrong place". The door does not know. THE ROW
// DOES - every todo raised in a room carries it in fields.room, which is the
// same value the room panel's door checks its path against.
//
// What that mistake cost, measured on 2026-08-19: at 00:12 I claimed a row
// through this door and said nothing; at 02:18 flowy-claude read the board, saw
// it unowned, announced taking it, and the guard refused them by name. Nobody
// was careless and the board knew the whole time - the room, which is where
// every seat here decides what to pick up, had no way to learn it. Earlier the
// same night two agents landed on one row within two minutes for the same
// reason.
//
// A claim is not a state change somebody can look up later. It is a message to
// the other seats, and the log is not a place anybody watches: the entry was
// always there, and being there is what makes "the state is not missing, the
// EVENT is unrouted" the accurate description of the gap.
//
// A row raised in NO room still says nothing, and that is honest rather than a
// remaining half of the bug: there is no conversation it belongs to, and
// inventing one would put a handover in front of people who never saw the work.
func (s *server) handleTodoAssign(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req assigneeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	// TWO VERBS THROUGH ONE DOOR, told apart by whether the caller stated what
	// it expected to find. With `expect` this is a claim, and exactly one of two
	// racing callers wins. Without it, it is a plain assignment - which an
	// UNHELD row still takes from anybody, and which a held row now refuses,
	// naming the holder: an unguarded write used to move a held row, and twice
	// in one morning that is exactly how a guarded claim got overwritten by a
	// careless one. The handover path is the guarded path with the holder named
	// - one field longer, and it cannot be fallen into by accident.
	var art *store.Artifact
	// WHICH ROOM, ASKED OF THE ROW. Read before the write, because after it the
	// row is the new state and the message names who HAD it - and a failure to
	// read it is not a reason to refuse the assignment, only to make it quietly.
	said, err := s.claimHeardIn(r, r.PathValue("id"), req.Assignee)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if req.Expect != nil {
		art, _, err = s.db.ClaimTodo(r.Context(), p, r.PathValue("id"), req.Assignee, *req.Expect, said)
	} else {
		art, _, err = s.db.AssignTodo(r.Context(), p, r.PathValue("id"), req.Assignee, said)
	}
	if err != nil {
		s.writeAssignError(w, r, err)
		return
	}
	view, err := viewAssignment(r.Context(), s.db, p, art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleTodoAssignee reads it back: who has the todo, who gave it to them, and
// every claim anybody has made on it that this reader may see.
//
// GET /api/todo/{id}/assignee
func (s *server) handleTodoAssignee(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	art, err := s.db.ReadWorkItem(r.Context(), p, r.PathValue("id"))
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	view, err := viewAssignment(r.Context(), s.db, p, art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleRoomTodoAssign says who is carrying one of the room's todos, and says it
// in the room.
//
// POST /api/chat/{room}/todo/{id}/assignee  {assignee}
//
// It is the raise's shape one step on: the field moves, the entry lands in the
// log, and the room hears about it in an ordinary chat message in the thread the
// todo was raised out of - all in one transaction, so the conversation that
// produced the plan is also the conversation that says who picked it up.
//
// The permission story is store.AssignTodo's and is not repeated here. What this
// door adds is the two things that are about the ROOM rather than about the
// assignment: the item has to be a todo of THIS room, and the room is told.
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
	name, err := store.NormalizeAssignee(req.Assignee)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	// Read before writing, because this door has two things to say that the verb
	// cannot: whether the item is the kind of thing that carries an assignee at
	// all, and whether it is this room's. Both are answered from the item, so the
	// item is read here - and the write reads it again through the same filter,
	// which is what settles it.
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

	// The room in the path has to be the room on the item. A panel edits its own
	// room's plan, and an id is a guess anybody can make: without this,
	// #general's panel could write into #build's queue and say so in #general,
	// which is a change nobody in #build would ever see said out loud. A todo
	// raised in no room at all is assigned through POST /api/todo/{id}/assignee,
	// which is not a room's door and does not pretend to be.
	fields, err := store.ArtifactFields(art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if was, _ := fields[store.RoomField].(string); was != room {
		writeJSON(w, http.StatusNotFound, errorBody("no todo "+id+" in #"+room))
		return
	}

	said, err := s.assignmentMessage(r, art, room, fields, assigneeOf(art), name)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The same two verbs as the door without a room, told apart the same way:
	// expect stated is a claim, expect absent is a plain assignment that a held
	// row refuses. The room hears either way - `said` is the message - because
	// the room this todo was raised in is the room its plan changes hands in
	// front of, whichever verb moved it.
	if req.Expect != nil {
		art, _, err = s.db.ClaimTodo(r.Context(), p, id, name, *req.Expect, said)
	} else {
		art, _, err = s.db.AssignTodo(r.Context(), p, id, name, said)
	}
	if err != nil {
		s.writeQueueError(w, r, err)
		return
	}
	view, err := viewAssignment(r.Context(), s.db, p, art)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// item and event are what this door has always answered with, and the
	// assignment beside them is the record the write now leaves: a client reading
	// either one is reading the same write.
	writeJSON(w, http.StatusOK, map[string]any{
		"item": view.Item, "event": said, "assignment": view.Assignment,
	})
}

// assignmentMessage builds the chat message the room reads, in the thread the
// todo was raised out of.
//
// The event has no clock reading and no project on it: store.SetArtifactFields
// stamps both, from the item, which is what makes the message the project's
// rather than the speaker's. An event with no project is readable by its own
// actor and nobody else - see EventFilterSQL - so a message announcing that the
// plan changed hands would otherwise be a message the room never got, which is
// indistinguishable from the feature working from everywhere except somebody
// else's screen.
func (s *server) assignmentMessage(
	r *http.Request, art *store.Artifact, room string, fields map[string]any, was, now string,
) (*store.Event, error) {
	p := principalOf(r)
	actor, kind := chatActor(p)
	meta, err := json.Marshal(speakerMeta(p, kind, s.speakerName(r.Context(), p)))
	if err != nil {
		return nil, err
	}
	// Under the message the todo was raised out of, so the assignment lands in
	// the conversation that produced it rather than at the bottom of the room on
	// its own. A todo raised out of nothing starts a thread here.
	message, _ := fields[store.MessageField].(string)
	thread, parents, err := s.raisedFrom(r, message)
	if err != nil {
		return nil, err
	}
	return heardInProject(&store.Event{
		Type:    chatEventType,
		Room:    room,
		Thread:  thread,
		Parents: parents,
		Actor:   actor,
		Body:    assignmentSaid(art.Title, was, now),
		Meta:    withTrace(json.RawMessage(meta), traceIDOf(r)),
	}, art), nil
}

// claimHeardIn is the message the room reads when a row changes hands through
// the door that is not a room's own, or nil when there is no room to say it in.
//
// It reads the row through the ordinary permission-filtered read, so a caller
// who cannot see the todo gets nil here and the refusal from the verb itself -
// this must not become a way to find out that an id exists.
//
// A FAILURE TO BUILD THE MESSAGE IS NOT A FAILURE TO ASSIGN. The room hearing
// about a handover is the point of this row, and it is still less important
// than the handover landing: a node that refused to move work because it could
// not announce it would have replaced a silent success with a loud nothing.
func (s *server) claimHeardIn(r *http.Request, id, now string) (*store.Event, error) {
	art, err := s.db.ReadArtifact(r.Context(), principalOf(r), id, false)
	if err != nil {
		// Including not-found and not-readable: the verb answers those, and
		// answering them here first would answer them twice.
		return nil, nil
	}
	fields, err := store.ArtifactFields(art)
	if err != nil {
		return nil, nil
	}
	room, _ := fields[store.RoomField].(string)
	if strings.TrimSpace(room) == "" {
		return nil, nil
	}
	return s.assignmentMessage(r, art, room, fields, assigneeOf(art), now)
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

// writeAssignError maps this door's refusals.
//
// A lost claim is 409 rather than 400: the request was well formed, the caller
// may make it again against another row, and a client retrying needs to tell
// "somebody beat you" apart from "you asked wrongly". Everything else keeps the
// mapping the queue verbs already share.
// A lost claim carries the row explaining it, when somebody has written one, on
// the same terms every other refusal here does - see knownissue.go. This is the
// door the row calls out by name: "a claim lost to a compare-and-set" is on the
// list of things already explained in chat at least once, and it will be
// explained again to whoever loses the next one.
func (s *server) writeAssignError(w http.ResponseWriter, r *http.Request, err error) {
	var held store.ErrHeldBy
	if errors.As(err, &held) {
		s.writeRefusal(w, r, http.StatusConflict, err, held.Error())
		return
	}
	s.writeQueueError(w, r, err)
}
