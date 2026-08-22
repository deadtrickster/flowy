package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// openspecDoor is a server over the gate's database and a principal that can
// reach exactly one fresh project - the same shape as scheduleDoor, because a
// door test that shares a project with everything else running would be
// reading rows other tests wrote.
func openspecDoor(t *testing.T) (context.Context, *server, *store.Principal, string) {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; run ./run-tests.sh for the live checks")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	db, err := store.Open(ctx, dsn, "test-node")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	project := "openspec-door-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &store.Principal{UserID: "u-" + ulid.NewString(), Project: project}
	return ctx, &server{db: db, node: "test-node"}, p, project
}

// openspecCall runs one request through one handler with the principal
// attached, and hands back the status and the decoded body.
func openspecCall(t *testing.T, ctx context.Context, p *store.Principal,
	h http.HandlerFunc, method, target, body string,
) (int, map[string]any) {
	t.Helper()

	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r = r.WithContext(context.WithValue(ctx, principalKey{}, p))
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

// openspecChangeBody is a change request body carrying exactly the files named.
func openspecChangeBody(files map[string]string) string {
	raw, err := json.Marshal(map[string]any{
		"kind":   store.ChangeKind,
		"title":  "the change",
		"fields": map[string]any{"openspec": map[string]any{"files": files}},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestOpenspecKindAtThisDoor(t *testing.T) {
	for _, kind := range []string{store.SpecKind, store.ChangeKind} {
		if why := openspecKindAtThisDoor("", kind); why != "" {
			t.Fatalf("kind %q with no type must be taken: %s", kind, why)
		}
		if why := openspecKindAtThisDoor(store.MemoryType, kind); why != "" {
			t.Fatalf("kind %q with type %q must be taken: %s", kind, store.MemoryType, why)
		}
	}
	if why := openspecKindAtThisDoor("bug", store.SpecKind); why == "" {
		t.Fatal("type bug is not a memory row - must be refused")
	}
	if why := openspecKindAtThisDoor("", "todo"); why == "" {
		t.Fatal("kind todo is not an openspec kind - must be refused")
	}
}

func TestOpenspecCreateFilesASpec(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"kind":"spec","title":"the-capability","body":"# the-capability\n"}`)
	if code != http.StatusOK {
		t.Fatalf("a spec with words and a name was answered %d: %v", code, body)
	}
	if got, _ := body["kind"].(string); got != store.SpecKind {
		t.Fatalf("the filed row is kind %q, not %q", got, store.SpecKind)
	}
	if got, _ := body["type"].(string); got != store.MemoryType {
		t.Fatalf("an openspec row is type %q; the door wrote %q", store.MemoryType, got)
	}
}

// The create door asks the store's shape check: a change with no proposal.md
// is refused with the check's own sentence. This is the wire proof for the
// CREATE call site - the refusal has to be checkOpenspecRow's, not this
// handler's.
func TestOpenspecCreateRefusesAChangeWithoutProposal(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{"tasks.md": "- [ ] do the thing\n"}))
	if code != http.StatusBadRequest {
		t.Fatalf("a change that proposes nothing was answered %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "proposal.md") {
		t.Fatalf("the refusal does not carry the shape check's sentence: %q", why)
	}
}

// The update half of the same door asks the check too: restating a change with
// files that dropped the proposal is refused rather than silently husking the
// row. Wire proof for the UPSERT call site.
func TestOpenspecUpdateRefusesToStripTheProposal(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{
			"proposal.md": "# why\n",
			"tasks.md":    "- [ ] do the thing\n",
		}))
	if code != http.StatusOK {
		t.Fatalf("a change with a proposal was answered %d: %v", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("the filed change carries no id: %v", body)
	}

	code, body = openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"id":"`+id+`","kind":"change","fields":{"openspec":{"files":{"tasks.md":"- [ ] x\n"}}}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("stripping the proposal from a change was answered %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "proposal.md") {
		t.Fatalf("the refusal does not carry the shape check's sentence: %q", why)
	}
}

// The general door asks the same check - an openspec row is an artifact row,
// and a husk is refused whichever surface writes it.
func TestArtifactDoorRefusesAChangeWithoutProposal(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleCreateArtifact, "POST", "/api/artifacts",
		`{"type":"memory","kind":"change","title":"x",`+
			`"fields":{"openspec":{"files":{"tasks.md":"- [ ] x\n"}}}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("the general door answered a husk change %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "proposal.md") {
		t.Fatalf("the general door's refusal does not carry the shape check's sentence: %q", why)
	}
}

func TestOpenspecCreateRefusesANonOpenspecKind(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"kind":"todo","title":"x"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("kind todo at the openspec door was answered %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "POST /api/artifacts") {
		t.Fatalf("the refusal does not point at the general door: %q", why)
	}
}

func TestOpenspecCreateRefusesATypeItDoesNotWrite(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"type":"bug","kind":"spec","title":"x","body":"# x\n"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("type bug at the openspec door was answered %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "memory") {
		t.Fatalf("the refusal does not name the type an openspec row has: %q", why)
	}
}

func TestOpenspecListReturnsBothKindsAndNothingElse(t *testing.T) {
	ctx, s, p, project := openspecDoor(t)

	if code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		`{"kind":"spec","title":"cap","body":"# cap\n"}`); code != http.StatusOK {
		t.Fatalf("filing the spec was answered %d: %v", code, body)
	}
	if code, body := openspecCall(t, ctx, p, s.handleOpenspecCreate, "POST", "/api/openspec",
		openspecChangeBody(map[string]string{"proposal.md": "# why\n"})); code != http.StatusOK {
		t.Fatalf("filing the change was answered %d: %v", code, body)
	}
	// A todo in the same project, written directly: it must not come back from
	// the openspec list.
	todo := &store.Artifact{
		ID: ulid.NewString(), Type: store.MemoryType, Kind: "todo",
		Project: &project, OwnerUser: p.UserID, Title: "ordinary work",
		Status: store.TodoStatus,
	}
	if err := s.db.UpsertArtifact(ctx, todo); err != nil {
		t.Fatalf("writing the todo: %v", err)
	}

	code, body := openspecCall(t, ctx, p, s.handleOpenspecList, "GET", "/api/openspec", "")
	if code != http.StatusOK {
		t.Fatalf("the list was answered %d: %v", code, body)
	}
	arts, _ := body["artifacts"].([]any)
	if len(arts) != 2 {
		t.Fatalf("the list answered %d rows, want the spec and the change: %v", len(arts), body)
	}
	kinds := map[string]bool{}
	for _, a := range arts {
		m, _ := a.(map[string]any)
		kinds[m["kind"].(string)] = true
	}
	if !kinds[store.SpecKind] || !kinds[store.ChangeKind] || kinds["todo"] {
		t.Fatalf("the list answered kinds %v, want exactly spec and change", kinds)
	}
}

func TestOpenspecListRefusesUnknownParams(t *testing.T) {
	ctx, s, p, _ := openspecDoor(t)
	code, body := openspecCall(t, ctx, p, s.handleOpenspecList, "GET", "/api/openspec?bogus=1", "")
	if code != http.StatusBadRequest {
		t.Fatalf("a parameter this list does not honour was answered %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "bogus") {
		t.Fatalf("the refusal does not name the parameter: %q", why)
	}
}
