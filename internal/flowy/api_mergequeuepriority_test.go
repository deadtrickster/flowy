package flowy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// A RANKED ROW REACHES THE QUEUE ANSWER, at the item level.
//
// The operator asked for priorities on todos AND merges, and the door stores
// the word on either kind - but the queue projection dropped it, so every
// surface that reads /api/merge-queue saw nothing to draw and nothing to set.
// The word travels here on purpose: the queue ORDER still keys on Queued (the
// operator settled FIFO for the time being), so this field is what the pane
// draws, not what the drainer takes.
//
// "" IS A REAL ANSWER AND MUST SURVIVE. A row nobody has judged is a different
// fact from an older node that does not rank at all, and a field that vanished
// when empty would make the two indistinguishable on the wire - see
// priorityView in priority.go, which made the same call for the write door's
// answer.
func TestTheQueueItemCarriesPriority(t *testing.T) {
	fields, err := json.Marshal(map[string]any{store.PriorityField: "now"})
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	ranked := &store.Artifact{Fields: fields}

	it := queueItemOf(ranked, "", nil, false, time.Now().UTC())
	if it.Priority != "now" {
		t.Errorf("a row ranked now came through the queue as %q", it.Priority)
	}

	// AND AN UNJUDGED ROW SAYS SO, rather than dropping the key.
	unjudged := queueItemOf(&store.Artifact{}, "", nil, false, time.Now().UTC())
	if unjudged.Priority != "" {
		t.Errorf("a row nobody ranked came through the queue as %q", unjudged.Priority)
	}
}
