package store

import (
	"context"
	"testing"
)

// The rule, as a table over the one predicate that decides it: a citer may pass
// a resource on exactly when their own access is a decision rather than a view.
func TestOnlyAPrincipalWhoMayDecideAboutTheRowMayPassItOn(t *testing.T) {
	project := "flowy"
	owner := &Principal{UserID: "01USER-OWNER", Project: project}
	member := &Principal{UserID: "01USER-MEMBER", Project: project}

	personal := &Artifact{ID: "01ART-PERSONAL", Visibility: VisibilityPersonal, OwnerUser: owner.UserID}
	projectRow := &Artifact{ID: "01ART-PROJECT", Visibility: VisibilityShared, OwnerUser: owner.UserID, Project: &project}

	cases := []struct {
		name string
		p    *Principal
		art  *Artifact
		want bool
		why  string
	}{
		{"the owner of a personal row", owner, personal, true,
			"the owner is the only principal who reads it at all, so citing it is the decision to share it"},
		{"somebody else, on a personal row", member, personal, false,
			"a reader who was shown it may not show it on - read is permission to know"},
		{"the owner, on a project row", owner, projectRow, false,
			"every member already reads it, and sharing it outward is a project decision rather than a quotation"},
		{"a principal with no user", &Principal{Project: project}, personal, false,
			"a token that resolves to no user cannot own the decision"},
		{"no principal at all", nil, personal, false, "nothing to decide with"},
		{"no artifact", owner, nil, false, "nothing to decide about"},
	}
	for _, c := range cases {
		if got := citerMayGrant(c.p, c.art); got != c.want {
			t.Errorf("%s: may grant = %v, want %v - %s", c.name, got, c.want, c.why)
		}
	}
}

// The half of the rule that is easy to lose in a refactor: citing something to
// yourself is not a decision about anybody, and must not write a capability row
// that a later reader would read as a share.
func TestCitingToYourselfGrantsNothing(t *testing.T) {
	me := &Principal{UserID: "01USER-ME", Project: "flowy"}
	granted, err := (&DB{}).GrantCitedArtifact(context.Background(), me, "01ART", me.UserID)
	if err != nil {
		t.Fatalf("citing to yourself returned an error: %v", err)
	}
	if granted {
		t.Error("citing to yourself wrote a grant")
	}
}

// And the arguments that are not there: no citer, no artifact, no subject. Each
// returns false without touching the database, which is what lets the caller
// run this on every message without a nil check of its own.
func TestGrantCitedArtifactIgnoresTheEmptyCases(t *testing.T) {
	d := &DB{}
	me := &Principal{UserID: "01USER-ME"}
	cases := []struct {
		name     string
		p        *Principal
		artifact string
		subject  string
	}{
		{"no citer", nil, "01ART", "01USER-THEM"},
		{"no artifact", me, "", "01USER-THEM"},
		{"no subject", me, "01ART", ""},
	}
	for _, c := range cases {
		granted, err := d.GrantCitedArtifact(context.Background(), c.p, c.artifact, c.subject)
		if err != nil || granted {
			t.Errorf("%s: granted=%v err=%v, want false and no error", c.name, granted, err)
		}
	}
}
