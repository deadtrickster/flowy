package agentfs

// An openspec change in the mount is a DIRECTORY, not a file. The row is the
// change, and its fields.openspec.files are the files inside it - one row,
// many paths, and each file is a view over the row rather than a row of its
// own: reading proposal.md is reading the change. A spec stays a plain file,
// because its body IS its spec.md and renders like any other memory row.
//
// The tree is READ-ONLY in this slice, deliberately, and the refusals say so
// rather than pretending. Writing a file here would be writing the row it
// views, and the rules for that write are the derivation work (p2: tasks.md
// is authoritative, edits re-sync the derived todos). Until that lands, a
// save through the mount has two ways to do damage: the drainer wedges on an
// apply it keeps refusing (see drain.go - the intent is retried forever),
// and a save of a spec without the front-matter header would husk it into an
// ordinary note past the openspec check, because the check is on the row and
// the header is what decides the kind. Both are worse than EPERM. The doors
// (POST /api/openspec) write; the mount shows.

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/deadtrickster/flowy/internal/store"
)

// changeNode is one openspec change seen as a directory. artID is fixed for
// the life of the inode and is part of every inode number inside it, so a
// directory can never answer for a change whose row has been replaced.
type changeNode struct {
	fs.Inode
	f     *FS
	scope store.FSScope
	artID string
	// rel is where this directory is inside the change: "" at its root,
	// "specs/cap" one level down. Files map keys are relative to the root.
	rel  string
	path string
}

var (
	_ = (fs.NodeGetattrer)((*changeNode)(nil))
	_ = (fs.NodeReaddirer)((*changeNode)(nil))
	_ = (fs.NodeLookuper)((*changeNode)(nil))
	_ = (fs.NodeCreater)((*changeNode)(nil))
	_ = (fs.NodeUnlinker)((*changeNode)(nil))
	_ = (fs.NodeMkdirer)((*changeNode)(nil))
	_ = (fs.NodeRmdirer)((*changeNode)(nil))
)

// openspecKey is the files-map key a name at this depth stands for.
func openspecKey(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func (n *changeNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.f.dirAttr(&out.Attr)
	return 0
}

// row reads the change through the same permission filter every other read
// here goes through, and its files.
func (n *changeNode) row(ctx context.Context) (map[string]string, *store.Artifact, error) {
	art, err := n.f.db.FSFind(ctx, n.f.p, n.scope, n.artID)
	if err != nil {
		return nil, nil, err
	}
	files, err := store.OpenspecFilesOf(art)
	if err != nil {
		return nil, nil, err
	}
	return files, art, nil
}

func (n *changeNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	var entries []fuse.DirEntry
	errno := n.f.op(ctx, "readdir "+n.path, func(ctx context.Context) error {
		files, _, err := n.row(ctx)
		if err != nil {
			return err
		}
		entries = changeTree(files, n.rel, n.path, n.artID)
		return nil
	})
	if errno != 0 {
		return nil, errno
	}
	return fs.NewListDirStream(entries), 0
}

// changeTree is the names one directory of a change holds: the files-map keys
// under rel, one segment deep - a file where the key ends there, a directory
// where more follows. Pure, so the tree shape is exercised without a
// database, and it is the only place the shape is decided: Readdir and
// Lookup both take their answer from it.
func changeTree(files map[string]string, rel, dirPath, artID string) []fuse.DirEntry {
	prefix := ""
	if rel != "" {
		prefix = rel + "/"
	}
	isDir := map[string]bool{}
	for key := range files {
		rest, ok := strings.CutPrefix(key, prefix)
		if !ok {
			continue
		}
		head, tail, _ := strings.Cut(rest, "/")
		if head == "" {
			continue
		}
		// A key that has more segments under the same head makes the head a
		// directory even if a file of the same name also exists - the
		// directory wins, as in a real tree where both cannot be.
		if tail != "" {
			isDir[head] = true
		} else if _, seen := isDir[head]; !seen {
			isDir[head] = false
		}
	}
	names := make([]string, 0, len(isDir))
	for name := range isDir {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]fuse.DirEntry, 0, len(names))
	for _, name := range names {
		childPath := path.Join(dirPath, name)
		if isDir[name] {
			out = append(out, fuse.DirEntry{Name: name, Mode: fuse.S_IFDIR, Ino: ino(childPath)})
		} else {
			out = append(out, fuse.DirEntry{Name: name, Mode: fuse.S_IFREG, Ino: fileIno(childPath, artID)})
		}
	}
	return out
}

func (n *changeNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var child *fs.Inode
	errno := n.f.op(ctx, "lookup "+path.Join(n.path, name), func(ctx context.Context) error {
		if err := checkName(name); err != nil {
			// Not a name this filesystem can hold, so nothing here is called
			// that. ENOENT rather than EINVAL, as in the leaf lookup.
			return store.ErrNotFound
		}
		files, art, err := n.row(ctx)
		if err != nil {
			return err
		}
		key := openspecKey(n.rel, name)
		if content, isFile := files[key]; isFile {
			childPath := path.Join(n.path, name)
			node := &openspecFileNode{f: n.f, scope: n.scope, artID: n.artID, rel: key, path: childPath}
			n.f.fileAttr(&out.Attr, uint64(len(content)), art.Updated)
			child = n.NewInode(ctx, node, fs.StableAttr{Mode: fuse.S_IFREG, Ino: fileIno(childPath, n.artID)})
			return nil
		}
		// A directory when any key lies under it.
		prefix := key + "/"
		for k := range files {
			if strings.HasPrefix(k, prefix) {
				childPath := path.Join(n.path, name)
				node := &changeNode{f: n.f, scope: n.scope, artID: n.artID, rel: key, path: childPath}
				n.f.dirAttr(&out.Attr)
				child = n.NewInode(ctx, node, fs.StableAttr{Mode: fuse.S_IFDIR, Ino: ino(childPath)})
				return nil
			}
		}
		return store.ErrNotFound
	})
	if errno != 0 {
		return nil, errno
	}
	return child, 0
}

// readOnly refuses a write into a change, and says why. It is called on every
// mutating entry point so no write path can reach the queue without the
// refusal - an intent the apply refuses wedges the drainer forever.
func (n *changeNode) readOnly(ctx context.Context, what string) syscall.Errno {
	return n.f.op(ctx, what, func(context.Context) error {
		return fmt.Errorf("%w: an openspec change is read-only in the mount - its files "+
			"are views over the row, and writing them is the derivation work (p2); "+
			"POST /api/openspec writes the row", errRefused)
	})
}

func (n *changeNode) Create(ctx context.Context, name string, _, _ uint32, _ *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	return nil, nil, 0, n.readOnly(ctx, "create "+path.Join(n.path, name))
}

func (n *changeNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.readOnly(ctx, "unlink "+path.Join(n.path, name))
}

func (n *changeNode) Mkdir(ctx context.Context, name string, _ uint32, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, n.readOnly(ctx, "mkdir "+path.Join(n.path, name))
}

func (n *changeNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.readOnly(ctx, "rmdir "+path.Join(n.path, name))
}

// openspecFileNode is one file inside a change: a view over one key of the
// row's files map. Read-only for the reason the change is - Open refuses a
// write flag before any buffer exists, so nothing can queue an intent the
// apply would refuse.
type openspecFileNode struct {
	fs.Inode
	f     *FS
	scope store.FSScope
	artID string
	rel   string
	path  string
}

var (
	_ = (fs.NodeGetattrer)((*openspecFileNode)(nil))
	_ = (fs.NodeOpener)((*openspecFileNode)(nil))
)

// content is the bytes this file views right now: the row's files map read
// through the same permission filter every other read here goes through.
func (n *openspecFileNode) content(ctx context.Context) ([]byte, time.Time, error) {
	art, err := n.f.db.FSFind(ctx, n.f.p, n.scope, n.artID)
	if err != nil {
		return nil, time.Time{}, err
	}
	files, err := store.OpenspecFilesOf(art)
	if err != nil {
		return nil, time.Time{}, err
	}
	return []byte(files[n.rel]), art.Updated, nil
}

func (n *openspecFileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := fh.(*roHandle); ok && h != nil {
		h.mu.Lock()
		size := uint64(len(h.data))
		h.mu.Unlock()
		n.f.fileAttr(&out.Attr, size, h.mtime)
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

func (n *openspecFileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	var handle *roHandle
	errno := n.f.op(ctx, "open "+n.path, func(ctx context.Context) error {
		if int(flags)&(syscall.O_WRONLY|syscall.O_RDWR) != 0 {
			return fmt.Errorf("%w: %s views an openspec change and is read-only in "+
				"the mount - the write is the derivation work (p2); POST /api/openspec "+
				"writes the row", errRefused, n.path)
		}
		data, mtime, err := n.content(ctx)
		if err != nil {
			return err
		}
		handle = &roHandle{data: data, mtime: mtime}
		return nil
	})
	if errno != 0 {
		return nil, 0, errno
	}
	// Direct IO, for the same reason Create gives: the store is the file, and
	// a page cache in front of it would answer with what this mount said last.
	return handle, fuse.FOPEN_DIRECT_IO, 0
}

// roHandle serves one buffer of bytes for reading. It is deliberately not a
// fileHandle: that type exists to become an intent on close, and this file
// must never have one.
type roHandle struct {
	mu    sync.Mutex
	data  []byte
	mtime time.Time
}

var _ = (fs.FileReader)((*roHandle)(nil))

func (h *roHandle) Read(_ context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
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
