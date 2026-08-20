package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A BRANCH CHECKED OUT SOMEWHERE CANNOT BE REBASED, and the queue learns it
// fifteen minutes late.
//
// The drainer picks the row up, records a block and moves on. From outside the
// row looks stalled rather than fixable, so whoever filed it has gone on to
// something else by the time anybody reads the reason. It happened FIVE TIMES
// on 2026-08-20 across three agents; one cost an hour on an unrelated flake
// because the row also carried a red, and one sat blocked for 25 minutes while
// its author reported that they were waiting for the gate.
//
// WHAT THIS PINS IS THE THIRD ANSWER. Held and not-held are the easy pair; the
// one worth a test is "this tree cannot answer", because that is the one a
// future simplification collapses into "not held" - and a confident clean from a
// directory that has never heard of the branch is exactly the failure the guard
// exists to prevent. Same shape as a nil slice serialising to null.
func TestHeldByTellsNotHeldFromCannotKnow(t *testing.T) {
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "f")
	git(repo, "commit", "-q", "--no-gpg-sign", "-m", "one")
	git(repo, "branch", "loose")

	// heldBy reads the CURRENT directory, which is what the CLI has when
	// somebody runs `flowy merge open` from their tree.
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(here) })

	// HELD: the branch the repo itself is standing on.
	if at, known := heldBy("main"); !known || at == "" {
		t.Fatalf(`heldBy("main") = %q, %v - the branch this repo has checked out must report as held`, at, known)
	}

	// A SECOND WORKTREE, which is the shape every real instance had.
	wt := filepath.Join(root, "wt")
	git(repo, "worktree", "add", "-q", wt, "loose")
	at, known := heldBy("loose")
	if !known || at != wt {
		t.Fatalf(`heldBy("loose") = %q, %v - want %q, true`, at, known, wt)
	}

	// NOT HELD, MEASURED: detach the worktree and the same branch is free. The
	// answer changes, and it changes to ("", true) - a measured no.
	git(wt, "checkout", "-q", "--detach")
	if at, known := heldBy("loose"); !known || at != "" {
		t.Fatalf(`heldBy("loose") after detach = %q, %v - want "", true`, at, known)
	}

	// CANNOT KNOW: a branch this repository has never heard of. The worktree
	// list here is about some other history, so answering "not held" would be a
	// guess dressed as a measurement - and the caller must not refuse on it.
	if at, known := heldBy("nobody/has/this"); known || at != "" {
		t.Fatalf(`heldBy("nobody/has/this") = %q, %v - a repo without the branch cannot answer, `+
			`and reporting a confident "not held" is how the guard would clear a branch `+
			`held in the tree that actually owns it`, at, known)
	}

	// AND OUTSIDE A REPOSITORY AT ALL, which is where an agent's shell often is.
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if at, known := heldBy("main"); known || at != "" {
		t.Fatalf(`heldBy outside a git repo = %q, %v - want "", false`, at, known)
	}

	// $FLOWY_REPO IS THE WHOLE POINT, and this arm is the one the first version
	// of the guard failed in production.
	//
	// It landed correct and caught nothing: every seat files with
	// `cd ~/Projects/flowy-dogfood && flowy merge open`, that directory holds
	// the built binary and is not a git repository, so the guard answered
	// cannot-know and proceeded on the only path anybody uses. Standing outside
	// the repo is the NORMAL case here, not the edge one.
	git(repo, "worktree", "add", "-q", filepath.Join(root, "wt2"), "loose")
	t.Setenv("FLOWY_REPO", repo)
	at, known = heldBy("loose")
	if !known || at != filepath.Join(root, "wt2") {
		t.Fatalf(`with FLOWY_REPO set, from outside any repo: heldBy("loose") = %q, %v - `+
			`want %q, true. The guard has to ask the tree that OWNS the branch, not the `+
			`directory the shell happens to be in`, at, known, filepath.Join(root, "wt2"))
	}

	// A FLOWY_REPO THAT IS NOT A REPO still cannot know, and must not pretend.
	t.Setenv("FLOWY_REPO", root)
	if at, known := heldBy("loose"); known || at != "" {
		t.Fatalf(`FLOWY_REPO pointing at a non-repo = %q, %v - want "", false`, at, known)
	}
}
