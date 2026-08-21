package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/deadtrickster/flowy/internal/otel"
)

// WHAT A CLOCK OWES A READER THAT WAS AWAY - the last store-side piece of row
// 01M0EW45RE before delivery itself.
//
// Nothing here delivers anything. It answers one question - IS THIS SIGNAL DUE
// FOR THIS READER RIGHT NOW - and records that it was handed over. Wiring it
// into the wait door is deliberately a separate step: that door is what every
// seat on this node blocks on, and the row itself names the hazard, that one
// reader is one point of failure for every signal. A table and a query can be
// landed, exercised and reverted; a change to the door every agent depends on
// deserves its own measurement and its own conversation.
//
// See schema.sql for why the stamp is keyed on (reader, signal) rather than on
// the schedule that produced it.

// Due is one signal that has come round for a reader, with what the schedule
// resolved to at the moment it was asked.
type Due struct {
	Signal   string    `json:"signal"`
	Cron     string    `json:"cron"`
	FromKind string    `json:"from_kind"`
	Since    time.Time `json:"since,omitempty"`

	// First is true when this reader has no stamp for this signal at all.
	// It rides along because "you have missed one" and "you have never had
	// one" are different things to say to a seat, and the caller is the only
	// one that can decide whether the difference matters to it.
	First bool `json:"first"`
}

// AT MOST ONE PER SIGNAL, however long a reader was away.
//
// Three answers were possible and only one is right for a signal whose job is
// to reach an idle agent:
//
//   - fire every missed occurrence: a seat down eight hours returns to
//     twenty-four nags, which is the loop-detection ask arriving as the thing
//     it detects.
//   - fire none and resume at the next boundary: a seat that reconnects at
//     09:01 waits until tomorrow for a 09:00 digest - the one moment it was
//     idle and askable is the one it is not told.
//   - fire once and resume: the reader learns the board changed, once.
//
// A READER WITH NO STAMP IS DUE IMMEDIATELY. This is the decision I flagged on
// the row as the operator's, and I am taking it as immediate with the reason
// written here so it is one line to reverse: the whole point of a clock signal
// is that an idle seat learns there is work, and a seat that has just connected
// is maximally idle. Waiting for the next boundary makes a new reader's first
// hour its quietest. It is reversible without a migration - the stamp would
// simply be written and nothing returned.
func (db *DB) Due(ctx context.Context, reader, project, room string, now time.Time) ([]Due, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "schedule.due")
	defer span.End()

	stamps, err := db.lastFired(ctx, reader)
	if err != nil {
		return nil, err
	}

	out := []Due{}
	for _, signal := range Signals {
		resolved, err := db.ResolveSchedule(ctx, project, room, signal)
		if err != nil {
			return nil, err
		}
		// No clock is not a failure and not a firing. A realtime-only
		// signal is delivered by the fact changing, which is a different
		// mechanism and not this one's business.
		if resolved.Cron == "" {
			continue
		}

		last, seen := stamps[signal]
		if !seen {
			out = append(out, Due{
				Signal: signal, Cron: resolved.Cron,
				FromKind: resolved.FromKind, First: true,
			})
			continue
		}

		next, ok, err := resolved.NextFiring(last)
		if err != nil {
			// A stored spec that no longer parses must be loud. It
			// cannot be silently skipped: that is a schedule a person
			// set, sees in the console, and never receives.
			return nil, err
		}
		if !ok || next.After(now) {
			continue
		}
		out = append(out, Due{
			Signal: signal, Cron: resolved.Cron,
			FromKind: resolved.FromKind, Since: next,
		})
	}
	return out, nil
}

// MarkFired records that a reader has been handed this signal.
//
// THE STAMP IS NOW, NOT THE BOUNDARY IT WAS DUE FOR. Writing the boundary would
// leave a reader that took a while to come back immediately eligible again -
// the missed firing would still be in the past - and one firing would arrive
// twice. Now is what makes "at most one" true rather than merely intended.
func (db *DB) MarkFired(ctx context.Context, reader, signal string, at time.Time) error {
	ctx, span := otel.Start(ctx, otel.KindQuery, "schedule.markfired")
	defer span.End()

	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO schedule_fired (reader, signal, fired_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (reader, signal) DO UPDATE SET fired_at = excluded.fired_at`,
		reader, signal, at)
	return err
}

// LastFired is when this reader last received this signal, and whether it ever
// has. The bool is the whole answer for a new reader, and a zero time is not a
// substitute for it - a zero time is also what a broken read returns.
func (db *DB) LastFired(ctx context.Context, reader, signal string) (time.Time, bool, error) {
	ctx, span := otel.Start(ctx, otel.KindQuery, "schedule.lastfired")
	defer span.End()

	var at time.Time
	err := db.sql.QueryRowContext(ctx, `
		SELECT fired_at FROM schedule_fired WHERE reader = $1 AND signal = $2`,
		reader, signal).Scan(&at)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return at, true, nil
}

// lastFired is every stamp this reader has, in one query rather than one per
// signal - Due asks about all of them and a round trip each would be a
// per-signal cost on a door that blocks.
func (db *DB) lastFired(ctx context.Context, reader string) (map[string]time.Time, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT signal, fired_at FROM schedule_fired WHERE reader = $1`, reader)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]time.Time{}
	for rows.Next() {
		var signal string
		var at time.Time
		if err := rows.Scan(&signal, &at); err != nil {
			return nil, err
		}
		out[signal] = at
	}
	return out, rows.Err()
}
