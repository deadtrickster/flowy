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
func (d *DB) DeclareInboxReader(ctx context.Context, p *Principal, name string) (*InboxReader, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("store: an inbox reader needs a name")
	}
	head, err := d.inboxHead(ctx, p)
	if err != nil {
		return nil, err
	}
	if _, err := d.sql.ExecContext(ctx,
		`INSERT INTO inbox_readers (principal, reader, read_cursor)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (principal, reader) DO NOTHING`,
		readerKey(p), name, head); err != nil {
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

// PresenceRow is one reader as the room sees it: who holds the label, and what
// the node can honestly say about their attachment. Attached is a poll in
// flight; LastPoll is when a poll last started. Neither is a claim about a
// process on somebody's machine - the node sees the polling, not the listener,
// and the views render exactly this and no more.
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
	LastPoll  *time.Time `json:"last_poll_at"`
	Updated   time.Time  `json:"updated"`
}

// PollStart marks a waiter attaching: the poll is the one signal the server
// has that does not depend on the room being busy.
func (d *DB) PollStart(ctx context.Context, p *Principal, name string) {
	// Swallowed on purpose: presence is observational, and a failed mark must
	// not refuse a waiter its messages. A stale row reads as less attached,
	// which is the safe direction.
	_, _ = d.sql.ExecContext(ctx,
		`UPDATE inbox_readers
		    SET last_poll_at = now(), polls_in_flight = polls_in_flight + 1
		  WHERE principal = $1 AND reader = $2`, readerKey(p), name)
}

// PollEnd marks the poll leaving, however it left.
func (d *DB) PollEnd(ctx context.Context, p *Principal, name string) {
	_, _ = d.sql.ExecContext(ctx,
		`UPDATE inbox_readers
		    SET polls_in_flight = greatest(0, polls_in_flight - 1)
		  WHERE principal = $1 AND reader = $2`, readerKey(p), name)
}

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

// Presence is every reader on the node, with names resolved from the principal
// key's user and agent ids. It is the roster of who has a place in the log and
// what the node last saw of their polling - "who is in the room" is who
// participates, and this table answers "who has an ear on", which is the half
// of that a reader row can carry.
func (d *DB) Presence(ctx context.Context) ([]*PresenceRow, error) {
	// The principal column is user \x1f agent \x1f project. Splitting it in
	// SQL keeps the join in one round trip; unit separators cannot appear in
	// an id, so split_part is exact. Only users carry a handle - see the
	// struct comment for why that is the name here.
	rows, err := d.sql.QueryContext(ctx,
		`SELECT r.principal,
		        split_part(r.principal, chr(31), 3) AS project,
		        r.reader, coalesce(u.handle, ''),
		        r.polls_in_flight > 0, r.last_poll_at, r.updated
		   FROM inbox_readers r
		   LEFT JOIN users u ON u.id = split_part(r.principal, chr(31), 1)
		  ORDER BY r.updated DESC, r.reader`)
	if err != nil {
		return nil, fmt.Errorf("store: presence: %w", err)
	}
	defer rows.Close()

	out := []*PresenceRow{}
	for rows.Next() {
		p := &PresenceRow{}
		if err := rows.Scan(&p.Principal, &p.Project, &p.Reader, &p.UserName,
			&p.Attached, &p.LastPoll, &p.Updated); err != nil {
			return nil, fmt.Errorf("store: presence: %w", err)
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
