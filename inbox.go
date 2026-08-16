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
}

// inboxReaderRequest names a waiter. It is `as` and not `reader` because that
// is the word on the command line, and one word for one thing across the two
// surfaces is worth more than the prettier noun.
// Delivered says why the mark is being moved: because messages were handed over
// and written out, or because a poll expired having read nothing but this
// principal's own. Both move the same mark, so without it a lost
// acknowledgement and a quiet night are the same row.
type inboxReaderRequest struct {
	As        string `json:"as"`
	Cursor    int64  `json:"cursor"`
	Delivered bool   `json:"delivered"`
}

// inboxFilter is what a waiter has asked to be woken for. Both fields narrow
// what is HANDED OVER and neither narrows what is READ, which is the one thing
// about this that has to be got right - see wakesFor.
type inboxFilter struct {
	room      string
	addressed bool
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
	if want.addressed {
		// isOwnActor asks "is this string this principal", which is the same
		// question of an addressee as it is of an actor: a message to the
		// person and a message to the agent working for them both reach here.
		return isOwnActor(p, e.Addressee)
	}
	return true
}

// handleInboxWait blocks until something this waiter should hear is said.
//
// GET /api/inbox/wait?as=NAME&window=&addressed=&limit=
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
	limit := intParam(q.Get("limit"))
	at, skipped := reader.Cursor, 0
	deliver := []*store.Event{}

	// The poll itself is presence: it is the one signal the node has that does
	// not depend on the room being busy. Marked on the way in and out however
	// the wait ends, so a waiter that is merely blocked on a quiet room still
	// reads as attached.
	s.db.PollStart(r.Context(), p, name)
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
	writeJSON(w, http.StatusOK, inboxWaitResponse{
		Reader: name, Events: deliver, Skipped: skipped, Since: reader.Cursor, Cursor: at,
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
	reader, err := s.db.DeclareInboxReader(r.Context(), p, req.As)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reader)
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

  --as NAME     the waiter's name. Its place in the log is kept on the node
                under this name, so a restart resumes rather than replays
  --deadline S  seconds to wait before giving up, default 28800 (eight hours).
                It is a budget, not a health check: a node that stops answering
                is caught within one poll window whatever this is set to
  --new         declare NAME, starting at the head of the log, and wait
  --to-me       wake only for messages addressed to this principal
  --room R      wake only for messages in one room, default every room
  --url URL     node to ask (default $FLOWY_ADDR, then http://127.0.0.1:8787)
  --token T     bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)

exit codes:
  0  somebody said something; the messages are on stdout, one JSON object per
     line, each carrying the cursor to resume from
  1  the deadline passed and the room was quiet
  2  something is wrong: no token, an unknown --as, the node stopped answering

Only messages go to stdout. The count of what went past, the reminder and every
error go to stderr, so a hook can read stdout as a stream of whole messages.
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
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	as := fs.String("as", "", "the waiter's name, which is what holds its place in the log")
	deadline := fs.Int("deadline", defaultInboxDeadline, "seconds to wait before giving up")
	fresh := fs.Bool("new", false, "declare --as at the head of the log before waiting")
	toMe := fs.Bool("to-me", false, "wake only for messages addressed to this principal")
	room := fs.String("room", "", "wake only for messages in this room")
	urlFlag := fs.String("url", "", "node to talk to (default $FLOWY_ADDR or "+defaultTUIAddr+")")
	token := fs.String("token", "", "bearer token (default $FLOWY_TOKEN, then ~/.config/flowy/token)")
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
	base := resolveURL(*urlFlag, os.Getenv("FLOWY_ADDR"))
	bearer, err := resolveToken(*token, os.Getenv("FLOWY_TOKEN"))
	if err != nil {
		return err
	}
	if bearer == "" {
		return errors.New("no token: pass --token, set FLOWY_TOKEN, or write one to " +
			"~/.config/" + tokenFile)
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

	return waitOnInbox(ctx, client, base, bearer, *as, *room, *toMe, *deadline)
}

// waitOnInbox is the loop: bounded server polls until the deadline, the first
// message ends it, and the mark moves only after what was said is written out.
func waitOnInbox(ctx context.Context, client *http.Client, base, bearer, as, room string,
	toMe bool, deadline int,
) error {
	query := url.Values{}
	query.Set("as", as)
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
			// Only now, and in this order: the messages are on stdout and
			// flushed, so the mark may move past them.
			ackInbox(ctx, client, base, bearer, as, page.Cursor, true)
			reportSkipped(skipped)
			fmt.Fprintf(os.Stderr, "re-arm with: flowy inbox --as %s --deadline %d &\n", as, deadline)
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
func writeInbox(page inboxWaitResponse) error {
	out := bufio.NewWriter(os.Stdout)
	enc := json.NewEncoder(out)
	for _, e := range page.Events {
		if err := enc.Encode(map[string]any{
			"room":      e.Room,
			"actor":     e.Actor,
			"addressee": e.Addressee,
			"body":      e.Body,
			"thread":    e.Thread,
			"id":        e.ID,
			"created":   e.Created,
			"cursor":    page.Cursor,
		}); err != nil {
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
// in-flight poll, and when a poll last started. "Last polled 4s ago" is a
// checkable fact; "online" would be a claim about a process on somebody
// else's machine, and the node does not have it.
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
