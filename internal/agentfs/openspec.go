package agentfs

// An openspec change in the mount is a DIRECTORY, not a file. The row is the
// change, and its fields.openspec.files are the files inside it - one row,
// many paths, and each file is a view over the row rather than a row of its
// own: reading proposal.md is reading the change. A spec stays a plain file,
// because its body IS its spec.md and renders like any other memory row.
//
// The tree is WRITABLE. A save of a view writes that key of the row's files
// map, through the ordinary write-behind queue (the intent carries the key),
// and the whole store write path rides behind it - the shape check, the
// tasks.md line ids, the derived todos, the conflict edges. Two arms make
// the writes safe, both in the store:
//
//   - the husk arm: an openspec row rewritten through the queue keeps its
//     kind - a spec saved without its front matter stays a spec, because
//     the ROW says what it is and the header cannot argue;
//   - the wedge arm: an apply the store refuses (say, a save that strips
//     proposal.md) is dropped once with the refusal's own sentence recorded
//     on the queue row, and the queue behind it keeps draining - nothing
//     wedges.
//
// Deletion is still refused: a change's end is the lifecycle (p3), not an
// rm. A spec's either - see dirNode.Unlink.

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"syscall"

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
			// The literal rather than a made-up fileNode value: vet is right
			// that copying a node copies its Inode's lock.
			view := &openspecFileNode{fileNode: fileNode{f: n.f, scope: n.scope, name: name, id: n.artID, path: childPath, viewKey: key}}
			n.f.fileAttr(&out.Attr, uint64(len(content)), art.Updated)
			child = n.NewInode(ctx, view, fs.StableAttr{Mode: fuse.S_IFREG, Ino: fileIno(childPath, n.artID)})
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

// hasKeysUnder reports whether the files map holds any key under key+"/" -
// which is what makes key a directory in the change's tree.
func hasKeysUnder(files map[string]string, key string) bool {
	prefix := key + "/"
	for k := range files {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// Create is a new file inside the change: a new key of the row's files map.
// Nothing is written to the row until the close that ends the write - the
// same write-behind every file here has; a create that closes empty writes
// nothing. The intent the close queues carries the key, and the store's
// apply edits the map through the ordinary write path: shape check, line
// ids, derived todos, conflict edges.
func (n *changeNode) Create(ctx context.Context, name string, _, _ uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	var (
		child  *fs.Inode
		handle fs.FileHandle
	)
	errno := n.f.op(ctx, "create "+path.Join(n.path, name), func(ctx context.Context) error {
		if err := checkName(name); err != nil {
			return err
		}
		if !n.f.writable(n.scope) {
			return fmt.Errorf("%w: %s is not yours to write in", errRefused, n.path)
		}
		key := openspecKey(n.rel, name)
		files, art, err := n.row(ctx)
		if err != nil {
			return err
		}
		if art.OwnerUser != n.f.p.UserID {
			return fmt.Errorf("%w: %s belongs to somebody else", errRefused, n.path)
		}
		// A directory cannot share its name with a file - the directory wins
		// in the tree (changeTree), and a create has to say so rather than
		// write a file the directory hides.
		if hasKeysUnder(files, key) {
			return syscall.EISDIR
		}
		childPath := path.Join(n.path, name)
		n.f.fileAttr(&out.Attr, 0, art.Updated)
		// The literal rather than a made-up fileNode value: vet is right that
		// copying a node copies its Inode's lock.
		child = n.NewInode(ctx, &openspecFileNode{fileNode: fileNode{f: n.f, scope: n.scope, name: name, id: n.artID, path: childPath, viewKey: key}},
			fs.StableAttr{Mode: fuse.S_IFREG, Ino: fileIno(childPath, n.artID)})
		// Not dirty yet - a create is an open, not a write; see the same
		// decision on dirNode.Create. The handle holds the inode's own node,
		// for the same reason dirNode.Create does: truncate fan-out walks the
		// node's open set, and a copy would miss it.
		ops := child.Operations().(*openspecFileNode)
		handle = &fileHandle{f: n.f, node: &ops.fileNode, writable: true}
		return nil
	})
	if errno != 0 {
		return nil, nil, 0, errno
	}
	return child, handle, fuse.FOPEN_DIRECT_IO, 0
}

// Unlink removes one key from the row's files map, and it does it now rather
// than through the queue - the same choice dirNode.Unlink makes, for the
// same reason: the caller is entitled to be told whether the thing is gone.
// A pending write of this same view is dropped first; other views' pending
// writes are their own and stay.
func (n *changeNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.f.op(ctx, "unlink "+path.Join(n.path, name), func(ctx context.Context) error {
		if err := checkName(name); err != nil {
			return err
		}
		if !n.f.writable(n.scope) {
			return fmt.Errorf("%w: %s is not yours to write in", errRefused, n.path)
		}
		key := openspecKey(n.rel, name)
		files, art, err := n.row(ctx)
		if err != nil {
			return err
		}
		if art.OwnerUser != n.f.p.UserID {
			return fmt.Errorf("%w: %s belongs to somebody else", errRefused, n.path)
		}
		if _, isFile := files[key]; !isFile {
			if hasKeysUnder(files, key) {
				return syscall.EISDIR
			}
			return store.ErrNotFound
		}
		here := path.Join(n.path, name)
		if queued := n.f.pendingAt(here); queued != nil {
			n.f.dropPending(here)
			if err := n.f.db.DropFSIntent(ctx, queued.intent); err != nil {
				return err
			}
		}
		delete(files, key)
		// The shape check rides this write like any other: unlinking
		// proposal.md is refused with the check's own sentence, and a
		// change is a proposal.
		if err := store.SetOpenspecFiles(art, files); err != nil {
			return err
		}
		return n.f.db.UpsertArtifact(ctx, art)
	})
}

// Mkdir makes a directory inside the change - which is nothing to write:
// directories are the keys' shape, not rows, and specs/cap exists as soon as
// a file lands under it. A name a file already holds is refused - a
// directory cannot share it.
func (n *changeNode) Mkdir(ctx context.Context, name string, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	var child *fs.Inode
	errno := n.f.op(ctx, "mkdir "+path.Join(n.path, name), func(ctx context.Context) error {
		if err := checkName(name); err != nil {
			return err
		}
		if !n.f.writable(n.scope) {
			return fmt.Errorf("%w: %s is not yours to write in", errRefused, n.path)
		}
		key := openspecKey(n.rel, name)
		files, art, err := n.row(ctx)
		if err != nil {
			return err
		}
		if art.OwnerUser != n.f.p.UserID {
			return fmt.Errorf("%w: %s belongs to somebody else", errRefused, n.path)
		}
		if _, isFile := files[key]; isFile {
			return syscall.EEXIST
		}
		childPath := path.Join(n.path, name)
		n.f.dirAttr(&out.Attr)
		child = n.NewInode(ctx, &changeNode{f: n.f, scope: n.scope, artID: n.artID, rel: key, path: childPath},
			fs.StableAttr{Mode: fuse.S_IFDIR, Ino: ino(childPath)})
		return nil
	})
	if errno != 0 {
		return nil, errno
	}
	return child, 0
}

// Rmdir removes a directory inside the change. A directory that still holds
// keys is not empty and is refused; an empty one goes away, which is nothing
// to write.
func (n *changeNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.f.op(ctx, "rmdir "+path.Join(n.path, name), func(ctx context.Context) error {
		if err := checkName(name); err != nil {
			return err
		}
		if !n.f.writable(n.scope) {
			return fmt.Errorf("%w: %s is not yours to write in", errRefused, n.path)
		}
		key := openspecKey(n.rel, name)
		files, _, err := n.row(ctx)
		if err != nil {
			return err
		}
		if _, isFile := files[key]; isFile {
			return syscall.ENOTDIR
		}
		if hasKeysUnder(files, key) {
			return syscall.ENOTEMPTY
		}
		return nil
	})
}

// openspecFileNode is one file inside a change: a view over one key of the
// row's files map. It is a fileNode carrying a viewKey, and everything a
// fileNode does it does - the buffer, the write-behind queue, the truncate
// marks, the read through the pending cache. What the key changes is what a
// read returns (that key of the map, not the rendered row) and what the
// committed intent carries, so the store's apply edits the map rather than
// writing a row whole.
type openspecFileNode struct {
	fs.Inode
	fileNode
}
