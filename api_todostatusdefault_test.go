package main

import (
	"encoding/json"
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

// The room half of the same rule. This door left status empty and six rows
// became unfindable; it left room empty and 22 rows belonged to no room at all,
// which is worse - a status filter merely excludes them, a room filter cannot
// include them under any value.
func TestWorkItemCreatedWithNoRoomLandsSomewhereBrowsable(t *testing.T) {
	art := &store.Artifact{Kind: "todo"}
	defaultWorkRoom(art, false)
	if got := store.RoomOf(art); got != store.DefaultRoom {
		t.Errorf("a todo created with no room: got %q, want %q", got, store.DefaultRoom)
	}
}

// A stated room is never overwritten, and an update never heals one - the same
// two guards the status default carries, for the same reason: silently moving
// somebody's row is worse than leaving it where they put it.
func TestAStatedRoomSurvivesAndUpdatesDoNotHeal(t *testing.T) {
	stated := &store.Artifact{Kind: "todo"}
	setField(t, stated, store.RoomField, "handoffs")
	defaultWorkRoom(stated, false)
	if got := store.RoomOf(stated); got != "handoffs" {
		t.Errorf("stated room became %q", got)
	}

	onUpdate := &store.Artifact{Kind: "todo"}
	defaultWorkRoom(onUpdate, true)
	if got := store.RoomOf(onUpdate); got != "" {
		t.Errorf("an update with no room set it to %q - it should leave the row alone", got)
	}
}

// A merge request is not raised in a room and must not appear in one. Without
// this the queue's rows would start showing up in the general todo panel.
func TestAMergeRequestGetsNoRoom(t *testing.T) {
	art := &store.Artifact{Kind: store.MergeKind}
	defaultWorkRoom(art, false)
	if got := store.RoomOf(art); got != "" {
		t.Errorf("a merge request was put in room %q", got)
	}
}

func setField(t *testing.T, a *store.Artifact, key, value string) {
	t.Helper()
	fields, err := store.ArtifactFields(a)
	if err != nil {
		t.Fatal(err)
	}
	fields[key] = value
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	a.Fields = raw
}
