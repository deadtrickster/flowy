package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THE ADVICE A REFUSAL GIVES MUST NOT CREATE THE NEXT FAILURE.
//
// 01M0HQKP0C. `merge open` refused a branch held by the SHARED CHECKOUT and told
// the filer to detach it. They did, and the drainer then refused to land
// anything: deploying from a detached main working tree is refused one layer
// down, "is on HEAD, not master". One gate pass and a frozen queue.
//
// This asserts a DIFFERENCE rather than the wording: the two kinds of tree must
// get different advice, and the main one must never be told to detach. A test
// that checked one string would pass on the version that says the same thing to
// both, which is the bug.
func TestTheAdviceDependsOnWhichTreeHoldsTheBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	run(main, "init", "-q", "-b", "master")
	if err := os.WriteFile(filepath.Join(main, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run(main, "add", "a")
	run(main, "commit", "-qm", "first", "--no-gpg-sign")

	linked := filepath.Join(root, "linked")
	run(main, "worktree", "add", "-q", "-b", "side", linked)

	// THE CONTROL: git really does tell these two apart on this machine. Without
	// it, a version of isLinkedWorktree that always answered false would make
	// the assertions below pass while saying nothing.
	if isLinkedWorktree(linked) != true {
		t.Fatal("a linked worktree was not recognised as one, so nothing below is measured")
	}
	if isLinkedWorktree(main) != false {
		t.Fatal("the main working tree was recognised as linked")
	}

	if got := freeItWith(main); !strings.Contains(got, "checkout master") {
		t.Errorf("the main tree is told %q, want it put back on master - detaching it is "+
			"what the drainer refuses to land from", got)
	}
	if strings.Contains(freeItWith(main), "--detach") {
		t.Errorf("the main tree is still being told to detach: %q", freeItWith(main))
	}
	if got := freeItWith(linked); !strings.Contains(got, "--detach") {
		t.Errorf("a linked worktree is told %q, want the detach that frees the branch", got)
	}
	if freeItBy(main) == freeItBy(linked) {
		t.Errorf("both trees get the same sentence: %q", freeItBy(main))
	}

	// AND AN UNANSWERABLE QUESTION GETS THE SAFE ADVICE. A path git cannot be
	// asked about must not be told to detach - "put it back on master" is
	// harmless in a scratch worktree and is the only correct thing in a shared
	// one, so cannot-know falls that way on purpose.
	nowhere := filepath.Join(root, "not-a-repo")
	if err := os.MkdirAll(nowhere, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if strings.Contains(freeItWith(nowhere), "--detach") {
		t.Errorf("a tree git could not be asked about was told to detach: %q", freeItWith(nowhere))
	}
}
