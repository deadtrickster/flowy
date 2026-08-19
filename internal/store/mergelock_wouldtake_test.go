package store

import (
	"testing"
	"time"
)

// WouldTake is TakeMergeLock's WHERE clause written in Go, so the case that
// matters is the one the SQL was changed to handle: a seat holding the target
// for ANOTHER row of its own.
//
// Every subagent of a seat runs under its parent's token, so two processes of
// one seat resolve to the same holder. On 18 Aug a sibling session landed
// through a lock it never took and invalidated a live verdict mid-flight, and
// the fix was to record WHICH row the lock is held for. A predicate that
// forgets the row would hand that bug straight back to every caller asking
// "may I declare".
func TestALockHeldForAnotherRowOfYourOwnDoesNotAdmitThisOne(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	me := &Principal{UserID: "u-1"}
	them := &Principal{UserID: "u-2"}
	mine, _ := voteActor(me)

	held := func(item string, until time.Time) *MergeLock {
		return &MergeLock{Target: "master", Holder: mine, Item: item, Until: until}
	}

	// FREE IS TAKEABLE, including a lock that has run out. Nothing here is
	// about politeness: an expired lock is one the upsert overwrites.
	if !(*MergeLock)(nil).WouldTake(me, "01ROW", now) {
		t.Error("a target nothing holds refused a declaration")
	}
	if !held("01OTHER", now.Add(-time.Minute)).WouldTake(me, "01ROW", now) {
		t.Error("an expired lock refused a declaration - the upsert would have overwritten it")
	}

	// MY OWN LOCK FOR THIS ROW RENEWS. A re-gate after a rebase is the same
	// work measured again, and refusing it would make every rebase a wait.
	if !held("01ROW", now.Add(5*time.Minute)).WouldTake(me, "01ROW", now) {
		t.Error("my own lock for this row refused a re-declaration of it")
	}

	// MY OWN LOCK FOR ANOTHER ROW DOES NOT. This is the sibling-session bug.
	if held("01OTHER", now.Add(5*time.Minute)).WouldTake(me, "01ROW", now) {
		t.Error("a lock held for another row admitted a declaration for this one - " +
			"that is a sibling session landing through a lock it never took")
	}

	// AND IT IS STILL MINE, which is a different fact and has to stay one: a
	// reader told 'not yours' about their own session's lock goes looking for
	// somebody else to blame.
	if !held("01OTHER", now.Add(5*time.Minute)).HeldBy(me, now) {
		t.Error("my own lock for another row reported as not mine")
	}

	// SOMEBODY ELSE'S IS NEITHER.
	theirs := &MergeLock{Target: "master", Holder: "u-2", Item: "01ROW", Until: now.Add(time.Minute)}
	if theirs.WouldTake(me, "01ROW", now) {
		t.Error("somebody else's live lock admitted my declaration")
	}
	if theirs.HeldBy(me, now) {
		t.Error("somebody else's lock reported as mine")
	}
	if !theirs.HeldBy(them, now) {
		t.Error("their own lock did not report as theirs")
	}
}
