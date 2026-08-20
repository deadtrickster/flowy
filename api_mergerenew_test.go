package main

import (
	"os"
	"strings"
	"testing"
)

// THE RENEW DOOR MUST NOT BE ABLE TO TAKE A LOCK.
//
// Asserted against the source rather than a database, for the same reason
// internal/store/mergerenew_test.go asserts its half that way: what makes this
// door safe is not a value it computes but a verb it never calls. A test that
// drove it against a live store would prove the happy path and say nothing
// about the one line that would turn a heartbeat into a theft.
//
// 01M0EBXHQ3 exists because nothing renewed the lock DURING a gate, and the
// obvious fix - "declare again" - is what this must not become: a re-declare
// rewrites gate_run and clears gated_tip, so renewing that way destroys the
// verdict it is renewing for. See RenewMergeLock's head comment.
func TestTheRenewDoorRenewsAndNeverTakes(t *testing.T) {
	src, err := os.ReadFile("api_mergerenew.go")
	if err != nil {
		t.Fatalf("read api_mergerenew.go: %v", err)
	}
	text := string(src)

	// The CALL shape, not the bare name: this file's own comments explain why
	// taking is forbidden, and a test that trips on the explanation would teach
	// the next author to delete the reasoning rather than keep the rule.
	for _, forbidden := range []string{"s.db.TakeMergeLock(", "s.db.SetMergeGate("} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the renew door calls %s - renewing something you do not hold is taking it, "+
				"and a re-declare would clear the gated_tip this exists to protect", forbidden)
		}
	}
	if !strings.Contains(text, "RenewMergeLock") {
		t.Error("the renew door does not call RenewMergeLock")
	}
	// A FALSE IS AN ANSWER, NOT AN ERROR, and the caller has to be able to tell
	// "your window lapsed" from "somebody else holds it" - those want different
	// responses, so the refusal carries the lock.
	for _, want := range []string{"if !held {", "StatusConflict", `"lock":  lock`} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal path is missing %q - a bare no leaves the caller unable to act", want)
		}
	}
}
