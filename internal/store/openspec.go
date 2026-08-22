package store

// Openspec artifacts live under the memory bucket, the same as todos and
// merges: kind is the identity and EntityType answers it (entitytype.go). Two
// kinds:
//
//	spec   - one capability. title is the capability name, body is its
//	         spec.md. A spec IS its words, so both are required.
//	change - one openspec change. The markdown lives in fields.openspec.files,
//	         one entry per file: proposal.md, tasks.md, design.md,
//	         specs/<capability>/spec.md. The files map is the whole of the
//	         change's content, so a checkout is a view over the row rather
//	         than the row being an index over a checkout - answer 1 of the
//	         openspec plan (room message 01M0KA567A9GQTZH5650RA2V91, thread
//	         01M0K9WFBNBZ9V9XBK5NGD7D9K).
//
//	One consequence of that shape, read it before changing it: a change's
//	history is per-ROW, not per-file. Editing proposal.md supersedes the whole
//	change - tasks.md and design.md included - and one signature covers the
//	set. If per-file history is ever wanted, it is a migration, not a
//	feature. (Recorded at the operator's request on review of the p1 slice.)
//
// The lifecycle statuses ride the artifact status column (proposed,
// in-progress, complete, archived). The doors and rules that move a row
// between them are later siblings, not this file.

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// SpecKind is an openspec capability: one spec, its requirements, its
	// scenarios. Body is the spec.md.
	SpecKind = "spec"
	// ChangeKind is an openspec change: the proposal, tasks, design and spec
	// deltas of one piece of work. The files map carries them all.
	ChangeKind = "change"
)

// IsOpenspec reports whether a row is one of the two openspec kinds. It goes
// through EntityType, not the raw columns, because that is the one function
// that answers what a row IS (entitytype.go).
func IsOpenspec(a *Artifact) bool {
	return IsEntityType(a, SpecKind) || IsEntityType(a, ChangeKind)
}

// OpenspecFilesOf reads fields.openspec.files off a row. Absent fields is an
// empty map, not an error - a change row without its files is a defect that
// checkOpenspecRow refuses at the next write, and a reader that cannot see
// the files says so by returning nothing, the same way every other read of an
// empty column does. Unparsable fields IS an error: that is not "no files",
// it is a row this code cannot read.
func OpenspecFilesOf(a *Artifact) (map[string]string, error) {
	if a == nil || len(a.Fields) == 0 {
		return nil, nil
	}
	var outer struct {
		Openspec *struct {
			Files map[string]string `json:"files"`
		} `json:"openspec"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil {
		return nil, fmt.Errorf("fields is not JSON: %w", err)
	}
	if outer.Openspec == nil {
		return nil, nil
	}
	return outer.Openspec.Files, nil
}

// OpenspecRowError is a refusal: the statement wanted to write a row that
// says it is an openspec artifact but is not one. The caller can fix it, so
// it is a 400 at the doors - it implements depRefusal, the same refusal
// contract checkMergeRow and checkQueueRow use (deps.go).
type OpenspecRowError struct {
	Row string
	Why string
}

func (e OpenspecRowError) Error() string {
	return fmt.Sprintf("openspec row %s: %s", e.Row, e.Why)
}

func (e OpenspecRowError) depRefusal() {}

// checkOpenspecFilePaths validates each path in a files map. A path is a
// NAME, not a route: relative, no ".." segment, no leading slash, and no
// backslash - the other slash is how a path escapes the change on a
// different OS, and a file that lives inside a change has nothing to reach
// out to. The fuse tree renders these names under one directory later, and a
// name that is a route is how one file turns into a write outside it.
func checkOpenspecFilePaths(files map[string]string) error {
	for path := range files {
		p := strings.TrimSpace(path)
		if p == "" {
			return fmt.Errorf("a file path is empty - a file is named")
		}
		if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
			return fmt.Errorf("path %q starts at the root - a file in a change lives inside it", path)
		}
		for _, seg := range strings.Split(strings.ReplaceAll(p, "\\", "/"), "/") {
			if seg == ".." {
				return fmt.Errorf("path %q climbs out of the change - %q is not a name", path, seg)
			}
		}
	}
	return nil
}

// checkOpenspecRow is the invariant, asked of the row a statement is about
// to write at the same three statements that ask checkQueueRow and
// checkMergeRow - upsert, set-fields and create. It refuses nothing that is
// not an openspec kind, and for those two it is the whole shape:
//
//   - a spec with no body is a spec that specifies nothing
//   - a change with no proposal.md is a change that proposes nothing
//   - a files map whose paths are routes is a change that escapes itself
//
// An edit that drops the files map on a change row is refused here rather
// than silently turning a change into a husk - the same reason a merge row
// cannot be written without its branch.
func checkOpenspecRow(a *Artifact) error {
	if a == nil || !IsOpenspec(a) {
		return nil
	}
	switch {
	case IsEntityType(a, SpecKind):
		if strings.TrimSpace(a.Body) == "" {
			return OpenspecRowError{Row: a.ID, Why: "a spec is its spec.md - body must carry it"}
		}
		if strings.TrimSpace(a.Title) == "" {
			return OpenspecRowError{Row: a.ID, Why: "a spec names its capability - title must carry it"}
		}
	case IsEntityType(a, ChangeKind):
		files, err := OpenspecFilesOf(a)
		if err != nil {
			return OpenspecRowError{Row: a.ID, Why: err.Error()}
		}
		if strings.TrimSpace(files["proposal.md"]) == "" {
			return OpenspecRowError{Row: a.ID,
				Why: "a change is a proposal - fields.openspec.files must carry a non-empty proposal.md"}
		}
		if err := checkOpenspecFilePaths(files); err != nil {
			return OpenspecRowError{Row: a.ID, Why: err.Error()}
		}
	}
	return nil
}
