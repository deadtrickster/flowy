package agentfs

// One file is one artifact. Reads go to the store through the same permission
// filter every other read here goes through; writes go into a buffer and, on
// the close that ends them, into the queue.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/deadtrickster/flowy/internal/store"
)

// fileNode is one artifact, seen as a file. id is fixed for the life of the
// inode and is part of the inode's identity - see fileIno - so a node can never
// be the node for a name whose row has been replaced underneath it.
type fileNode struct {
	fs.Inode
	f     *FS
	scope store.FSScope
	name  string
	id    string
	path  string

	// mu guards the handles open on this node and the truncate below.
	mu   sync.Mutex
	open map[*fileHandle]struct{}
	// pendingTrunc is a truncate that arrived with no open handle to apply it
	// to. See Setattr for why one can.
	pendingTrunc *truncMark
}

// truncMark is a size the file was cut to, waiting for the open that will read
// it. It carries the time because it is only ever the other half of an
// open(O_TRUNC) that the kernel split in two, and those two arrive in the same
// breath - a mark older than that is not part of one and is not honoured.
type truncMark struct {
	size int
	at   time.Time
}

// truncWindow is how long a handle-less truncate is remembered for.
const truncWindow = 5 * time.Second

var (
	_ = (fs.NodeGetattrer)((*fileNode)(nil))
	_ = (fs.NodeSetattrer)((*fileNode)(nil))
	_ = (fs.NodeOpener)((*fileNode)(nil))
)

// fileIno is the inode number of one file. The id is in it as well as the path,
// which matters exactly once and matters a lot: a file that is deleted and
// written again under the same name is a different row, and an inode number
// that did not say so would let the kernel hand the new file's writes to a node
// still pointing at the tombstoned one.
func fileIno(p, id string) uint64 { return ino(p + "\x00" + id) }

func (n *fileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := fh.(*fileHandle); ok && h != nil {
		h.mu.Lock()
		size := uint64(len(h.data))
		h.mu.Unlock()
		n.f.fileAttr(&out.Attr, size, time.Now())
		return 0
	}
	return n.f.op(ctx, "getattr "+n.path, func(ctx context.Context) error {
		data, mtime, err := n.content(ctx)
		if err != nil {
			return err
		}
		n.f.fileAttr(&out.Attr, uint64(len(data)), mtime)
		return nil
	})
}

// Setattr takes a truncate and ignores the rest.
//
// The rest is chmod, chown and utimes, and every one of them is a lie this
// filesystem would have to keep: the mode is fixed, the owner is whoever
// mounted it, and the times come off the row. Refusing them would be honest and
// would also make cp, install and every editor that saves through a temp file
// print an error over a write that worked. So they are accepted and dropped,
// and the attributes that come back are the real ones.
//
// The truncate is where knowing the library matters. go-fuse does not put
// FUSE_ATOMIC_O_TRUNC in the flags it agrees to at INIT, so the kernel does not
// pass O_TRUNC through to Open: it opens the file and then sends a separate
// SETATTR with the size and, on the kernels measured here, without a file
// handle. Believing the documented shape of open(O_TRUNC) instead of reading
// the trace is how a rewrite through this mount ends up with the tail of the
// previous content still on the end of it - the buffer was loaded whole and
// nothing ever cut it. So a size arrives one of three ways and all three are
// handled: on the handle it names, on the handles this node already has open,
// and - for a kernel that sends it the other way round - as a mark for the open
// that is about to happen.
func (n *fileNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if size, ok := in.GetSize(); ok {
		switch h, isHandle := fh.(*fileHandle); {
		case isHandle && h != nil:
			h.truncate(int(size))
		case n.truncateOpen(int(size)):
		default:
			n.markTruncate(int(size))
		}
	}
	return n.Getattr(ctx, fh, out)
}

// truncateOpen cuts every handle open on this node, and says whether there were
// any.
func (n *fileNode) truncateOpen(size int) bool {
	n.mu.Lock()
	handles := make([]*fileHandle, 0, len(n.open))
	for h := range n.open {
		handles = append(handles, h)
	}
	n.mu.Unlock()
	for _, h := range handles {
		h.truncate(size)
	}
	return len(handles) > 0
}

func (n *fileNode) markTruncate(size int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pendingTrunc = &truncMark{size: size, at: time.Now()}
}

// takeTruncate reads and clears a mark, if there is a fresh one.
func (n *fileNode) takeTruncate() (int, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	mark := n.pendingTrunc
	n.pendingTrunc = nil
	if mark == nil || time.Since(mark.at) > truncWindow {
		return 0, false
	}
	return mark.size, true
}

func (n *fileNode) track(h *fileHandle) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.open == nil {
		n.open = map[*fileHandle]struct{}{}
	}
	n.open[h] = struct{}{}
}

func (n *fileNode) forget(h *fileHandle) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.open, h)
}

// content is what a read of this file returns right now: the bytes waiting in
// the queue if this file has been written and not yet drained, and the row
// otherwise.
func (n *fileNode) content(ctx context.Context) ([]byte, time.Time, error) {
	if w := n.f.pendingAt(n.path); w != nil && w.artifact == n.id {
		return w.data, time.Now(), nil
	}
	art, err := n.f.db.FSFind(ctx, n.f.p, n.scope, n.id)
	if err != nil {
		return nil, time.Time{}, err
	}
	return render(art), art.Updated, nil
}

func (n *fileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	var handle *fileHandle
	errno := n.f.op(ctx, "open "+n.path, func(ctx context.Context) error {
		wantsWrite := int(flags)&(syscall.O_WRONLY|syscall.O_RDWR) != 0
		if wantsWrite && !n.f.writable(n.scope) {
			return fmt.Errorf("%w: %s is not yours to write in", errRefused, path.Dir(n.path))
		}
		// An openspec row is not rewritten through the generic file path - the
		// mount shows, the doors write; see the same refusal in Create. This is
		// the one that keeps a spec a spec: a save without the front-matter
		// header defaults the kind to "note" and the shape check only sees the
		// row, so without this refusal the rewrite would husk it silently.
		if wantsWrite {
			art, err := n.f.db.FSFind(ctx, n.f.p, n.scope, n.id)
			switch {
			case errors.Is(err, store.ErrNotFound):
				// A pending write: not in the store yet, and nothing openspec
				// to refuse - the close-time checks decide.
			case err != nil:
				return err
			case store.IsOpenspec(art):
				return fmt.Errorf("%w: %s is an openspec %s and read-only in the mount - "+
					"POST /api/openspec writes it", errRefused, n.name, art.Kind)
			}
		}
		handle = &fileHandle{f: n.f, node: n, writable: wantsWrite}

		if wantsWrite && int(flags)&syscall.O_TRUNC != 0 {
			// Emptied for whatever is about to be written into it. The buffer
			// starts empty and the handle is not dirty until something is
			// written: an open that truncates and closes with nothing written
			// is not a write, and an item is not emptied by opening it - unlink
			// is how one goes away.
			return nil
		}
		data, _, err := n.content(ctx)
		if err != nil {
			return err
		}
		handle.data = append([]byte(nil), data...)
		if size, ok := n.takeTruncate(); ok && size < len(handle.data) {
			// The truncate half of an open(O_TRUNC) the kernel sent first.
			handle.data = handle.data[:size]
		}
		if int(flags)&syscall.O_APPEND != 0 {
			handle.appending = true
		}
		return nil
	})
	if errno != 0 {
		return nil, 0, errno
	}
	n.track(handle)
	// Direct IO, for the reason given in Create: the store is the file, and a
	// page cache between them would answer with what this mount said last.
	return handle, fuse.FOPEN_DIRECT_IO, 0
}

// fileHandle is one open file. The bytes live here until the close.
type fileHandle struct {
	f    *FS
	node *fileNode

	mu        sync.Mutex
	data      []byte
	writable  bool
	appending bool
	// dirty is "there is a write in this buffer that the queue has not been
	// told about". It is what makes the enqueue happen exactly once per close
	// even though flush can be called more than once for one handle - a dup'd
	// descriptor closes twice, and two intents for one write would be two
	// writes of the same bytes with two events behind them.
	dirty bool
}

var (
	_ = (fs.FileReader)((*fileHandle)(nil))
	_ = (fs.FileWriter)((*fileHandle)(nil))
	_ = (fs.FileFlusher)((*fileHandle)(nil))
	_ = (fs.FileReleaser)((*fileHandle)(nil))
	_ = (fs.FileFsyncer)((*fileHandle)(nil))
	_ = (fs.FileGetattrer)((*fileHandle)(nil))
)

func (h *fileHandle) Read(_ context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if off >= int64(len(h.data)) {
		return fuse.ReadResultData(nil), 0
	}
	end := off + int64(len(dest))
	if end > int64(len(h.data)) {
		end = int64(len(h.data))
	}
	return fuse.ReadResultData(h.data[off:end]), 0
}

func (h *fileHandle) Write(_ context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.writable {
		return 0, syscall.EBADF
	}
	if h.appending {
		off = int64(len(h.data))
	}
	if off < 0 {
		return 0, syscall.EINVAL
	}
	if grow := int(off) + len(data) - len(h.data); grow > 0 {
		h.data = append(h.data, make([]byte, grow)...)
	}
	copy(h.data[off:], data)
	h.dirty = true
	return uint32(len(data)), 0
}

// truncate cuts the buffer. It does not make the handle dirty: emptying a file
// is not a write of anything, and an item is not emptied by being opened -
// unlink is how one goes away. What it is for is the shape above, where the
// content the writer is replacing has to come off before the shorter thing it
// is replacing it with goes on.
func (h *fileHandle) truncate(size int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case size < 0:
		return
	case size <= len(h.data):
		h.data = h.data[:size]
	default:
		h.data = append(h.data, make([]byte, size-len(h.data))...)
	}
}

func (h *fileHandle) Getattr(_ context.Context, out *fuse.AttrOut) syscall.Errno {
	h.mu.Lock()
	size := uint64(len(h.data))
	h.mu.Unlock()
	h.f.fileAttr(&out.Attr, size, time.Now())
	return 0
}

// Fsync is where a caller asks for durability by name, so it is the enqueue
// too: when it returns the write is in the queue, which is what durable means
// here - it will reach the store, this run or the next one.
func (h *fileHandle) Fsync(ctx context.Context, _ uint32) syscall.Errno {
	return h.enqueue(ctx, "fsync")
}

// Flush is close(2). It is where the write is queued and where the answer to it
// is given, because Release cannot: the kernel ignores what Release returns, so
// a failure reported there is a failure reported to nobody.
func (h *fileHandle) Flush(ctx context.Context) syscall.Errno {
	return h.enqueue(ctx, "flush")
}

// Release is the last close of this handle. Anything still unqueued is queued
// here - a writer that never flushed, which mmap does - and the errno goes
// nowhere, so this is the belt and Flush is the braces.
func (h *fileHandle) Release(ctx context.Context) syscall.Errno {
	if errno := h.enqueue(ctx, "release"); errno != 0 {
		h.f.log.Printf("release %s: the write was refused after the close: errno %d",
			h.node.path, errno)
	}
	h.node.forget(h)
	return 0
}

// enqueue writes the buffer down as an intent, once. A handle that has nothing
// new in it is a no-op, which is what makes two flushes of one write one write.
func (h *fileHandle) enqueue(ctx context.Context, what string) syscall.Errno {
	h.mu.Lock()
	if !h.writable || !h.dirty {
		h.mu.Unlock()
		return 0
	}
	data := append([]byte(nil), h.data...)
	h.mu.Unlock()

	errno := h.f.op(ctx, what+" "+h.node.path, func(ctx context.Context) error {
		return h.f.commit(ctx, h.node, data)
	})
	if errno != 0 {
		return errno
	}
	// Clear the flag only once the intent is down. A close that failed leaves
	// the buffer dirty, so the next one tries again rather than dropping it.
	h.mu.Lock()
	h.dirty = false
	h.mu.Unlock()
	return 0
}
