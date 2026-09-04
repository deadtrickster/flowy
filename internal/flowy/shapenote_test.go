package flowy

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// A TEN-CHARACTER ID IS A CLOCK READING, AND THE REFUSAL SAYS SO.
//
// 01M0BQ2RF98DSC2D3B73KYNG9W. Rooms speak ten characters because that is what a
// person can repeat; every door takes twenty-six. The short form answered
// "no such todo: 01M0BQ2RF9 - searched flowy, which is what this credential
// reads", which reads as "the row is not in this project" and sent three
// readings wrong across two agents, one of whom wrote a ruling on top of it.
//
// THE ASSERTIONS ARE DIFFERENCES. A sentence that appeared for every id would
// pass any single check while saying nothing, so each case is measured against
// the one next to it: short against whole, invalid against short, and the
// ordinary 404 against all of them.
func TestAShortIdIsRefusedAsAPartOfAnId(t *testing.T) {
	whole := ulid.NewString()

	t.Run("a whole, well-formed id gets no shape note at all", func(t *testing.T) {
		// THE ONE THAT KEEPS THE ORDINARY 404 ORDINARY. If this ever returns a
		// sentence, every missing row in the fabric starts being called
		// malformed, and the note that exists to end a misreading becomes one.
		if note := shapeNote(whole); note != "" {
			t.Fatalf("a whole id was called malformed: %q", note)
		}
	})

	t.Run("the ten-character form says it is a part and why", func(t *testing.T) {
		note := shapeNote(whole[:10])
		if note == "" {
			t.Fatal("the ten-character form - the one rooms actually speak - got no sentence")
		}
		if shapeNote(whole) == note {
			t.Fatal("a prefix and a whole id answer the same way, so the check is not firing")
		}
		if !strings.Contains(note, "clock reading") {
			t.Fatalf("the sentence does not say the ten characters are a clock: %q", note)
		}
	})

	// AND THE CLOCK CLAUSE ONLY WHERE IT IS TRUE. Past the tenth character a
	// prefix has begun carrying randomness, so calling it a clock reading would
	// replace one confident falsehood with another.
	t.Run("a longer prefix is still a part, but is not called a clock", func(t *testing.T) {
		note := shapeNote(whole[:20])
		if note == "" {
			t.Fatal("a twenty-character id got no sentence")
		}
		if strings.Contains(note, "clock reading") {
			t.Fatalf("twenty characters carry randomness and were called a clock: %q", note)
		}
	})

	t.Run("the right length but not an id says something different", func(t *testing.T) {
		bad := strings.Repeat("!", ulid.EncodedSize)
		note := shapeNote(bad)
		if note == "" {
			t.Fatal("a 26-character non-id got no sentence")
		}
		if note == shapeNote(whole[:10]) {
			t.Fatal("a mistyped id and a prefix say the same thing, but the next move differs")
		}
		if strings.Contains(note, "part of one") {
			t.Fatalf("a full-length mistyped id was called a prefix: %q", note)
		}
	})

	t.Run("an empty id is left alone", func(t *testing.T) {
		if note := shapeNote(""); note != "" {
			t.Fatalf("an empty id produced a sentence: %q", note)
		}
	})
}

// THE SAFETY PROPERTY, PROVED BY CONSTRUCTION RATHER THAN ASSERTED.
//
// The other two diagnoses in notFoundNote run a permission-filtered read before
// they speak. This one must not, because it has to work for a prefix that
// matches nothing and for a caller who can read nothing - and because a door
// that queried the store on a malformed id would be answering a question about
// the store's contents.
//
// So the server here has a NIL DATABASE. If notFoundNote reached the store for
// a short id, this test would panic rather than fail, which is a louder signal
// than any assertion about what the sentence contains.
func TestTheShapeNoteAsksTheStoreNothing(t *testing.T) {
	s := &server{}
	r := httptest.NewRequest("GET", "/api/todo/01M0BQ2RF9/notes", nil)
	// A principal with no project and no reach: the worst case for every other
	// diagnosis here, and irrelevant to this one.
	r = withPrincipal(r, &store.Principal{UserID: "u"})

	note := s.notFoundNote(r, "01M0BQ2RF9")
	if note == "" {
		t.Fatal("a caller with no reach got no sentence about their own argument")
	}
	if !strings.Contains(note, "characters") {
		t.Fatalf("the answer was not the shape note: %q", note)
	}
	// AND IT OUTRANKS THE SCOPE NOTE, which is the actual defect: "searched
	// flowy" is a true sentence about a search that could not have found
	// anything, and it is what sent the reader looking in other projects.
	if strings.Contains(note, "searched") {
		t.Fatalf("the scope note still answers a malformed id: %q", note)
	}
}
