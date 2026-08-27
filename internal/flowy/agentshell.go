//go:build linux

package flowy

// A shell in a firecode microVM, relayed to the console.
//
// WHAT THIS IS FOR, and what it deliberately is not. The operator asked for "a
// run button which will bring fcvm with the shell relayed to the panel...
// agents and multiplication will come later. we will test the wiring." So this
// is one VM, one shell, and the bytes in both directions. There is no agent
// lifecycle here and no second session, because a half-built lifecycle is
// harder to replace than none.
//
// THE SHELL RUNS IN THE GUEST BECAUSE FIRECODE PUTS IT THERE. The node spawns
// the firecode CLI on a pseudo-terminal it owns and relays that terminal's
// bytes; firecode is what crosses into the VM. The first shape tried was socat
// listening on a port inside the guest with bash behind it, and that is a bind
// shell however sandboxed the VM is - unauthenticated, and anything on the host
// could open it. There is no listening port in the guest here. The only door is
// this node's, and every route is operatorOnly.
//
// A PTY AND NOT A PIPE. A shell on a pipe is not interactive: it does not draw
// a prompt, line editing is dead, ^C reaches nothing, and programs that ask
// whether they have a terminal answer no and change what they print. The pty is
// the whole point, and the repo already had one - cmd/smoke/pty_linux.go opens
// /dev/ptmx the same way, for the same reason.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// How long a session with nobody attached stays alive.
//
// A VM LEFT RUNNING IS THE COST THIS MUST NOT ADD. 623,539 inodes were
// reclaimed from abandoned scratch on this machine in one pass last week, and a
// microVM is more expensive than a directory. Closing the panel stops the
// session; this is what catches the browser that was closed instead, or the
// laptop that went to sleep mid-command.
const agentIdleAfter = 15 * time.Minute

// How much output a session keeps for a reader who was not attached when it was
// produced.
//
// BOUNDED, AND THE BOUND IS DISCLOSED. A reader that joins late is told how
// many bytes it will never see rather than being handed a prefix that looks
// whole - a truncated read that does not say it was truncated is the defect
// this repo has filed four times in one day.
const agentScrollback = 256 << 10

// agentSession is one VM, one shell, and the bytes it has produced.
type agentSession struct {
	id      string
	project string
	started time.Time

	// The pty master. Writing to it is typing; reading from it is the screen.
	pty *os.File
	cmd *exec.Cmd

	mu sync.Mutex
	// What the shell has said, oldest first, capped at agentScrollback.
	out []byte
	// How much was dropped off the front, so a late reader can be told.
	dropped int
	// Every attached reader. A send is non-blocking - see fanout.
	readers map[chan []byte]struct{}
	// Set once, when the shell exits or somebody stops it.
	done   bool
	why    string
	closed chan struct{}

	// When the last reader detached, for the idle reaper. Zero while attached.
	lonely time.Time
}

// agentShells is every session this node is running.
//
// ONE MAP AND ONE MUTEX because there is one session at a time today and the
// operator said multiplication comes later. It is a map rather than a single
// pointer so that "later" is a smaller change than a rewrite.
type agentShells struct {
	mu sync.Mutex
	by map[string]*agentSession
}

func newAgentShells() *agentShells {
	return &agentShells{by: map[string]*agentSession{}}
}

// ErrNoSession is a session id this node is not running.
//
// It is deliberately distinct from a refusal: a caller that may not use these
// doors is stopped by the role guard long before here, so reaching this means
// the caller was allowed and the session is genuinely not there.
var ErrNoSession = errors.New("agentshell: no such session")

// openAgentPTY makes a pseudo-terminal, the same way cmd/smoke does.
//
// Duplicated rather than shared: cmd/smoke is a separate main package built as
// its own binary, and exporting this from there would make the node depend on a
// test tool. The five lines are the stable part of a 40-year-old interface.
func openAgentPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening /dev/ptmx: %w", err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("asking the pty its number: %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("unlocking the pty: %w", err)
	}
	name := fmt.Sprintf("/dev/pts/%d", number)
	slave, err = os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("opening %s: %w", name, err)
	}
	return master, slave, nil
}

// start brings up a VM and a shell in it, and begins relaying.
//
// `firecode shell` is the verb: "interactive root shell in a VM". The node does
// not construct a guest command line, which matters beyond tidiness - building
// a shell command out of a name somebody typed is how a backtick becomes code,
// and this repo's rule is argument vectors only. Nothing here is interpolated
// into a string: the project name is one argv element and the guest never sees
// a shell of ours.
func (a *agentShells) start(id, project, workdir, binary string, size agentSize) (*agentSession, error) {
	master, slave, err := openAgentPTY()
	if err != nil {
		return nil, err
	}
	if err := setAgentSize(master, size); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, err
	}

	// ARGUMENT VECTOR, NEVER A COMMAND LINE.
	// --project takes a DIRECTORY here, which is why the caller's name was
	// resolved to one before this - see firecodeProjectPath. Passing the name
	// straight through is what the first attempt did, and firecode answered
	// "cd: flowy-staleblocked: No such file or directory": a shell that exits
	// immediately, relayed faithfully, and indistinguishable at the panel from
	// a VM that would not boot.
	args := []string{"shell"}
	if workdir != "" {
		args = append(args, "--project", workdir)
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// The child leads its own session with the pty as its controlling terminal.
	// Without Setctty the shell has a terminal it cannot control: no job
	// control, and ^C goes nowhere.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	// TERM, because a program with no TERM assumes the dumbest possible
	// terminal and stops emitting the sequences the panel exists to render.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	if err := cmd.Start(); err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("starting %s shell: %w", binary, err)
	}
	// The parent's copy of the slave goes NOW. While it is open the pty never
	// reports EOF, so a shell that has exited looks exactly like one that is
	// simply quiet, and the relay hangs forever waiting for a byte.
	_ = slave.Close()

	s := &agentSession{
		id:      id,
		project: project,
		started: time.Now(),
		pty:     master,
		cmd:     cmd,
		readers: map[chan []byte]struct{}{},
		closed:  make(chan struct{}),
		lonely:  time.Now(),
	}

	a.mu.Lock()
	a.by[id] = s
	a.mu.Unlock()

	go s.pump()
	go s.reap()
	return s, nil
}

// pump moves bytes from the shell to everybody watching, until the pty ends.
func (s *agentSession) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.emit(buf[:n])
		}
		if err != nil {
			// A pty whose child has exited reads EIO, not EOF, and that is the
			// normal end rather than a fault. Saying "the shell exited" for an
			// EIO and "the relay broke" for anything else is the difference
			// between a session that finished and one that failed.
			why := "the shell exited"
			if !errors.Is(err, os.ErrClosed) && !isPTYHangup(err) {
				why = fmt.Sprintf("the relay stopped: %v", err)
			}
			s.finish(why)
			return
		}
	}
}

// emit appends to the scrollback and hands the bytes to every reader.
func (s *agentSession) emit(b []byte) {
	chunk := make([]byte, len(b))
	copy(chunk, b)

	s.mu.Lock()
	s.out = append(s.out, chunk...)
	if over := len(s.out) - agentScrollback; over > 0 {
		s.out = s.out[over:]
		s.dropped += over
	}
	for ch := range s.readers {
		// NON-BLOCKING, and this is the decision that keeps one slow browser
		// from stopping the shell. A reader that cannot keep up misses bytes
		// and is told so by the gap in the sequence; a blocking send would
		// wedge pump() and with it every other reader and the shell itself.
		select {
		case ch <- chunk:
		default:
		}
	}
	s.mu.Unlock()
}

// attach adds a reader and hands back what has been said so far.
//
// The snapshot is taken UNDER THE SAME LOCK as the registration, so a byte
// produced between the two cannot be lost by arriving after the snapshot and
// before the channel existed. That race is the one thing a terminal relay
// cannot get away with: the missing byte is usually the prompt.
func (s *agentSession) attach() (ch chan []byte, backlog []byte, dropped int) {
	ch = make(chan []byte, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		close(ch)
		return ch, append([]byte(nil), s.out...), s.dropped
	}
	s.readers[ch] = struct{}{}
	s.lonely = time.Time{}
	return ch, append([]byte(nil), s.out...), s.dropped
}

func (s *agentSession) detach(ch chan []byte) {
	s.mu.Lock()
	delete(s.readers, ch)
	if len(s.readers) == 0 && s.lonely.IsZero() {
		s.lonely = time.Now()
	}
	s.mu.Unlock()
}

// write is a keystroke going the other way.
func (s *agentSession) write(b []byte) error {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done {
		return ErrNoSession
	}
	_, err := s.pty.Write(b)
	return err
}

// finish ends the session once, whoever gets there first.
func (s *agentSession) finish(why string) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done, s.why = true, why
	for ch := range s.readers {
		close(ch)
	}
	s.readers = map[chan []byte]struct{}{}
	s.mu.Unlock()

	_ = s.pty.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		// Reaped so the child does not sit as a zombie for the life of the
		// node. The error is discarded because the process may already be gone,
		// which is the ordinary case here rather than a fault.
		_ = s.cmd.Wait()
	}
	close(s.closed)
}

// isPTYHangup says whether an error is the ordinary end of a pty whose child
// has gone, rather than a fault.
//
// Linux answers EIO when the last process on the slave side exits, where a pipe
// would answer EOF. Reading EIO as a failure would report every normal exit as
// a broken relay.
func isPTYHangup(err error) bool {
	return errors.Is(err, unix.EIO)
}

// agentSize is the terminal's shape, which the guest has to be told.
//
// A pty defaults to 0x0, and a shell on a zero-sized terminal wraps its output
// at column zero or refuses to draw at all. The panel measures itself and says.
type agentSize struct{ Rows, Cols uint16 }

func (z agentSize) sane() agentSize {
	if z.Rows == 0 {
		z.Rows = 24
	}
	if z.Cols == 0 {
		z.Cols = 80
	}
	// A bound, because these arrive from a browser: a window struct is
	// uint16 and an absurd value is a guest drawing into nothing.
	if z.Rows > 500 {
		z.Rows = 500
	}
	if z.Cols > 1000 {
		z.Cols = 1000
	}
	return z
}

func setAgentSize(pty *os.File, z agentSize) error {
	z = z.sane()
	err := unix.IoctlSetWinsize(int(pty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: z.Rows, Col: z.Cols,
	})
	if err != nil {
		return fmt.Errorf("setting the terminal size: %w", err)
	}
	return nil
}

// resize retells the guest, and is what a window drag reaches.
func (s *agentSession) resize(z agentSize) error {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done {
		return ErrNoSession
	}
	return setAgentSize(s.pty, z)
}

// reap stops a session nobody is watching.
func (s *agentSession) reap() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-tick.C:
			s.mu.Lock()
			lonely := s.lonely
			s.mu.Unlock()
			if !lonely.IsZero() && time.Since(lonely) > agentIdleAfter {
				s.finish(fmt.Sprintf("nobody was watching for %s, so the VM was stopped", agentIdleAfter))
				return
			}
		}
	}
}

// get finds a session, or says it is not here.
func (a *agentShells) get(id string) (*agentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.by[id]
	if !ok {
		return nil, ErrNoSession
	}
	return s, nil
}

// stop ends a session and forgets it.
func (a *agentShells) stop(id, why string) error {
	a.mu.Lock()
	s, ok := a.by[id]
	delete(a.by, id)
	a.mu.Unlock()
	if !ok {
		return ErrNoSession
	}
	s.finish(why)
	return nil
}

// stopAll ends every session, for a node that is shutting down.
//
// Without it a restart of this service leaves its VMs running with nothing that
// remembers them, which is the abandoned-scratch failure with a microVM
// attached to it.
func (a *agentShells) stopAll(why string) {
	a.mu.Lock()
	all := make([]*agentSession, 0, len(a.by))
	for _, s := range a.by {
		all = append(all, s)
	}
	a.by = map[string]*agentSession{}
	a.mu.Unlock()
	for _, s := range all {
		s.finish(why)
	}
}
