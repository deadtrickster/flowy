package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// Assignment is three writes and one clock reading.
//
// Handing a piece of work to somebody in another project means all of:
//
//   - a share, so they can read the artifact at all - a task pointing at
//     something the assignee gets a 404 on is not a handoff, it is a riddle;
//   - a task row, which is the state of the handoff and the only thing either
//     side has to poll;
//   - a thread, opened with a message, so the conversation about the work is
//     the same log as everything else rather than a comment field.
//
// All three carry the same hlc reading. They are one operation as far as
// ordering goes, so a peer merging them cannot interleave anything between the
// share and the task it exists for.
//
// They are written in that order for the same reason: if the node dies halfway,
// what is left behind is a share with nothing pointing at it - harmless, and
// visible - rather than a task whose artifact the assignee cannot open.
const (
	// assignRoom is the room assignment threads live in. It is a room like any
	// other: the console lists it, the chat endpoints read it, and the thread
	// carries the conversation.
	assignRoom = "handoffs"
	// taskEventType is what a task's own moves are logged as, in the same
	// thread as the conversation, so the audit trail and the chat are one
	// thing read in one order.
	taskEventType = "task"
)

// assignRequest is the body of POST /api/assign.
type assignRequest struct {
	Artifact string `json:"artifact"`
	ToUser   string `json:"to_user"`
	Note     string `json:"note"`
}

// assignResponse is the task, with the two rows written alongside it. The task
// is inlined rather than nested: a client that only wants "what did I just
// create" reads .id and .state off the top level.
type assignResponse struct {
	*store.Task
	Grant   *store.Grant `json:"grant"`
	Opening *store.Event `json:"opening"`
}

// handleAssign shares an artifact with somebody and hands them the work on it.
//
// The caller has to own the artifact, which is the bar POST /api/grants keeps -
// because the first of the three writes is a share, and a share is the owner's
// to give. Reading was not enough and never could be: the share it wrote named
// the reader as its grantor, so any reader of an artifact could hand any user a
// read on it, and the row it minted was one no peer would take either -
// checkGrant refuses a share whose granted_by is not the artifact's owner, so a
// reader-made assignment landed its task on the far side with the share that
// makes the task openable refused behind it.
//
// Passing on work that was shared with you is a real thing to want and it is
// deliberately not this: it needs a re-share capability the owner sets on the
// grant, and the push rule to match. See the delegation note in the README.
//
// POST /api/assign  {artifact, to_user, note?}
func (s *server) handleAssign(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()

	var req assignRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if req.Artifact == "" || req.ToUser == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("artifact and to_user are required"))
		return
	}

	art, err := s.db.ReadArtifact(ctx, p, req.Artifact, false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such artifact"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The personal floor again: a personal artifact has no project to share it
	// into and no grant reaches through it, so an assignment of one would be a
	// task nobody but the owner could open. 'project-only' is the same thing one
	// step up - the read filter stops at the project and the share clause below
	// it is never reached - so a share written for one is a share that can never
	// take effect, and the task it comes with is the riddle again.
	if art.Visibility == store.VisibilityPersonal ||
		art.Visibility == store.VisibilityProjectOnly || art.Project == nil {
		writeJSON(w, http.StatusBadRequest,
			errorBody("a personal or project-only artifact cannot be assigned; share it first"))
		return
	}
	// And a share is the owner's to give - the rule POST /api/grants keeps.
	if art.OwnerUser != p.UserID {
		writeJSON(w, http.StatusForbidden, errorBody("not the owner of "+art.ID))
		return
	}

	to, err := s.db.GetUser(ctx, req.ToUser)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusBadRequest, errorBody("no such user: "+req.ToUser))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	grant := &store.Grant{
		FromProject: p.Project,
		ToProject:   *art.Project,
		Subject:     to.ID,
		Artifact:    art.ID,
		Cap:         "read",
		GrantedBy:   p.UserID,
	}

	// The id and the thread are minted here rather than by the store, because
	// the message that opens the thread names the task in its meta and is built
	// before any of the three rows is written. Ids are minted anywhere: that is
	// what a ULID is for.
	task := &store.Task{
		ID:       ulid.NewString(),
		Artifact: art.ID,
		FromUser: p.UserID,
		ToUser:   to.ID,
		Project:  *art.Project,
		State:    store.TaskOpen,
		Thread:   ulid.NewString(),
	}

	// auto_delegate is the assignee's standing answer to inbound work: yes,
	// give it to my agent. It is read here rather than asked for in the
	// request, because it is the receiver's policy and not the sender's.
	var agent *store.Agent
	if to.AutoDelegate {
		agent, err = s.db.AgentForUser(ctx, to.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Nobody to delegate to. The task waits for the person, which is
			// what it would have done with auto_delegate off.
			agent = nil
		case err != nil:
			serverError(w, r, err)
			return
		default:
			task.AssigneeAgent = agent.ID
			task.State = store.TaskDelegated
		}
	}
	opening, err := s.assignmentOpening(r, task, art, req.Note)
	if err != nil {
		serverError(w, r, err)
		return
	}

	// The three of them, or none of them. A failure between two of these writes
	// used to leave whatever had already landed: a share nothing points at at
	// best, and at worst a task about an artifact its assignee gets a 404 on,
	// or a handoff nobody was told about. Nothing came back to finish it, and
	// the half replicated on its own.
	if err := s.db.WriteAssignment(ctx, grant, task, opening); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, assignResponse{Task: task, Grant: grant, Opening: opening})
}

// assignmentOpening builds the message that starts the conversation. It is an
// ordinary chat event - same type, same room, same meta - so the console renders
// it with the chat it already had, and the thread it opens is the one both
// sides answer in.
//
// It is built rather than written: the assignment's three rows go in together,
// under one reading, in WriteAssignment.
func (s *server) assignmentOpening(
	r *http.Request, task *store.Task, art *store.Artifact, note string,
) (*store.Event, error) {
	p := principalOf(r)
	actor, kind := chatActor(p)

	body := strings.TrimSpace(note)
	if body == "" {
		what := art.Title
		if what == "" {
			what = art.Type + " " + art.ID
		}
		body = "assigned " + what + " to you"
	}

	meta, err := json.Marshal(map[string]string{
		"actor_kind": kind,
		"actor_user": p.UserID,
		"task":       task.ID,
		"artifact":   art.ID,
	})
	if err != nil {
		return nil, err
	}

	// The thread's project is the artifact's, not the sender's: the assignment
	// is about that artifact, and the two are read by the same people. Either
	// side reaches the thread through the task itself in any case - see the
	// tasks clause in EventFilterSQL.
	project := *art.Project
	return &store.Event{
		Type:     chatEventType,
		Project:  &project,
		Room:     assignRoom,
		Thread:   task.Thread,
		Parents:  []string{},
		Actor:    actor,
		Artifact: art.ID,
		Body:     body,
		Meta:     json.RawMessage(meta),
	}, nil
}

// handleInboxTasks is the work waiting for the principal: the tasks handed to
// them or to their agent, newest first, with the artifact they are about.
//
// GET /api/inbox/tasks?state=&limit=
func (s *server) handleInboxTasks(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()

	list, err := s.db.ListTasks(r.Context(), p, store.TaskQuery{
		State: q.Get("state"),
		Limit: intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": list})
}

// handleGetTask returns one task, to a party to it. Anybody else gets 404: a
// handoff is between two people, and telling a third that it exists is the leak.
//
// GET /api/task/{id}
func (s *server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParty(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// delegateRequest names the agent to hand the work to. Leave it out and the
// assignee's own agent is chosen.
type delegateRequest struct {
	Agent string `json:"agent"`
}

// handleDelegateTask hands a task to the assignee's agent.
//
// Only the assignee may, or an agent holding a token that resolves to them:
// delegation is the receiver deciding how their work gets done, and the sender
// pushing it onto somebody's agent would be the sender deciding.
//
// POST /api/task/{id}/delegate  {agent?}
func (s *server) handleDelegateTask(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	ctx := r.Context()

	task, ok := s.taskParty(w, r)
	if !ok {
		return
	}
	if task.ToUser != p.UserID {
		writeJSON(w, http.StatusForbidden, errorBody("only the assignee may delegate this task"))
		return
	}

	// The body is optional here - "delegate this to whoever normally does my
	// work" is the common case and does not need one - so an empty request is
	// not a malformed one.
	var req delegateRequest
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}

	var agent *store.Agent
	var err error
	if req.Agent != "" {
		agent, err = s.db.GetAgent(ctx, req.Agent)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusBadRequest, errorBody("no such agent: "+req.Agent))
			return
		}
	} else {
		agent, err = s.db.AgentForUser(ctx, task.ToUser)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusBadRequest, errorBody("the assignee has no agent to delegate to"))
			return
		}
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	// An agent acts for exactly one user, so handing a task to somebody else's
	// agent would put the work outside the person who owns it.
	if agent.UserID != task.ToUser {
		writeJSON(w, http.StatusForbidden,
			errorBody("agent "+agent.ID+" does not act for the assignee"))
		return
	}

	was := task.State
	task.AssigneeAgent = agent.ID
	task.State = store.TaskDelegated

	// The move and its entry, or neither. They were two writes, and a failure
	// between them left a task in a state its own thread does not account for -
	// and the half that landed replicated on its own, so every peer held it.
	event, err := s.taskEvent(r, task, was+"->"+task.State, agent.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.db.UpdateTaskEvent(ctx, task, event); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task, "event": event})
}

// stateRequest is the body of a state move.
type stateRequest struct {
	State string `json:"state"`
}

// handleTaskState moves a task between open, delegated and done. Either party
// may: the assignee finishes the work, and the person who handed it over can
// take it back or close it themselves.
//
// POST /api/task/{id}/state  {state}
func (s *server) handleTaskState(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParty(w, r)
	if !ok {
		return
	}

	var req stateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if !store.ValidTaskState(req.State) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("state must be one of open, delegated, done"))
		return
	}
	// Delegation is what names an agent, so moving to it by hand without one
	// would leave a task marked as somebody's agent's with no agent on it.
	if req.State == store.TaskDelegated && task.AssigneeAgent == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody("delegate the task first: /api/task/"+task.ID+"/delegate"))
		return
	}

	was := task.State
	task.State = req.State

	// One write, for the same reason the delegate above is one: a state a task
	// is in and the record of it getting there are one fact.
	event, err := s.taskEvent(r, task, was+"->"+task.State, "")
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.db.UpdateTaskEvent(r.Context(), task, event); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task, "event": event})
}

// autoDelegateRequest is the body of PUT /api/me/auto_delegate.
type autoDelegateRequest struct {
	On bool `json:"on"`
}

// handleAutoDelegate sets the principal's standing answer to inbound work.
//
// It is the user's row that moves, not the agent's: a token held by an agent
// resolves to its user, and the policy belongs to the person either way.
//
// PUT /api/me/auto_delegate  {on}
func (s *server) handleAutoDelegate(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	var req autoDelegateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if p.UserID == "" {
		writeJSON(w, http.StatusForbidden, errorBody("this token resolves to no user"))
		return
	}

	user, err := s.db.SetAutoDelegate(r.Context(), p.UserID, req.On)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such user"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// taskParty reads the task named in the path and answers 404 itself when the
// principal is not a party to it. It reports whether the caller should carry on.
func (s *server) taskParty(w http.ResponseWriter, r *http.Request) (*store.Task, bool) {
	task, err := s.db.ReadTask(r.Context(), principalOf(r), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such task"))
		return nil, false
	}
	if err != nil {
		serverError(w, r, err)
		return nil, false
	}
	return task, true
}

// taskEvent builds a move's entry in the task's own thread, as a child of
// whatever was last said there. The thread is the conversation and the audit
// trail at once: reading it in order tells you what was said and what happened
// between the messages.
//
// It is built rather than written: the move and its entry go in together, in
// UpdateTaskEvent.
func (s *server) taskEvent(
	r *http.Request, task *store.Task, body, agent string,
) (*store.Event, error) {
	p := principalOf(r)
	actor, kind := chatActor(p)

	fields := map[string]string{"actor_kind": kind, "actor_user": p.UserID, "task": task.ID}
	if agent != "" {
		fields["agent"] = agent
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}

	parents := []string{}
	if last, err := s.db.LatestThreadEvent(r.Context(), task.Thread); err == nil {
		parents = append(parents, last.ID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	var project *string
	if task.Project != "" {
		home := task.Project
		project = &home
	}
	e := &store.Event{
		Type:     taskEventType,
		Project:  project,
		Room:     assignRoom,
		Thread:   task.Thread,
		Parents:  parents,
		Actor:    actor,
		Artifact: task.Artifact,
		Body:     body,
		Meta:     json.RawMessage(meta),
	}
	return e, nil
}
