// Package agentfs hosts a principal's memory as files.
//
// It is the Phase 7 file layer, and it is on top of the Phase 2 memory tools
// rather than under them: memory works whole without ever mounting anything,
// and nothing in the node knows this package exists unless somebody runs
// `flowy fuse`. What it adds is one thing - an agent that writes a file into a
// directory has written to shared memory, indexed and searchable, without
// having been taught a tool.
//
// # The path is the scope
//
//	/<project>/<user>/<type>/<name>
//	/_personal/<user>/<type>/<name>
//
// A directory is not a container here, it is a question the permission filter
// has already answered. /pa is the project pa, and it is listed only if the
// principal may read something in it; /pa/<someone> is that person's memory in
// pa, and it is listed only for the rows the principal may read. _personal is
// the reserved name of the floor - the rows with no project at all - and it
// holds the principal's own memory and nobody else's, because that is what
// personal means and no grant reaches through it.
//
// Writing follows from the same idea. A file lands in the scope its path names:
// under _personal it is personal, under a project it is that project's, and the
// principal may only write under its own user directory and in the project its
// token is for. There is no path that means "promote this": a personal item
// cannot be moved into a project by saving it somewhere else, which is the rule
// mem_write keeps and the reason it is checked twice here - at the door, and
// again inside the transaction that would do the write.
//
// # A callback answers exactly once
//
// Every operation below returns a syscall.Errno, and the go-fuse fs layer turns
// exactly one of those into exactly one reply to the kernel. There is no path
// through this package that returns without one, including the panicking path:
// op() recovers, logs, and answers EIO. A callback that panics takes the mount
// down and leaves the kernel holding a request that will never be answered, and
// a mount that hangs is worse than one that refuses - so no store error is ever
// unwrapped into a callback, and every one of them is mapped to an errno.
package agentfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/deadtrickster/flowy/internal/store"
	"github.com/deadtrickster/flowy/internal/ulid"
)

// Personal is the reserved top-level name for the personal floor: the rows with
// no project. It is not a project and it cannot be one - a project of this name
// is skipped in the listing rather than shadowing the floor, and the mount says
// so in the log if it ever meets one.
const Personal = "_personal"

// opTimeout bounds one store operation behind one callback. A filesystem call
// that never comes back is a process stuck in uninterruptible sleep, so a
// database that has stopped answering becomes an errno rather than a hang.
const opTimeout = 20 * time.Second

// inFlight is how many callbacks may be in the store at once.
//
// go-fuse dispatches a goroutine per request and bounds only the background
// ones (MaxBackground, 12 by default); synchronous reads and writes are not
// bounded at all. So the bound is here, and it is sized to the pool the store
// opens - 16 connections - with room left for the drainer and for whatever else
// this process is doing. Assuming the library is serial is how a filesystem
// ends up with more requests in flight than the database has connections, and
// then the pool, not the disk, is what makes it slow.
const inFlight = 8

// FS is the mounted view of one principal's memory.
type FS struct {
	db  *store.DB
	p   *store.Principal
	log *log.Logger
	sem chan struct{}
	uid uint32
	gid uint32

	// wake nudges the drainer after an enqueue. Buffered and never blocked on:
	// a missed nudge costs a tick, and a blocked callback costs the mount.
	wake chan struct{}

	// mu guards pending, which holds what has been written and not yet applied
	// to the store, keyed by mount path. It is what makes a file readable
	// between the close that wrote it and the drain that stores it - the write
	// is behind, but the file is not.
	mu      sync.Mutex
	pending map[string]*pendingWrite
}

// pendingWrite is one closed file whose intent is still in the queue.
type pendingWrite struct {
	intent   string
	artifact string
	data     []byte
	hash     string
}

// New builds the filesystem for one principal. It does not mount anything.
func New(db *store.DB, p *store.Principal, logger *log.Logger) *FS {
	if logger == nil {
		logger = log.New(os.Stderr, "fuse: ", log.LstdFlags)
	}
	return &FS{
		db:      db,
		p:       p,
		log:     logger,
		sem:     make(chan struct{}, inFlight),
		uid:     uint32(os.Getuid()),
		gid:     uint32(os.Getgid()),
		wake:    make(chan struct{}, 1),
		pending: map[string]*pendingWrite{},
	}
}

// Root is the node to mount.
func (f *FS) Root() fs.InodeEmbedder {
	return &dirNode{f: f, depth: 0, path: ""}
}

// actor is who the events this mount writes are attributed to: the agent when
// the token names one, the person otherwise. It is the same choice mem_write
// makes for the same reason - an agent acting for someone is not that someone.
func (f *FS) actor() string {
	if f.p.AgentID != "" {
		return f.p.AgentID
	}
	return f.p.UserID
}

// ------------------------------------------------------------------ plumbing

// errRefused is a request that was understood and is not allowed. It becomes
// EACCES; store.ErrNotFound becomes ENOENT, and the two are kept apart here
// because a directory the principal may not write to is a fact it may know -
// it can see the directory - while a row it may not read is not.
var errRefused = errors.New("agentfs: refused")

// errUnsupported is an operation this filesystem does not have.
var errUnsupported = errors.New("agentfs: not supported here")

// op runs the store side of one callback: bounded, deadlined, and unable to
// panic its way out.
//
// The deferred recover is the point of it. A panic in a FUSE callback unwinds
// through go-fuse's request loop and takes the mount with it, which leaves
// every process with a file open under the mountpoint stuck on a filesystem
// that has no server - and leaves this callback with no reply sent, which is
// the failure the kernel handles worst. So a panic becomes a log line and EIO,
// exactly one reply, and the mount stays up.
func (f *FS) op(ctx context.Context, what string, fn func(ctx context.Context) error) (errno syscall.Errno) {
	defer func() {
		if r := recover(); r != nil {
			f.log.Printf("%s: panic: %v", what, r)
			errno = syscall.EIO
		}
	}()

	select {
	case f.sem <- struct{}{}:
		defer func() { <-f.sem }()
	case <-ctx.Done():
		return syscall.EINTR
	}

	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	return f.errno(what, fn(opCtx))
}

// errno maps an error to the one the kernel is told. Everything that is not a
// rule of this filesystem is EIO and a log line: a caller cannot act on a
// database error, but whoever runs the mount can.
func (f *FS) errno(what string, err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, store.ErrNotFound):
		return syscall.ENOENT
	case errors.Is(err, errRefused):
		f.log.Printf("%s: %v", what, err)
		return syscall.EACCES
	case errors.Is(err, errUnsupported):
		return syscall.EPERM
	case isNameError(err):
		f.log.Printf("%s: %v", what, err)
		return syscall.EINVAL
	case errors.Is(err, context.DeadlineExceeded):
		f.log.Printf("%s: timed out", what)
		return syscall.ETIMEDOUT
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
	default:
		f.log.Printf("%s: %v", what, err)
		return syscall.EIO
	}
}

func isNameError(err error) bool {
	var ne nameError
	return errors.As(err, &ne)
}

// ino is a stable inode number for a path. The tree is rebuilt from the store
// on every lookup, so the number cannot come from a counter: two lookups of the
// same path have to be the same inode or the kernel treats the second as a
// different file with the same name.
func ino(p string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(p))
	n := h.Sum64()
	// 1 is the root's, and 0 means "no opinion" to go-fuse.
	if n <= 1 {
		n += 2
	}
	return n
}

func (f *FS) dirAttr(out *fuse.Attr) {
	out.Mode = fuse.S_IFDIR | 0o755
	out.Owner = fuse.Owner{Uid: f.uid, Gid: f.gid}
	out.Nlink = 2
}

func (f *FS) fileAttr(out *fuse.Attr, size uint64, mtime time.Time) {
	out.Mode = fuse.S_IFREG | 0o644
	out.Owner = fuse.Owner{Uid: f.uid, Gid: f.gid}
	out.Nlink = 1
	out.Size = size
	if !mtime.IsZero() {
		out.SetTimes(nil, &mtime, &mtime)
	}
}

// writable reports whether the principal may write in this scope. It is the
// whole of the write rule and it is three sentences: your own user directory,
// the personal floor is always yours, and a project is yours only if it is the
// one your token is for - you write where you are, as everywhere else here.
func (f *FS) writable(s store.FSScope) bool {
	if f.p.UserID == "" || s.Owner != f.p.UserID {
		return false
	}
	if s.Project == nil {
		return true
	}
	return *s.Project != "" && *s.Project == f.p.Project
}

// --------------------------------------------------------------- directories

// dirNode is a directory in the mount. There are four kinds of them and the
// only difference is how much of the scope the path has fixed, so it is one
// type with a depth rather than four that differ by a switch statement each.
//
//	0  the root      children: _personal, and every project with something readable in it
//	1  a project     children: the people who own something readable in it
//	2  a person      children: the artifact types the mount hosts
//	3  a type        children: the files
type dirNode struct {
	fs.Inode
	f     *FS
	depth int
	scope store.FSScope
	path  string
}

var (
	_ = (fs.NodeGetattrer)((*dirNode)(nil))
	_ = (fs.NodeLookuper)((*dirNode)(nil))
	_ = (fs.NodeReaddirer)((*dirNode)(nil))
	_ = (fs.NodeCreater)((*dirNode)(nil))
	_ = (fs.NodeUnlinker)((*dirNode)(nil))
	_ = (fs.NodeMkdirer)((*dirNode)(nil))
	_ = (fs.NodeRmdirer)((*dirNode)(nil))
)

func (d *dirNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	d.f.dirAttr(&out.Attr)
	return 0
}

// Mkdir refuses. A directory here is a project, a person or a type: the first
// two are facts about the fabric that a mkdir cannot make true, and the third
// is a fixed list. The directories a principal may write in are all there
// already, so nothing has to be created before a file can be.
func (d *dirNode) Mkdir(_ context.Context, name string, _ uint32, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	d.f.log.Printf("mkdir %s: directories here are scopes, not something to create", path.Join(d.path, name))
	return nil, syscall.EPERM
}

// Rmdir refuses, for the same reason - and explicitly, because the default is
// to succeed, and a directory that reports being removed and is still there on
// the next listing is a lie the kernel repeats.
func (d *dirNode) Rmdir(_ context.Context, _ string) syscall.Errno {
	return syscall.EPERM
}

func (d *dirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	var entries []fuse.DirEntry
	errno := d.f.op(ctx, "readdir "+d.dirPath(), func(ctx context.Context) error {
		var err error
		entries, err = d.entries(ctx)
		return err
	})
	if errno != 0 {
		return nil, errno
	}
	return fs.NewListDirStream(entries), 0
}

func (d *dirNode) dirPath() string {
	if d.path == "" {
		return "/"
	}
	return d.path
}

func (d *dirNode) entries(ctx context.Context) ([]fuse.DirEntry, error) {
	switch d.depth {
	case 0:
		projects, err := d.f.db.FSProjects(ctx, d.f.p)
		if err != nil {
			return nil, err
		}
		// The floor first, then the projects - and a project that has taken the
		// floor's name is dropped rather than shadowing it.
		names := append([]string{Personal}, filterReserved(d.f, d.f.plus(projects, d.f.p.Project))...)
		return dirEntries(d.path, names), nil

	case 1:
		owners, err := d.f.db.FSOwners(ctx, d.f.p, d.scope.Project)
		if err != nil {
			return nil, err
		}
		mine := ""
		if d.f.writable(store.FSScope{Project: d.scope.Project, Owner: d.f.p.UserID}) {
			mine = d.f.p.UserID
		}
		return dirEntries(d.path, d.f.plus(owners, mine)), nil

	case 2:
		return dirEntries(d.path, store.FSTypes), nil

	default:
		arts, err := d.f.db.FSList(ctx, d.f.p, d.scope)
		if err != nil {
			return nil, err
		}
		names := mountNames(arts)
		out := make([]fuse.DirEntry, 0, len(names)+1)
		seen := make(map[string]bool, len(names))
		for i, name := range names {
			seen[name] = true
			out = append(out, fuse.DirEntry{
				Name: name, Mode: fuse.S_IFREG,
				Ino: fileIno(path.Join(d.path, name), arts[i].ID),
			})
		}
		// What has been written and not yet drained is in the directory too:
		// the write is behind, the file is not.
		for name, w := range d.f.pendingIn(d.path) {
			if seen[name] {
				continue
			}
			out = append(out, fuse.DirEntry{
				Name: name, Mode: fuse.S_IFREG,
				Ino: fileIno(path.Join(d.path, name), w.artifact),
			})
		}
		return out, nil
	}
}

// plus appends one name to a sorted list if it is not already in it, and keeps
// the list sorted. It is how a principal's own directory is listed in a scope
// it has never written to: the store has nothing to say about it, and it has to
// be there to be written in.
func (f *FS) plus(names []string, one string) []string {
	if one == "" {
		return names
	}
	for _, n := range names {
		if n == one {
			return names
		}
	}
	names = append(names, one)
	sort.Strings(names)
	return names
}

// filterReserved drops a project that would shadow the personal directory. A
// project may be called anything, and there is exactly one name it cannot have
// here; saying so in the log is better than two directories with one name.
func filterReserved(f *FS, names []string) []string {
	out := names[:0]
	for _, n := range names {
		if n == Personal {
			f.log.Printf("a project is called %q, which is the mount's own name for the personal "+
				"floor; it is not listed - reach it over the API or the memory tools", Personal)
			continue
		}
		out = append(out, n)
	}
	return out
}

func dirEntries(parent string, names []string) []fuse.DirEntry {
	out := make([]fuse.DirEntry, 0, len(names))
	for _, name := range names {
		out = append(out, fuse.DirEntry{
			Name: name, Mode: fuse.S_IFDIR, Ino: ino(path.Join(parent, name)),
		})
	}
	return out
}

func (d *dirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var child *fs.Inode
	errno := d.f.op(ctx, "lookup "+path.Join(d.dirPath(), name), func(ctx context.Context) error {
		var err error
		child, err = d.lookup(ctx, name, out)
		return err
	})
	if errno != 0 {
		return nil, errno
	}
	return child, 0
}

func (d *dirNode) lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, error) {
	if err := checkName(name); err != nil {
		// Not a name this filesystem can hold, so nothing here is called that.
		// ENOENT rather than EINVAL: a lookup is a question about what exists.
		return nil, store.ErrNotFound
	}

	if d.depth == 3 {
		return d.lookupFile(ctx, name, out)
	}

	scope := d.scope
	switch d.depth {
	case 0:
		if name != Personal {
			project := name
			scope.Project = &project
			ok, err := d.reachable(ctx, project)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, store.ErrNotFound
			}
		}
	case 1:
		ok, err := d.knownOwner(ctx, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, store.ErrNotFound
		}
		scope.Owner = name
	case 2:
		if !store.FSTypeOK(name) {
			return nil, store.ErrNotFound
		}
		scope.Type = name
	}

	childPath := path.Join(d.path, name)
	child := &dirNode{f: d.f, depth: d.depth + 1, scope: scope, path: childPath}
	d.f.dirAttr(&out.Attr)
	return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR, Ino: ino(childPath)}), nil
}

// reachable reports whether a project has a directory: the principal's own
// always does, so it can be written in, and any other only if something in it
// is readable.
func (d *dirNode) reachable(ctx context.Context, project string) (bool, error) {
	if project == Personal {
		return false, nil
	}
	if project != "" && project == d.f.p.Project {
		return true, nil
	}
	projects, err := d.f.db.FSProjects(ctx, d.f.p)
	if err != nil {
		return false, err
	}
	for _, p := range projects {
		if p == project {
			return true, nil
		}
	}
	return false, nil
}

func (d *dirNode) knownOwner(ctx context.Context, owner string) (bool, error) {
	if owner != "" && owner == d.f.p.UserID {
		return true, nil
	}
	owners, err := d.f.db.FSOwners(ctx, d.f.p, d.scope.Project)
	if err != nil {
		return false, err
	}
	for _, o := range owners {
		if o == owner {
			return true, nil
		}
	}
	return false, nil
}

// lookupFile resolves one filename in a leaf directory to the row it names.
//
// Two kinds of name reach here. <ULID>.md is a row by id, which is what a row
// that was never given a name of its own is called. Anything else is a name in
// artifacts.file_path, which is what a file written through this mount is
// called - so an agent that writes decisions.md reads decisions.md back rather
// than a ULID it never chose.
func (d *dirNode) lookupFile(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, error) {
	if w := d.f.pendingAt(path.Join(d.path, name)); w != nil {
		return d.fileInode(ctx, name, w.artifact, uint64(len(w.data)), time.Now(), out), nil
	}
	art, err := d.f.resolve(ctx, d.scope, name)
	if err != nil {
		return nil, err
	}
	body := render(art)
	return d.fileInode(ctx, name, art.ID, uint64(len(body)), art.Updated, out), nil
}

func (d *dirNode) fileInode(ctx context.Context, name, id string, size uint64, mtime time.Time, out *fuse.EntryOut) *fs.Inode {
	childPath := path.Join(d.path, name)
	child := &fileNode{f: d.f, scope: d.scope, name: name, id: id, path: childPath}
	d.f.fileAttr(&out.Attr, size, mtime)
	return d.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG, Ino: fileIno(childPath, id)})
}

// resolve finds the artifact one filename names, or ErrNotFound.
func (f *FS) resolve(ctx context.Context, scope store.FSScope, name string) (*store.Artifact, error) {
	if id, ok := idFromName(name); ok {
		return f.db.FSFind(ctx, f.p, scope, id)
	}
	arts, err := f.db.FSList(ctx, f.p, scope)
	if err != nil {
		return nil, err
	}
	names := mountNames(arts)
	for i, n := range names {
		if n == name {
			return arts[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// ------------------------------------------------------------------- writing

// Create is a new file. The id is minted here rather than at the close, so the
// intent that is queued names a row that was decided before anything was
// written - a crash between the two replays one write of one id, not a second
// artifact with the same content.
func (d *dirNode) Create(ctx context.Context, name string, flags, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	var (
		child  *fs.Inode
		handle fs.FileHandle
	)
	errno := d.f.op(ctx, "create "+path.Join(d.dirPath(), name), func(ctx context.Context) error {
		if d.depth != 3 {
			return fmt.Errorf("%w: a file belongs in <project>/<user>/<type>", errUnsupported)
		}
		if err := checkName(name); err != nil {
			return err
		}
		if !d.f.writable(d.scope) {
			return fmt.Errorf("%w: %s is not yours to write in", errRefused, d.dirPath())
		}

		// An existing name is an overwrite of that row, which is what an agent
		// rewriting a file means. A row that is not the principal's own is not
		// overwritten and not reported as anything but refused: it is visible,
		// so its existence is not the secret - being allowed to change it is.
		id := ""
		art, err := d.f.resolve(ctx, d.scope, name)
		switch {
		case err == nil && art.OwnerUser != d.f.p.UserID:
			return fmt.Errorf("%w: %s belongs to somebody else", errRefused, name)
		case err == nil:
			id = art.ID
		case errors.Is(err, store.ErrNotFound):
			if known, ok := idFromName(name); ok {
				// <ULID>.md for a row that is not here. The id is taken from
				// the name so that a file copied out of one mount and into
				// another lands as the same row rather than as a second copy of
				// it. If that id turns out to belong to somebody else, the
				// upsert refuses it - an id is a guess anybody can make, and it
				// is never a capability.
				id = known
			} else {
				id = ulid.NewString()
			}
		default:
			return err
		}

		child = d.fileInode(ctx, name, id, 0, time.Now(), out)
		node, _ := child.Operations().(*fileNode)
		// Not dirty yet. A create is an open, not a write: a shell opening a
		// redirect and closing it again in the parent while the child writes
		// through its own descriptor would otherwise queue an empty item and
		// then the real one. What makes a file dirty is bytes going into it.
		handle = &fileHandle{f: d.f, node: node, writable: true}
		return nil
	})
	if errno != 0 {
		return nil, nil, 0, errno
	}
	// FOPEN_DIRECT_IO: the store is the file, and a page cache in front of it
	// would answer a read with what this mount said last time rather than with
	// what memory says now - including for a row somebody else has just
	// changed, over the API or from another node.
	return child, handle, fuse.FOPEN_DIRECT_IO, 0
}

// Unlink deletes, and it does it now rather than through the queue. A write is
// queued because a close cannot wait for a signed, indexed, transactional
// write and has nothing useful to say if it fails; an unlink has no content to
// dedup, no ordering to keep against itself, and a caller that is entitled to
// be told whether the thing is gone.
func (d *dirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return d.f.op(ctx, "unlink "+path.Join(d.dirPath(), name), func(ctx context.Context) error {
		if d.depth != 3 {
			return fmt.Errorf("%w: there are no files here", errUnsupported)
		}
		if !d.f.writable(d.scope) {
			return fmt.Errorf("%w: %s is not yours to write in", errRefused, d.dirPath())
		}
		here := path.Join(d.path, name)
		art, err := d.f.resolve(ctx, d.scope, name)
		if errors.Is(err, store.ErrNotFound) {
			// Nothing in the store. It may still be a file: one that was closed
			// and has not been drained yet. Deleting that one is taking the
			// write off the queue, and it has to be, or the item the caller just
			// deleted would appear a second later.
			queued := d.f.pendingAt(here)
			if queued == nil {
				return err
			}
			d.f.dropPending(here)
			if _, cancelErr := d.f.db.CancelFSIntents(ctx, queued.artifact, d.f.p.UserID); cancelErr != nil {
				return cancelErr
			}
			// The drainer may have got there between the read above and the
			// cancel. If it did, the row is here now and this is an ordinary
			// delete of it; if it did not, there is nothing to delete and the
			// cancel was the whole of it.
			if _, err := d.f.db.TombstoneArtifact(ctx, d.f.p, queued.artifact); err != nil &&
				!errors.Is(err, store.ErrNotFound) {
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		d.f.dropPending(here)
		if _, err := d.f.db.TombstoneArtifact(ctx, d.f.p, art.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Readable and not the caller's to delete.
				return fmt.Errorf("%w: %s is not yours to delete", errRefused, name)
			}
			return err
		}
		// A queued write for the row that was just deleted is dropped rather
		// than applied: the store would refuse it as superseded anyway - a
		// delete is not undone by a write that was in flight when it happened -
		// and taking it off the queue says so where somebody can see it.
		if _, err := d.f.db.CancelFSIntents(ctx, art.ID, d.f.p.UserID); err != nil {
			return err
		}
		return nil
	})
}

// ---------------------------------------------------------- the pending cache

func (f *FS) pendingAt(p string) *pendingWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pending[p]
}

// pendingIn returns the queued writes in one directory, by filename.
func (f *FS) pendingIn(dir string) map[string]*pendingWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]*pendingWrite{}
	for p, w := range f.pending {
		if path.Dir(p) == dir {
			out[path.Base(p)] = w
		}
	}
	return out
}

func (f *FS) putPending(p string, w *pendingWrite) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending[p] = w
}

func (f *FS) dropPending(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.pending[p]; !ok {
		return false
	}
	delete(f.pending, p)
	return true
}

// settled is called by the drainer when an intent has been applied: the store
// now holds these bytes, so the cache entry is not the newer truth any more. An
// entry written again since - a second close, a second intent - is left alone.
func (f *FS) settled(intent string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p, w := range f.pending {
		if w.intent == intent {
			delete(f.pending, p)
			return
		}
	}
}

// Wake nudges the drainer. It never blocks: the channel holds one nudge and one
// is enough, because the drainer drains everything it finds.
func (f *FS) Wake() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// ------------------------------------------------------------- the enqueue

// commit is what a close(2) on a written file does: work out what row the file
// is, write the intent down, and answer. The store write happens afterwards,
// in the drainer, out of the caller's way - see fs_intents in schema.sql.
func (f *FS) commit(ctx context.Context, n *fileNode, data []byte) error {
	if !f.writable(n.scope) {
		return fmt.Errorf("%w: %s is not yours to write in", errRefused, path.Dir(n.path))
	}
	if err := checkName(n.name); err != nil {
		return err
	}
	// The body goes into a text column. Bytes that are not text cannot, and
	// finding that out here means the agent is told on the close rather than
	// the drainer failing forever on a row nobody can see.
	if !utf8.Valid(data) {
		return nameError{"content that is not valid UTF-8; a memory item is text"}
	}

	d := parse(data)
	if strings.TrimSpace(d.Title) == "" && strings.TrimSpace(d.Body) == "" {
		return nameError{"an empty file; a memory item needs a title or a body, and unlink is how one goes away"}
	}

	// Parsed and checked here as well as in the drainer, so a file with a kind
	// or a scope that is not one gets an error on the close rather than sitting
	// in the queue being refused by something the agent cannot see.
	if _, err := fieldsOf(n.scope.Type, d); err != nil {
		return err
	}

	// What is in the store now, if anything: it decides whether this is a
	// create or an edit, and an edit that says nothing about scope keeps the
	// scope the row has.
	held, err := f.db.FSFind(ctx, f.p, n.scope, n.id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		held = nil
	case err != nil:
		return err
	case held.OwnerUser != f.p.UserID:
		return fmt.Errorf("%w: %s belongs to somebody else", errRefused, n.name)
	}

	visibility, err := f.visibilityFor(n.scope, d.Scope, held)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	intent := &store.FSIntent{
		// Minted here rather than left to the enqueue, because the cache entry
		// below has to name it before the row exists: the drainer can pick an
		// intent up the instant it commits, and a cache entry that learned its
		// intent id afterwards would be one the drainer had already finished
		// with - and would sit in front of the store for the life of the mount.
		ID:         ulid.NewString(),
		Artifact:   n.id,
		Path:       n.path,
		OwnerUser:  f.p.UserID,
		Actor:      f.actor(),
		Project:    n.scope.Project,
		Type:       n.scope.Type,
		Visibility: visibility,
		Name:       n.name,
		Hash:       hash,
		Content:    string(data),
	}
	// The cache entry goes in before the row, so a read that arrives between
	// the two sees the new bytes rather than the old ones. An enqueue that then
	// fails takes it out again.
	f.putPending(n.path, &pendingWrite{
		intent: intent.ID, artifact: n.id, hash: hash,
		data: append([]byte(nil), data...),
	})
	if err := f.db.EnqueueFSIntent(ctx, intent); err != nil {
		f.dropPending(n.path)
		return err
	}
	f.Wake()
	return nil
}

// fieldsOf is a parsed file as the columns it will be written to. It is what
// the drainer applies and what a close validates, so the write that is queued
// is the write that will land.
func fieldsOf(artifactType string, d doc) (store.FSFields, error) {
	kind, err := kindFor(artifactType, d.Kind)
	if err != nil {
		return store.FSFields{}, err
	}
	return store.FSFields{Title: d.Title, Body: d.Body, Kind: kind, Tags: d.Tags}, nil
}

// kindFor validates the kind a file asked for. A misspelled kind is refused
// rather than defaulted, for the reason mem_write refuses one: defaulting it
// writes something the file did not say, and the file is the only record of
// what was meant.
func kindFor(artifactType, kind string) (string, error) {
	if artifactType != "memory" {
		// A note is a note. Only memory is narrowed by kind.
		return "", nil
	}
	if kind == "" {
		return "note", nil
	}
	for _, k := range memKinds {
		if k == kind {
			return kind, nil
		}
	}
	return "", nameError{fmt.Sprintf("kind %q; it must be one of %s",
		kind, strings.Join(memKinds, ", "))}
}

// memKinds are what a memory item can be. It is the same list the memory tools
// offer, and it is short enough to be worth a second copy rather than an export
// of the tool layer into this one - the tools are in package main, and this
// package is under them.
var memKinds = []string{"note", "todo", "feature", "handoff"}

// visibilityFor decides what the row's visibility will be, from the directory
// the file is in and the one line of front matter that is allowed to narrow or
// widen it inside that directory.
//
// The path is the authority and the header cannot argue with it. Under
// _personal the scope is personal, and a header saying anything else is refused
// rather than ignored: a file that says shared and is stored personal has lied
// to whoever wrote it, and a file that says personal and is stored shared is
// the promotion this whole design is built to make impossible. Inside a
// project, project-only and shared are both that project - the header chooses
// between them, and project-only is what it gets by default, because the
// narrower of two scopes is the one to take when nobody said.
func (f *FS) visibilityFor(s store.FSScope, headerScope string, held *store.Artifact) (string, error) {
	if headerScope != "" {
		known := false
		for _, scope := range store.MemScopes {
			if scope == headerScope {
				known = true
			}
		}
		if !known {
			return "", nameError{fmt.Sprintf("scope %q; it must be one of %s",
				headerScope, strings.Join(store.MemScopes, ", "))}
		}
	}

	if s.Project == nil {
		if headerScope != "" && headerScope != "personal" {
			return "", fmt.Errorf("%w: this file is under %s, so it is personal; "+
				"a scope of %q would move it out of the personal floor, and moving it "+
				"is something to do with mem_write and not by saving a file",
				errRefused, Personal, headerScope)
		}
		return store.VisibilityPersonal, nil
	}

	switch headerScope {
	case "personal":
		return "", fmt.Errorf("%w: this file is in project %s; a scope of personal would take it "+
			"out of the project, which is not something a save does", errRefused, *s.Project)
	case "":
		if held != nil {
			// An edit that says nothing keeps what the row has, verbatim.
			return "", nil
		}
		return store.VisibilityProjectOnly, nil
	default:
		return store.VisibilityForScope(headerScope), nil
	}
}
