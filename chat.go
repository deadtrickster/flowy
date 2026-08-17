package main

import (
	"context"
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
const chatEventType = store.ChatEventType

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
//
// To is who the message is directed at, and it is orthogonal to all three of
// those: a thread is where a message sits in the log, an addressee is who it is
// for. Leave it out and the message is for the room, which is what a message
// has always been here - unless the body names somebody with an @, which is the
// same field said in prose and fills it in. See mentions.go.
//
// Cite is what this message is about, and it is orthogonal to all four in the
// same way again: a parent says what came before, a citation says which message
// - or which words of which message - is being answered. See citations.go for
// why the span is what travels and the quoted text never is.
type chatSayRequest struct {
	Body string `json:"body"`
	// Attachments are ids of attachments the speaker has already written -
	// the file-token shape: bytes land once through attachment_write, and a
	// message carries the reference rather than the payload. Validated like
	// parents: through the read filter, because a card drawn for every reader
	// names rows every reader can reach, and an id that is not there and one
	// that is out of reach get the same answer.
	Attachments []string  `json:"attachments"`
	Thread      string    `json:"thread"`
	Parents     []string  `json:"parents"`
	To          string    `json:"to"`
	Cite        *chatCite `json:"cite"`
}

// chatCite is a citation as a client asks for one: the message, and the byte
// span into its body when the citation is of a part of it. Leave the offsets
// out and the citation is of the whole message.
type chatCite struct {
	Message string `json:"message"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
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

// speakerName is what the speaker is called: the handle of the principal that
// is saying something, or "" when there is no name to be had.
//
// An agent has no handle of its own. The two tables say two different things
// about a speaker - users carry the handle a person is known by, agents carry
// the runtime they are (claude|glm|opencode) and the person they act for - so
// an agent speaks under that person's handle, which is the name the room
// already knows it by, and under its runtime kind when the person it acts for
// has no handle to lend. Which of the two is talking is still meta.actor_kind's
// job; this answers what to call them.
func (s *server) speakerName(ctx context.Context, p *store.Principal) string {
	if user, err := s.db.GetUser(ctx, p.UserID); err == nil && user.Handle != "" {
		return user.Handle
	}
	if p.AgentID != "" {
		if agent, err := s.db.GetAgent(ctx, p.AgentID); err == nil {
			return agent.Kind
		}
	}
	return ""
}

// speakerMeta is the speaker, as the handlers that mint an event stamp it:
// which kind of principal said it, which person, and what they were called.
//
// The name is recorded on the write rather than joined on the read, and that is
// the point of it. It is what the speaker was called *when they spoke*, so a
// handle edited later does not silently reattribute everything that person ever
// said; and a room read stays one query, instead of growing a lookup per
// message the first time a client wants to draw a name.
//
// A name that is not there is left out rather than written empty, because every
// message said before this field existed has no name either and a reader has to
// treat the two the same - the id it fell back to then is the id it falls back
// to now. The key sits under the actor_ prefix on purpose: speakerStripped in
// api.go and metaSpeaker in the merge both work off that prefix, so a name is
// something this node stamps and never something a client or a peer says about
// itself.
func speakerMeta(p *store.Principal, kind, name string) map[string]string {
	fields := map[string]string{"actor_kind": kind, "actor_user": p.UserID}
	if name != "" {
		fields["actor_name"] = name
	}
	return fields
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
	if !s.mayReadThread(w, r, thread) {
		return false
	}
	// And a thread you CAN read is not necessarily one this write belongs in.
	// Every caller of this is a public write - a room say, POST /api/events, the
	// timeline - and a private conversation is the one thread a public write
	// must not join. See mayWritePublicThread. The private send path asks
	// mayReadThread instead, because for it the answer is the opposite.
	return s.mayWritePublicThread(w, r, thread)
}

// mayReadThread is the first half of mayWriteThread: writing into a thread is
// not a way round reading it. It is separate because the private send path needs
// exactly this and not the public rule beside it.
func (s *server) mayReadThread(w http.ResponseWriter, r *http.Request, thread string) bool {
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

// mayCarryAttachments reports whether every attachment id the writer named is
// an attachment this principal may read, and answers 400 when one is not.
//
// The same rule as parents, for the same reason: a card is drawn beside the
// message for every reader, so the ids it names must be rows the speaker could
// reach - and a card for bytes the speaker cannot read would be a message
// laundering a reference into a room that could not have made it.
func (s *server) mayCarryAttachments(w http.ResponseWriter, r *http.Request, ids []string) bool {
	for _, id := range ids {
		art, err := s.db.ReadArtifact(r.Context(), principalOf(r), id, false)
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeJSON(w, http.StatusBadRequest,
				errorBody("attachment "+id+" is not an attachment you can read; "+
					"a message carries what is in front of you or nothing"))
			return false
		case err != nil:
			serverError(w, r, err)
			return false
		case art.Type != attachmentType:
			writeJSON(w, http.StatusBadRequest,
				errorBody(id+" is not an attachment; a message carries attachments, not other rows"))
			return false
		}
	}
	return true
}

// mayNameArtifact reports whether the artifact an event says it is about is one
// the writer can read, and answers 404 when it is not.
//
// The artifact column is not decoration. The per-artifact share clause in the
// event filter shows the events about an artifact to everybody that artifact is
// shared with, and /api/artifact/{id}/history is gated on reading the artifact
// rather than on reading each event - so naming one is a claim about somebody
// else's work in the same way an edge in the DAG is, and it was taken on trust
// while the thread and the parents beside it were checked. An id is a guess
// anybody can make, so a writer who cannot read the artifact could put an entry
// into what its readers see, and it replicated from there.
//
// A missing artifact and one out of reach get the same answer, which is the
// answer a read of it would give.
func (s *server) mayNameArtifact(w http.ResponseWriter, r *http.Request, artifact string) bool {
	if artifact == "" {
		return true
	}
	p := principalOf(r)
	_, err := s.db.ReadArtifact(r.Context(), p, artifact, p.Operator)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound,
			errorBody("artifact "+artifact+" is not one you can read; "+
				"an event is about something in front of you or about nothing"))
		return false
	}
	if err != nil {
		serverError(w, r, err)
		return false
	}
	return true
}

// mayAddress reports whether to names a principal this node knows - a user or
// an agent, which are the two things an actor can be - and answers 400 when it
// does not.
//
// It is checked for the reason POST /api/assign checks its to_user, and it is
// emphatically not a permission check, because there is no permission here to
// check: an addressed message is a room message and its readers are the room's.
// What a name nothing answers to produces is the worst available failure - the
// sender believes somebody was told, the person they meant is never told, and
// no surface anywhere says the name was wrong. A typo is refused at the door
// instead, where the writer can still fix it.
//
// It tells a caller nothing they could not already read: every message in every
// room they can see carries an actor, and assignment has answered "no such
// user" since Phase 4.
//
// The merge does not ask this, deliberately, and for the reason it does not ask
// UnreadableParents either - an event replicated from a peer is legitimately
// addressed to a principal that only exists over there, and refusing it here
// would be refusing federation rather than forgery.
// It also accepts A NAME rather than only an id, which is the difference
// between a roster you can read and one you can use. Every surface draws
// people by handle - the transcript, the roster, a todo's owner - and --to
// took only the ULID underneath it, so the name the console showed you was
// refused by the door: "no principal called claude-host here", about a
// principal called claude-host. Reported by flowy-claude, who could see the
// name and could not address it.
//
// The name goes through PrincipalsNamed, the SAME resolver @-mentions use,
// rather than a second lookup written beside it. One resolver means @alice
// and --to alice can never disagree about who alice is, which is exactly the
// disagreement a second implementation eventually produces.
func (s *server) resolveAddressee(w http.ResponseWriter, r *http.Request, to string) (string, bool) {
	if to == "" {
		return "", true
	}
	ctx := r.Context()
	_, err := s.db.GetUser(ctx, to)
	if err == nil {
		return to, true
	}
	if !errors.Is(err, store.ErrNotFound) {
		serverError(w, r, err)
		return "", false
	}
	if _, err = s.db.GetAgent(ctx, to); err == nil {
		return to, true
	}
	if !errors.Is(err, store.ErrNotFound) {
		serverError(w, r, err)
		return "", false
	}

	// Not an id this node holds. Try it as a name.
	named, err := s.db.PrincipalsNamed(ctx, []string{to})
	if err != nil {
		serverError(w, r, err)
		return "", false
	}
	if id, found := named[strings.ToLower(to)]; found {
		if id == "" {
			// Two principals answer to it. Refusing is the only honest
			// answer: picking one delivers to somebody the sender did not
			// mean, and posting unaddressed tells them it was delivered.
			writeJSON(w, http.StatusBadRequest,
				errorBody("more than one principal is called "+to+" here, so it is "+
					"not an address: name the id you mean"))
			return "", false
		}
		// Stored as the id, always. A handle can be changed later and a
		// message addressed to a string would silently retarget with it -
		// the addressee is a principal, not a spelling.
		return id, true
	}

	writeJSON(w, http.StatusBadRequest,
		errorBody("no principal called "+to+" here: an addressee is a user or an agent, "+
			"by id or by handle, and a message with none is addressed to the room"))
	return "", false
}

// mayCite reads the citation a client asked for, and answers the request
// itself when it is not one this writer may make.
//
// The message is checked through the read filter, exactly as a parent is and
// for the same reason: an id is a guess anybody can make, and a citation is a
// claim about somebody else's words - the console draws it under their name, in
// their colour. So citing something out of reach and citing something that is
// not there get the same answer, which is the answer a read of it would give.
//
// The span is checked against the body it is a span of, here rather than on the
// way out. The node never stores the quoted text, so a span that cannot derive
// one is a citation that will render as broken on every read forever, on a row
// that cannot be edited - and the only moment anybody can still fix it is this
// one.
func (s *server) mayCite(w http.ResponseWriter, r *http.Request, req *chatCite) (store.CiteRef, bool) {
	if req == nil {
		return store.CiteRef{}, true
	}
	ref := store.CiteRef{
		Message: strings.TrimSpace(req.Message),
		Start:   req.Start,
		End:     req.End,
	}
	if ref.Message == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody("a citation names the message it is of; leave cite out and this message cites none"))
		return ref, false
	}
	source, err := s.db.ReadEvent(r.Context(), principalOf(r), ref.Message)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound,
			errorBody("message "+ref.Message+" is not one you can read; "+
				"a citation quotes a message in front of you, or nothing"))
		return ref, false
	}
	if err != nil {
		serverError(w, r, err)
		return ref, false
	}
	if !ref.Whole() {
		if fault := store.CiteSpanFault(source.Body, ref.Start, ref.End); fault != "" {
			writeJSON(w, http.StatusBadRequest, errorBody(fault))
			return ref, false
		}
	}
	return ref, true
}

// handleChatSay appends a message to a room.
//
// POST /api/chat/{room}/say  {body, thread?, parents?, to?, cite?}
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
	req.To = strings.TrimSpace(req.To)
	to, ok := s.resolveAddressee(w, r, req.To)
	if !ok {
		return
	}
	req.To = to
	if !s.mayNameParents(w, r, req.Parents) {
		return
	}
	if !s.mayCarryAttachments(w, r, req.Attachments) {
		return
	}
	cite, ok := s.mayCite(w, r, req.Cite)
	if !ok {
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
		//
		// A parent that is not readable and a store that could not answer are
		// not the same thing, and this used to treat them as one: any error at
		// all meant "start a fresh thread". So a dropped connection or a
		// statement timeout silently forked the conversation - a new thread id,
		// the DAG edge still pointing at the parent, the reply sitting where
		// nobody looking at the thread will find it - and nothing said the
		// store had been unreachable. ThreadHidden below has always told the
		// two apart; this asks the same question.
		if len(req.Parents) > 0 {
			parent, err := s.db.ReadEvent(r.Context(), p, req.Parents[0])
			switch {
			case errors.Is(err, store.ErrNotFound):
				// Deliberate: a fresh thread, and no 403 for a thread the
				// caller never named.
			case err != nil:
				serverError(w, r, err)
				return
			case parent.Thread != "":
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
	// A message into a thread that is already part of a trace joins it. On the
	// far side of a handoff this is what makes "the assignee replied" a span in
	// the story of the handoff rather than a request nothing connects to.
	s.adoptThreadTrace(r, req.Thread)

	// The @names in the body, resolved here rather than left to every reader.
	// An addressee written into the sentence is the same fact as one written
	// into the `to` field, so it lands in the same column - see mentions.go.
	//
	// An explicit `to` still wins. It is a field somebody filled in
	// deliberately, and a message that says "@alice, ask bob" with --to bob is
	// a writer being specific about which of the two names is the addressing.
	found, err := resolveMentions(req.Body, s.principalsNamed(r.Context()))
	if err != nil {
		serverError(w, r, err)
		return
	}
	if req.To == "" {
		req.To = mentionAddressee(found)
	}

	actor, kind := chatActor(p)
	fields := speakerMeta(p, kind, s.speakerName(r.Context(), p))
	if len(found) > 0 {
		fields[store.MentionsMetaKey] = mentionMeta(found)
	}
	// The citation rides in meta beside them, and it is inside the signature
	// because meta is: a relay that could rewrite which message a reply quotes,
	// or which half of it, would be a relay choosing what somebody is recorded
	// as having answered.
	if cite.Message != "" {
		fields[store.CiteMetaKey] = store.EncodeCiteRef(cite)
	}
	if len(req.Attachments) > 0 {
		fields[store.AttachmentsMetaKey] = strings.Join(req.Attachments, " ")
	}
	meta, err := json.Marshal(fields)
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
		Type:      chatEventType,
		Project:   project,
		Room:      room,
		Thread:    req.Thread,
		Parents:   req.Parents,
		Actor:     actor,
		Addressee: req.To,
		Body:      req.Body,
		Meta:      withTrace(json.RawMessage(meta), traceIDOf(r)),
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

	var list []*store.Event
	err = pollUntil(r.Context(), waitWindowOf(q.Get("window")), func() (bool, error) {
		var err error
		list, err = s.readRoom(r, room, thread, cursor, intParam(q.Get("limit")))
		return len(list) > 0, err
	})
	switch {
	case errors.Is(err, errClientGone):
		return
	case err != nil:
		serverError(w, r, err)
		return
	}
	writeChatEvents(w, room, cursor, list)
}

// pollUntil is the watcher loop, and there is one of it. It calls look until
// look says there is something to answer with or the window runs out, and it is
// shared by the room poll above and the inbox poll below so that the two agree
// on the tick, on the finite window, and on what a cancelled request means.
//
// A second implementation of this is how two long polls end up with two
// different ideas of how long "blocks" is, and how one of them ends up hanging
// past the server's write timeout. The contract is the room's, unchanged: a
// poll always returns.
func pollUntil(ctx context.Context, window time.Duration, look func() (bool, error)) error {
	deadline := time.Now().Add(window)
	for {
		ready, err := look()
		if err != nil {
			return err
		}
		if ready || !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errClientGone
		case <-time.After(waitTick):
		}
	}
}

// errClientGone says the request was cancelled under the poll - the client hung
// up, or the server is shutting down. There is nothing to write, and writing
// anyway would only log a broken pipe.
var errClientGone = errors.New("the client went away")

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

	all := scopeAll(r, p)
	list, err := s.db.ListEvents(r.Context(), p, store.EventQuery{
		Type:      chatEventType,
		Room:      q.Get("room"),
		Since:     since,
		NotActors: []string{p.UserID, p.AgentID},
		ScopeAll:  all,
		Limit:     intParam(q.Get("limit")),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	// The same resolution the room read does, because a second door onto the
	// same messages is where a filter gets forgotten.
	if err := s.db.Citations(r.Context(), p, list, all); err != nil {
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
	all := scopeAll(r, p)
	list, err := s.db.ListEvents(r.Context(), p, store.EventQuery{
		Type:     chatEventType,
		Room:     room,
		Thread:   thread,
		Since:    since,
		ScopeAll: all,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	// What each reply is answering, resolved for THIS reader: the row records a
	// pointer and a span, and the words come off the message it points at
	// through the same filter this read used. A reader who cannot reach that
	// message is told so and handed none of it - see store.Citations.
	if err := s.db.Citations(r.Context(), p, list, all); err != nil {
		return nil, err
	}
	return list, nil
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
