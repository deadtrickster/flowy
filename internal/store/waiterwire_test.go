package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAWaiterThatSaidNothingSaysNothing pins the SHAPE of a listener that has
// claimed no process, because that shape turned out to depend on the toolchain
// rather than on the code.
//
// Listener tags the claim `omitzero`, which arrived in Go 1.24. An older
// toolchain does not reject an unknown tag option - it ignores it - and the
// field is then emitted always, as `{}`, because the fields inside carry
// omitempty. Absent and empty are different answers here: absent is "this
// waiter never said which process it is", and `{}` reads as "it said, and there
// was nothing to say".
//
// go.mod's minimum is the real guard - an old toolchain now refuses to build
// rather than building this quietly wrong. This test is what says why, and it
// fails on 1.22 rather than passing there with a different meaning.
func TestAWaiterThatSaidNothingSaysNothing(t *testing.T) {
	quiet, err := json.Marshal(PresenceRow{Reader: "roster-forked"})
	if err != nil {
		t.Fatalf("marshalling a listener: %v", err)
	}
	if strings.Contains(string(quiet), `"process"`) {
		t.Fatalf(`a waiter that claimed no process carries a process key: %s

That is empty where the answer is absent. On a toolchain older than 1.24 the
omitzero tag is ignored, which is exactly what this looks like.`, quiet)
	}

	// THE OTHER HALF, so the key is not simply always missing. A complete claim
	// must appear, or this test would pass against a field nobody serialises.
	pid := 4242
	said, err := json.Marshal(PresenceRow{
		Reader:  "roster-tracked",
		Process: WaiterProcess{Pid: pid, Host: "roster-check-host"},
	})
	if err != nil {
		t.Fatalf("marshalling a listener that claimed: %v", err)
	}
	if !strings.Contains(string(said), `"waiter_pid":4242`) {
		t.Fatalf("a waiter that claimed pid %d did not carry it: %s", pid, said)
	}
}
