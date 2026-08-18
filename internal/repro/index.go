package repro

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The run index: what GET /runs answers after this process has been
// restarted.
//
// A VERDICT IS DURABLE THE MOMENT IT IS RECORDED - it is a signed event on
// the finding, in the store, and it survives anything that happens to this
// binary. THE RUN IS NOT THE VERDICT. Its id, its status, its three times,
// which project it was for and where its log file is lived only in the
// runner's map, so a restart left a finding that had genuinely been run
// reading as never run: the console's runs drawer asks this process, this
// process answered an empty list, and an empty list is what a finding nobody
// has ever run looks like too. A display that cannot tell a fact from its
// absence is the defect, not the lost bytes.
//
// The index is one JSON file beside the logs, rewritten whole under the
// runner's lock and renamed into place. Whole rather than appended because
// the number here is tens of runs and not millions: a rewrite has no replay,
// no compaction and no half-written tail to parse around, and the price of
// being wrong about that is a few kilobytes per status change. The logs
// themselves already survive - they are files in the same directory - so it
// was only ever the index that did not.
const indexName = "runs.json"

// indexFile is the on-disk shape. Next is carried explicitly rather than
// recomputed from the highest id: a run that was accepted and then trimmed
// away by a future retention pass must still not have its id handed out
// twice, because the log file is named after the id.
type indexFile struct {
	Next int64 `json:"next"`
	Runs []Run `json:"runs"`
}

// restartNote is what a run that was in flight when the process went away is
// told about itself. It says the containers may still be there because they
// may: this process cancels its runs on Stop, but it is not around to cancel
// anything after a kill -9 or a machine reboot, and a person reading the run
// deserves to know there is possibly something to clean up.
const restartNote = "the runner restarted while this run was in flight, so nothing " +
	"here knows how it ended - its containers, if any, were left behind"

func (r *Runner) indexPath() string { return filepath.Join(r.opt.LogDir, indexName) }

// loadIndex reads the index into the runner. It is called from NewRunner,
// before any worker exists, so it takes no lock.
//
// AN INDEX THAT EXISTS AND CANNOT BE READ REFUSES THE RUNNER rather than
// starting it empty. Starting empty would answer every reader with exactly
// the empty list this file exists to stop being a lie, and it would do it
// silently and forever; refusing names the path, and moving that path aside
// is a deliberate act by somebody who has read the error.
//
// A RESTORED RUN IS TERMINAL, ALWAYS. Run.principal is deliberately not
// persisted - it is a capability, and a capability written to disk beside
// its logs is a worse bug than the one being fixed here - so a restored run
// has nobody to read findings or sign a verdict as, and a run that cannot
// name who it is for must never execute. Everything non-terminal therefore
// comes back as an error saying what happened to it, which is an answer; the
// status it was frozen at would be a claim that is no longer true.
func (r *Runner) loadIndex() error {
	b, err := os.ReadFile(r.indexPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("repro: read the run index %s: %w", r.indexPath(), err)
	}
	var f indexFile
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("repro: the run index %s does not parse (%w) - move it "+
			"aside to start from an empty one, and know that the runs it held "+
			"will not come back", r.indexPath(), err)
	}
	now := time.Now().UTC()
	for i := range f.Runs {
		run := f.Runs[i]
		if !run.Status.Terminal() {
			run.Status = StatusError
			run.Note = restartNote
			run.Confirmed = nil
			if run.EndedAt == nil {
				ended := now
				run.EndedAt = &ended
			}
		}
		r.runs[run.ID] = &run
		if run.ID > r.next {
			r.next = run.ID
		}
	}
	if f.Next > r.next {
		r.next = f.Next
	}
	return nil
}

// saveIndex writes the index out. THE CALLER HOLDS r.mu - every write to a
// run record already happens under that lock (accept and update are the only
// two), so this rides along with them and there is no window where the map
// and the file disagree with a reader in between.
//
// A failure to write is kept on the runner rather than returned, because
// every caller is a worker mid-run with nobody to report to and a run that
// executes without being indexed is still better than a run refused over its
// bookkeeping. It is kept and not swallowed: IndexError is served beside the
// run list, so the failure is visible while the runs are still in memory -
// which is the only time anybody can do anything about it.
func (r *Runner) saveIndex() {
	f := indexFile{Next: r.next, Runs: make([]Run, 0, len(r.runs))}
	for _, run := range r.runs {
		f.Runs = append(f.Runs, run.clone())
	}
	sort.Slice(f.Runs, func(i, j int) bool { return f.Runs[i].ID < f.Runs[j].ID })
	r.indexErr = writeIndex(r.indexPath(), f)
}

// writeIndex writes and renames. The temp file is created in the same
// directory so the rename is within one filesystem, and it is synced before
// the rename so what lands is the whole file or the old one - a torn index
// is the corrupt-index case above, and this is the cheap way not to make it.
func writeIndex(path string, f indexFile) error {
	b, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return fmt.Errorf("repro: encode the run index: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), indexName+".*")
	if err != nil {
		return fmt.Errorf("repro: open a temporary run index: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("repro: write the run index: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("repro: sync the run index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("repro: close the run index: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("repro: put the run index in place: %w", err)
	}
	return nil
}

// IndexError is the last failure to write the run index, or nil. A runner
// answering runs while this is set is a runner whose next restart will
// forget them.
func (r *Runner) IndexError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.indexErr
}
