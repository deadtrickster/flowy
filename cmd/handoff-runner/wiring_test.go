package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deadtrickster/flowy/internal/repro"
	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// linkedFor builds the real adapter over the fixture's store and config,
// with the worker pool stood in for. Every check this file is about happens
// before the pool is reached, and a test that had to bring up Docker to
// exercise a refusal would be a test nobody runs.
func linkedFor(f *fixture) (*linkedQueue, *[]string) {
	var asked []string
	q := &linkedQueue{
		db: f.svc.db, cfg: f.svc.cfg,
		enqueue: func(p *store.Principal, project string, findings []string, version string) ([]int64, error) {
			who := "<nil>"
			if p != nil {
				who = p.UserID + p.AgentID
			}
			asked = append(asked, strings.Join(findings, ",")+"@"+version+" for "+project+" as "+who)
			return []int64{7}, nil
		},
	}
	return q, &asked
}

// TestQueueEnqueuesOnBehalfOfTheCaller is the principal-per-enqueue decision
// seen from this side of the seam: whatever principal the door resolved off
// the bearer token is the principal handed to the runner, and the run is
// remembered against the finding's project.
func TestQueueEnqueuesOnBehalfOfTheCaller(t *testing.T) {
	ctx, f := newFixture(t)
	q, asked := linkedFor(f)

	p, err := f.svc.db.PrincipalForToken(ctx, f.token)
	if err != nil {
		t.Fatalf("principal: %v", err)
	}
	id, err := q.Enqueue(ctx, p, f.finding, "latest")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id != "7" {
		t.Errorf("run id %q, want the queue's own id", id)
	}
	want := f.finding + "@latest for " + f.project + " as " + p.UserID
	if len(*asked) != 1 || (*asked)[0] != want {
		t.Errorf("runner asked %v, want [%q] - a run is performed on behalf of the caller", *asked, want)
	}
}

// TestQueueRefusesWhatItCannotRun: the two refusals runQueue's own contract
// names, plus the two the door needs to stay honest about reach.
//
// EACH ONE NAMES WHAT IS MISSING. That is the whole standard errQueueUnlinked
// set: a queue that answered these with silence, or with an accepted run that
// only fails twenty minutes later, tells an operator nothing they can act on.
func TestQueueRefusesWhatItCannotRun(t *testing.T) {
	ctx, f := newFixture(t)
	q, asked := linkedFor(f)
	p, err := f.svc.db.PrincipalForToken(ctx, f.token)
	if err != nil {
		t.Fatalf("principal: %v", err)
	}

	// A finding with no repro tree: accepted by the store, unrunnable here.
	bare := &store.Artifact{Type: "finding", Project: &f.project, OwnerUser: p.UserID,
		Title: "no tree on this one"}
	if err := f.svc.db.UpsertArtifact(ctx, bare); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A todo, which carries no repro tree by construction.
	todo := &store.Artifact{Type: "todo", Project: &f.project, OwnerUser: p.UserID, Title: "not a finding"}
	if err := f.svc.db.UpsertArtifact(ctx, todo); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	cases := []struct {
		name    string
		finding string
		code    int
		says    string
	}{
		{"no repro tree", bare.ID, http.StatusConflict, "nothing to run"},
		{"not a finding", todo.ID, http.StatusBadRequest, "not a finding"},
		{"no such finding", ulid.NewString(), http.StatusNotFound, "no such"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := q.Enqueue(ctx, p, c.finding, "latest")
			if err == nil {
				t.Fatal("accepted a run it cannot perform")
			}
			if !strings.Contains(strings.ToLower(faultMessage(err)), c.says) {
				t.Errorf("refusal %q does not say %q", faultMessage(err), c.says)
			}
			// The status the door will turn it into. A refusal that reads
			// right and arrives as a 500 is one an operator reads as our bug
			// rather than as a fact about their finding.
			w := httptest.NewRecorder()
			writeFault(w, httptest.NewRequest("POST", "/run", nil), err)
			if w.Code != c.code {
				t.Errorf("refusal arrives as %d, want %d", w.Code, c.code)
			}
		})
	}

	// A finding the caller can read perfectly well, in a project this
	// DEPLOYMENT holds no configuration for. It is the runner's own refusal
	// and not the store's, so it is provoked by narrowing the runner rather
	// than by moving the finding out of the caller's reach - those are two
	// different refusals and only one of them is this one.
	narrowed := *f.svc.cfg
	narrowed.Projects = map[string]Project{
		"hr-somewhere-else": {Source: "/nowhere", BaseImage: "example/base:1.0"},
	}
	q.cfg = &narrowed
	if _, err := q.Enqueue(ctx, p, f.finding, "latest"); err == nil {
		t.Error("ran a finding of a project this runner holds no configuration for")
	} else if msg := faultMessage(err); !strings.Contains(msg, f.project) ||
		!strings.Contains(msg, "hr-somewhere-else") {
		t.Errorf("refusal %q names neither the project asked for nor the ones held", msg)
	}
	q.cfg = f.svc.cfg

	if len(*asked) != 0 {
		t.Errorf("the worker pool was handed %v; every one of these should have stopped here", *asked)
	}
}

// TestUnrunnableQueueRefusesByName: a host with no docker command answers
// the three run routes with a sentence naming what is missing, and reports
// linked:false - not an empty list, which is what a runner nobody has asked
// anything looks like.
func TestUnrunnableQueueRefusesByName(t *testing.T) {
	_, f := newFixture(t)
	f.svc.queue = unrunnableQueue{err: errNoDocker}

	if linked(f.svc.queue) {
		t.Error("a queue that cannot run reported itself linked")
	}

	w := f.send("POST", "/run", f.token, `{"finding":"`+f.finding+`"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /run answered %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "docker") {
		t.Errorf("the refusal does not name what is missing: %s", w.Body.String())
	}

	w = f.do("GET", "/runs", f.token)
	var runs struct {
		Runs   []Run `json:"runs"`
		Linked bool  `json:"linked"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode /runs: %v", err)
	}
	if runs.Linked {
		t.Error("/runs said linked from a host that cannot run anything")
	}

	w = f.do("GET", "/run/1/log", f.token)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /run/1/log answered %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "docker") {
		t.Errorf("the log refusal does not name what is missing: %s", w.Body.String())
	}
}

// TestRunRecordsCarryTheirProjectAndUnixTimes checks the two
// translations the adapter exists for and that nothing else does: the
// project, which now rides on the run record itself, and unix seconds rather
// than a zero time for a run that has not started.
func TestRunRecordsCarryTheirProjectAndUnixTimes(t *testing.T) {
	queued := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	q := &linkedQueue{}
	got := []Run{
		q.record(repro.Run{ID: 1, Finding: "f1", Project: "serenedb", Status: repro.StatusQueued, QueuedAt: queued}),
		q.record(repro.Run{ID: 2, Finding: "f2", Project: "ragflow", Status: repro.StatusRunning, QueuedAt: queued}),
	}
	if got[0].Project != "serenedb" || got[1].Project != "ragflow" {
		t.Errorf("projects lost in translation: %q, %q", got[0].Project, got[1].Project)
	}
	if got[0].QueuedAt != queued.Unix() {
		t.Errorf("queued_at %d, want unix seconds %d", got[0].QueuedAt, queued.Unix())
	}
	if got[0].StartedAt != 0 || got[0].EndedAt != 0 {
		t.Errorf("a run that has not started reported start %d end %d, want 0 - the epoch is not a start time",
			got[0].StartedAt, got[0].EndedAt)
	}
}
