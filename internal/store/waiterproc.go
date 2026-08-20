package store

// WHICH PROCESS A WAITER IS, so a repair can name it instead of hunting for it.
//
// MEASURED, twice in one night on this fleet: the documented repair for a dead
// waiter is `pkill -9 -f 'flowy inbox --as NAME'`, and it killed the shell that
// ran it - exit 144 - because the pattern matched the process evaluating the
// pattern. A third seat did the same to a scratch node an hour later, and a
// fourth measurement the same evening had pgrep returning two wrong answers for
// the same reason. A COMMAND LINE IS A NAME THAT ANYTHING CAN WEAR, INCLUDING
// THE SEARCH.
//
// THE DISTINCTION THAT MAKES THIS SAFE WHERE pkill IS NOT: matching a pattern to
// FIND a process is unsafe, because the searcher matches itself and because two
// processes may wear one name. Checking the identity of a pid you were GIVEN is
// exact. This type is how the pid gets given.
//
// PID ALONE IS STILL NOT AN IDENTITY, which is why this carries three things:
//
//   Pid    the number, as the waiter itself reported it
//   Since  its start time - a pid is REUSED, so a stale one can name a
//          completely different process, and killing that is the pkill failure
//          in a new costume. Pid plus start time is what /proc keeps and what
//          makes the pair unambiguous.
//   Host   the machine that owns the number. A pid from a federated node's
//          reader means nothing here, and a number that looks actionable and is
//          not is worse than no number at all.
//
// IT IS A CLAIM, NOT A MEASUREMENT. The node cannot see the process; only the
// process can say what it is. That is the same standing waiter_kind has, and it
// is why every field is optional and an absent one reads as "not said" rather
// than as zero - see WaiterProcessOf.

import (
	"strconv"
	"strings"
	"time"
)

// WaiterProcess is what a waiter says it is, and it is either COMPLETE OR
// NOTHING.
//
// A pid with no start time is exactly the ambiguity this exists to remove, and
// a pid with no host is a number an operator might act on from the wrong
// machine. So a partial claim is discarded rather than stored: half of an
// identity is not a weaker identity, it is a different and worse one.
type WaiterProcess struct {
	Pid   int        `json:"waiter_pid,omitempty"`
	Since *time.Time `json:"waiter_since,omitempty"`
	Host  string     `json:"waiter_host,omitempty"`
}

// Complete reports whether all three parts are present, which is the only state
// in which any of them may be acted on.
func (w WaiterProcess) Complete() bool {
	return w.Pid > 0 && w.Since != nil && strings.TrimSpace(w.Host) != ""
}

// WaiterProcessOf normalises what a client sent, and is the only way a process
// claim gets into a row - WaiterKindOf's rule, for WaiterKindOf's reason: the
// values arrive on query parameters, so without this the roster would render
// whatever was typed.
//
// A claim that is not complete is discarded ENTIRELY rather than stored in
// part. An operator reading a pid with no start time beside it would have
// exactly the identity they had with pkill: a number that names whatever is
// wearing it now.
func WaiterProcessOf(pid, since, host string) WaiterProcess {
	var out WaiterProcess

	n, err := strconv.Atoi(strings.TrimSpace(pid))
	if err != nil || n <= 0 {
		return WaiterProcess{}
	}
	// A pid larger than any the kernel hands out is a typo or a different
	// machine's numbering, and storing it would put an actionable-looking
	// number in front of somebody. 4194304 is the ceiling Linux allows to be
	// configured; anything past it cannot be a local pid.
	if n > 4194304 {
		return WaiterProcess{}
	}
	started, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(since))
	if err != nil {
		return WaiterProcess{}
	}
	// A start time in the future is a clock disagreement, and the whole point
	// of carrying it is to tell one process from another that reused its pid.
	// A value that cannot do that is worse than none.
	if started.After(time.Now().Add(time.Minute)) {
		return WaiterProcess{}
	}
	name := strings.TrimSpace(host)
	if name == "" || len(name) > 253 {
		return WaiterProcess{}
	}
	out.Pid, out.Since, out.Host = n, &started, name
	return out
}
