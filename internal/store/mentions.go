package store

// Turning a name somebody wrote into the principal it names.
//
// The roster is the definition, and this is its inverse. A room draws a user
// under their registry handle and an agent under the name the room knows it by
// - see speakerName in chat.go and RoomMembers above - so those are the two
// names an @ can be, and resolving anything else would be inventing a naming
// scheme that exists nowhere on screen.

import (
	"context"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// MentionsMetaKey is the meta key the resolved @names of a message ride in.
//
// It is the node's to write, like the actor keys and the trace id beside it,
// and for the same reason: it says which words in a body the node decided were
// people and which principals they are. A client that could write its own
// would be drawing somebody else's name - or the reader's own - on a message
// that named nobody, on a row that is correctly signed and correctly actored.
// So it is stripped off meta a client hands in - see speakerStripped.
const MentionsMetaKey = "mentions"

// PrincipalsNamed maps each name that means exactly one principal here to that
// principal's id, keyed by the lowercase name. Names nothing answers to are
// absent from the answer rather than an error: an @ in prose is prose, and the
// caller leaves it as text.
//
// Case-insensitive, because "@Alice" at the start of a sentence is the same
// person as "@alice" in the middle of one, and a reader who has to remember
// which is a colder version of no feature at all.
//
// AMBIGUITY IS NOT A GUESS. A name that two principals answer to resolves to
// neither, so the mention stays plain text and the writer can see it did not
// take. Handles are unique in the schema, so the case this really covers is
// the agent branch below, where a runtime kind is not unique at all - and
// picking one of two agents called claude would address the wrong one silently,
// which is the failure the addressee field exists to prevent rather than cause.
//
// It tells a caller nothing they could not already learn from POST /api/assign
// or from an addressed say, both of which have answered "no such principal"
// since Phase 4.
func (d *DB) PrincipalsNamed(ctx context.Context, names []string) (map[string]string, error) {
	out := map[string]string{}
	if len(names) == 0 {
		return out, nil
	}
	lowered := make([]string, 0, len(names))
	for _, name := range names {
		lowered = append(lowered, strings.ToLower(name))
	}

	// The agent half is the inverse of speakerName's fallback, and it is
	// narrowed the same way: an agent speaks under the handle of the person it
	// acts for, and is called by its runtime kind only when that person has no
	// handle to lend. So an agent answers to its kind exactly when the room
	// would have drawn it under that kind, and @alice reaches the person -
	// which also reaches their agent, because a waiter wakes for either half of
	// its own principal.
	rows, err := d.sql.QueryContext(ctx,
		`SELECT lower(u.handle), u.id
		   FROM users u
		  WHERE u.handle IS NOT NULL AND lower(u.handle) = ANY($1)
		 UNION ALL
		 SELECT lower(a.kind), a.id
		   FROM agents a
		   LEFT JOIN users u ON u.id = a.user_id
		  WHERE a.kind IS NOT NULL AND lower(a.kind) = ANY($1)
		    AND coalesce(u.handle, '') = ''
		 UNION ALL
		 -- THE ROLE ARM. A role is a name people already use for a person, and
		 -- on this node it is the one they use most: four seats address the
		 -- operator as @operator every day and the node resolved none of them,
		 -- so the ring that says "this is for you" never appeared on the only
		 -- surface the operator reads. 01M0GGSM99 listed three causes and this
		 -- was a fourth - measured by seeding one message: @deadtrickster and
		 -- @orchestrator resolved, @operator did not.
		 --
		 -- It is narrowed to roles the store DEFINES, not to any word in the
		 -- column, so this cannot become a naming scheme by somebody writing a
		 -- new value into a row. And it feeds the same map as the two arms
		 -- above, so two operators collide there and the name resolves to
		 -- neither - which is the rule this file already had, applied to a
		 -- name that is now allowed to be shared.
		 SELECT lower(u.role), u.id
		   FROM users u
		  WHERE u.role = $2 AND lower(u.role) = ANY($1)`,
		pq.Array(lowered), RoleOperator)
	if err != nil {
		return nil, fmt.Errorf("store: resolve names: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			return nil, fmt.Errorf("store: resolve names: %w", err)
		}
		if seen, found := out[name]; found && seen != id {
			// Two principals, one name: mark it taken by nobody. The empty
			// string cannot be an id, and dropping the key instead would let a
			// third row put the name back.
			out[name] = ""
			continue
		}
		out[name] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: resolve names: %w", err)
	}
	for name, id := range out {
		if id == "" {
			delete(out, name)
		}
	}
	return out, nil
}
