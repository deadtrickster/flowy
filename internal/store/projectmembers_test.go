package store

import (
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// WHERE A PERSON WORKS, which until 2026-08-20 was nowhere.
//
// MEASURED: a cookie session resolved to a principal with no project at all -
// token_projects is an AGENT's reach and there was no project_members - so a
// logged-in person's writes had no home and "switch projects" had nothing to
// switch. The operator's words: "i as a user dont care about per project tokens
// - i want a human thing - my own projects without logging out/in."
func TestAPersonBelongsToProjectsAndASessionHoldsOne(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "member")
	one := "member-a-" + ulid.NewString()[:6]
	two := "member-b-" + ulid.NewString()[:6]
	for _, id := range []string{one, two} {
		if err := db.DeclareProject(ctx, &Project{ID: id, Name: id, CreatedBy: u.ID}); err != nil {
			t.Fatalf("declare %s: %v", id, err)
		}
	}

	// BELONGING TO NOTHING IS THE STARTING STATE and it answers an empty list,
	// not an error: a person with no membership has nowhere to write, and that
	// has to be sayable rather than a failure.
	mine, err := db.ProjectsOfUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("projects of user: %v", err)
	}
	if len(mine) != 0 {
		t.Fatalf("a new person already belongs to %v", mine)
	}

	if err := db.JoinProject(ctx, u.ID, one, ""); err != nil {
		t.Fatalf("join: %v", err)
	}
	// Twice is not an error: "make sure they are in it" is what an operator
	// means, and a second call that failed would make the safe thing to do the
	// thing you avoid.
	if err := db.JoinProject(ctx, u.ID, one, ""); err != nil {
		t.Fatalf("join twice: %v", err)
	}
	if mine, err = db.ProjectsOfUser(ctx, u.ID); err != nil || len(mine) != 1 || mine[0] != one {
		t.Fatalf("after joining %s the memberships are %v (%v)", one, mine, err)
	}

	session, err := db.StartSession(ctx, u.ID, "test")
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	// A SESSION THAT HAS NOT CHOSEN ANSWERS NOTHING, which is a real state and
	// not a blank: it reads as "you are in no project" rather than as an empty
	// name that something might write with.
	where, err := db.SessionProject(ctx, session.ID)
	if err != nil || where != "" {
		t.Fatalf("a fresh session is already in %q (%v)", where, err)
	}

	if err := db.EnterProject(ctx, session.ID, u.ID, one); err != nil {
		t.Fatalf("enter %s: %v", one, err)
	}
	if where, err = db.SessionProject(ctx, session.ID); err != nil || where != one {
		t.Fatalf("after entering %s the session says %q (%v)", one, where, err)
	}

	// AND YOU CANNOT WORK WHERE YOU DO NOT BELONG. The refusal names the
	// project and says which fact decided, because "you are not a member of X"
	// and "there is no project called X" send somebody to two different people.
	err = db.EnterProject(ctx, session.ID, u.ID, two)
	if err == nil {
		t.Fatalf("entered %s without belonging to it", two)
	}
	if !strings.Contains(err.Error(), two) || !strings.Contains(err.Error(), "not a member") {
		t.Errorf("the refusal does not say which fact decided: %v", err)
	}
	// And the session is left where it was rather than nowhere: a refused move
	// is not a move.
	if where, err = db.SessionProject(ctx, session.ID); err != nil || where != one {
		t.Errorf("a refused move left the session in %q (%v)", where, err)
	}

	// A project that does not exist is a different sentence again.
	err = db.EnterProject(ctx, session.ID, u.ID, "no-such-project-"+ulid.NewString()[:6])
	if err == nil || !strings.Contains(err.Error(), "no project called") {
		t.Errorf("entering a project that does not exist said: %v", err)
	}

	// LEAVING IS A REAL ACT. A person who has left every project must not be
	// silently left writing into the last one they were in.
	if err := db.EnterProject(ctx, session.ID, u.ID, ""); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if where, err = db.SessionProject(ctx, session.ID); err != nil || where != "" {
		t.Errorf("after leaving, the session says %q (%v)", where, err)
	}
}

// OWNERSHIP IS NOT MEMBERSHIP. The operator: "normal ownership and
// collaboration - i will invite other humans to projects." Working somewhere and
// deciding who else works there are different powers, and collapsing them is how
// a project becomes open to everybody who was ever added to it.
func TestOnlyAnOwnerInvites(t *testing.T) {
	ctx, db := open(t)
	owner := presenceUser(t, ctx, db, "owner")
	worker := presenceUser(t, ctx, db, "worker")
	project := "owned-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: project, Name: project, CreatedBy: owner.ID}); err != nil {
		t.Fatalf("declare: %v", err)
	}

	// The person who declared it owns it, without being a member of it yet -
	// a project can be declared before anybody is in it, and an owner who
	// cannot invite is an owner in name.
	may, err := db.MayInvite(ctx, owner.ID, project)
	if err != nil || !may {
		t.Fatalf("the declarer cannot invite into their own project (%v)", err)
	}

	if err := db.JoinProject(ctx, worker.ID, project, "member"); err != nil {
		t.Fatalf("join: %v", err)
	}
	may, err = db.MayInvite(ctx, worker.ID, project)
	if err != nil {
		t.Fatalf("may invite: %v", err)
	}
	if may {
		t.Error("an ordinary member can decide who else works in the project")
	}

	// Made an owner, they can.
	if err := db.JoinProject(ctx, worker.ID, project, "owner"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if may, err = db.MayInvite(ctx, worker.ID, project); err != nil || !may {
		t.Errorf("a promoted owner still cannot invite (%v)", err)
	}
}
