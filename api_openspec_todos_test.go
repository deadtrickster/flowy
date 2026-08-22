package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestOpenspecTodosListsTheDerivedRows is the door's reason to exist: the
// change's tasks.md derived todo rows on the write (p2), and this is the read
// that names them - no filter on GET /api/artifacts reaches a todo's origin
// fields.
func TestOpenspecTodosListsTheDerivedRows(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)

	files := map[string]string{
		"proposal.md": "change\n",
		"tasks.md":    "## Work\n\n- [ ] first\n- [ ] second\n",
	}
	code, a := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(files))
	if code != http.StatusOK {
		t.Fatalf("the change was answered %d: %v", code, a)
	}
	id, _ := a["id"].(string)
	if id == "" {
		t.Fatalf("the filed row carries an id: %v", a)
	}

	code, out := openspecPathCall(t, ctx, p, s.handleOpenspecTodos,
		"GET", "/api/openspec/"+id+"/todos", id, "")
	if code != http.StatusOK {
		t.Fatalf("todos of a change was answered %d: %v", code, out)
	}
	todos, ok := out["todos"].([]any)
	if !ok || len(todos) != 2 {
		t.Fatalf("two tasks.md lines derived two todos, the door lists %v", out["todos"])
	}
	for _, raw := range todos {
		row, ok := raw.(map[string]any)
		if !ok || row["kind"] != "todo" {
			t.Fatalf("the answer is todo rows: %v", raw)
		}
		if title, _ := row["title"].(string); title != "first" && title != "second" {
			t.Fatalf("the derived rows are the tasks.md lines, got title %v", title)
		}
	}
}

// TestOpenspecTodosRefusals keeps the door's two edges in their own words: a
// spec derives nothing, and an unknown id is the ordinary 404.
func TestOpenspecTodosRefusals(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)

	code, spec := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"kind":"spec","title":"the-capability","body":"# the-capability\n"}`)
	if code != http.StatusOK {
		t.Fatalf("the spec was answered %d: %v", code, spec)
	}
	specID, _ := spec["id"].(string)
	code, out := openspecPathCall(t, ctx, p, s.handleOpenspecTodos,
		"GET", "/api/openspec/"+specID+"/todos", specID, "")
	if code != http.StatusBadRequest {
		t.Fatalf("todos of a spec was answered %d, want 400: %v", code, out)
	}
	if err, _ := out["error"].(string); !strings.Contains(err, "derives no todos") {
		t.Fatalf("the refusal says what derives todos: %v", out)
	}

	code, out = openspecPathCall(t, ctx, p, s.handleOpenspecTodos,
		"GET", "/api/openspec/01NEVERFILED/todos", "01NEVERFILED", "")
	if code != http.StatusNotFound {
		t.Fatalf("todos of an unknown id was answered %d, want 404: %v", code, out)
	}
	if err, _ := out["error"].(string); !strings.Contains(err, "no such artifact") {
		t.Fatalf("the 404 is the ordinary sentence: %v", out)
	}
}
