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
	"strings"
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
	// Which machine this shell is on. Carried so the panel can SAY so: a guest
	// shell and a host shell look identical once a prompt is drawn, and the
	// difference is whether what you type can reach this machine.
	where   shellWhere
	started time.Time

	// The pty master. Writing to it is typing; reading from it is the screen.
	pty *os.File
	cmd *exec.Cmd

	mu sync.Mutex
	// What the shell has said, oldest first, capped at agentScrollback.
	out []byte
	// How much was dropped off the front, so a late reader can be told.
	dropped int
	// The scrollback's filter. A terminal QUESTION must not be replayed to a
	// reader that arrives later - see agentscrub.go. Held under the same mutex
	// as s.out, because it is state about the stream that produced it.
	scrub scrubber
	// Every attached reader. A send is non-blocking - see fanout.
	readers map[chan []byte]struct{}
	// Set once, when the shell exits or somebody stops it.
	done   bool
	why    string
	closed chan struct{}

	// When the last reader detached, for the idle reaper. Zero while attached.
	lonely time.Time
	// When this session last did anything - output from the shell, or a
	// keystroke into it.
	//
	// IDLE MEANS QUIET, NOT UNWATCHED, and the difference is a defect I had.
	// Reaping on `lonely` alone kills a session fifteen minutes after the panel
	// closes EVEN IF THE SHELL IS WORKING - start a twenty-minute build, close
	// the tab, come back to a stopped VM and a build that never finished. From
	// the outside that is indistinguishable from the shells the operator
	// reported as randomly exiting.
	//
	// Taken from how OpenChamber's terminal runtime does it: its sweep is
	// `!attached && now - lastActivity > IDLE`, and lastActivity is bumped by
	// both output and input. So an unattended session that is still producing
	// output is not idle, which is the honest meaning of the word.
	active time.Time
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
// Where a shell runs. Two values rather than a bool, because a bool would be
// read as "the special case" and neither of these is special - they are two
// different machines, and which one a person got is the first thing they need
// to know.
type shellWhere string

const (
	// shellInGuest is a shell inside a firecode microVM: its own kernel, its
	// own root, and nothing it does reaches the host.
	shellInGuest shellWhere = "vm"
	// shellOnHost is a shell on the machine serving this console, as the node's
	// own user. No isolation at all - see the comment at the switch below.
	shellOnHost shellWhere = "host"
)

func (a *agentShells) start(id, project, workdir, binary string, where shellWhere, size agentSize) (*agentSession, error) {
	return a.startIn(id, project, workdir, binary, where, "", size)
}

// startIn is start with a session named explicitly.
//
// A NAME THE CALLER CHOSE beats the one derived from the project, because the
// list a person picks from holds sessions this node never started - their
// editor's, and whatever they left running. Deriving would make those
// unreachable from the panel, which is the opposite of what the list is for.
func (a *agentShells) startIn(id, project, workdir, binary string, where shellWhere, mux string, size agentSize) (*agentSession, error) {
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
	// WHERE THE SHELL RUNS, and this is the one decision in this file with a
	// consequence outside it.
	//
	// The operator: "I want host-based shells, not only fc/libvirt - do a
	// selector." A guest shell is inside a microVM: its own kernel, its own
	// root, and nothing it does reaches this machine. A HOST shell has none of
	// that - it is a shell on the box serving this console, with the node's own
	// user and its files. That is a legitimate thing to want and a different
	// thing to hand out, so it is chosen explicitly and never by default.
	//
	// The only gate is that every one of these routes is operatorOnly. That was
	// already true and was already enough for a guest; for a host shell it is
	// the ONLY thing between a browser and the machine, which is worth saying
	// where the choice is made rather than in a commit message.
	var cmd *exec.Cmd
	switch where {
	case shellOnHost:
		// A BYOBU SESSION, PER PROJECT, AND IT IS THE OPERATOR'S OWN ONE.
		//
		// "per project byobu session i can attach to just over ssh, so your
		// stuff is just byobu management." So this is not a shell of ours in a
		// pty nobody else can reach: it is `byobu new-session -A -s
		// projectile/<project>`, the same name their editor uses, so the panel,
		// their Emacs terminal and `ssh host; byobu attach -t projectile/<x>`
		// are three clients of one session.
		//
		// -A ATTACHES IF IT EXISTS AND CREATES OTHERWISE, which is the whole
		// verb: a project they are already working in is joined, a project they
		// are not gets one made the way `bb` would make it.
		//
		// THIS REVERSES --no-tmux FOR THIS PATH ON PURPOSE, and it is not a
		// retraction of the fix that landed as 495a6e9. What was wrong there was
		// joining firecode/<project> - a session `firecode shell` had made for
		// its own VM, which had already exited, with nothing managing it. F3 and
		// F4 worked in the browser precisely because the panel had accidentally
		// become a byobu client; the accident had the right shape. The flag
		// still belongs on what runs INSIDE a window, where firecode must not
		// wrap itself a second time.
		//
		// A NAMELESS PROJECT GETS A PLAIN SHELL rather than projectile/, which
		// every unnamed project would otherwise share.
		session := strings.TrimSpace(mux)
		if session == "" {
			session = byobuSessionFor(project)
		}
		mux, muxErr := byobuBin()
		if session != "" && muxErr == nil {
			cmd = exec.Command(mux, "new-session", "-A", "-s", session)
			if workdir != "" {
				// Only where the session is CREATED does this matter; an attach
				// ignores it, which is right - a session that already exists has
				// a directory somebody chose.
				cmd.Dir = workdir
			}
			break
		}
		// The person's own login shell, not a hardcoded /bin/bash: a shell is a
		// preference, and $SHELL is where that preference already lives. Falling
		// back to sh rather than bash because sh is the one that is always there.
		login := os.Getenv("SHELL")
		if strings.TrimSpace(login) == "" {
			login = "/bin/sh"
		}
		// -l so it reads the profile and behaves like a terminal somebody
		// opened, rather than a bare shell with none of their environment.
		cmd = exec.Command(login, "-l")
		if workdir != "" {
			cmd.Dir = workdir
		}
	default:
		// --no-tmux, AND IT IS THE WHOLE FIX FOR "the shell exits immediately".
		//
		// `firecode shell` wraps an interactive session in byobu/tmux with
		// `new-session -A -s firecode/<project>` - bin/firecode:4126, and -A is
		// deliberate there: it makes the session a SINGLETON PER PROJECT so a
		// person can close a laptop and reattach over ssh.
		//
		// That is exactly wrong here. It means the panel is not talking to a
		// microVM at all: it is a tmux client on a session shared with every
		// other `firecode shell` for the same project, including the operator's
		// own terminal. Two consequences, both seen:
		//
		//   the panel adopted the operator's byobu - F3 and F4 switched THEIR
		//   windows, and what they typed into the browser went to their session
		//
		//   when that session's VM had already exited, attach found a dead
		//   window and returned at once, which the relay reported faithfully as
		//   a shell that exited immediately
		//
		// A relayed shell must be its own session. The flag firecode already
		// has says so, so nothing new is invented here.
		args := []string{"shell", "--no-tmux"}
		if workdir != "" {
			args = append(args, "--project", workdir)
		}
		cmd = exec.Command(binary, args...)
	}
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
		where:   where,
		started: time.Now(),
		pty:     master,
		cmd:     cmd,
		readers: map[chan []byte]struct{}{},
		closed:  make(chan struct{}),
		lonely:  time.Now(),
		active:  time.Now(),
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
	s.active = time.Now()
	// TWO STREAMS, AND ONLY ONE OF THEM IS STORED VERBATIM. Readers attached
	// now get the bytes exactly as the shell produced them, because a live
	// terminal is entitled to answer what it is asked. The scrollback keeps a
	// copy with the questions removed, because it is replayed to somebody who
	// arrives long after the asker has gone - and their terminal would answer
	// into the shell's stdin. See agentscrub.go.
	s.out = append(s.out, s.scrub.filter(chunk)...)
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
	s.active = time.Now()
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
			lonely, active := s.lonely, s.active
			s.mu.Unlock()
			// BOTH, and neither alone. Unwatched is not enough - a shell can be
			// working with nobody looking. Quiet is not enough either - a
			// terminal somebody is reading may say nothing for an hour.
			if !lonely.IsZero() && time.Since(lonely) > agentIdleAfter &&
				time.Since(active) > agentIdleAfter {
				s.finish(fmt.Sprintf(
					"nothing happened here and nobody was watching for %s, so the VM was stopped",
					agentIdleAfter))
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
