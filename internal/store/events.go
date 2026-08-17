package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
)

// likeEscaped makes a search term safe to put between two per-cent signs: a
// caller looking for "100%" or for "a_b" is looking for those characters, not
// for a wildcard they did not know they had typed.
//
// The backslash is LIKE's default escape character on a Postgres-wire store, so
// it needs no ESCAPE clause - and the backslash itself has to be doubled first,
// or escaping the other two would introduce the very wildcard it removes.
func likeEscaped(term string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(term)
}

// eventColumns is the read list, in the order scanEvent expects.
const eventColumns = `id, type, project, room, thread, parents, actor, artifact, seq_hlc,
	node, body, meta, addressee, sig, author_sig, authorship, created`

// scanEvent reads one row of eventColumns.
func scanEvent(sc scanner) (*Event, error) {
	var (
		e                              Event
		typeCol, project, room, thread sql.NullString
		actor, artifact, nodeCol, body sql.NullString
		addressee, authorship          sql.NullString
		meta                           []byte
		seq                            sql.NullInt64
	)
	err := sc.Scan(&e.ID, &typeCol, &project, &room, &thread, pq.Array(&e.Parents), &actor,
		&artifact, &seq, &nodeCol, &body, &meta, &addressee, &e.Sig, &e.AuthorSig, &authorship,
		&e.Created)
	if err != nil {
		return nil, err
	}
	// Whatever the column holds, a reader is told one of the two things this
	// node can actually say - see authorshipOr. A row from a store written
	// before the column existed reads as attributed, which is what it is.
	e.Authorship = authorshipOr(authorship.String)
	if project.Valid {
		p := project.String
		e.Project = &p
	}
	e.Type, e.Room, e.Thread = typeCol.String, room.String, thread.String
	e.Actor, e.Artifact, e.Node, e.Body = actor.String, artifact.String, nodeCol.String, body.String
	e.Addressee = addressee.String
	e.SeqHLC = seq.Int64
	if len(meta) > 0 {
		e.Meta = json.RawMessage(meta)
	}
	if e.Parents == nil {
		e.Parents = []string{}
	}
	// Derived from the row that was just read, in the one place every read of an
	// event goes through, so a surface cannot get it from somewhere else and a
	// peer cannot send it. See Event.Private.
	e.Private = IsDirectMessage(&e)
	return &e, nil
}

// ReadEvent returns one event only if p may read it. An event that is there but
// out of reach comes back as ErrNotFound, exactly like one that is not there -
// the same rule ReadArtifact keeps.
//
// It exists because GetEvent does not ask who wants it, and a caller that looks
// a message up by an id somebody handed it needs the filter: a reply that
// inherits its thread from a message it may not read joins a conversation it
// was never in.
func (d *DB) ReadEvent(ctx context.Context, p *Principal, id string) (*Event, error) {
	a := &args{}
	idArg := a.next(id)
	filter := EventFilterSQL(p, "e", a, false)
	e, err := scanEvent(d.sql.QueryRowContext(ctx,
		`SELECT `+eventColumns+` FROM events e WHERE e.id = `+idArg+` AND `+filter, a.vals...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: read event %s: %w", id, err)
	}
	return e, nil
}

// UnreadableParents returns the ids in parents that do not name an event this
// principal may read - one that is not here at all, and one that is here and out
// of reach, which are the same answer for the same reason every filtered read
// gives it. Duplicates are collapsed and the caller's order is kept, so the
// refusal names the first one the writer wrote.
//
// It is the check both write paths owed the DAG. parents used to be stored
// verbatim: POST /api/events took the whole list on trust, and the chat path
// looked at parents[0] only to decide which thread to inherit, and only when the
// request named no thread. So an event could claim descent from ids that are not
// here, or from a conversation the writer cannot see, and the console's thread
// pane and every reader that walks the DAG afterwards take those edges for
// structure. Nothing leaks - a parent id is echoed only to somebody who can
// already read the event carrying it, and following one is itself a filtered
// read - but an edge nobody checked is an edge that says something untrue about
// what came before what.
//
// One query for the whole list, with the read filter in the WHERE clause: the
// same shape as every other read here, and it does not become a query per parent
// on a long list.
//
// It is asked on the way in, of a client's write. The merge does not ask it: an
// event replicated from a peer legitimately arrives before - or without - the
// events it names, and refusing it there would refuse federation rather than
// forgery.
func (d *DB) UnreadableParents(ctx context.Context, p *Principal, parents []string) ([]string, error) {
	if len(parents) == 0 {
		return nil, nil
	}
	if p == nil {
		return parents, nil
	}
	a := &args{}
	idsArg := a.next(pq.Array(parents))
	filter := EventFilterSQL(p, "e", a, false)
	rows, err := d.sql.QueryContext(ctx,
		`SELECT e.id FROM events e WHERE e.id = ANY(`+idsArg+`) AND `+filter, a.vals...)
	if err != nil {
		return nil, fmt.Errorf("store: read parents: %w", err)
	}
	defer rows.Close()

	readable := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: read parents: %w", err)
		}
		readable[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read parents: %w", err)
	}

	var out []string
	seen := map[string]bool{}
	for _, id := range parents {
		if readable[id] || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// EventQuery narrows a read of the log. Since pages by seq_hlc, which is the
// same cursor peer replication will use: strictly greater, so a caller can hand
// back the last value it saw.
//
// NotActors drops events written by the named actors. It is what an inbox is:
// everything you may see that you did not write yourself. It is a filter in the
// query rather than a loop over the result, so paging by Since and Limit still
// counts the rows the caller actually gets.
type EventQuery struct {
	Thread string
	Room   string
	Type   string
	// Types is any of these types, ORed together and ANDed with the rest. It is
	// what the activity timeline narrows by: a timeline showing turns and
	// steers and nothing else is one read, not one read per kind.
	Types []string
	// Contains is a plain substring of the body, matched case-insensitively.
	//
	// It is deliberately not the full text index: the timeline searches what was
	// said, including a log line that is half punctuation and an id somebody
	// pasted, and to_tsquery drops both. lower(body) LIKE lower(...) means the
	// same thing on any Postgres-wire store, which the index does not.
	Contains string
	// Private narrows to direct messages and nothing else. It is a narrowing
	// like every other field here and never a permission: the filter has already
	// decided which DMs this principal reaches, and this only stops the answer
	// carrying rooms as well. GET /api/dm is what asks for it.
	Private bool
	Since   int64
	// Before is the other end of the log, and only EventsBefore reads it: the
	// newest events STRICTLY BELOW this reading. Zero means the newest events
	// there are, which is where a room opens.
	//
	// It is a separate field from Since rather than a sign on it because the two
	// page in opposite directions and no read wants both: Since walks forward
	// from where a reader got to, Before walks backward from where a reader
	// started, and a query carrying both would have to decide which end its
	// Limit came off.
	Before    int64
	NotActors []string
	ScopeAll  bool
	Limit     int
}

func (q EventQuery) limit() int { return clampLimit(q.Limit) }

// narrow appends the caller's own filters - the ones that are about what they
// asked for rather than what they may see, which is the permission filter this
// is ANDed with and never a substitute for it. The cursor is not in here: it is
// the one clause the two readers below disagree about, because one pages forward
// through the log and the other looks back from the end of it.
func (q EventQuery) narrow(a *args, alias string) string {
	where := ""
	if q.Thread != "" {
		where += " AND " + alias + ".thread = " + a.next(q.Thread)
	}
	if q.Room != "" {
		where += " AND " + alias + ".room = " + a.next(q.Room)
	}
	if q.Type != "" {
		where += " AND " + alias + ".type = " + a.next(q.Type)
	}
	if len(q.Types) > 0 {
		holders := make([]string, 0, len(q.Types))
		for _, t := range q.Types {
			holders = append(holders, a.next(t))
		}
		where += " AND " + alias + ".type IN (" + strings.Join(holders, ", ") + ")"
	}
	if q.Contains != "" {
		where += " AND lower(coalesce(" + alias + ".body, '')) LIKE lower(" +
			a.next("%"+likeEscaped(q.Contains)+"%") + ")"
	}
	if q.Private {
		where += " AND " + privateEventSQL(alias)
	}
	for _, actor := range q.NotActors {
		if actor != "" {
			where += " AND coalesce(" + alias + ".actor, '') <> " + a.next(actor)
		}
	}
	return where
}

// ListEvents returns the events p may read, in log order. The log is
// append-only, so this is the only read it needs: ordering by seq_hlc then id
// is total, and it agrees with the order the events were appended in.
//
// The page never cuts a reading in half - see pageOf - because Since is one
// integer and the order is two columns. Two events written in the same instant
// on two nodes carry the same seq_hlc, and a LIMIT falling between them would
// hand back the first and a cursor that steps over the second for good. What a
// reader hands back is the last event's reading, so every event at that reading
// has to have been in the page.
func (d *DB) ListEvents(ctx context.Context, p *Principal, q EventQuery) ([]*Event, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "events.list")
	defer span.End()
	limit := q.limit()
	return pageOf(ctx, d, "list events", limit,
		func(a *args, tie *tieAt, lim int) string {
			filter := EventFilterSQL(p, "e", a, q.ScopeAll)
			where := q.narrow(a, "e")
			if q.Since > 0 || tie != nil {
				where += " AND " + above("e.seq_hlc", "e.id", q.Since, tie, a)
			}
			return `SELECT ` + eventColumns + `
	            FROM events e
	           WHERE ` + filter + where + `
	           ORDER BY e.seq_hlc, e.id` + limitSQL(a, lim)
		},
		scanEvent,
		func(e *Event) (int64, string) { return e.SeqHLC, e.ID })
}

// CountEvents is how many events matching q this principal may read.
//
// The same filter and the same narrowing as ListEvents, without the page. A
// badge wants a number and a page is not one: above the limit it would report
// the page size, which is a lie that gets more convincing the more there is to
// read - and the count it is wrong about is the count of what somebody has not
// seen yet.
func (d *DB) CountEvents(ctx context.Context, p *Principal, q EventQuery) (int, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "events.count")
	defer span.End()
	a := &args{}
	filter := EventFilterSQL(p, "e", a, q.ScopeAll)
	where := q.narrow(a, "e")
	if q.Since > 0 {
		where += " AND " + above("e.seq_hlc", "e.id", q.Since, nil, a)
	}
	var n int
	if err := d.sql.QueryRowContext(ctx,
		`SELECT count(*) FROM events e WHERE `+filter+where, a.vals...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count events: %w", err)
	}
	return n, nil
}

// RecentEvents is the newest events p may read, newest first.
//
// It is the read a fresh seat does: what happened lately, not everything that
// ever happened. ListEvents cannot answer that with a limit, because it pages
// forward from a cursor - a limit there takes the oldest rows above the cursor,
// which is the opposite end of the log from the one recent means.
//
// It hands back no cursor, deliberately. A descending page cuts at its old end,
// and two events written in the same instant on two nodes carry the same
// seq_hlc, so a cursor taken from the last row of one of these pages would step
// over whatever shared that reading and was left behind - the rows are not late,
// they never arrive. A bounded look-back has no use for one; a caller that wants
// to page the log forwards wants ListEvents, which is built not to cut a reading
// in half.
func (d *DB) RecentEvents(ctx context.Context, p *Principal, q EventQuery) ([]*Event, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "events.recent")
	defer span.End()
	return readPage(ctx, d, "recent events", func(a *args) string {
		filter := EventFilterSQL(p, "e", a, q.ScopeAll)
		where := q.narrow(a, "e")
		if q.Since > 0 {
			where += " AND e.seq_hlc > " + a.next(q.Since)
		}
		return `SELECT ` + eventColumns + `
	            FROM events e
	           WHERE ` + filter + where + `
	           ORDER BY e.seq_hlc DESC, e.id DESC` + limitSQL(a, q.limit())
	}, scanEvent)
}

// EventsBefore is a window of the log taken from its NEW end, handed back in log
// order: the newest Limit events p may read that are strictly below q.Before, or
// simply the newest ones there are when Before is zero.
//
// It exists because neither read above can open a room on its last screenful.
// ListEvents pages forward, so a limit there takes the OLDEST rows - opening a
// busy room on its first day and dragging the rest in behind the long poll,
// which is the whole complaint this answers. RecentEvents takes the right end
// but is a look-back and nothing more: it hands back no cursor, so there is no
// way to ask for the screenful before the one you got.
//
// THE CURSOR IS THE POINT, AND IT IS THE OLD END THAT HAS TO BE HONEST. A
// descending page cuts where its limit runs out, and that cut is at the OLD end;
// two events written in the same instant on two nodes carry the same seq_hlc, so
// a page that stopped between them and handed its last row's reading back would
// have the caller ask for "strictly below that reading" and step over whatever
// shared it. Those rows are not late, they never arrive, and nothing says so.
//
// So a page that filled its limit is followed by the REST OF THE READING it
// stopped in - every row at that seq_hlc sorting below the last id it took. The
// rows above that id at the same reading were already taken, because the order
// is descending, so the page ends on a complete reading and `< seq_hlc` is then
// exact in both directions: no message repeats and none is skipped. That is
// pageOf's rule, run off the other end of the log.
func (d *DB) EventsBefore(ctx context.Context, p *Principal, q EventQuery) ([]*Event, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "events.before")
	defer span.End()
	limit := q.limit()

	read := func(tie *tieAt, lim int) ([]*Event, error) {
		return readPage(ctx, d, "events before", func(a *args) string {
			filter := EventFilterSQL(p, "e", a, q.ScopeAll)
			where := q.narrow(a, "e")
			if q.Before > 0 || tie != nil {
				where += " AND " + below("e.seq_hlc", "e.id", q.Before, tie, a)
			}
			return `SELECT ` + eventColumns + `
	            FROM events e
	           WHERE ` + filter + where + `
	           ORDER BY e.seq_hlc DESC, e.id DESC` + limitSQL(a, lim)
		}, scanEvent)
	}

	page, err := read(nil, limit)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(page) == limit {
		last := page[len(page)-1]
		rest, err := read(&tieAt{hlc: last.SeqHLC, id: last.ID}, 0)
		if err != nil {
			return nil, err
		}
		page = append(page, rest...)
	}
	// Into log order. The read has to be descending to take the right end of the
	// log, but what a caller does with this is draw a transcript or prepend to
	// one, and both want oldest first - the same order every other read hands
	// back, so a view never has to know which read filled it.
	for i, j := 0, len(page)-1; i < j; i, j = i+1, j-1 {
		page[i], page[j] = page[j], page[i]
	}
	return page, nil
}
