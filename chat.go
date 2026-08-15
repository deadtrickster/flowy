package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// Chat is the event log seen from the side. A message is an event of type
// 'chat' in a room, and nothing about it is special: it carries the same
// project, the same seq_hlc cursor and the same parents DAG every other event
// carries, and it is read back through the same permission filter. A thread
// branches when two messages name the same parent, and merges when one names
// two - which is how a human and an agent can answer the same message without
// either of them losing the other's reply.
const chatEventType = "chat"

// waitWindow is how long a long poll blocks before giving up and answering with
// nothing. It is finite on purpose: the watcher contract is that a poll always
// returns, so a client that is wedged is wedged for a bounded time and a proxy
// in the middle never sees an idle socket it wants to cut. It sits under the
// server's WriteTimeout with room to spare.
const waitWindow = 25 * time.Second

// waitTick is how often a blocked poll asks the store whether anything landed.
// Polling rather than LISTEN/NOTIFY: the store is meant to be Postgres-wire
// portable, and NOTIFY is a property of Postgres the engine.
const waitTick = 250 * time.Millisecond

// chatSayRequest is the body of a message. Thread and Parents are how a client
// answers a particular message instead of the room: leave both out to start a
// thread, name the thread to continue it, name parents to say which message
// this one answers.
type chatSayRequest struct {
	Body    string   `json:"body"`
	Thread  string   `json:"thread"`
	Parents []string `json:"parents"`
}

// chatActor decides who is speaking. An agent posts as itself, a human as
// themselves - the same token rule the rest of the API uses, so a message
// cannot be attributed to somebody who did not hold the token that wrote it.
//
// The kind goes into the event's meta because the actor id alone does not carry
// it: a console rendering a room has to tell a person from the agent working
// for that person, and asking it to join against two tables to find out is how
// a chat view ends up doing a query per message.
func chatActor(p *store.Principal) (actor, kind string) {
	if p.AgentID != "" {
		return p.AgentID, "agent"
	}
	return p.UserID, "user"
}

// isOwnActor reports whether actor is the principal itself - either the person
// or the agent holding the token. It is what the inbox excludes.
func isOwnActor(p *store.Principal, actor string) bool {
	return actor != "" && (actor == p.UserID || actor == p.AgentID)
}

// roomOf reads and checks the {room} path segment. A room is one path segment:
// it has to survive being put back into a URL by a client, and a name with a
// slash in it does not.
func roomOf(r *http.Request) (string, bool) {
	room := strings.TrimSpace(r.PathValue("room"))
	if room == "" || strings.Contains(room, "/") {
		return "", false
	}
	return room, true
}

// mayWriteThread reports whether the principal may say something in a thread it
// named itself, and answers 403 when it may not.
//
// Writing into a thread is not a way round reading it. A thread id is a guess
// anybody can make, and the tasks clause in the event filter shows a thread's
// events to the parties to the task that names it - so a message dropped into
// somebody else's conversation is read by exactly the people whose conversation
// it is not, over a thread the speaker cannot see. The rule is the one the
// merge applies to a pushed event and a pushed task: a thread holding anything
// the caller may not read is closed to them. A thread with nothing in it is
// nobody's yet, and every conversation starts as one.
func (s *server) mayWriteThread(w http.ResponseWriter, r *http.Request, thread string) bool {
	p := principalOf(r)
	hidden, err := s.db.ThreadHidden(r.Context(), p, thread)
	if err != nil {
		serverError(w, r, err)
		return false
	}
	if hidden {
		writeJSON(w, http.StatusForbidden,
			errorBody("thread "+thread+" is a conversation you cannot read; "+
				"leave thread out and this starts one of its own"))
		return false
	}
	return true
}

// mayNameParents reports whether every id the writer named as a parent is an
// event it may read, and answers 400 when one is not.
//
// An edge in the log is a claim about what came before what, and it was the one
// thing on a write nobody checked: whatever the body said went into the column.
// A message could descend from an id that is not here, or from a conversation
// the writer cannot see, and the console draws those edges and future readers
// walk them. So the ids are checked the same way everything else that arrives by
// id is - through the read filter - and a parent that is not there and a parent
// that is out of reach get the same answer, which is the same answer a read of
// it would give.
func (s *server) mayNameParents(w http.ResponseWriter, r *http.Request, parents []string) bool {
	unreadable, err := s.db.UnreadableParents(r.Context(), principalOf(r), parents)
	if err != nil {
		serverError(w, r, err)
		return false
	}
	if len(unreadable) > 0 {
		writeJSON(w, http.StatusBadRequest,
			errorBody("parent "+unreadable[0]+" is not an event you can read; "+
				"an event descends from what is in front of you or from nothing"))
		return false
	}
	return true
}

// handleChatSay appends a message to a room.
//
// POST /api/chat/{room}/say  {body, thread?, parents?}
func (s *server) handleChatSay(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	room, ok := roomOf(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("room must be one non-empty path segment"))
		return
	}

	var req chatSayRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("body is required"))
		return
	}
	if req.Parents == nil {
		req.Parents = []string{}
	}
	if !s.mayNameParents(w, r, req.Parents) {
		return
	}
	if req.Thread == "" {
		// A message that answers something inherits that message's thread, so
		// a reply cannot start a second thread by accident. Every parent is an
		// event the speaker can read by the time this runs - an id is a guess
		// anybody can make, and inheriting a thread from a message the speaker
		// may not read would put them in a conversation they cannot see, and
		// put what they say next in front of the people who can.
		//
		// A readable parent is not a readable thread, either. The tasks clause
		// in the event filter shows one message of a thread to a party to the
		// task and the rest of it to nobody else, so a reply to that message
		// would have landed in a conversation the speaker cannot read - the
		// exact injection the explicit-thread path refuses. So the inherited
		// thread goes through the same test, and a closed one is not a 403: the
		// caller never named a thread, and answering 403 to a request that
		// mentioned no thread would say that the parent's is one worth
		// guessing. It starts a fresh thread instead.
		if len(req.Parents) > 0 {
			parent, err := s.db.ReadEvent(r.Context(), p, req.Parents[0])
			if err == nil && parent.Thread != "" {
				hidden, err := s.db.ThreadHidden(r.Context(), p, parent.Thread)
				if err != nil {
					serverError(w, r, err)
					return
				}
				if !hidden {
					req.Thread = parent.Thread
				}
			}
		}
		if req.Thread == "" {
			req.Thread = ulid.NewString()
		}
	} else if !s.mayWriteThread(w, r, req.Thread) {
		return
	}

	actor, kind := chatActor(p)
	meta, err := json.Marshal(map[string]string{"actor_kind": kind, "actor_user": p.UserID})
	if err != nil {
		serverError(w, r, err)
		return
	}

	// A message lands in the principal's home project, like every other write:
	// the room is scoped by the project, so two projects may both have a room
	// called general and neither one reads the other's.
	var project *string
	if p.Project != "" {
		home := p.Project
		project = &home
	}

	e := &store.Event{
		Type:    chatEventType,
		Project: project,
		Room:    room,
		Thread:  req.Thread,
		Parents: req.Parents,
		Actor:   actor,
		Body:    req.Body,
		Meta:    json.RawMessage(meta),
	}
	if err := s.db.AppendEvent(r.Context(), e); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// handleChatRead reads a room in log order, filtered to what the principal may
// see. since is the seq_hlc of the last message the caller has, and is strictly
// exclusive, so handing back the cursor from the previous read never repeats a
// message and never skips one.
//
// GET /api/chat/{room}?since=&thread=&limit=
func (s *server) handleChatRead(w http.ResponseWriter, r *http.Request) {
	room, ok := roomOf(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("room must be one non-empty path segment"))
		return
	}
	q := r.URL.Query()
	since, err := cursorParam(q.Get("since"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	list, err := s.readRoom(r, room, q.Get("thread"), since, intParam(q.Get("limit")))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeChatEvents(w, room, since, list)
}

// handleChatWait is the watcher: it blocks until something is said in the room
// after cursor, and answers with an empty list when the window runs out. The
// caller's own messages are included - it is a view of the room, not an inbox -
// so a second tab of the same console stays in step.
//
// GET /api/chat/{room}/wait?cursor=&thread=
func (s *server) handleChatWait(w http.ResponseWriter, r *http.Request) {
	room, ok := roomOf(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("room must be one non-empty path segment"))
		return
	}
	q := r.URL.Query()
	cursor, err := cursorParam(q.Get("cursor"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	thread := q.Get("thread")

	deadline := time.Now().Add(waitWindowOf(q.Get("window")))
	for {
		list, err := s.readRoom(r, room, thread, cursor, intParam(q.Get("limit")))
		if err != nil {
			serverError(w, r, err)
			return
		}
		if len(list) > 0 || !time.Now().Before(deadline) {
			writeChatEvents(w, room, cursor, list)
			return
		}

		select {
		case <-r.Context().Done():
			// The client hung up or the server is shutting down. Nothing to
			// write, and writing anyway would only log a broken pipe.
			return
		case <-time.After(waitTick):
		}
	}
}

// handleInbox is every chat message the principal may see and did not write.
// It crosses rooms and projects: what it answers is "what happened while I was
// away", which is the question a console asks on load and an agent asks when it
// wakes up.
//
// GET /api/inbox?since=&room=&limit=
func (s *server) handleInbox(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	since, err := cursorParam(q.Get("since"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	list, err := s.db.ListEvents(r.Context(), p, store.EventQuery{
		Type:      chatEventType,
		Room:      q.Get("room"),
		Since:     since,
		NotActors: []string{p.UserID, p.AgentID},
		ScopeAll:  scopeAll(r, p),
		Limit:     intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": list,
		"since":  since,
		"cursor": cursorOf(since, list),
	})
}

// readRoom is the one read the three chat endpoints share, so the room view,
// the long poll and a reload all narrow the log the same way.
func (s *server) readRoom(r *http.Request, room, thread string, since int64, limit int) ([]*store.Event, error) {
	p := principalOf(r)
	return s.db.ListEvents(r.Context(), p, store.EventQuery{
		Type:     chatEventType,
		Room:     room,
		Thread:   thread,
		Since:    since,
		ScopeAll: scopeAll(r, p),
		Limit:    limit,
	})
}

// writeChatEvents answers with the events and the cursor to ask for next, so a
// client never has to know that the cursor is a packed clock reading.
func writeChatEvents(w http.ResponseWriter, room string, since int64, list []*store.Event) {
	writeJSON(w, http.StatusOK, map[string]any{
		"room":   room,
		"events": list,
		"since":  since,
		"cursor": cursorOf(since, list),
	})
}

// cursorOf is the seq_hlc a caller should hand back next time: the last event's,
// or the one it came in with when nothing landed.
//
// That is only safe because the page it reads never cuts a reading in half -
// see ListEvents and pageOf in the store. Two messages written in the same
// instant on two nodes carry the same seq_hlc, and a page that ended between
// them would hand back a cursor that steps over the second one for good: the
// message is not late, it never arrives, and nothing says so.
func cursorOf(since int64, list []*store.Event) int64 {
	if n := len(list); n > 0 {
		return list[n-1].SeqHLC
	}
	return since
}

// waitWindowOf reads the ?window=<seconds> a client may ask for. It is clamped
// rather than trusted: a caller behind a proxy that cuts idle sockets at ten
// seconds needs a shorter window, and nobody needs a longer one than the
// server's own write timeout.
func waitWindowOf(param string) time.Duration {
	seconds, err := strconv.Atoi(param)
	if err != nil || seconds <= 0 {
		return waitWindow
	}
	if window := time.Duration(seconds) * time.Second; window < waitWindow {
		return window
	}
	return waitWindow
}

// cursorParam parses a packed-hlc cursor, treating absent as zero.
func cursorParam(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errNotACursor
	}
	return n, nil
}

// errNotACursor is what a malformed cursor turns into.
var errNotACursor = errors.New("since/cursor must be a packed hlc integer")
