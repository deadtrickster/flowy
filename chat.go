package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
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
	return speakerNameOf(ctx, s.db, p)
}

// speakerNameOf is that answer for a caller that has a database and no server:
// the MCP surface is a process of its own and still has to name a speaker the
// same way, and a second copy of this rule would be the second place a handle
// gets resolved differently.
func speakerNameOf(ctx context.Context, db *store.DB, p *store.Principal) string {
	if user, err := db.GetUser(ctx, p.UserID); err == nil && user.Handle != "" {
		return user.Handle
	}
	if p.AgentID != "" {
		if agent, err := db.GetAgent(ctx, p.AgentID); err == nil {
			return agent.Kind
		}
	}
	return ""
}

// speakerNameOfEvent is what the speaker of a message that HAS ALREADY BEEN
// SAID was called, for a reader looking at the message rather than holding the
// token that wrote it.
//
// The stamped name comes first, and it is the one that is true. speakerMeta
// records what somebody was called AT THE MOMENT THEY SPOKE, on purpose - a
// handle edited later must not silently reattribute everything that person ever
// said - so a reader that went and resolved the actor id afresh would undo
// exactly the thing that key exists to do.
//
// The fallback is speakerNameOf's rule with an id in place of a principal, for
// the messages written before the node stamped a name: the person's handle when
// the actor is a person or an agent acting for one, and the agent's runtime kind
// when there is no handle to lend. It answers "" when it can resolve neither -
// a speaker this node can only name by id - and an id is not a handle, so
// nothing here hands one back as though it were.
func speakerNameOfEvent(ctx context.Context, db *store.DB, e *store.Event) string {
	if e == nil {
		return ""
	}
	// Into raw messages rather than map[string]string, for activityItem's
	// reason: meta is not all strings - a worklog entry keeps its refs there as
	// a list - and one non-string value would fail the whole unmarshal and drop
	// the speaker off every event that had one.
	var fields map[string]json.RawMessage
	if len(e.Meta) > 0 {
		_ = json.Unmarshal(e.Meta, &fields)
	}
	if name := metaString(fields, "actor_name"); name != "" {
		return name
	}
	// actor_user first, because an agent speaks under the handle of the person
	// it acts for and the actor itself is the agent. A message said by a person
	// carries the same id in both places, so the order costs nothing there.
	for _, id := range []string{metaString(fields, "actor_user"), e.Actor} {
		if id == "" {
			continue
		}
		if user, err := db.GetUser(ctx, id); err == nil && user.Handle != "" {
			return user.Handle
		}
	}
	if e.Actor != "" {
		if agent, err := db.GetAgent(ctx, e.Actor); err == nil {
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
	room, err := roomNamed(r.PathValue("room"))
	return room, err == nil
}

// roomNamed is that check for a caller whose room did not arrive in a path - an
// MCP argument, say. The rule is the URL's either way: a tool that accepted
// "a/b" would be writing rooms nothing else can address.
func roomNamed(room string) (string, error) {
	room = strings.TrimSpace(room)
	if room == "" || strings.Contains(room, "/") {
		return "", refuseChat(http.StatusBadRequest, "room must be one non-empty path segment")
	}
	return room, nil
}

// chatFault is a refusal about the REQUEST rather than about the store: a room
// that is not one segment, an id out of reach, a thread that is not this
// writer's to join. It carries the status the HTTP door answers with, so the
// rule is written once and each door says it in its own idiom - a 4xx with a
// body over HTTP, a tool refusal over MCP, where a 403 becomes the protocol
// error mcp.go's `forbidden` describes.
//
// Anything else these paths return is the store failing, which is a 500 at one
// door and an error at the other, and which must never be reworded into a
// refusal: "the database was unreachable" and "you may not do that" are the two
// answers a caller has to be able to tell apart.
type chatFault struct {
	status int
	why    string
}

func (f chatFault) Error() string { return f.why }

// refuseChat builds one.
func refuseChat(status int, why string) error { return chatFault{status: status, why: why} }

// allowed answers a rule that refused, and reports whether it did not. It is
// what turns the shared rules below back into the HTTP behaviour every handler
// already had: the fault's own status and wording, or a 500 that says nothing
// about the query.
func (s *server) allowed(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return true
	}
	var fault chatFault
	if errors.As(err, &fault) {
		writeJSON(w, fault.status, errorBody(fault.why))
		return false
	}
	serverError(w, r, err)
	return false
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
	return s.allowed(w, r, mayWriteThreadOf(r.Context(), s.db, principalOf(r), thread))
}

// mayWriteThreadOf is that rule for a caller that has a database and no request,
// which is what the MCP surface is - the same split, for the same reason, as
// speakerName and speakerNameOf above. A second copy of this would be the second
// place a thread decides who may join it, and the two would drift.
func mayWriteThreadOf(ctx context.Context, db *store.DB, p *store.Principal, thread string) error {
	if err := mayReadThreadOf(ctx, db, p, thread); err != nil {
		return err
	}
	// And a thread you CAN read is not necessarily one this write belongs in.
	// Every caller of this is a public write - a room say, POST /api/events, the
	// timeline - and a private conversation is the one thread a public write
	// must not join. See mayWritePublicThread. The private send path asks
	// mayReadThread instead, because for it the answer is the opposite.
	return mayWritePublicThreadOf(ctx, db, thread)
}

// mayReadThread is the first half of mayWriteThread: writing into a thread is
// not a way round reading it. It is separate because the private send path needs
// exactly this and not the public rule beside it.
func (s *server) mayReadThread(w http.ResponseWriter, r *http.Request, thread string) bool {
	return s.allowed(w, r, mayReadThreadOf(r.Context(), s.db, principalOf(r), thread))
}

func mayReadThreadOf(ctx context.Context, db *store.DB, p *store.Principal, thread string) error {
	hidden, err := db.ThreadHidden(ctx, p, thread)
	if err != nil {
		return err
	}
	if hidden {
		return refuseChat(http.StatusForbidden,
			"thread "+thread+" is a conversation you cannot read; "+
				"leave thread out and this starts one of its own")
	}
	return nil
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
	return s.allowed(w, r, mayNameParentsOf(r.Context(), s.db, principalOf(r), parents))
}

func mayNameParentsOf(ctx context.Context, db *store.DB, p *store.Principal, parents []string) error {
	unreadable, err := db.UnreadableParents(ctx, p, parents)
	if err != nil {
		return err
	}
	if len(unreadable) > 0 {
		return refuseChat(http.StatusBadRequest,
			"parent "+unreadable[0]+" is not an event you can read; "+
				"an event descends from what is in front of you or from nothing")
	}
	return nil
}

// mayCarryAttachments reports whether every attachment id the writer named is
// an attachment this principal may read, and answers 400 when one is not.
//
// The same rule as parents, for the same reason: a card is drawn beside the
// message for every reader, so the ids it names must be rows the speaker could
// reach - and a card for bytes the speaker cannot read would be a message
// laundering a reference into a room that could not have made it.
func (s *server) mayCarryAttachments(w http.ResponseWriter, r *http.Request, ids []string) bool {
	return s.allowed(w, r, mayCarryAttachmentsOf(r.Context(), s.db, principalOf(r), ids))
}

func mayCarryAttachmentsOf(ctx context.Context, db *store.DB, p *store.Principal, ids []string) error {
	for _, id := range ids {
		art, err := db.ReadArtifact(ctx, p, id, false)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return refuseChat(http.StatusBadRequest,
				"attachment "+id+" is not an attachment you can read; "+
					"a message carries what is in front of you or nothing")
		case err != nil:
			return err
		case art.Type != attachmentType:
			return refuseChat(http.StatusBadRequest,
				id+" is not an attachment; a message carries attachments, not other rows")
		}
	}
	return nil
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
	id, err := resolveAddresseeOf(r.Context(), s.db, to)
	return id, s.allowed(w, r, err)
}

// resolveAddresseeOf is that resolution for a caller with a database and no
// request. It takes no principal because there is no permission in it: an
// addressed message is a room message, and this only decides whether the name
// means anybody at all.
func resolveAddresseeOf(ctx context.Context, db *store.DB, to string) (string, error) {
	if to == "" {
		return "", nil
	}
	_, err := db.GetUser(ctx, to)
	if err == nil {
		return to, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if _, err = db.GetAgent(ctx, to); err == nil {
		return to, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}

	// Not an id this node holds. Try it as a name.
	named, err := db.PrincipalsNamed(ctx, []string{to})
	if err != nil {
		return "", err
	}
	if id, found := named[strings.ToLower(to)]; found {
		if id == "" {
			// Two principals answer to it. Refusing is the only honest
			// answer: picking one delivers to somebody the sender did not
			// mean, and posting unaddressed tells them it was delivered.
			return "", refuseChat(http.StatusBadRequest,
				"more than one principal is called "+to+" here, so it is "+
					"not an address: name the id you mean")
		}
		// Stored as the id, always. A handle can be changed later and a
		// message addressed to a string would silently retarget with it -
		// the addressee is a principal, not a spelling.
		return id, nil
	}

	return "", refuseChat(http.StatusBadRequest,
		"no principal called "+to+" here: an addressee is a user or an agent, "+
			"by id or by handle, and a message with none is addressed to the room")
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
	ref, err := mayCiteOf(r.Context(), s.db, principalOf(r), req)
	return ref, s.allowed(w, r, err)
}

func mayCiteOf(
	ctx context.Context, db *store.DB, p *store.Principal, req *chatCite,
) (store.CiteRef, error) {
	if req == nil {
		return store.CiteRef{}, nil
	}
	ref := store.CiteRef{
		Message: strings.TrimSpace(req.Message),
		Start:   req.Start,
		End:     req.End,
	}
	if ref.Message == "" {
		return ref, refuseChat(http.StatusBadRequest,
			"a citation names the message it is of; leave cite out and this message cites none")
	}
	source, err := db.ReadEvent(ctx, p, ref.Message)
	if errors.Is(err, store.ErrNotFound) {
		return ref, refuseChat(http.StatusNotFound,
			"message "+ref.Message+" is not one you can read; "+
				"a citation quotes a message in front of you, or nothing")
	}
	if err != nil {
		return ref, err
	}
	if !ref.Whole() {
		if bad := store.CiteSpanFault(source.Body, ref.Start, ref.End); bad != "" {
			return ref, refuseChat(http.StatusBadRequest, bad)
		}
	}
	return ref, nil
}

// handleChatSay appends a message to a room.
//
// POST /api/chat/{room}/say  {body, thread?, parents?, to?, cite?}
//
// The door and nothing else: it reads the request, hands it to sayInRoom, and
// turns whatever comes back into a status. Every rule about what a message may
// name and who it may be for is down there, where the MCP surface reaches it too
// - see the header of sayInRoom for why that matters.
func (s *server) handleChatSay(w http.ResponseWriter, r *http.Request) {
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

	said, err := sayInRoom(r.Context(), s.db, principalOf(r), room, req)
	if err != nil {
		// Which writes the refusal the say path made, or a 500 when it was the
		// store that failed.
		s.allowed(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, said)
}

// sayInRoom is what saying something in a room IS: the checks, the speaker's
// name, the thread it lands in, the row, and the grant a citation makes.
//
// It is a function of a context and a database rather than of a request because
// there are two doors onto it and there must never be two implementations of it.
// POST /api/chat/{room}/say is one; chat_say on the MCP surface is the other,
// and until that tool existed an agent whose only door is MCP could read a room
// and not answer it. A second write path would be a second answer to every
// question here - which threads are closed, which names address somebody, what a
// speaker is called - and the two would drift the first time one of them was
// fixed.
//
// The refusals come back as chatFault, so each door can say them in its own
// idiom without deciding them.
func sayInRoom(
	ctx context.Context, db *store.DB, p *store.Principal, room string, req chatSayRequest,
) (*store.Event, error) {
	room, err := roomNamed(room)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Body) == "" {
		return nil, refuseChat(http.StatusBadRequest, "body is required")
	}
	if req.Parents == nil {
		req.Parents = []string{}
	}
	req.To = strings.TrimSpace(req.To)
	if req.To, err = resolveAddresseeOf(ctx, db, req.To); err != nil {
		return nil, err
	}
	if err := mayNameParentsOf(ctx, db, p, req.Parents); err != nil {
		return nil, err
	}
	if err := mayCarryAttachmentsOf(ctx, db, p, req.Attachments); err != nil {
		return nil, err
	}
	cite, err := mayCiteOf(ctx, db, p, req.Cite)
	if err != nil {
		return nil, err
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
			parent, err := db.ReadEvent(ctx, p, req.Parents[0])
			switch {
			case errors.Is(err, store.ErrNotFound):
				// Deliberate: a fresh thread, and no 403 for a thread the
				// caller never named.
			case err != nil:
				return nil, err
			case parent.Thread != "":
				hidden, err := db.ThreadHidden(ctx, p, parent.Thread)
				if err != nil {
					return nil, err
				}
				if !hidden {
					req.Thread = parent.Thread
				}
			}
		}
		if req.Thread == "" {
			req.Thread = ulid.NewString()
		}
	} else if err := mayWriteThreadOf(ctx, db, p, req.Thread); err != nil {
		return nil, err
	}
	// A message into a thread that is already part of a trace joins it. On the
	// far side of a handoff this is what makes "the assignee replied" a span in
	// the story of the handoff rather than a request nothing connects to.
	adoptThreadTraceOf(ctx, db, p, req.Thread)

	// The @names in the body, resolved here rather than left to every reader.
	// An addressee written into the sentence is the same fact as one written
	// into the `to` field, so it lands in the same column - see mentions.go.
	//
	// An explicit `to` still wins. It is a field somebody filled in
	// deliberately, and a message that says "@alice, ask bob" with --to bob is
	// a writer being specific about which of the two names is the addressing.
	found, err := resolveMentions(req.Body, principalsNamedBy(ctx, db))
	if err != nil {
		return nil, err
	}
	if req.To == "" {
		req.To = mentionAddressee(found)
	}

	// The speaker's name, stamped the way every other message stamps it. A
	// message written without it renders as anonymous forever - actor_name is
	// absent, the console falls back to an id, and nothing says who spoke - so
	// this is the one line a second write path would be most likely to leave out
	// and the room would be least likely to forgive.
	actor, kind := chatActor(p)
	fields := speakerMeta(p, kind, speakerNameOf(ctx, db, p))
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
		return nil, err
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
		Meta:      withTrace(json.RawMessage(meta), otel.TraceID(ctx)),
	}
	if err := db.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	// AND THE GRANT THE CITATION MAKES, if it makes one - see citegrant.go for
	// which citations do. A citation from somebody who may only READ the source
	// is a pointer and grants nothing; one from somebody who may decide about it
	// is a grant, recorded with the granter named.
	//
	// After the append, not before: the grant exists because this message does,
	// and a capability written for a message the store then refused would be a
	// share with nothing pointing at it. The other order fails the other way -
	// a citation the recipient cannot resolve - and that failure is visible and
	// harmless where a silent over-share is neither.
	//
	// A grant that cannot be written does not fail the message. It has already
	// been said, and answering 500 to a message that is in the log would have
	// the caller say it again.
	if cite.Message != "" && req.To != "" {
		if src, err := db.ReadEvent(ctx, p, cite.Message); err == nil && src.Artifact != "" {
			granted, err := db.GrantCitedArtifact(ctx, p, src.Artifact, req.To)
			switch {
			case err != nil:
				log.Printf("citations: the grant %s makes on %s was not recorded: %v",
					e.ID, src.Artifact, err)
			case granted:
				// Said out loud, because a share is the kind of thing an
				// operator reads a log to find afterwards.
				log.Printf("citations: %s cited %s to %s, which grants read on it",
					p.UserID, src.Artifact, req.To)
			}
		}
	}
	return e, nil
}

// handleChatRead reads a room in log order, filtered to what the principal may
// see. since is the seq_hlc of the last message the caller has, and is strictly
// exclusive, so handing back the cursor from the previous read never repeats a
// message and never skips one.
//
// order=recent reads the OTHER END of the same log: the newest `limit` messages
// rather than the oldest above a cursor, still in log order, and `before` pages
// backwards from there. That is how a room opens on a bounded window and fetches
// its history as somebody scrolls up, instead of loading everything ever said in
// it - reported by the operator as "on reload the whole chat history loads".
//
// It is the same endpoint and the same filter rather than a second door onto the
// messages, because a second door is where a permission filter gets forgotten:
// both orders go through readRoom, which resolves citations for THIS reader, so
// a page fetched an hour after the room opened still renders its quotes.
//
// GET /api/chat/{room}?since=&thread=&limit=
// GET /api/chat/{room}?order=recent&before=&thread=&limit=
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
	before, err := cursorParam(q.Get("before"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	switch q.Get("order") {
	case "", "log":
		if before > 0 {
			writeJSON(w, http.StatusBadRequest,
				errorBody("before pages backwards and only order=recent reads that way"))
			return
		}
		list, err := s.readRoom(r, room, q.Get("thread"), since, intParam(q.Get("limit")))
		if err != nil {
			serverError(w, r, err)
			return
		}
		writeChatEvents(w, room, since, list)
	case "recent":
		list, err := s.readRoomBefore(r, room, q.Get("thread"), before, intParam(q.Get("limit")))
		if err != nil {
			serverError(w, r, err)
			return
		}
		writeChatWindow(w, room, before, list)
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("order must be log or recent"))
	}
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
	// The inbox read shows other people's messages, which is precisely where a
	// disowned line must not read like an ordinary one.
	if err := s.db.FillDisowned(r.Context(), p, nil, list); err != nil {
		log.Printf("disowned: could not resolve repudiations for an inbox page: %v", err)
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
	// AND WHOSE WORD EACH LINE IS NOT - see the room read. Resolved here rather
	// than at the two callers, because this is the function both of them get
	// their messages from and a mark added at one door only is the shape this
	// fleet has spent the night removing.
	if err := s.db.FillDisowned(r.Context(), p, nil, list); err != nil {
		log.Printf("disowned: could not resolve repudiations for a room page: %v", err)
	}
	return list, nil
}

// readRoomBefore is readRoom off the other end of the log: the newest messages
// below `before`, or simply the newest ones when it is zero. Same narrowing,
// same permission filter, same citation resolution - see store.EventsBefore for
// why its old end is a complete reading and `before` is therefore exact.
func (s *server) readRoomBefore(r *http.Request, room, thread string, before int64, limit int) ([]*store.Event, error) {
	p := principalOf(r)
	return roomBefore(r.Context(), s.db, p, room, thread, before, limit, scopeAll(r, p))
}

// roomBefore is that read for a caller with a database and no request: the MCP
// surface's chat_read is the other one, and it opens on the same end of the log
// the console opens on. A room read that started at the beginning of a busy log
// would hand an agent the oldest hundred messages of a conversation it is trying
// to catch up with, which is the one page nobody wants.
func roomBefore(
	ctx context.Context, db *store.DB, p *store.Principal,
	room, thread string, before int64, limit int, all bool,
) ([]*store.Event, error) {
	list, err := db.EventsBefore(ctx, p, store.EventQuery{
		Type:     chatEventType,
		Room:     room,
		Thread:   thread,
		Before:   before,
		ScopeAll: all,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	if err := db.Citations(ctx, p, list, all); err != nil {
		return nil, err
	}
	// The backwards read is the same page seen from the other end, so it
	// carries the same mark - see readRoom.
	if err := db.FillDisowned(ctx, p, nil, list); err != nil {
		log.Printf("disowned: could not resolve repudiations for a backwards page: %v", err)
	}
	return list, nil
}

// writeChatWindow answers a backwards read with BOTH cursors, because a window
// taken out of the middle of a log has two ends and a client walks them in
// opposite directions:
//
//   - `cursor` is forwards, for /wait. It is only the end of the log when the
//     caller asked for the newest window - `before` unset - which is what a room
//     opening does, and it is exactly the reading /wait must continue from so
//     the first poll neither replays the window nor steps over a message said
//     between the two requests. On a page taken from further back it echoes the
//     `before` it came in with, the way the activity timeline does with a
//     descending read: that page says nothing new about the front of the log.
//   - `before` is backwards, for the next older page: the reading of the OLDEST
//     message here, strictly exclusive. Zero when nothing came back, which is
//     the beginning of the room.
//
// A client can tell whether anything older exists without another request: the
// window filled its limit or it did not.
func writeChatWindow(w http.ResponseWriter, room string, before int64, list []*store.Event) {
	cursor, older := chatWindowEnds(before, list)
	writeJSON(w, http.StatusOK, map[string]any{
		"room":   room,
		"events": list,
		"since":  int64(0),
		"cursor": cursor,
		"before": older,
	})
}

// chatWindowEnds is the arithmetic behind those two cursors, so the tool surface
// pages a room exactly as the console does rather than working it out again. See
// writeChatWindow above for what each end is for.
func chatWindowEnds(before int64, list []*store.Event) (cursor, older int64) {
	cursor = before
	if n := len(list); n > 0 {
		if before == 0 {
			cursor = list[n-1].SeqHLC
		}
		older = list[0].SeqHLC
	}
	return cursor, older
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
