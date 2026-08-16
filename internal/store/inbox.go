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
