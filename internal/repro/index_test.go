package repro

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deadtrickster/flowy/internal/store"
)

// newIndexRunner is a runner over one pair of directories, so that a second
// one can be built over the same pair - which is what a restart is.
func newIndexRunner(t *testing.T, logDir, cacheDir string) *Runner {
	t.Helper()
	r, err := NewRunner(&fakeStore{art: &store.Artifact{}},
		func(string) (ProjectConfig, bool) { return ProjectConfig{}, true },
		Options{Workers: 1, QueueDepth: 8, LogDir: logDir, CacheDir: cacheDir})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

// TestRunsSurviveARestart is the whole point of the index.
//
// The verdict was always durable - it is a signed event on the finding. The
// RUN was not: its id, project, times and log path lived in one map in one
// process, so a restart made a finding that had genuinely been run read as
// never run, which is what a finding nobody has ever run reads as too.
//
// Three properties, because they fail separately:
//
//	A FINISHED RUN COMES BACK WHOLE - every field the console draws.
//	A RUN THAT WAS IN FLIGHT COMES BACK AS AN ERROR SAYING SO, not frozen at
//	  "running", which would be a claim that is no longer true and would never
//	  become false on its own.
//	IDS DO NOT START AGAIN. They name the log files; a second run 1 would
//	  write over the first one's log.
func TestRunsSurviveARestart(t *testing.T) {
	logDir, cacheDir := t.TempDir(), t.TempDir()
	first := newIndexRunner(t, logDir, cacheDir)

	// Nothing is started, so nothing drains the queue: these two are exactly
	// the states a restart catches, without a Docker daemon in sight.
	ids, err := first.Enqueue(testAsker, "serenedb", []string{"finding-done", "finding-live"}, "26.07.5")
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	done, live := ids[0], ids[1]
	yes := true
	first.update(done, func(run *Run) {
		run.Status = StatusConfirmed
		run.SHA = "deadbeef"
		run.Confirmed = &yes
		run.StartedAt = nowPtr()
		run.EndedAt = nowPtr()
	})
	first.update(live, func(run *Run) {
		run.Status = StatusRunning
		run.StartedAt = nowPtr()
	})
	before, _ := first.RunByID(done)

	second := newIndexRunner(t, logDir, cacheDir)

	after, ok := second.RunByID(done)
	if !ok {
		t.Fatal("the finished run did not survive the restart at all")
	}
	switch {
	case after.Finding != before.Finding:
		t.Errorf("finding %q, want %q", after.Finding, before.Finding)
	case after.Project != "serenedb":
		t.Errorf("project %q, want serenedb - a run that comes back not knowing "+
			"which project it was for did not come back", after.Project)
	case after.Version != "26.07.5" || after.SHA != "deadbeef":
		t.Errorf("version %q sha %q, want 26.07.5 deadbeef", after.Version, after.SHA)
	case after.Status != StatusConfirmed:
		t.Errorf("status %q, want confirmed - the verdict is the run's own", after.Status)
	case after.Confirmed == nil || !*after.Confirmed:
		t.Errorf("confirmed %v, want true", after.Confirmed)
	case after.Log != before.Log:
		t.Errorf("log %q, want %q - the log file is still there", after.Log, before.Log)
	case !after.QueuedAt.Equal(before.QueuedAt):
		t.Errorf("queued_at %v, want %v", after.QueuedAt, before.QueuedAt)
	case after.StartedAt == nil || after.EndedAt == nil:
		t.Errorf("a finished run came back with start %v end %v", after.StartedAt, after.EndedAt)
	}

	inflight, ok := second.RunByID(live)
	if !ok {
		t.Fatal("the in-flight run did not survive the restart at all")
	}
	if inflight.Status != StatusError {
		t.Errorf("a run that was %q when the process went away came back %q - "+
			"nothing is going to finish it", StatusRunning, inflight.Status)
	}
	if !strings.Contains(inflight.Note, "restarted") || inflight.EndedAt == nil {
		t.Errorf("note %q ended %v, want a note saying what happened and an end",
			inflight.Note, inflight.EndedAt)
	}
	if inflight.Confirmed != nil {
		t.Errorf("confirmed %v on a run that never reached a verdict", *inflight.Confirmed)
	}

	next, err := second.Enqueue(testAsker, "serenedb", []string{"finding-new"}, "latest")
	if err != nil {
		t.Fatalf("Enqueue after the restart: %v", err)
	}
	if next[0] <= live {
		t.Errorf("the run after the restart got id %d, and %d already exists - "+
			"two runs sharing an id share a log file", next[0], live)
	}
}

// TestAnUnreadableIndexRefusesTheRunner rather than starting empty. An empty
// list is what a runner that has never run anything answers, so a runner
// that starts empty because it could not read its index is telling every
// reader something false, silently, for as long as it runs.
func TestAnUnreadableIndexRefusesTheRunner(t *testing.T) {
	logDir, cacheDir := t.TempDir(), t.TempDir()
	path := filepath.Join(logDir, indexName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewRunner(&fakeStore{art: &store.Artifact{}},
		func(string) (ProjectConfig, bool) { return ProjectConfig{}, true },
		Options{LogDir: logDir, CacheDir: cacheDir})
	if err == nil {
		t.Fatal("a runner started on an index it could not read, and will answer " +
			"an empty run list as if it had never run anything")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the refusal does not name the file to move aside: %v", err)
	}
}

// TestTheIndexIsWrittenAsRunsChange: the file on disk keeps up with the map,
// because the restart it exists for does not announce itself first.
func TestTheIndexIsWrittenAsRunsChange(t *testing.T) {
	logDir, cacheDir := t.TempDir(), t.TempDir()
	r := newIndexRunner(t, logDir, cacheDir)
	path := filepath.Join(logDir, indexName)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a runner with no runs wrote an index anyway")
	}
	ids, err := r.Enqueue(testAsker, "serenedb", []string{"f"}, "latest")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the index was not written at enqueue: %v", err)
	}
	if !strings.Contains(string(b), `"queued"`) {
		t.Errorf("the index does not hold the queued run: %s", b)
	}
	r.update(ids[0], func(run *Run) { run.Status = StatusNotConfirmed })
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), `"not-confirmed"`) {
		t.Errorf("the index still says the old status after a change: %s", b)
	}
	if err := r.IndexError(); err != nil {
		t.Errorf("IndexError = %v on a runner whose writes worked", err)
	}
}
