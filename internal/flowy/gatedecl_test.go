package flowy

import (
	"os/exec"
	"strings"
	"testing"
)

// TestTheProjectDeclaresItsGate reads the git index, not the working tree.
//
// The drainer no longer knows what this project's suite is called: it runs
// .flowy-gate from the root of the checkout, and refuses to gate a project that
// declares none (01M0DZPFQD). That makes both files load-bearing in a way
// nothing in the suite would otherwise notice - a rename or a dropped mode bit
// is invisible until a gate dies, and a gate that dies at startup reads as a
// red on whatever branch happened to be next.
//
// WHY THE INDEX AND NOT THE FILE. On 2026-08-18 a commit dropped run-tests.sh
// from 100755 to 100644 and every gate that day still passed, because an
// existing worktree keeps the mode it was checked out with and `bash file.sh`
// ignores the bit entirely. Only a FRESH checkout exec'ing it sees "Permission
// denied". os.Stat here would be asking the same worktree that already got away
// with it; `git ls-files -s` is asking what a fresh checkout will get.
func TestTheProjectDeclaresItsGate(t *testing.T) {
	out, err := exec.Command("git", "-C", "../..", "ls-files", "-s", ".flowy-gate", ".flowy-pregate").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	modes := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 {
			modes[f[3]] = f[0]
		}
	}
	for _, want := range []string{".flowy-gate", ".flowy-pregate"} {
		mode, ok := modes[want]
		if !ok {
			t.Errorf("%s is not tracked, and the drainer runs it by that name - "+
				"a project that declares no gate is refused rather than guessed at", want)
			continue
		}
		if mode != "100755" {
			t.Errorf("%s is %s in the index, not 100755 - a fresh worktree cannot ./run it, "+
				"and the gate dies at startup on somebody else's branch", want, mode)
		}
	}
}

// A NOTE ON RUNNING THIS BY HAND. `go test` caches a result against the package
// SOURCES, and a file mode in the git index is not one of them - so chmod the
// bit away, run this again, and it prints a cached `ok` while the thing it
// checks is broken. That happened while this test was being written and it is
// the failure mode the test exists to catch, one level up.
//
// The gate runs `go test -count=1 ./...`, so it is honest there. By hand, pass
// -count=1 or you are reading a statement about an earlier tree.
