package ulid

// A SHORT NAME THAT IS ACTUALLY UNIQUE.
//
// Tests and fixtures want a few characters to hang on the end of a project or
// a handle, and nineteen places in this tree reached for `ulid.Short()`
// to get them. That is not a short id. It is a CLOCK READING.
//
// A ULID is 10 characters of millisecond timestamp followed by 16 of
// randomness, so a prefix shorter than 11 characters contains no randomness at
// all - it is the high bits of the clock, and every caller inside one tick of
// those bits gets the SAME string:
//
//	[:6]   30 bits of a 48-bit millisecond count   constant for 2^18 ms, ~4.4 minutes
//	[:8]   40 bits                                 constant for 2^8 ms, ~0.26 seconds
//
// MEASURED 2026-08-21, two ids 1.2 seconds apart: 01M0HJ vs 01M0HJ, equal.
// That is why four store tests passed once per database and red on the second
// run - a "unique" project name that repeats for four minutes is a fixture
// two runs share, and the second run collides with the first run's rows. It
// cost three false diagnoses in one night, twice explained away as residue.
//
// Short returns the RANDOM half instead, which is what the callers meant: 50
// bits, independent of when it was called, and it still sorts nowhere - if you
// want the ordering, you want the whole id.
func Short() string { return NewString()[EncodedSize-10:] }
