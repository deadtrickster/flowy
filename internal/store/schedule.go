package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/deadtrickster/flowy/internal/cron"
	"github.com/deadtrickster/flowy/internal/otel"
)

// THE SCHEDULE THE NODE HOLDS - the store side of row 01M0EW45RE.
//
// A signal is something an idle agent is told about: a message in a room, work
// on the board. Today each one is its own persistent loop in each agent's
// harness, so a schedule change is an edit to N harnesses and a dead loop is
// silence nobody notices. This moves the SCHEDULE into the node; the delivery
// stays a per-seat monitor, because being woken is a property of the seat.
//
// See schema.sql for why there are two controls and not three, why resolution
// is whole-row, and why a row EXISTING is what decides rather than a row being
// switched on.

// The levels a schedule row can sit at, least specific first. The order is the
// resolution order and Scopes depends on it.
//
// They are named Sched* rather than Scope* because this package ALREADY has a
// ScopeProject, and it means something else: announce.go's scopes are node,
// project and federation, and they answer HOW FAR AN ANNOUNCEMENT TRAVELS.
// These answer WHICH SETTING APPLIES TO A READER. Two different axes that share
// the word project, so they do not share a name.
const (
	SchedFleet   = "fleet"
	SchedProject = "project"
	SchedRoom    = "room"
)

// Signals are the built-in signals a schedule can be written for.
//
// It is a CLOSED SET on purpose. A signal name that nothing delivers is a
// schedule a person edits, sees saved, and never receives - the same failure
// the cron reachability check exists to prevent, arriving through a typo
// instead of through a date. Adding one is a line here and a label in the
// console, which is two deliberate edits rather than a free-text field.
var Signals = []string{"chat", "board"}

// Scope is where a schedule row sits. ID is empty for fleet, the project id for
// project, and project + unit separator + room for room.
type Scope struct {
	Kind string
	ID   string
}

// FleetScope, ProjectScope and RoomScope build the three, so the ID format
// lives in one place rather than at every call site.
func FleetScope() Scope                 { return Scope{Kind: SchedFleet} }
func ProjectScope(project string) Scope { return Scope{Kind: SchedProject, ID: project} }
func RoomScope(project, room string) Scope {
	return Scope{Kind: SchedRoom, ID: project + "\x1f" + room}
}

func (s Scope) String() string {
	if s.ID == "" {
		return s.Kind
	}
	return s.Kind + " " + strings.ReplaceAll(s.ID, "\x1f", "/")
}

// Schedule is one row: what a signal does at one scope.
type Schedule struct {
	Scope     Scope     `json:"-"`
	ScopeKind string    `json:"scope_kind"`
	ScopeID   string    `json:"scope_id"`
	Signal    string    `json:"signal"`
	Realtime  bool      `json:"realtime"`
	Cron      string    `json:"cron"`
	UpdatedBy string    `json:"updated_by,omitempty"`
	Updated   time.Time `json:"updated"`
}

// Never reports the explicit off state: no realtime and no clock. It is a
// legitimate saved value rather than an absent row - a room turning a signal
// off has to be able to override a project that turned it on.
func (s Schedule) Never() bool { return !s.Realtime && s.Cron == "" }

// Resolved is what a reader at some scope actually gets for a signal, and WHERE
// the answer came from.
//
// From is not decoration. The console shows "this is what orchestrator will
// actually receive", and a person checking that needs to know whether they are
// looking at a room's own setting or a fleet default they have not overridden -
// those two look identical in every field except this one.
type Resolved struct {
	Signal   string `json:"signal"`
	Realtime bool   `json:"realtime"`
	Cron     string `json:"cron"`
	From     Scope  `json:"-"`
	FromKind string `json:"from_kind"`
	FromID   string `json:"from_id,omitempty"`

	// Defaulted is true when NO scope had a row. The zero value of a
	// schedule and "nobody has ever configured this" are different states,
	// and a console that shows them the same way is telling somebody their
	// deliberate off is the same as an untouched signal.
	Defaulted bool `json:"defaulted"`
}

func (r Resolved) Never() bool { return !r.Realtime && r.Cron == "" }

// ErrUnknownSignal is returned rather than a generic failure, because the door
// shows it to whoever is editing and "chatt is not a signal" is actionable in a
// way that "invalid request" is not.
var ErrUnknownSignal = errors.New("not a signal this node delivers")

func knownSignal(signal string) bool {
	for _, s := range Signals {
		if s == signal {
			return true
		}
	}
	return false
}

func validScope(s Scope) error {
	switch s.Kind {
	case SchedFleet:
		if s.ID != "" {
			return fmt.Errorf("fleet scope takes no id, got %q", s.ID)
		}
	case SchedProject:
		if s.ID == "" {
			return errors.New("project scope needs a project")
		}
		if strings.Contains(s.ID, "\x1f") {
			return errors.New("project scope id contains a separator, so it is a room scope written wrongly")
		}
	case SchedRoom:
		project, room, ok := strings.Cut(s.ID, "\x1f")
		if !ok || project == "" || room == "" {
			return errors.New("room scope needs a project and a room")
		}
	default:
		return fmt.Errorf("%q is not a scope (fleet, project, room)", s.Kind)
	}
	return nil
}

// PutSchedule writes one scope's setting for one signal, replacing whatever was
// there.
//
// IT REFUSES A CRON THAT CAN NEVER FIRE. That is the whole reason the parse
// happens here rather than in the console: a spec that is saved, shown back,
// and dead is worse than one rejected, because the display is evidence that it
// works. The refusal carries the parser's own sentence, which names the field
// and the token rather than saying the line is invalid.
func (db *DB) PutSchedule(ctx context.Context, s Schedule, by string) (Schedule, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "schedule.put")
	defer span.End()

	if s.Scope.Kind == "" {
		s.Scope = Scope{Kind: s.ScopeKind, ID: s.ScopeID}
	}
	if err := validScope(s.Scope); err != nil {
		return Schedule{}, err
	}
	if !knownSignal(s.Signal) {
		return Schedule{}, fmt.Errorf("%q: %w (%s)", s.Signal, ErrUnknownSignal, strings.Join(Signals, ", "))
	}

	s.Cron = strings.TrimSpace(s.Cron)
	if s.Cron != "" {
		if _, err := cron.Parse(s.Cron); err != nil {
			return Schedule{}, err
		}
	}

	row := db.sql.QueryRowContext(ctx, `
		INSERT INTO schedules (scope_kind, scope_id, signal, realtime, cron, updated_by, updated)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (scope_kind, scope_id, signal) DO UPDATE
		   SET realtime = excluded.realtime,
		       cron = excluded.cron,
		       updated_by = excluded.updated_by,
		       updated = now()
		RETURNING scope_kind, scope_id, signal, realtime, cron, coalesce(updated_by, ''), updated`,
		s.Scope.Kind, s.Scope.ID, s.Signal, s.Realtime, s.Cron, by)

	var out Schedule
	if err := row.Scan(&out.ScopeKind, &out.ScopeID, &out.Signal, &out.Realtime, &out.Cron, &out.UpdatedBy, &out.Updated); err != nil {
		return Schedule{}, err
	}
	out.Scope = Scope{Kind: out.ScopeKind, ID: out.ScopeID}
	return out, nil
}

// DeleteSchedule removes a scope's row so that scope inherits again.
//
// This is the ONLY way back to inheriting, and it is why the console needs a
// verb distinct from turning a signal off: an off row and no row are different
// answers, and a person who wants the project default back cannot get there by
// unchecking a box.
func (db *DB) DeleteSchedule(ctx context.Context, scope Scope, signal string) (bool, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "schedule.delete")
	defer span.End()

	if err := validScope(scope); err != nil {
		return false, err
	}
	res, err := db.sql.ExecContext(ctx, `
		DELETE FROM schedules WHERE scope_kind = $1 AND scope_id = $2 AND signal = $3`,
		scope.Kind, scope.ID, signal)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Scopes is the chain a reader in this project and room sits in, LEAST specific
// first. An empty room gives the two-element chain, an empty project the
// one-element one - a reader with no room is still in the fleet.
func Scopes(project, room string) []Scope {
	chain := []Scope{FleetScope()}
	if project != "" {
		chain = append(chain, ProjectScope(project))
		if room != "" {
			chain = append(chain, RoomScope(project, room))
		}
	}
	return chain
}

// ResolveSchedule answers what a reader in this project and room receives for
// this signal, and which scope decided.
//
// The most specific scope WITH A ROW wins, whole-row. When no scope has one the
// answer is the built-in default, flagged Defaulted so a console can say
// "nothing is configured" rather than showing an off switch somebody chose.
func (db *DB) ResolveSchedule(ctx context.Context, project, room, signal string) (Resolved, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "schedule.resolve")
	defer span.End()

	if !knownSignal(signal) {
		return Resolved{}, fmt.Errorf("%q: %w (%s)", signal, ErrUnknownSignal, strings.Join(Signals, ", "))
	}

	chain := Scopes(project, room)
	out := defaultFor(signal)

	// Walk least specific first and let each present row overwrite. One
	// query per scope would be three round trips to answer one question, so
	// this asks once and orders in Go where the chain already lives.
	kinds := make([]string, 0, len(chain))
	ids := make([]string, 0, len(chain))
	for _, s := range chain {
		kinds = append(kinds, s.Kind)
		ids = append(ids, s.ID)
	}

	rows, err := db.sql.QueryContext(ctx, `
		SELECT scope_kind, scope_id, realtime, cron
		  FROM schedules
		 WHERE signal = $1
		   AND (scope_kind, scope_id) IN (
		       SELECT * FROM unnest($2::text[], $3::text[]))`,
		signal, pq.Array(kinds), pq.Array(ids))
	if err != nil {
		return Resolved{}, err
	}
	defer rows.Close()

	found := map[string]Resolved{}
	for rows.Next() {
		var kind, id string
		var r Resolved
		if err := rows.Scan(&kind, &id, &r.Realtime, &r.Cron); err != nil {
			return Resolved{}, err
		}
		r.Signal = signal
		r.From = Scope{Kind: kind, ID: id}
		r.FromKind = kind
		r.FromID = id
		found[kind+"\x1f"+id] = r
	}
	if err := rows.Err(); err != nil {
		return Resolved{}, err
	}

	for _, s := range chain {
		if r, ok := found[s.Kind+"\x1f"+s.ID]; ok {
			out = r
		}
	}
	return out, nil
}

// defaultFor is what a signal does when no scope has said. It is a function
// rather than a table row because a default nobody wrote is not a setting
// somebody chose, and storing it as one would make the two indistinguishable.
//
// Chat defaults realtime: a message is only useful now. Board defaults to a
// clock, because "the board changed" fires on every landing - which is the same
// reason board-nag.sh carries an interval today.
func defaultFor(signal string) Resolved {
	r := Resolved{Signal: signal, Defaulted: true, From: FleetScope(), FromKind: SchedFleet}
	switch signal {
	case "chat":
		r.Realtime = true
	case "board":
		r.Cron = "*/20 * * * *"
	}
	return r
}

// ListSchedules is every row at one scope, for the console's table. It lists
// what was SET, so a signal with no row is absent rather than shown as off -
// the same distinction the resolver makes, kept on the way out.
func (db *DB) ListSchedules(ctx context.Context, scope Scope) ([]Schedule, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "schedule.list")
	defer span.End()

	if err := validScope(scope); err != nil {
		return nil, err
	}
	rows, err := db.sql.QueryContext(ctx, `
		SELECT scope_kind, scope_id, signal, realtime, cron, coalesce(updated_by, ''), updated
		  FROM schedules
		 WHERE scope_kind = $1 AND scope_id = $2
		 ORDER BY signal`, scope.Kind, scope.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Schedule{}
	for rows.Next() {
		var s Schedule
		if err := rows.Scan(&s.ScopeKind, &s.ScopeID, &s.Signal, &s.Realtime, &s.Cron, &s.UpdatedBy, &s.Updated); err != nil {
			return nil, err
		}
		s.Scope = Scope{Kind: s.ScopeKind, ID: s.ScopeID}
		out = append(out, s)
	}
	return out, rows.Err()
}

// NextFiring is when this resolved schedule next fires on the clock, if it has
// one. Realtime firings are not on a clock and are not this question.
//
// The bool is false for a schedule with no cron - which is a real answer, not a
// failure, and is why this does not return an error for it.
func (r Resolved) NextFiring(after time.Time) (time.Time, bool, error) {
	if r.Cron == "" {
		return time.Time{}, false, nil
	}
	spec, err := cron.Parse(r.Cron)
	if err != nil {
		// A stored spec that no longer parses is a real possibility if
		// the parser ever tightens, and it must be loud rather than
		// silently never firing.
		return time.Time{}, false, err
	}
	next, ok := spec.Next(after)
	return next, ok, nil
}
