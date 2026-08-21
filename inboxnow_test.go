package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// THE ANSWER SAYS WHAT TIME IT IS, by the clock its timestamps came from.
//
// The operator: "change chat signal so it always includes the current time
// (messages should include theirs too). why? agents keep getting time wrong."
//
// Both halves are asserted here, and the second is the one that was already
// true: a message carries `created`, and now the page it arrives on carries the
// node's clock beside it. A reader handed "[03:10]" and nothing else cannot
// tell a minute ago from an hour ago, and an agent session is handed a date
// once and keeps it for hours - so the correction has to arrive unasked.
func TestADeliverySaysWhatTimeTheNodeThinksItIs(t *testing.T) {
	before := time.Now().UTC().Add(-time.Second)
	now := nodeNow()
	after := time.Now().UTC().Add(time.Second)

	got, err := time.Parse(time.RFC3339, now)
	if err != nil {
		t.Fatalf("the node clock is not RFC3339: %q (%v)", now, err)
	}
	if got.Before(before) || got.After(after) {
		t.Errorf("the node clock reads %s, which is not now", now)
	}
	// UTC, and said so in the string. A time with no zone, or with the box's
	// local one, is read by four agents in different places as four times.
	if !strings.HasSuffix(now, "Z") {
		t.Errorf("the node clock is not in UTC: %q", now)
	}

	// AND IT SURVIVES THE WIRE under the name the readers use. The field is
	// what the CLI and the chat hook read; a rename here is a signal that
	// silently loses its clock, which reads exactly like an older node.
	page := inboxWaitResponse{Reader: "orchestrator", Now: now}
	body, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back["now"] != now {
		t.Errorf("the wait payload carries %v under \"now\", want %q", back["now"], now)
	}
}

// AND AN OLDER NODE IS ANSWERED WITH SILENCE, not with this machine's clock.
//
// A waiter talks to one node, and the timestamps it prints are that node's. A
// line reading "node clock: ..." taken from the reader's own box would be the
// unverifiable comparison this change exists to remove, and it would look
// exactly like the real thing - which is the worst of the three outcomes.
func TestAnOlderNodeGetsNoInventedClock(t *testing.T) {
	// reportNow writes to stderr and returns nothing, so what is asserted is
	// the branch: an empty or blank clock must not be printed. The test drives
	// it for the panic-free path and the source for the rule.
	reportNow("")
	reportNow("   ")

	src := readSource(t, "inbox.go")
	if !strings.Contains(src, `if strings.TrimSpace(now) == ""`) {
		t.Error("reportNow no longer guards an absent clock - an older node would get one invented for it")
	}
	if strings.Contains(src, "reportNow(time.Now()") {
		t.Error("the delivery line is printing this machine's clock rather than the node's")
	}
}
