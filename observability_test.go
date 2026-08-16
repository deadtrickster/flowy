package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// A trace id is the node's to stamp. A client that could write one into meta
// could join its own events to somebody else's trace - or scatter a trace
// across events that had nothing to do with it - and the far side of a handoff
// reads that key to decide which trace to continue.
func TestClientMetaCannotCarryATrace(t *testing.T) {
	const mine = "aabbccddeeff00112233445566778899"
	stripped := speakerStripped(json.RawMessage(
		`{"trace":"` + mine + `","actor_kind":"agent","actor_user":"somebody","topic":"kept"}`))

	var fields map[string]any
	if err := json.Unmarshal(stripped, &fields); err != nil {
		t.Fatalf("the stripped meta does not parse: %v", err)
	}
	if _, found := fields[store.TraceMetaKey]; found {
		t.Fatalf("a client's trace id survived: %s", stripped)
	}
	if _, found := fields["actor_user"]; found {
		t.Fatalf("a client's speaker survived: %s", stripped)
	}
	if fields["topic"] != "kept" {
		t.Fatalf("stripping took what meta is for with it: %s", stripped)
	}
}

// And what the node stamps is readable back off the row, which is the whole
// mechanism by which a handoff crosses the node boundary in one trace.
func TestWithTraceStampsWhatTheNodeDecided(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	meta := withTrace(json.RawMessage(`{"actor_kind":"user"}`), id)
	if got := store.TraceOfMeta(meta); got != id {
		t.Fatalf("the stamped trace read back as %q, want %q", got, id)
	}

	// Nothing is stamped when there is no trace: an event written outside one
	// must not carry a key that says it was part of something.
	plain := withTrace(json.RawMessage(`{"actor_kind":"user"}`), "")
	if got := store.TraceOfMeta(plain); got != "" {
		t.Fatalf("a write outside a trace was stamped %q", got)
	}
	if got := store.TraceOfMeta(withTrace(nil, "not-a-trace")); got != "" {
		t.Fatal("a malformed trace id was stamped onto a row")
	}
}

// A span's name is a route, not a URL: a thousand requests for a thousand
// artifacts have to be one line in a trace view rather than a thousand.
func TestRouteNameDropsIDs(t *testing.T) {
	for path, want := range map[string]string{
		"/api/artifacts": "/api/artifacts",
		"/api/artifact/01M032P7JN0NECCR3BGJEY3WFG":   "/api/artifact/{id}",
		"/api/task/01M032P7JN0NECCR3BGJEY3WFG/state": "/api/task/{id}/state",
		"/api/chat/general/say":                      "/api/chat/general/say",
		"/api/metrics":                               "/api/metrics",
	} {
		got := routeName(httptest.NewRequest("GET", path, nil))
		if got != want {
			t.Errorf("routeName(%q) = %q, want %q", path, got, want)
		}
	}
}

// The timeline shows four kinds by name, and everything else as activity -
// visible rather than hidden, because a timeline that quietly omits rows lies
// about what happened.
func TestTimelineKinds(t *testing.T) {
	for eventType, want := range map[string]string{
		store.ChatEventType: activityChat,
		turnEventType:       activityTurn,
		logEventType:        activityLog,
		steerEventType:      activitySteer,
		worklogEventType:    activityWorklog,
		taskEventType:       activityOther,
		statusEventType:     activityOther,
		"memory.write":      activityOther,
	} {
		if got := kindOfEvent(&store.Event{Type: eventType}); got != want {
			t.Errorf("an event of type %q shows as %q, want %q", eventType, got, want)
		}
	}
	// And what a client may post is the four, never a type the node mints by
	// doing the thing.
	for _, minted := range []string{statusEventType, taskEventType, forgeEventType} {
		for _, mapped := range postableKinds {
			if mapped == minted {
				t.Errorf("the timeline lets a client post %q, which the node mints", minted)
			}
		}
	}
	// The worklog is readable here and deliberately not postable. Its entries
	// are the one kind on this timeline whose write validates arguments no event
	// column carries - the artifact ids in refs, against the writer's own read
	// filter - so a generic POST /api/activity that accepted the kind would be a
	// second door onto the stream with none of the checks on the first.
	if postableKinds[activityWorklog] != "" {
		t.Error("a client may post a worklog entry over /api/activity, bypassing worklog_append")
	}
	if readableKinds[activityWorklog] != worklogEventType {
		t.Error("the timeline cannot be narrowed to the worklog, so the kind is unreachable")
	}
	for kind, eventType := range postableKinds {
		if readableKinds[kind] != eventType {
			t.Errorf("kind %q may be posted and not read back", kind)
		}
	}
}

// The scope key is what a series of readings is recorded under. Two principals
// see two different corpora, so their histories must not be averaged together -
// and the operator's node-wide view is a third series again.
func TestScopeKeysDoNotCollide(t *testing.T) {
	a := &store.Principal{UserID: "ua", Project: "pa"}
	b := &store.Principal{UserID: "ub", Project: "pb"}
	operator := &store.Principal{UserID: "uop", Project: "pa", Operator: true}

	if scopeKey(a, false) == scopeKey(b, false) {
		t.Fatal("two principals share one history")
	}
	if scopeKey(operator, true) == scopeKey(operator, false) {
		t.Fatal("the node-wide view shares a history with the operator's own")
	}
	// scope=all is the operator's: for anybody else it is their own key.
	if got := scopeKey(a, true); got != scopeKey(a, false) {
		t.Fatalf("scope=all changed a non-operator's history to %q", got)
	}
}
