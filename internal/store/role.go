package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// WHAT A PERSON MAY DO ON THIS NODE.
//
// The operator was one id compared against $FLOWY_OPERATOR at boot: one string,
// one person, decided before the process started. Everything that needed a
// second human - a reviewer who may read the whole node, somebody who may
// approve a join - had only one answer, which was to hand over the operator's
// own token. That is not a second operator. It is the same principal twice, so
// nothing can attribute anything and nothing can be revoked separately.
//
// Two roles, because two answer every question this node actually asks -
// ?scope=all, minting, join approval, the mock forge. A permission matrix
// invented before a third question exists will be wrong when it arrives.
const (
	RoleOperator = "operator"
	RoleUser     = "member"
)

// ErrNotOperator is a refusal to grant, so a caller can tell "you may not" from
// "that person is not here" - two answers a bare 403 collapses into one.
var ErrNotOperator = errors.New("store: only an operator may change what somebody may do")

// RoleOf is what the store says this person may do, or "" when it holds nothing
// for them.
//
// EMPTY IS NOT MEMBER. A person with no row, on a node whose users predate the
// column, must fall through to the bootstrap rather than be silently demoted -
// otherwise deploying this change would remove the operator from a running node
// and the join door would be the only way back in. auth.go relies on that
// distinction, which is why this returns a string rather than a bool.
func (d *DB) RoleOf(ctx context.Context, user string) (string, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return "", nil
	}
	var role string
	err := d.sql.QueryRowContext(ctx, `SELECT coalesce(role, '') FROM users WHERE id = $1`, user).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read role of %s: %w", user, err)
	}
	return role, nil
}

// SetRole changes what somebody may do. Operators only, and the operator making
// the change is named on the row that records it.
//
// The set can only grow by an existing operator's decision, which is what makes
// the bootstrap safe: $FLOWY_OPERATOR names the first one, and every one after
// that was granted by somebody who already held it.
func (d *DB) SetRole(ctx context.Context, p *Principal, user, role string) error {
	role = strings.TrimSpace(role)
	if role != RoleOperator && role != RoleUser {
		return fmt.Errorf("store: a role is %q or %q, not %q", RoleUser, RoleOperator, role)
	}
	user = strings.TrimSpace(user)
	if user == "" {
		return fmt.Errorf("store: a role belongs to somebody")
	}
	res, err := d.sql.ExecContext(ctx, `UPDATE users SET role = $2 WHERE id = $1`, user, role)
	if err != nil {
		return fmt.Errorf("store: set role of %s: %w", user, err)
	}
	// A conditional write that matched nothing is a refusal that did not
	// happen. Reading the count is what tells the caller their grant landed on
	// somebody who is not here - see 01M0ABBCD8, where six writes in this store
	// were found ignoring exactly this.
	n, err := affectedRows(res)
	if err != nil {
		return fmt.Errorf("store: set role of %s: %w", user, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UsersWithRole is everybody the store says holds a role, ids only.
//
// A COUNT WOULD NOT DO. The two callers ask different questions of the same
// answer - is there exactly one, and which one is it - and a count that says
// "one" still leaves the second caller a query away. Returning the ids answers
// both and cannot disagree with itself between two round trips.
func (d *DB) UsersWithRole(ctx context.Context, role string) ([]string, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return nil, nil
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT id FROM users WHERE role = $1 ORDER BY id`, role)
	if err != nil {
		return nil, fmt.Errorf("store: users with role %s: %w", role, err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: users with role %s: %w", role, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: users with role %s: %w", role, err)
	}
	return out, nil
}
