package store

import (
	"errors"
	"strings"
	"testing"
)

// A well-formed reference round-trips: String encodes the three segments in
// project/type/id order, and ParseRef reads the same triple back out.
func TestARefRoundTrips(t *testing.T) {
	want := Ref{Project: "flowy", Type: "report", ID: "01HMESSAGE0000000000000000"}
	encoded := want.String()
	if encoded != "flowy/report/01HMESSAGE0000000000000000" {
		t.Fatalf("String() = %q, want project/type/id", encoded)
	}
	got, err := ParseRef(encoded)
	if err != nil {
		t.Fatalf("ParseRef(%q) failed: %v", encoded, err)
	}
	if got != want {
		t.Fatalf("ParseRef(%q) = %+v, want %+v", encoded, got, want)
	}
}

// Anything that is not exactly three non-empty, slash-separated segments is
// refused with a message naming what was wrong, rather than half-parsed into
// a Ref with a silently empty field.
func TestParseRefRefusesMalformed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string // substring the error must contain
	}{
		{"empty string", "", "1 segment"},
		{"bare id, no separators", "01HMESSAGE", "1 segment"},
		{"only project and type", "flowy/report", "2 segment"},
		{"four segments", "flowy/report/01H/extra", "4 segment"},
		{"empty project", "/report/01H", "no project"},
		{"empty type", "flowy//01H", "no type"},
		{"empty id", "flowy/report/", "no id"},
		{"all empty", "//", "no project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := ParseRef(tc.value)
			if err == nil {
				t.Fatalf("ParseRef(%q) = %+v, want a refusal", tc.value, ref)
			}
			if !errors.Is(err, ErrBadRef) {
				t.Fatalf("ParseRef(%q) error %v does not wrap ErrBadRef", tc.value, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ParseRef(%q) error %q does not mention %q", tc.value, err.Error(), tc.want)
			}
		})
	}
}

// RefOf builds a triple from an artifact this code already holds: the
// project, type and id on the row, exactly as they are, never a guess.
func TestRefOfBuildsFromAnArtifact(t *testing.T) {
	project := "flowy"
	art := &Artifact{ID: "01HTODO00000000000000000000", Type: "memory", Kind: "todo", Project: &project}
	ref, err := RefOf(art)
	if err != nil {
		t.Fatalf("RefOf(%+v) failed: %v", art, err)
	}
	want := Ref{Project: "flowy", Type: "memory", ID: art.ID}
	if ref != want {
		t.Fatalf("RefOf(%+v) = %+v, want %+v", art, ref, want)
	}
	// And it round-trips through the canonical string, like any other Ref.
	back, err := ParseRef(ref.String())
	if err != nil || back != ref {
		t.Fatalf("RefOf's ref did not round-trip: %v (err %v)", back, err)
	}
}

// RefOf refuses rather than invents one, for every way an artifact can be
// missing a piece a Ref needs: nil, no project (personal scope), no type, no
// id. A Ref that silently stood for a route nobody can follow would be worse
// than a refusal naming which piece was missing.
func TestRefOfRefusesAnArtifactMissingAPiece(t *testing.T) {
	project := "flowy"
	for _, tc := range []struct {
		name string
		art  *Artifact
		want string
	}{
		{"nil artifact", nil, "no artifact"},
		{"no project", &Artifact{ID: "01HTODO", Type: "memory"}, "no project"},
		{"empty project", &Artifact{ID: "01HTODO", Type: "memory", Project: strPtr("")}, "no project"},
		{"no type", &Artifact{ID: "01HTODO", Project: &project}, "no type"},
		{"no id", &Artifact{Type: "memory", Project: &project}, "no id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := RefOf(tc.art)
			if err == nil {
				t.Fatalf("RefOf(%+v) = %+v, want a refusal", tc.art, ref)
			}
			if !errors.Is(err, ErrBadRef) {
				t.Fatalf("RefOf(%+v) error %v does not wrap ErrBadRef", tc.art, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RefOf(%+v) error %q does not mention %q", tc.art, err.Error(), tc.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
