package flowy

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/deadtrickster/flowy/internal/store"
)

// The two halves of being reachable, which are not the same thing.
//
// A waiter that delivers and exits leaves the room unheard until somebody
// arms another, and that depends on an agent noticing a background task
// finished. Across this fleet, agents did not: the user posted "who is here"
// into a room where every listener had delivered once and stopped.
//
// And exit 0 is not delivery. The messages went to a background task's
// output, the cursor moved past them, and if nobody read that output they
// were gone from both places at once.
//
// So a waiter does two things before it returns: it SPOOLS what it took
// somewhere a hook can deliver later, and it FORKS a successor so the room
// stays heard while the agent reads. The exiting process is what wakes the
// harness; the successor is what covers the gap. Neither replaces the other,
// which is the mistake that made this worse before it made it better - a
// detached successor is not a harness task, so it hears everything and has
// nothing to wake.

// waiterKind is which of the two this process is, and it is read here rather
// than in each place that needs it so the environment variable has ONE reader.
// Three surfaces ask now - the name lock, the poll, and the roster the poll
// feeds - and a second reading of FLOWY_WAITER_KIND is a second chance for one
// of them to disagree about what this process can do.
func waiterKind() string {
	if os.Getenv("FLOWY_WAITER_KIND") == store.WaiterForked {
		return store.WaiterForked
	}
	return store.WaiterTracked
}

// spoolEvents appends what was just delivered to a file the hook drains.
//
// Best effort on purpose: a spool that cannot be written must not fail a
// delivery that has already happened.
func spoolEvents(as string, page inboxWaitResponse) {
	dir, err := waiterDir()
	if err != nil {
		return
	}
	path := filepath.Join(dir, "inbox-spool-"+unsafeInName.ReplaceAllString(as, "-")+".jsonl")
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer fh.Close()
	enc := json.NewEncoder(fh)
	for _, e := range page.Events {
		_ = enc.Encode(e)
	}
}

// forkSuccessor starts a detached waiter under the same name and returns.
//
// Setsid so it outlives the shell that armed this one, and FLOWY_WAITER_KIND
// so the successor records itself as forked - a tracked waiter armed by a
// harness must be able to stand it down, because only the tracked one can
// wake anybody. Without that marking the successor would refuse the very
// takeover it exists to make room for.
//
// The lock is released by the caller before this runs, or the successor
// refuses itself.
func forkSuccessor(as, url string, deadline int) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	args := []string{"inbox", "--as", as, "--deadline", strconv.Itoa(deadline)}
	if url != "" {
		args = append(args, "--url", url)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "FLOWY_WAITER_KIND="+store.WaiterForked)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	if dir, err := waiterDir(); err == nil {
		logPath := filepath.Join(dir, "inbox-"+unsafeInName.ReplaceAllString(as, "-")+".log")
		if fh, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			cmd.Stdout = fh
			cmd.Stderr = fh
		}
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "could not hand over to a successor: %v\n", err)
		return
	}
	_ = cmd.Process.Release()
}
