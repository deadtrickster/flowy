package flowy

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A LISTENER THAT NEVER RE-EXECS CANNOT NOTICE IT IS OLD.
//
// 01M2FYTJP67A44HJAQQNSRQDH3. `listen` holds one process for the life of a
// session, so a deploy replacing the binary underneath it leaves it running the
// previous build indefinitely - still delivering, silently missing whatever was
// shipped. An `inbox` loop self-heals because it re-execs at every deadline;
// this is the price of the continuity that makes listen worth having.
//
// The check is deliberately about THIS machine: is the file I was started from
// still the file I am running. No node, no version string, no hash agreement.
func TestAReplacedBinaryIsNoticedAndAMissingOneIsNot(t *testing.T) {
	ident := func(t *testing.T, path string) runningBinary {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			t.Skip("no syscall.Stat_t on this platform")
		}
		return runningBinary{path: path, dev: uint64(st.Dev), ino: st.Ino}
	}

	t.Run("the same file is not replaced", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "flowy")
		if err := os.WriteFile(p, []byte("one"), 0o755); err != nil {
			t.Fatal(err)
		}
		if ident(t, p).replaced() {
			t.Fatal("an untouched file was reported replaced - this would cry stale on every poll")
		}
	})

	// THE REAL MECHANISM: mv a different file over the path, which is exactly
	// what shipping a new build does. The inode changes; the running process
	// would still be on the old one.
	t.Run("a file replaced by rename is noticed", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "flowy")
		if err := os.WriteFile(p, []byte("one"), 0o755); err != nil {
			t.Fatal(err)
		}
		was := ident(t, p)

		newer := filepath.Join(dir, "flowy.new")
		if err := os.WriteFile(newer, []byte("two"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(newer, p); err != nil {
			t.Fatal(err)
		}
		if !was.replaced() {
			t.Fatal("a binary replaced by rename was not noticed - this is the whole defect")
		}
	})

	// THE HALF THAT KEEPS IT HONEST. A check that cannot read must not report a
	// finding: a vanished path, a blipped mount or a chmod would otherwise make
	// every listener announce staleness it has no evidence for.
	t.Run("a path that cannot be read is not stale", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "flowy")
		if err := os.WriteFile(p, []byte("one"), 0o755); err != nil {
			t.Fatal(err)
		}
		was := ident(t, p)
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if was.replaced() {
			t.Fatal("an unreadable path was reported as replaced - the check failing is not the check finding something")
		}
	})

	t.Run("an empty path is not stale", func(t *testing.T) {
		if (runningBinary{}).replaced() {
			t.Fatal("a binary with no known path reported replaced")
		}
	})
}

// thisBinary must describe the test process itself, and must strip the
// " (deleted)" the kernel appends once a running file has been replaced -
// that suffix is a description of the link, not part of any filename.
func TestThisBinaryNamesTheRunningProcess(t *testing.T) {
	b, err := thisBinary()
	if err != nil {
		t.Skipf("cannot read /proc/self/exe here: %v", err)
	}
	if b.ino == 0 {
		t.Fatal("thisBinary returned no inode, so nothing can be compared against it")
	}
	if b.path == "" {
		t.Fatal("thisBinary returned no path")
	}
	if filepath.Base(b.path) == "" || len(b.path) < 2 {
		t.Fatalf("implausible path %q", b.path)
	}
	if got := (runningBinary{path: b.path + " (deleted)"}).path; got != b.path+" (deleted)" {
		t.Fatal("fixture sanity")
	}
	// The running test binary has not been replaced under itself.
	if b.replaced() {
		t.Fatal("the running test binary reported itself replaced")
	}
}
