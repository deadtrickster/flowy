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
