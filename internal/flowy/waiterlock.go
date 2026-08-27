package flowy

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// One waiter per name, enforced rather than asked for.
//
// Two waiters under one name share one cursor, so the second takes deliveries
// the first should have made and both look healthy while the room goes quiet on
// somebody. That is not hypothetical: it silenced the orchestrator's watcher
// tonight, and it is why `firecode chat` grew the same guard (a44016a). The
// finding that this one was missing is the orchestrator's - flowy is the live
// room, so it is the place the failure actually costs something.
//
// The mechanism is deliberately not clever, because every clever version of it
// was wrong tonight. Counting processes cannot work: a pgrep matches the shell
// that runs it, one waiter is two or three processes, killing a parent orphans
// its child into a new one, and another agent's waiter under a shared name
// looks identical to this session's. So the waiter WRITES DOWN THAT IT EXISTS,
// and liveness is `kill -0` on a number.

// waiterLock is a claim on a name, released when the waiter ends.
//
// It remembers the pid it WROTE rather than assuming the pid it would write,
// so releasing can ask "is the claim on disk still the one I made" instead of
// "is the claim on disk from a process like me". Those come apart whenever two
// locks exist in one process - a stale lock and the live one that took its file
// over - and the pid test silently deletes the live claim in that case, which
// is the guard disabling itself at the one moment it is needed.
type waiterLock struct {
	path string
	pid  int
}

// unsafeInName is everything that does not belong in a file name. The waiter's
// name comes from a flag, and a name with a slash in it would otherwise write
// the pid file somewhere else entirely.
var unsafeInName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// waiterDir is where the claims live. XDG_RUNTIME_DIR is the right home for
// them - it is per-user, and it is emptied when the session ends, so a machine
// that lost power does not come back holding claims for waiters that died with
// it. When it is not set, fall back to the user's cache, where a stale file is
// still handled: a claim is only honoured if its pid is alive.
func waiterDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		base = cache
	}
	dir := filepath.Join(base, "flowy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// holdWaiterName claims a name for this process, or refuses.
//
// A refusal names the pid holding it, because the only useful thing to say to
// somebody who just tried to start a second waiter is which one is already
// theirs. A stale claim - the file exists, the process does not - is taken
// over rather than treated as an error: a waiter killed with -9 never ran its
// cleanup, and refusing forever because of that would make the guard worse than
// the bug it prevents.
func holdWaiterName(name string) (*waiterLock, error) {
	dir, err := waiterDir()
	if err != nil {
		return nil, fmt.Errorf("cannot find somewhere to record this waiter: %w", err)
	}
	path := filepath.Join(dir, "inbox-"+unsafeInName.ReplaceAllString(name, "-")+".pid")

	// A TRACKED WAITER STANDS DOWN A FORKED ONE. They are not equivalent and
	// the difference is the whole point of the guard.
	//
	// The successor forked at delivery is detached, so it is not a background
	// task of anybody's harness: it keeps the room heard and has NOTHING TO
	// WAKE when a message arrives. If it can refuse a tracked waiter, a
	// session ends up with a live listener, no shells, and silence - which is
	// what happened before this distinction existed. Two TRACKED waiters
	// still refuse each other, because those genuinely would split a cursor.
	kind := waiterKind()
	if held, ok := livePIDIn(path); ok {
		heldKind := kindIn(path)
		if heldKind == store.WaiterForked && kind == store.WaiterTracked {
			fmt.Fprintf(os.Stderr,
				"standing down the forked waiter (pid %d) - a tracked one can wake you, it cannot\n",
				held)
			_ = syscall.Kill(held, syscall.SIGTERM)
			time.Sleep(500 * time.Millisecond)
		} else {
			return nil, fmt.Errorf(
				"a waiter for %q is already running (pid %d, %s).\n"+
					"Two of them share one cursor, so the second would take messages the first\n"+
					"should have delivered - and both would look healthy. Keep that one, or stop\n"+
					"it with 'kill %d' if it is not the one your harness is watching.",
				name, held, heldKind, held)
		}
	}

	mine := os.Getpid()
	if err := os.WriteFile(path, []byte(strconv.Itoa(mine)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("cannot record this waiter: %w", err)
	}
	// The kind goes in a SIDECAR rather than into the pid file. Adding a
	// second field there broke two readers within ten minutes - release()
	// stopped matching its own claim, and a test that asserts the file's
	// exact contents failed. A format with one reader is easy to change; this
	// one has three, so the new fact gets its own file instead.
	_ = os.WriteFile(kindPath(path), []byte(kind+"\n"), 0o600)
	return &waiterLock{path: path, pid: mine}, nil
}

// kindPath is where the tracked/forked marking for a claim lives.
func kindPath(pidPath string) string { return pidPath + ".kind" }

// kindIn reports how the holder of a claim described itself. Anything
// unreadable is tracked, which is the safe reading: it refuses rather than
// killing something whose nature is unknown.
//
// The reader row on the node defaults the other way - an unsaid kind is
// unknown there, never tracked - and the two are the same caution about
// different questions. Here an unknown claim must not be killed; there an
// unknown listener must not be reported as one that can wake you.
func kindIn(pidPath string) string {
	raw, err := os.ReadFile(kindPath(pidPath))
	if err != nil {
		return store.WaiterTracked
	}
	if strings.TrimSpace(string(raw)) == store.WaiterForked {
		return store.WaiterForked
	}
	return store.WaiterTracked
}

// livePIDIn reports the pid recorded at path, and whether it is still running.
func livePIDIn(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	// Signal 0 asks the kernel whether the process exists and is ours, without
	// touching it. EPERM means it exists and belongs to somebody else, which
	// still means the name is taken.
	err = syscall.Kill(pid, 0)
	return pid, err == nil || errors.Is(err, syscall.EPERM)
}

// release drops the claim, and only if it is still the claim this lock made.
//
// Comparing against what we WROTE rather than against our own pid is what makes
// this correct when a stale lock and the live one share a process: the stale
// lock wrote nothing, so it matches nothing, so it removes nothing. Reading our
// own pid back would have made the two indistinguishable and let a dead lock
// delete a live waiter's claim - which the test for exactly that caught.
//
// It is safe to call twice.
func (w *waiterLock) release() {
	if w == nil || w.path == "" || w.pid == 0 {
		return
	}
	raw, err := os.ReadFile(w.path)
	if err != nil {
		w.path = ""
		return
	}
	if strings.TrimSpace(string(raw)) != strconv.Itoa(w.pid) {
		// Somebody else's claim now. Ours is already gone.
		w.path = ""
		return
	}
	os.Remove(kindPath(w.path))
	os.Remove(w.path)
	w.path = ""
}

// releaseOnSignal drops the claim when the process is killed rather than
// leaving it for the next waiter to find and take over.
//
// It matters because the ordinary end of a waiter is somebody stopping it. A
// claim that is only cleaned up on a graceful return would be stale most of the
// time, and a guard whose normal state is stale is one nobody trusts. SIGKILL
// still cannot be caught, which is what the takeover path above is for.
func (w *waiterLock) releaseOnSignal() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		s := <-signals
		w.release()
		// Die of what killed us, so a caller sees the signal rather than a
		// clean exit that hides it.
		signal.Reset(s.(syscall.Signal))
		_ = syscall.Kill(os.Getpid(), s.(syscall.Signal))
	}()
}
