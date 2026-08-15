// Package hlc implements a hybrid logical clock.
//
// A timestamp is a wall clock reading in milliseconds plus a logical counter
// that breaks ties, tagged with the node that produced it. Timestamps pack into
// a single sortable int64 (wall_ms<<16 | logical) which is what the hlc and
// seq_hlc columns store.
package hlc

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// LogicalBits is the width of the logical counter inside a packed value.
const LogicalBits = 16

// MaxLogical is the largest logical counter that fits.
const MaxLogical = 1<<LogicalBits - 1

// MaxWallMS is the largest wall reading that still packs into a positive
// int64. A reading above it would shift its top bits into the sign and come
// back out of Pack as a negative number - which sorts below every reading the
// node has ever written, so every later write would lose its merge and
// replication would never move again. It is roughly the year 6.6 million, so
// clamping to it costs nothing a real clock will ever notice.
const MaxWallMS = math.MaxInt64 >> LogicalBits

// Timestamp is a single reading of a hybrid logical clock.
type Timestamp struct {
	WallMS  int64  `json:"wall_ms"`
	Logical uint16 `json:"logical"`
	Node    string `json:"node"`
}

// Pack folds the timestamp into one sortable int64.
func (t Timestamp) Pack() int64 { return Pack(t.WallMS, t.Logical) }

// Before reports whether t orders strictly before other, ignoring the node.
func (t Timestamp) Before(other Timestamp) bool { return t.Pack() < other.Pack() }

// String renders the timestamp for logs.
func (t Timestamp) String() string {
	return fmt.Sprintf("%d.%d@%s", t.WallMS, t.Logical, t.Node)
}

// Pack folds a wall/logical pair into one sortable int64.
//
// The wall reading is clamped rather than shifted blindly: a reading a peer
// made up - MaxInt64, say - would otherwise pack to a negative number and every
// reading this node ever handed out would sort above it. Packing is the one
// place that can happen, so it is the one place that has to refuse.
func Pack(wallMS int64, logical uint16) int64 {
	if wallMS < 0 {
		wallMS = 0
	}
	if wallMS > MaxWallMS {
		wallMS = MaxWallMS
	}
	return wallMS<<LogicalBits | int64(logical)
}

// Unpack splits a packed value back into its wall and logical parts.
func Unpack(packed int64) (int64, uint16) {
	return packed >> LogicalBits, uint16(packed & MaxLogical)
}

// MaxSkew is how far ahead of this node's physical clock a reading made
// somewhere else may be and still be believed. A day is generous for a clock
// that is merely wrong and impossible for one that is lying: a reading further
// ahead than this drags every node that merges it along with it, and nothing
// brings the fabric back down again.
const MaxSkew = 24 * time.Hour

// BelievableAt reports whether packed is a reading a working clock could have
// produced at nowMS: not negative, and no further ahead than MaxSkew.
func BelievableAt(packed, nowMS int64) bool {
	if packed < 0 {
		return false
	}
	wall, _ := Unpack(packed)
	return wall <= nowMS+MaxSkew.Milliseconds()
}

// Believable is BelievableAt against this machine's clock.
func Believable(packed int64) bool {
	return BelievableAt(packed, time.Now().UnixMilli())
}

// UnpackTimestamp splits a packed value into a Timestamp tagged with node.
func UnpackTimestamp(packed int64, node string) Timestamp {
	wall, logical := Unpack(packed)
	return Timestamp{WallMS: wall, Logical: logical, Node: node}
}

// Clock is a hybrid logical clock. It is safe for concurrent use; every reading
// it hands out is strictly greater than the one before it - and when it can no
// longer be, it hands out none and says so rather than repeating itself.
type Clock struct {
	mu      sync.Mutex
	wall    int64
	logical uint16
	node    string
	now     func() int64
}

// New returns a clock tagged with node, reading the system wall clock.
func New(node string) *Clock {
	return &Clock{
		node: node,
		now:  func() int64 { return time.Now().UnixMilli() },
	}
}

// Node returns the node tag this clock stamps onto its readings.
func (c *Clock) Node() string { return c.node }

// ErrSaturated is what a clock at the top of its range answers a request for a
// reading with. It is the end of the line: wall and logical are both at their
// maximum, so there is no value left that is greater than the last one handed
// out, and handing that one out again would give two rows one reading - which
// is the merge silently dropping the second of them.
//
// It takes a local wall clock at roughly the year 6.6 million to get here. A
// reading off the wire cannot: checkReadings refuses anything past now+MaxSkew
// and Pack clamps below MaxWallMS. So this is a broken machine, and the write
// that wanted the reading fails rather than proceeding with a duplicate.
var ErrSaturated = errors.New("hlc: clock is saturated: no reading left above the last one")

// tick advances the counter past the given physical reading, and reports
// whether it moved. Callers hold mu.
func (c *Clock) tick(phys int64) bool {
	if phys > c.wall {
		c.wall = phys
		c.logical = 0
		return true
	}
	return c.bump()
}

// bump advances the logical counter by one, carrying into the wall clock when
// the counter is exhausted, and reports whether it moved. Callers hold mu.
//
// The carry saturates at MaxWallMS rather than wrapping: a clock that has been
// pushed to the top of the range by a bad reading stops advancing, which is a
// clock that has stopped rather than a clock that has gone negative. Saying so
// is the caller's business - see Now.
func (c *Clock) bump() bool {
	if c.logical == MaxLogical {
		if c.wall >= MaxWallMS {
			return false
		}
		c.wall++
		c.logical = 0
		return true
	}
	c.logical++
	return true
}

// Now returns the next local timestamp, which is strictly greater than every
// reading this clock has handed out before it. When there is no such value left
// it returns ErrSaturated and no timestamp: the alternative is repeating the
// last reading, which two rows would then share, and the older of them loses a
// merge it never took part in.
func (c *Clock) Now() (Timestamp, error) {
	phys := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.tick(phys) {
		return Timestamp{}, ErrSaturated
	}
	return Timestamp{WallMS: c.wall, Logical: c.logical, Node: c.node}, nil
}

// Pack returns the next local timestamp already folded into an int64, which is
// what the hlc and seq_hlc columns want. A saturated clock returns 0 and
// ErrSaturated, so a caller that ignores the error stamps nothing usable.
func (c *Clock) Pack() (int64, error) {
	t, err := c.Now()
	if err != nil {
		return 0, err
	}
	return t.Pack(), nil
}

// Reading returns where the clock stands without moving it.
//
// Now is the only way to get a reading to stamp a row with, and it has to
// advance: two writes must never share one. But a caller that only wants to
// report the clock - /healthz says what this node's reading is - is not writing
// anything, and asking through Now made looking at the clock a use of it. An
// unauthenticated probe could then walk the logical counter up on its own, one
// request at a time, which is the counter being spent by somebody who wrote
// nothing.
func (c *Clock) Reading() Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Timestamp{WallMS: c.wall, Logical: c.logical, Node: c.node}
}

// Update merges a timestamp observed from another node and returns the local
// reading that results. The returned timestamp is greater than both the remote
// one and anything this clock handed out earlier.
//
// It is not a reading to stamp a row with - it is the clock learning what has
// been seen - so a saturated clock is not an error here: it simply does not
// move, and the next Now that wanted a value to write says so.
func (c *Clock) Update(remote Timestamp) Timestamp {
	phys := c.now()

	// A remote reading is somebody else's claim about the time. Adopting one
	// past the top of the range would leave this clock unable to hand out a
	// reading greater than the one it just took, which is the end of every
	// merge it takes part in afterwards.
	if remote.WallMS > MaxWallMS {
		remote.WallMS = MaxWallMS
	}
	if remote.WallMS < 0 {
		remote.WallMS = 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case remote.WallMS > c.wall && remote.WallMS >= phys:
		// The remote is ahead of us: adopt its wall and step past it.
		c.wall = remote.WallMS
		c.logical = remote.Logical
		c.bump()
	case remote.WallMS == c.wall && c.wall >= phys:
		// Same wall reading: take the larger counter, then step past it.
		if remote.Logical > c.logical {
			c.logical = remote.Logical
		}
		c.bump()
	default:
		c.tick(phys)
	}
	return Timestamp{WallMS: c.wall, Logical: c.logical, Node: c.node}
}

// UpdatePacked merges a packed timestamp received from node and returns the
// resulting local packed value.
func (c *Clock) UpdatePacked(packed int64, node string) int64 {
	return c.Update(UnpackTimestamp(packed, node)).Pack()
}
