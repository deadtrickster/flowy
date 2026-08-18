package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// The waiter's side of the inbox: where a thing that blocks on the log got to.
//
// A room read pages by a cursor the caller holds, which is right for a console
// that is looking at a room - the position is a property of the view. A waiter
// is the other case: it blocks, returns once, exits, and is restarted, so its
// position has to outlive the process. Every harness that solved that with a
// file beside the client reread what it had already answered the first time two
// of them ran, or the first time one was started from a different directory. A
// position in a shared log belongs to the log.
//
// Nothing here is a permission and nothing here is replicated. A reader row
// says where somebody had got to; what they may see when they get there is
// EventFilterSQL, exactly as it is for every other read of the log.

// InboxReader is one waiter's position: a label, the last reading it has
// acknowledged, and why the mark last moved.
//
// Delivered and Quiet are that last part, and they are here because the mark
// alone cannot answer the question anybody asks of it. The mark advances both
// when messages were handed over and when a poll expired having read nothing
// but the reader's own messages, so a lost acknowledgement and a quiet night
// leave the same row behind - which is precisely the ambiguity somebody hits
// while trying to work out why a message never arrived.
type InboxReader struct {
	Reader    string    `json:"reader"`
	Cursor    int64     `json:"cursor"`
	Delivered int64     `json:"acked_delivery"`
	Quiet     int64     `json:"acked_quiet"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// ErrNoReader is an inbox label this principal has not declared. It is a
// distinct error rather than an empty answer because that is the whole of what
// the explicit declaration buys: an unknown label that quietly became a new
// reader starting from now would be an inbox that is permanently empty, never
// says why, and reads exactly like a quiet room.
var ErrNoReader = errors.New("store: no such inbox reader")

// readerKey is the principal a reader row belongs to: the whole triple, joined
// the way sync_pending joins it, because both tables are answering the same
// question - which reader is this row about - and one encoding of a principal
// is worth more than two.
//
// The whole triple and not just the user, because the same person in two
// projects is two principals reading two different slices of the log - see the
// Identity section of the README - and one cursor across both would hand each
// of them a position the other moved.
func readerKey(p *Principal) string {
	if p == nil {
		return pendingKey(&Principal{})
	}
	return pendingKey(p)
}

// InboxReaders is every waiter label this principal has declared, oldest first.
// It is what a refusal lists, so somebody who mistyped a label is shown the
// ones that exist rather than left to guess.
func (d *DB) InboxReaders(ctx context.Context, p *Principal) ([]*InboxReader, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT reader, read_cursor, acked_delivery, acked_quiet, created, updated
		   FROM inbox_readers
		  WHERE principal = $1
		  ORDER BY created, reader`, readerKey(p))
	if err != nil {
		return nil, fmt.Errorf("store: list inbox readers: %w", err)
	}
	defer rows.Close()

	out := []*InboxReader{}
	for rows.Next() {
		var r InboxReader
		if err := rows.Scan(&r.Reader, &r.Cursor, &r.Delivered, &r.Quiet,
			&r.Created, &r.Updated); err != nil {
			return nil, fmt.Errorf("store: list inbox readers: %w", err)
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list inbox readers: %w", err)
	}
	return out, nil
}

// InboxReaderAt is one label's position, or ErrNoReader.
func (d *DB) InboxReaderAt(ctx context.Context, p *Principal, name string) (*InboxReader, error) {
	var r InboxReader
	err := d.sql.QueryRowContext(ctx,
		`SELECT reader, read_cursor, acked_delivery, acked_quiet, created, updated
		   FROM inbox_readers
		  WHERE principal = $1 AND reader = $2`,
		readerKey(p), name).Scan(&r.Reader, &r.Cursor, &r.Delivered, &r.Quiet,
		&r.Created, &r.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoReader
	}
	if err != nil {
		return nil, fmt.Errorf("store: read inbox reader %s: %w", name, err)
	}
	return &r, nil
}

// DeclareInboxReader creates a label at the head of what this principal can
// already read, and answers with the row as it stands when it is already there.
//
// The head, and not zero, is where a new waiter starts. A waiter is armed to
// hear what happens next; starting one at the beginning of the log would hand
// it every message ever said in every room it can see, as though all of it had
// just arrived, and the first thing anybody would do about that is throw the
// batch away - which is a cursor file with extra steps.
//
// The head is the principal's own: the highest reading among the chat events
// the permission filter lets them read. It is a filtered read like any other,
// so declaring a reader tells its caller nothing a read of the inbox would not.
// kind is what the label IS, and it is asked here because it can only be
// answered here: a cursor and a waiter that has not polled yet are the same row
// afterwards, so a roster reading the row later cannot tell them apart. Anything
// that is not one of the claims is unknown, exactly as on a poll.
func (d *DB) DeclareInboxReader(ctx context.Context, p *Principal, name, kind string) (*InboxReader, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("store: an inbox reader needs a name")
	}
	head, err := d.inboxHead(ctx, p)
	if err != nil {
		return nil, err
	}
	if _, err := d.sql.ExecContext(ctx,
		`INSERT INTO inbox_readers (principal, reader, read_cursor, waiter_kind)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (principal, reader) DO NOTHING`,
		readerKey(p), name, head, WaiterKindOf(kind)); err != nil {
		return nil, fmt.Errorf("store: declare inbox reader %s: %w", name, err)
	}
	return d.InboxReaderAt(ctx, p, name)
}

// AckInbox moves a reader's mark, and only ever forwards. delivered says why -
// because messages were handed over, or because a poll expired quietly and the
// mark still had to pass what it read.
//
// Forwards only because the mark is what stops a waiter answering the same
// message twice, and a client that hands back a stale cursor - a slow process
// that woke up after a faster one, a retried request - would otherwise reopen
// everything between. A caller that genuinely wants to re-read a room reads the
// room; that is what GET /api/chat/{room} is.
//
// The counters move with the mark and not beside it: an acknowledgement that
// moves nothing is a duplicate of one already recorded, and counting it would
// make a retry look like traffic.
func (d *DB) AckInbox(ctx context.Context, p *Principal, name string, cursor int64,
	delivered bool,
) (*InboxReader, error) {
	onDelivery, onQuiet := 0, 1
	if delivered {
		onDelivery, onQuiet = 1, 0
	}
	if _, err := d.sql.ExecContext(ctx,
		`UPDATE inbox_readers
		    SET read_cursor = $3, updated = now(),
		        acked_delivery = acked_delivery + $4,
		        acked_quiet    = acked_quiet + $5
		  WHERE principal = $1 AND reader = $2 AND read_cursor < $3`,
		readerKey(p), name, cursor, onDelivery, onQuiet); err != nil {
		return nil, fmt.Errorf("store: ack inbox reader %s: %w", name, err)
	}
	// The row count is not the answer here. No rows updated means either the
	// label is not there or the mark was already past this reading, and those
	// are a refusal and a successful no-op respectively. Reading the row back
	// tells them apart and answers with where the mark actually is.
	return d.InboxReaderAt(ctx, p, name)
}

// inboxHead is the highest reading among the chat events this principal may
// read: where a waiter declared now would start.
func (d *DB) inboxHead(ctx context.Context, p *Principal) (int64, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "inbox.head")
	defer span.End()
	a := &args{}
	typeArg := a.next(ChatEventType)
	filter := EventFilterSQL(p, "e", a, false)
	var head sql.NullInt64
	err := d.sql.QueryRowContext(ctx,
		`SELECT max(e.seq_hlc) FROM events e WHERE e.type = `+typeArg+` AND `+filter,
		a.vals...).Scan(&head)
	if err != nil {
		return 0, fmt.Errorf("store: inbox head: %w", err)
	}
	return head.Int64, nil
}

// The three things a listener can be. They are about what happens when the
// room says something, not about how attached the listener looks.
//
// WaiterTracked is a waiter some harness is watching: it exits on a delivery
// and that exit wakes somebody. WaiterForked is the detached successor a waiter
// leaves behind so the room stays heard while the agent reads - it hears
// everything and CAN WAKE NOBODY, because there is no task to finish.
// WaiterUnknown is a row nothing has claimed: written before this field
// existed, or polled by a client that does not send one.
//
// Unknown is emphatically not tracked. The whole cost of this distinction was
// paid by reading absence as the good case: presence answered "is somebody
// polling" for 28 minutes while the question was "can anybody be woken".
//
// WaiterCursor is the fourth and it is not a waiter at all: a label somebody
// keeps a POSITION under without ever blocking on it. The console holds one
// per room to draw its unread badges. It cannot be woken, it is not starting
// up, and it never polls - so on a roster built out of polls it sat forever in
// the one state that means "any moment now". Three of them were half the
// listening pane. A cursor says so at declare, because nothing about the row
// afterwards distinguishes it from a waiter that has not got going yet.
const (
	WaiterTracked = "tracked"
	WaiterForked  = "forked"
	WaiterCursor  = "cursor"
	WaiterUnknown = "unknown"
)

// WaiterKindOf is the only way a kind gets into a row. It exists because the
// value arrives on a query parameter, so without it the roster would render
// whatever a client typed - and a fourth word is a state nothing knows how to
// draw. Anything that is not one of the two claims is unknown.
func WaiterKindOf(s string) string {
	switch strings.TrimSpace(s) {
	case WaiterTracked:
		return WaiterTracked
	case WaiterForked:
		return WaiterForked
	case WaiterCursor:
		return WaiterCursor
	}
	return WaiterUnknown
}

// The three things a row on the roster can be. They answer "what is this seat
// doing", which is a different question from Kind's "what could it do if it
// heard something", and both are needed: a tracked waiter that stopped polling
// six hours ago is still tracked and is not listening to anything.
//
// PresenceListening is a reader whose last poll is inside PresenceWindow -
// polling now, or between two polls of a waiter that is coming straight back.
//
// PresenceStarting is a reader that EXISTS and has never polled, young enough
// that a waiter arming itself is the honest reading. The roster is where
// somebody watches a waiter start, so this is a real state and not a ghost.
//
// PresenceLost is the one this fleet keeps paying for: the node is holding a
// poll it never saw end, and the poll is older than any poll can be. Something
// armed a waiter here and it stopped. It is NOT attached and it is NOT dropped
// from the roster - see PresenceLostWindow for why saying so out loud is the
// whole point.
const (
	PresenceListening = "listening"
	PresenceStarting  = "starting"
	PresenceLost      = "lost"
)

// PresenceRow is one reader as the room sees it: who holds the label, and what
// the node can honestly say about their attachment. Attached is a poll in
// flight; LastPoll is when a poll last started. Neither is a claim about a
// process on somebody's machine - the node sees the polling, not the listener,
// and the views render exactly this and no more.
//
// Kind is the half those two cannot carry: WHAT the thing that polled can do
// about what it hears. A forked listener is attached, polling, seconds fresh
// and unable to wake anybody, so a roster drawn from Attached and LastPoll
// alone reports it healthy - which it did for 28 minutes, while the person who
// had written into the room got silence.
//
// State is the half ATTACHED cannot carry, and it is the reason Attached is no
// longer the raw column. polls_in_flight is a counter that only comes down when
// a handler returns, so a node restarted mid-poll - or a decrement that ran on
// an already-cancelled request context - leaves it up forever, and the row then
// reads attached for as long as the table lives. Two of those were sitting on
// this node's roster claiming to be attached, one for six hours and one for
// thirty, while the operator asked twice why an agent was not answering. So
// Attached now means "a poll is in flight AND it started recently enough to
// still be one", and State says which of the three things a row that fails that
// test actually is.
//
// The name is the user's handle - an agent has no handle of its own and speaks
// under that person's, which is the rule the chat already renders by - and the
// reader label is what the listener chose to be called, which for an agent is
// the name worth showing.
type PresenceRow struct {
	Principal string     `json:"principal"`
	Project   string     `json:"project"`
	Reader    string     `json:"reader"`
	UserName  string     `json:"user_name"`
	Attached  bool       `json:"attached"`
	Kind      string     `json:"waiter_kind"`
	State     string     `json:"state"`
	LastPoll  *time.Time `json:"last_poll_at"`
	Updated   time.Time  `json:"updated"`
}

// PollStart marks a waiter attaching: the poll is the one signal the server
// has that does not depend on the room being busy. kind is what the poller says
// it is, and the last poll decides - a listener that stops saying is not still
// the listener that used to say so.
//
// PollEnd deliberately leaves the kind alone, so the row keeps answering "what
// was listening here" between polls rather than lapsing to unknown in every
// gap - which would make the roster flicker through a state that means "nobody
// knows" while somebody was perfectly well armed.
func (d *DB) PollStart(ctx context.Context, p *Principal, name, kind string) {
	// Swallowed on purpose: presence is observational, and a failed mark must
	// not refuse a waiter its messages. A stale row reads as less attached,
	// which is the safe direction.
	_, _ = d.sql.ExecContext(ctx,
		`UPDATE inbox_readers
		    SET last_poll_at = now(), polls_in_flight = polls_in_flight + 1,
		        waiter_kind = $3
		  WHERE principal = $1 AND reader = $2`, readerKey(p), name, WaiterKindOf(kind))
}

// PollEnd marks the poll leaving, however it left.
//
// ON A CONTEXT THAT OUTLIVES THE REQUEST, which is not a detail: the caller is
// a deferred call in the /api/inbox/wait handler, and the commonest way for a
// poll to end is the CLIENT GOING AWAY - the waiter is killed, the session is
// torn down, the socket drops. That cancels the request context, and a decrement
// issued on a cancelled context never reaches the database. The error was
// swallowed here, so the row kept polls_in_flight up with nobody on the other
// end and read as attached forever after. That is the six-hour ghost the roster
// has been showing: not a listener that stopped being noticed, a decrement that
// was never allowed to run.
//
// The timeout is what keeps "detached from the request" from meaning "may block
// a shutting-down server indefinitely". Still swallowed: presence is
// observational, and a failed mark must not be able to affect a waiter that has
// already been served - and Presence now judges an in-flight poll by its age, so
// a decrement lost anyway costs a row that reads lost rather than one that reads
// attached forever.
func (d *DB) PollEnd(ctx context.Context, p *Principal, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pollEndTimeout)
	defer cancel()
	_, _ = d.sql.ExecContext(ctx,
		`UPDATE inbox_readers
		    SET polls_in_flight = greatest(0, polls_in_flight - 1)
		  WHERE principal = $1 AND reader = $2`, readerKey(p), name)
}

// pollEndTimeout bounds the detached decrement above. One statement on a primary
// key, so seconds is generous; it exists so a request that has already ended
// cannot hold a connection for as long as the database is willing to wait.
const pollEndTimeout = 5 * time.Second

// DeleteInboxReader drops a reader label outright. A reader row is not a
// listener - it exists whether or not anything is attached - so a test label
// or a retired name lives forever in every roster unless it can be deleted.
// The WHERE scopes to the caller's own principal: nobody deletes somebody
// else's place in the log.
func (d *DB) DeleteInboxReader(ctx context.Context, p *Principal, name string) (bool, error) {
	res, err := d.sql.ExecContext(ctx,
		`DELETE FROM inbox_readers WHERE principal = $1 AND reader = $2`,
		readerKey(p), name)
	if err != nil {
		return false, fmt.Errorf("store: delete inbox reader %s: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: delete inbox reader %s: %w", name, err)
	}
	return n > 0, nil
}

// PresenceWindow is how long after its last poll a reader is still a LISTENER.
//
// Every row in inbox_readers is a CURSOR - the console keeps one per room, a
// probe keeps one, a one-off test keeps one - and only some of them are somebody
// listening. Presence used to return all of them, so the roster filled with
// console panes, dead probes and duplicate names, and the operator called it an
// infestation twice. Thirteen rows for five agents.
//
// The rows are not the problem and MUST NOT BE DELETED: a console cursor is what
// makes the unread badge clear, and dropping it would recount every message the
// next time somebody opened the room. So this narrows the READ, and the table
// keeps everything.
//
// Ten minutes because a waiter polls on a 25-second server window and re-polls
// promptly when it returns, whatever its total deadline is and however quiet the
// room is - a quiet waiter still polls, so silence for ten minutes is silence
// from something that is not coming back within the next one. It is twenty-four
// re-polls of slack, which is enough that a slow node, a retry and a restart all
// pass through without anybody being called dead who is not.
const PresenceWindow = 10 * time.Minute

// PresenceLostWindow is how long a reader that stopped MID-POLL stays on the
// roster saying so.
//
// The complaint this answers is not that the roster was too long. It is that
// claude-glm had not polled in six hours, the row said attached, and NOTHING ON
// ANY SURFACE SAID THE SEAT WAS DEAF - the operator asked twice why the agent
// was not answering. Dropping the row at PresenceWindow would have tidied the
// panel and destroyed the only evidence there was. A reader who knows a seat
// stopped answering six hours ago can go and restart it; a reader looking at a
// short clean list learns nothing and asks a third time.
//
// So a row holding an unfinished poll is kept and rendered as PresenceLost, and
// this is how long that is worth saying. Eight hours because that is the
// waiter's own default deadline - past its own budget a waiter would have gone
// home anyway, so an unfinished poll older than that is no longer evidence of
// anything going wrong, just a row nobody cleaned up. It is checked against the
// waiter's constant in TestPresenceLostWindowFollowsTheWaitersDeadline so the
// two cannot drift apart.
//
// The split falls exactly where the two rows on this node fall: claude-glm at
// six hours is the incident and is named; ho-test at thirty is a dead test label
// and drops off.
const PresenceLostWindow = 8 * time.Hour

// Presence is the roster: every reader the node can say something useful about
// right now, with names resolved from the principal key's user and agent ids.
// "Who is in the room" is who participates; this answers "who has an ear on",
// which is the half a reader row can carry - and, since a row can hold a poll
// that never ended, "who had an ear on and stopped", which is the half that had
// no answer at all.
func (d *DB) Presence(ctx context.Context) ([]*PresenceRow, error) {
	// The principal column is user \x1f agent \x1f project. Splitting it in
	// SQL keeps the join in one round trip; unit separators cannot appear in
	// an id, so split_part is exact. Only users carry a handle - see the
	// struct comment for why that is the name here.
	rows, err := d.sql.QueryContext(ctx,
		`SELECT r.principal,
		        split_part(r.principal, chr(31), 3) AS project,
		        r.reader, coalesce(u.handle, ''),
		        r.polls_in_flight > 0, r.waiter_kind, r.last_poll_at, r.updated,
		        -- THE DATABASE'S CLOCK, ONCE, FOR EVERY ROW. The states below
		        -- are ages, and an age measured by a Go clock against timestamps
		        -- written by the database is wrong by the skew in both
		        -- directions: behind, and a live waiter reads lost; ahead, and a
		        -- dead one reads listening. Same rule the landing lock arrived
		        -- at - one clock stamps the window and judges it.
		        now()
		   FROM inbox_readers r
		   LEFT JOIN users u ON u.id = split_part(r.principal, chr(31), 1)
		     -- Polled inside the window: listening, whether or not a poll is in
		     -- flight at this instant. A waiter between two polls is not gone.
		  WHERE r.last_poll_at > now() - $1::interval
		     -- Holding a poll that never ended, and older than any poll can be.
		     -- This clause is a LONGER window on purpose and it used to be no
		     -- window at all: polls_in_flight > 0 alone let a leaked counter
		     -- keep a row on the roster forever, attached, six hours after its
		     -- listener stopped. It is kept - not dropped - so that the seat can
		     -- be named as deaf rather than quietly disappearing. See
		     -- PresenceLostWindow.
		     OR (r.polls_in_flight > 0 AND r.last_poll_at > now() - $2::interval)
		     -- A reader that EXISTS AND HAS NEVER POLLED is a waiter starting
		     -- up, and the roster is how somebody watches it start - it reads
		     -- as kind unknown, which is a real answer and not a ghost. The
		     -- first version of this dropped it with the console cursors and
		     -- broke four checks that encode exactly that contract.
		     --
		     -- The difference between the two is AGE, not the missing poll:
		     -- never-polled-and-seconds-old is starting, never-polled-and-hours-
		     -- old is a cursor somebody's page left behind. Same empty field,
		     -- two different facts, and the first cut used the field.
		     --
		     -- CREATED, not updated. The updated column moves on every ack, and the
		     -- console acks a cursor per room every time somebody reads it - so
		     -- judging by it made three browser bookmarks permanent residents of
		     -- the listening pane, refreshed by the very act of looking at the
		     -- page they were cluttering. A row's age as a candidate listener is
		     -- how long ago it was declared.
		     --
		     -- AND A CURSOR IS NOT A WAITER STARTING UP. A label declared as a
		     -- cursor never polls by design - the console keeps one per room to
		     -- draw its unread badges - so "starting" was a state it could
		     -- never leave, and three of them were half this roster. It is
		     -- excluded here rather than filtered by the view because the
		     -- question this query answers is who has an ear on, and a cursor
		     -- has none.
		     OR (r.last_poll_at IS NULL AND r.created > now() - $1::interval
		         AND coalesce(r.waiter_kind, '') <> 'cursor')
		  ORDER BY r.updated DESC, r.reader`,
		PresenceWindow.String(), PresenceLostWindow.String())
	if err != nil {
		return nil, fmt.Errorf("store: presence: %w", err)
	}
	defer rows.Close()

	out := []*PresenceRow{}
	for rows.Next() {
		p := &PresenceRow{}
		var holdsPoll bool
		var now time.Time
		if err := rows.Scan(&p.Principal, &p.Project, &p.Reader, &p.UserName,
			&holdsPoll, &p.Kind, &p.LastPoll, &p.Updated, &now); err != nil {
			return nil, fmt.Errorf("store: presence: %w", err)
		}
		// Read through the same funnel it was written through, so a row from
		// a database that predates the column - or one somebody edited by hand
		// - reaches the roster as unknown rather than as an empty string the
		// view has no case for.
		p.Kind = WaiterKindOf(p.Kind)

		// The three states, from the two facts the row has. A poll counts as in
		// flight only while it is young enough to be one: the server blocks for
		// at most 25 seconds and a waiter comes straight back, so a poll that
		// started before the window is not slow, it is abandoned - and the
		// counter that says otherwise is a decrement that never ran.
		fresh := p.LastPoll != nil && now.Sub(*p.LastPoll) < PresenceWindow
		switch {
		case fresh:
			p.State = PresenceListening
			p.Attached = holdsPoll
		case p.LastPoll == nil:
			p.State = PresenceStarting
			p.Attached = false
		default:
			// Polled once and then stopped. The WHERE only lets one kind of
			// this through - the row still holding a poll - but the reading
			// does not depend on that clause staying exactly as it is: a
			// reader whose last poll is older than the window stopped
			// listening, and that is the word for it either way.
			p.State = PresenceLost
			p.Attached = false
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: presence: %w", err)
	}
	return out, nil
}

// RoomMember is one participant of the rooms this principal may read: somebody
// who has spoken, named as the names feature knows them.
type RoomMember struct {
	Actor string `json:"actor"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
}

// RoomMembers is who is in the room: every distinct actor of a chat event the
// principal may read, newest speaker first. Presence answers who has an ear
// on; this answers who has ever spoken - the two rosters a room view wants,
// and neither implies the other.
func (d *DB) RoomMembers(ctx context.Context, p *Principal) ([]*RoomMember, error) {
	a := &args{}
	typeArg := a.next(ChatEventType)
	filter := EventFilterSQL(p, "e", a, false)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT DISTINCT ON (e.actor) e.actor,
		        coalesce(
		          -- The name a speaker chose beats the registry's fallbacks: it
		          -- is what every message they sent is rendered under, and the
		          -- roster should not disagree with the transcript beside it.
		          (SELECT e2.meta->>'actor_name' FROM events e2
		            WHERE e2.actor = e.actor AND e2.meta->>'actor_name' IS NOT NULL
		            ORDER BY e2.seq_hlc DESC LIMIT 1),
		          u.handle, '') AS name,
		        CASE WHEN a2.id IS NOT NULL THEN 'agent' ELSE 'user' END AS kind,
		        max(e.seq_hlc) AS last
		   FROM events e
		   LEFT JOIN agents a2 ON a2.id = e.actor
		   LEFT JOIN users u  ON u.id = e.actor
		  WHERE e.type = `+typeArg+` AND `+filter+`
		  GROUP BY e.actor, u.handle, a2.id
		  ORDER BY e.actor, last DESC`, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: room members: %w", err)
	}
	defer rows.Close()

	out := []*RoomMember{}
	for rows.Next() {
		m := &RoomMember{}
		var last int64
		if err := rows.Scan(&m.Actor, &m.Name, &m.Kind, &last); err != nil {
			return nil, fmt.Errorf("store: room members: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: room members: %w", err)
	}
	return out, nil
}
