package store

import (
	"testing"
	"time"
)

// TWO PROJECTS DO NOT CONTEND FOR ONE TARGET, because every repository's target
// is called master.
//
// This is the LOUD half of the defect merge_lands had silently: two projects
// shared one lock row, so the second to declare was refused and told about a
// holder and an item from a repository it has never heard of. Loud is better
// than silent and is still wrong.
//
// The arms are chosen so none of them passes on the broken version, and so that
// none of them passes on a lock that had simply been deleted - which is the way
// a per-project key can be got wrong in the other direction.
func TestALockBelongsToItsProject(t *testing.T) {
	ctx, db := open(t)
	now := time.Now()
	alpha := &Principal{UserID: "u-alpha", Project: "alpha"}
	beta := &Principal{UserID: "u-beta", Project: "beta"}

	if _, err := db.TakeMergeLock(ctx, alpha, "alpha", "master", "01ALPHA"); err != nil {
		t.Fatalf("alpha could not take its own master: %v", err)
	}

	// THE OTHER PROJECT'S MASTER IS A DIFFERENT TARGET. On the broken version
	// this is refused with alpha's holder and alpha's item.
	if _, err := db.TakeMergeLock(ctx, beta, "beta", "master", "01BETA"); err != nil {
		t.Fatalf("beta was refused its own master by another project's lock: %v", err)
	}

	// AND THE SAME PROJECT STILL CONTENDS, which is the arm that fails if the
	// key were made unique enough to stop locking anything at all.
	other := &Principal{UserID: "u-other", Project: "alpha"}
	if _, err := db.TakeMergeLock(ctx, other, "alpha", "master", "01OTHER"); err == nil {
		t.Error("a second declarer in the SAME project took a held target - the lock " +
			"stopped locking rather than started scoping")
	}

	// EACH READS ITS OWN HOLDER. A read that crossed projects would report the
	// wrong seat to whoever is deciding whether to wait.
	a, err := db.MergeLockOf(ctx, "alpha", "master")
	if err != nil || a == nil {
		t.Fatalf("alpha's lock: %+v %v", a, err)
	}
	if a.Item != "01ALPHA" {
		t.Errorf("alpha's master is held for %q", a.Item)
	}
	b, err := db.MergeLockOf(ctx, "beta", "master")
	if err != nil || b == nil {
		t.Fatalf("beta's lock: %+v %v", b, err)
	}
	if b.Item != "01BETA" {
		t.Errorf("beta's master is held for %q - it is reading another project's lock", b.Item)
	}

	// RELEASING ONE DOES NOT RELEASE THE OTHER.
	if _, err := db.ReleaseMergeLock(ctx, alpha, "alpha", "master", "01ALPHA"); err != nil {
		t.Fatalf("alpha's release: %v", err)
	}
	still, err := db.MergeLockOf(ctx, "beta", "master")
	if err != nil || still == nil {
		t.Fatalf("beta's lock did not survive alpha's release: %+v %v", still, err)
	}
	if !still.Live(now) {
		t.Error("beta's lock is not live after alpha released its own")
	}
}
