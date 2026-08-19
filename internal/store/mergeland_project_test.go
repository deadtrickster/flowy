package store

import (
	"testing"
)

// A TARGET IS A NAME AND EVERY REPOSITORY'S IS CALLED MASTER.
//
// merge_lands was keyed on that name alone, so two projects shared one row: the
// second project's landing became the tip the FIRST project's rows were judged
// against, and every green verdict there would read as "the target moved after
// its gate ran". A whole queue refusing itself, correctly by its own rule, for
// a reason that is not about it.
//
// The arms are chosen so that neither passes on the broken version: two
// projects must NOT see each other's tip, and a row written before the column
// existed must still be readable, or the fix is an outage for every node that
// has been landing all week.
func TestALandingBelongsToItsProject(t *testing.T) {
	ctx, db := open(t)
	p := &Principal{UserID: "u-lander"}

	if err := db.RecordLandedTip(ctx, p, "alpha", "master", "aaa1111"); err != nil {
		t.Fatalf("alpha's landing: %v", err)
	}
	if err := db.RecordLandedTip(ctx, p, "beta", "master", "bbb2222"); err != nil {
		t.Fatalf("beta's landing: %v", err)
	}

	// NEITHER PROJECT SEES THE OTHER'S. This is the whole defect: on the broken
	// version beta's land overwrites the single row and alpha reads bbb2222.
	alpha, err := db.LandedTipOf(ctx, "alpha", "master")
	if err != nil || alpha == nil {
		t.Fatalf("alpha's tip: %+v %v", alpha, err)
	}
	if alpha.Tip != "aaa1111" {
		t.Errorf("alpha's master is %q - it is reading another project's landing, and "+
			"every gate of alpha's would now measure as stale", alpha.Tip)
	}
	beta, err := db.LandedTipOf(ctx, "beta", "master")
	if err != nil || beta == nil {
		t.Fatalf("beta's tip: %+v %v", beta, err)
	}
	if beta.Tip != "bbb2222" {
		t.Errorf("beta's master is %q", beta.Tip)
	}

	// A PROJECT THAT HAS NEVER LANDED HAS NO TIP, rather than inheriting one.
	// Inheriting would be the same defect with a friendlier face: a first gate
	// judged against somebody else's base.
	fresh, err := db.LandedTipOf(ctx, "gamma", "master")
	if err != nil {
		t.Fatalf("gamma: %v", err)
	}
	if fresh != nil {
		t.Errorf("a project that has never landed reads %q as its target tip", fresh.Tip)
	}

	// AND THE MIGRATION. A row written before the column existed carries '', and
	// a caller that names no project must still read it - a node that has been
	// landing all week would otherwise wake up with no target tip at all, which
	// is every one of its rows refused at once.
	if err := db.RecordLandedTip(ctx, p, "", "legacy-target", "ccc3333"); err != nil {
		t.Fatalf("the legacy landing: %v", err)
	}
	old, err := db.LandedTipOf(ctx, "", "legacy-target")
	if err != nil || old == nil || old.Tip != "ccc3333" {
		t.Fatalf("a landing with no project is unreadable: %+v %v", old, err)
	}
	// A NAMED PROJECT FALLS BACK TO IT, which is what carries a running node
	// across the migration: its rows name a project, its landings do not, and
	// the answer has to keep being the tip it was yesterday.
	inherited, err := db.LandedTipOf(ctx, "flowy", "legacy-target")
	if err != nil || inherited == nil {
		t.Fatalf("the legacy row did not answer a named project: %+v %v", inherited, err)
	}
	if inherited.Tip != "ccc3333" {
		t.Errorf("the fallback read %q, so a node mid-migration loses its target tip", inherited.Tip)
	}
}
