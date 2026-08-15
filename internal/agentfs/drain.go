package agentfs

// The drainer: the half of a write that happens after the close.
//
// A close(2) on a file in the mount writes an intent down and returns. This is
// what turns that intent into the fabric row it describes - the artifact, the
// event that records the write, the tsvector that makes it searchable - and
// marks the intent applied, all in one transaction.
//
// The properties it is built for, in the order they matter:
//
//   - at-least-once. An intent stays pending until the transaction that applies
//     it commits. A node that dies between the close and the write comes back
//     with the work still in the queue.
//   - reconcile on startup. Which is what makes the line above worth anything:
//     the first thing a mount does, before it serves a single callback, is
//     replay whatever the last run left behind.
//   - exactly one write. At-least-once means an intent can be applied twice
//     across a crash, so the apply is deduped by hash against the last intent
//     applied for the same row - see store.ApplyFSIntent. Twice through the
//     queue, once in the store, and one event rather than two.
//   - no partial. The artifact, its event and the applied stamp are one
//     transaction. There is no state in between for a crash to leave behind.

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/deadtrickster/flowy/internal/store"
)

// drainBatch is how many intents one pass takes at a time. The queue is a
// person typing into files, so it is short; the batch is there so that a queue
// that is not short does not become one enormous read.
const drainBatch = 200

// drainEvery is how often the drainer looks without being nudged. The nudge
// from an enqueue is what normally wakes it - this is the backstop for a nudge
// that was dropped because one was already waiting, and for intents another
// process left behind.
const drainEvery = 2 * time.Second

// Drainer applies queued intents to the store.
type Drainer struct {
	db  *store.DB
	log *log.Logger
	// fs is the mount whose pending cache is cleared as intents land. It is nil
	// for `flowy fuse --reconcile`, which drains a queue with nothing mounted.
	fs *FS
}

// DrainStats counts one pass, by what happened to each intent.
type DrainStats struct {
	Applied    int
	Duplicate  int
	Superseded int
	Refused    int
}

// Total is how many intents the pass took off the queue.
func (s DrainStats) Total() int { return s.Applied + s.Duplicate + s.Superseded + s.Refused }

func (s DrainStats) String() string {
	return fmt.Sprintf("applied %d, duplicate %d, superseded %d, refused %d",
		s.Applied, s.Duplicate, s.Superseded, s.Refused)
}

// NewDrainer builds a drainer for a queue with no mount in front of it, which
// is what a reconcile at startup is.
func NewDrainer(db *store.DB, logger *log.Logger) *Drainer {
	if logger == nil {
		logger = log.New(os.Stderr, "fuse: ", log.LstdFlags)
	}
	return &Drainer{db: db, log: logger}
}

// Drainer returns the drainer for this mount: the same thing, plus the pending
// cache to clear as writes land.
func (f *FS) Drainer() *Drainer {
	return &Drainer{db: f.db, log: f.log, fs: f}
}

// Reconcile applies everything in the queue and returns what it did. It is
// called at startup - before the mount serves anything - and by Run.
//
// An error stops it where it is, with whatever it has applied already applied:
// the rest is still pending, which is the state the whole design is built
// around, so there is nothing to unwind and nothing to remember.
func (d *Drainer) Reconcile(ctx context.Context) (DrainStats, error) {
	var stats DrainStats
	for {
		pending, err := d.db.PendingFSIntents(ctx, drainBatch)
		if err != nil {
			return stats, err
		}
		for _, in := range pending {
			result, err := d.apply(ctx, in)
			if err != nil {
				return stats, err
			}
			switch result {
			case store.FSApplied:
				stats.Applied++
			case store.FSDuplicate:
				stats.Duplicate++
			case store.FSSuperseded:
				stats.Superseded++
			default:
				stats.Refused++
			}
		}
		if len(pending) < drainBatch {
			return stats, nil
		}
	}
}

// apply is one intent. The content is parsed here rather than in the store,
// because the file format is this package's business and the store's job is to
// write columns transactionally - and because a replay parses the bytes that
// were written rather than trusting a parse that was recorded beside them.
func (d *Drainer) apply(ctx context.Context, in *store.FSIntent) (store.FSApplyResult, error) {
	fields, err := fieldsOf(in.Type, parse([]byte(in.Content)))
	if err != nil {
		// The file cannot be turned into an item, and it will never be able to
		// be: retrying it forever would stop everything behind it in the queue.
		// So it is marked refused and said out loud.
		d.log.Printf("drain %s: %v; the file was not stored", in.Path, err)
		if markErr := d.markUnstorable(ctx, in); markErr != nil {
			return store.FSRefused, markErr
		}
		return store.FSRefused, nil
	}

	result, err := d.db.ApplyFSIntent(ctx, in, fields)
	if err != nil {
		return store.FSRefused, err
	}
	switch result {
	case store.FSApplied, store.FSDuplicate:
	case store.FSSuperseded:
		d.log.Printf("drain %s: the item was deleted after the file was written; "+
			"the write is dropped rather than bringing it back", in.Path)
	default:
		d.log.Printf("drain %s: refused; the row is not %s's to write, or the write "+
			"would move it between a project and the personal floor", in.Path, in.OwnerUser)
	}
	if d.fs != nil {
		d.fs.settled(in.ID)
	}
	return result, nil
}

// markUnstorable takes an intent out of the queue without writing anything.
func (d *Drainer) markUnstorable(ctx context.Context, in *store.FSIntent) error {
	if err := d.db.DropFSIntent(ctx, in.ID); err != nil {
		return err
	}
	if d.fs != nil {
		d.fs.settled(in.ID)
	}
	return nil
}

// Run drains until ctx is done: on a nudge from an enqueue, and on a tick for
// the nudges that were dropped because one was already waiting.
func (d *Drainer) Run(ctx context.Context, wake <-chan struct{}) {
	ticker := time.NewTicker(drainEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// One last pass, off the cancelled context, so a write that was
			// closed a moment before the unmount is not left in the queue for a
			// restart that may never come. It has until the shutdown deadline.
			last, cancel := context.WithTimeout(context.Background(), opTimeout)
			if stats, err := d.Reconcile(last); err != nil {
				d.log.Printf("drain on the way out: %v", err)
			} else if stats.Total() > 0 {
				d.log.Printf("drain on the way out: %s", stats)
			}
			cancel()
			return
		case <-wake:
		case <-ticker.C:
		}

		if stats, err := d.Reconcile(ctx); err != nil {
			// Nothing is lost: what did not apply is still pending, and the
			// next tick tries again.
			d.log.Printf("drain: %v", err)
		} else if stats.Total() > 0 {
			d.log.Printf("drain: %s", stats)
		}
	}
}
