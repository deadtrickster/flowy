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

	// A VM takes a while to boot and the shell says nothing useful until it
	// has. Waiting for the prompt is unreliable across shells, so the canary is
	// the answer to a question only the guest can answer.
	deadline := time.After(4 * time.Minute)
	typed := false
	var seen strings.Builder
	for {
		select {
		case <-deadline:
			t.Fatalf("the guest never answered in four minutes; what came back was:\n%s", seen.String())
		case chunk, ok := <-ch:
			if !ok {
				t.Fatalf("the shell ended before it answered:\n%s", seen.String())
			}
			seen.Write(chunk)
			// Typed once, after the first output - which is the earliest moment
			// there is anything on the other end to type at.
			if !typed {
				typed = true
				if err := sess.write([]byte("hostname\n")); err != nil {
					t.Fatalf("typing into the shell: %v", err)
				}
				continue
			}
			// THE ASSERTION. The transcript carries the command being echoed
			// back, so finding "hostname" proves nothing; finding a name that
			// is not this machine's is the difference.
			out := seen.String()
			for _, line := range strings.Split(out, "\n") {
				name := strings.TrimSpace(line)
				if name == "" || strings.Contains(name, "hostname") || strings.Contains(name, "$") {
					continue
				}
				if name == mine {
					t.Fatalf("the shell answered %q, which is THIS machine - the relay is wired to a"+
						" local shell and not to the guest at all", name)
				}
				if isPlausibleHostname(name) {
					t.Logf("the guest calls itself %q and this machine is %q", name, mine)
					return
				}
			}
		}
	}
}

// isPlausibleHostname keeps the loop above from accepting a fragment of a
// prompt or an escape sequence as the guest's answer.
func isPlausibleHostname(s string) bool {
	if len(s) < 2 || len(s) > 64 || strings.ContainsAny(s, " \t\x1b[]()#~") {
		return false
	}
	for _, r := range s {
		if !(r == '-' || r == '.' || (r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}
