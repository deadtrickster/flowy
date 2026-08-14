package ulid

import (
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStringLengthAndAlphabet(t *testing.T) {
	id := New()
	s := id.String()
	if len(s) != EncodedSize {
		t.Fatalf("len(%q) = %d, want %d", s, len(s), EncodedSize)
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(encoding, rune(s[i])) {
			t.Fatalf("character %q at %d is not in the Crockford alphabet", s[i], i)
		}
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	before := time.Now().UnixMilli()
	id := New()
	after := time.Now().UnixMilli()

	if got := id.Timestamp(); got < before || got > after {
		t.Fatalf("timestamp %d outside [%d, %d]", got, before, after)
	}
	parsed, err := Parse(id.String())
	if err != nil {
		t.Fatalf("Parse(%q): %v", id.String(), err)
	}
	if parsed != id {
		t.Fatalf("round trip changed the id: %x -> %x", id, parsed)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	valid := New().String()
	cases := map[string]string{
		"too short":        valid[:25],
		"too long":         valid + "0",
		"letter U":         "U" + valid[1:],
		"overflow leading": "Z" + valid[1:],
	}
	for name, in := range cases {
		if _, err := Parse(in); err == nil {
			t.Errorf("%s: Parse(%q) returned no error", name, in)
		}
	}
}

func TestParseAcceptsCrockfordAliases(t *testing.T) {
	id := ID{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	s := id.String()
	lower := strings.ToLower(s)
	parsed, err := Parse(lower)
	if err != nil {
		t.Fatalf("Parse(%q): %v", lower, err)
	}
	if parsed != id {
		t.Fatalf("lowercase parse mismatch: %x != %x", parsed, id)
	}
	// I, L and O are aliases for 1, 1 and 0.
	withAliases := strings.NewReplacer("1", "I", "0", "O").Replace(s)
	parsed, err = Parse(withAliases)
	if err != nil {
		t.Fatalf("Parse(%q): %v", withAliases, err)
	}
	if parsed != id {
		t.Fatalf("alias parse mismatch: %x != %x", parsed, id)
	}
}

// TestMonotonicWithinMillisecond pins the generator to a single millisecond so
// every id has to come from an increment of the random component.
func TestMonotonicWithinMillisecond(t *testing.T) {
	g := NewGenerator()
	g.now = func() int64 { return 1_700_000_000_000 }
	g.fill = func(b []byte) error {
		for i := range b {
			b[i] = 0
		}
		return nil
	}

	const n = 100_000
	prev := g.New()
	for i := 1; i < n; i++ {
		id := g.New()
		if id.Compare(prev) <= 0 {
			t.Fatalf("id %d (%s) did not advance past %s", i, id, prev)
		}
		if id.Timestamp() != 1_700_000_000_000 {
			t.Fatalf("id %d carried into the timestamp: %d", i, id.Timestamp())
		}
		prev = id
	}
}

// TestBackwardsClock checks that a clock that jumps backwards cannot produce a
// smaller id.
func TestBackwardsClock(t *testing.T) {
	g := NewGenerator()
	ms := int64(1_700_000_000_000)
	g.now = func() int64 { return ms }

	first := g.New()
	ms -= 10_000
	second := g.New()

	if second.Compare(first) <= 0 {
		t.Fatalf("after a backwards clock, %s did not advance past %s", second, first)
	}
	if second.Timestamp() != first.Timestamp() {
		t.Fatalf("timestamp moved with the backwards clock: %d -> %d", first.Timestamp(), second.Timestamp())
	}
}

// TestUniqueAndSorted is the property the gate asserts: a batch of ids is
// unique and already in sorted order.
func TestUniqueAndSorted(t *testing.T) {
	const n = 10_000
	ids := make([]string, n)
	seen := make(map[string]int, n)
	for i := range ids {
		ids[i] = NewString()
		if prev, dup := seen[ids[i]]; dup {
			t.Fatalf("id %d duplicates id %d: %s", i, prev, ids[i])
		}
		seen[ids[i]] = i
	}
	if !sort.SliceIsSorted(ids, func(a, b int) bool { return ids[a] < ids[b] }) {
		t.Fatal("generation order does not match sort order")
	}
}

func TestConcurrentGeneratorIsUnique(t *testing.T) {
	const goroutines, per = 8, 2_000
	var wg sync.WaitGroup
	out := make([][]string, goroutines)
	for gi := 0; gi < goroutines; gi++ {
		wg.Add(1)
		go func(gi int) {
			defer wg.Done()
			batch := make([]string, per)
			for i := range batch {
				batch[i] = NewString()
			}
			out[gi] = batch
		}(gi)
	}
	wg.Wait()

	seen := make(map[string]struct{}, goroutines*per)
	for gi, batch := range out {
		for i, id := range batch {
			if _, dup := seen[id]; dup {
				t.Fatalf("goroutine %d id %d is a duplicate: %s", gi, i, id)
			}
			seen[id] = struct{}{}
		}
		if !sort.StringsAreSorted(batch) {
			t.Fatalf("goroutine %d saw its own ids out of order", gi)
		}
	}
}

// TestOverflowCarriesIntoTimestamp drives the random component to its last
// value by hand. reseed clears the top bit, so reaching this state honestly
// would take 2^79 ids inside one millisecond.
func TestOverflowCarriesIntoTimestamp(t *testing.T) {
	const ms = int64(1_700_000_000_000)

	g := NewGenerator()
	g.now = func() int64 { return ms }
	g.lastMS = ms
	for i := range g.rand {
		g.rand[i] = 0xff
	}
	exhausted := ID{}
	copy(exhausted[6:], g.rand[:])

	got := g.New()
	if got.Timestamp() != ms+1 {
		t.Fatalf("overflow did not carry: %d, want %d", got.Timestamp(), ms+1)
	}
	if got.Compare(exhausted) <= 0 {
		t.Fatalf("%s did not advance past the exhausted component", got)
	}
}
