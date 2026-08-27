package flowy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// Direct messages.
//
// A DM is a chat event with no project, no room, and an addressee - see
// store.IsDirectMessage - and it is read by its author and the principal it
// names, by one clause in EventFilterSQL's projectless branch. Everything below
// is the write side of that, and it is deliberately the same shape as
// handleChatSay: the same event type, the same speaker stamping, the same thread
// and parent gates. What it is not is a second kind of message.
//
// Two rules here have no counterpart in a room, because a room has no party set
// and this does:
//
//   - a DM descends from a DM. See store.PublicParents.
//   - a DM joins a thread that is already all-DM, is named by no task, and
//     already holds the principal it is addressed to. See store.PrivateThread.
//
// Both are about a REPLY not widening what the first message promised. The read
// filter cannot see that: each row it judges names one addressee and looks
// perfectly private on its own, and the leak would be the thread as a whole
// having three people in it while each message said two.
//
// PRIVACY IS ON THE EVENT AND NOT ON A ROOM. There is no DM room, no member
// table and no per-room scope: the room column is a label, nothing in the
// permission filter reads it, and the first thing here that decided a read from
// a room would be the first per-room scope this fabric has ever had.

// dmSayRequest is the body of a direct message. The addressee is the path
// segment rather than a field: who a DM is for is the whole of what makes it
// one, and a body field that could be left out is a body field that will be.
type dmSayRequest struct {
	Body    string   `json:"body"`
	Thread  string   `json:"thread"`
	Parents []string `json:"parents"`
}

// addresseeOf reads and checks the {to} path segment. Same rule as a room: one
// path segment, because a client has to be able to put it back into a URL.
func addresseeOf(r *http.Request) (string, bool) {
	to := strings.TrimSpace(r.PathValue("to"))
	if to == "" || strings.Contains(to, "/") {
		return "", false
	}
	return to, true
}

// handleDMSay sends a direct message.
//
// POST /api/dm/{to}  {body, thread?, parents?}
func (s *server) handleDMSay(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	to, ok := addresseeOf(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			errorBody("the addressee is one non-empty path segment: POST /api/dm/{user-or-agent}"))
		return
	}

	var req dmSayRequest
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
	// A name nothing answers to, refused at the door, for the reason a room
	// message refuses one - except that here it is worse. A room message to a
	// typo is still said in the room and somebody reads it; a DM to a typo is
	// read by nobody at all, for ever, and the sender is told it was sent.
	to, ok = s.resolveAddressee(w, r, to)
	if !ok {
		return
	}
	if !s.mayNameParents(w, r, req.Parents) {
		return
	}
	if !s.mayNamePrivateParents(w, r, req.Parents) {
		return
	}

	thread, ok := s.dmThread(w, r, &req, to)
	if !ok {
		return
	}
	// A DM into a thread that is already part of a trace joins it, as a room
	// message does. The trace is correlation and carries no reach of its own -
	// what a reader may see is still this event's own row.
	s.adoptThreadTrace(r, thread)

	actor, kind := chatActor(p)
	meta, err := json.Marshal(speakerMeta(p, kind, s.speakerName(r.Context(), p)))
	if err != nil {
		serverError(w, r, err)
		return
	}

	// No project and no room, and both are the point. The project is what every
	// project read is decided on, so a DM that carried one would be a message the
	// sender's whole project could read; the room is what a room read narrows by,
	// so a DM that carried one would turn up in a room nobody sent it to. The
	// signature covers both - see sign.CanonicalEvent - so neither can be put
	// back on in flight.
	e := &store.Event{
		Type:      chatEventType,
		Project:   nil,
		Room:      "",
		Thread:    thread,
		Parents:   req.Parents,
		Actor:     actor,
		Addressee: to,
		Body:      req.Body,
		Meta:      withTrace(json.RawMessage(meta), traceIDOf(r)),
	}
	if err := s.db.AppendEvent(r.Context(), e); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// dmThread decides which thread a direct message lands in, and answers the
// request itself when the answer is a refusal.
//
// Three ways in, and the middle one is the one that matters:
//
//   - no thread and no parents: a fresh conversation, named after nothing.
//   - a thread the caller named, or one inherited from the message it answers:
//     it has to be a private conversation the caller is in, and the addressee
//     has to be in it already.
//   - a thread INHERITED from a parent that the caller cannot read the whole
//     of: a fresh thread, not a 403, because the caller never named a thread -
//     the rule handleChatSay keeps, and for its reason. It is reachable: a
//     conversation whose parties are a person, another person and that person's
//     agent can hold a message between the last two that the first cannot read,
//     and answering it should open a conversation rather than produce a refusal
//     about a thread nobody typed.
func (s *server) dmThread(w http.ResponseWriter, r *http.Request, req *dmSayRequest, to string) (string, bool) {
	p := principalOf(r)
	thread := strings.TrimSpace(req.Thread)
	// Whether the caller typed the thread or the parent supplied it. The two
	// differ only in what an unreadable thread means, and that is exactly the
	// distinction handleChatSay draws: a 403 about a thread the caller never
	// mentioned would say that the id is one worth guessing.
	named := thread != ""

	if thread == "" && len(req.Parents) > 0 {
		// Every parent is a DM this principal can read by the time this runs,
		// so the thread it inherits is a private one - but it is still put
		// through the gates below rather than trusted, because "the parent is
		// readable" and "the thread is joinable" are not the same question and
		// telling them apart is what stops a reply landing in a conversation
		// the writer is only half in.
		parent, err := s.db.ReadEvent(r.Context(), p, req.Parents[0])
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Deliberate: a fresh thread. mayNameParents has already refused a
			// parent that is out of reach, so this is a row that vanished
			// under the request.
		case err != nil:
			serverError(w, r, err)
			return "", false
		default:
			thread = parent.Thread
		}
	}
	if thread == "" {
		return ulid.NewString(), true
	}

	// Can the caller read here at all - the first half of what a room message
	// asks, and the one that keeps a non-party out of somebody else's
	// conversation entirely. A DM thread holds nothing but DMs between its
	// parties, so a principal that is not one of them cannot read a single row
	// of it.
	//
	// Only the first half: the rest of mayWriteThread refuses a PRIVATE thread,
	// which is the thread this path exists to write into.
	if named {
		if !s.mayReadThread(w, r, thread) {
			return "", false
		}
	} else {
		hidden, err := s.db.ThreadHidden(r.Context(), p, thread)
		if err != nil {
			serverError(w, r, err)
			return "", false
		}
		if hidden {
			return ulid.NewString(), true
		}
	}
	pt, err := s.db.ReadPrivateThread(r.Context(), thread)
	if err != nil {
		serverError(w, r, err)
		return "", false
	}
	if !pt.Exists {
		// A thread with nothing in it is nobody's yet, and naming one is how a
		// client that minted its own id starts a conversation.
		return thread, true
	}
	if !pt.Private || pt.NamedByTask {
		writeJSON(w, http.StatusBadRequest,
			errorBody("thread "+thread+" is not a private conversation - it holds messages "+
				"other people read, and a private reply into it would be read by nobody but you; "+
				"leave thread out and this starts one of its own"))
		return "", false
	}
	// And the two ends of the party set. The writer has to be in it - which the
	// readability test above has already made true, and which is asserted here
	// anyway because the two answers must not drift apart. The addressee
	// has to be in it too, and that is the clause that stops a reply widening
	// the conversation: the set is fixed by the first message, and every message
	// after it names somebody who was already there.
	if !pt.HasParty(p.UserID) && !pt.HasParty(p.AgentID) {
		writeJSON(w, http.StatusForbidden,
			errorBody("thread "+thread+" is a private conversation you are not part of"))
		return "", false
	}
	if !pt.HasParty(to) {
		writeJSON(w, http.StatusBadRequest,
			errorBody("thread "+thread+" is a private conversation and "+to+" is not in it; "+
				"a reply cannot widen one - start a new conversation with them instead"))
		return "", false
	}
	return thread, true
}

// mayWritePublicThread reports whether a message that other people will read may
// join this thread, and answers 400 when it may not.
//
// It is the mirror of the party rules above, and it is the trap it closes rather
// than a leak. A party to a private conversation can write into their own thread
// through any of the public doors - a room say, POST /api/events, the timeline's
// message box - and what they would write is a row with their home project on
// it. That row does not make anybody else's messages readable, because the
// filter judges every row on its own. What it does is put words the writer
// believed were private in front of their whole project, in a thread whose every
// other line says private, from a box that gave no sign of the difference.
//
// So the refusal is here, at the doors, rather than in the filter: it is not a
// question about who may read a row, it is a question about which conversation a
// row is being added to. Every public write path asks it.
func (s *server) mayWritePublicThread(w http.ResponseWriter, r *http.Request, thread string) bool {
	return s.allowed(w, r, mayWritePublicThreadOf(r.Context(), s.db, thread))
}

// mayWritePublicThreadOf is that rule for a caller with a database and no
// request - the MCP say path asks it through mayWriteThreadOf, because a tool
// that could drop a room message into a private conversation would be the same
// leak through a different door.
func mayWritePublicThreadOf(ctx context.Context, db *store.DB, thread string) error {
	if thread == "" {
		return nil
	}
	private, err := db.ThreadIsPrivate(ctx, thread)
	if err != nil {
		return err
	}
	if private {
		return refuseChat(http.StatusBadRequest,
			"thread "+thread+" is a private conversation; a message written here "+
				"would carry your project and be read by everybody in it - "+
				"answer it with POST /api/dm/{to} instead")
	}
	return nil
}

// mayNamePrivateParents reports whether every parent the writer named is itself
// a direct message, and answers 400 when one is not.
//
// mayNameParents has already refused the ones that are out of reach, so what is
// left to refuse is a parent that is perfectly readable and public. See
// store.PublicParents for why the DAG is kept closed.
func (s *server) mayNamePrivateParents(w http.ResponseWriter, r *http.Request, parents []string) bool {
	public, err := s.db.PublicParents(r.Context(), principalOf(r), parents)
	if err != nil {
		serverError(w, r, err)
		return false
	}
	if len(public) > 0 {
		writeJSON(w, http.StatusBadRequest,
			errorBody("parent "+public[0]+" is not a private message; a direct message "+
				"answers a direct message or nothing, so that the whole thread is private "+
				"rather than each row of it"))
		return false
	}
	return true
}

// handleDMRead is the private log: every direct message this principal is a
// party to, in log order, oldest first, paged by the same cursor a room is.
//
// It is a narrowing of the same read and never a second permission. The events
// it can return are exactly the ones EventFilterSQL already allows; Private only
// stops the answer carrying rooms as well.
//
// GET /api/dm?since=&thread=&limit=
func (s *server) handleDMRead(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, err := cursorParam(q.Get("since"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	list, err := s.readDMs(r, q.Get("thread"), since, intParam(q.Get("limit")))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeDMs(w, since, list)
}

// handleDMWait is the watcher over the private log, and it is the room watcher's
// loop: the same tick, the same finite window, the same meaning for a cancelled
// request. A second idea of how long "blocks" is would be one too many.
//
// GET /api/dm/wait?cursor=&thread=&window=&limit=
func (s *server) handleDMWait(w http.ResponseWriter, r *http.Request) {
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
		list, err = s.readDMs(r, thread, cursor, intParam(q.Get("limit")))
		return len(list) > 0, err
	})
	switch {
	case errors.Is(err, errClientGone):
		return
	case err != nil:
		serverError(w, r, err)
		return
	}
	writeDMs(w, cursor, list)
}

// readDMs is the one read both endpoints share, so the list and the long poll
// narrow the log the same way.
func (s *server) readDMs(r *http.Request, thread string, since int64, limit int) ([]*store.Event, error) {
	p := principalOf(r)
	return s.db.ListEvents(r.Context(), p, store.EventQuery{
		Type:    chatEventType,
		Private: true,
		Thread:  thread,
		Since:   since,
		// No ScopeAll. ?scope=all is the operator's window onto their own node
		// and every other read honours it, but a private conversation is the one
		// thing an operator reading it would be reading over somebody's shoulder
		// rather than operating. The operator can still see that DMs exist - the
		// rows are in their database - and this simply is not the endpoint that
		// hands them over.
		Limit: limit,
	})
}

// writeDMs answers with the messages and the cursor to ask for next. It says
// private in the body rather than leaving a client to work it out from three
// absent fields: a surface that has to infer "this is not a room" is a surface
// that will one day draw a DM as a room message.
func writeDMs(w http.ResponseWriter, since int64, list []*store.Event) {
	writeJSON(w, http.StatusOK, map[string]any{
		"private": true,
		"events":  list,
		"since":   since,
		"cursor":  cursorOf(since, list),
	})
}
