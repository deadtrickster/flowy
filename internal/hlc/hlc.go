// Package hlc implements a hybrid logical clock.
//
// A timestamp is a wall clock reading in milliseconds plus a logical counter
// that breaks ties, tagged with the node that produced it. Timestamps pack into
// a single sortable int64 (wall_ms<<16 | logical) which is what the hlc and
// seq_hlc columns store.
package hlc

import (
	"fmt"
	"sync"
	"time"
)

// LogicalBits is the width of the logical counter inside a packed value.
const LogicalBits = 16

// MaxLogical is the largest logical counter that fits.
const MaxLogical = 1<<LogicalBits - 1

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
func Pack(wallMS int64, logical uint16) int64 {
	return wallMS<<LogicalBits | int64(logical)
}

// Unpack splits a packed value back into its wall and logical parts.
func Unpack(packed int64) (int64, uint16) {
	return packed >> LogicalBits, uint16(packed & MaxLogical)
}

// UnpackTimestamp splits a packed value into a Timestamp tagged with node.
func UnpackTimestamp(packed int64, node string) Timestamp {
	wall, logical := Unpack(packed)
	return Timestamp{WallMS: wall, Logical: logical, Node: node}
}

// Clock is a hybrid logical clock. It is safe for concurrent use; every reading
// it hands out is strictly greater than the one before it.
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

// tick advances the counter past the given physical reading. Callers hold mu.
func (c *Clock) tick(phys int64) {
	if phys > c.wall {
		c.wall = phys
		c.logical = 0
		return
	}
	c.bump()
}

// bump advances the logical counter by one, carrying into the wall clock when
// the counter is exhausted. Callers hold mu.
func (c *Clock) bump() {
	if c.logical == MaxLogical {
		c.wall++
		c.logical = 0
		return
	}
	c.logical++
}

// Now returns the next local timestamp.
func (c *Clock) Now() Timestamp {
	phys := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.tick(phys)
	return Timestamp{WallMS: c.wall, Logical: c.logical, Node: c.node}
}

// Pack returns the next local timestamp already folded into an int64, which is
// what the hlc and seq_hlc columns want.
func (c *Clock) Pack() int64 { return c.Now().Pack() }

// Update merges a timestamp observed from another node and returns the local
// reading that results. The returned timestamp is greater than both the remote
// one and anything this clock handed out earlier.
func (c *Clock) Update(remote Timestamp) Timestamp {
	phys := c.now()

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
