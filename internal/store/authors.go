package store

// Names for the seats a reader is handed.
//
// An artifact's owner_user and each note entry's actor are principal ids. A
// queue read by four seats is read as names, not ids - so the read paths that
// hand artifacts to readers fill a resolved name beside each id, by the SAME
// rules chat resolves a speaker (speakerNameOf in chat.go): a person is their
// registry handle, an agent their person's handle or else their runtime kind,
// and unnameable stays "" so no surface draws an id dressed as a name.

import (
	"context"
	"fmt"

	"github.com/lib/pq"
)

// FillAuthorNames resolves the display name for each artifact's owner and
// each note entry's actor, in place. One query for the whole page, so every
// row on it is judged against one state of the registries - the property a
// per-row lookup would lose, and the one FillDisowned buys the same way.
//
// A failure is the caller's to decide: the rows are what was asked for, and
// the name is an annotation on them. Every door that calls this logs and
// answers without it, exactly as the disowned fill beside it is treated.
func (d *DB) FillAuthorNames(ctx context.Context, arts []*Artifact) error {
	if len(arts) == 0 {
		return nil
	}
	ids := make([]string, 0, len(arts)*2)
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, a := range arts {
		if a == nil {
			continue
		}
		add(a.OwnerUser)
		for i := range a.Notes {
			add(a.Notes[i].Actor)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	// Two arms, keyed the way the speaker rule walks: a user's handle first,
	// then an agent's - which is their person's handle when there is one to
	// lend and their runtime kind when there is not. An id in both tables
	// resolves to the user's handle, because the union hands the user arm's
	// row first and the map keeps the first name it sees. An id naming
	// neither stays "" - unnameable, never the id.
	//
	// nullif rather than a bare handle: the Go rule treats '' as absent, and
	// the column stores '' rather than NULL, so a bare coalesce would hand
	// the agent arm an empty string to coalesce first - measured, the agent
	// came back unnameable while its kind sat in the table.
	rows, err := d.sql.QueryContext(ctx,
		`SELECT u.id, u.handle
		   FROM users u
		  WHERE u.id = ANY($1) AND coalesce(nullif(u.handle, ''), '') <> ''
		 UNION ALL
		 SELECT a.id, coalesce(nullif(u.handle, ''), a.kind)
		   FROM agents a
		   LEFT JOIN users u ON u.id = a.user_id
		  WHERE a.id = ANY($1) AND coalesce(nullif(u.handle, ''), a.kind) <> ''`,
		pq.Array(ids))
	if err != nil {
		return fmt.Errorf("store: resolve author names: %w", err)
	}
	defer rows.Close()

	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("store: resolve author names: %w", err)
		}
		if _, ok := names[id]; !ok {
			names[id] = name
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: resolve author names: %w", err)
	}

	for _, a := range arts {
		if a == nil {
			continue
		}
		a.Author = names[a.OwnerUser]
		for i := range a.Notes {
			a.Notes[i].ActorName = names[a.Notes[i].Actor]
		}
	}
	return nil
}
