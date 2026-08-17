package store

import (
	"errors"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/ulid"
)

// TestFindingRunBodyAndEntry is the pure half: what a run renders as on a
// surface that knows nothing about this event type, and what comes back out
// of an event's meta the way it went in. No DATABASE_URL needed.
func TestFindingRunBodyAndEntry(t *testing.T) {
	if got := findingRunBody(FindingRun{Version: "v3", Confirmed: true, Status: "reproduced"}); got != "run v3: confirmed (reproduced)" {
		t.Errorf("body = %q", got)
	}
	if got := findingRunBody(FindingRun{Version: "v2", Confirmed: false}); got != "run v2: not confirmed" {
		t.Errorf("body = %q", got)
	}

	e := &Event{
		ID: "e1", Artifact: "f1", Actor: "u1", SeqHLC: 42, Node: "n1",
		Created: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		Meta: []byte(`{"version":"v3","sha":"abc123","confirmed":true,"status":"reproduced",
			"actor_kind":"agent","actor_user":"u1"}`),
	}
	entry := findingRunEntryOf(e)
	if entry.ID != "e1" || entry.Finding != "f1" || entry.Version != "v3" || entry.SHA != "abc123" ||
		!entry.Confirmed || entry.Status != "reproduced" || entry.ActorKind != "agent" {
		t.Errorf("entry came back as %+v", entry)
	}
	if entry.At != "2026-08-18T12:00:00Z" {
		t.Errorf("at = %q", entry.At)
	}
}

// TestFindingRunsRoundTrip is the store half: two runs of the same version -
// one failing, one confirming - both stay in the log in order, which is the
// whole point of an append-only verdict over a field that would have lost
// the first one.
func TestFindingRunsRoundTrip(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "runs")
	owner := &User{Handle: "runner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}

	finding := &Artifact{Type: "finding", Project: &project, OwnerUser: owner.ID, Title: "flaky under load"}
	if err := db.UpsertArtifact(ctx, finding); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}

	if _, err := db.RecordFindingRun(ctx, p, finding.ID, FindingRun{
		Version: "v2", SHA: "aaa111", Confirmed: false, Status: "did not reproduce",
	}); err != nil {
		t.Fatalf("record run 1: %v", err)
	}
	if _, err := db.RecordFindingRun(ctx, p, finding.ID, FindingRun{
		Version: "v2", SHA: "bbb222", Confirmed: true, Status: "reproduced",
	}); err != nil {
		t.Fatalf("record run 2: %v", err)
	}

	runs, err := db.FindingRuns(ctx, p, finding.ID)
	if err != nil {
		t.Fatalf("finding runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[0].SHA != "aaa111" || runs[0].Confirmed {
		t.Errorf("run 0 = %+v, want the red one first", runs[0])
	}
	if runs[1].SHA != "bbb222" || !runs[1].Confirmed {
		t.Errorf("run 1 = %+v, want the green one second", runs[1])
	}
	for _, r := range runs {
		if r.Finding != finding.ID || r.Version != "v2" || r.Actor != owner.ID {
			t.Errorf("run %+v does not name the finding, version or actor correctly", r)
		}
	}
}

// TestFindingRunsRefusesNoProject mirrors AddDep's refusal of an edge into a
// projectless todo: a run event on a projectless finding would be readable
// only by whoever reported it, silently, which is exactly the failure this
// file's head comment explains.
func TestFindingRunsRefusesNoProject(t *testing.T) {
	ctx, db := open(t)
	owner := &User{Handle: "runner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID}

	finding := &Artifact{Type: "finding", OwnerUser: owner.ID, Visibility: VisibilityPersonal, Title: "personal"}
	if err := db.UpsertArtifact(ctx, finding); err != nil {
		t.Fatalf("upsert finding: %v", err)
	}

	_, err := db.RecordFindingRun(ctx, p, finding.ID, FindingRun{Version: "v1", Confirmed: true})
	if err == nil {
		t.Fatal("a run on a projectless finding should be refused")
	}
}

// TestFindingRunsRefusesWrongType is the namespace answer, same as
// findingrepro.go's.
func TestFindingRunsRefusesWrongType(t *testing.T) {
	ctx, db := open(t)
	project := declaredProject(t, ctx, db, "runs")
	owner := &User{Handle: "runner-" + ulid.NewString()}
	if err := db.InsertUser(ctx, owner); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	p := &Principal{UserID: owner.ID, Project: project}
	bug := &Artifact{Type: "bug", Project: &project, OwnerUser: owner.ID, Title: "not a finding"}
	if err := db.UpsertArtifact(ctx, bug); err != nil {
		t.Fatalf("upsert bug: %v", err)
	}

	_, err := db.RecordFindingRun(ctx, p, bug.ID, FindingRun{Version: "v1"})
	var nf NotAFindingError
	if !errors.As(err, &nf) {
		t.Fatalf("run on a bug id should be NotAFindingError, got %v", err)
	}
	if _, err := db.FindingRuns(ctx, p, bug.ID); !errors.As(err, &nf) {
		t.Fatalf("FindingRuns on a bug id should be NotAFindingError, got %v", err)
	}
}
