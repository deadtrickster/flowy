package store

import (
	"testing"
	"time"
)

// A PROCESS CLAIM IS COMPLETE OR IT IS NOTHING.
//
// MEASURED, four times across three seats in one night: `pkill -9 -f 'flowy
// inbox --as NAME'` killed the shell running it, exit 144, because the pattern
// matched the process evaluating the pattern. Every one of those was done by
// somebody who had already written the lesson down, which is the argument for a
// mechanism rather than a rule.
//
// The replacement is a pid the node hands over - but a pid alone is the same
// failure in a new costume, because pids are reused. These arms are about the
// pieces that make it an identity rather than a number.
func TestAProcessClaimIsCompleteOrNothing(t *testing.T) {
	now := time.Now().UTC()
	good := now.Add(-time.Hour).Format(time.RFC3339Nano)

	whole := WaiterProcessOf("4321", good, "dead-XMG")
	if !whole.Complete() {
		t.Fatalf("a complete claim was discarded: %+v", whole)
	}
	if whole.Pid != 4321 || whole.Host != "dead-XMG" || whole.Since == nil {
		t.Fatalf("the claim came back as %+v", whole)
	}

	for _, c := range []struct {
		name             string
		pid, since, host string
	}{
		// THE PID WITHOUT ITS START TIME is exactly the ambiguity this exists to
		// remove: it names whatever is wearing the number now.
		{"no start time", "4321", "", "dead-XMG"},
		{"unparseable start time", "4321", "yesterday", "dead-XMG"},
		// A PID WITHOUT A HOST is a number somebody might act on from the wrong
		// machine - and a federated node's reader pid means nothing here.
		{"no host", "4321", good, ""},
		{"no pid", "", good, "dead-XMG"},
		{"pid zero", "0", good, "dead-XMG"},
		{"negative pid", "-1", good, "dead-XMG"},
		{"not a number", "init", good, "dead-XMG"},
		// Past any pid the kernel can hand out: a typo, or another machine's
		// numbering. Storing it would put an actionable-looking number in front
		// of somebody.
		{"beyond the pid ceiling", "99999999", good, "dead-XMG"},
	} {
		if got := WaiterProcessOf(c.pid, c.since, c.host); got.Complete() {
			t.Fatalf("%s: a partial claim was accepted as %+v", c.name, got)
		} else if got.Pid != 0 || got.Since != nil || got.Host != "" {
			t.Fatalf("%s: a rejected claim kept part of itself: %+v", c.name, got)
		}
	}

	// A START TIME IN THE FUTURE cannot tell one process from another that
	// reused its pid, which is the only thing it is carried for. It is a clock
	// disagreement and the safe reading is to say nothing.
	ahead := now.Add(2 * time.Hour).Format(time.RFC3339Nano)
	if got := WaiterProcessOf("4321", ahead, "dead-XMG"); got.Complete() {
		t.Fatalf("a start time in the future was accepted: %+v", got)
	}

	// AND A SMALL SKEW IS NOT A FUTURE. A node and a waiter on different
	// machines disagree by seconds routinely, and refusing that would discard
	// every claim on a federated node.
	skewed := now.Add(30 * time.Second).Format(time.RFC3339Nano)
	if got := WaiterProcessOf("4321", skewed, "dead-XMG"); !got.Complete() {
		t.Fatalf("a thirty-second skew was treated as a future start: %+v", got)
	}
}
