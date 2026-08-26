package store

import "testing"

// TestFabricWriteRefusal asserts each rule on its own call, because they refuse
// for different reasons and a caller should learn which.
func TestFabricWriteRefusal(t *testing.T) {
	op := &Principal{UserID: "u1", Operator: true}
	seat := &Principal{UserID: "u2"}

	if why := FabricWriteRefusal(op, SkillKind, FabricVisibility); why != "" {
		t.Fatalf("the operator writing a fabric skill was refused: %s", why)
	}
	// EVERY OTHER VISIBILITY IS UNTOUCHED. This guard must not become a second
	// opinion on ordinary writes - it answers one question.
	for _, v := range []string{"", "project", "personal", "project-only"} {
		if why := FabricWriteRefusal(seat, "todo", v); why != "" {
			t.Fatalf("visibility %q was refused for an ordinary seat: %s", v, why)
		}
	}
	// AN ORDINARY SEAT MAY PUBLISH ITS OWN SKILL. This was operator-only and the
	// operator could not satisfy it - every credential on this fleet is a worker,
	// so the door was one nobody present could pass. Ownership is still enforced,
	// one layer up: the create door refuses owner_user != the caller, so a seat
	// widens its own row and never somebody else's.
	if why := FabricWriteRefusal(seat, SkillKind, FabricVisibility); why != "" {
		t.Fatalf("an ordinary seat could not publish its own skill: %s", why)
	}
	// UNAUTHENTICATED STILL CANNOT. A principal with no user is not a seat.
	if FabricWriteRefusal(nil, SkillKind, FabricVisibility) == "" {
		t.Fatal("no principal wrote a fabric row")
	}
	if FabricWriteRefusal(&Principal{}, SkillKind, FabricVisibility) == "" {
		t.Fatal("a principal with no user id wrote a fabric row")
	}
	// ONLY A SKILL. A visibility that quietly worked for todos and findings would
	// cross projects where nobody asked it to.
	// A NOTE CROSSES TOO. "memory can cross project too" - a note is a memory
	// written from a shell, and it is the same shape as a skill: something one
	// seat learned that every other seat needs, read-only.
	if why := FabricWriteRefusal(seat, NoteKind, FabricVisibility); why != "" {
		t.Fatalf("a seat could not publish its own note to the fabric: %s", why)
	}
	// AND THE LIST IS STILL CLOSED. A todo crossing projects appears on boards
	// nobody filed it to; a metric lands in dashboards that never asked for the
	// series; a merge row offers another project's branch to this drainer. Three
	// different mistakes, none of them what "global memories" meant.
	for _, k := range []string{"todo", "finding", "dashboard", "metric", "merge", ""} {
		if FabricWriteRefusal(seat, k, FabricVisibility) == "" {
			t.Fatalf("kind %q was allowed to be fabric - the fabric keeps skills and notes", k)
		}
	}
}
