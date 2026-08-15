package agentfs

// Mounting, unmounting, and the one thing to check before believing either.

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/deadtrickster/flowy/internal/store"
)

// The FUSE protocol level this mount will serve on, and no lower.
//
// It is not a guess and it is not configuration: go-fuse's own INIT handler
// refuses anything below 7.12 (fuse/opcode.go), and it refuses it by failing
// the INIT request - which leaves a mountpoint that exists, answers nothing,
// and has to be diagnosed by somebody. So the negotiated version is read back
// off the server after the mount and checked here, where the answer can be a
// sentence rather than an EIO. Ask for what you use and then look at what you
// were given; a config flag saying which protocol you wanted is not the same
// statement.
const (
	protoMajor = 7
	protoMinor = 12
)

// maxWrite is the biggest write the kernel will send in one request. Memory
// items are prose, and a page at a time would turn one save into a hundred
// callbacks; 128 KiB is what the kernel will do without MAX_PAGES and is plenty
// for a file that has to fit in a text column.
const maxWrite = 128 << 10

// unmountTries is how many times an unmount is attempted before giving up. A
// mountpoint whose directory somebody is still sitting in comes back EBUSY, and
// a second attempt a moment later usually finds them gone.
const unmountTries = 5

// Options are the choices `flowy fuse` passes down.
type Options struct {
	// Mountpoint is the directory the filesystem appears at. It has to exist
	// and be a directory: creating it here would leave a directory behind on
	// every failed mount.
	Mountpoint string
	// Debug turns on go-fuse's protocol trace, on the logger below.
	Debug bool
	// Logger is where the mount says what it is doing. Nil means stderr.
	Logger *log.Logger
}

// Mounted is a live filesystem: the tree, the server the kernel talks to, and
// where it is attached.
type Mounted struct {
	FS     *FS
	Server *fuse.Server
	Dir    string
	log    *log.Logger
}

// Mount attaches the filesystem to a directory and returns once the kernel has
// finished the handshake. It does not start the drainer - see Serve.
func Mount(db *store.DB, p *store.Principal, opts Options) (*Mounted, error) {
	logger := opts.Logger
	if logger == nil {
		logger = log.New(os.Stderr, "fuse: ", log.LstdFlags)
	}
	info, err := os.Stat(opts.Mountpoint)
	if err != nil {
		return nil, fmt.Errorf("mountpoint %s: %w", opts.Mountpoint, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("mountpoint %s is not a directory", opts.Mountpoint)
	}

	f := New(db, p, logger)

	// Every timeout is zero on purpose. The store is the filesystem, and it
	// changes underneath this process all the time - another agent's mem_write,
	// the API, a merge from a peer - so a cached entry or a cached attribute is
	// this mount telling the kernel something that was true a second ago. The
	// cost is a lookup per access, which is a query the node was going to serve
	// anyway.
	zero := time.Duration(0)
	fsOpts := &fs.Options{
		EntryTimeout:    &zero,
		AttrTimeout:     &zero,
		NegativeTimeout: &zero,
		// The files belong to whoever mounted this, because they are that
		// principal's memory and nobody else on this machine has a token for
		// them. Without it everything is owned by root and the mounting user
		// cannot write to their own memory.
		UID:    f.uid,
		GID:    f.gid,
		Logger: logger,
	}
	fsOpts.MountOptions = fuse.MountOptions{
		FsName: "flowy",
		Name:   "flowy",
		// Private by default, and this is where that is enforced rather than
		// promised: without allow_other the kernel refuses every other uid,
		// root included, before a callback here is reached.
		AllowOther: false,
		MaxWrite:   maxWrite,
		// go-fuse is threaded: it runs a goroutine per request. Saying so out
		// loud rather than leaving the zero value, because the bound on how
		// many of those may be in the store at once (inFlight) is sized to this
		// answer, and a library that turned out to be serial would make that
		// bound a lie in the safe direction and this one a lie in the other.
		SingleThreaded: false,
		MaxBackground:  inFlight * 2,
		// There are no extended attributes here. Saying so stops the kernel
		// asking on every file operation, which on a store-backed filesystem is
		// a round trip to answer "no".
		DisableXAttrs: true,
		Debug:         opts.Debug,
		Logger:        logger,
	}

	server, err := fs.Mount(opts.Mountpoint, f.Root(), fsOpts)
	if err != nil {
		return nil, fmt.Errorf("mount %s: %w", opts.Mountpoint, err)
	}

	m := &Mounted{FS: f, Server: server, Dir: opts.Mountpoint, log: logger}
	if err := m.checkProtocol(); err != nil {
		// Nothing has been served yet, and a mount that cannot do what this
		// filesystem needs is not left attached for somebody to trip over.
		if unmountErr := m.Unmount(); unmountErr != nil {
			logger.Printf("unmount after a failed handshake: %v", unmountErr)
		}
		return nil, err
	}
	return m, nil
}

// checkProtocol reads back what the kernel actually negotiated.
func (m *Mounted) checkProtocol() error {
	settings := m.Server.KernelSettings()
	if !settings.SupportsVersion(protoMajor, protoMinor) {
		return fmt.Errorf("the kernel negotiated FUSE %d.%d and this mount needs %d.%d or newer",
			settings.Major, settings.Minor, protoMajor, protoMinor)
	}
	m.log.Printf("mounted %s: FUSE %d.%d, init flags %#x, max write %d",
		m.Dir, settings.Major, settings.Minor, settings.Flags64(), maxWrite)
	return nil
}

// Serve runs the mount until ctx is done or the filesystem is unmounted from
// outside, draining the queue before it serves anything.
//
// The reconcile is first and it is not optional. It is the whole reason a close
// is allowed to return before the store has the write: whatever the last run
// did not finish is finished here, before this run can add to it.
func (m *Mounted) Serve(ctx context.Context, drain bool) error {
	// The drainer's own context, so it can be stopped when the mount goes away
	// for a reason that is not this one - somebody running fusermount -u.
	drainCtx, stopDraining := context.WithCancel(ctx)
	defer stopDraining()

	drained := make(chan struct{})
	if drain {
		drainer := m.FS.Drainer()
		stats, err := drainer.Reconcile(ctx)
		if err != nil {
			close(drained)
			return fmt.Errorf("reconcile the write queue: %w", err)
		}
		if stats.Total() > 0 {
			m.log.Printf("reconciled the queue left by the last run: %s", stats)
		}
		go func() {
			defer close(drained)
			drainer.Run(drainCtx, m.FS.wake)
		}()
	} else {
		close(drained)
		m.log.Printf("--no-drain: writes are queued and nothing applies them; " +
			"run `flowy fuse --reconcile` to store them")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Server.Wait()
	}()

	var err error
	select {
	case <-done:
		// Somebody unmounted it from outside - fusermount -u, or the machine
		// going down around us.
	case <-ctx.Done():
		err = m.Unmount()
	}

	// And then wait for the drainer to finish its last pass before returning,
	// because the caller closes the database as soon as this comes back: a pass
	// that is still running would fail on a closed pool and leave a write in the
	// queue that had already been paid for.
	stopDraining()
	select {
	case <-drained:
	case <-time.After(opTimeout + drainEvery):
		m.log.Printf("the drainer did not finish; what it had not applied is still queued")
	}
	return err
}

// Unmount detaches the filesystem. A mountpoint somebody is still using is
// EBUSY, and that is a reason to wait and try again rather than to leave a
// half-shut server behind.
func (m *Mounted) Unmount() error {
	var err error
	for attempt := 1; attempt <= unmountTries; attempt++ {
		if err = m.Server.Unmount(); err == nil {
			m.log.Printf("unmounted %s", m.Dir)
			return nil
		}
		m.log.Printf("unmount %s: %v (attempt %d of %d)", m.Dir, err, attempt, unmountTries)
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("unmount %s: %w", m.Dir, err)
}
