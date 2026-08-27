package flowy

import "testing"

// ?assignee= has three answers and the dangerous one is the middle.
//
// NormalizeAssignee turns every nobody-word into the empty string, and an empty
// Assignee on the query means "do not filter". So a door that normalised first
// would answer ?assignee=none with THE WHOLE QUEUE - an answer that is wrong and
// looks exactly like a right one, which is the failure this codebase has now
// made in three other shapes.
//
// No database: the decision is entirely in the reading of the parameter, and a
// test that needed a cluster to check it would be a test nobody runs.
func TestAssigneeAsksForAName_ANobody_OrNothing(t *testing.T) {
	name, unassigned, err := assigneeArg("orchestrator")
	if err != nil || name != "orchestrator" || unassigned {
		t.Errorf("a handle read as (%q, %v, %v)", name, unassigned, err)
	}

	// Every word this queue has used for the state, because two words for one
	// state read as two states - see store.NobodyName.
	for _, word := range []string{"none", "nobody", "unassigned", "unowned", "n/a", "-", "?", "TBD"} {
		name, unassigned, err := assigneeArg(word)
		if err != nil {
			t.Errorf("%q was refused: %v", word, err)
			continue
		}
		if !unassigned {
			t.Errorf("%q did not ask for the unowned rows - it would answer with the whole queue", word)
		}
		if name != "" {
			t.Errorf("%q also asked for the name %q", word, name)
		}
	}

	// Absent is not a filter, and that is the only case where an empty name and
	// no unassigned flag is right.
	name, unassigned, err = assigneeArg("   ")
	if err != nil || name != "" || unassigned {
		t.Errorf("an absent parameter read as (%q, %v, %v)", name, unassigned, err)
	}

	if _, _, err := assigneeArg("two\nlines"); err == nil {
		t.Error("a name that is not a name was accepted")
	}
}
