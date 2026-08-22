package store

// Conflict edges between openspec changes: two changes that edit the same
// capability's spec delta clash, and the clash is a fact worth a table
// rather than a guess a reader recomputes from the two rows.
//
// Why its own table rather than a third shape in deps.go: depends-on edges
// are acyclic and acyclic is enforced there; conflicts are symmetric and
// nobody refuses to write one - a conflict is resolved by merging one of
// the changes, not by refusing to file it. Two different rules, two
// different tables. (Operator's answer on thread 01M0K9WFBNBZ9V9XBK5NGD7D9K,
// message 01M0KENVHE554V04WN16B8M4RH.)
//
// BOTH ENDS ON BOTH ROWS: a pair is stored twice, once per direction, so
// reading one change's edges is one indexed query, and the recompute on a
// write deletes both halves of every pair this change holds. The edges are
// a function of the rows - every write of a change recomputes its own -
// not a log; a change that moved supersedes them.
//
// The lifecycle sibling (p3) will refuse to archive a change that still
// holds edges; until then the table answers reads only.

import (
	"context"
	"fmt"
	"strings"
)

// OpenspecConflict is one edge, as a reader sees it: which other change,
// over which capability.
type OpenspecConflict struct {
	Change string `json:"change"`
	Spec   string `json:"spec"`
}

// conflictCapabilities extracts the capabilities a change's files touch:
// the specs/<capability>/... paths, and only those. proposal.md, tasks.md
// and design.md are the change's own words and clash with nothing.
func conflictCapabilities(files map[string]string) map[string]bool {
	out := map[string]bool{}
	for path := range files {
		if !strings.HasPrefix(path, "specs/") {
			continue
		}
		rest := strings.TrimPrefix(path, "specs/")
		cap, _, ok := strings.Cut(rest, "/")
		if !ok || cap == "" {
			continue
		}
		out[cap] = true
	}
	return out
}

// recomputeConflicts rebuilds the edges a written change holds, on the
// caller's execer so it rides the write's transaction. It is asked of the
// same three statements as deriveChange, for the same reason - see
// syncOpenspec, the one hook they share.
func (d *DB) recomputeConflicts(ctx context.Context, q execer, change *Artifact) error {
	if !IsEntityType(change, ChangeKind) {
		return nil
	}
	// Both directions go: the other half of each pair is as stale as this
	// one, and a delete that kept it would leave a row pointing at a
	// change with no answering row.
	if _, err := q.ExecContext(ctx,
		`DELETE FROM openspec_conflicts WHERE change = $1 OR other = $1`, change.ID); err != nil {
		return fmt.Errorf("store: conflicts of %s: %w", change.ID, err)
	}
	files, err := OpenspecFilesOf(change)
	if err != nil {
		return err
	}
	mine := conflictCapabilities(files)
	if len(mine) == 0 {
		return nil
	}
	// The live changes this one could clash with, inside its own project -
	// projects are the isolation boundary, and a capability in one project
	// is a different row from the same name in another, so their deltas can
	// never meet. Every write of a change asks this, so it stays a scan of
	// the active change set - which is small by construction, since a change
	// that landed is archived and gone from this population (p3). An index
	// can wait for a node that measures it needs one.
	rows, err := q.QueryContext(ctx,
		`SELECT `+artifactColumns+`
		   FROM artifacts
		  WHERE kind = '`+ChangeKind+`'
		    AND coalesce(tombstone, false) = false
		    AND project = $2
		    AND id <> $1
		    AND fields -> 'openspec' ? 'files'`, change.ID, change.Project)
	if err != nil {
		return fmt.Errorf("store: conflicts of %s: %w", change.ID, err)
	}
	defer rows.Close()
	for rows.Next() {
		art, err := scanArtifact(rows, nil)
		if err != nil {
			return fmt.Errorf("store: conflicts of %s: %w", change.ID, err)
		}
		theirs, err := OpenspecFilesOf(art)
		if err != nil {
			return err
		}
		for cap := range conflictCapabilities(theirs) {
			if !mine[cap] {
				continue
			}
			if _, err := q.ExecContext(ctx,
				`INSERT INTO openspec_conflicts (change, other, spec) VALUES ($1, $2, $3), ($2, $1, $3)`,
				change.ID, art.ID, cap); err != nil {
				return fmt.Errorf("store: conflicts of %s: %w", change.ID, err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: conflicts of %s: %w", change.ID, err)
	}
	return nil
}

// conflictsOf reads one change's edges. Unfiltered by permission: an edge
// is a fact about the two rows, and the door decides what the caller may
// be told about the other end.
func (d *DB) conflictsOf(ctx context.Context, q execer, change string) ([]OpenspecConflict, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT other, spec FROM openspec_conflicts WHERE change = $1 ORDER BY spec, other`, change)
	if err != nil {
		return nil, fmt.Errorf("store: conflicts of %s: %w", change, err)
	}
	defer rows.Close()
	var out []OpenspecConflict
	for rows.Next() {
		var e OpenspecConflict
		if err := rows.Scan(&e.Change, &e.Spec); err != nil {
			return nil, fmt.Errorf("store: conflicts of %s: %w", change, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: conflicts of %s: %w", change, err)
	}
	return out, nil
}

// ConflictsOf reads one change's conflict edges off the pool, for the doors.
func (d *DB) ConflictsOf(ctx context.Context, change string) ([]OpenspecConflict, error) {
	return d.conflictsOf(ctx, d.sql, change)
}
