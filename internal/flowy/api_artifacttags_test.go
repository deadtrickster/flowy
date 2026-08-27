package flowy

// What the artifacts list does with a tag, and with a parameter it has never
// heard of.
//
// THE MEASUREMENT. On the live node, 0.8.0+980a537:
//
//	GET /api/artifacts?type=finding             -> 40 artifacts
//	GET /api/artifacts?type=finding&tag=ragflow -> 40 artifacts
//
// while 24 of those findings carry `serenedb` and 16 carry `ragflow`. The data
// was right and the query was ignored, and an ignored filter answers 200 with
// MORE than was asked for - a wrong answer in the shape of a right one. A
// console stacking that filter shows every ragflow row under a serenedb heading
// and neither the console nor the person reading it can tell.
//
// These go over the door rather than into the store on purpose: the defect was
// the handler dropping a parameter, and a store-level test would have passed
// throughout.

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

// listDoor is a server over the database the gate started, and the principal
// that will do the asking. Without DATABASE_URL there is nothing to talk to, so
// this sits out a plain `go test ./...` exactly as the store's own tests do.
func listDoor(t *testing.T) (context.Context, *server, *store.Principal, string) {
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

	project := "tagfilter-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("declare project %s: %v", project, err)
	}
	p := &store.Principal{UserID: "u-" + ulid.NewString(), Project: project}
	return ctx, &server{db: db, node: "test-node"}, p, project
}

// write puts one artifact in the project, owned by the principal that reads it
// back. The tags are the point; everything else is scaffolding.
func write(t *testing.T, ctx context.Context, s *server, p *store.Principal,
	project, title, artifactType, status string, tags, userTags []string,
) string {
	t.Helper()

	art := &store.Artifact{
		ID: ulid.NewString(), Type: artifactType, OwnerUser: p.UserID,
		Title: title, Body: "seeded by the tag filter test",
		Status: status, Tags: tags, UserTags: userTags,
		Visibility: "project", Project: &project,
	}
	if err := s.db.UpsertArtifact(ctx, art); err != nil {
		t.Fatalf("upsert %s: %v", title, err)
	}
	return art.ID
}

// ask calls the list door with a query string and returns the decoded body and
// the status, so a test can assert on a refusal and on an answer with the same
// call.
func ask(t *testing.T, ctx context.Context, s *server, p *store.Principal, query string) (int, map[string]any) {
	t.Helper()

	r := httptest.NewRequest("GET", "/api/artifacts?"+query, nil)
	r = r.WithContext(context.WithValue(ctx, principalKey{}, p))
	w := httptest.NewRecorder()
	s.handleListArtifacts(w, r)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer to %q was not json: %v (%s)", query, err, w.Body.String())
	}
	return w.Code, body
}

// titles is what came back, in the order it came back.
func titles(t *testing.T, body map[string]any) []string {
	t.Helper()

	raw, err := json.Marshal(body["artifacts"])
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var arts []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(raw, &arts); err != nil {
		t.Fatalf("the artifacts were not a list of artifacts: %v", err)
	}
	out := make([]string, 0, len(arts))
	for _, a := range arts {
		out = append(out, a.Title)
	}
	return out
}

func has(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// TestATagNarrowsTheListAndTwoTagsMeanBoth is the defect itself.
//
// Repeated tag is AND, not OR: stacked filters in a console mean "and" to the
// person clicking them - picking `serenedb` and then `ragflow` asks for the
// rows that are both, and a widening answer to a second click is the same wrong
// answer this whole test is about, one step along.
func TestATagNarrowsTheListAndTwoTagsMeanBoth(t *testing.T) {
	ctx, s, p, project := listDoor(t)

	write(t, ctx, s, p, project, "both", "finding", "open",
		[]string{"serenedb", "ragflow"}, nil)
	write(t, ctx, s, p, project, "serenedb only", "finding", "open",
		[]string{"serenedb"}, nil)
	write(t, ctx, s, p, project, "ragflow only", "finding", "open",
		[]string{"ragflow"}, nil)
	write(t, ctx, s, p, project, "untagged", "finding", "open", nil, nil)

	code, body := ask(t, ctx, s, p, "type=finding&tag=ragflow")
	if code != http.StatusOK {
		t.Fatalf("a tag filter was answered with %d: %v", code, body)
	}
	got := titles(t, body)
	if len(got) != 2 || !has(got, "both") || !has(got, "ragflow only") {
		t.Fatalf("?tag=ragflow returned %v, want exactly the two ragflow rows - "+
			"a filter that is not applied answers with MORE than was asked for", got)
	}

	code, body = ask(t, ctx, s, p, "type=finding&tag=ragflow&tag=serenedb")
	if code != http.StatusOK {
		t.Fatalf("two tags were answered with %d: %v", code, body)
	}
	if got := titles(t, body); len(got) != 1 || got[0] != "both" {
		t.Fatalf("two tags returned %v, want only the row carrying both: "+
			"repeated tag is AND", got)
	}
}

// TestATagMatchesEitherColumnOfLabels holds the half a caller cannot see.
//
// tags and user_tags are two columns and one list to every reader here - the
// console draws them merged (todoTags in web/src/lib/todos.ts) and the TUI
// prints both - so the chip a person clicks may have come from either. A filter
// that knew only about `tags` would answer nothing for half the chips it was
// offered, which is an empty page that reads like "there are none".
func TestATagMatchesEitherColumnOfLabels(t *testing.T) {
	ctx, s, p, project := listDoor(t)

	write(t, ctx, s, p, project, "node tag", "finding", "open", []string{"lithe"}, nil)
	write(t, ctx, s, p, project, "author tag", "finding", "open", nil, []string{"lithe"})
	write(t, ctx, s, p, project, "neither", "finding", "open", []string{"other"}, nil)

	code, body := ask(t, ctx, s, p, "type=finding&tag=lithe")
	if code != http.StatusOK {
		t.Fatalf("a tag filter was answered with %d: %v", code, body)
	}
	got := titles(t, body)
	if len(got) != 2 || !has(got, "node tag") || !has(got, "author tag") {
		t.Fatalf("?tag=lithe returned %v, want the row tagged by the node and "+
			"the one tagged by its author", got)
	}
}

// TestATagComposesWithTheOtherNarrowingsAndIsAppliedBeforeTheLimit is the third
// and fourth claims: a tag ANDs with type, kind and status like every other
// narrowing, and it runs in the query rather than over the page.
//
// The limit half is not tidiness. A filter applied after the page is cut is the
// same defect in different clothes - it returns fewer rows and still lies about
// the set - so the seeding puts the rows that do NOT match at the top of the
// order, where an unfiltered LIMIT 2 would take them.
func TestATagComposesWithTheOtherNarrowingsAndIsAppliedBeforeTheLimit(t *testing.T) {
	ctx, s, p, project := listDoor(t)

	// Oldest first: the list is newest first, so these three are what a correct
	// LIMIT 2 has to reach past four newer rows to find.
	write(t, ctx, s, p, project, "wanted 1", "finding", "open", []string{"deep"}, nil)
	write(t, ctx, s, p, project, "wanted 2", "finding", "open", []string{"deep"}, nil)
	write(t, ctx, s, p, project, "wanted 3", "finding", "open", []string{"deep"}, nil)
	// Newer, and none of them wanted: a different status, a different type, and
	// no tag at all.
	write(t, ctx, s, p, project, "closed", "finding", "done", []string{"deep"}, nil)
	write(t, ctx, s, p, project, "a report", "report", "open", []string{"deep"}, nil)
	write(t, ctx, s, p, project, "untagged", "finding", "open", nil, nil)
	write(t, ctx, s, p, project, "untagged too", "finding", "open", nil, nil)

	code, body := ask(t, ctx, s, p, "type=finding&status=open&tag=deep")
	if code != http.StatusOK {
		t.Fatalf("tag beside type and status was answered with %d: %v", code, body)
	}
	got := titles(t, body)
	if len(got) != 3 {
		t.Fatalf("tag with type and status returned %v, want the three open "+
			"findings carrying it", got)
	}

	code, body = ask(t, ctx, s, p, "type=finding&status=open&tag=deep&limit=2")
	if code != http.StatusOK {
		t.Fatalf("tag with a limit was answered with %d: %v", code, body)
	}
	got = titles(t, body)
	if len(got) != 2 {
		t.Fatalf("limit=2 returned %d rows: %v", len(got), got)
	}
	for _, title := range got {
		if !strings.HasPrefix(title, "wanted") {
			t.Fatalf("limit=2 returned %v - the page was cut before the filter "+
				"ran, so it is short AND wrong", got)
		}
	}
}

// TestTheListRefusesAParameterItDoesNotHonour is the rule the tag defect is one
// instance of.
//
// An unsupported filter that answers 200 with unfiltered data is undetectable
// from the client: there is no field to check, no count to compare, nothing.
// `?tags=node-wide` - the plural, which is what the report was filed with - is
// exactly that mistake, and it read as five matching rows.
//
// No database: every refusal here happens before s.db is touched, so a case
// that reached the store would nil-deref instead of returning a status. A
// passing case is proof the door stopped short of the query.
//
// `assignee` used to be in this table and is not any more, because the door
// honours it now - see assigneeArg and the board check in run-tests.sh. That
// removal is the correct half of the rule: this list is what is REFUSED, and
// implementing a filter means moving it out of here rather than leaving a
// refusal that outlived its reason. The nil-deref above is what caught it,
// which is the same property the comment is claiming.
func TestTheListRefusesAParameterItDoesNotHonour(t *testing.T) {
	s := &server{}
	p := &store.Principal{UserID: "u-nobody", Project: "p"}

	for _, c := range []struct{ query, param string }{
		{"tags=node-wide", "tags"},
		{"type=finding&tags=ragflow", "tags"},
		{"severity=high", "severity"},
		{"visibility=personal", "visibility"},
		{"q=tailrace", "q"},
	} {
		r := httptest.NewRequest("GET", "/api/artifacts?"+c.query, nil)
		r = r.WithContext(context.WithValue(context.Background(), principalKey{}, p))
		w := httptest.NewRecorder()
		s.handleListArtifacts(w, r)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("%q was answered with %d, not 400 - an unhonoured filter "+
				"that answers 200 hands back more than was asked for", c.query, w.Code)
		}
		var got map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("the refusal to %q was not json: %v (%s)", c.query, err, w.Body.String())
		}
		// Naming it is most of the value: "bad request" leaves the caller
		// re-reading their own URL for the typo.
		if !strings.Contains(got["error"], c.param) {
			t.Fatalf("the refusal to %q does not name %q: %q", c.query, c.param, got["error"])
		}
	}
}

// The other half: the fix must not have shut the door on the queries this node
// already answers. These are the ones its own clients send - the console
// (web/src/lib/api.ts, web/src/lib/diagrams.ts), the TUI
// (internal/tui/client.go) and firecode's board-nag.sh.
func TestTheListStillTakesTheParametersItDocuments(t *testing.T) {
	ctx, s, p, project := listDoor(t)

	write(t, ctx, s, p, project, "a todo", "memory", "open", []string{"keep"}, nil)

	for _, query := range []string{
		"type=report",
		"type=finding",
		"type=memory&kind=todo&limit=200",
		"type=memory&kind=note&limit=200",
		"type=memory&kind=todo&room=build",
		"type=diagram&kind=drawio&limit=200",
		"kind=todo&limit=200",
		"type=memory&kind=todo&category=bug",
		"project=" + project,
		"status=open",
		"scope=all",
		"type=memory&kind=todo&tag=keep",
		"",
	} {
		if code, body := ask(t, ctx, s, p, query); code != http.StatusOK {
			t.Fatalf("a query this node's own clients send was answered with %d: %q -> %v",
				code, query, body)
		}
	}
}
