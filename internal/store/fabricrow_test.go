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
	if FabricWriteRefusal(seat, SkillKind, FabricVisibility) == "" {
		t.Fatal("a non-operator wrote a row readable from every project - that is a way to publish everywhere")
	}
	if FabricWriteRefusal(nil, SkillKind, FabricVisibility) == "" {
		t.Fatal("no principal wrote a fabric row")
	}
	// ONLY A SKILL. A visibility that quietly worked for todos and findings would
	// cross projects where nobody asked it to.
	for _, k := range []string{"todo", "finding", "dashboard", "metric", ""} {
		if FabricWriteRefusal(op, k, FabricVisibility) == "" {
			t.Fatalf("kind %q was allowed to be fabric - only a skill is kept by the fabric", k)
		}
	}
}
