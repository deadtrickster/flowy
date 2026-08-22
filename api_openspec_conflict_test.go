package main

// The conflicts door: what GET /api/openspec/{id}/conflicts answers, and what
// it refuses - a spec id, an unknown id.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// openspecPathCall is openspecCall with one path value set - the conflicts
// door reads its subject from the path, and a plain httptest request carries
// none.
func openspecPathCall(t *testing.T, ctx context.Context, p *store.Principal,
	h http.HandlerFunc, method, target, id, body string,
) (int, map[string]any) {
	t.Helper()

	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r = r.WithContext(context.WithValue(ctx, principalKey{}, p))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h(w, r)

	out := map[string]any{}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s answered something that is not json: %s", method, target, w.Body.String())
		}
	}
	return w.Code, out
}

func TestOpenspecConflictsListsClashingChange(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)

	files := map[string]string{
		"proposal.md":       "change\n",
		"specs/foo/spec.md": "a delta on foo\n",
	}
	code, a := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(files))
	if code != http.StatusOK {
		t.Fatalf("first change was answered %d: %v", code, a)
	}
	code, b := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(files))
	if code != http.StatusOK {
		t.Fatalf("second change was answered %d: %v", code, b)
	}
	aID, _ := a["id"].(string)
	bID, _ := b["id"].(string)
	if aID == "" || bID == "" {
		t.Fatalf("the filed rows carry ids: %v, %v", a, b)
	}

	code, out := openspecPathCall(t, ctx, p, s.handleOpenspecConflicts,
		"GET", "/api/openspec/"+aID+"/conflicts", aID, "")
	if code != http.StatusOK {
		t.Fatalf("conflicts of a change was answered %d: %v", code, out)
	}
	edges, ok := out["conflicts"].([]any)
	if !ok || len(edges) != 1 {
		t.Fatalf("one clash over foo was filed, the door lists %v", out["conflicts"])
	}
	edge, ok := edges[0].(map[string]any)
	if !ok || edge["change"] != bID || edge["spec"] != "foo" {
		t.Fatalf("the edge names the other change and the capability: %v", edges[0])
	}

	// The pair is symmetric - b's answer names a.
	code, out = openspecPathCall(t, ctx, p, s.handleOpenspecConflicts,
		"GET", "/api/openspec/"+bID+"/conflicts", bID, "")
	if code != http.StatusOK {
		t.Fatalf("conflicts of the second change was answered %d: %v", code, out)
	}
	edges, _ = out["conflicts"].([]any)
	if len(edges) != 1 {
		t.Fatalf("the reverse half of the pair exists: %v", out["conflicts"])
	}
	edge, _ = edges[0].(map[string]any)
	if edge["change"] != aID {
		t.Fatalf("the reverse edge names a, got %v", edges[0])
	}
}

func TestOpenspecConflictsRefusals(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)

	// A spec row is refused with the door's own sentence - edges are between
	// changes.
	code, spec := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"kind":"spec","title":"the-capability","body":"# the-capability\n"}`)
	if code != http.StatusOK {
		t.Fatalf("the spec was answered %d: %v", code, spec)
	}
	specID, _ := spec["id"].(string)
	code, out := openspecPathCall(t, ctx, p, s.handleOpenspecConflicts,
		"GET", "/api/openspec/"+specID+"/conflicts", specID, "")
	if code != http.StatusBadRequest {
		t.Fatalf("conflicts of a spec was answered %d, want 400: %v", code, out)
	}
	if err, _ := out["error"].(string); !strings.Contains(err, "edges between changes") {
		t.Fatalf("the refusal names what conflicts are: %v", out)
	}

	// An unknown id is a 404, the same sentence as every other read door.
	code, out = openspecPathCall(t, ctx, p, s.handleOpenspecConflicts,
		"GET", "/api/openspec/01NEVERFILED/conflicts", "01NEVERFILED", "")
	if code != http.StatusNotFound {
		t.Fatalf("conflicts of a missing row was answered %d, want 404: %v", code, out)
	}
	if err, _ := out["error"].(string); !strings.Contains(err, "no such artifact") {
		t.Fatalf("the 404 names the row: %v", out)
	}
}
