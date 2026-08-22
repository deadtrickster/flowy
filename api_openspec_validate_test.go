package main

// The validate door, at the wire: what POST /api/openspec/{id}/validate
// caches on the row, what it refuses, and that the complete arm refuses the
// cached verdict with the checks' own words - the same sentence the caller
// got from the door.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// cleanValidateFiles is a change that validates with no derived todos: the
// tasks file holds prose rather than checkbox lines, so the tasks arm of
// complete passes vacuously and the wire tests exercise the verdict alone.
func cleanValidateFiles() map[string]string {
	return map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "# tasks\n",
		"specs/session/spec.md": "## ADDED Requirements\n\n" +
			"### Requirement: Sessions remember\n" +
			"A session SHALL remember its reader.\n\n" +
			"#### Scenario: remembered\n" +
			"- **WHEN** a reader returns\n- **THEN** the session is found\n",
	}
}

// fileSpec files the capability row a clean change's delta names, the way a
// caller would.
func fileSpec(t *testing.T, ctx context.Context, s *server, p *store.Principal, title string) {
	t.Helper()
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"kind":"spec","title":"`+title+`","body":"# `+title+`\n"}`)
	if code != http.StatusOK {
		t.Fatalf("filing the spec %s was answered %d: %v", title, code, body)
	}
}

// fileChange files a change carrying exactly the files named and hands back
// its id.
func fileChange(t *testing.T, ctx context.Context, s *server, p *store.Principal, files map[string]string) string {
	t.Helper()
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(files))
	if code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("the filed change carries no id: %v", body)
	}
	return id
}

// updateChange restates an existing change with exactly the files named -
// the create door's update half, which the edit arms drive.
func updateChange(id string, files map[string]string) string {
	raw, err := json.Marshal(map[string]any{
		"id":   id,
		"kind": store.ChangeKind,
		"fields": map[string]any{
			"openspec": map[string]any{"files": files},
		},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// validateChange runs the validate door and hands back the status and body.
func validateChange(t *testing.T, ctx context.Context, s *server, p *store.Principal, id string) (int, map[string]any) {
	t.Helper()
	return openspecPathCall(t, ctx, p, s.handleOpenspecValidate, "POST",
		"/api/openspec/{id}/validate", id, "")
}

// cachedVerdictOf reads the verdict the door cached on the row, the way the
// complete arm reads it.
func cachedVerdictOf(t *testing.T, ctx context.Context, s *server, p *store.Principal, id string) *store.OpenspecValidation {
	t.Helper()
	art, err := s.db.ReadArtifact(ctx, p, id, false)
	if err != nil {
		t.Fatalf("read back %s: %v", id, err)
	}
	verdict, err := store.OpenspecValidationOf(art)
	if err != nil {
		t.Fatalf("verdict of %s: %v", id, err)
	}
	return verdict
}

// moveChange moves a change along the line through the transition door.
func moveChange(t *testing.T, ctx context.Context, s *server, p *store.Principal, id, to string) (int, map[string]any) {
	t.Helper()
	return openspecPathCall(t, ctx, p, s.handleOpenspecTransition, "POST",
		"/api/openspec/{id}/transition", id, `{"to":"`+to+`"}`)
}

// A green verdict comes back whole and lands on the row, and the validate
// write preserves the lifecycle state the way every other edit does - no
// state move, no forged trail entry.
func TestOpenspecValidateCachesAVerdictAndPreservesTheState(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	fileSpec(t, ctx, s, p, "session")
	id := fileChange(t, ctx, s, p, cleanValidateFiles())

	code, body := validateChange(t, ctx, s, p, id)
	if code != http.StatusOK {
		t.Fatalf("a clean change was answered %d: %v", code, body)
	}
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("a clean change answered ok=false: %v", body)
	}
	if got, _ := body["files_hash"].(string); got == "" {
		t.Fatalf("the verdict carries no files hash: %v", body)
	}
	if _, has := body["problems"]; has {
		t.Fatalf("a green verdict carries problems: %v", body)
	}

	cached := cachedVerdictOf(t, ctx, s, p, id)
	if cached == nil || !cached.Ok || cached.FilesHash == "" || cached.CheckedAt == 0 {
		t.Fatalf("the cached verdict did not survive the write: %+v", cached)
	}
	if state := openspecStateOf(t, ctx, s, p, id); state != store.OpenspecProposed {
		t.Fatalf("the validate write moved the state to %q - the door is not the lifecycle", state)
	}
	if n := openspecTransitionEvents(t, ctx, s, id); n != 0 {
		t.Fatalf("%d transition entries after a validate, want none", n)
	}
}

// A red verdict is also the answer and also cached - validation reports, the
// lifecycle refuses. And the refusal at complete says the checks' own words,
// the same sentence the door gave the caller.
func TestOpenspecCompleteRefusesTheCachedRedVerdict(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	// No spec row "ghost": the delta names a capability the project does not
	// hold, which is the red arm here.
	id := fileChange(t, ctx, s, p, map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "# tasks\n",
		"specs/ghost/spec.md": "## ADDED Requirements\n\n" +
			"### Requirement: Something\n" +
			"#### Scenario: it works\n- **WHEN** x\n- **THEN** y\n",
	})

	code, body := validateChange(t, ctx, s, p, id)
	if code != http.StatusOK {
		t.Fatalf("validating was answered %d: %v", code, body)
	}
	if ok, _ := body["ok"].(bool); ok {
		t.Fatalf("a change naming no spec validated green: %v", body)
	}
	problems, _ := body["problems"].([]any)
	joined := ""
	for _, pr := range problems {
		joined += pr.(string)
	}
	if !strings.Contains(joined, `names no spec - the capability "ghost"`) {
		t.Fatalf("the problems do not say the check's words: %v", joined)
	}

	if code, body := moveChange(t, ctx, s, p, id, "in-progress"); code != http.StatusOK {
		t.Fatalf("the move was answered %d: %v", code, body)
	}
	code, body = moveChange(t, ctx, s, p, id, "complete")
	if code != http.StatusConflict {
		t.Fatalf("complete on a red verdict was answered %d, want 409: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "the change does not validate - ") ||
		!strings.Contains(why, `names no spec - the capability "ghost"`) {
		t.Fatalf("the refusal does not carry the cached sentence: %q", why)
	}
}

// A verdict outlives nothing, whichever way an edit reaches the row: a
// restate through the create door replaces the fields, dropping the verdict,
// and complete refuses with the absent-cache sentence - the stale-hash
// sentence is the mount edit's, proven at the store where the merge lives.
func TestOpenspecCompleteRefusesAfterAnEditDroppedTheVerdict(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	fileSpec(t, ctx, s, p, "session")
	id := fileChange(t, ctx, s, p, cleanValidateFiles())

	if code, body := validateChange(t, ctx, s, p, id); code != http.StatusOK {
		t.Fatalf("validating was answered %d: %v", code, body)
	}
	if code, body := moveChange(t, ctx, s, p, id, "in-progress"); code != http.StatusOK {
		t.Fatalf("the move was answered %d: %v", code, body)
	}

	// Restate the change with an edited proposal: the files move, and the
	// replaced fields take the verdict with them.
	files := cleanValidateFiles()
	files["proposal.md"] = "# edited after validation\n"
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		updateChange(id, files))
	if code != http.StatusOK {
		t.Fatalf("the edit was answered %d: %v", code, body)
	}

	code, body = moveChange(t, ctx, s, p, id, "complete")
	if code != http.StatusConflict {
		t.Fatalf("complete after the edit was answered %d, want 409: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why,
		"has not been validated - run POST /api/openspec/{id}/validate") {
		t.Fatalf("the refusal does not carry the absent-cache sentence: %q", why)
	}
}

// The kind check is this door's, with its own sentence: a spec has no tasks
// and no deltas, and the refusal says what the caller is holding.
func TestOpenspecValidateRefusesANonChange(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	fileSpec(t, ctx, s, p, "session")

	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"kind":"spec","title":"session","body":"# session\n"}`)
	if code != http.StatusOK {
		t.Fatalf("filing the spec was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)

	code, body = validateChange(t, ctx, s, p, id)
	if code != http.StatusBadRequest {
		t.Fatalf("validating a spec was answered %d, want 400: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "has nothing to validate") {
		t.Fatalf("the refusal does not say what the caller is holding: %q", why)
	}
}

func TestOpenspecValidateRefusesAnUnknownId(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := validateChange(t, ctx, s, p, "no-such-change")
	if code != http.StatusNotFound {
		t.Fatalf("validating nothing was answered %d, want 404: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "no such artifact") {
		t.Fatalf("the refusal does not carry the door's sentence: %q", why)
	}
}
