package main

import (
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// defaultWorkStatus is the rule both create doors have to agree on. The test
// calls the function the handler calls, not a restatement of it - a copy of the
// rule in the test file passes forever after the handler stops using it.

func TestWorkItemCreatedWithNoStatusStartsAtTodo(t *testing.T) {
	for _, kind := range []string{"todo", "merge"} {
		if got := defaultWorkStatus("", kind, false); got != store.TodoStatus {
			t.Errorf("a %s created with no status: got %q, want %q - an empty status is not a "+
				"state any board can filter for", kind, got, store.TodoStatus)
		}
	}
}

// A note is not in a lifecycle, so it keeps whatever its author typed,
// including nothing. Defaulting it would put every memory on the board.
func TestNonWorkKindKeepsAnEmptyStatus(t *testing.T) {
	if got := defaultWorkStatus("", "note", false); got != "" {
		t.Errorf("a note created with no status: got %q, want it left alone", got)
	}
}

// A stated status is never overwritten, in either direction.
func TestAStatedStatusSurvives(t *testing.T) {
	if got := defaultWorkStatus("done", "todo", false); got != "done" {
		t.Errorf("got %q, want the status the caller stated", got)
	}
}

// And the half that stops this healing rows behind their owners' backs: an
// UPDATE that restates nothing keeps what the row already has, empty included.
// Silently promoting an empty status on an unrelated edit moves somebody
// else's work.
func TestUpdateDoesNotHealAnEmptyStatus(t *testing.T) {
	if got := defaultWorkStatus("", "todo", true); got != "" {
		t.Errorf("update with no status: got %q, want the row left as it is", got)
	}
}
