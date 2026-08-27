package flowy

// WHAT THE RUN DOOR ANSWERS, over the door rather than into the store.
//
// The store's own round trip is tested in internal/store/findingruns_test.go and
// passed throughout the defect this file is about: the log was readable, and the
// only readers were a test and an MCP tool. The console speaks HTTP, so what had
// to be proven is that the HTTP door exists and hands back the HISTORY.
//
// Two runs of the same version, one failing and one confirming, is the case
// worth asserting rather than one run: a door that answered with the latest
// verdict passes a one-run test and loses exactly the fact the log was written
// to keep.

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

// runsDoor is a server over the database the gate started, and the principal
// doing the asking - listDoor's shape in api_artifacttags_test.go, and its rule:
// without DATABASE_URL there is nothing to talk to, so this sits out a plain
// `go test ./...` the way the store's own tests do.
func runsDoor(t *testing.T) (context.Context, *server, *store.Principal, string) {
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

	project := "runsdoor-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("declare project %s: %v", project, err)
	}
	owner := &store.User{Handle: "runsdoor-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &store.Principal{UserID: owner.ID, Project: project}
	return ctx, &server{db: db, node: "test-node"}, p, project
}

// askRuns calls the run door for one id and returns the status and the decoded
// body, so a test can assert on a refusal and on an answer with the same call.
func askRuns(t *testing.T, ctx context.Context, s *server, p *store.Principal, id string) (int, map[string]any) {
	t.Helper()

	r := httptest.NewRequest("GET", "/api/finding/"+id+"/runs", nil)
	r.SetPathValue("id", id)
	r = r.WithContext(context.WithValue(ctx, principalKey{}, p))
	w := httptest.NewRecorder()
	s.handleFindingRuns(w, r)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the answer for %s was not json: %v (%s)", id, err, w.Body.String())
	}
	return w.Code, body
}

func TestTheRunDoorHandsBackTheHistoryAndNotTheLatest(t *testing.T) {
	ctx, s, p, project := runsDoor(t)

	finding := &store.Artifact{
		ID: ulid.NewString(), Type: "finding", OwnerUser: p.UserID,
		Title: "spills to a directory it cannot create", Body: "seeded by the run door test",
		Visibility: "project", Project: &project,
	}
	if err := s.db.UpsertArtifact(ctx, finding); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}

	// A finding nobody has run yet answers with an empty log rather than a 404:
	// "never run" and "not there" are different facts and the console draws them
	// differently.
	code, body := askRuns(t, ctx, s, p, finding.ID)
	if code != http.StatusOK {
		t.Fatalf("a finding with no runs answered %d, not 200: %v", code, body)
	}
	if n, _ := body["count"].(float64); n != 0 {
		t.Fatalf("a finding with no runs reported count %v", body["count"])
	}

	for _, run := range []store.FindingRun{
		{Version: "26.07.4", SHA: "aaa111", Confirmed: false, Status: "not_confirmed"},
		{Version: "26.07.4", SHA: "bbb222", Confirmed: true, Status: "confirmed"},
	} {
		if _, err := s.db.RecordFindingRun(ctx, p, finding.ID, run); err != nil {
			t.Fatalf("record run %s: %v", run.SHA, err)
		}
	}

	code, body = askRuns(t, ctx, s, p, finding.ID)
	if code != http.StatusOK {
		t.Fatalf("the run door answered %d: %v", code, body)
	}
	if n, _ := body["count"].(float64); n != 2 {
		t.Fatalf("two runs were recorded and the door reported %v - a door that "+
			"answers with the latest verdict loses the run before it", body["count"])
	}

	raw, err := json.Marshal(body["runs"])
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var runs []store.FindingRunEntry
	if err := json.Unmarshal(raw, &runs); err != nil {
		t.Fatalf("the runs did not decode: %v (%s)", err, raw)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d entries", len(runs))
	}
	// Oldest first, which is what makes red-then-green legible: the failing run
	// has to be the one that comes back first.
	if runs[0].SHA != "aaa111" || runs[0].Confirmed {
		t.Errorf("the first entry is %+v - the log is not oldest first", runs[0])
	}
	if runs[1].SHA != "bbb222" || !runs[1].Confirmed {
		t.Errorf("the second entry is %+v", runs[1])
	}
	if runs[1].Version != "26.07.4" {
		t.Errorf("the version did not survive the door: %+v", runs[1])
	}
}

// The other id namespace. A todo id at this door is a 404 that says FINDING -
// writeFindingError's whole reason - because a caller told "no such todo" goes
// looking in the wrong space for a row that is right there.
func TestTheRunDoorRefusesAnIDThatIsNotAFinding(t *testing.T) {
	ctx, s, p, project := runsDoor(t)

	todo := &store.Artifact{
		ID: ulid.NewString(), Type: "memory", Kind: "todo", OwnerUser: p.UserID,
		Title: "not a finding", Visibility: "project", Project: &project,
	}
	if err := s.db.UpsertArtifact(ctx, todo); err != nil {
		t.Fatalf("upsert todo: %v", err)
	}

	code, body := askRuns(t, ctx, s, p, todo.ID)
	if code != http.StatusNotFound {
		t.Fatalf("a todo id at the run door answered %d, not 404: %v", code, body)
	}
	msg, _ := body["error"].(string)
	if msg == "" {
		t.Fatalf("the refusal carried no message: %v", body)
	}
	if !strings.Contains(msg, "finding") {
		t.Errorf("the refusal does not say which namespace missed: %q", msg)
	}
}
