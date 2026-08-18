package store

import (
	"encoding/json"
	"testing"
)

func instruction(t *testing.T, project *string, scope, seat, title string) *Artifact {
	t.Helper()

	fields := map[string]string{ScopeField: scope}
	if seat != "" {
		fields[SeatField] = seat
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return &Artifact{
		Type: InstructionType, Kind: scope, Project: project,
		OwnerUser: "01USER-OPERATOR", Title: title, Visibility: VisibilityShared,
		Fields: encoded,
	}
}

// A ROW THAT CANNOT BE READ BACK UNAMBIGUOUSLY IS REFUSED AT THE WRITE.
//
// The interpretation would otherwise live in every reader, and readers are
// where this system has repeatedly disagreed with itself.
func TestAnInstructionThatCannotBeReadBackIsRefused(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "instr")

	for _, tc := range []struct {
		what string
		art  *Artifact
	}{
		{"no scope", instruction(t, &project, "", "", "a rule")},
		{"a scope nobody defined", instruction(t, &project, "fleet", "", "a rule")},
		{"seat scope naming no seat", instruction(t, &project, ScopeSeat, "", "a rule")},
		{"project scope with no project", instruction(t, nil, ScopeProject, "", "a rule")},
		{"no title", instruction(t, &project, ScopeNode, "", "")},
	} {
		if err := db.WriteInstruction(ctx, tc.art, nil); err == nil {
			t.Errorf("%s was accepted", tc.what)
		}
	}
}

// The ordered set, and the order is the product.
func TestInstructionsComeBackWidestScopeFirst(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "instr")
	p := &Principal{UserID: "01USER-OPERATOR", AgentID: "01AGENT-ME", Project: project}
	mine, _ := voteActor(p)

	for _, art := range []*Artifact{
		instruction(t, &project, ScopeSeat, mine, "my own rule"),
		instruction(t, &project, ScopeNode, "", "the node rule"),
		instruction(t, &project, ScopeProject, "", "the project rule"),
	} {
		if err := db.WriteInstruction(ctx, art, nil); err != nil {
			t.Fatalf("write %q: %v", art.Title, err)
		}
	}

	got, err := db.InstructionsFor(ctx, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var order []string
	for _, a := range got {
		order = append(order, InstructionScopeOf(a))
	}
	want := []string{ScopeNode, ScopeProject, ScopeSeat}
	if len(order) != len(want) {
		t.Fatalf("got %d instructions %v, want %d", len(order), order, len(want))
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order is %v, want %v", order, want)
		}
	}
}

// SOMEBODY ELSE'S SEAT RULES DO NOT APPLY TO ME.
//
// They are readable - permission is a separate question - but a list that mixes
// them in is a list an agent would follow, and following another seat's rule is
// the failure this whole row exists to prevent from happening silently.
func TestAnotherSeatsRulesAreNotMine(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "instr")
	me := &Principal{UserID: "01USER-OPERATOR", AgentID: "01AGENT-ME", Project: project}
	mineActor, _ := voteActor(me)

	if err := db.WriteInstruction(ctx, instruction(t, &project, ScopeSeat, mineActor, "mine"), nil); err != nil {
		t.Fatalf("write mine: %v", err)
	}
	if err := db.WriteInstruction(ctx, instruction(t, &project, ScopeSeat, "01AGENT-SOMEBODY-ELSE", "theirs"), nil); err != nil {
		t.Fatalf("write theirs: %v", err)
	}

	got, err := db.InstructionsFor(ctx, me)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, a := range got {
		if a.Title == "theirs" {
			t.Error("another seat's rule came back in my set")
		}
	}
	// And mine did, or this test would pass with the filter refusing everything.
	found := false
	for _, a := range got {
		if a.Title == "mine" {
			found = true
		}
	}
	if !found {
		t.Error("my own seat rule is missing - the filter refuses everything")
	}
}

// AN EDIT IS A NEW ROW, and the old one stops applying while staying readable.
// That is what makes "who decided this and when" survive a change.
func TestASupersededInstructionStopsApplying(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "instr")
	p := &Principal{UserID: "01USER-OPERATOR", AgentID: "01AGENT-ME", Project: project}

	first := instruction(t, &project, ScopeNode, "", "the old rule")
	if err := db.WriteInstruction(ctx, first, nil); err != nil {
		t.Fatalf("write first: %v", err)
	}

	second := instruction(t, &project, ScopeNode, "", "the new rule")
	fields := map[string]string{ScopeField: ScopeNode, SupersedesField: first.ID}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	second.Fields = encoded
	if err := db.WriteInstruction(ctx, second, nil); err != nil {
		t.Fatalf("write second: %v", err)
	}

	got, err := db.InstructionsFor(ctx, p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	titles := map[string]bool{}
	for _, a := range got {
		titles[a.Title] = true
	}
	if titles["the old rule"] {
		t.Error("a superseded rule is still in the applying set")
	}
	if !titles["the new rule"] {
		t.Error("the replacement is not in the set")
	}
	// AND THE OLD ROW IS STILL THERE. Superseded is not deleted: the record of
	// what the rule used to be, and who decided it, is the point.
	if _, err := db.GetArtifact(ctx, first.ID); err != nil {
		t.Errorf("the superseded row is gone: %v", err)
	}
}
