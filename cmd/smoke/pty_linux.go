//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The one check on `flowy tui` that cannot be made from inside the process:
// that it gives the terminal back.
//
// teatest drives the model through an in-memory reader and writer, which is the
// right way to assert what the client renders and what a keystroke does - but a
// model has no terminal, so it cannot answer the question a person over ssh
// actually cares about: after q, is the shell still usable, or is it echoing
// nothing and swallowing ctrl-c because raw mode was left on?
//
// So this runs the real binary on a real pty, resizes it underneath, quits it,
// and then reads the pty's termios back. ECHO and ICANON are off in raw mode
// and on in a shell, so their state after the process is gone is the answer.

// tuiPTY runs `flowy tui` on a pseudo-terminal, resizes it, quits it with q and
// checks that the terminal came back.
func tuiPTY(binary, url, token string) error {
	master, slave, name, err := openPTY()
	if err != nil {
		return err
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	// The state a shell leaves a terminal in, so that "restored" means
	// something specific rather than "whatever it happened to be".
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		return fmt.Errorf("reading the pty's termios: %w", err)
	}
	if before.Lflag&unix.ECHO == 0 || before.Lflag&unix.ICANON == 0 {
		return errors.New("the fresh pty is not in canonical mode; nothing to compare against")
	}
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ,
		&unix.Winsize{Row: 24, Col: 80}); err != nil {
		return fmt.Errorf("sizing the pty: %w", err)
	}

	cmd := exec.Command(binary, "tui", "--url", url, "--token", token)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own with the pty as its controlling terminal: that is
	// what makes the process the foreground group, and therefore what makes a
	// window-size change reach it as SIGWINCH.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=C.UTF-8")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s tui: %w", binary, err)
	}

	screen := &transcript{}
	go screen.drain(master)

	quit := make(chan error, 1)
	go func() { quit <- cmd.Wait() }()

	fail := func(format string, args ...any) error {
		_ = cmd.Process.Kill()
		<-quit
		return fmt.Errorf(format+"\nthe pty (%s) saw:\n%s", append(args, name, screen.tail())...)
	}

	if err := screen.await("ROOMS", 30*time.Second); err != nil {
		return fail("the client never drew its tab bar: %v", err)
	}

	// A resize, from underneath, twice - which is what a tmux pane split does.
	// Nothing is asserted about what it draws afterwards beyond that it is
	// still drawing and still alive: the layout itself is checked by the
	// model tests, and what this one is for is that SIGWINCH does not kill it.
	for _, size := range []unix.Winsize{{Row: 40, Col: 132}, {Row: 24, Col: 80}} {
		if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &size); err != nil {
			return fail("resizing the pty: %v", err)
		}
		time.Sleep(400 * time.Millisecond)
		select {
		case err := <-quit:
			return fmt.Errorf("the client exited on a resize (%v)\nthe pty saw:\n%s",
				err, screen.tail())
		default:
		}
	}

	if _, err := master.Write([]byte("q")); err != nil {
		return fail("typing q: %v", err)
	}

	select {
	case err := <-quit:
		if err != nil {
			return fmt.Errorf("q did not quit cleanly: %w\nthe pty saw:\n%s", err, screen.tail())
		}
	case <-time.After(20 * time.Second):
		return fail("q did not quit within twenty seconds")
	}

	// The terminal, after. Raw mode clears ECHO and ICANON; a client that left
	// them clear is a client that has just made the user's shell unusable.
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		return fmt.Errorf("reading the pty's termios back: %w", err)
	}
	if after.Lflag&unix.ECHO == 0 {
		return fmt.Errorf("ECHO is still off after the client exited: raw mode was leaked")
	}
	if after.Lflag&unix.ICANON == 0 {
		return fmt.Errorf("ICANON is still off after the client exited: raw mode was leaked")
	}

	// And the screen: the alt screen has to be left, or the pane comes back
	// blank with the scrollback gone.
	drawn := screen.tail()
	if !strings.Contains(drawn, "\x1b[?1049l") && !strings.Contains(drawn, "\x1b[?47l") {
		return fmt.Errorf("the client never left the alternate screen")
	}

	fmt.Printf("ran on %s, survived two resizes, quit on q, "+
		"left the alt screen and gave the terminal back (ECHO and ICANON on)\n", name)
	return nil
}

// openPTY opens a pty pair the old-fashioned way: /dev/ptmx, unlock, and the
// numbered slave. There is no dependency for this in the tree and one function
// is cheaper than one.
func openPTY() (master, slave *os.File, name string, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, "", fmt.Errorf("opening /dev/ptmx: %w", err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		_ = master.Close()
		return nil, nil, "", fmt.Errorf("asking the pty its number: %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		return nil, nil, "", fmt.Errorf("unlocking the pty: %w", err)
	}
	name = fmt.Sprintf("/dev/pts/%d", number)
	slave, err = os.OpenFile(name, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, "", fmt.Errorf("opening %s: %w", name, err)
	}
	return master, slave, name, nil
}

// transcript is everything the pty has shown, read on a goroutine so the master
// never fills up and blocks the client mid-frame.
type transcript struct {
	mu   sync.Mutex
	seen []byte
}

func (t *transcript) drain(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.seen = append(t.seen, buf[:n]...)
			t.mu.Unlock()
		}
		if err != nil {
			// EIO is what a master read gives when the last slave closes, which
			// is the ordinary end of this.
			return
		}
	}
}

func (t *transcript) contains(text string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Contains(string(t.seen), text)
}

func (t *transcript) await(text string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if t.contains(text) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("%q never appeared within %s", text, within)
}

// tail is as much of the transcript as belongs in an error.
func (t *transcript) tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	const keep = 2000
	seen := t.seen
	if len(seen) > keep {
		seen = seen[len(seen)-keep:]
	}
	return string(seen)
}
