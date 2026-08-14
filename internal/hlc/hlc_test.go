package hlc

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	cases := []struct {
		wall    int64
		logical uint16
	}{
		{0, 0},
		{1, 1},
		{1_700_000_000_000, 0},
		{1_700_000_000_000, MaxLogical},
	}
	for _, c := range cases {
		packed := Pack(c.wall, c.logical)
		wall, logical := Unpack(packed)
		if wall != c.wall || logical != c.logical {
			t.Errorf("Pack(%d, %d) = %d unpacked to (%d, %d)", c.wall, c.logical, packed, wall, logical)
		}
	}
}

func TestPackedOrderMatchesLogicalOrder(t *testing.T) {
	ordered := []Timestamp{
		{WallMS: 10, Logical: 0},
		{WallMS: 10, Logical: 1},
		{WallMS: 10, Logical: MaxLogical},
		{WallMS: 11, Logical: 0},
	}
	for i := 1; i < len(ordered); i++ {
		if !ordered[i-1].Before(ordered[i]) {
			t.Errorf("%s does not pack before %s", ordered[i-1], ordered[i])
		}
	}
}

func TestNowAdvancesOnTies(t *testing.T) {
	c := New("node-a")
	c.now = func() int64 { return 1_000 }

	prev := c.Now()
	if prev.WallMS != 1_000 || prev.Logical != 0 {
		t.Fatalf("first reading = %s, want 1000.0", prev)
	}
	for i := 0; i < 100; i++ {
		got := c.Now()
		if got.Pack() <= prev.Pack() {
			t.Fatalf("reading %d (%s) did not advance past %s", i, got, prev)
		}
		if got.WallMS != 1_000 {
			t.Fatalf("wall clock moved without the physical clock: %s", got)
		}
		if got.Node != "node-a" {
			t.Fatalf("node tag = %q, want node-a", got.Node)
		}
		prev = got
	}
}

func TestNowSurvivesBackwardsClock(t *testing.T) {
	c := New("node-a")
	wall := int64(5_000)
	c.now = func() int64 { return wall }

	first := c.Now()
	wall = 1_000
	second := c.Now()

	if second.Pack() <= first.Pack() {
		t.Fatalf("%s did not advance past %s after the clock went backwards", second, first)
	}
	if second.WallMS != first.WallMS {
		t.Fatalf("wall clock followed the backwards jump: %s -> %s", first, second)
	}
}

func TestLogicalOverflowCarries(t *testing.T) {
	c := New("node-a")
	c.now = func() int64 { return 1_000 }
	c.wall = 1_000
	c.logical = MaxLogical

	got := c.Now()
	if got.WallMS != 1_001 || got.Logical != 0 {
		t.Fatalf("overflow gave %s, want 1001.0", got)
	}
	if got.Pack() <= Pack(1_000, MaxLogical) {
		t.Fatalf("overflow went backwards: %s", got)
	}
}

func TestUpdateAdoptsRemote(t *testing.T) {
	c := New("local")
	c.now = func() int64 { return 1_000 }
	local := c.Now()

	remote := Timestamp{WallMS: 9_000, Logical: 4, Node: "remote"}
	merged := c.Update(remote)

	if merged.Pack() <= remote.Pack() {
		t.Fatalf("merge %s did not advance past the remote %s", merged, remote)
	}
	if merged.Pack() <= local.Pack() {
		t.Fatalf("merge %s went behind the local reading %s", merged, local)
	}
	if merged.WallMS != 9_000 || merged.Logical != 5 {
		t.Fatalf("merge = %s, want 9000.5", merged)
	}
	if merged.Node != "local" {
		t.Fatalf("merge kept the remote node tag: %s", merged)
	}

	next := c.Now()
	if next.Pack() <= merged.Pack() {
		t.Fatalf("reading after a merge went backwards: %s then %s", merged, next)
	}
}

func TestUpdateIgnoresStaleRemote(t *testing.T) {
	c := New("local")
	c.now = func() int64 { return 5_000 }
	local := c.Now()

	merged := c.Update(Timestamp{WallMS: 10, Logical: 3, Node: "remote"})
	if merged.Pack() <= local.Pack() {
		t.Fatalf("stale merge %s did not advance past %s", merged, local)
	}
	if merged.WallMS != 5_000 {
		t.Fatalf("stale merge moved the wall clock: %s", merged)
	}
}

func TestUpdateOnEqualWallTakesLargerLogical(t *testing.T) {
	c := New("local")
	c.now = func() int64 { return 1_000 }
	c.Now() // 1000.0

	merged := c.Update(Timestamp{WallMS: 1_000, Logical: 41, Node: "remote"})
	if merged.WallMS != 1_000 || merged.Logical != 42 {
		t.Fatalf("merge = %s, want 1000.42", merged)
	}
}

func TestUpdatePackedRoundTrip(t *testing.T) {
	c := New("local")
	remote := Pack(9_000, 7)
	got := c.UpdatePacked(remote, "remote")
	if got <= remote {
		t.Fatalf("UpdatePacked(%d) = %d, which is not greater", remote, got)
	}
}

// TestConcurrentMonotonic is the property the gate asserts: eight goroutines
// minting five thousand readings each produce no duplicate and no value that
// goes backwards.
func TestConcurrentMonotonic(t *testing.T) {
	const goroutines, per = 8, 5_000

	c := New("node-a")
	var wg sync.WaitGroup
	out := make([][]int64, goroutines)

	for gi := 0; gi < goroutines; gi++ {
		wg.Add(1)
		go func(gi int) {
			defer wg.Done()
			batch := make([]int64, per)
			for i := range batch {
				batch[i] = c.Pack()
			}
			out[gi] = batch
		}(gi)
	}
	wg.Wait()

	all := make([]int64, 0, goroutines*per)
	for gi, batch := range out {
		for i := 1; i < len(batch); i++ {
			if batch[i] <= batch[i-1] {
				t.Fatalf("goroutine %d went backwards at %d: %d then %d", gi, i, batch[i-1], batch[i])
			}
		}
		all = append(all, batch...)
	}

	sort.Slice(all, func(a, b int) bool { return all[a] < all[b] })
	for i := 1; i < len(all); i++ {
		if all[i] == all[i-1] {
			t.Fatalf("duplicate packed value %d", all[i])
		}
	}
}

// TestConcurrentUpdate mixes local readings with merges from a peer clock.
func TestConcurrentUpdate(t *testing.T) {
	const goroutines, per = 8, 2_000

	c := New("local")
	peer := New("remote")

	var wg sync.WaitGroup
	var count int64
	seen := make([][]int64, goroutines)

	for gi := 0; gi < goroutines; gi++ {
		wg.Add(1)
		go func(gi int) {
			defer wg.Done()
			batch := make([]int64, 0, per)
			for i := 0; i < per; i++ {
				if i%2 == 0 {
					batch = append(batch, c.Pack())
				} else {
					batch = append(batch, c.Update(peer.Now()).Pack())
				}
				atomic.AddInt64(&count, 1)
			}
			seen[gi] = batch
		}(gi)
	}
	wg.Wait()

	if count != goroutines*per {
		t.Fatalf("counted %d readings, want %d", count, goroutines*per)
	}
	uniq := make(map[int64]struct{}, goroutines*per)
	for gi, batch := range seen {
		for i := 1; i < len(batch); i++ {
			if batch[i] <= batch[i-1] {
				t.Fatalf("goroutine %d went backwards at %d: %d then %d", gi, i, batch[i-1], batch[i])
			}
		}
		for _, v := range batch {
			if _, dup := uniq[v]; dup {
				t.Fatalf("duplicate packed value %d", v)
			}
			uniq[v] = struct{}{}
		}
	}
}
