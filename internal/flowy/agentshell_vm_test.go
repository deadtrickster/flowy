//go:build linux

package flowy

// THE ARM THE GATE CANNOT RUN: that the shell is the GUEST'S.
//
// Everything else about this relay is asserted in checks.d/console/vm-shell.sh.
// This one boots a real firecracker VM, so it must not run in the suite: the
// gate normally runs INSIDE a firecode VM, and firecode inside a firecode guest
// is nested virtualisation this host does not offer. It would also leave a VM
// behind on any run that died between start and stop.
//
// So it is opt-in, by name, on a machine that has firecode:
//
//	FLOWY_VM_SHELL=1 go test ./internal/flowy/ -run TestAShellIsTheGuests -v
//
// IT IS IN THE REPO RATHER THAN IN SOMEBODY'S SHELL HISTORY because a
// measurement nobody else can repeat is a claim, not a measurement. The row
// carries the reading; this carries the way to take it again.
//
// WHAT IT ASSERTS IS A DIFFERENCE, and it is the whole point of the row: the
// hostname the shell answers must NOT be this machine's. A relay accidentally
// wired to a local shell passes every other arm in the set - it draws a prompt,
// it echoes what you type, it renders colours - and is completely wrong.

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAShellIsTheGuests(t *testing.T) {
	if os.Getenv("FLOWY_VM_SHELL") == "" {
		t.Skip("set FLOWY_VM_SHELL=1 to boot a real VM; this does not run in the gate")
	}
	binary, err := exec.LookPath("firecode")
	if err != nil {
		t.Skip("no firecode on PATH")
	}
	mine, err := os.Hostname()
	if err != nil {
		t.Fatalf("asking this machine its name: %v", err)
	}

	shells := newAgentShells()
	// The DIRECTORY, because that is what `firecode shell --project` takes. The
	// node resolves a name to one; this test is below that layer.
	sess, err := shells.start("vmshelltest", "", os.Getenv("FLOWY_VM_DIR"), binary, agentSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("starting the shell: %v", err)
	}
	defer shells.stop("vmshelltest", "the test finished")

	ch, _, _ := sess.attach()
	defer sess.detach(ch)

	// A CANARY, AND TYPED UNTIL IT IS ANSWERED.
	//
	// The first attempt typed once, on the first output, and timed out: the
	// first output is the boot banner, which arrives long before there is a
	// shell to read a keystroke. Waiting for a prompt instead is unreliable
	// across shells, so this retypes on a timer until the answer comes back.
	//
	// The canary is a printf rather than a bare `hostname` because the
	// transcript carries the command being ECHOED as well as its answer, and
	// those must not be confusable. The typed line contains the literal %s; only
	// the answer has a name between the brackets.
	const canary = "printf 'GUESTNAME[%s]\\n' \"$(hostname)\"\n"
	answer := regexp.MustCompile(`GUESTNAME\[([^\]%]+)\]`)

	deadline := time.After(4 * time.Minute)
	retype := time.NewTicker(5 * time.Second)
	defer retype.Stop()
	var seen strings.Builder
	for {
		select {
		case <-deadline:
			t.Fatalf("the guest never answered in four minutes; what came back was:\n%s", seen.String())
		case <-retype.C:
			if err := sess.write([]byte(canary)); err != nil {
				t.Fatalf("typing into the shell: %v", err)
			}
		case chunk, ok := <-ch:
			if !ok {
				t.Fatalf("the shell ended before it answered:\n%s", seen.String())
			}
			seen.Write(chunk)
			m := answer.FindStringSubmatch(seen.String())
			if m == nil {
				continue
			}
			// THE ASSERTION, and it is a difference rather than a reading: a
			// relay wired to a local shell would draw a prompt, echo what you
			// type and render colour, and be completely wrong.
			name := strings.TrimSpace(m[1])
			if name == mine {
				t.Fatalf("the shell answered %q, which is THIS machine - the relay is wired to a"+
					" local shell and not to the guest at all", name)
			}
			t.Logf("the guest calls itself %q and this machine is %q", name, mine)
			return
		}
	}
}
