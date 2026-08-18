package store

import (
	"context"
	"fmt"
)

// Who this node would take a peer's word about.
//
// The rule above is per-principal and it only bites where a key is: a row
// naming a principal this node holds a key for, at or after that key's epoch,
// must carry their signature. A principal this node holds NO key for has no
// such rule - authorshipOf returns attributed and the row lands - so a pinned
// peer may author rows under that name and this node will store them and show
// them to that person in their own room. Measured, with a positive control
// either side of the pin: 01M0AG9HVG.
//
// THE THREE DESCRIPTIONS THAT LOOKED LIKE FIXES AND ARE NOT are written down in
// that row, and the short version is that metadata describes and does not
// attest: users.node is stamped by whatever seeded the row, isLocalUser is true
// of a peer's user one sync after it arrives, and holding a token here is true
// of BOTH machines of one person, which is what having two machines means. No
// rule of that shape can separate "my other machine relaying my row" from "a
// hostile pinned peer forging my row". Only a key can, because only a key had
// to be USED.
//
// So the exposure is not a bug to be reasoned away, it is a provisioning gap:
// these principals need keys. What was missing was not the mechanism - `flowy
// principal keygen` and `flowy principal pin` have both existed since the rule
// did - but the LIST. An operator cannot provision names nobody has counted,
// and this node knew every one of them and said nothing.
//
// MINTING ONE AUTOMATICALLY AT USER CREATION WAS MEASURED AND IS THE SAME TRAP
// IN A NEW COSTUME: the two-node fixture seeds the same person on both nodes,
// which is precisely the shape of one person with two machines, and each node
// would mint them a DIFFERENT key. The second node would then refuse the first
// node's relayed rows about their own person from the epoch of a key that
// person has never held. One principal, one key, distributed by the operator,
// is the only arrangement that is not a rule refusing federation.

// UnkeyedPrincipal is one name a pinned peer could speak for here.
type UnkeyedPrincipal struct {
	// Principal is the user id or agent id, as it appears in an event's actor
	// column - which is what `flowy principal keygen --as` takes.
	Principal string `json:"principal"`
	// Handle is what the room calls them, empty for a principal this node has
	// rows from and no user or agent row for. An agent's name is its user's:
	// the agents table has no handle of its own, which is the same reason the
	// roster reads one through the other.
	Handle string `json:"handle,omitempty"`
	// Rows is how many rows on this node already name them as their author. It
	// is the size of what a forgery would be mixed into, not a severity: one
	// row is enough to be worth a key.
	Rows int `json:"rows"`
	// Credentialed says this node authenticates them - they hold a token here,
	// so this is a machine they write from.
	//
	// It decides WHICH COMMAND closes it, and nothing else. As an accept rule
	// it is the discriminator that failed (see above); as advice it is exactly
	// right, because a principal who writes here needs a key made here, and a
	// principal who writes somewhere else needs the key from there pinned here.
	// A description is allowed to guide an operator. It is not allowed to
	// decide what this node believes.
	Credentialed bool `json:"credentialed"`
}

// Fix is the one command that closes this principal's exposure on this node.
func (u UnkeyedPrincipal) Fix() string {
	if u.Credentialed {
		return "flowy principal keygen --as " + u.Principal
	}
	return "flowy principal pin --as " + u.Principal +
		" --key <their public key from the node they write from>"
}

// UnkeyedPrincipals is every principal with rows on this node and no key here,
// most rows first.
//
// The reach is deliberately the WHOLE NODE and not a reader's projects: this is
// the operator's own question about their own machine, asked from the command
// line against the database, and an answer filtered by what some principal may
// read would be an answer that quietly leaves names exposed. Nothing here is
// content - an id, a handle, a count - so there is nothing in it to leak that a
// roster does not already say.
func (d *DB) UnkeyedPrincipals(ctx context.Context) ([]UnkeyedPrincipal, error) {
	rows, err := d.sql.QueryContext(ctx,
		`WITH authored AS (
		     SELECT actor AS principal, count(*) AS rows FROM events
		      WHERE actor IS NOT NULL AND actor <> '' GROUP BY actor
		     UNION ALL
		     SELECT owner_user AS principal, count(*) AS rows FROM artifacts
		      WHERE owner_user IS NOT NULL AND owner_user <> '' GROUP BY owner_user
		 ), totals AS (
		     SELECT principal, sum(rows)::int AS rows FROM authored GROUP BY principal
		 )
		 SELECT t.principal,
		        coalesce(u.handle, au.handle, '') AS handle,
		        t.rows,
		        EXISTS (SELECT 1 FROM tokens k
		                 WHERE k.user_id = t.principal OR k.agent_id = t.principal) AS credentialed
		   FROM totals t
		   LEFT JOIN users u ON u.id = t.principal
		   LEFT JOIN agents a ON a.id = t.principal
		   LEFT JOIN users au ON au.id = a.user_id
		  WHERE NOT EXISTS (SELECT 1 FROM principal_identity pi WHERE pi.principal = t.principal)
		  ORDER BY t.rows DESC, t.principal`)
	if err != nil {
		return nil, fmt.Errorf("store: read the principals with no key: %w", err)
	}
	defer rows.Close()
	out := []UnkeyedPrincipal{}
	for rows.Next() {
		var u UnkeyedPrincipal
		if err := rows.Scan(&u.Principal, &u.Handle, &u.Rows, &u.Credentialed); err != nil {
			return nil, fmt.Errorf("store: read a principal with no key: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read the principals with no key: %w", err)
	}
	return out, nil
}
