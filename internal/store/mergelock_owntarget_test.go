package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// NO TEST IN THIS PACKAGE TAKES A LOCK ON THE REAL MASTER.
//
// The suite shares one database with a running node, and nothing releases a
// lock early: it is held for MergeLockBelievedFor - fifteen minutes, longer
// than the whole run - unless a red, a land or an abandon gives it back. A test
// that takes `master` and finishes therefore keeps the gate shut for every
// check that comes after it, and /api/merge-queue asked with no project answers
// about the target, so those checks are told a run is in progress by a run that
// ended in a second.
//
// It has cost twice. The second time it was diagnosed and fixed IN THE TEST
// THAT NOTICED - mergelock_project_test.go still carries the note, "a failure
// about the fixture wearing the face of a failure about the code" - while the
// test doing the leaking two functions above it was left alone, because the
// leak reads as handled once the fix is in the file. This is that fix made
// mechanical instead of remembered.
//
// ownTarget(t) is the answer: "master-" and a ULID, unique per test, contending
// with nothing. A test that genuinely needs two projects to share a target name
// gets that from one ownTarget used twice.
func TestNoLockTestTakesTheRealMaster(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test sources found - this walk would pass by reading nothing")
	}

	// The target is the third argument to either verb, so the literal is caught
	// where it is USED as a target and not where it appears in a message.
	takes := regexp.MustCompile(`(?:TakeMergeLock|ReleaseMergeLock)\(ctx, [^,]+, [^,]+, "master"`)
	reads := regexp.MustCompile(`MergeLockOf\(ctx, [^,]+, "master"\)`)

	var walked, found int
	for _, name := range files {
		if name == "mergelock_owntarget_test.go" {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		walked++
		for i, line := range strings.Split(string(body), "\n") {
			if takes.MatchString(line) || reads.MatchString(line) {
				found++
				t.Errorf(`%s:%d takes the real master:
%s
Use ownTarget(t) - this database is shared with a running node and the lock
outlives the test by fifteen minutes.`, name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if walked == 0 {
		t.Fatal("walked no files, so this asserts nothing")
	}
	t.Logf("%d test sources walked, %d locks on the real master", walked, found)
}
