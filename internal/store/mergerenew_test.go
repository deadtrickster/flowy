package store

import (
	"os"
	"testing"
)

// The renew has one job and one prohibition, and the prohibition is the half
// worth testing: it must not become a second way to TAKE the target. These
// cases are written against the SQL's shape - an UPDATE matching on holder and
// item - because that is what makes taking impossible rather than merely
// unlikely.

// A renew is an UPDATE. An UPDATE that matches nothing changes nothing, so a
// caller who does not hold the lock cannot acquire it by renewing, and a free
// target stays free.
func TestRenewIsAnUpdateSoItCannotTake(t *testing.T) {
	const sql = renewMergeLockSQL
	for _, forbidden := range []string{"INSERT", "ON CONFLICT", "UPSERT"} {
		if containsFold(sql, forbidden) {
			t.Fatalf("renew must not be able to create a lock, found %q in:\n%s", forbidden, sql)
		}
	}
	for _, required := range []string{"UPDATE merge_locks", "holder = $2", "item = $4"} {
		if !containsFold(sql, required) {
			t.Errorf("renew must match the holder and the item, missing %q in:\n%s", required, sql)
		}
	}
}

// The window is stamped by the database, never by the caller: two clocks on one
// deadline is how a lock ends up believed for a length nobody chose.
func TestRenewWindowComesFromTheServerClock(t *testing.T) {
	if !containsFold(renewMergeLockSQL, "now() + $3::interval") {
		t.Errorf("renew must set until from the server's own clock:\n%s", renewMergeLockSQL)
	}
}

func containsFold(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexFold(haystack, needle) >= 0
}

func indexFold(haystack, needle string) int {
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + ('a' - 'A')
		}
		return b
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if lower(haystack[i+j]) != lower(needle[j]) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// A VERDICT NEEDS THE LOCK. The record branch does not take the target - taking
// is what declare is for - but it must not accept a measurement from somebody
// who never held it either.
//
// This is asserted against the SQL and the call shape rather than a database,
// for the same reason the renew cases above are: what makes it safe is that the
// renew cannot match a lock the caller does not hold, and that a false from it
// is now refused instead of ignored.
func TestRecordingAVerdictRefusesWhenNothingWasRenewed(t *testing.T) {
	src := readStoreSource(t, "mergegate.go")

	// The renew's answer must be read. Before this, the call discarded it with
	// `_`, so a non-holder's verdict was written as if it had been measured
	// under a lock nobody held.
	if containsFold(src, "if _, err := d.RenewMergeLock") {
		t.Error("the renew's answer is being discarded again - a false there is the whole check")
	}
	for _, want := range []string{"held, err := d.RenewMergeLock", "if !held {", "ErrTargetHeld"} {
		if !containsFold(src, want) {
			t.Errorf("mergegate.go should refuse a verdict with no lock behind it, missing %q", want)
		}
	}
}

// And the prohibition that must survive the fix: refusing is not taking. The
// record branch still must not call TakeMergeLock, or a verdict from somebody
// who never declared would acquire the target instead of being refused.
func TestRecordingAVerdictStillCannotTakeTheTarget(t *testing.T) {
	src := readStoreSource(t, "mergegate.go")
	after := src[indexFold(src, "if !declaring {"):]
	if containsFold(after, "TakeMergeLock") {
		t.Error("the record branch must never take the lock - that is what declare is for")
	}
}

func readStoreSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
