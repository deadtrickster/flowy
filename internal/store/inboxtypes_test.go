package store

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// THE MARK IS COMPUTED OVER WHAT THE INBOX DELIVERS.
//
// 01M1PEMY3C9BJHR6GW15A6K1SD. 01M17CVHH9FX3YVTHT9WMFSDDY widened the wait door
// to carry a note as well as a message, and left inboxHead - where a newly
// declared waiter is marked - computing max(seq_hlc) over chat alone. So a
// waiter declared at the head started above the newest CHAT event, and any note
// written before that event sat below its mark and was never delivered, for as
// long as that reader existed.
//
// Nothing could notice, because the two sets were two literals in two files in
// two packages. One half of a door was widened and the other kept its old idea
// of what an event is.
//
// SO THE SET IS ONE VARIABLE AND THIS ASSERTS BOTH SIDES READ IT. Sharing alone
// is not enough to keep: the next person to add a type can as easily write a
// third literal, and the sharing would still be true of the two that exist.
//
// A SOURCE WALK, in the family of TestEveryRegisteredAPIRouteIsAdvertised and
// TestEveryAgentKindSaysWhetherItGates: the property is about what the tree
// declares, and no request exercises the difference between a mark and a
// delivery until somebody's answer goes missing for seventy minutes.
func TestTheInboxMarkAndItsDeliveryReadOneSet(t *testing.T) {
	if len(InboxDeliveredTypes) < 2 {
		t.Fatalf("InboxDeliveredTypes holds %d type(s): a set this small cannot be the thing two doors disagree about, "+
			"so either it has been narrowed or this test is reading the wrong variable", len(InboxDeliveredTypes))
	}
	// The set has to contain what it was widened FOR, or the widening has been
	// undone and every note is silent again.
	want := map[string]bool{ChatEventType: false, EventTodoNote: false}
	for _, k := range InboxDeliveredTypes {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("InboxDeliveredTypes does not contain %q, so the inbox no longer delivers it - "+
				"and the door that used to name it directly will not either", k)
		}
	}

	src, err := os.ReadFile("inbox.go")
	if err != nil {
		t.Fatalf("read inbox.go: %v", err)
	}
	head := string(src)
	// inboxHead's body, which is the half that was left behind.
	start := strings.Index(head, "func (d *DB) inboxHead(")
	if start < 0 {
		t.Fatal("inboxHead is not in inbox.go any more, so this walk is looking at the wrong file - " +
			"find where the mark is computed and point it there rather than deleting this")
	}
	body := head[start:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "InboxDeliveredTypes") {
		t.Error("inboxHead does not read InboxDeliveredTypes, so the mark is computed over a set of its own again - " +
			"which is the exact shape that made a note land below the mark of every waiter declared after it")
	}
	// AND THE BARE CONSTANT IS NOT BACK. Reading the shared set AND filtering on
	// ChatEventType beside it would satisfy the check above and restore the bug.
	if regexp.MustCompile(`a\.next\(\s*ChatEventType\s*\)`).MatchString(body) {
		t.Error("inboxHead binds ChatEventType directly as well - a mark that reads the shared set and then " +
			"narrows to chat is the original defect wearing the fix's clothes")
	}
	t.Logf("the inbox delivers %d type(s) and marks over the same variable: %v",
		len(InboxDeliveredTypes), InboxDeliveredTypes)
}
