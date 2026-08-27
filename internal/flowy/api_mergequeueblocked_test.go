package flowy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// A ROW WHOSE BLOCK NOBODY HAS RE-CHECKED IS NOT A CLEAR ROW, at the door.
//
// queueBlockedOf dropped an aged-out skip, so the queue answer for a row whose
// branch was still checked out somewhere was byte-identical to the answer for a
// row with nothing wrong with it. Every reader downstream - the CLI, the
// console, an agent through MCP - then drew a clear row, correctly, from an
// answer that had lost the distinction before it left the node.
func TestTheQueueSaysWhenNobodyHasRecheckedABlock(t *testing.T) {
	now := time.Now().UTC()
	row := func(at time.Time) *store.Artifact {
		fields, err := json.Marshal(map[string]any{
			store.BlockedWhyField: "feat/x is checked out in /home/dead/Projects/wt-drain",
			store.BlockedAtField:  at.Format(time.RFC3339Nano),
		})
		if err != nil {
			t.Fatalf("fields: %v", err)
		}
		return &store.Artifact{Fields: fields}
	}

	fresh := queueBlockedOf(row(now), now.Add(time.Minute))
	if fresh == nil {
		t.Fatalf("a skip one minute old was dropped")
	}
	if fresh.Stale {
		t.Errorf("a skip one minute old is marked stale")
	}

	old := queueBlockedOf(row(now), now.Add(store.BlockBelievedFor+time.Minute))
	if old == nil {
		t.Fatalf("a skip %v old was dropped - the row now reads as clear, which is the bug",
			store.BlockBelievedFor)
	}
	if !old.Stale {
		t.Errorf("a skip %v old is not marked stale", store.BlockBelievedFor)
	}
	// THE REASON SURVIVES. Marking it stale and throwing away what it said would
	// move the problem rather than fix it: "something was wrong once" is not
	// something a person can act on, and the path is the actionable half.
	if !strings.Contains(old.Why, "wt-drain") {
		t.Errorf("the stale block reads %q, without the path somebody has to go and free", old.Why)
	}

	// AND A ROW WITH NO SKIP IS STILL NIL, which is the answer the other two
	// must be distinguishable from.
	if got := queueBlockedOf(&store.Artifact{}, now); got != nil {
		t.Errorf("a row with no skip produced %+v", got)
	}

	// FALSE ARRIVES AS FALSE. With omitempty a fresh block and a stale one are
	// told apart by the ABSENCE of a field, which is exactly the shape this
	// change exists to remove - a reader that does not know the field exists
	// cannot tell them apart at all.
	wire, err := json.Marshal(fresh)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(wire), `"stale":false`) {
		t.Errorf("a fresh block goes on the wire as %s - stale must be present and false", wire)
	}
}
