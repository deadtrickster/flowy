package store

// What a row IS, in one function, because the column that answers it is not one
// column.
//
// MEASURED, 551 rows on the live node: `type` reads memory 344, attachment 143,
// finding 40, report 16, bug 3, note 2, todo 1, diagram 1, announcement 1 - and
// under type=memory, `kind` reads todo 194, merge 89, note 50, report 9,
// diagram 2. So todo, note, report and diagram each exist BOTH as a type and as
// a kind of memory, neither side is empty, and a reader filtering on one misses
// the other in silence.
//
// `kind` IS THREE DIFFERENT FACTS SHARING A NAME. Under memory it says what the
// row is. Under finding it says what sort of defect it is - a crash and a
// correctness bug are both findings, so that is orthogonal to identity. Under
// attachment it is a media type. Only the first of the three is identity, which
// is why "type or kind" was the wrong question and why this cannot be a
// two-line ternary written at each call site.
//
// `memory` IS A STORAGE BUCKET, NOT A TYPE. Every row under it already carries
// its real identity in kind. Straightening that in the database is a migration
// of 344 rows and every query that filters them; this function makes the
// ambiguity invisible above the store so the migration can follow behind a
// column that already has exactly one reader.
//
// The rule and its consequence, from the ruling this file implements
// (01M0ANFYWY): identity is one column and one function answers it. A caller
// that reads .Type or .Kind to decide what a row is has decided it a second
// time, and two answers is the defect - not the spelling.
const memoryBucket = MemoryType

// EntityType is what a row is: its kind when the type is the memory bucket, and
// its type otherwise.
//
// A memory row with no kind falls back to the bucket rather than to "", because
// an empty answer here would read as "this row is nothing" at every call site,
// and the honest answer for a row nobody classified is the bucket it is in.
func EntityType(a *Artifact) string {
	if a == nil {
		return ""
	}
	if a.Type == memoryBucket && a.Kind != "" {
		return a.Kind
	}
	return a.Type
}

// IsEntityType reports whether a row is of the given resolved type. It exists
// so a caller says what it means - "is this a diagram" - rather than comparing
// strings and inviting the second answer this file exists to prevent.
func IsEntityType(a *Artifact, want string) bool { return EntityType(a) == want }
