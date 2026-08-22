package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// The transition door, at the wire: what POST /api/openspec/{id}/transition
// moves, what it refuses, and - the arms the door-only setter stands on - that
// the generic write doors cannot move a state around it.

// openspecStateOf reads a row's lifecycle state the way the store reads it,
// so the assertion is about the stored row and not a copy of the field.
func openspecStateOf(t *testing.T, ctx context.Context, s *server,
	p *store.Principal, id string,
) store.OpenspecState {
	t.Helper()
	art, err := s.db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("read back %s: %v", id, err)
	}
	state, err := store.OpenspecStateOf(art)
	if err != nil {
		t.Fatalf("state of %s: %v", id, err)
	}
	return state
}

// openspecTransitionEvents is how many openspec.transition entries the log
// holds for the row - the audit trail, which a forged state must not extend.
func openspecTransitionEvents(t *testing.T, ctx context.Context, s *server, id string) int {
	t.Helper()
	events, err := s.db.ArtifactEvents(ctx, id, openspecTransitionEventType)
	if err != nil {
		t.Fatalf("transition events of %s: %v", id, err)
	}
	return len(events)
}

func TestOpenspecTransitionMovesAChange(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{"proposal.md": "# why\n"}))
	if code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("the filed change carries no id: %v", body)
	}

	// A fresh change reads as proposed - the start of the line, by absence.
	if state := openspecStateOf(t, ctx, s, p, id); state != store.OpenspecProposed {
		t.Fatalf("a fresh change reads %q, want proposed", state)
	}

	code, body = openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{"to":"in-progress"}`)
	if code != http.StatusOK {
		t.Fatalf("the move proposed -> in-progress was answered %d: %v", code, body)
	}
	if state := openspecStateOf(t, ctx, s, p, id); state != store.OpenspecInProgress {
		t.Fatalf("the moved change reads %q, want in-progress", state)
	}
	ev, _ := body["event"].(map[string]any)
	if got, _ := ev["type"].(string); got != openspecTransitionEventType {
		t.Fatalf("the move's entry is type %q, want %q", got, openspecTransitionEventType)
	}
	if got, _ := ev["body"].(string); got != "proposed->in-progress" {
		t.Fatalf("the move's entry reads %q, want proposed->in-progress", got)
	}
	if n := openspecTransitionEvents(t, ctx, s, id); n != 1 {
		t.Fatalf("%d transition entries, want exactly the one that records the move", n)
	}
}

// The move and its entry are one fact: the entry in the answer carries the
// row's clock reading, the way a status move's does.
func TestOpenspecTransitionEventSharesTheRowsReading(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{"proposal.md": "# why\n"}))
	if code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)

	code, body = openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{"to":"in-progress"}`)
	if code != http.StatusOK {
		t.Fatalf("the move was answered %d: %v", code, body)
	}
	art, _ := body["artifact"].(map[string]any)
	ev, _ := body["event"].(map[string]any)
	if art["hlc"] != ev["seq_hlc"] {
		t.Fatalf("the row reads %v and its entry %v - a state and its trail must be one reading",
			art["hlc"], ev["seq_hlc"])
	}
}

func TestOpenspecTransitionRefusesASpec(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"kind":"spec","title":"the-capability","body":"# the-capability\n"}`)
	if code != http.StatusOK {
		t.Fatalf("filing the spec was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)

	code, body = openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{"to":"archived"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("moving a spec was answered %d, want 400: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "has no openspec lifecycle") {
		t.Fatalf("the refusal does not say what a spec lacks: %q", why)
	}
}

func TestOpenspecTransitionRefusesAnUnknownChange(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", "01M0000000000000000000000000", `{"to":"in-progress"}`)
	if code != http.StatusNotFound {
		t.Fatalf("moving a change that is not there was answered %d: %v", code, body)
	}
}

// The line has no backward edges and no skips, and the refusal is the store's
// own sentence - the door relays the rule, it does not own it.
func TestOpenspecTransitionRefusesOffLineMoves(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{"proposal.md": "# why\n"}))
	if code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)

	code, body = openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{"to":"complete"}`)
	if code != http.StatusConflict {
		t.Fatalf("skipping to complete was answered %d, want 409: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "from proposed the lifecycle allows in-progress") {
		t.Fatalf("the refusal is not the store's sentence: %q", why)
	}

	code, body = openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{"to":"banana"}`)
	if code != http.StatusConflict {
		t.Fatalf("a state off the line was answered %d, want 409: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "from proposed the lifecycle allows in-progress") {
		t.Fatalf("the refusal is not the store's sentence: %q", why)
	}
}

func TestOpenspecTransitionRequiresATarget(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{"proposal.md": "# why\n"}))
	if code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)

	code, body = openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("a move with no target was answered %d, want 400: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "to is required") {
		t.Fatalf("the refusal does not say what is missing: %q", why)
	}
}

// THE ARM: the generic artifact door can rewrite a change's fields, and a
// caller can put any state they like in that blob. The stored row keeps the
// state the lifecycle holds - the write applies, the forged state does not,
// and the trail gains nothing. This is the hole the update branch used to
// be, closed at the store funnel.
func TestGenericArtifactWritePreservesState(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{
			"proposal.md": "# why\n",
			"tasks.md":    "- [ ] do it\n",
		}))
	if code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)

	code, body = openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{"to":"in-progress"}`)
	if code != http.StatusOK {
		t.Fatalf("the move was answered %d: %v", code, body)
	}

	// The update rewrites content and forges a state in the same blob.
	code, body = openspecCall(t, ctx, p, s.handleCreateArtifact, "POST", "/api/artifacts",
		`{"id":"`+id+`","type":"memory","kind":"change","title":"x",`+
			`"fields":{"openspec":{"state":"archived",`+
			`"files":{"proposal.md":"rewritten\n","tasks.md":"- [ ] do it\n"}}}}`)
	if code != http.StatusOK {
		t.Fatalf("the update was answered %d: %v", code, body)
	}
	stored, err := s.db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	files, err := store.OpenspecFilesOf(stored)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if files["proposal.md"] != "rewritten\n" {
		t.Fatalf("the update did not apply its content - files %v", files)
	}
	if state := openspecStateOf(t, ctx, s, p, id); state != store.OpenspecInProgress {
		t.Fatalf("the update moved the state to %q - the caller's blob is not the lifecycle", state)
	}
	if n := openspecTransitionEvents(t, ctx, s, id); n != 1 {
		t.Fatalf("%d transition entries after a forged state, want still 1", n)
	}
}

// The same arm through the openspec door, which shares the write path.
func TestOpenspecDoorUpdatePreservesState(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{"proposal.md": "# why\n"}))
	if code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)

	code, body = openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{"to":"in-progress"}`)
	if code != http.StatusOK {
		t.Fatalf("the move was answered %d: %v", code, body)
	}

	code, body = openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"id":"`+id+`","kind":"change","fields":{"openspec":`+
			`{"state":"complete","files":{"proposal.md":"# why\n"}}}}`)
	if code != http.StatusOK {
		t.Fatalf("the update was answered %d: %v", code, body)
	}
	if state := openspecStateOf(t, ctx, s, p, id); state != store.OpenspecInProgress {
		t.Fatalf("the update moved the state to %q - the caller's blob is not the lifecycle", state)
	}
	if n := openspecTransitionEvents(t, ctx, s, id); n != 1 {
		t.Fatalf("%d transition entries after a forged state, want still 1", n)
	}
}

// The status door is the door a caller reaches for to move a lifecycle word,
// and a change is not in its vocabulary: it refuses, and the refusal is the
// whole of what it may do - the state stays.
func TestStatusDoorRefusesAChangeAndLeavesState(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{"proposal.md": "# why\n"}))
	if code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)

	code, body = openspecPathCall(t, ctx, p, s.handleArtifactStatus, "POST",
		"/api/artifact/{id}/status", id, `{"status":"done"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("the status door answered %d, want 400 - a change is not in its vocabulary", code)
	}
	if why := errorOf(body); !strings.Contains(why, "has no lifecycle") {
		t.Fatalf("the refusal does not say why: %q", why)
	}
	if state := openspecStateOf(t, ctx, s, p, id); state != store.OpenspecProposed {
		t.Fatalf("the refused status move left the state %q", state)
	}
	if n := openspecTransitionEvents(t, ctx, s, id); n != 0 {
		t.Fatalf("%d transition entries after a refused status move, want none", n)
	}
}

// The trail cannot be forged: the transition event type is minted, so POST
// /api/events refuses a hand-written entry and a sync push refuses it at the
// far end - the same two faces as a proposal vote (mcp_proposals_test.go).
func TestOpenspecTransitionEventsAreMinted(t *testing.T) {
	if openspecTransitionEventType != store.EventOpenspecTransition {
		t.Errorf("the door writes %q and the store mints %q - one word, two spellings",
			openspecTransitionEventType, store.EventOpenspecTransition)
	}
	if !mintedTypes[openspecTransitionEventType] {
		t.Error("POST /api/events would accept an openspec.transition written by hand")
	}
	if !store.MintedEventType(openspecTransitionEventType) {
		t.Error("a pushed openspec.transition would be taken from a peer")
	}

	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleAppendEvent, "POST", "/api/events",
		`{"type":"openspec.transition","artifact":"01M0000000000000000000000000",`+
			`"body":"proposed->in-progress"}`)
	if code != http.StatusForbidden {
		t.Fatalf("a hand-written transition entry was answered %d, want 403: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "written by the endpoint that does the thing") {
		t.Fatalf("the refusal does not say the type is minted: %q", why)
	}
}
