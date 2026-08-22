package store

// The openspec validate arm (p4).
//
// A change's delta checks run on demand through the validate door, and the
// verdict is cached on the row (fields.openspec.validation) so the lifecycle's
// complete arm can read it instead of re-deriving it - the arm is
// ValidateChange in openspec_lifecycle.go, which until this file existed
// failed closed. The cache carries the files-map hash it checked; a change
// edited after validation reads stale and complete refuses, so a verdict can
// never outlive the files it was about. The row's own hlc cannot serve that
// compare - the validation write itself bumps it.
//
// The checks are the core set the operator approved (note on row
// 01M0KA7RN6BP0QT9B155BC4VW2, 2026-08-22): proposal.md present and
// non-empty (already the write path's rule - checkOpenspecRow), tasks.md
// present, every task line names a delta, every delta names a spec that
// exists, and delta sections are well-formed - a requirement with at least
// one scenario. Not the full openspec lint: the plumbing is this file's
// subject, the checks are the shape that proves it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// OpenspecValidation is the cached verdict a change row holds in
// fields.openspec.validation.
type OpenspecValidation struct {
	Ok        bool     `json:"ok"`
	Problems  []string `json:"problems,omitempty"`
	FilesHash string   `json:"files_hash"`
	CheckedAt int64    `json:"checked_at"`
}

// OpenspecValidationOf reads the cached verdict off a row. Absent is nil, not
// an error - "never validated" is a state, and the caller refuses it with its
// own sentence. Unparsable fields IS an error: that is not "no verdict", it
// is a row this code cannot read.
func OpenspecValidationOf(a *Artifact) (*OpenspecValidation, error) {
	if a == nil || len(a.Fields) == 0 {
		return nil, nil
	}
	var outer struct {
		Openspec *struct {
			Validation *OpenspecValidation `json:"validation"`
		} `json:"openspec"`
	}
	if err := json.Unmarshal(a.Fields, &outer); err != nil {
		return nil, fmt.Errorf("fields is not JSON: %w", err)
	}
	if outer.Openspec == nil {
		return nil, nil
	}
	return outer.Openspec.Validation, nil
}

// SetOpenspecValidation puts the cached verdict on a change row's fields,
// keeping the state, the files map and every other key. It is the write-side
// sibling of OpenspecValidationOf. The write itself is the caller's and goes
// through the ordinary artifact path - the validate door upserts the row -
// which is what keeps the lifecycle state carried and the shape checked.
func SetOpenspecValidation(a *Artifact, v *OpenspecValidation) error {
	fields, err := ArtifactFields(a)
	if err != nil {
		return err
	}
	os, _ := fields["openspec"].(map[string]any)
	if os == nil {
		os = map[string]any{}
	}
	os["validation"] = v
	fields["openspec"] = os
	raw, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("store: openspec fields of %s: %w", a.ID, err)
	}
	a.Fields = raw
	return nil
}

// openspecFilesHash is the identity of a files map: sha256 over the sorted
// path+content pairs, so an edit to any file reads as a different map and an
// insertion-order shuffle reads as the same one. The cache stores it; the
// complete arm compares against it.
func openspecFilesHash(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(files[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// deltaPathsOf is the change's spec deltas: the specs/<capability>/... paths,
// and only those - proposal.md, tasks.md and design.md are the change's own
// words and are not deltas. The same walk as conflictCapabilities, kept to
// full paths because a task cites a file, not a capability.
func deltaPathsOf(files map[string]string) []string {
	var out []string
	for path := range files {
		if strings.HasPrefix(path, "specs/") {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

// deltaCapabilityOf is the capability a delta path names: its first segment.
// It returns false for a path that is not specs/<capability>/..., which is a
// problem of its own and the caller says so.
func deltaCapabilityOf(path string) (string, bool) {
	rest := strings.TrimPrefix(path, "specs/")
	cap, _, ok := strings.Cut(rest, "/")
	if !ok || cap == "" {
		return "", false
	}
	return cap, true
}

// checkOpenspecValidate is the whole of the delta checks, pure over the files
// map and the spec names of the change's project, so every refusal is
// exercised without a database. It returns the problems; empty means the
// change validates. Each sentence is the whole story, in the style of the
// other shape checks - the caller refuses with them verbatim.
func checkOpenspecValidate(files map[string]string, specs map[string]bool) []string {
	var problems []string

	if strings.TrimSpace(files["tasks.md"]) == "" {
		problems = append(problems, "tasks.md is absent or empty - a change's tasks live there")
	}

	tasks := parseTasks(files["tasks.md"])
	deltas := deltaPathsOf(files)
	for _, line := range tasks {
		if !taskNamesADelta(line.text, deltas) {
			problems = append(problems,
				fmt.Sprintf("task %d (%s) names no delta - a task has to touch a spec to be a task",
					line.num, line.text))
		}
	}

	for _, path := range deltas {
		cap, ok := deltaCapabilityOf(path)
		if !ok {
			problems = append(problems,
				fmt.Sprintf("delta %q names no capability - a delta lives at specs/<capability>/spec.md", path))
			continue
		}
		if !specs[cap] {
			problems = append(problems,
				fmt.Sprintf("delta %s names no spec - the capability %q is not a spec row in this project",
					path, cap))
		}
	}

	for _, path := range deltas {
		problems = append(problems, checkOpenspecDeltaSections(path, files[path])...)
	}

	return problems
}

// taskNamesADelta reports whether a task line's text cites at least one of the
// change's delta paths. The citation is a substring - the openspec convention
// writes the path into the task ("Add a delta to specs/session/spec.md") - and
// only files that exist in the change's own map count: a task pointing at a
// delta that is not in the change does nothing this change can see.
func taskNamesADelta(text string, deltas []string) bool {
	for _, d := range deltas {
		if strings.Contains(text, d) {
			return true
		}
	}
	return false
}

// checkOpenspecDeltaSections is the well-formed check on one delta file: the
// file holds at least one requirement, and every requirement has at least one
// scenario beneath it. A delta that requires nothing changes nothing, and a
// requirement nobody can tell when it is met is not a requirement.
func checkOpenspecDeltaSections(path, content string) []string {
	var problems []string
	req := ""
	scenarios := 0
	flush := func() {
		if req != "" && scenarios == 0 {
			problems = append(problems,
				fmt.Sprintf("requirement %q in %s has no scenario - a requirement says when it is met", req, path))
		}
	}
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "### Requirement:"):
			flush()
			req = strings.TrimSpace(strings.TrimPrefix(line, "### Requirement:"))
			scenarios = 0
		case strings.HasPrefix(line, "#### Scenario:"):
			scenarios++
		}
	}
	flush()
	if req == "" {
		problems = append(problems,
			fmt.Sprintf("delta %s holds no requirements - a delta that requires nothing changes nothing", path))
	}
	return problems
}

// ValidateOpenspecChange runs a change's delta checks and returns the verdict,
// with the files-map hash it covered and the clock reading it ran at. It does
// not write - the door caches the verdict through the ordinary artifact path,
// which is what carries the lifecycle state and re-runs the shape checks.
func (d *DB) ValidateOpenspecChange(ctx context.Context, a *Artifact) (*OpenspecValidation, error) {
	files, err := OpenspecFilesOf(a)
	if err != nil {
		return nil, err
	}
	specs, err := d.specNamesOf(ctx, a.Project)
	if err != nil {
		return nil, err
	}
	problems := checkOpenspecValidate(files, specs)
	at, err := d.clock.Pack()
	if err != nil {
		return nil, fmt.Errorf("store: validate openspec change %s: %w", a.ID, err)
	}
	return &OpenspecValidation{
		Ok:        len(problems) == 0,
		Problems:  problems,
		FilesHash: openspecFilesHash(files),
		CheckedAt: at,
	}, nil
}

// specNamesOf is the set of capability names a project holds: the titles of
// its live spec rows. A delta names a capability, and the capability is the
// spec row's title - the project is the boundary, the same as the conflict
// edges.
func (d *DB) specNamesOf(ctx context.Context, project *string) (map[string]bool, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT title
		   FROM artifacts
		  WHERE kind = $1
		    AND coalesce(tombstone, false) = false
		    AND project IS NOT DISTINCT FROM $2`, SpecKind, project)
	if err != nil {
		return nil, fmt.Errorf("store: spec names: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return nil, fmt.Errorf("store: spec names: %w", err)
		}
		if title != "" {
			out[title] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: spec names: %w", err)
	}
	return out, nil
}
