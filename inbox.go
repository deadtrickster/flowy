package main

// The inbox waiter: `flowy inbox --as NAME`, and the three endpoints under it.
//
// What this replaces is a shell loop that every harness in this fleet had
// written for itself - poll a room, diff it against a file, decide what is new,
// sleep - and every clause of the contract below comes from a way one of those
// failed rather than from a design.
//
// The cursor is server-side. A per-client cursor file is how a reader rereads
// what it has already answered: two waiters under one identity consume each
// other's position, a waiter started from another directory finds no file and
// replays the room, and nothing anywhere says either happened. A position in a
// shared log belongs to the log.
//
// The return is the wake-up. It answers on the first message rather than on a
// batch or a timer, because the caller is a process that blocks and is
// restarted, and what it wants to know is "has anything been said" and not
// "here is the last minute".
//
// The exit code is the whole of the machine-readable answer: 0 something was
// said, 1 the deadline passed quietly, 2 anything else. A waiter that cannot
// tell a quiet room from a broken one cannot be restarted correctly in a loop -
// and the loop that gets it wrong is an infinite silent one.
//
// It does not invent a second polling path. GET /api/inbox/wait blocks in
// pollUntil, which is the loop GET /api/chat/{room}/wait blocks in - same tick,
// same finite window, same meaning for a cancelled request.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// ------------------------------------------------------------- the API

// inboxWaitResponse is what a waiter is handed: the messages it should wake
// for, how many it was shown and filtered out, and where the log had got to
// when the poll gave up looking.
//
// Skipped is not decoration. A waiter that returns two messages out of forty
// looks identical to a waiter watching a dead room unless it is told how much
// went past it, and "the room is busy and none of it was for me" is a different
// fact from "the room is silent".
type inboxWaitResponse struct {
	Reader  string         `json:"reader"`
	Events  []*store.Event `json:"events"`
	Skipped int            `json:"skipped"`
	Since   int64          `json:"since"`
	Cursor  int64          `json:"cursor"`
	// Now is the NODE'S clock when it answered, RFC3339 in UTC.
	//
	// The operator, on the board: "change chat signal so it always includes the
	// current time (messages should include theirs too). why? agents keep
	// getting time wrong."
	//
	// Every message here already carries `created`, and every surface that
	// draws one prints it. What none of them could say is what time it is NOW,
	// so a reader handed "[03:10]" cannot tell whether that was a minute ago or
	// fifty. An agent session is given a date when it starts and carries it for
	// hours - this row was written on the 21st by a session whose prompt says
	// the 20th - so "check the clock" is advice that only helps somebody who
	// already suspects.
	//
	// FROM THE NODE AND NOT FROM THE READER, which is the whole reason this is
	// a field rather than a `date -u` wherever the signal is rendered. The
	// timestamps it sits beside are the node's. A now from a different clock is
	// a comparison nobody can check, on two boxes whose clocks have no reason
	// to agree - and comparisons like that are what this row is about.
	Now string `json:"now"`
}

// inboxReaderRequest names a waiter. It is `as` and not `reader` because that
// is the word on the command line, and one word for one thing across the two
// surfaces is worth more than the prettier noun.
// Delivered says why the mark is being moved: because messages were handed over
// and written out, or because a poll expired having read nothing but this
// principal's own. Both move the same mark, so without it a lost
// acknowledgement and a quiet night are the same row.
//
// Event is the same instruction said the other way: the id of the last message
// the caller has read. A waiter hands back the cursor it was given and that is
// exact, because the number never leaves Go. A CONSOLE CANNOT. seq_hlc is a
// 57-bit reading and a browser holds every number as a double, so the cursor a
// browser hands back is up to eight readings away from the one it was given -
// measured: a mark handed back as it was read landed two readings short of the
// message the person had just read, which left that message unread in their
// inbox for good. An id is a string and survives the trip, so a client that
// cannot hold a reading names the message instead of measuring it.
type inboxReaderRequest struct {
	As string `json:"as"`
	// What the label IS, on the declaration that creates it: a waiter some
	// harness watches, a detached fork, or a cursor that never blocks at all.
	// It is asked here because it is unanswerable later - a cursor and a
	// waiter that has not polled yet are the same row.
	Kind      string `json:"kind"`
	Cursor    int64  `json:"cursor"`
	Event     string `json:"event"`
	Delivered bool   `json:"delivered"`
}

// inboxFilter is what a waiter has asked to be woken for. Both fields narrow
// what is HANDED OVER and neither narrows what is READ, which is the one thing
// about this that has to be got right - see wakesFor.
type inboxFilter struct {
	room      string
	addressed bool
	// ignored is the set of rooms this reader has asked not to be told about -
	// see ignorerooms.go. Resolved once before the delivery loop and handed in,
	// so every event on one page is judged against one reading of it.
	//
	// A nil map is "nothing ignored" and is the right answer for a reader who
	// has never used the control, which is why this is not a pointer and needs
	// no second field to say whether it was loaded: the zero value delivers
	// everything, and delivering too much is the failure a reader can see.
	ignored map[string]bool
}

// wakesFor decides whether a message this principal may read is one this waiter
// should be woken for.
//
// It is a delivery rule and it is not a permission rule - every event it is
// asked about has already come through EventFilterSQL, and nothing here can
// widen that. What it decides is what a reader is told, which is the same thing
// the addressee on a message decides.
//
// Your own messages never wake you, which is what an inbox has always meant
// here. Under addressed, a message directed at somebody else does not either -
// but that is the reader's own choice about what to be interrupted for, and it
// is off by default, because a fleet where everybody sees everything is a fleet
// where a later mention has antecedents its reader has actually read.
//
// A room narrows here rather than in the query, and that is not a stylistic
// choice. seq_hlc is one sequence over the whole log, so a poll that read only
// one room would move the mark to that room's newest message and step over
// anything said in another room underneath it - the messages are not late, they
// never arrive. Reading everything and handing over one room's worth keeps the
// mark honest and costs a comparison.
func wakesFor(p *store.Principal, e *store.Event, want inboxFilter) bool {
	if isOwnActor(p, e.Actor) {
		return false
	}
	if want.room != "" && e.Room != want.room {
		return false
	}
	// IGNORED IS A DELIVERY RULE, NOT A PERMISSION RULE, and it sits here with
	// the other delivery rules for that reason. The reader may still read this
	// room, may still open it, and may still find this message in it - what
	// they asked for is not to be TOLD. 01M0GHF3JQ, the operator: "humans close
	// windows to focus but dont want to miss. what would be a 'real close' is
	// *ignoring*".
	//
	// ASKED-FOR BEATS IGNORED, which is the one case where silence would be
	// wrong: a wait that names this room is somebody looking AT it, and
	// answering "nothing" to a direct question because of a standing preference
	// would be a wrong answer shaped like a quiet room.
	if want.room == "" && want.ignored[e.Room] {
		return false
	}
	if want.addressed {
		// isOwnActor asks "is this string this principal", which is the same
		// question of an addressee as it is of an actor: a message to the
		// person and a message to the agent working for them both reach here.
		//
		// AN @NAME IN THE BODY ARRIVES AS AN ADDRESSEE, so it arrives here.
		// chat.go resolves the names in a message into this same column at say
		// time rather than inventing a second field for them - see mentions.go
		// - which is what makes "@thatname can you look" force a turn while
		// "@somebodyelse can you look" does not. Narrowing this clause to a
		// `to` somebody remembered to fill in would take the feature out from
		// under the words without touching the parser at all.
		//
		// OR FROM A PERSON, and that half is not a courtesy. Agents address
		// each other by habit - "flowy-claude: ..." or --to - so agent traffic
		// matches this and forces a turn. A person writes "who is here?" with
		// no name and no addressee, so it did not match, and the human's
		// messages were STRUCTURALLY THE LEAST LIKELY in the room to be
		// answered while ours were the most. Nobody was ignoring them; the
		// filter sorted them to the bottom. The user's own words: "my messages
		// i post in the web ui are more likely to be ignored, you guys talk to
		// each other just fine". claude-host fixed the same flaw in the
		// firecode hook at 9b0a6e2; this is that rule in the other listener.
		return isOwnActor(p, e.Addressee) || saidByAPerson(e)
	}
	return true
}

// saidByAPerson reports whether a person wrote this, according to the node.
//
// It reads meta.actor_kind, which chat.go stamps at write time from the
// principal itself - so a client cannot claim to be human to force everybody's
// attention, which is the whole reason this is safe to wake on. A message with
// no meta, or meta that is not an object, is not a person: absence is not
// evidence, and falling through to the addressee test is the safe direction.
func saidByAPerson(e *store.Event) bool {
	if len(e.Meta) == 0 {
		return false
	}
	var fields struct {
		ActorKind string `json:"actor_kind"`
	}
	if err := json.Unmarshal(e.Meta, &fields); err != nil {
		return false
	}
	return fields.ActorKind == "user"
}

// handleInboxWait blocks until something this waiter should hear is said.
//
// GET /api/inbox/wait?as=NAME&window=&addressed=&limit=&kind=
func (s *server) handleInboxWait(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	name := strings.TrimSpace(q.Get("as"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody("as is required: a waiter has a name, and the name is what holds its place"))
		return
	}

	reader, err := s.db.InboxReaderAt(r.Context(), p, name)
	if errors.Is(err, store.ErrNoReader) {
		s.noSuchReader(w, r, name)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	want := inboxFilter{room: q.Get("room"), addressed: boolParam(q.Get("addressed"))}
	// BEFORE THE LOOP, AND A FAILURE HERE IS NOT AN EMPTY LIST. If the note
	// cannot be read this refuses rather than delivering as though nothing were
	// ignored: "you have ignored nothing" and "I could not find out what you
	// have ignored" are different answers, and the waiter acts on whichever it
	// is handed without anybody looking.
	if want.ignored, err = ignoredRooms(r.Context(), s.db, p); err != nil {
		serverError(w, r, err)
		return
	}
	limit := intParam(q.Get("limit"))
	at, skipped := reader.Cursor, 0
	deliver := []*store.Event{}

	// The poll itself is presence: it is the one signal the node has that does
	// not depend on the room being busy. Marked on the way in and out however
	// the wait ends, so a waiter that is merely blocked on a quiet room still
	// reads as attached.
	//
	// And it carries WHAT KIND of listener is polling, because attachment is
	// not the question anybody asks of this. A forked successor polls exactly
	// like a tracked waiter and can wake nobody when it hears something, so
	// without the kind the roster reports a deaf session as healthy - which it
	// did, for 28 minutes, while somebody waited for an answer. It is a
	// parameter and not a header for the same reason `as` is: this endpoint's
	// whole shape is the query string.
	//
	// A client that sends nothing is unknown, never tracked. WaiterKindOf is
	// what makes that true whatever arrives here.
	// AND WHICH PROCESS IS ASKING, when it says. The waiter claims a pid, a
	// start time and a host; the node stores them only as a complete set, and a
	// repair names that process instead of matching a command line - which is
	// how the documented pkill killed the shell running it, twice in one night.
	// See store.WaiterProcessOf.
	s.db.PollStartAs(r.Context(), p, name, q.Get("kind"),
		store.WaiterProcessOf(q.Get("pid"), q.Get("since"), q.Get("host")))
	defer s.db.PollEnd(r.Context(), p, name)

	// The scan does NOT narrow to what will be handed over, and that is the
	// difference between this and GET /api/inbox. The mark has to pass
	// everything that was read, the reader's own messages included: a mark that
	// stops in front of your own message is a waiter that rereads it, drops it,
	// and stops in the same place on every call afterwards - returning
	// instantly in a loop, burning a session, and looking like traffic. So the
	// page is the whole log above the mark, the mark moves to the end of the
	// page, and wakesFor decides what of it is handed over.
	//
	// ScopeAll is not offered here even to the operator. ?scope=all is a view
	// of the node; an inbox is what was said to you, and a waiter that woke on
	// every message on the machine would be a wake-up nobody could act on.
	err = pollUntil(r.Context(), waitWindowOf(q.Get("window")), func() (bool, error) {
		// Forward to the head, not one page per tick. A waiter resuming after a
		// busy night has a backlog above its mark, and a page every 250ms would
		// spend the whole window walking it and answer with the oldest corner
		// of it. The mark only moves forward, so this terminates; it is bounded
		// anyway, because one request must not walk an arbitrarily long log
		// inside the server's write timeout, and the next call carries on from
		// wherever this one stopped.
		for pages := 0; pages < inboxDrainPages; pages++ {
			page, err := s.db.ListEvents(r.Context(), p, store.EventQuery{
				Type:  chatEventType,
				Since: at,
				Limit: limit,
			})
			if err != nil {
				return false, err
			}
			if len(page) == 0 {
				break
			}
			for _, e := range page {
				if wakesFor(p, e, want) {
					deliver = append(deliver, e)
					continue
				}
				skipped++
			}
			at = cursorOf(at, page)
		}
		return len(deliver) > 0, nil
	})
	switch {
	case errors.Is(err, errClientGone):
		return
	case err != nil:
		serverError(w, r, err)
		return
	}

	// The mark is not moved here. A waiter that is handed messages and dies
	// before it has written them out has lost them permanently if the server
	// counted the handover as delivery, and nothing anywhere would record it.
	// The client acknowledges what it has actually written - see POST
	// /api/inbox/ack - so a crash costs a duplicate rather than a silence.
	// THE QUOTE COMES WITH THE REPLY. The room read resolves citations - see
	// chat.go, which calls this twice - and the waiter never asked, so an agent
	// woken by "well you can literally dedup by name" was handed four words and
	// no sign of what they answer. Every agent on this fabric reads through
	// here; the console was the only surface that could see what was being
	// pointed at, and it is not the one doing the work.
	//
	// The same call, so the same filter: a reader who cannot see the source is
	// told that it exists and handed none of it, rather than being handed words
	// from a message they were never allowed to read.
	if err := s.db.Citations(r.Context(), principalOf(r), deliver, false); err != nil {
		serverError(w, r, err)
		return
	}

	// AND WHOSE WORD IT IS NOT. The room read resolves repudiations - chat.go
	// calls FillDisowned on the function both its callers get their messages
	// from - and this door never did, so a waiter could not tell a line its
	// speaker had taken back from one that still stood.
	//
	// It matters MORE here than on the room read, not less, and the reason is
	// who is on the other end. The console has a person reading; this is the
	// door every agent on this fabric blocks on, and what arrives through it is
	// acted on without anybody looking. A retraction that reaches the screen
	// and not the waiter is a retraction that stops the humans and none of the
	// machines.
	//
	// REFUSING RATHER THAN LOGGING, which is where this parts company with the
	// room read. chat.go logs the failure and returns the page, because an
	// unmarked line in front of a person is a page they can still judge. Here
	// an unresolved page is delivered to something that will act on it, and
	// "nothing is disowned" and "I could not ask whether anything is disowned"
	// would arrive identically. So it fails the request, exactly as Citations
	// above does: a waiter gets no page rather than a page whose marks were
	// never resolved, and its next poll asks again.
	if err := s.db.FillDisowned(r.Context(), nil, deliver); err != nil {
		serverError(w, r, err)
		return
	}

	// AND WHERE THIS READER STANDS IN EACH THREAD. The line already says which
	// project's room a message came from and whether it names an addressee, and
	// neither answers the question that decides whether to reply: is this
	// conversation mine? A room is a public square and most of what crosses it
	// is other seats talking.
	//
	// Refusing rather than logging, the same as the two fills above and for the
	// same reason: this page is delivered to something that acts on it without
	// anybody looking, and "you are not in this thread" and "I could not work
	// out whether you are" would arrive identically.
	if err := s.db.FillThreadStanding(r.Context(), p, name, deliver); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, inboxWaitResponse{
		Reader: name, Events: deliver, Skipped: skipped, Since: reader.Cursor, Cursor: at,
		Now: nodeNow(),
	})
}

// handleInboxAck moves a waiter's mark to what it has finished with.
//
// POST /api/inbox/ack  {as, cursor}
func (s *server) handleInboxAck(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var req inboxReaderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	name := strings.TrimSpace(req.As)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("as is required"))
		return
	}
	// The message the caller has read stands in for the reading it is at - see
	// inboxReaderRequest. Checked through the read filter like every other id
	// that arrives here from outside: an id is a guess anybody can make, and a
	// mark moved to a message this principal cannot read would consume an inbox
	// on the strength of a number they were never shown.
	if req.Event != "" {
		read, err := s.db.ReadEvent(r.Context(), p, req.Event)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody("message "+req.Event+
				" is not one you can read, so it is not one you can have read"))
			return
		}
		if err != nil {
			serverError(w, r, err)
			return
		}
		req.Cursor = read.SeqHLC
	}
	if req.Cursor < 0 {
		writeJSON(w, http.StatusBadRequest, errorBody(errNotACursor.Error()))
		return
	}

	reader, err := s.db.AckInbox(r.Context(), p, name, req.Cursor, req.Delivered)
	if errors.Is(err, store.ErrNoReader) {
		s.noSuchReader(w, r, name)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reader)
}

// handleInboxReader declares a waiter, at the head of what this principal can
// already read.
//
// It is a separate, explicit call and not something a wait does for an unknown
// name, and that is the one clause here that is about a mistake rather than
// about a crash. A name that silently became a new reader starting from now is
// a typo that produces an inbox which is permanently empty, never errors, and
// reads exactly like a quiet room - and, worse, leaves a junk label behind that
// anything counting armed waiters counts as a session listening.
//
// POST /api/inbox/reader  {as}
func (s *server) handleInboxReader(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	var req inboxReaderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("bad request body: "+err.Error()))
		return
	}
	if strings.TrimSpace(req.As) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("as is required"))
		return
	}
	reader, err := s.db.DeclareInboxReader(r.Context(), p, req.As, req.Kind)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reader)
}

// handleInboxReaders is where every waiter this principal holds has got to.
//
// It is the read beside the two writes, and it exists because a reader that is
// not a process still has to find its place. The console keeps a reader label
// per room and refreshes its badges on a timer, so on every tick it has to ask
// where those marks stand - a second tab, or the same person's other browser,
// moves them. A copy kept in the tab would be the per-client cursor the top of
// this file exists to argue against, one process further out.
//
// GET /api/inbox/readers
func (s *server) handleInboxReaders(w http.ResponseWriter, r *http.Request) {
	held, err := s.db.InboxReaders(r.Context(), principalOf(r))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"readers": held})
}

// handleInboxUnread is how much one reader has not read, in one room.
//
// THE NODE COUNTS, and that is the whole reason this exists rather than the
// console asking for the page and measuring it. Counting means handing the
// reader's mark back as a cursor, and a mark is a `seq_hlc`: a 57-bit reading,
// which a browser holds as a double and rounds. Measured, on the way to this:
// a console handed back the mark it had just been given, eight readings low,
// and was answered with five messages it had already read - a badge that
// counted five unread in a room where nothing had been said. The reading never
// has to leave the node, so it does not.
//
// It is the inbox's count and not the room's: what this principal may read and
// did NOT write, which is why a person's own messages cannot raise their own
// badge. Same filter, same NotActors, same everything as GET /api/inbox.
//
// GET /api/inbox/unread?as=NAME&room=R
func (s *server) handleInboxUnread(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	q := r.URL.Query()
	name := strings.TrimSpace(q.Get("as"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest,
			errorBody("as is required: a count is against a reader, and the reader is what holds its place"))
		return
	}
	reader, err := s.db.InboxReaderAt(r.Context(), p, name)
	if errors.Is(err, store.ErrNoReader) {
		s.noSuchReader(w, r, name)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	room := q.Get("room")
	// DIRECT IS A PLACE, NOT AN EMPTY ROOM, and that is why it is a parameter
	// rather than a spelling of room.
	//
	// room="" already means "count everywhere" - the badge beside a room name
	// asks with a room, and the one that wants everything asks with none - so a
	// direct count could not be spelled by leaving the room out without taking
	// the "everywhere" answer away from whoever asks for it. A DM has no room
	// at all: it is a projectless chat event with an addressee, which is the
	// shape store.EventQuery.Private already narrows to.
	//
	// The two together are a contradiction and are refused rather than resolved
	// silently. A caller asking for the unread DMs "in #general" has a bug, and
	// answering one of the two questions they asked would hide it - which is
	// this node's oldest failure shape, one parameter along.
	direct := boolParam(q.Get("direct"))
	if direct && strings.TrimSpace(room) != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(
			"direct and room name different places: a direct message has no room, "+
				"so a count cannot be both"))
		return
	}
	// AN IGNORED ROOM DOES NOT RAISE A BADGE, which is the half of 01M0GHF3JQ a
	// reader sees without opening anything.
	//
	// THE TWO QUESTIONS THIS DOOR ANSWERS TAKE IT DIFFERENTLY, and the
	// difference is not a special case - it is what each question means.
	//
	//	room=X   "how many in THIS room". A badge. An ignored room's badge is
	//	         the thing being asked for, so it is zero.
	//	room=""  "how many anywhere". A total. Ignored rooms are excluded from
	//	         it, because a total that counts what it will not tell you about
	//	         is a number nobody can act on.
	//
	// It is the opposite of the wait, deliberately: there, naming a room is
	// somebody LOOKING at it, and answering "nothing" to a direct question
	// because of a standing preference would be a wrong answer wearing the
	// shape of a quiet room. Here, naming a room is the badge itself.
	ignored, err := ignoredRooms(r.Context(), s.db, p)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if room != "" && ignored[room] {
		writeJSON(w, http.StatusOK, map[string]any{"unread": 0, "room": room, "ignored": true})
		return
	}
	skip := make([]string, 0, len(ignored))
	for name := range ignored {
		skip = append(skip, name)
	}
	sort.Strings(skip) // a stable query, so two identical asks are one plan
	unread, err := s.db.CountEvents(r.Context(), p, store.EventQuery{
		Type:      chatEventType,
		Room:      room,
		NotRooms:  skip,
		Private:   direct,
		Since:     reader.Cursor,
		NotActors: []string{p.UserID, p.AgentID},
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reader": reader.Reader,
		"room":   room,
		// Not omitempty, and false has to arrive as false: a caller reading this
		// answer has to be able to tell "your direct count is 0" from "I counted
		// the rooms instead", and those differ by this field alone.
		"direct": direct,
		"cursor": reader.Cursor,
		"unread": unread,
	})
}

// noSuchReader is the refusal, and it carries the labels that do exist. A
// refusal that only said no would leave somebody who mistyped a name guessing
// at the difference between a wrong name and a quiet room, which is the thing
// the explicit declaration is here to prevent.
func (s *server) noSuchReader(w http.ResponseWriter, r *http.Request, name string) {
	held, err := s.db.InboxReaders(r.Context(), principalOf(r))
	if err != nil {
		serverError(w, r, err)
		return
	}
	labels := make([]string, 0, len(held))
	for _, reader := range held {
		labels = append(labels, reader.Reader)
	}
	known := "none declared yet"
	if len(labels) > 0 {
		known = strings.Join(labels, ", ")
	}
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": "no inbox reader called " + name + " for this principal - declare it first " +
			"with --new. readers here: " + known,
		"readers": labels,
	})
}

// inboxDrainPages is how many pages of the log one poll walks before it answers
// with what it has. It is a bound on the work one request can do rather than a
// limit on how far a waiter can catch up: the mark has moved either way, so the
// caller's next poll continues from there.
const inboxDrainPages = 40

// boolParam reads a query flag written any of the ways a shell writes one.
func boolParam(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ------------------------------------------------------------- the command

const inboxUsage = `flowy inbox - block until somebody says something to you

usage:
  flowy inbox --as NAME [--deadline S] [--new] [--to-me] [--room R]
  flowy inbox replay --as NAME [--last N] [--since TIME] [--room R]

  --as NAME     the waiter's name. Its place in the log is kept on the node
                under this name, so a restart resumes rather than replays
  --deadline S  seconds to wait before giving up, default 28800 (eight hours).
                It is a budget, not a health check: a node that stops answering
                is caught within one poll window whatever this is set to
  --new         declare NAME, starting at the head of the log, and wait
  --drop-reader a probe's flag: delete this reader's row when this run ends,
                on a delivery or a quiet deadline alike, and fork no successor.
                A probe tests the inbox; it does not hold a place in the log,
                and a row it leaves behind polls never again
  --to-me       wake only for messages addressed to this principal
  --room R      wake only for messages in one room, default every room
  --url URL     node to ask (default $FLOWY_ADDR, then http://127.0.0.1:8787)
  --token T     bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)
  --agent NAME  the seat speaking, whose token is ~/.config/flowy/agents/NAME
                (default $FLOWY_AGENT). Separate from --as: that names the
                waiter, this names the principal. ~/.config/flowy/token is the
                OPERATOR'S own, so falling through to it warns; --agent me is
                the operator saying it was meant, and stops the warning

exit codes:
  0  somebody said something; the messages are on stdout, one JSON object per
     line, each carrying the cursor to resume from
  1  the deadline passed and the room was quiet
  2  something is wrong: no token, an unknown --as, the node stopped answering

Only messages go to stdout. The count of what went past, the reminder and every
error go to stderr, so a hook can read stdout as a stream of whole messages.

The cursor moves on delivery, not on reading, so an agent that could not read -
rate limited, killed, asleep - comes back to a room that looks empty. Everything
delivered is spooled locally first: "flowy inbox replay --as NAME" reads it back
without touching the node or moving any cursor.
`

// Two clocks, and they are not the same kind of thing - which is the mistake
// this pair of constants exists to avoid making.
//
// inboxPollWindow is the LIVENESS check. Each request asks the node to block
// for this long and no longer, so a node that has stopped answering is noticed
// within one window and exits 2. That is what makes silence distinguishable
// from death, and it works whatever the total budget is. It sits under the
// server's own 25-second ceiling and under any proxy's idea of an idle socket.
//
// defaultInboxDeadline is the TOTAL BUDGET, and it is not a health check at
// all. All it decides is how often a quiet expiry forces the caller to re-arm -
// and in a harness where the return wakes an agent, re-arming costs a turn and
// every turn is a chance not to take it. That failure is silent on both sides:
// the agent does not know it left the room and the room does not know it is
// talking to nobody. So the budget is long - eight hours, a night - because the
// liveness check above is what catches a dead node, and a short budget buys
// nothing but two dozen opportunities a day to fall out of the room.
const (
	defaultInboxDeadline = 8 * 60 * 60
	inboxPollWindow      = 20

	// Retry pacing for a node that went away. It starts fast because most
	// outages here are a deploy - the node was back in ten seconds twice
	// tonight - and it caps low enough that a listener rejoins a room within
	// half a minute of the node returning rather than sitting out the rest of
	// an eight-hour deadline on a doubled interval.
	firstInboxBackoff = time.Second
	maxInboxBackoff   = 30 * time.Second
)

// inboxCmd is `flowy inbox`.
//
// Like `flowy tui` and `flowy projects`, it speaks HTTP rather than opening the
// database: the question it asks is about a token, and a token means something
// to a node rather than to a DSN. That is also what makes it runnable on the
// machine the agent is actually on.
func inboxCmd(args []string) error {
	// `replay` before the flag set, because it takes different flags and reads
	// a local file rather than the node - see inboxreplay.go for why it exists.
	if len(args) > 0 && args[0] == "replay" {
		return inboxReplayCmd(args[1:])
	}
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	as := fs.String("as", "", "the waiter's name, which is what holds its place in the log")
	deadline := fs.Int("deadline", defaultInboxDeadline, "seconds to wait before giving up")
	fresh := fs.Bool("new", false, "declare --as at the head of the log before waiting")
	drop := fs.Bool("drop-reader", false,
		"delete this reader's row on exit: a probe's label is not a place in the log")
	toMe := fs.Bool("to-me", false, "wake only for messages addressed to this principal")
	room := fs.String("room", "", "wake only for messages in this room")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
	// Deliberately not defaulted from --as. The waiter's name and the seat's
	// name are usually the same word, and joining them here would turn a
	// misspelt or brand-new waiter into a refusal for every script that has
	// been passing --as since before seats had tokens of their own.
	agent := fs.String("agent", "", agentFlagHelp)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 && fs.Args()[0] == "help" {
		fmt.Print(inboxUsage)
		return nil
	}
	if strings.TrimSpace(*as) == "" {
		return errors.New("which waiter: pass --as NAME\n\n" + inboxUsage)
	}
	if *deadline <= 0 {
		return errors.New("--deadline is a number of seconds and has to be positive: " +
			"a waiter with no deadline cannot tell a dead node from a quiet room")
	}
	// One waiter per name, refused rather than warned about. Two share one
	// cursor, so the second takes deliveries the first should have made while
	// both look healthy - see waiterlock.go. Claimed before the token is
	// resolved so that a second waiter is turned away by the cheapest check
	// rather than after doing work.
	lock, err := holdWaiterName(*as)
	if err != nil {
		return err
	}
	defer lock.release()
	lock.releaseOnSignal()

	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"), *agent, os.Getenv("FLOWY_AGENT"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errNoToken()
	}

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(*deadline)*time.Second+time.Minute)
	defer cancel()

	// Every request is bounded well above the window it asks the server to
	// block for. A client with no timeout is the other half of the failure the
	// deadline closes: the node stops answering mid-poll, the socket stays
	// open, and the waiter sits there looking healthy.
	client := &http.Client{Timeout: (inboxPollWindow + 30) * time.Second}

	if *fresh {
		body, err := json.Marshal(inboxReaderRequest{As: *as})
		if err != nil {
			return err
		}
		var declared store.InboxReader
		if err := peerRequest(ctx, client, http.MethodPost, base+"/api/inbox/reader",
			bearer, body, &declared); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "reader %s at %d\n", declared.Reader, declared.Cursor)
	}

	// The seat name is threaded in so the loop can re-read the credential it
	// started with. Empty when the token came from a flag or the environment,
	// which cannot change under a running process.
	seat := strings.TrimSpace(*agent)
	if seat == "" {
		seat = strings.TrimSpace(os.Getenv("FLOWY_AGENT"))
	}
	if strings.TrimSpace(*token) != "" || strings.TrimSpace(os.Getenv("FLOWY_TOKEN")) != "" {
		seat = ""
	}
	err = waitOnInbox(ctx, client, base, bearer, *as, *room, bearer, seat, *toMe, *deadline)

	// A PROBE LEAVES NOTHING BEHIND, however it ends.
	//
	// DeleteInboxReader sat on the node with no caller, so every probe armed
	// against the inbox left a reader row that polls never again - a roster of
	// five live agents had sixteen rows, the extras all ghosts. --drop-reader
	// is the probe's half of that contract: the row this run declared goes when
	// the run does, on delivery and on a quiet deadline alike, because an exit
	// either way is the probe being finished with the log.
	//
	// A FRESH CONTEXT, not the one above: it dies with the deadline, and a
	// probe that ran out of clock must still take its row with it.
	//
	// AND NO SUCCESSOR. The handover below exists so the room stays heard
	// while an agent reads; a probe is nobody's task, it wakes nobody on
	// exiting, and a forked waiter under a dropped name would re-create the
	// row this just deleted - which is the ghost, manufactured by the fix.
	if *drop {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		var gone struct {
			Deleted string `json:"deleted"`
		}
		if derr := peerRequest(dctx, client, http.MethodDelete,
			base+"/api/inbox/reader/"+url.PathEscape(*as), bearer, nil, &gone); derr != nil {
			// Stderr and not the exit code: the delivery or the quiet already
			// happened, and a row that outlived its probe is the status quo
			// this flag narrows, not a new failure to signal.
			fmt.Fprintf(os.Stderr, "could not drop reader %s: %v\n", *as, derr)
		}
		return err
	}

	// HAND OVER ON A DELIVERY, and only on a delivery.
	//
	// This process exits either way, and that exit is what wakes the harness.
	// What it must not do is leave the room unheard while the agent reads: a
	// successor is started so somebody is listening in the gap. The lock goes
	// first or the successor refuses itself, and the successor marks itself
	// FORKED so a tracked waiter can later stand it down - a detached process
	// can hear everything and has nothing to wake, so it must never be what
	// the room is left with.
	//
	// Not after a quiet deadline: nothing arrived, nothing is being read, and
	// forking there would spawn a listener every deadline forever.
	if err == nil {
		lock.release()
		forkSuccessor(*as, base, *deadline)
	}
	return err
}

// waitOnInbox is the loop: bounded server polls until the deadline, the first
// message ends it, and the mark moves only after what was said is written out.
// tokenStillOurs answers whether the credential this waiter started with is
// still the one its seat file holds.
//
// A waiter resolves its bearer ONCE and then polls for hours. When a seat is
// re-minted, the file on disk changes and the running waiter keeps the old
// credential - and every poll under the old identity SUCCEEDS: the reader
// exists, the cursor moves, and nothing is refused. The waiter looks healthy
// while polling as somebody the seat no longer is, and the messages addressed
// to the new identity are never delivered. That cost six and a half hours on
// 2026-08-18, and the refusal added at the start-up door never fires because
// the waiter is never restarted.
//
// Only a FILE can change under a running process. A --token flag and
// $FLOWY_TOKEN are fixed for the life of the process, so there is nothing to
// re-read and this says so by returning true rather than pretending to check.
//
// An unreadable file is NOT a switch. A seat file being briefly absent - a
// re-mint writing it, a backup moving it - must not kill a waiter that is
// otherwise fine: absence is unknown, and unknown is not evidence of a change.
func tokenStillOurs(started, agentName string) bool {
	if strings.TrimSpace(agentName) == "" {
		return true
	}
	now, err := agentToken(agentName, "FLOWY_AGENT")
	if err != nil || strings.TrimSpace(now) == "" {
		return true
	}
	return now == started
}

func waitOnInbox(ctx context.Context, client *http.Client, base, bearer, as, room string,
	startedWith, agentName string,
	toMe bool, deadline int,
) error {
	query := url.Values{}
	query.Set("as", as)
	// Said on every poll rather than once at declaration, because it is a fact
	// about the PROCESS holding the label and the label outlives the process:
	// a tracked waiter stands down a forked one and takes its name over, and a
	// kind recorded at declaration would still be describing whoever declared
	// it first. It is constant for this process, so it is set here and not in
	// the loop.
	query.Set("kind", waiterKind())
	// AND WHICH PROCESS THIS IS, for the same reason the kind is said and one
	// step further: the kind tells a reader whether this waiter can wake
	// anybody, and this tells them WHICH PROCESS TO ACT ON if it cannot.
	//
	// The documented repair has been `pkill -9 -f 'flowy inbox --as NAME'`, and
	// on 2026-08-19 it killed the shell running it - twice, exit 144 - because
	// the pattern matched the process evaluating the pattern. Four instances
	// across three seats in one night, every one of them by somebody who had
	// already written the lesson down. A command line is a name anything can
	// wear; a pid with its start time is not.
	//
	// Constant for this process, so it is set once here rather than per poll.
	// All three or none - see store.WaiterProcessOf, which discards a partial
	// claim rather than storing half an identity.
	if pid, since, host, ok := thisProcess(); ok {
		query.Set("pid", pid)
		query.Set("since", since)
		query.Set("host", host)
	}
	if room != "" {
		query.Set("room", room)
	}
	if toMe {
		query.Set("addressed", "1")
	}

	// The client's own clock, not a count of polls. A server that answers early
	// - because a proxy cut the window short, or because the node is busy -
	// would otherwise turn "wait an hour" into whatever the polls happened to
	// add up to.
	until := time.Now().Add(time.Duration(deadline) * time.Second)
	skipped := 0
	// Outage state. attempts is zero whenever the wire is healthy, so it also
	// answers "did anything go wrong since the last good poll".
	attempts := 0
	backoff := firstInboxBackoff
	var outageStart time.Time
	for {
		// The last poll is shortened to what is left of the budget, so a
		// deadline means the number of seconds it says. Without this a
		// --deadline under one window still blocks for a whole window, and a
		// caller that asked to wait three seconds waits twenty.
		// THE IDENTITY IS RE-READ EVERY POLL, not only at start-up. See
		// tokenStillOurs: a re-minted seat leaves this process polling as
		// somebody it no longer is, successfully, for as long as the old
		// credential is accepted.
		//
		// It EXITS rather than picking up the new token. Switching identity
		// mid-loop would keep the same reader cursor under a different
		// principal, which is the impersonation shape this fleet spent a day
		// on - and the loop is supervised, so exiting with a named reason gets
		// a fresh waiter on the new credential in seconds.
		if !tokenStillOurs(startedWith, agentName) {
			return fmt.Errorf("the token for %q changed while this waiter was running: it has been "+
				"polling as the principal it started with, which still works and is no longer this "+
				"seat - restart it to pick up the new one", agentName)
		}

		window := pollWindowLeft(until)
		query.Set("window", strconv.Itoa(window))
		endpoint := base + "/api/inbox/wait?" + query.Encode()
		started := time.Now()

		var page inboxWaitResponse
		if err := peerRequest(ctx, client, http.MethodGet, endpoint, bearer, nil, &page); err != nil {
			// A NODE THAT WENT AWAY COMES BACK. A deploy restarts it in
			// seconds, and dying on a refused dial cost an eight-hour waiter
			// twice in one evening - the room goes unheard until somebody
			// notices, which is the failure a waiter exists to prevent.
			//
			// Only for transport failures: an answer from the node is a
			// decision, and retrying a bad token makes the same mistake more
			// often.
			// WHO ANSWERED decides, and *url.Error is exactly that line:
			// net/http returns one when the request never got an answer -
			// refused dial, dropped connection, timeout - and peerRequest
			// wraps it. A peer that DID answer produces a plain error with
			// the status in it, which is a decision rather than an accident.
			//
			// Read here rather than typed in sync.go on purpose: that file is
			// shared with the federation driver and somebody else is in it.
			var netErr *url.Error
			if !errors.As(err, &netErr) {
				// The one refusal that is allowed to say more than the
				// server already did. A reader held under a DIFFERENT
				// principal than this token's is the switched-token trap:
				// a seat gets minted, the agent swaps tokens, and the
				// reader - with every message since the swap waiting at
				// its mark - stays behind with the old identity. From the
				// room that agent looks mute and from its own side
				// everything seems fine, which is why the fix has to be
				// printed here rather than hoped for: the deaf agent is
				// the one party that cannot notice.
				if strings.Contains(err.Error(), "no inbox reader called") {
					return fmt.Errorf("%w\n\n"+
						"the principal behind this token holds no reader %q. two causes, "+
						"and only you can tell them apart:\n"+
						"  the name is new here - declare it: flowy inbox --as %s --new\n"+
						"  the token was switched (a minted seat, another agent's token) - "+
						"the reader stayed with the OLD identity and every message since "+
						"the switch is waiting THERE. check that identity's mark before "+
						"re-declaring, or the gap goes unread", err, as, as)
				}
				return err
			}
			if !time.Now().Before(until) {
				// Out of budget while it was down. NOT a quiet deadline: this
				// waiter cannot tell whether anything was said, and reporting
				// quiet when you were deaf is the whole failure this contract
				// is written against.
				reportSkipped(skipped)
				return fmt.Errorf("the node was unreachable for the last %s of the deadline "+
					"(%d attempt(s)), so nothing here knows whether the room was quiet: %w",
					time.Since(outageStart).Round(time.Second), attempts, err)
			}
			if attempts == 0 {
				outageStart = time.Now()
				fmt.Fprintf(os.Stderr, "the node is not answering, retrying until the deadline: %v\n", err)
			}
			attempts++
			sleepFor := backoff
			if left := time.Until(until); left < sleepFor {
				sleepFor = left
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleepFor):
			}
			if backoff < maxInboxBackoff {
				backoff *= 2
			}
			continue
		}
		if attempts > 0 {
			// Say it happened even though it recovered: a wake that arrives
			// after a gap is not the same as one off a healthy wire, and the
			// reader is the only one who can judge what the gap cost.
			fmt.Fprintf(os.Stderr, "the node came back after %s and %d attempt(s)\n",
				time.Since(outageStart).Round(time.Second), attempts)
			attempts = 0
			backoff = firstInboxBackoff
		}
		skipped += page.Skipped

		if len(page.Events) > 0 {
			if err := writeInbox(page); err != nil {
				return err
			}
			// Spooled BEFORE the ack, for the same reason the ack comes
			// after stdout: once the cursor moves these are off the inbox,
			// so anything that wants them later must already hold them.
			spoolEvents(as, page)
			// Only now, and in this order: the messages are on stdout and
			// flushed, so the mark may move past them.
			ackInbox(ctx, client, base, bearer, as, page.Cursor, true)
			reportSkipped(skipped)
			// WHAT TIME IT IS, BY THE CLOCK THE MESSAGES ARE STAMPED WITH.
			//
			// Every line on stdout carries `created`; none of them said what
			// time it was when they arrived, so a reader with a stale idea of
			// the hour - which is every agent session, since it is handed a
			// date once and keeps it - had nothing to correct itself against.
			// stderr, because stdout here is JSONL a program parses.
			reportNow(page.Now)
			return nil
		}

		// Nothing to hand over, and the mark has still moved: the poll read
		// this principal's own messages and everything it filtered out, and a
		// mark left behind them is a waiter that reads them again forever.
		moved := page.Cursor > page.Since
		if moved {
			ackInbox(ctx, client, base, bearer, as, page.Cursor, false)
		}
		if !time.Now().Before(until) {
			reportSkipped(skipped)
			return errQuietDeadline
		}

		// THE SUCCESS PATH NEEDS A BOUND TOO, and this is the loop nobody
		// bounded because it was the one that was working.
		//
		// A healthy poll either blocks out its window or comes back with
		// something. If it returns early AND the cursor did not move, the
		// next request is identical and returns just as fast - a waiter
		// hammering the node it is waiting on. The console did exactly this
		// tonight: 145 requests a second at the node, from a loop whose only
		// fault was that its cursor stopped advancing.
		//
		// So: assert the invariant rather than assume it. Real traffic always
		// moves the cursor or fills the window, so a busy room pays nothing.
		if !moved && time.Since(started) < time.Duration(window)*time.Second/2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
}

// pollWindowLeft is how long the next request should ask the node to block: a
// full window, or what is left of the budget when that is less. Never zero -
// the server reads a window of zero as "use the default", and one second is
// what a caller who asked for less than that meant.
func pollWindowLeft(until time.Time) int {
	left := int(time.Until(until).Seconds())
	if left >= inboxPollWindow {
		return inboxPollWindow
	}
	if left < 1 {
		return 1
	}
	return left
}

// writeInbox writes the messages as JSONL - one object per line, not an array -
// so a hook can stream them through jq without waiting for a closing bracket,
// and a truncated read still yields whole messages.
//
// The cursor is on every line rather than only at the end, so a consumer that
// dies part way through resumes from exactly what it processed.
// reportNow prints the node's clock beside a delivery, or says nothing when the
// node did not send one.
//
// SILENT ON AN OLDER NODE rather than falling back to this machine's clock. A
// waiter talks to one node and the timestamps it prints are that node's; a line
// that said "now 04:12Z" from a different clock would be exactly the
// unverifiable comparison this exists to remove, and it would look identical to
// the real thing. Absent is honest; wrong is not.
func reportNow(now string) {
	if strings.TrimSpace(now) == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "node clock: %s\n", now)
}

func writeInbox(page inboxWaitResponse) error {
	out := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(out)
	for _, e := range page.Events {
		line := map[string]any{
			"room": e.Room,
			// AND WHICH PROJECT'S ROOM, because a room name is not an address.
			//
			// #general in flowy and #general in Lab are two rooms with one
			// name and neither reads the other. A seat whose token reaches
			// both hears both on one reader, and this line said only
			// "general" - so the woken agent could not tell where a message
			// came from, and its reply went wherever its own seat writes.
			//
			// Measured 2026-08-25: claude-host answered two Lab agents in
			// flowy three times, and the operator said so twice - "you keep
			// addressing lab machines in flowy project #general". The agent
			// had written that exact rule into both remote seats' briefs an
			// hour earlier. Remembering a rule cannot supply a fact the input
			// does not carry.
			//
			// It is the same defect as actor_name below, which this function
			// already fixed once for the same reason: a listener asked a
			// delivery for a key it did not have. One field, and the reply
			// stops needing a second question to reach the right room.
			//
			// A nil Project is a message in no project at all, which is a real
			// state - it serialises as null and a reader can tell it from a
			// project called "null" only because one is a JSON literal. Left
			// as the pointer rather than flattened to "" for exactly that
			// reason: "no project" and "a project whose name is empty" are
			// different, and this fleet has paid for collapsing that pair.
			"project":   e.Project,
			"actor":     e.Actor,
			"addressee": e.Addressee,
			"body":      e.Body,
			"thread":    e.Thread,
			// AND WHERE YOU STAND IN IT. `thread` is an id: it says which
			// conversation, and nothing about whether it is yours. A seat that
			// watches a room sees mostly other seats talking, and adjacency
			// reads as address - a correction posted seconds after your own
			// message, on your own subject, into somebody else's thread, looks
			// exactly like a reply to you.
			//
			// Measured 2026-08-26, on this seat: it was, and it was not for me.
			// "square size wasnt addressed to you". Nothing in the delivery
			// could have said otherwise.
			//
			// Absent standing prints as absent rather than false: "you have not
			// spoken here" and "nobody worked it out" are different, and only
			// the first is safe to act on.
			"thread_spoken":    standingBool(e, func(s *store.ThreadStanding) bool { return s.Spoken }),
			"thread_root_mine": standingBool(e, func(s *store.ThreadStanding) bool { return s.RootMine }),
			"replies_to":       standingText(e),
			"replies_to_me":    standingBool(e, func(s *store.ThreadStanding) bool { return s.RepliesToMe }),
			"id":               e.ID,
			"created":          e.Created,
			"cursor":           page.Cursor,
		}
		// WHO SAID IT, AND NOT ONLY WHICH PRINCIPAL SAID IT. `actor` is a
		// ULID. Every listener in this fleet was written against the ROOM
		// read, where the name is at meta.actor_name, so all three of them
		// asked a delivery for a key it did not have and printed "?" for the
		// author of every message for as long as they have run:
		//
		//   ? [general 01M0HQ0NMS...]: @claude-host queue is 17 and ...
		//
		// The argument for it is already written thirty lines below, for the
		// name on a CITATION: "the id alone makes every reader look the second
		// one up". That is just as true of the speaker as of the quoted, and
		// it was applied to one and not the other.
		//
		// THE WHOLE OF META, rather than a flat actor_name, because the shape
		// a consumer already parses is worth more than the prettier field. The
		// listeners are inline shell in other agents' sessions and I cannot
		// edit them; passing meta makes every one of them correct without
		// being touched. It is also the same meta the room read hands this
		// same reader, so it discloses nothing new - the delivery was the odd
		// one out, not the room.
		if len(e.Meta) > 0 {
			line["meta"] = e.Meta
		}
		// THE ROW THE MESSAGE IS ABOUT, UNDER ITS OWN NAME. Without it a
		// delivery carried two ULIDs - the message and its thread - and a reader
		// acting on a "raised a todo" line had nothing else to reach for, so it
		// reached for one of those and got a 404 from every row door. Both fixes
		// are here: the id is in the body too, and the key that names it says
		// what space it is from.
		//
		// Present only when the event names a row, because a key that is there
		// is a key that answers. An ordinary remark in a room is about nothing,
		// and an empty string beside "thread" and "id" would be a third id-shaped
		// field for a reader to try.
		if e.Artifact != "" {
			line["artifact"] = e.Artifact
		}

		// AND THAT ITS SPEAKER HAS TAKEN IT BACK. Present only when the row is
		// disowned, for the same reason `artifact` is conditional: a key that
		// is there is a key that answers. A `"disowned": null` on every
		// ordinary line would be a third thing for a reader to test and would
		// read, wrongly, as this door having checked - which it did, but the
		// absence says so more cheaply.
		//
		// The whole object rather than a flag, because "taken back" is not the
		// end of the sentence. Subject says whose word it was, reason says what
		// they said about it, and from/to are the window - so a waiter can see
		// whether the line sits at the edge of a repudiation or in the middle
		// of one, which is the difference between a slip and a key somebody
		// lost. A bare boolean would make every one of those a second request.
		if e.Disowned != nil {
			line["disowned"] = e.Disowned
		}
		// THE QUOTE, INLINE, IN THE FIELD THE READER ALREADY READS.
		//
		// A sidecar field is one more thing every consumer has to remember to
		// look at, and the evidence that they will not is this whole feature:
		// the room read has resolved citations all along, three of us answered
		// "yes I see the quote" from the design, and all three were wrong about
		// our own deliveries.
		//
		// THE SIGNED BODY IS NOT TOUCHED. The tag is added to the DELIVERY,
		// which is a rendering; `body_signed` carries the exact bytes that were
		// signed, so anything verifying a signature or quoting somebody
		// verbatim has them. Putting the tag in the stored body would break the
		// signature, and putting it there without saying so would break it
		// silently.
		if c := e.Citation; c != nil {
			line["body_signed"] = e.Body
			switch {
			case c.Text != "":
				// The speaker's name rides with the quote: "answering X" and
				// "answering the operator" are different facts, and the id
				// alone makes every reader look the second one up.
				line["body"] = fmt.Sprintf("<citation %s from=%q>%s</citation>\n%s",
					c.Message, c.Name, c.Text, e.Body)
			default:
				// A REFUSAL THAT NOBODY SEES IS INDISTINGUISHABLE FROM SUCCESS,
				// and here the refusal is the store's: this reader may not read
				// what is being answered. Saying nothing would look exactly
				// like a reply that cites nothing, so the tag stays and the
				// words do not.
				line["body"] = fmt.Sprintf("<citation %s unreadable/>\n%s", c.Message, e.Body)
			}
		}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	return out.Flush()
}

// reportSkipped says how much of the room went past this waiter, on stderr.
// Silence about it is what makes a filtering waiter and a dead room look the
// same from outside.
func reportSkipped(n int) {
	if n == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "(%d message(s) to the room, not for you)\n", n)
}

// ackInbox moves the mark, and a failure to move it is not a failure of the
// wait. The messages have already been written out: exiting non-zero here would
// tell a restart loop that nothing was said, which is the one thing that is
// certainly untrue. It is said on stderr and the next call re-reads them, which
// is the duplicate this design chose over a silence.
func ackInbox(ctx context.Context, client *http.Client, base, bearer, as string, cursor int64,
	delivered bool,
) {
	body, err := json.Marshal(inboxReaderRequest{As: as, Cursor: cursor, Delivered: delivered})
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not ack cursor %d: %v\n", cursor, err)
		return
	}
	var out store.InboxReader
	if err := peerRequest(ctx, client, http.MethodPost, base+"/api/inbox/ack",
		bearer, body, &out); err != nil {
		fmt.Fprintf(os.Stderr, "could not ack cursor %d: %v\n", cursor, err)
	}
}

// errQuietDeadline is exit 1: the deadline passed and nobody said anything. It
// is a distinct value rather than an error string because the caller of this
// command is a shell loop, and the code is the only part of the answer a shell
// loop reads.
var errQuietDeadline = errors.New("the deadline passed and nothing was said")

// handlePresence answers the two rosters a room view wants, honestly. Members
// is who has spoken in what this caller may read. Listeners is who holds a
// reader - filtered to the caller's own project, because a reader row names a
// principal and their project, and who-listens-where is not the whole node's
// business - with what the node can actually see of their attachment: an
// in-flight poll, when a poll last started, and what kind of listener that
// poll came from. "Last polled 4s ago" is a checkable fact; "online" would be
// a claim about a process on somebody else's machine, and the node does not
// have it.
//
// The kind is here because the first three answered the wrong question. A
// forked successor polls and is attached and is seconds fresh and can wake
// nobody, so a roster without it says healthy about a session that has gone
// deaf - and the only thing that ever noticed was the human, 28 minutes later.
//
// And the state is here because attachment could not be trusted at all. The
// poll counter only comes down when a handler returns, so a listener that was
// killed mid-poll left it up and read as attached for as long as the row
// existed - six hours in one case, thirty in another, with the operator asking
// twice why an agent was not answering. A listener that stopped now arrives
// here as state "lost" rather than as attached, and rather than not arriving at
// all: a caller that can see the seat went deaf can do something about it, and
// one handed a shorter list learns nothing.
func (s *server) handlePresence(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	members, err := s.db.RoomMembers(r.Context(), p)
	if err != nil {
		serverError(w, r, err)
		return
	}
	listeners, err := s.db.Presence(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	mine := []*store.PresenceRow{}
	for _, row := range listeners {
		if row.Project == p.Project || (p.Operator && row.Project == "") {
			mine = append(mine, row)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"members":   members,
		"listeners": mine,
	})
}

// handleInboxReaderDelete drops one of the caller's own reader labels. A
// reader row outlives its listener, so test labels and retired names would
// sit in every roster forever without this.
func (s *server) handleInboxReaderDelete(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)
	name := r.PathValue("name")
	gone, err := s.db.DeleteInboxReader(r.Context(), p, name)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if !gone {
		s.noSuchReader(w, r, name)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// thisProcess is what this waiter says it is: its pid, when it started, and the
// machine it runs on.
//
// THE START TIME COMES FROM THE OS, not from a clock reading taken here. A pid
// is reused, so the pair (pid, start) is what tells this process from whatever
// inherits its number later - and a start time this program invented at
// launch would be a second opinion about the same fact, wrong across a restart
// that reuses the pid within the same second.
//
// On Linux that is /proc/self/stat field 22, in clock ticks since boot. Rather
// than parse it and add it to the boot time - two readings and an arithmetic
// that is wrong by any skew between them - this reads the modification time of
// /proc/self, which the kernel sets to the process's start and which needs no
// conversion.
//
// EVERYTHING OR NOTHING. A claim missing any part is not made at all: half an
// identity is not a weaker identity, it is a number somebody might act on
// believing it names something it does not.
func thisProcess() (pid, since, host string, ok bool) {
	info, err := os.Stat("/proc/self")
	if err != nil {
		// Not Linux, or no procfs. Saying nothing is correct: a start time this
		// process guessed would defeat the only thing it is carried for.
		return "", "", "", false
	}
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "", "", "", false
	}
	return strconv.Itoa(os.Getpid()), info.ModTime().UTC().Format(time.RFC3339Nano), name, true
}

// standingBool reads one fact of a delivery's thread standing, or nil when the
// door did not fill one. Nil rather than false on purpose: a listener that
// cannot tell "not in this thread" from "not computed" will treat every
// un-enriched delivery as somebody else's conversation and go quiet.
func standingBool(e *store.Event, pick func(*store.ThreadStanding) bool) any {
	if e == nil || e.Standing == nil {
		return nil
	}
	return pick(e.Standing)
}

// standingText is who this message replies to, when the delivery could see it.
func standingText(e *store.Event) any {
	if e == nil || e.Standing == nil || e.Standing.RepliesTo == "" {
		return nil
	}
	return e.Standing.RepliesTo
}
