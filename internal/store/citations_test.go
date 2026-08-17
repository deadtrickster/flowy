package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// What a citation records, and what it does not. The row holds a pointer and a
// span; the quoted words are never on it, because a copy the citing author
// controls is a quotation of somebody else that nobody checked.
//
// The two shapes and the round trip, because the meta value is what a peer
// reads back and what an older build wrote: a whole-message citation encodes to
// exactly the id, so it stays usable as one, and a span adds the two offsets.
func TestACitationRecordsTheSpanAndNeverTheText(t *testing.T) {
	whole := CiteRef{Message: "01HMESSAGE"}
	if got := EncodeCiteRef(whole); got != "01HMESSAGE" {
		t.Fatalf("a whole-message citation encodes to %q, want the bare id", got)
	}
	part := CiteRef{Message: "01HMESSAGE", Start: 4, End: 11}
	if got := EncodeCiteRef(part); got != "01HMESSAGE:4:11" {
		t.Fatalf("a span citation encodes to %q, want the id and the span", got)
	}
	for _, ref := range []CiteRef{whole, part} {
		back, found := ParseCiteRef(EncodeCiteRef(ref))
		if !found || back != ref {
			t.Fatalf("%v encoded and read back as %v (found %v)", ref, back, found)
		}
	}
	if !whole.Whole() || part.Whole() {
		t.Fatalf("whole and part are not told apart: %v %v", whole.Whole(), part.Whole())
	}

	// And nothing that is not one of the two shapes is half a citation. These
	// arrive from peers and from builds that wrote the key differently, and a
	// half-read one drawn as a quotation is worse than none at all.
	for _, value := range []string{"", "01H:4", "01H:4:4", "01H:9:4", "01H:x:2", ":4:9", "01H:-1:4"} {
		if ref, found := ParseCiteRef(value); found {
			t.Fatalf("%q was read as the citation %v, and it is not one", value, ref)
		}
	}

	// It is read off meta, where the node stamps it, and a message that cites
	// nothing says so rather than coming back as a citation of "".
	if _, found := CiteOf(json.RawMessage(`{"actor_kind":"user"}`)); found {
		t.Fatal("a message that cites nothing came back with a citation")
	}
	ref, found := CiteOf(json.RawMessage(`{"cite":"01HMESSAGE:4:11","actor_kind":"user"}`))
	if !found || ref != part {
		t.Fatalf("the citation on a message read back as %v (found %v), want %v", ref, found, part)
	}
}

// The span is checked against the body it is a span of, at the door, because
// nothing stores the quote: a span that cannot derive one is a citation that
// renders as broken on every read of a row that cannot be edited.
//
// The last case is the one a byte count gets wrong on real prose. An offset
// inside a multi-byte character derives bytes that are not text - replacement
// marks under somebody's name - and it is refused rather than nudged onto the
// boundary, because nudging it quotes something other than what was selected.
func TestASpanThatIsNotInTheBodyIsNotACitation(t *testing.T) {
	body := "the café is closed"
	for _, bad := range []struct {
		what       string
		start, end int
		says       string
	}{
		{"past the end", 0, 999, "past the end"},
		{"backwards", 9, 4, "ends where it starts"},
		{"empty", 4, 4, "ends where it starts"},
		{"negative", -1, 4, "cannot be negative"},
		{"ending inside a character", 4, 8, "in half"},
		{"starting inside a character", 8, 12, "in half"},
	} {
		fault := CiteSpanFault(body, bad.start, bad.end)
		if fault == "" {
			t.Fatalf("a span %s (%d:%d) was accepted as a citation of %q",
				bad.what, bad.start, bad.end, body)
		}
		if !strings.Contains(fault, bad.says) {
			t.Fatalf("a span %s was refused as %q, which does not say %q",
				bad.what, fault, bad.says)
		}
	}
	// The span that stops on the boundary instead is a citation, and it is the
	// whole word rather than the first three bytes of it.
	if fault := CiteSpanFault(body, 4, 9); fault != "" {
		t.Fatalf("the span holding the whole word was refused: %s", fault)
	}
}

// The quote is DERIVED from the body it cites, every time it is read, which is
// the whole of why a citation cannot misquote: there is nowhere for a different
// set of words to have been stored.
func TestAQuoteIsDerivedFromTheBodyItCites(t *testing.T) {
	body := "the flange is fine but the impeller is cracked"

	whole, truncated := QuoteOf(body, CiteRef{Message: "01H"})
	if whole != body || truncated {
		t.Fatalf("a whole-message citation quoted %q (truncated %v), want the message", whole, truncated)
	}

	part, _ := QuoteOf(body, CiteRef{Message: "01H", Start: 23, End: 46})
	if part != "the impeller is cracked" {
		t.Fatalf("a span citation quoted %q, want the span it names", part)
	}

	// A span that does not fit quotes NOTHING rather than being clamped to what
	// does fit. Clamping answers by misquoting, and this is the case a peer's
	// row reaches - the merge does not check a span, for the reason it does not
	// check parents.
	if adrift, _ := QuoteOf(body, CiteRef{Message: "01H", Start: 23, End: 400}); adrift != "" {
		t.Fatalf("a span running past the body quoted %q, want nothing at all", adrift)
	}

	// And a quote long enough to be an amplification is cut, on a character
	// boundary, and says it was cut - a quotation that silently ends early is a
	// misquote of the shape this whole design refuses.
	long := strings.Repeat("é", maxCiteQuote)
	cut, wasCut := QuoteOf(long, CiteRef{Message: "01H"})
	if !wasCut {
		t.Fatalf("a %d-byte quote came back whole and unmarked", len(long))
	}
	if len(cut) > maxCiteQuote {
		t.Fatalf("the cut quote is %d bytes, past the cap of %d", len(cut), maxCiteQuote)
	}
	if strings.ContainsRune(cut, '�') || len(cut)%2 != 0 {
		t.Fatalf("the quote was cut inside a character: %q", cut[len(cut)-4:])
	}
}
