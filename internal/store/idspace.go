package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/deadtrickster/flowy/internal/otel"
)

// Id spaces, and how a lookup that missed says which one the caller was holding.
//
// Artifacts, events and threads are all named by ULIDs. They are the same shape
// - twenty-six characters, minted from the same clock - so two ids from two of
// these spaces that were minted milliseconds apart share a long prefix and are
// told apart only by their last character or two. Nothing about a bare ULID
// says which space it came from, which means a caller that copied one out of a
// message and pasted it into a row lookup gets 404, and 404 reads as "there is
// no such row" when the truth is "that id is not a row id at all".
//
// That difference matters because the two truths call for opposite next moves.
// "There is no such row" sends a reader looking for something deleted or out of
// reach. "That is a thread id" sends them to the row the thread raised, which
// is the row they wanted and which is sitting right there. On 2026-08-18 two
// agents spent the minutes between them on the first reading of a 404 that was
// really the second.

// The id spaces a lookup by artifact id can land in by mistake. Both are read
// out of the events table: an event id names one message, an event's thread
// names the conversation it sits in.
const (
	IDSpaceMessage = "message"
	IDSpaceThread  = "thread"
)

// MisreadID is what an id turned out to be when it did not name an artifact:
// which space it came from, and the artifact that message or thread is about
// when there is one to name.
//
// Artifact is empty when the message or thread names no row - an ordinary
// remark in a room is a real message about nothing in particular. The space is
// still worth saying: "that is a message id" is already the whole of what the
// caller got wrong.
type MisreadID struct {
	Space    string `json:"space"`
	Artifact string `json:"artifact,omitempty"`
}

// MisreadArtifactID answers what the caller was holding when a lookup by
// artifact id found nothing.
//
// Every read here runs the principal's own filter, the same one the failed
// lookup ran, so this adds no way to learn that something exists. A reader who
// is told "that id names a chat thread" could have read that thread through
// GET /api/chat and learned the same thing; a reader who could not read it is
// told nothing at all, and gets the bare 404 they got before. The answer is a
// fact about the caller's own hand, not about the store's contents.
//
// A nil answer and a nil error mean the id names nothing this principal can
// reach in either space, which is the ordinary case and the one where the
// original 404 was already saying the right thing.
func (d *DB) MisreadArtifactID(ctx context.Context, p *Principal, id string) (*MisreadID, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "id.misread")
	defer span.End()
	id = strings.TrimSpace(id)
	if id == "" || p == nil {
		return nil, nil
	}
	// A message first, because an event id is the more specific claim: a row in
	// the events table carrying this id is a message the caller can point at,
	// where a thread is only the name a set of messages share.
	e, err := d.ReadEvent(ctx, p, id)
	switch {
	case err == nil:
		if e.Artifact != "" {
			return &MisreadID{Space: IDSpaceMessage, Artifact: e.Artifact}, nil
		}
		// A message that names no row of its own, and is the head of its own
		// thread - which is what an event written with no thread becomes, see
		// appendEvent - is BOTH a message id and a thread id. So the thread it
		// heads is asked what it is about, and the caller is still told they are
		// holding a message, because that is the more specific of the two true
		// things and the one that names what they copied.
		//
		// A message in the middle of somebody else's conversation is not asked:
		// the row raised further down a room thread is not the row that message
		// is about, and offering it would be a guess dressed as an answer.
		if e.Thread == "" || e.Thread == id {
			if thread, tErr := d.misreadThread(ctx, p, id); tErr == nil && thread != nil {
				return &MisreadID{Space: IDSpaceMessage, Artifact: thread.Artifact}, nil
			}
		}
		return &MisreadID{Space: IDSpaceMessage}, nil
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}
	return d.misreadThread(ctx, p, id)
}

// misreadThread asks whether the id names a conversation, and which row that
// conversation is about.
//
// The row it names is the FIRST one any message in the thread named, in log
// order. A todo raised out of a room posts its "raised a todo" message as the
// message that names the new row, so in the case this whole path exists for the
// first is the one the reader meant. A later message naming a second row is a
// different conversation about a different thing, and answering with that one
// would be handing back a row the reader never heard of.
func (d *DB) misreadThread(ctx context.Context, p *Principal, id string) (*MisreadID, error) {
	a := &args{}
	idArg := a.next(id)
	filter := EventFilterSQL(p, "e", a, false)
	// coalesce and not `IS NULL`: a message about nothing is stored with an
	// EMPTY artifact rather than a null one - appendEvent's nullif covers the
	// addressee column and not this one - so a null test would sort every row in
	// the thread equal and hand back whichever was written first, which in a room
	// is the remark somebody made before the todo was raised.
	var artifact sql.NullString
	err := d.sql.QueryRowContext(ctx,
		`SELECT e.artifact FROM events e
	      WHERE e.thread = `+idArg+` AND `+filter+`
	   ORDER BY (coalesce(e.artifact, '') = ''), e.seq_hlc, e.id
	      LIMIT 1`, a.vals...).Scan(&artifact)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: misread thread %s: %w", id, err)
	}
	// The ORDER BY puts every message that names a row ahead of every message
	// that does not, so one row answers both questions at once: that the thread
	// is there and readable, and what it is about. A thread whose messages all
	// name nothing still comes back - as a thread with no artifact - because
	// "that id is a thread" is the correction the caller needs either way.
	return &MisreadID{Space: IDSpaceThread, Artifact: artifact.String}, nil
}
