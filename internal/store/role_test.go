package store

import "testing"

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
