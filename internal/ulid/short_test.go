package ulid

import "testing"

// THE TEST IS THE MEASUREMENT THE DOC COMMENT CLAIMS, and it asserts both
// halves, because only the pair says anything: a prefix that repeats while
// Short does not is the whole reason Short exists. Written as one loop rather
// than two ids so it also covers the ordinary case - many fixtures minted
// inside one millisecond, which is what a package test actually does.
func TestShortDoesNotRepeatWhereAPrefixDoes(t *testing.T) {
	const n = 2000

	seen := make(map[string]bool, n)
	prefixes := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id := NewString()
		prefixes[id[:6]] = true
		s := Short()
		if seen[s] {
			t.Fatalf("Short repeated %q within %d calls", s, n)
		}
		seen[s] = true
	}
	if len(seen) != n {
		t.Fatalf("Short minted %d distinct values in %d calls", len(seen), n)
	}
	// THE NEGATIVE CONTROL, INSIDE THE TEST. If this ever stops holding, the
	// truncation was never the bug and this whole file is answering the wrong
	// question - so it must fail loudly rather than quietly agreeing.
	if len(prefixes) != 1 {
		t.Fatalf("the 6-character prefix varied over %d calls (%d values) - "+
			"it is meant to be a clock tick, so Short's reason for existing needs re-measuring",
			n, len(prefixes))
	}
}

// AND IT IS THE RANDOM HALF, not a shorter clock: Short's value must never be
// a prefix of the id it came from.
func TestShortIsTheRandomHalf(t *testing.T) {
	if got := len(Short()); got != 10 {
		t.Fatalf("Short is %d characters, want 10", got)
	}
	id := NewString()
	if tail := id[EncodedSize-10:]; id[:10] == tail {
		t.Fatalf("the tail of %s equals its timestamp half", id)
	}
}
