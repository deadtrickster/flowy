package store

// A finding's repro is a SET of attachments, not a blob.
//
// A real repro tree looks like repro-01-*.sh, REPORT.md, RESULT.md,
// DISCOVERY.md, snippet.sql and sometimes evidence/*.log, all together. Each
// of those is its own file with its own bytes and its own reason to exist -
// the script that reproduces it and the log a run of that script left behind
// are not the same kind of thing and must not be flattened into one payload
// that a later addition (a second evidence log from a rerun, say) would have
// no way to join. So each file becomes its own attachment through the
// existing WriteAttachment (attachments.go:46) - one id, one digest, written
// once - and the finding records the SET as a manifest in its own Fields:
// which attachment is which path, and how to run the tree once the files are
// back on disk. No new column: see the head of lifecycle.go on why a finding
// gets none.
//
// The manifest is latest-call-wins, and that is deliberate and different from
// findingruns.go next door. A repro tree is a fact about what the finding
// currently ships - like a report's body - and an update stating a new one
// says what stands now. A RUN of that tree is a fact about one attempt at one
// version of it, and folding that into a field would be exactly the mistake
// the head of findingruns.go explains: it would lose the sequence that makes
// a rerun worth doing. This file only ever touches the "what is the repro"
// question; the "did the last run confirm it" question lives there.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// findingType is the artifact type this file and findingruns.go both narrow
// an id to. A repro tree and a run log are each about ONE particular finding,
// and an id that names something else - a bug, a report, another finding's
// attachment - is not one, the same one-namespace answer readWorkItem gives
// for a todo.
const findingType = "finding"

// The keys a finding's repro manifest rides in Fields, alongside whatever
// else that finding already carries.
const (
	// ReproFilesField holds the file list: [{path, attachment_id}, ...], in
	// the order WriteFindingRepro wrote them.
	ReproFilesField = "repro_files"
	// ReproEntrypointField names the one file in the tree a runner executes -
	// "repro-01-crash.sh" - out of a tree that may hold several.
	ReproEntrypointField = "repro_entrypoint"
	// ReproInterpField is what runs the entrypoint - "bash", "python3" - or
	// empty when the entrypoint is executed directly.
	ReproInterpField = "repro_interp"
	// IsolationField is what the tree wants to run inside - "dind",
	// "plain", or empty for whatever a runner does by default. See
	// CheckIsolation for what those two words mean and why they are the
	// only ones.
	IsolationField = "isolation"
	// CmdOverrideField is the rare tree whose command line is not
	// "interp entrypoint": a full command a runner should use instead of
	// building one from the two fields above.
	CmdOverrideField = "cmd_override"
)

// ReproFile is one file of a finding's repro tree as the manifest names it:
// the path it had inside the tree, and the attachment its bytes live in. It
// is [{path, attachment_id}] on the wire, which is what ReproFilesField holds
// an array of.
type ReproFile struct {
	Path         string `json:"path"`
	AttachmentID string `json:"attachment_id"`
}

// ReproManifest is how to run a finding's repro tree, once the files named in
// it are back on disk. Every field is optional: a tree that is just a script
// named by its entrypoint runs with the other three empty.
type ReproManifest struct {
	Entrypoint  string `json:"repro_entrypoint,omitempty"`
	Interp      string `json:"repro_interp,omitempty"`
	Isolation   string `json:"isolation,omitempty"`
	CmdOverride string `json:"cmd_override,omitempty"`
}

// The isolation vocabulary: the whole set of words a manifest's isolation
// may be, and the one place that set is written down.
//
// IsolationDind is a repro that launches its OWN containers and therefore
// needs a Docker daemon of its own: the packager wraps it in a privileged
// docker:dind service, and the script's `docker run` talks to that inner
// daemon instead of a host socket. IsolationPlain is a repro that is just a
// command run directly in an image - a unit test, a script - with no daemon
// and no binary. Empty means neither is stated and a runner uses its own
// per-project default, which is "plain" unless the operator configured
// otherwise (internal/repro/version.go's ProjectConfig.DefaultIsolation).
const (
	IsolationDind  = "dind"
	IsolationPlain = "plain"
)

// CheckIsolation refuses an isolation nothing builds, and it is deliberately
// refused HERE, at the write, rather than at the run.
//
// This field used to be documented as "vm" or "container", which no code on
// either side of it has ever implemented. hands-off's packager.py - the
// service this one was ported from - only ever knew "dind" and "plain", and
// internal/repro/packager.go asks `iso == "dind"` and renders everything
// else as plain. So a finding whose repro spins its own containers, recorded
// in the vocabulary this file documented, would have been run with no daemon
// at all: it would fail for a reason that has nothing to do with the code
// under test, and that failure would then be recorded as a verdict - the
// exact confusion the harness-error split exists to keep out of the record.
//
// Two ways to close that gap: teach the packager two more isolation modes,
// or narrow the vocabulary to what is actually built. The words were narrowed,
// because there is no VM machinery anywhere in this tree to name and a
// vocabulary that promises what nothing implements is the defect either way.
// "container" in particular was never a third thing: a repro tree ALWAYS
// runs in a container here, and the only real question is whether it gets a
// daemon of its own - which is what "dind" answers.
//
// A word outside the set is refused when it is WRITTEN, so a finding cannot
// carry an isolation no runner could honour and be discovered to be
// unrunnable weeks later by whoever tries to reproduce it. The runner keeps
// its own check at the door for rows written before this rule (see
// cmd/handoff-runner/render.go), and the packager keeps one at the point of
// rendering, so nothing downgrades silently at any of the three.
func CheckIsolation(iso string) error {
	switch iso {
	case "", IsolationDind, IsolationPlain:
		return nil
	}
	return fmt.Errorf("isolation %q is not one a runner can build: a repro tree is either "+
		"%q (it launches its own containers, so it needs a Docker daemon of its own) or "+
		"%q (a command run directly in the image), or empty for the runner's default",
		iso, IsolationDind, IsolationPlain)
}

// ReproSource is one file going INTO WriteFindingRepro: the path it will have
// in the tree, and its bytes. Path is used exactly once - see
// WriteFindingRepro on why a repeated path is refused rather than merged.
type ReproSource struct {
	Path    string
	Content []byte
}

// ReproFileBytes is one file of a finding's repro tree coming back OUT of
// ReadFindingRepro: its path in the tree, and the bytes ReadAttachment
// returned for it.
type ReproFileBytes struct {
	Path    string
	Content []byte
}

// NotAFindingError is what an id that does not name a finding this principal
// may read gets back - one that is not here, one that is out of reach, and
// one that is here and is a bug or a report are all the same answer, which is
// the answer a plain read of it would give. See NotATodoError in deps.go,
// whose reasoning this repeats for the other id namespace a caller might
// confuse this one with.
type NotAFindingError struct{ ID string }

func (e NotAFindingError) Error() string { return "no such finding: " + e.ID }
func (e NotAFindingError) Unwrap() error { return ErrNotFound }

// readFinding reads one finding this principal may read, or the answer a read
// of an id that is not here would give for anything else - readWorkItem's
// rule in deps.go, narrowed to the one type this file and findingruns.go
// share.
func (d *DB) readFinding(ctx context.Context, p *Principal, id string) (*Artifact, error) {
	art, err := d.ReadArtifact(ctx, p, id, false)
	if errors.Is(err, ErrNotFound) {
		return nil, NotAFindingError{ID: id}
	}
	if err != nil {
		return nil, err
	}
	if art.Type != findingType {
		return nil, NotAFindingError{ID: id}
	}
	return art, nil
}

// WriteFindingRepro attaches every source file to the project - one
// WriteAttachment per file, titled by its path in the tree - and records the
// result as the finding's repro manifest: the file list and how to run it,
// replacing whatever manifest was there before. It is the whole tree stated
// fresh, the same way a report's body is: what came before this call is
// exactly what a caller that never called it again would have kept.
//
// Read permission is the write permission here, as it is for a status move in
// lifecycle.go and an edge in deps.go: a finding is collaborative exactly the
// way a bug is, and a participant who could not attach their own reproduction
// of it would have to ask somebody else to.
//
// A path used twice is refused before anything is written: a manifest cannot
// say which of two attachments "evidence/errors.log" now means, and a caller
// that meant to add a second log needs a second name for it, not a coin flip
// decided by map iteration order.
//
// This is NOT one transaction end to end - it cannot be, because each
// WriteAttachment is its own (see attachments.go:46 on why an attachment's
// create and its bytes are one write and nothing wider). A node that stops
// partway through leaves some files attached and the finding's manifest not
// yet naming them: orphaned attachments, not corruption, and the caller sees
// the error and may call this again. What the manifest ends up naming is
// exactly what got attached AND survived to the one SetArtifactFields call at
// the end - never a partial list silently swapped in for a full one.
func (d *DB) WriteFindingRepro(
	ctx context.Context, p *Principal, findingID string, sources []ReproSource, manifest ReproManifest,
) ([]ReproFile, error) {
	finding, err := d.readFinding(ctx, p, findingID)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("finding %s: a repro tree needs at least one file", findingID)
	}
	// Before any attachment is written, not after: an isolation no runner
	// can build makes the whole tree unrunnable, and a call that refused
	// halfway would leave the files attached and orphaned for nothing.
	if err := CheckIsolation(manifest.Isolation); err != nil {
		return nil, fmt.Errorf("finding %s: %w", findingID, err)
	}
	if p.UserID == "" {
		return nil, fmt.Errorf("this token resolves to no user, so it cannot own a repro attachment")
	}
	actor, _ := voteActor(p)

	seen := map[string]bool{}
	files := make([]ReproFile, 0, len(sources))
	for _, src := range sources {
		path := strings.TrimSpace(src.Path)
		if path == "" {
			return nil, fmt.Errorf("finding %s: a repro file needs a path", findingID)
		}
		if seen[path] {
			return nil, fmt.Errorf("finding %s: repro path %q is named twice; "+
				"a manifest entry names one attachment, so a second file at the same "+
				"path needs a path of its own", findingID, path)
		}
		seen[path] = true

		art := &Artifact{
			Type:       reproAttachmentType,
			Kind:       reproKind(src.Content),
			Title:      path,
			OwnerUser:  p.UserID,
			Project:    finding.Project,
			Visibility: finding.Visibility,
		}
		if err := d.WriteAttachment(ctx, art, src.Content, &Event{
			Type: reproAttachmentEventType, Room: "attachments", Actor: actor, Body: path,
		}); err != nil {
			return nil, fmt.Errorf("store: write finding %s repro file %s: %w", findingID, path, err)
		}
		files = append(files, ReproFile{Path: path, AttachmentID: art.ID})
	}

	merged := map[string]any{}
	if len(finding.Fields) > 0 {
		if err := json.Unmarshal(finding.Fields, &merged); err != nil {
			return nil, fmt.Errorf("finding %s carries fields that do not parse: %w", findingID, err)
		}
	}
	merged[ReproFilesField] = files
	merged[ReproEntrypointField] = manifest.Entrypoint
	merged[ReproInterpField] = manifest.Interp
	merged[IsolationField] = manifest.Isolation
	merged[CmdOverrideField] = manifest.CmdOverride

	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("store: marshal finding %s repro manifest: %w", findingID, err)
	}
	if err := d.SetArtifactFields(ctx, finding, raw); err != nil {
		return nil, fmt.Errorf("store: record finding %s repro manifest: %w", findingID, err)
	}
	return files, nil
}

// FindingRepro reads the manifest and file list a finding's repro tree
// carries off its own Fields, touching nothing but the row already in hand.
// It is the pure half of ReadFindingRepro, for a caller that already has the
// finding (a list page, say) and wants to know whether it has a repro tree
// and how to run it, without a second read and before fetching a single byte.
func FindingRepro(finding *Artifact) (ReproManifest, []ReproFile, error) {
	if len(finding.Fields) == 0 {
		return ReproManifest{}, nil, fmt.Errorf("finding %s has no repro tree recorded", finding.ID)
	}
	var raw struct {
		Files       []ReproFile `json:"repro_files"`
		Entrypoint  string      `json:"repro_entrypoint"`
		Interp      string      `json:"repro_interp"`
		Isolation   string      `json:"isolation"`
		CmdOverride string      `json:"cmd_override"`
	}
	if err := json.Unmarshal(finding.Fields, &raw); err != nil {
		return ReproManifest{}, nil, fmt.Errorf("finding %s carries fields that do not parse: %w",
			finding.ID, err)
	}
	if len(raw.Files) == 0 {
		return ReproManifest{}, nil, fmt.Errorf("finding %s has no repro tree recorded", finding.ID)
	}
	manifest := ReproManifest{
		Entrypoint: raw.Entrypoint, Interp: raw.Interp,
		Isolation: raw.Isolation, CmdOverride: raw.CmdOverride,
	}
	return manifest, raw.Files, nil
}

// ReadFindingRepro reconstitutes a finding's repro tree: every file
// repro_files names, in the order it names them, with its bytes - what a
// packager stages onto disk to run the tree again. It is the read half of
// WriteFindingRepro and never a second idea of the manifest: the list it
// walks is the one WriteFindingRepro wrote, read back off the finding's own
// row through FindingRepro.
//
// A file whose bytes are not on this node - replicated in but never pulled,
// see ErrNoBytes on ReadAttachment - stops the whole reconstruction rather
// than handing back a partial tree with one script silently missing: half a
// repro tree someone runs without knowing it is half is a worse failure than
// this refusal, exactly the reasoning attachment_write's own size ceiling
// rests on.
func (d *DB) ReadFindingRepro(
	ctx context.Context, p *Principal, findingID string,
) (ReproManifest, []ReproFileBytes, error) {
	finding, err := d.readFinding(ctx, p, findingID)
	if err != nil {
		return ReproManifest{}, nil, err
	}
	manifest, files, err := FindingRepro(finding)
	if err != nil {
		return ReproManifest{}, nil, err
	}

	out := make([]ReproFileBytes, 0, len(files))
	for _, f := range files {
		_, content, err := d.ReadAttachment(ctx, p, f.AttachmentID)
		if err != nil {
			return ReproManifest{}, nil, fmt.Errorf(
				"finding %s repro file %s (attachment %s): %w", findingID, f.Path, f.AttachmentID, err)
		}
		out = append(out, ReproFileBytes{Path: f.Path, Content: content})
	}
	return manifest, out, nil
}

// reproAttachmentType and reproAttachmentEventType are the same literal
// values mcp_attachments.go's attachmentType ("attachment") and its
// attachment.write event carry. They are re-declared here rather than
// imported because the store cannot import the server package - the same
// reason chatActor is re-derived as voteActor in proposals.go - and they must
// stay these exact strings: a repro file is meant to be an ordinary
// attachment in every other way, findable by attachment_list and readable by
// attachment_read like any other.
const (
	reproAttachmentType      = "attachment"
	reproAttachmentEventType = "attachment.write"
)

// reproKind decides text or binary from the bytes, never from a claim -
// sniffType's rule in mcp_attachments.go, re-derived here for the reason
// reproAttachmentType is.
func reproKind(content []byte) string {
	if strings.HasPrefix(http.DetectContentType(content), "text/") {
		return "text"
	}
	return "binary"
}
