package store

// WHERE A ROW CAME FROM, which is not what blocks it.
//
// The diagram row (01M08N148B) said "a diagram that starts a task and a diagram
// that results from one are both dependency edges ... dep.add between a diagram
// and a todo needs nothing added". deps.go says the opposite and gives the
// better reason, so the code won and this file is what the row actually wanted:
//
//	Both ends have to be queue items because DEPENDS-ON is an ordering ON THE
//	QUEUE. An edge onto something the ready query never reads is a dependency
//	that silently does nothing, which is worse than a refusal.
//
// It is worse than silent for a diagram specifically. A diagram never becomes
// done, so a todo blocked by one would never be ready - the queue would stop on
// a row that cannot move, and the name on the edge would promise ordering that
// nothing delivers.
//
// So this is a RELATION, not an ordering: X came out of Y. Nothing computes
// readiness from it, nothing waits on it, and its verb says so - `origin.add`
// rather than `dep.add` - because the failure the ruling (01M0ANFYWY) names is
// an edge the ready query never reads sharing a name with one it does.
//
// WHAT IS BORROWED FROM deps.go, deliberately, rather than invented:
//
//   - the entry IS the edge. Both types are minted, so the only way to get one
//     is through the verb, which is where the refusals are; removal appends the
//     entry that takes it back rather than deleting anything.
//   - the event lands on the row that HAS an origin, never on the origin. That
//     column decides who reads the edge, and the reader who needs it is whoever
//     holds the thing that came from somewhere.
//   - the other end is a BARE ID in the meta. This event is readable by
//     everybody who can read the dependent, which by design includes principals
//     who cannot read the origin, so a title or a project copied in here would
//     be a leak across the boundary the id exists to respect. Telling a reader
//     "this came out of something you cannot see" is the honest answer.
//
// WHAT IS DIFFERENT, and why:
//
//   - EITHER END MAY BE ANY ARTIFACT this principal can read. That is the whole
//     point: a todo can come out of a diagram, a report out of a finding, a
//     diagram out of a todo. readWorkItem's gate is about the queue, and this
//     is not the queue.
//   - CYCLES ARE NOT REFUSED. deps.go walks for one because a loop in an
//     ordering is a queue that stops; nothing walks this graph, so a loop here
//     costs a reader a puzzled moment and nothing else. Refusing it would mean
//     a walk over every read, paid for a defect that has no victim.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/otel"
)

// The two entries a relation leaves in the log. Both are minted - see
// mintedEventTypes - so the only way to get one is to have gone through the
// verb below.
const (
	EventOriginAdd    = "origin.add"
	EventOriginRemove = "origin.remove"
)

// OriginField is the meta key holding the other end: the id of the thing this
// row came out of. An id and nothing else, for the reason at the head of this
// file.
const OriginField = "origin"

// OriginRoom is where an entry lands when the row it is about names no room, so
// that provenance is somewhere a reader can find rather than in the roomless
// part of the log.
const OriginRoom = "origins"

// OriginEntry is one entry in the log, as a reader sees it.
type OriginEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Artifact  string `json:"artifact"`
	Origin    string `json:"origin"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	Created   string `json:"created"`
}

// OriginEntryOf reads one entry off the event that carries it.
func OriginEntryOf(e *Event) OriginEntry {
	entry := OriginEntry{
		ID: e.ID, Type: e.Type, Artifact: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		Created: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]string
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.Origin = meta[OriginField]
		entry.ActorKind, entry.ActorUser = meta["actor_kind"], meta["actor_user"]
	}
	return entry
}

// LiveOrigins folds a row's entries, oldest first, into the origins that stand.
// The last entry naming an origin decides it, which is what makes an add after a
// remove work without anything being deleted.
func LiveOrigins(entries []OriginEntry) []string {
	live := map[string]bool{}
	order := []string{}
	for _, e := range entries {
		if e.Origin == "" {
			continue
		}
		if _, seen := live[e.Origin]; !seen {
			order = append(order, e.Origin)
		}
		live[e.Origin] = e.Type == EventOriginAdd
	}
	out := []string{}
	for _, id := range order {
		if live[id] {
			out = append(out, id)
		}
	}
	return out
}

// AddOrigin records that one row came out of another.
func (d *DB) AddOrigin(ctx context.Context, p *Principal, artifact, origin string) (*Event, error) {
	return d.writeOrigin(ctx, p, EventOriginAdd, artifact, origin)
}

// RemoveOrigin records that it no longer did - somebody said so and was wrong,
// or the work moved. Nothing is deleted: the entry that takes it back is
// appended, and both are in the log afterwards.
func (d *DB) RemoveOrigin(ctx context.Context, p *Principal, artifact, origin string) (*Event, error) {
	return d.writeOrigin(ctx, p, EventOriginRemove, artifact, origin)
}

func (d *DB) writeOrigin(
	ctx context.Context, p *Principal, verb, artifact, origin string,
) (*Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, verb)
	defer span.End()

	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, refuseDep("this token resolves to nobody, so it cannot say where anything came from")
	}
	artifact, origin = strings.TrimSpace(artifact), strings.TrimSpace(origin)
	if artifact == "" || origin == "" {
		return nil, refuseDep("provenance names two rows: the one that came from somewhere " +
			"and the one it came from")
	}
	if artifact == origin {
		return nil, refuseDep("a row cannot have come out of itself (%s)", artifact)
	}

	// BOTH ENDS READ, and read the ordinary way. The refusal for an id this
	// principal cannot see is the one every other door gives - it does not
	// become a way to find out what an id is.
	came, err := d.ReadArtifact(ctx, p, artifact, false)
	if err != nil {
		return nil, err
	}
	if _, err := d.ReadArtifact(ctx, p, origin, false); err != nil {
		return nil, err
	}
	// The same rule dep.add has, for the same reason: an entry about a row with
	// no project is readable by whoever wrote it rather than by whoever can read
	// the row, so the provenance would be invisible to the people it is for.
	if came.Project == nil || *came.Project == "" {
		return nil, refuseDep("%s has no project and is its owner's alone, so a note about "+
			"where it came from would be readable by whoever wrote that note - write it at "+
			"scope=project or scope=shared first", artifact)
	}

	live, err := d.OriginsOf(ctx, p, artifact)
	if err != nil {
		return nil, err
	}
	already := false
	for _, id := range live {
		if id == origin {
			already = true
			break
		}
	}
	// An add of a relation that stands, or a removal of one that does not,
	// changes nothing and would go in the log as if it had.
	if already == (verb == EventOriginAdd) {
		if already {
			return nil, refuseDep("%s already says it came out of %s", artifact, origin)
		}
		return nil, refuseDep("%s does not say it came out of %s, so there is nothing to take back",
			artifact, origin)
	}

	meta, err := json.Marshal(map[string]string{
		OriginField:  origin,
		"actor_kind": actorKind,
		"actor_user": p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: %s %s: %w", verb, artifact, err)
	}
	e := &Event{
		Type:    verb,
		Project: came.Project,
		Room:    originRoom(came),
		Thread:  came.ID,
		// The row that CAME FROM somewhere, never the origin. This column
		// decides who reads the entry.
		Artifact: came.ID,
		Actor:    actor,
		Body:     originBody(verb, origin),
		Meta:     meta,
	}
	if err := d.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	span.SetArtifact(came.ID)
	return e, nil
}

// OriginsOf is where this row says it came from, as this reader sees it.
func (d *DB) OriginsOf(ctx context.Context, p *Principal, artifact string) ([]string, error) {
	entries, err := d.OriginLog(ctx, p, artifact)
	if err != nil {
		return nil, err
	}
	return LiveOrigins(entries), nil
}

// OriginLog is every entry about this row that p may read, oldest first - so a
// reader sees a relation being written and taken back rather than only the state
// it ended in.
func (d *DB) OriginLog(ctx context.Context, p *Principal, artifact string) ([]OriginEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "origin.log")
	defer span.End()

	artifact = strings.TrimSpace(artifact)
	if artifact == "" {
		return nil, nil
	}
	events, err := readPage(ctx, d, "origin events", func(a *args) string {
		idArg := a.next(artifact)
		typesArg := a.next(pq.Array([]string{EventOriginAdd, EventOriginRemove}))
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ` + idArg + ` AND e.type = ANY(` + typesArg + `)
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
	if err != nil {
		return nil, err
	}
	out := make([]OriginEntry, 0, len(events))
	for _, e := range events {
		out = append(out, OriginEntryOf(e))
	}
	return out, nil
}

func originRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return OriginRoom
}

// originBody is what the entry reads as on every surface that renders an event
// body and knows nothing about this one.
func originBody(verb, origin string) string {
	if verb == EventOriginRemove {
		return "no longer says it came out of " + origin
	}
	return "came out of " + origin
}
