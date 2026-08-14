package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ExternalRef is an artifact's link to an issue on a forge: which forge, which
// repo, which number, where it is and what state it was last seen in.
//
// It lives in artifacts.external as jsonb rather than in a table of its own
// because it is a property of the artifact and travels with it: federation
// replicates artifacts last-writer-wins by hlc, so a bug filed on one node and
// pulled by another arrives already carrying the issue it was filed as, with no
// second table to merge and no second cursor to keep.
//
// Thread, Since, Seen and Pushed are the sync's bookkeeping. They are in here
// for the same reason: a node that pulled this artifact from a peer knows what
// has already been threaded in and what has already been pushed out, so the two
// nodes do not double-post the same reply onto the issue.
type ExternalRef struct {
	Forge  string `json:"forge"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"`

	// Thread is the chat thread the issue's conversation lands in.
	Thread string `json:"thread,omitempty"`
	// Author is the login the node's own comments appear under on the forge.
	// Comments by it are not threaded back in - that is what stops the reviewer
	// loop echoing itself.
	Author string `json:"author,omitempty"`
	// Since is the time of the newest comment that has been threaded in, and is
	// what the next ListComments asks for.
	Since time.Time `json:"since,omitempty"`
	// Seen holds the comments already threaded in, newest last and capped. It
	// is the belt to Since's braces: a forge whose timestamps have one-second
	// resolution can hand back two comments at the same instant, and a cursor
	// alone cannot tell the second one from the first.
	//
	// Each entry carries the time of its comment as well as its id, because the
	// cap has to know which entries the cursor already covers. It used to be a
	// list of bare ids trimmed to the newest hundred, and the hundred-and-first
	// same-second comment pushed the first one out - after which Since could
	// not rule it out either, because a comment made at exactly the cursor is
	// not before it, and the node threaded it in a second time.
	Seen []SeenComment `json:"seen,omitempty"`
	// Pushed is the seq_hlc of the last thread event the sync has considered.
	// Everything above it is a reply that has not been out to the forge yet.
	Pushed int64 `json:"pushed,omitempty"`
	// Filed is when the issue was opened from here.
	Filed time.Time `json:"filed,omitempty"`
}

// SeenComment is one comment this node has already threaded in: which comment,
// and when the forge said it was made.
type SeenComment struct {
	ID string    `json:"id"`
	At time.Time `json:"at,omitempty"`
}

// UnmarshalJSON accepts both shapes a seen entry has had: the bare id earlier
// nodes wrote, and the {id, at} pair this one writes. Refs replicate, so a
// link written by an older node has to keep parsing here - an entry with no
// time is one whose age is unknown, and is the first to be forgotten.
func (s *SeenComment) UnmarshalJSON(data []byte) error {
	var id string
	if err := json.Unmarshal(data, &id); err == nil {
		s.ID, s.At = id, time.Time{}
		return nil
	}
	var raw struct {
		ID string    `json:"id"`
		At time.Time `json:"at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.ID, s.At = raw.ID, raw.At
	return nil
}

// seenCap is how many comments an ExternalRef carries. Enough to cover any
// plausible batch of same-second comments, small enough that the column stays a
// link rather than a copy of the issue.
const seenCap = 100

// MarkSeen records a comment as threaded in and advances the cursor past it.
func (r *ExternalRef) MarkSeen(id string, at time.Time) {
	if id != "" {
		r.Seen = append(r.Seen, SeenComment{ID: id, At: at})
	}
	if at.After(r.Since) {
		r.Since = at
	}
	r.forget()
}

// forget trims the list back towards the cap, oldest first, and only ever drops
// an entry the cursor covers on its own - one whose comment is strictly older
// than Since. An entry made at exactly the cursor is the one case the cursor
// cannot decide, so it stays however long the list gets; when the cursor moves
// past it, it becomes forgettable like everything else.
//
// The cap is therefore a target rather than a limit. A hard limit is what the
// bug was: it threw away the entries that were doing the work.
func (r *ExternalRef) forget() {
	drop := len(r.Seen) - seenCap
	if drop <= 0 {
		return
	}
	keep := make([]SeenComment, 0, len(r.Seen))
	for _, seen := range r.Seen {
		if drop > 0 && (seen.At.IsZero() || seen.At.Before(r.Since)) {
			drop--
			continue
		}
		keep = append(keep, seen)
	}
	r.Seen = keep
}

// AlreadySeen reports whether a comment has been threaded in: by its id, or by
// being older than the cursor and so accounted for by an earlier sync.
func (r *ExternalRef) AlreadySeen(id string, at time.Time) bool {
	for _, seen := range r.Seen {
		if seen.ID == id {
			return true
		}
	}
	return !r.Since.IsZero() && !at.IsZero() && at.Before(r.Since)
}

// SetArtifactExternal writes an artifact's forge link and its reported flag,
// and nothing else.
//
// It is not an upsert of the artifact, for the same reason SetArtifactStatus is
// not: filing a bug changes two columns, and replacing the row would make it
// look to every peer like a rewrite of the whole artifact. The clock does move,
// because the link is part of what replicates.
func (d *DB) SetArtifactExternal(
	ctx context.Context, art *Artifact, ref *ExternalRef, reported bool,
) error {
	var raw any
	if ref != nil {
		encoded, err := json.Marshal(ref)
		if err != nil {
			return fmt.Errorf("store: external ref of %s: %w", art.ID, err)
		}
		raw = encoded
	}

	art.External, art.Reported = ref, reported
	art.HLC = d.clock.Pack()
	art.Node = d.node
	_, err := d.sql.ExecContext(ctx,
		`UPDATE artifacts SET external = $2, reported = $3, hlc = $4, node = $5, updated = now()
		  WHERE id = $1`, art.ID, raw, art.Reported, art.HLC, art.Node)
	if err != nil {
		return fmt.Errorf("store: set external ref of %s: %w", art.ID, err)
	}
	return nil
}

// LatestTaskForArtifact is the newest handoff about an artifact, or ErrNotFound
// when nobody has been assigned it.
//
// The forge bridge asks so that an issue's conversation lands in the thread the
// people working on it are already talking in, rather than opening a second one
// beside it. It does not filter by principal: the callers have already had to
// read the artifact, and the thread they get is one the task's own clause in
// EventFilterSQL governs.
func (d *DB) LatestTaskForArtifact(ctx context.Context, artifact string) (*Task, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT `+taskColumns+`
		   FROM tasks t
		  WHERE t.artifact = $1 AND coalesce(t.thread, '') <> ''
		  ORDER BY t.hlc DESC, t.id DESC
		  LIMIT 1`, artifact)
	if err != nil {
		return nil, fmt.Errorf("store: latest task for %s: %w", artifact, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("store: latest task for %s: %w", artifact, err)
		}
		return nil, ErrNotFound
	}
	t, err := scanTask(rows, false)
	if err != nil {
		return nil, fmt.Errorf("store: latest task for %s: %w", artifact, err)
	}
	return t, nil
}
