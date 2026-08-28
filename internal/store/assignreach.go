package store

// CAN THE SEAT YOU ARE HANDING WORK TO SEE IT.
//
// Assignment takes a handle and, until this file, asked nothing about what that
// handle can reach. Every refusal on the way in is about the CALLER - may you
// read this row, are you the holder - and none of them is about the party the
// row is being handed TO. So a row in one project could be given to a seat
// holding no credential for that project, and the failure surfaced later and
// somewhere else: the agent polls, sees nothing, and reports that it has no
// work, which is indistinguishable from a quiet queue. That is the shape this
// fabric has spent two days removing - a fact discoverable only by the party
// that cannot act on it.
//
// REACH IS A PROPERTY OF A TOKEN, NOT OF AN AGENT, which is the part that makes
// this more than a column comparison. token_projects keys on the token (see
// Principal.Reach), so what a seat can reach is the union over every credential
// naming it: t.project for each of its tokens, plus the rows those tokens carry
// in token_projects. Checking agents.project alone would refuse work to a seat
// holding a perfectly good two-project credential - the same failure this rule
// exists to stop, pointed the other way.
//
// AND "I CANNOT SAY" IS NOT "NO". A party with no token at all is the ordinary
// case for a person who has not been given one yet, and for every handle on a
// board that predates credentials. Refusing there would break assignment for a
// defect nobody has. So the refusal fires only when the party is KNOWN, holds
// at least one credential, and none of them reaches the row's project - which
// is the only state where the answer is certain and wrong. It is GatingAt's
// rule and BlockedAt's rule: an absent measurement reads as nothing to say.

import (
	"context"
	"fmt"
	"strings"
)

// reachOfParty is every project the credentials naming this party can read, and
// whether the party holds any credential at all.
//
// The second answer is not a detail: no credential means the question is
// unanswerable, and this file's whole rule is that unanswerable is not a
// refusal.
func (d *DB) reachOfParty(ctx context.Context, party string) (projects []string, credentialed bool, err error) {
	party = strings.TrimSpace(party)
	if party == "" {
		return nil, false, nil
	}
	// One query over both halves of a credential: the project a token acts in,
	// and the projects it additionally reaches. A token with neither still
	// counts as a credential - it is why `credentialed` is separate from the
	// length of the list.
	rows, err := d.sql.QueryContext(ctx,
		`SELECT coalesce(t.project, ''), coalesce(tp.project, '')
		   FROM tokens t
		   LEFT JOIN token_projects tp ON tp.token = t.token
		  WHERE t.user_id = $1 OR t.agent_id = $1`, party)
	if err != nil {
		if isUndefinedTable(err) {
			// A database that predates token_projects answers the question with
			// the acting project alone, which is the true answer there - see
			// PrincipalForToken, which draws the same line.
			return d.reachOfPartyWithoutSet(ctx, party)
		}
		return nil, false, fmt.Errorf("store: reach of %s: %w", party, err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var acting, extra string
		if err := rows.Scan(&acting, &extra); err != nil {
			return nil, false, fmt.Errorf("store: reach of %s: %w", party, err)
		}
		credentialed = true
		for _, project := range []string{acting, extra} {
			if project == "" || seen[project] {
				continue
			}
			seen[project] = true
			projects = append(projects, project)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("store: reach of %s: %w", party, err)
	}
	return projects, credentialed, nil
}

// reachOfPartyWithoutSet is the same question on a database that has no
// token_projects table yet.
func (d *DB) reachOfPartyWithoutSet(ctx context.Context, party string) ([]string, bool, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT coalesce(t.project, '') FROM tokens t
		  WHERE t.user_id = $1 OR t.agent_id = $1`, party)
	if err != nil {
		return nil, false, fmt.Errorf("store: reach of %s: %w", party, err)
	}
	defer rows.Close()

	var projects []string
	seen := map[string]bool{}
	credentialed := false
	for rows.Next() {
		var acting string
		if err := rows.Scan(&acting); err != nil {
			return nil, false, fmt.Errorf("store: reach of %s: %w", party, err)
		}
		credentialed = true
		if acting != "" && !seen[acting] {
			seen[acting] = true
			projects = append(projects, acting)
		}
	}
	return projects, credentialed, rows.Err()
}

// refuseOutOfReach is the sentence a caller reads when the seat they named
// cannot see the row.
//
// BOTH NAMES ARE IN IT, and the projects on both sides, because the caller has
// to decide which of the two is wrong: the row is in the wrong project, or the
// seat needs a credential that reaches it. A refusal that said only "not
// allowed" would send them to re-read a token that was never the problem.
func refuseOutOfReach(name, id, project string, reach []string) error {
	where := "no project"
	if len(reach) > 0 {
		where = strings.Join(reach, ", ")
	}
	// Once, not twice - see assign.go's site.
	return refuseAssign(
		"%s cannot read work in project %s - every credential naming %s reaches %s. "+
			"Give that seat a token in %s, or file the row where they can see it",
		name, project, id, where, project)
}

// assigneeCanReach refuses an assignment to a party that certainly cannot see
// the row, and says nothing when it cannot tell.
func (d *DB) assigneeCanReach(ctx context.Context, art *Artifact, name string) error {
	if art == nil || art.Project == nil || strings.TrimSpace(*art.Project) == "" {
		// A personal row is its owner's and reaches nobody by project. There is
		// no ceiling to check against.
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" || NobodyName(name) {
		// Putting a row down names nobody, and nobody has no reach to check.
		return nil
	}
	ids, err := d.PrincipalsNamed(ctx, []string{name})
	if err != nil {
		return err
	}
	id := ids[strings.ToLower(name)]
	if id == "" {
		// Unknown, or a name two principals answer to. Either way this cannot
		// say, and cannot-say is not no.
		return nil
	}
	reach, credentialed, err := d.reachOfParty(ctx, id)
	if err != nil {
		return err
	}
	if !credentialed {
		return nil
	}
	for _, project := range reach {
		if project == *art.Project {
			return nil
		}
	}
	return refuseOutOfReach(name, id, *art.Project, reach)
}
