package store

import (
	"os"
	"strings"
	"testing"
)

// EMPTY IS NOT MEMBER, and this is the case that makes the deploy survivable.
//
// A node whose users predate the role column holds no role for anybody. If that
// read as "member", deploying this change would demote the running node's
// operator - and the only way back in would be the join door, on a node whose
// operator can no longer approve one. auth.go falls through to the bootstrap
// exactly when this returns "".
func TestNoRoleIsNotTheSameAsMember(t *testing.T) {
	if RoleUser == "" {
		t.Fatal("member must be a value, so that absent and member can differ")
	}
}

// Two roles, and the vocabulary is closed. A third would need a third question
// to answer, and inventing it early means inventing it wrong.
func TestARoleIsOneOfTwo(t *testing.T) {
	if RoleOperator == RoleUser {
		t.Fatal("the two roles must differ")
	}
	for _, bad := range []string{"", "admin", "owner", "OPERATOR", "root"} {
		if bad == RoleOperator || bad == RoleUser {
			t.Errorf("%q should not be a role", bad)
		}
	}
}

// THE ARM NOBODY DRIVES: a store with no operator in it.
//
// This is the state of every node that predates the column, including the live
// one at the moment this deploys. If RoleOf answered "member" there, isOperator
// would refuse the bootstrap operator and the node would have nobody who can
// grant the role back - a safety mechanism that locks everyone out, which is
// the same failure the land guard's escape hatch exists to prevent.
//
// The distinction lives in the return type, so this asserts the type's promise
// rather than a database: RoleOf must be able to say "I hold nothing for this
// person", and that must not be spellable as either role.
func TestAStoreWithNoOperatorFallsThroughRatherThanLockingOut(t *testing.T) {
	const nothingHeld = ""
	if nothingHeld == RoleOperator || nothingHeld == RoleUser {
		t.Fatal("absent must be distinguishable from both roles, or the bootstrap cannot be reached")
	}
	// And the caller's side of the contract, stated here because auth.go is
	// where it is relied on: only a NON-empty role decides, and an empty one
	// falls through to $FLOWY_OPERATOR.
	decides := func(role string) bool { return role != "" }
	if decides(nothingHeld) {
		t.Error("an empty role must not decide - it is the signal to consult the bootstrap")
	}
	if !decides(RoleOperator) || !decides(RoleUser) {
		t.Error("a role the store holds must decide, or the store is not authoritative")
	}
}

// AND THE STATE HAS TO BE REACHABLE, which is what the case above did not prove.
//
// The first version of this schema said `role text NOT NULL DEFAULT 'member'`.
// Every test above still passed - they assert the code path, and the code path
// was right - while the column made "absent" impossible, so isOperator's
// fallback to $FLOWY_OPERATOR could never fire and every operator-only route
// refused the operator. Seventeen suite checks caught it; these did not.
//
// So this one reads the schema. A test that asserts a fallback for a state the
// store cannot hold is a fixture that cannot occur.
func TestTheAbsentRoleIsAStateTheStoreCanActuallyHold(t *testing.T) {
	// Comments are stripped first. The block below explains the lockout in
	// prose that contains the very words being searched for, so a check reading
	// raw text finds its own warning and fails - which is measuring the text
	// rather than the schema, the same error that produced this bug.
	schema := withoutSQLComments(readSchema(t))
	users := schema[indexFold(schema, "CREATE TABLE IF NOT EXISTS users"):]
	users = users[:indexFold(users, ");")]
	if containsFold(users, "role") && containsFold(users, "NOT NULL") {
		t.Error("users.role must be nullable - a NOT NULL default makes the bootstrap unreachable")
	}
	alter := schema[indexFold(schema, "ALTER TABLE users ADD COLUMN IF NOT EXISTS role"):]
	alter = alter[:indexFold(alter, ";")]
	if containsFold(alter, "DEFAULT") {
		t.Error("the migration must not default role - it would fill in every existing user and lock the operator out")
	}
}

// withoutSQLComments drops -- lines so a check reads what the database will be
// told, not what a human was told about it.
func withoutSQLComments(sql string) string {
	out := make([]string, 0, 256)
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func readSchema(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// And the check itself goes red on the schema that caused the outage, so it is
// not passing because it looks at nothing.
func TestTheReachabilityCheckRejectsTheSchemaThatLockedTheOperatorOut(t *testing.T) {
	bad := `CREATE TABLE IF NOT EXISTS users (
    id            text PRIMARY KEY,
    role          text NOT NULL DEFAULT 'member'
);`
	users := bad[indexFold(bad, "CREATE TABLE IF NOT EXISTS users"):]
	users = users[:indexFold(users, ");")]
	if !(containsFold(users, "role") && containsFold(users, "NOT NULL")) {
		t.Fatal("the check would not have caught the schema that shipped - it proves nothing")
	}
}
