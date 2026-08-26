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
	for _, k := range []string{"todo", "finding", "dashboard", "metric", ""} {
		if FabricWriteRefusal(seat, k, FabricVisibility) == "" {
			t.Fatalf("kind %q was allowed to be fabric - only a skill is kept by the fabric", k)
		}
	}
}
