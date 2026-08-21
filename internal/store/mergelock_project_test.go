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

	// THE NAME BOTH REPOSITORIES USE, and deliberately not the literal
	// "master". This suite shares one database with a running node, and a lock
	// taken on the real master here is never released - the test proves lock
	// independence by releasing alpha's and leaving beta's - so it is held for
	// MergeLockBelievedFor, fifteen minutes, by a test that finished in a
	// second. /api/merge-queue asked with no project answers about the target,
	// so from here to the end of the run every check that asks whether the gate
	// is free is told u-beta is gating. One console check spent an hour being
	// debugged for reporting exactly that.
	//
	// The property under test is that TWO PROJECTS SHARE ONE TARGET NAME and do
	// not contend, which one name used twice states as exactly as "master" did.
	// ownTarget is what every other lock test in this package already uses.
	target := ownTarget(t)

	if _, err := db.TakeMergeLock(ctx, alpha, "alpha", target, "01ALPHA"); err != nil {
		t.Fatalf("alpha could not take its own master: %v", err)
	}

	// THE OTHER PROJECT'S MASTER IS A DIFFERENT TARGET. On the broken version
	// this is refused with alpha's holder and alpha's item.
	if _, err := db.TakeMergeLock(ctx, beta, "beta", target, "01BETA"); err != nil {
		t.Fatalf("beta was refused its own master by another project's lock: %v", err)
	}

	// AND THE SAME PROJECT STILL CONTENDS, which is the arm that fails if the
	// key were made unique enough to stop locking anything at all.
	other := &Principal{UserID: "u-other", Project: "alpha"}
	if _, err := db.TakeMergeLock(ctx, other, "alpha", target, "01OTHER"); err == nil {
		t.Error("a second declarer in the SAME project took a held target - the lock " +
			"stopped locking rather than started scoping")
	}

	// EACH READS ITS OWN HOLDER. A read that crossed projects would report the
	// wrong seat to whoever is deciding whether to wait.
	a, err := db.MergeLockOf(ctx, "alpha", target)
	if err != nil || a == nil {
		t.Fatalf("alpha's lock: %+v %v", a, err)
	}
	if a.Item != "01ALPHA" {
		t.Errorf("alpha's master is held for %q", a.Item)
	}
	b, err := db.MergeLockOf(ctx, "beta", target)
	if err != nil || b == nil {
		t.Fatalf("beta's lock: %+v %v", b, err)
	}
	if b.Item != "01BETA" {
		t.Errorf("beta's master is held for %q - it is reading another project's lock", b.Item)
	}

	// RELEASING ONE DOES NOT RELEASE THE OTHER.
	if _, err := db.ReleaseMergeLock(ctx, alpha, "alpha", target, "01ALPHA"); err != nil {
		t.Fatalf("alpha's release: %v", err)
	}
	still, err := db.MergeLockOf(ctx, "beta", target)
	if err != nil || still == nil {
		t.Fatalf("beta's lock did not survive alpha's release: %+v %v", still, err)
	}
	if !still.Live(now) {
		t.Error("beta's lock is not live after alpha released its own")
	}
}

// AN UNNAMED PROJECT ASKS ABOUT THE TARGET; A NAMED ONE ASKS ABOUT ITS OWN.
//
// The landing outage: land-guard.sh asks /api/merge-queue with no project, that
// door passed "" through, and the read looked for the legacy ” row twice while
// the gate had taken the lock under "flowy". The guard concluded nobody held
// master and refused every ref update.
//
// The two questions are genuinely different and this pins both. The guard is
// about to move a ref on THIS MACHINE and wants to know whether anybody is
// mid-landing. A project asking about its own target must NOT be told about
// somebody else's hold: per-project checkouts mean their masters are different
// refs, which is why the lock is keyed this way at all.
func TestAnUnnamedProjectAsksAboutTheTarget(t *testing.T) {
	ctx, db := open(t)
	// A TARGET OF ITS OWN. The database is shared with every other test in this
	// package, and the first version of this asserted "beta holds nothing" on a
	// target beta had taken a lock on two tests earlier - a failure about the
	// fixture wearing the face of a failure about the code.
	target := ownTarget(t)
	holder := &Principal{UserID: "u-alpha", Project: "alpha"}
	if _, err := db.TakeMergeLock(ctx, holder, "alpha", target, "01ALPHA"); err != nil {
		t.Fatalf("alpha could not take its master: %v", err)
	}

	// THE GUARD'S QUESTION. No project named, and it must see the hold - this
	// returning nil is the outage.
	any, err := db.MergeLockOf(ctx, "", target)
	if err != nil {
		t.Fatalf("unnamed read: %v", err)
	}
	if any == nil {
		t.Fatal("an unnamed read saw no lock while alpha holds the target - this is the " +
			"shape that told land-guard.sh nobody held the target and broke every landing")
	}
	if any.Item != "01ALPHA" {
		t.Errorf("the unnamed read found %q", any.Item)
	}

	// A NAMED PROJECT WITH NO LOCK OF ITS OWN SEES NOTHING, rather than being
	// told about alpha's. Their masters are different refs.
	mine, err := db.MergeLockOf(ctx, "beta", target)
	if err != nil {
		t.Fatalf("beta read: %v", err)
	}
	if mine != nil {
		t.Errorf("beta was told the target is held for %q, which is alpha's row in alpha's "+
			"checkout - cross-project contention is what keying the lock removed", mine.Item)
	}

	// AND A NAMED PROJECT WITH ONE SEES ITS OWN.
	own, err := db.MergeLockOf(ctx, "alpha", target)
	if err != nil || own == nil || own.Item != "01ALPHA" {
		t.Fatalf("alpha could not read its own lock: %+v %v", own, err)
	}
}
