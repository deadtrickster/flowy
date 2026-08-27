package flowy

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

// scheduleDoor is a server over the gate's database and a principal that can
// reach exactly one project - which is what makes the reach arm below mean
// something.
func scheduleDoor(t *testing.T) (context.Context, *server, *store.Principal, string) {
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

	project := "sched-door-" + ulid.NewString()
	if err := db.DeclareProject(ctx, &store.Project{ID: project}); err != nil {
		t.Fatalf("declare project: %v", err)
	}
	p := &store.Principal{UserID: "u-" + ulid.NewString(), Project: project}
	return ctx, &server{db: db, node: "test-node"}, p, project
}

// call runs one request through one handler with the principal attached, and
// hands back the status and the decoded body.
func schedCall(t *testing.T, ctx context.Context, p *store.Principal,
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

func errorOf(body map[string]any) string {
	if s, ok := body["error"].(string); ok {
		return s
	}
	return ""
}

// THE DOOR'S REASON FOR EXISTING: a cron that can never fire is refused, and
// the refusal carries the PARSER'S sentence rather than a generic 400, so the
// console can show it without keeping a second copy of the rules.
func TestTheDoorRefusesADeadCronWithTheReason(t *testing.T) {
	ctx, s, p, project := scheduleDoor(t)

	code, body := schedCall(t, ctx, p, s.handlePutSchedule, "PUT", "/api/schedules",
		`{"scope":"project","signal":"board","cron":"0 0 30 2 *"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("February 30th was answered %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "February") || !strings.Contains(why, "can never fire") {
		t.Errorf("the refusal does not carry the parser's sentence: %q", why)
	}

	// Nothing stored, so the console cannot show a saved schedule that will
	// never fire.
	rows, err := s.db.ListSchedules(ctx, store.ProjectScope(project))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("the refused save left %d row(s)", len(rows))
	}

	// THE CONTROL: the same door, the same scope, a real crontab line.
	code, body = schedCall(t, ctx, p, s.handlePutSchedule, "PUT", "/api/schedules",
		`{"scope":"project","signal":"board","cron":"0 9 * * 1-5"}`)
	if code != http.StatusOK {
		t.Fatalf("a weekday 09:00 schedule was answered %d: %v", code, body)
	}
	if body["cron"] != "0 9 * * 1-5" {
		t.Errorf("the stored row came back as %v", body)
	}
}

// A signal the node does not deliver is refused, and the refusal LISTS what it
// does deliver - a typo needs the right spelling, not just a no.
func TestTheDoorRefusesASignalNothingDelivers(t *testing.T) {
	ctx, s, p, _ := scheduleDoor(t)

	code, body := schedCall(t, ctx, p, s.handlePutSchedule, "PUT", "/api/schedules",
		`{"scope":"project","signal":"chatt","realtime":true}`)
	if code != http.StatusBadRequest {
		t.Fatalf("an unknown signal was answered %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "chat") {
		t.Errorf("the refusal does not list the signals: %q", why)
	}
}

// FLEET IS THE OPERATOR'S, because one fleet row changes what every project and
// every room resolves. The control is the same principal writing at project
// scope in the same test: it is a scope check and not a broken credential.
func TestFleetScopeIsTheOperators(t *testing.T) {
	ctx, s, p, _ := scheduleDoor(t)

	code, body := schedCall(t, ctx, p, s.handlePutSchedule, "PUT", "/api/schedules",
		`{"scope":"fleet","signal":"chat","realtime":true}`)
	if code != http.StatusForbidden {
		t.Fatalf("a fleet write by a non-operator was answered %d: %v", code, body)
	}
	if why := errorOf(body); !strings.Contains(why, "operator") {
		t.Errorf("the refusal does not say whose it is: %q", why)
	}

	code, _ = schedCall(t, ctx, p, s.handlePutSchedule, "PUT", "/api/schedules",
		`{"scope":"project","signal":"chat","realtime":true}`)
	if code != http.StatusOK {
		t.Fatalf("the same principal was refused at project scope too (%d), so the arm above measured the credential and not the scope", code)
	}
}

// A project this credential cannot reach is a 403 whatever it asks for.
func TestAProjectYouCannotReachIsRefused(t *testing.T) {
	ctx, s, p, _ := scheduleDoor(t)
	other := "sched-elsewhere-" + ulid.NewString()

	code, body := schedCall(t, ctx, p, s.handleListSchedules, "GET",
		"/api/schedules?scope=project&project="+other, "")
	if code != http.StatusForbidden {
		t.Fatalf("reading another project's schedules was answered %d: %v", code, body)
	}
	code, _ = schedCall(t, ctx, p, s.handlePutSchedule, "PUT", "/api/schedules",
		`{"scope":"project","project":"`+other+`","signal":"chat","realtime":true}`)
	if code != http.StatusForbidden {
		t.Fatalf("writing another project's schedule was answered %d", code)
	}
}

// DELETE SAYS WHETHER THERE WAS ANYTHING TO REMOVE. "It was already inheriting"
// and "the override is gone" are different outcomes, and a console that cannot
// tell them apart cannot tell a person whether what they were looking at was
// ever theirs.
func TestDeleteSaysWhetherThereWasAnOverride(t *testing.T) {
	ctx, s, p, project := scheduleDoor(t)

	code, body := schedCall(t, ctx, p, s.handleDeleteSchedule, "DELETE",
		"/api/schedules/board?scope=project", "")
	if code != http.StatusNotFound {
		t.Fatalf("deleting a row that never existed was answered %d: %v", code, body)
	}

	if _, err := s.db.PutSchedule(ctx, store.Schedule{
		Scope: store.ProjectScope(project), Signal: "board", Cron: "0 9 * * *",
	}, "test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	r := httptest.NewRequest("DELETE", "/api/schedules/board?scope=project", nil)
	r.SetPathValue("signal", "board")
	r = r.WithContext(context.WithValue(ctx, principalKey{}, p))
	w := httptest.NewRecorder()
	s.handleDeleteSchedule(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("deleting a real override was answered %d: %s", w.Code, w.Body.String())
	}
}

// THE RESOLVED VIEW SAYS WHICH SCOPE ANSWERED. Without that an inherited
// default and a room's own setting are identical in every field, and the view
// can be read but not checked.
func TestResolvedSaysWhichScopeAnswered(t *testing.T) {
	ctx, s, p, project := scheduleDoor(t)

	code, body := schedCall(t, ctx, p, s.handleResolvedSchedules, "GET", "/api/schedules/resolved", "")
	if code != http.StatusOK {
		t.Fatalf("resolve answered %d: %v", code, body)
	}
	entries, _ := body["resolved"].([]any)
	if len(entries) != len(store.Signals) {
		t.Fatalf("resolved %d signals, want %d: %v", len(entries), len(store.Signals), body)
	}
	for _, e := range entries {
		row, _ := e.(map[string]any)
		if row["defaulted"] != true {
			t.Errorf("an untouched signal is not flagged as a default: %v", row)
		}
	}

	if _, err := s.db.PutSchedule(ctx, store.Schedule{
		Scope: store.ProjectScope(project), Signal: "board", Cron: "*/30 * * * *",
	}, "test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, body = schedCall(t, ctx, p, s.handleResolvedSchedules, "GET", "/api/schedules/resolved", "")
	entries, _ = body["resolved"].([]any)
	var board map[string]any
	for _, e := range entries {
		if row, _ := e.(map[string]any); row["signal"] == "board" {
			board = row
		}
	}
	if board == nil {
		t.Fatal("board is missing from the resolved view")
	}
	if board["from_kind"] != store.SchedProject {
		t.Errorf("board resolved from %v, want the project row that was just written", board["from_kind"])
	}
	if board["defaulted"] != false {
		t.Errorf("a written row is being reported as an untouched default: %v", board)
	}
	if board["cron"] != "*/30 * * * *" {
		t.Errorf("board's cron is %v", board["cron"])
	}
}

// LISTING SHOWS WHAT IS SET. A signal with no row is ABSENT rather than shown
// as off, because those are different states and this list is where a person
// first learns which one they are looking at.
func TestListingShowsWhatIsSetAndNothingElse(t *testing.T) {
	ctx, s, p, project := scheduleDoor(t)

	if _, err := s.db.PutSchedule(ctx, store.Schedule{
		Scope: store.ProjectScope(project), Signal: "chat",
	}, "test"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	code, body := schedCall(t, ctx, p, s.handleListSchedules, "GET", "/api/schedules?scope=project", "")
	if code != http.StatusOK {
		t.Fatalf("list answered %d: %v", code, body)
	}
	rows, _ := body["schedules"].([]any)
	if len(rows) != 1 {
		t.Fatalf("listed %d row(s), want the one that was written: %v", len(rows), body)
	}
	row, _ := rows[0].(map[string]any)
	if row["signal"] != "chat" {
		t.Fatalf("listed %v", row)
	}
	// The explicit off that was written reads as off, not as absent.
	if row["realtime"] != false || row["cron"] != "" {
		t.Errorf("the stored never came back as %v", row)
	}
	// And the signal set rides along, so a console renders the node's list
	// rather than hard-coding one that drifts.
	signals, _ := body["signals"].([]any)
	if len(signals) != len(store.Signals) {
		t.Errorf("the door advertised %d signals, the store knows %d", len(signals), len(store.Signals))
	}
}
