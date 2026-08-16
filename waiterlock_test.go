package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The guard has to refuse a second waiter and it has to not refuse a legitimate
// one, and the second half is the half that would quietly make things worse: a
// guard that holds a stale claim forever locks somebody out of their own room
// after one kill -9, which is a bigger outage than the duplicate it prevents.

func TestASecondWaiterForTheSameNameIsRefused(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	first, err := holdWaiterName("flowy-claude")
	if err != nil {
		t.Fatalf("the first waiter could not claim the name: %v", err)
	}
	defer first.release()

	_, err = holdWaiterName("flowy-claude")
	if err == nil {
		t.Fatal("a second waiter claimed the same name, so both would share one cursor")
	}
	// The refusal has to name the pid: the only useful thing to tell somebody
	// who just started a second waiter is which one is already theirs.
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("the refusal does not say which process holds the name: %v", err)
	}
}

func TestADifferentNameIsNotRefused(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	mine, err := holdWaiterName("flowy-claude")
	if err != nil {
		t.Fatalf("could not claim: %v", err)
	}
	defer mine.release()

	theirs, err := holdWaiterName("orchestrator")
	if err != nil {
		t.Fatalf("a waiter for another name was refused, and names are the whole point: %v", err)
	}
	theirs.release()
}

func TestAReleasedNameCanBeClaimedAgain(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	first, err := holdWaiterName("flowy-claude")
	if err != nil {
		t.Fatalf("could not claim: %v", err)
	}
	first.release()

	again, err := holdWaiterName("flowy-claude")
	if err != nil {
		t.Fatalf("the name stayed claimed after its waiter ended: %v", err)
	}
	again.release()
}

// A waiter killed with -9 never runs its cleanup, so the file outlives it. That
// is the ordinary case after somebody stops a waiter the blunt way, and the
// name has to be reclaimable or the guard becomes the outage.
func TestAStaleClaimIsTakenOverRatherThanHonoured(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	// A pid that is over: a child that has run and been reaped. Writing an
	// arbitrary number would risk naming a live process on a busy machine and
	// testing the opposite of what this claims.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatalf("could not produce an exited process: %v", err)
	}
	gone := dead.Process.Pid

	path := filepath.Join(dir, "flowy", "inbox-flowy-claude.pid")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(gone)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	lock, err := holdWaiterName("flowy-claude")
	if err != nil {
		t.Fatalf("a claim held by a process that no longer exists refused a new waiter: %v", err)
	}
	defer lock.release()

	held, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(held)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("the claim still names %s, not this process", strings.TrimSpace(string(held)))
	}
}

// Releasing must not delete somebody else's claim. It happens when a waiter is
// killed with -9, its stale file is taken over by a new waiter, and the old
// process is somehow still around to run its cleanup - which would leave the
// live waiter unguarded and silently allow a second one.
func TestReleasingDoesNotDropSomebodyElsesClaim(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	path := filepath.Join(dir, "flowy", "inbox-flowy-claude.pid")
	stale := &waiterLock{path: path}

	live, err := holdWaiterName("flowy-claude")
	if err != nil {
		t.Fatalf("could not claim: %v", err)
	}
	defer live.release()

	stale.release()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the live waiter's claim was deleted by another lock's release: %v", err)
	}
}
