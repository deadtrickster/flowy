package store

import "testing"

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
