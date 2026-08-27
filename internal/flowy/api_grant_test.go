package flowy

import (
	"os"
	"strings"
	"testing"
)

// THE GRANT DOOR IS THE OPERATOR'S AND CANNOT WIDEN ITSELF.
//
// 01M0FNQSZ2. Reach is this fabric's permission boundary: what a token may
// touch. A seat that could widen its own reach would make that boundary
// advisory, so the door goes through operatorOnly - the same wrapper the role
// door uses, and a wrapper rather than a check in the handler because a set of
// routes that all need one gate is a set where one eventually does not have it.
//
// Asserted against the registration rather than by driving a live node,
// because what makes this safe is which wrapper it is registered with, and
// that is a fact about one line. A test that posted to it as a non-operator
// would prove the wrapper works today and say nothing about the door being
// re-registered without it tomorrow.
func TestTheGrantDoorIsOperatorOnly(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	text := string(src)
	const route = `api.HandleFunc("POST /api/agent/{id}/projects"`
	i := strings.Index(text, route)
	if i < 0 {
		t.Fatal("the grant door is not registered")
	}
	line := text[i : i+strings.Index(text[i:], "\n")]
	if !strings.Contains(line, "s.operatorOnly(") {
		t.Errorf("the grant door is registered without operatorOnly:\n  %s\n"+
			"a seat that can widen its own reach makes reach advisory", line)
	}
}

// MINT STILL REPLACES, AND THE GRANT STILL ADDS.
//
// The two verbs say different things and the difference is the whole design: a
// mint that names a reach states the WHOLE of it, so that re-minting cannot
// widen a token by accident; a grant says "also this", once. If the grant ever
// grew a DELETE it would silently become a second mint, and a caller adding a
// project would narrow the seat to one.
func TestGrantAddsAndMintReplaces(t *testing.T) {
	src, err := os.ReadFile("../../internal/store/perm.go")
	if err != nil {
		t.Fatalf("read perm.go: %v", err)
	}
	text := string(src)
	i := strings.Index(text, "func (d *DB) GrantProject")
	if i < 0 {
		t.Fatal("GrantProject is gone")
	}
	body := text[i:]
	if j := strings.Index(body[1:], "\nfunc "); j > 0 {
		body = body[:j]
	}
	if strings.Contains(body, "DELETE FROM token_projects") {
		t.Error("the grant deletes reach - it is additive, and a DELETE here " +
			"turns adding a project into narrowing a seat to one")
	}
	if !strings.Contains(body, "ON CONFLICT DO NOTHING") {
		t.Error("the grant is not idempotent - re-running it is how somebody " +
			"makes sure, and making sure must not fail")
	}
	// And mint's rule must survive: it is the reason the grant is a separate
	// verb rather than a flag.
	if !strings.Contains(text, "DELETE FROM token_projects WHERE token = $1") {
		t.Error("mint no longer replaces the set - re-minting can now widen a " +
			"token by accident, which is the direction that matters")
	}
}
