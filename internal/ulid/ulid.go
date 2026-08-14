// Package ulid implements a small, monotonic ULID: a 48-bit big-endian
// millisecond timestamp followed by 80 bits of randomness, rendered as 26
// characters of Crockford base32.
//
// Ids are lexicographically sortable and, within a single process, strictly
// increasing: two ids minted in the same millisecond differ by an increment of
// the random component rather than by a fresh draw, so generation order and
// sort order agree.
package ulid

import (
	crand "crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"
)

// encoding is Crockford base32: no I, L, O or U.
const encoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// EncodedSize is the length of the textual form.
const EncodedSize = 26

// dec maps an ASCII byte back to its 5-bit value, or 0xff when the byte is not
// part of the alphabet.
var dec [256]byte

func init() {
	for i := range dec {
		dec[i] = 0xff
	}
	for i := 0; i < len(encoding); i++ {
		c := encoding[i]
		dec[c] = byte(i)
		if c >= 'A' && c <= 'Z' {
			dec[c+('a'-'A')] = byte(i)
		}
	}
	// Crockford aliases: I and L read as 1, O reads as 0.
	for _, a := range []struct {
		c byte
		v byte
	}{{'i', 1}, {'I', 1}, {'l', 1}, {'L', 1}, {'o', 0}, {'O', 0}} {
		dec[a.c] = a.v
	}
}

// ID is the raw 128-bit identifier: 6 bytes of timestamp, 10 of randomness.
type ID [16]byte

// ErrInvalid is returned by Parse for anything that is not a well-formed ULID.
var ErrInvalid = errors.New("ulid: invalid id")

// Timestamp returns the embedded time in milliseconds since the Unix epoch.
func (id ID) Timestamp() int64 {
	var ms int64
	for _, b := range id[:6] {
		ms = ms<<8 | int64(b)
	}
	return ms
}

// Time returns the embedded timestamp as a time.Time in UTC.
func (id ID) Time() time.Time {
	return time.UnixMilli(id.Timestamp()).UTC()
}

// bit returns bit j of the 130-bit stream that the textual form encodes: two
// zero pad bits followed by the 128 bits of the id, most significant first.
func (id ID) bit(j int) byte {
	if j < 2 {
		return 0
	}
	k := j - 2
	return (id[k/8] >> (7 - uint(k%8))) & 1
}

// String renders the id as 26 Crockford base32 characters.
func (id ID) String() string {
	var out [EncodedSize]byte
	for i := 0; i < EncodedSize; i++ {
		var v byte
		for b := 0; b < 5; b++ {
			v = v<<1 | id.bit(i*5+b)
		}
		out[i] = encoding[v]
	}
	return string(out[:])
}

// Compare orders two ids the same way their textual forms compare.
func (id ID) Compare(other ID) int {
	for i := range id {
		switch {
		case id[i] < other[i]:
			return -1
		case id[i] > other[i]:
			return 1
		}
	}
	return 0
}

// Parse decodes the textual form produced by String.
func Parse(s string) (ID, error) {
	var id ID
	if len(s) != EncodedSize {
		return id, fmt.Errorf("%w: want %d chars, got %d", ErrInvalid, EncodedSize, len(s))
	}
	// The first character carries only 3 significant bits; anything larger
	// would overflow 128 bits.
	if dec[s[0]] > 7 {
		return id, fmt.Errorf("%w: leading character %q overflows", ErrInvalid, s[0])
	}
	var bits [130]byte
	for i := 0; i < EncodedSize; i++ {
		v := dec[s[i]]
		if v == 0xff {
			return id, fmt.Errorf("%w: character %q not in alphabet", ErrInvalid, s[i])
		}
		for b := 0; b < 5; b++ {
			bits[i*5+b] = (v >> uint(4-b)) & 1
		}
	}
	for k := 0; k < 128; k++ {
		if bits[k+2] == 1 {
			id[k/8] |= 1 << (7 - uint(k%8))
		}
	}
	return id, nil
}

// Generator mints monotonic ids. The zero value is not usable; call NewGenerator.
type Generator struct {
	mu     sync.Mutex
	lastMS int64
	rand   [10]byte
	now    func() int64
	fill   func([]byte) error
}

// NewGenerator returns a generator reading the system clock and crypto/rand.
func NewGenerator() *Generator {
	return &Generator{
		now: func() int64 { return time.Now().UnixMilli() },
		fill: func(b []byte) error {
			_, err := crand.Read(b)
			return err
		},
	}
}

// reseed draws fresh randomness. The top bit is cleared so that a full
// millisecond of increments cannot carry into the timestamp.
func (g *Generator) reseed() {
	if err := g.fill(g.rand[:]); err != nil {
		panic("ulid: entropy source failed: " + err.Error())
	}
	g.rand[0] &= 0x7f
}

// incr adds one to the 80-bit random component, reporting false on overflow.
func (g *Generator) incr() bool {
	for i := len(g.rand) - 1; i >= 0; i-- {
		g.rand[i]++
		if g.rand[i] != 0 {
			return true
		}
	}
	return false
}

// New mints the next id. It is safe for concurrent use and never returns an id
// that is less than or equal to one it returned earlier, even if the wall clock
// jumps backwards.
func (g *Generator) New() ID {
	ms := g.now()

	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case ms > g.lastMS:
		g.lastMS = ms
		g.reseed()
	default:
		// Same millisecond, or the clock went backwards: keep the last
		// timestamp and step the randomness so ordering still holds.
		if !g.incr() {
			g.lastMS++
			g.reseed()
		}
	}

	var id ID
	t := g.lastMS
	for i := 5; i >= 0; i-- {
		id[i] = byte(t)
		t >>= 8
	}
	copy(id[6:], g.rand[:])
	return id
}

// NewString mints the next id in textual form.
func (g *Generator) NewString() string { return g.New().String() }

var defaultGen = NewGenerator()

// New mints an id from the process-wide generator.
func New() ID { return defaultGen.New() }

// NewString mints an id from the process-wide generator in textual form.
func NewString() string { return defaultGen.NewString() }
