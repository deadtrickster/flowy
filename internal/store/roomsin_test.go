package store

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// A ROOM LIST IS ABOUT A PROJECT, and until this it was always about the
// caller's own.
//
// MEASURED 2026-08-20: /projects landed saying on its own face that it could
// not show what was inside anything it listed, because GET /api/rooms took no
// project. A person with three projects on this node could read three names and
// not one thing in any of them.
//
// The permission is NOT here - it is ReadableProjects, at the door, so there is
// one implementation of "may this token read that project". What is here is
// that the QUERY follows the project it was given rather than the principal's.
func TestRoomsInAnswersAboutTheProjectItWasAsked(t *testing.T) {
	ctx, db := open(t)
	u := presenceUser(t, ctx, db, "roomsin")

	mine := "roomsin-a-" + ulid.NewString()[:6]
	other := "roomsin-b-" + ulid.NewString()[:6]
	for _, id := range []string{mine, other} {
		if err := db.DeclareProject(ctx, &Project{ID: id, Name: id, CreatedBy: u.ID}); err != nil {
			t.Fatalf("declare %s: %v", id, err)
		}
	}

	p := &Principal{UserID: u.ID, Project: mine}
	if _, err := db.CreateRoom(ctx, p, "here", "a room in my own project"); err != nil {
		t.Fatalf("declare room here: %v", err)
	}
	elsewhere := &Principal{UserID: u.ID, Project: other}
	if _, err := db.CreateRoom(ctx, elsewhere, "there", "a room in the other project"); err != nil {
		t.Fatalf("declare room there: %v", err)
	}

	// The default is unchanged: no project named, the caller's own.
	own, err := db.RoomsFor(ctx, p)
	if err != nil {
		t.Fatalf("rooms for: %v", err)
	}
	if !hasRoom(own, "here") {
		t.Errorf("the caller's own project does not list its own room: %v", names(own))
	}
	if hasRoom(own, "there") {
		t.Errorf("the caller's own project lists another project's room: %v", names(own))
	}

	// AND ASKED ABOUT THE OTHER ONE, IT ANSWERS ABOUT THE OTHER ONE. Same
	// principal, different project: this is the whole change.
	theirs, err := db.RoomsIn(ctx, p, other)
	if err != nil {
		t.Fatalf("rooms in %s: %v", other, err)
	}
	if !hasRoom(theirs, "there") {
		t.Errorf("asked about %s and did not get its room: %v", other, names(theirs))
	}
	if hasRoom(theirs, "here") {
		t.Errorf("asked about %s and got the caller's own room: %v", other, names(theirs))
	}

	// A project with nothing in it answers an empty list rather than an error -
	// the difference between "nothing here" and "you cannot look" is the
	// door's to make, and it makes it by refusing before it ever gets here.
	empty := "roomsin-c-" + ulid.NewString()[:6]
	if err := db.DeclareProject(ctx, &Project{ID: empty, Name: empty, CreatedBy: u.ID}); err != nil {
		t.Fatalf("declare %s: %v", empty, err)
	}
	got, err := db.RoomsIn(ctx, p, empty)
	if err != nil {
		t.Fatalf("rooms in an empty project: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a project with no rooms and no traffic answered %v", names(got))
	}
}

func hasRoom(rooms []Room, name string) bool {
	for _, r := range rooms {
		if r.Name == name {
			return true
		}
	}
	return false
}

func names(rooms []Room) []string {
	out := []string{}
	for _, r := range rooms {
		out = append(out, r.Name)
	}
	return out
}
