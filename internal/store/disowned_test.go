package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestTheMarkIsPutOnTheSubjectsRowsAndNobodyElsesIsTheNEGATIVECONTROL the row
// asked for before anybody picked it up.
//
// A window is a range of CLOCK READINGS and every principal writes into it at
// once. So a reader that matched on the window and forgot the subject would
// disown the whole fabric for that period - every row by everybody, on the word
// of one person about their own key. That is a worse failure than not marking
// at all, because it is confident.
//
// Pure: no database. The fill takes rows and a repudiation list and decides;
// which rows a reader may see is the permission filter's question and is asked
// before this.
func TestTheMarkIsPutOnTheSubjectsRowsAndNobodyElses(t *testing.T) {
	reps := []*Artifact{repudiation(t, "u-alice", SpeakerSubject, 100, 200)}

	hers := &Artifact{ID: ulid.NewString(), OwnerUser: "u-alice", HLC: 150}
	his := &Artifact{ID: ulid.NewString(), OwnerUser: "u-bob", HLC: 150}
	before := &Artifact{ID: ulid.NewString(), OwnerUser: "u-alice", HLC: 99}
	after := &Artifact{ID: ulid.NewString(), OwnerUser: "u-alice", HLC: 201}

	for _, a := range []*Artifact{hers, his, before, after} {
		a.Disowned = disownedBy(reps, a.OwnerUser, a.HLC)
	}

	if hers.Disowned == nil {
		t.Fatal("her row inside her own window is not marked")
	}
	if hers.Disowned.Subject != "u-alice" || hers.Disowned.By == "" {
		t.Errorf("the mark does not name who disowned it or which row says so: %+v", hers.Disowned)
	}
	if hers.Disowned.From != 100 || hers.Disowned.To != 200 {
		t.Errorf("the mark carries window %d-%d, want 100-200 - a reader cannot tell "+
			"an edge from the middle without it", hers.Disowned.From, hers.Disowned.To)
	}

	// THE CONTROL. bob wrote at the same reading and alice's repudiation says
	// nothing about him.
	if his.Disowned != nil {
		t.Fatalf("another principal's row in the same clock window was disowned: %+v",
			his.Disowned)
	}
	// And the edges, from the other side: the window is closed, so a row one
	// reading outside it is untouched.
	if before.Disowned != nil || after.Disowned != nil {
		t.Errorf("a row outside the window was marked: before=%+v after=%+v",
			before.Disowned, after.Disowned)
	}
}

// TestTheMarkDoesNotReplaceTheAuthorshipItQualifies is the other half of the
// ruling: authorship records whether a signature verified HERE, which stays
// true of a stolen key. The honest sentence is "authored, and its author
// disowns it", so a reader must still be able to see both halves.
func TestTheMarkDoesNotReplaceTheAuthorshipItQualifies(t *testing.T) {
	reps := []*Artifact{repudiation(t, "u-alice", SpeakerSubject, 100, 200)}
	row := &Artifact{
		ID: ulid.NewString(), OwnerUser: "u-alice", HLC: 150,
		Authorship: AuthorshipAuthored,
	}
	row.Disowned = disownedBy(reps, row.OwnerUser, row.HLC)

	if row.Disowned == nil {
		t.Fatal("the row is not marked")
	}
	if row.Authorship != AuthorshipAuthored {
		t.Errorf("authorship reads %q after the mark - the signature still verified, "+
			"and a reader that lost that cannot tell a stolen key from a forgery",
			row.Authorship)
	}
}
