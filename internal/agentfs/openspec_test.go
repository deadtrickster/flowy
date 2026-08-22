package agentfs

import (
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// The shape of a change's tree is decided in one pure function, so it is
// exercised without a database: files are the keys that end at a depth,
// directories are the ones with more beneath, and both listings and lookups
// take their answer from the same names.

func treeNames(t *testing.T, files map[string]string, rel string) (map[string]uint32, []string) {
	t.Helper()

	entries := changeTree(files, rel, "/x", "art")
	modes := map[string]uint32{}
	var order []string
	for _, e := range entries {
		modes[e.Name] = e.Mode
		order = append(order, e.Name)
	}
	return modes, order
}

func TestChangeTreeFlattened(t *testing.T) {
	modes, order := treeNames(t, map[string]string{
		"proposal.md": "# why\n",
		"tasks.md":    "- [ ] do it\n",
		"design.md":   "## shape\n",
	}, "")

	if len(modes) != 3 {
		t.Fatalf("a flat change listed %d names: %v", len(modes), order)
	}
	for _, name := range order {
		if modes[name]&fuse.S_IFREG == 0 {
			t.Fatalf("%s in a flat change listed as mode %o, not a file", name, modes[name])
		}
	}
	// Sorted, so the mount answers the same order every readdir.
	for i := 1; i < len(order); i++ {
		if order[i-1] >= order[i] {
			t.Fatalf("names not sorted: %v", order)
		}
	}
}

func TestChangeTreeNested(t *testing.T) {
	files := map[string]string{
		"proposal.md":        "# why\n",
		"specs/cap/spec.md":  "# cap\n",
		"specs/other.md":     "# other\n",
		"specification/x.md": "# decoy\n",
	}

	// The root: proposal.md a file, specs a directory, and the decoy its own
	// thing - the prefix match is on the whole segment, so "specs" does not
	// swallow "specification".
	modes, order := treeNames(t, files, "")
	if len(order) != 3 {
		t.Fatalf("the root listed %d names, want proposal, specification, specs: %v", len(order), order)
	}
	if modes["proposal.md"]&fuse.S_IFREG == 0 {
		t.Fatalf("proposal.md listed as %o, not a file", modes["proposal.md"])
	}
	if modes["specs"]&fuse.S_IFDIR == 0 {
		t.Fatalf("specs listed as %o, not a directory", modes["specs"])
	}
	if modes["specification"]&fuse.S_IFDIR == 0 {
		t.Fatalf("specification listed as %o, not a directory", modes["specification"])
	}

	// One level down: only what is under specs/, at that depth.
	modes, order = treeNames(t, files, "specs")
	if len(order) != 2 {
		t.Fatalf("specs/ listed %d names, want cap and other.md: %v", len(order), order)
	}
	if modes["cap"]&fuse.S_IFDIR == 0 {
		t.Fatalf("cap listed as %o, not a directory", modes["cap"])
	}
	if modes["other.md"]&fuse.S_IFREG == 0 {
		t.Fatalf("other.md listed as %o, not a file", modes["other.md"])
	}

	// The leaf: the file itself, and nothing that belongs to other subtrees.
	modes, order = treeNames(t, files, "specs/cap")
	if len(order) != 1 || modes["spec.md"]&fuse.S_IFREG == 0 {
		t.Fatalf("specs/cap/ listed %v with modes %v, want just the file", order, modes)
	}
}

func TestChangeTreeDirWinsOverFile(t *testing.T) {
	modes, _ := treeNames(t, map[string]string{
		"a":   "flat\n",
		"a/b": "nested\n",
	}, "")
	if modes["a"]&fuse.S_IFDIR == 0 {
		t.Fatalf("a key with children under it listed as %o, not a directory", modes["a"])
	}
}

func TestChangeTreeEmpty(t *testing.T) {
	if entries := changeTree(nil, "", "/x", "art"); len(entries) != 0 {
		t.Fatalf("an empty change listed %v", entries)
	}
	// A directory that exists only in the map's parent depths.
	if entries := changeTree(map[string]string{"a/b": "x"}, "a/b", "/x", "art"); len(entries) != 0 {
		t.Fatalf("a leaf without files listed %v", entries)
	}
}

func TestOpenspecKey(t *testing.T) {
	if got := openspecKey("", "proposal.md"); got != "proposal.md" {
		t.Fatalf("root key is %q, want proposal.md", got)
	}
	if got := openspecKey("specs", "cap"); got != "specs/cap" {
		t.Fatalf("nested key is %q, want specs/cap", got)
	}
}
