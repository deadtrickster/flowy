package store

// A repro run leaves a verdict in an append-only log, MIRRORING DepLog and
// depEvents in deps.go - read that file's head comment first, because the
// same three decisions are load-bearing here for the same reasons.
//
// A VERDICT IS AN EVENT, NOT A FIELD. The Python service this replaces kept
// runs.jsonl - {version, sha, confirmed, status, at} - appended to on every
// run, precisely so a console can show red-then-green across reruns of the
// SAME version: version 3 failing after version 2 passed is the fact worth
// keeping, and a field only ever holds the latest verdict. Recording the
// latest verdict where the one before it stood is exactly the mistake
// findingrepro.go's manifest is allowed to make and this may not - a rerun
// is the whole reason this file exists, and latest-write-wins would erase
// the history that makes a rerun worth doing.
//
// THE EVENT HANGS OFF THE FINDING, AND THAT IS THE SAFETY PROPERTY - simpler
// here than the edge deps.go draws, because a run names only one artifact.
// The event's artifact column is the finding, so EventFilterSQL's floor
// clause gives every principal who can read the finding every run event
// naming it, with nothing to hide it from a reader who could otherwise see
// the finding. A finding with no project cannot make that promise -
// EventFilterSQL reads a projectless event back by its actor and nobody
// else, deps.go's exact reasoning for refusing an edge into a projectless
// todo - so RecordFindingRun refuses one here too, for the same reason and
// with the same message shape.
//
// ONE LIMIT, INHERITED. finding.run is minted (see mintedEventTypes in
// sync.go and mintedTypes in api.go), so it does not cross a node boundary
// in either direction and the only way to get an entry is to have gone
// through RecordFindingRun - a run typed in by hand is a green verdict on a
// version nobody ran, and the whole value of this log is that it cannot be
// that.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// EventFindingRun is the event type a repro run's verdict rides. Minted -
// see the head of this file.
const EventFindingRun = "finding.run"

// The meta keys a run's verdict carries, matching runs.jsonl's own field
// names exactly - version, sha, confirmed, status - so a run recorded here
// says the same thing the file it replaces said. "at" is not among them: it
// is the event's own Created, minted by AppendEvent, and a second copy of it
// in meta is a second clock nothing here reads.
const (
	RunVersionField   = "version"
	RunSHAField       = "sha"
	RunConfirmedField = "confirmed"
	RunStatusField    = "status"
)

// FindingRunRoom is where a run's entry lands when the finding it is about
// names no room of its own - depRoom's rule in deps.go, for the reason that
// exists: an entry nobody can find in a room is an entry nobody reads.
const FindingRunRoom = "finding-runs"

// FindingRun is a repro run's verdict, as a caller states it to
// RecordFindingRun: which version of the repro tree ran, at which commit,
// whether it reproduced the finding, and what came of it.
type FindingRun struct {
	Version   string `json:"version"`
	SHA       string `json:"sha"`
	Confirmed bool   `json:"confirmed"`
	Status    string `json:"status"`
}

// FindingRunEntry is one entry in the log behind a finding's run history:
// the verdict, who reported it and when - DepEntry's shape, in deps.go, for
// the same reason: what makes the record worth keeping is that a later run
// does not erase an earlier one, it appends the fact that this run said
// something different.
type FindingRunEntry struct {
	ID        string `json:"id"`
	Finding   string `json:"finding"`
	Version   string `json:"version"`
	SHA       string `json:"sha"`
	Confirmed bool   `json:"confirmed"`
	Status    string `json:"status"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind,omitempty"`
	ActorUser string `json:"actor_user,omitempty"`
	SeqHLC    int64  `json:"seq_hlc"`
	Node      string `json:"node"`
	At        string `json:"at"`
}

// RecordFindingRun appends one run's verdict to the finding's run log.
//
// The refusals, in the order they are asked:
//
//   - findingID does not name a finding this principal may read - readFinding's
//     answer, the same one WriteFindingRepro asks first.
//   - the finding has no project. See the head of this file: a projectless
//     event is read back by its actor alone, which would make a run's
//     verdict invisible to everyone else who can read the finding, silently.
//   - the run names no version. A verdict with nothing to say it is a verdict
//     ABOUT is not a fact a console could ever place on the red/green line
//     this file exists to draw.
func (d *DB) RecordFindingRun(ctx context.Context, p *Principal, findingID string, run FindingRun) (*Event, error) {
	ctx, span := otel.Start(ctx, otel.KindIngest, EventFindingRun)
	defer span.End()

	finding, err := d.readFinding(ctx, p, findingID)
	if err != nil {
		return nil, err
	}
	actor, actorKind := voteActor(p)
	if actor == "" {
		return nil, fmt.Errorf("this token resolves to nobody, so it cannot report a run's verdict")
	}
	if finding.Project == nil || *finding.Project == "" {
		return nil, fmt.Errorf("finding %s has no project and is its owner's alone, so a run event "+
			"on it would be readable by whoever reported the run rather than by whoever can read "+
			"the finding - write it at scope=project or scope=shared first", findingID)
	}
	version := strings.TrimSpace(run.Version)
	if version == "" {
		return nil, fmt.Errorf("a run names the version of the repro tree it ran")
	}

	meta, err := json.Marshal(map[string]any{
		RunVersionField:   version,
		RunSHAField:       run.SHA,
		RunConfirmedField: run.Confirmed,
		RunStatusField:    run.Status,
		"actor_kind":      actorKind,
		"actor_user":      p.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("store: record finding %s run: %w", findingID, err)
	}

	e := &Event{
		Type:     EventFindingRun,
		Project:  finding.Project,
		Room:     findingRunRoom(finding),
		Thread:   finding.ID,
		Artifact: finding.ID,
		Actor:    actor,
		Body:     findingRunBody(run),
		Meta:     meta,
	}
	if err := d.AppendEvent(ctx, e); err != nil {
		return nil, err
	}
	span.SetArtifact(finding.ID)
	return e, nil
}

// FindingRuns is every run entry naming this finding that p may read, oldest
// first, so a reader sees red-then-green across reruns of the same version
// rather than only the verdict it ended up at - DepLog's shape in deps.go,
// over the log RecordFindingRun appends to.
func (d *DB) FindingRuns(ctx context.Context, p *Principal, findingID string) ([]FindingRunEntry, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "finding.runs")
	defer span.End()

	// readFinding first, so an id that is not a finding at all answers "no
	// such finding" rather than an empty run list a reader cannot tell apart
	// from a finding that has never been run.
	if _, err := d.readFinding(ctx, p, findingID); err != nil {
		return nil, err
	}

	events, err := d.findingRunEvents(ctx, p, findingID)
	if err != nil {
		return nil, err
	}
	out := make([]FindingRunEntry, 0, len(events))
	for _, e := range events {
		out = append(out, findingRunEntryOf(e))
	}
	return out, nil
}

// findingRunEvents reads the run entries naming findingID, in log order,
// through the same event filter every other read of the log uses -
// depEvents' rule in deps.go, narrowed to one finding rather than a set of
// todos because FindingRuns only ever asks about one.
//
// No LIMIT, deliberately and for depEvents' own reason: a finding's run log
// is a handful of entries, and a page that stopped early would show a run
// history that is not the history.
func (d *DB) findingRunEvents(ctx context.Context, p *Principal, findingID string) ([]*Event, error) {
	return readPage(ctx, d, "finding run events", func(a *args) string {
		idArg := a.next(findingID)
		typeArg := a.next(EventFindingRun)
		filter := EventFilterSQL(p, "e", a, false)
		return `SELECT ` + eventColumns + `
		            FROM events e
		           WHERE e.artifact = ` + idArg + ` AND e.type = ` + typeArg + `
		             AND ` + filter + `
		           ORDER BY e.seq_hlc, e.id`
	}, scanEvent)
}

// findingRunEntryOf renders one event as the entry it is - DepEntryOf's rule
// in deps.go.
func findingRunEntryOf(e *Event) FindingRunEntry {
	entry := FindingRunEntry{
		ID: e.ID, Finding: e.Artifact, Actor: e.Actor,
		SeqHLC: e.SeqHLC, Node: e.Node,
		At: e.Created.UTC().Format(time.RFC3339Nano),
	}
	var meta map[string]any
	if len(e.Meta) > 0 && json.Unmarshal(e.Meta, &meta) == nil {
		entry.Version, _ = meta[RunVersionField].(string)
		entry.SHA, _ = meta[RunSHAField].(string)
		entry.Confirmed, _ = meta[RunConfirmedField].(bool)
		entry.Status, _ = meta[RunStatusField].(string)
		entry.ActorKind, _ = meta["actor_kind"].(string)
		entry.ActorUser, _ = meta["actor_user"].(string)
	}
	return entry
}

// findingRunRoom is depRoom's rule in deps.go, for the same reason: an entry
// nobody can find in a room is an entry nobody reads.
func findingRunRoom(a *Artifact) string {
	if room := RoomOf(a); room != "" {
		return room
	}
	return FindingRunRoom
}

// findingRunBody is what a run's entry reads as on every surface that
// renders an event body and knows nothing about this one - the timeline, the
// console's activity view, the TUI. depBody's reasoning in deps.go: it names
// the verdict rather than assuming a reader already has the row in hand.
func findingRunBody(run FindingRun) string {
	verdict := "not confirmed"
	if run.Confirmed {
		verdict = "confirmed"
	}
	body := "run " + run.Version + ": " + verdict
	if run.Status != "" {
		body += " (" + run.Status + ")"
	}
	return body
}
