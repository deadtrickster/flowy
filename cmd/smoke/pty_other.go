//go:build !linux

package main

import "errors"

// tuiPTY needs a pty and the termios ioctls to read one back, and this tree
// only has them for Linux. The gate runs on Linux; everywhere else this says
// so rather than being quietly skipped, because a check that reports success
// without running is worse than one that is not there.
func tuiPTY(_, _, _ string) error {
	return errors.New("the pty check for `flowy tui` is implemented for linux only")
}
